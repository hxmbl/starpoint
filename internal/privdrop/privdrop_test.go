package privdrop

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/elevation"
	"github.com/hxmbl/starpoint/internal/exec"
)

func envMap(env []string) map[string]string {
	out := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := out[k]; dup {
			panic("duplicate variable " + k)
		}
		out[k] = v
	}
	return out
}

func alice(t *testing.T) elevation.Target {
	t.Helper()
	return elevation.Target{User: "alice", UID: 4242, GID: 4243, Home: t.TempDir(), Groups: []int{4243, 27}}
}

func TestEnvironmentIsTheUsersNotRoots(t *testing.T) {
	a := alice(t)
	got := envMap(Environment(a, "/root", []string{
		"HOME=/root", "USER=root", "LOGNAME=root", "SHELL=/bin/bash", "MAIL=/var/mail/root",
		"SUDO_USER=alice", "SUDO_UID=4242", "SUDO_GID=4243", "SUDO_COMMAND=/usr/bin/starpoint install ruff",
		"XDG_RUNTIME_DIR=/run/user/0", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/0/bus",
		"PWD=/root", "TERM=xterm-256color", "LANG=en_GB.UTF-8", "LC_ALL=en_GB.UTF-8",
		"https_proxy=http://proxy:3128", "SSL_CERT_FILE=/etc/ssl/certs/ca.pem",
		"RANDOM_ROOT_THING=1", "LS_COLORS=di=1",
	}))
	if got["HOME"] != a.Home || got["USER"] != "alice" || got["LOGNAME"] != "alice" {
		t.Errorf("identity variables are alice's: %v", got)
	}
	for _, gone := range []string{"SUDO_USER", "SUDO_UID", "SUDO_GID", "SUDO_COMMAND", "MAIL", "PWD",
		"DBUS_SESSION_BUS_ADDRESS", "RANDOM_ROOT_THING", "LS_COLORS"} {
		if _, ok := got[gone]; ok {
			t.Errorf("%s must not be inherited: %v", gone, got)
		}
	}
	if v := got["XDG_RUNTIME_DIR"]; v == "/run/user/0" {
		t.Error("root's runtime directory is never the user's")
	}
	if v := got["SHELL"]; v == "/bin/bash" && loginShell("alice") == "" {
		t.Error("root's SHELL is not inherited")
	}
	for k, want := range map[string]string{"TERM": "xterm-256color", "LANG": "en_GB.UTF-8", "LC_ALL": "en_GB.UTF-8",
		"https_proxy": "http://proxy:3128", "SSL_CERT_FILE": "/etc/ssl/certs/ca.pem"} {
		if got[k] != want {
			t.Errorf("%s should be kept as %q, got %q", k, want, got[k])
		}
	}
}

func TestLocationVariablesSurviveOnlyInsideTheUsersHome(t *testing.T) {
	a := alice(t)
	got := envMap(Environment(a, "/root", []string{
		"CARGO_HOME=" + filepath.Join(a.Home, ".cargo-custom"),
		"RUSTUP_HOME=/root/.rustup",
		"GOPATH=/opt/go",
		"GOBIN=" + filepath.Join(a.Home, "gobin"),
		"UV_TOOL_DIR=/root/.local/share/uv/tools",
		"UV_CACHE_DIR=" + filepath.Join(a.Home, ".cache", "uv"),
		"XDG_DATA_HOME=/root/.local/share",
		"XDG_CONFIG_HOME=" + filepath.Join(a.Home, ".config"),
		"PYTHONUSERBASE=/usr/local",
		"UV_INDEX_URL=https://pypi.example/simple",
		"GOPROXY=https://proxy.golang.org",
		"GOFLAGS=-mod=mod",
		"npm_config_registry=https://registry.example",
		"PIP_INDEX_URL=https://pypi.example/simple",
		"PYTHONPATH=/root/lib/python",
		"GOOGLE_APPLICATION_CREDENTIALS=/etc/gcloud.json",
	}))
	for k, want := range map[string]string{
		"CARGO_HOME":          filepath.Join(a.Home, ".cargo-custom"),
		"GOBIN":               filepath.Join(a.Home, "gobin"),
		"UV_CACHE_DIR":        filepath.Join(a.Home, ".cache", "uv"),
		"XDG_CONFIG_HOME":     filepath.Join(a.Home, ".config"),
		"UV_INDEX_URL":        "https://pypi.example/simple",
		"GOPROXY":             "https://proxy.golang.org",
		"GOFLAGS":             "-mod=mod",
		"npm_config_registry": "https://registry.example",
		"PIP_INDEX_URL":       "https://pypi.example/simple",
	} {
		if got[k] != want {
			t.Errorf("%s: want %q, got %q", k, want, got[k])
		}
	}
	for _, gone := range []string{"RUSTUP_HOME", "GOPATH", "UV_TOOL_DIR", "XDG_DATA_HOME", "PYTHONUSERBASE", "PYTHONPATH",
		"GOOGLE_APPLICATION_CREDENTIALS"} {
		if v, ok := got[gone]; ok {
			t.Errorf("%s=%s must not reach the user's provider", gone, v)
		}
	}
}

