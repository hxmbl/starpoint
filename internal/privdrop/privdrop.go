// Package privdrop builds the identity provider commands run as when an
// elevated Starpoint drops privileges for them.
//
// Under `sudo starpoint install ruff` Starpoint runs as root on behalf of the
// user who typed sudo. Package managers that install into that user's home
// (uv, pip --user, Cargo, Go, rustup, and npm, pnpm, yarn, gem or Homebrew
// when their prefix belongs to the user) have no use for root, and running
// them as root is what leaves root-owned files behind in the user's home.
// For those providers Starpoint starts the command as the user: their uid,
// primary gid and supplementary groups, and an environment built for them
// rather than inherited from root.
//
// The environment is built, not filtered from root's. See Environment for the
// rules; the short form is that a command run as the user sees the user's
// HOME, USER, LOGNAME, a PATH that leads with the user's own tool
// directories, the user's own XDG_RUNTIME_DIR, and only those location
// variables (CARGO_HOME, GOPATH, UV_TOOL_DIR, ...) that point into the user's
// home. Nothing that points into root's home survives.
package privdrop

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hxmbl/starpoint/internal/elevation"
	"github.com/hxmbl/starpoint/internal/exec"
)

// Identity is the identity provider commands run as for target, with an
// environment built from environ (normally os.Environ() of the elevated
// process) by Environment. rootHome is the elevated account's own home.
func Identity(t elevation.Target, rootHome string, environ []string) *exec.Identity {
	return &exec.Identity{
		User:   t.User,
		UID:    t.UID,
		GID:    t.GID,
		Groups: append([]int(nil), t.Groups...),
		Home:   t.Home,
		Env:    Environment(t, rootHome, environ),
	}
}

// never are variables a command run as the user never inherits: they name
// the elevated account or the elevation, or they are rebuilt below.
var never = map[string]bool{
	"HOME": true, "USER": true, "LOGNAME": true, "USERNAME": true, "MAIL": true,
	"SHELL": true, "PATH": true, "PWD": true, "OLDPWD": true,
	"SUDO_USER": true, "SUDO_UID": true, "SUDO_GID": true, "SUDO_COMMAND": true, "SUDO_HOME": true,
	"SUDO_PS1": true, "DOAS_USER": true, "XDG_RUNTIME_DIR": true,
	// Root's session bus and agent sockets are root's.
	"DBUS_SESSION_BUS_ADDRESS": true, "SSH_AUTH_SOCK": true, "GPG_AGENT_INFO": true,
}

// locations are variables that place a provider's files. A command run as
// the user keeps one only when every path in it lies inside the user's home:
// that is a value the user set (and sudo's env_keep or `sudo -E` carried
// through). Anywhere else it may be root's (`sudo -i` sources root's
// profile) and would send the user's install into a directory they cannot
// write, so the provider's own default, which is under the user's home, is
// used instead.
var locations = map[string]bool{
	"CARGO_HOME": true, "CARGO_INSTALL_ROOT": true, "CARGO_TARGET_DIR": true, "RUSTUP_HOME": true,
	"GOPATH": true, "GOBIN": true, "GOMODCACHE": true, "GOCACHE": true, "GOENV": true, "GOTMPDIR": true,
	"UV_TOOL_DIR": true, "UV_TOOL_BIN_DIR": true, "UV_CACHE_DIR": true, "UV_PYTHON_INSTALL_DIR": true,
	"UV_PYTHON_BIN_DIR": true, "UV_INSTALL_DIR": true, "UV_CONFIG_FILE": true,
	"PYTHONUSERBASE": true, "PIP_CACHE_DIR": true, "PIP_CONFIG_FILE": true, "PIPX_HOME": true, "PIPX_BIN_DIR": true,
	"NPM_CONFIG_PREFIX": true, "NPM_CONFIG_CACHE": true, "NPM_CONFIG_USERCONFIG": true,
	"PNPM_HOME": true, "YARN_GLOBAL_FOLDER": true, "YARN_CACHE_FOLDER": true, "PREFIX": true,
	"GEM_HOME": true, "GEM_PATH": true, "GEM_SPEC_CACHE": true, "BUNDLE_PATH": true, "BUNDLE_USER_HOME": true,
	"XDG_CONFIG_HOME": true, "XDG_DATA_HOME": true, "XDG_CACHE_HOME": true, "XDG_STATE_HOME": true, "XDG_BIN_HOME": true,
	"TMPDIR": true,
}

// keepExact and keepPrefixes are the settings a command run as the user
// inherits: terminal and locale, proxies and certificates, and each
// provider's own configuration family. Everything else of the elevated
// process's environment is dropped.
var keepExact = map[string]bool{
	"TERM": true, "COLORTERM": true, "NO_COLOR": true, "CLICOLOR": true, "FORCE_COLOR": true,
	"LANG": true, "LANGUAGE": true, "TZ": true, "CI": true,
	"http_proxy": true, "https_proxy": true, "no_proxy": true, "all_proxy": true, "ftp_proxy": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true, "FTP_PROXY": true,
	"SSL_CERT_FILE": true, "SSL_CERT_DIR": true, "REQUESTS_CA_BUNDLE": true, "CURL_CA_BUNDLE": true,
	"NODE_EXTRA_CA_CERTS": true, "XDG_CONFIG_DIRS": true, "XDG_DATA_DIRS": true,
}

var keepPrefixes = []string{
	"LC_", "UV_", "PIP_", "PYTHON", "CARGO_", "RUSTUP_", "RUSTFLAGS", "CGO_",
	"GOPROXY", "GOFLAGS", "GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GONOSUMCHECK", "GOSUMDB", "GOINSECURE",
	"GOTOOLCHAIN", "GOEXPERIMENT", "GODEBUG", "GOAMD64", "GOARM", "GOVCS",
	"NPM_CONFIG_", "npm_config_", "PNPM_", "YARN_", "COREPACK_", "NODE_OPTIONS",
	"GEM_", "BUNDLE_", "HOMEBREW_",
}