func TestPathLeadsWithTheUsersToolDirsAndDropsRootsHome(t *testing.T) {
	a := alice(t)
	for _, dir := range []string{".cargo/bin", "go/bin", ".local/bin"} {
		if err := os.MkdirAll(filepath.Join(a.Home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	got := envMap(Environment(a, "/root", []string{
		"PATH=/root/.cargo/bin:/usr/local/sbin:/usr/local/bin:relative/bin:/usr/sbin:/usr/bin:/sbin:/bin:/usr/bin",
	}))
	want := []string{
		filepath.Join(a.Home, ".cargo", "bin"),
		filepath.Join(a.Home, "go", "bin"),
		filepath.Join(a.Home, ".local", "bin"),
		"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin",
	}
	if path := strings.Split(got["PATH"], string(os.PathListSeparator)); !reflect.DeepEqual(path, want) {
		t.Errorf("PATH:\n got %v\nwant %v", path, want)
	}
}

func TestPathFollowsTheUsersOwnLocations(t *testing.T) {
	a := alice(t)
	cargo := filepath.Join(a.Home, "rust", "cargo")
	if err := os.MkdirAll(filepath.Join(cargo, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(a.Home, ".cargo", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := envMap(Environment(a, "/root", []string{"CARGO_HOME=" + cargo, "PATH=/usr/bin"}))
	path := strings.Split(got["PATH"], string(os.PathListSeparator))
	if path[0] != filepath.Join(cargo, "bin") {
		t.Errorf("the user's CARGO_HOME decides Cargo's bin directory: %v", path)
	}
	for _, dir := range path {
		if dir == filepath.Join(a.Home, ".cargo", "bin") {
			t.Errorf("the default ~/.cargo/bin is not added when CARGO_HOME moves it: %v", path)
		}
	}
}

func TestEmptyPathFallsBackToTheSystemDirectories(t *testing.T) {
	got := envMap(Environment(alice(t), "/root", nil))
	if got["PATH"] != "/usr/local/bin:/usr/bin:/bin" {
		t.Errorf("PATH = %q", got["PATH"])
	}
}

func TestEnvironmentIsSortedAndUnique(t *testing.T) {
	env := Environment(alice(t), "/root", []string{"TERM=a", "TERM=b", "LANG=C"})
	for i := 1; i < len(env); i++ {
		if env[i-1] >= env[i] {
			t.Fatalf("not sorted and unique: %v", env)
		}
	}
	if envMap(env)["TERM"] != "b" {
		t.Error("the last value of a repeated variable wins, as in the process environment")
	}
}

func TestIdentityCarriesIdsGroupsAndEnvironment(t *testing.T) {
	a := alice(t)
	id := Identity(a, "/root", []string{"TERM=dumb"})
	if id.User != "alice" || id.UID != 4242 || id.GID != 4243 || id.Home != a.Home || !reflect.DeepEqual(id.Groups, []int{4243, 27}) {
		t.Errorf("identity: %+v", id)
	}
	if id.Getenv("HOME") != a.Home || id.Getenv("TERM") != "dumb" {
		t.Errorf("environment: %v", id.Env)
	}
}

func TestRootHomeOverride(t *testing.T) {
	got := envMap(Environment(alice(t), "/var/lib/rootish", []string{"GOFLAGS=-modcacherw", "PIP_CONFIG_FILE=/var/lib/rootish/pip.conf"}))
	if _, ok := got["PIP_CONFIG_FILE"]; ok {
		t.Error("a value in the elevated account's own home is dropped wherever that home is")
	}
	if got["GOFLAGS"] != "-modcacherw" {
		t.Error("unrelated settings stay")
	}
}

func TestProbeOfNoIdentityIsANoOp(t *testing.T) {
	if err := Probe(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestProbeFailsWithoutTheRightToDrop(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can drop privileges; see the root tests")
	}
	if !exec.DropSupported {
		t.Skip("no privilege dropping on this platform")
	}
	home := t.TempDir()
	id := Identity(elevation.Target{User: "nobody", UID: 65534, GID: 65534, Home: home}, "/root", []string{"PATH=/usr/bin:/bin"})
	err := Probe(context.Background(), id)
	if err == nil {
		t.Fatal("an unprivileged process cannot start one as another user; the probe must say so")
	}
	if !strings.Contains(err.Error(), "could not start a process as nobody") {
		t.Errorf("the reason names the user: %v", err)
	}
}