func kept(key string) bool {
	if keepExact[key] || locations[key] {
		return true
	}
	for _, p := range keepPrefixes {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

// Environment builds the environment for a command run as t.
//
//   - HOME, USER and LOGNAME are the user's; SHELL is the user's login shell
//     when the user database says, otherwise unset.
//   - PATH leads with the user's tool directories that exist (Cargo's and
//     Go's bin directories, ~/.local/bin, PNPM_HOME, ~/bin), followed by the
//     elevated process's PATH with relative entries and anything inside
//     root's home removed. That PATH is sudo's secure_path or doas's, so the
//     system directories stay.
//   - XDG_RUNTIME_DIR is /run/user/<uid> when that directory exists and is
//     the user's (Linux), otherwise unset; root's is never inherited.
//   - Location variables (CARGO_HOME, RUSTUP_HOME, GOPATH, GOBIN, UV_*
//     directories, PYTHONUSERBASE, npm's prefix, XDG_*_HOME, ...) are kept
//     only when they point into the user's home.
//   - Terminal, locale, proxy and certificate settings and each provider's
//     own configuration family (UV_*, PIP_*, CARGO_*, GO*, npm_config_*, ...)
//     are kept, unless their value points into root's home.
//   - Nothing else is inherited.
//
// The result is sorted, so it is stable for tests and logs.
func Environment(t elevation.Target, rootHome string, environ []string) []string {
	vars := map[string]string{}
	for _, kv := range environ {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || key == "" || never[key] || !kept(key) {
			continue
		}
		if mentionsRootHome(value, rootHome) {
			continue
		}
		if locations[key] && !allInside(value, t.Home) {
			continue
		}
		vars[key] = value
	}
	vars["HOME"] = t.Home
	vars["USER"] = t.User
	vars["LOGNAME"] = t.User
	if shell := loginShell(t.User); shell != "" {
		vars["SHELL"] = shell
	}
	if runtime.GOOS == "linux" {
		dir := filepath.Join("/run/user", strconv.Itoa(t.UID))
		if ownedDir(dir, t.UID) {
			vars["XDG_RUNTIME_DIR"] = dir
		}
	}
	vars["PATH"] = userPath(t, rootHome, vars, lookup(environ, "PATH"))

	out := make([]string, 0, len(vars))
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// userPath builds PATH for the user; see Environment.
func userPath(t elevation.Target, rootHome string, vars map[string]string, inherited string) string {
	home := t.Home
	first := func(vals ...string) string {
		for _, v := range vals {
			if v != "" {
				return v
			}
		}
		return ""
	}
	gopath := ""
	if list := filepath.SplitList(vars["GOPATH"]); len(list) > 0 {
		gopath = list[0]
	}
	candidates := []string{
		filepath.Join(first(vars["CARGO_HOME"], filepath.Join(home, ".cargo")), "bin"),
		first(vars["GOBIN"], filepath.Join(first(gopath, filepath.Join(home, "go")), "bin")),
		first(vars["UV_TOOL_BIN_DIR"], vars["XDG_BIN_HOME"], filepath.Join(home, ".local", "bin")),
		vars["PNPM_HOME"],
		filepath.Join(home, "bin"),
	}
	if prefix := first(vars["NPM_CONFIG_PREFIX"], vars["npm_config_prefix"]); prefix != "" {
		candidates = append(candidates, filepath.Join(prefix, "bin"))
	}
	var dirs []string
	seen := map[string]bool{}
	add := func(dir string) {
		if dir == "" || !filepath.IsAbs(dir) {
			return
		}
		dir = filepath.Clean(dir)
		if seen[dir] || mentionsRootHome(dir, rootHome) {
			return
		}
		seen[dir] = true
		dirs = append(dirs, dir)
	}
	for _, dir := range candidates {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			add(dir)
		}
	}
	for _, dir := range filepath.SplitList(inherited) {
		add(dir)
	}
	if len(dirs) == 0 {
		for _, dir := range []string{"/usr/local/bin", "/usr/bin", "/bin"} {
			add(dir)
		}
	}
	return strings.Join(dirs, string(os.PathListSeparator))
}

func lookup(environ []string, key string) string {
	for i := len(environ) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(environ[i], "="); ok && k == key {
			return v
		}
	}
	return ""
}

// rootHomes are the elevated account's home directories: the one the process
// resolved and the conventional ones, so a value from `sudo -i` is caught
// whichever was in effect.
func rootHomes(rootHome string) []string {
	homes := []string{"/root", "/var/root"}
	if rootHome != "" && rootHome != "/" {
		homes = append(homes, filepath.Clean(rootHome))
	}
	return homes
}

// mentionsRootHome reports whether any path in a value (a single path or a
// PATH-style list) lies in root's home.
func mentionsRootHome(value, rootHome string) bool {
	for _, part := range filepath.SplitList(value) {
		part = strings.TrimSpace(part)
		if !filepath.IsAbs(part) {
			continue
		}
		for _, h := range rootHomes(rootHome) {
			if within(filepath.Clean(part), h) {
				return true
			}
		}
	}
	return false
}

// allInside reports whether every path in a value lies in home.
func allInside(value, home string) bool {
	parts := filepath.SplitList(value)
	if len(parts) == 0 || home == "" {
		return false
	}
	for _, part := range parts {
		if !filepath.IsAbs(part) || !within(filepath.Clean(part), filepath.Clean(home)) {
			return false
		}
	}
	return true
}

func within(path, dir string) bool {
	if path == dir {
		return true
	}
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// loginShell reads the user's shell from /etc/passwd. The user database has
// no portable API for it; when it cannot be found SHELL is left unset rather
// than guessed (providers do not depend on it).
func loginShell(name string) string {
	data, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) >= 7 && fields[0] == name && filepath.IsAbs(fields[6]) {
			return fields[6]
		}
	}
	return ""
}

// Probe checks, before anything changes, that a command can really be
// started as id: it runs `id -u` as id and compares the answer, and checks
// that the user can write to their home. A run that cannot drop privileges is
// refused rather than carried out as root.
func Probe(ctx context.Context, id *exec.Identity) error {
	if id == nil {
		return nil
	}
	if !exec.DropSupported {
		return exec.ErrDropUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r := exec.RealRunner{As: id}
	res, err := r.Run(ctx, "/bin/sh", "-c", `id -u && test -d "$HOME" && test -w "$HOME"`)
	if err != nil {
		return fmt.Errorf("could not start a process as %s (uid %d): %w", id.User, id.UID, unwrapDrop(err))
	}
	got := strings.TrimSpace(strings.SplitN(res.Stdout, "\n", 2)[0])
	if got != strconv.Itoa(id.UID) {
		if got == "" {
			return fmt.Errorf("a process started as %s did not report its user id: %s", id.User, strings.TrimSpace(res.Field()))
		}
		return fmt.Errorf("a process started as %s ran as uid %s instead of %d", id.User, got, id.UID)
	}
	if res.Failed() {
		return fmt.Errorf("%s cannot write to their home directory %s", id.User, id.Home)
	}
	return nil
}

func unwrapDrop(err error) error {
	for {
		de, ok := err.(*exec.DropError)
		if !ok {
			return err
		}
		err = de.Err
	}
}
