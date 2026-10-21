// Package locator answers "is this software on the machine, and where does it
// come from".
//
// Locating is separate from installing on purpose. Every provider that can
// install something can usually only report what it installed itself, which is
// not enough to answer the question Starpoint actually needs: when Python is on
// PATH, which of the four plausible owners put it there? The locator inspects
// real filesystem paths and attributes each one, so an installation Starpoint
// did not make is still visible to it.
package locator

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/brew"
	"github.com/hxmbl/starpoint/internal/providers/cargo"
	"github.com/hxmbl/starpoint/internal/providers/flatpak"
	"github.com/hxmbl/starpoint/internal/providers/golang"
	"github.com/hxmbl/starpoint/internal/providers/nix"
	"github.com/hxmbl/starpoint/internal/providers/npm"
	"github.com/hxmbl/starpoint/internal/providers/snap"
	"github.com/hxmbl/starpoint/internal/providers/system"
	"github.com/hxmbl/starpoint/internal/providers/uv"
)

// Locator inspects the host filesystem.
type Locator struct {
	// LookPath is overridable so tests do not depend on the host's PATH. A nil
	// value means the host implementation.
	LookPath func(string) (string, error)
	// Stat is overridable for the same reason.
	Stat func(string) (os.FileInfo, error)
	// SearchPath overrides PATH for executable inventory. Empty means PATH from
	// the current process.
	SearchPath string
	// PackageMappings optionally provides provider package to executable maps
	// used to attribute system binaries to their package manager.
	PackageMappings []provider.ExecutableMappingProvider
}

// OnPath returns every executable visible in PATH, preserving duplicate names
// from different directories. Symlinks are attributed using both their visible
// path and resolved target; Path always remains the entry users execute.
//
// A directory is read once, however many times PATH names it and whichever
// spelling it uses: /bin and /usr/bin on a merged-/usr system, a trailing slash,
// or a symlinked directory all resolve to the same place. Reading it twice
// reported every file in it as a duplicate of itself. A later entry with the
// same name is a duplicate only when it is a different file; two links to one
// program are not a shadowing problem.
func (l *Locator) OnPath() []Found {
	path := l.SearchPath
	if path == "" {
		path = os.Getenv("PATH")
	}
	var out []Found
	firstTarget := map[string]string{}
	seenDirs := map[string]bool{}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" {
			dir = "."
		}
		key := filepath.Clean(dir)
		if real, err := filepath.EvalSymlinks(key); err == nil {
			key = real
		}
		if seenDirs[key] {
			continue
		}
		seenDirs[key] = true
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			full := filepath.Join(dir, entry.Name())
			info, err := os.Stat(full) // follow symlinks; dangling links are not executable
			if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
				continue
			}
			target, targetErr := filepath.EvalSymlinks(full)
			resolved := full
			if targetErr == nil {
				resolved = target
			}
			first, seen := firstTarget[entry.Name()]
			if !seen {
				firstTarget[entry.Name()] = resolved
			}
			duplicate := seen && first != resolved
			scope, owner, ok := l.AttributeExecutable(full)
			linkInfo, _ := os.Lstat(full)
			found := Found{Path: full, Attributed: ok, Duplicate: duplicate, Symlink: linkInfo != nil && linkInfo.Mode()&os.ModeSymlink != 0}
			if targetErr == nil && target != full {
				found.Target = target
			}
			if ok {
				found.Installation = &model.Installation{Name: entry.Name(), Path: full, Provider: owner, Scope: scope}
			} else {
				found.Reason = "ownership unknown; no known provider matches " + full
			}
			out = append(out, found)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		left, right := filepath.Base(out[i].Path), filepath.Base(out[j].Path)
		if left != right {
			return left < right
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// PATHSummary condenses a PATH inventory for people. The full inventory is a
// few thousand rows on an ordinary machine; what is worth reading is the count,
// the names that are shadowed by an earlier entry, and the files nobody owns.
type PATHSummary struct {
	Executables int `json:"executables"`
	Directories int `json:"directories"`
	Shadowed    int `json:"shadowed"`
	Unknown     int `json:"unknown_owner"`
}

// Summarize counts an inventory.
func Summarize(found []Found) PATHSummary {
	dirs := map[string]bool{}
	s := PATHSummary{Executables: len(found)}
	for _, f := range found {
		dirs[filepath.Dir(f.Path)] = true
		if f.Duplicate {
			s.Shadowed++
		}
		if !f.Attributed {
			s.Unknown++
		}
	}
	s.Directories = len(dirs)
	return s
}

// New builds a Locator backed by the real filesystem.
func New() *Locator { return &Locator{} }

// lookPath resolves an executable, defaulting to the host.
func (l *Locator) lookPath(name string) (string, error) {
	if l.LookPath != nil {
		return l.LookPath(name)
	}
	return exec.LookPath(name)
}

// stat inspects a path, defaulting to the host.
func (l *Locator) stat(path string) (os.FileInfo, error) {
	if l.Stat != nil {
		return l.Stat(path)
	}
	return os.Stat(path)
}

// Exists reports whether a file is present at path, following symlinks, so a
// dangling link to a removed tool environment does not count.
func (l *Locator) Exists(path string) bool {
	if path == "" {
		return false
	}
	_, err := l.stat(path)
	return err == nil
}

// Found is the result of locating a single name.
type Found struct {
	// Installation is what was located, or nil.
	Installation *model.Installation `json:"installation,omitempty"`
	// Path is the raw resolved path even when no owner could be attributed.
	Path string `json:"path,omitempty"`
	// Attributed is true when the path could be assigned to a known owner.
	Attributed bool `json:"attributed"`
	// Reason explains an unattributed result.
	Reason string `json:"reason,omitempty"`
	// Duplicate is true when an earlier PATH entry has the same name.
	Duplicate bool `json:"duplicate,omitempty"`
	// Symlink records whether Path is a symlink; Target is its resolved path.
	Symlink bool   `json:"symlink,omitempty"`
	Target  string `json:"target,omitempty"`
}

// Locate finds an executable by name on PATH and attributes it to an owner.
//
// Attribution is by path prefix, in order of specificity. This matters: a
// Homebrew binary in /opt/homebrew/bin and an APT binary in /usr/bin are in
// different places but both share the generic /usr prefix, so the more specific
// prefix has to win.
func (l *Locator) Locate(name string) Found {
	if name == "" {
		return Found{Reason: "no name given"}
	}
	path, err := l.lookPath(name)
	if err != nil {
		return Found{Reason: "not on PATH"}
	}
	scope, owner, ok := l.AttributeExecutable(path)
	if ok && owner == system.Name {
		if packageOwner, packageName, found := l.packageOwner(name); found {
			return Found{Path: path, Installation: &model.Installation{Name: packageName, Path: path, Provider: packageOwner, Scope: scope}, Attributed: true}
		}
	}
	if !ok {
		return Found{Path: path, Reason: "no known owner for " + path}
	}
	return Found{
		Path: path,
		Installation: &model.Installation{
			Name:     name,
			Path:     path,
			Provider: owner,
			Scope:    scope,
		},
		Attributed: true,
	}
}

// AttributeExecutable attributes a visible executable, preferring a more
// specific resolved symlink target over a generic directory owner such as the
// operating system's /usr/bin.
func (l *Locator) AttributeExecutable(path string) (model.Scope, string, bool) {
	scope, owner, ok := l.Attribute(path)
	target, err := filepath.EvalSymlinks(path)
	if err == nil && target != path {
		if targetScope, targetOwner, targetOK := l.Attribute(target); targetOK &&
			(!ok || owner == system.Name || targetOwner != system.Name) {
			return targetScope, targetOwner, true
		}
	}
	return scope, owner, ok
}

func (l *Locator) packageOwner(executable string) (string, string, bool) {
	for _, p := range l.PackageMappings {
		if p == nil {
			continue
		}
		if packageName, ok := p.ExecutableToPackage()[executable]; ok {
			return p.Name(), packageName, true
		}
	}
	return "", "", false
}

// prefix is an attribution rule.
//
// A rule with a rooted path matches that absolute location. A rule written as
// relative segments matches anywhere those segments end the path, which is what
// makes ~/.cargo/bin work regardless of where the home directory lives.
type prefix struct {
	// path is an absolute prefix when rooted, or a slash-separated tail
	// otherwise.
	path  string
	owner string
	scope model.Scope
	// goos restricts the rule to one platform.
	goos string
	// partial makes the last segment a prefix match, for directories whose name
	// carries a version: python3.12 matches a rule for python.
	partial bool
}

// attributionRules maps install locations to their owners. Order matters: the
// first match wins, so more specific rules come first.
var attributionRules = []prefix{
	{path: "/opt/homebrew", owner: brew.Name, scope: model.ScopeUser},
	{path: "/usr/local/Cellar", owner: brew.Name, scope: model.ScopeUser},
	{path: "/usr/local/Homebrew", owner: brew.Name, scope: model.ScopeUser},
	{path: "/home/linuxbrew/.linuxbrew", owner: brew.Name, scope: model.ScopeUser},
	{path: "/opt/brew", owner: brew.Name, scope: model.ScopeUser},

	// uv places tool environments and interpreters under its data directory.
	{path: ".local/share/uv/tools", owner: uv.Name, scope: model.ScopeUser},
	{path: ".local/share/uv/python", owner: uv.Name, scope: model.ScopeUser},
	{path: ".local/share/uv/archive-v0", owner: uv.Name, scope: model.ScopeUser},

	// A rustup toolchain is its own owner; ~/.cargo/bin only holds shims and
	// cargo-installed crates, so the toolchain directory must be matched first or
	// every rustc would look like it belonged to Cargo.
	{path: ".rustup/toolchains", owner: "rustup", scope: model.ScopeUser},

	// pip's user site, as macOS and Linux lay it out.
	{path: "Library/Python", owner: "pip", scope: model.ScopeUser},
	{path: ".local/lib/python", owner: "pip", scope: model.ScopeUser, partial: true},

	// pnpm keeps its global project in a versioned directory beside its bin.
	{path: "pnpm/global", owner: "pnpm", scope: model.ScopeUser},
	{path: ".local/share/pnpm", owner: "pnpm", scope: model.ScopeUser},
	{path: ".pnpm-store", owner: "pnpm", scope: model.ScopeUser},

	// yarn classic's global project.
	{path: "yarn/global", owner: "yarn", scope: model.ScopeUser},
	{path: ".config/yarn/global", owner: "yarn", scope: model.ScopeUser},

	// RubyGems' two homes.
	{path: "Ruby/Gems", owner: "gem", scope: model.ScopeGlobal},
	{path: ".gem/ruby", owner: "gem", scope: model.ScopeUser},

	{path: ".nix-profile/bin", owner: nix.Name, scope: model.ScopeUser},

	{path: ".cargo/bin", owner: cargo.Name, scope: model.ScopeUser},
	{path: "go/bin", owner: golang.Name, scope: model.ScopeUser},

	// System application installations. A flatpak app's exported command is
	// named after its application ID, and snap's links are named after the apps
	// they wrap.
	{path: "/var/lib/flatpak/exports/bin", owner: flatpak.Name, scope: model.ScopeGlobal},
	{path: "/snap/bin", owner: snap.Name, scope: model.ScopeGlobal},

	{path: ".npm-global/bin", owner: npm.Name, scope: model.ScopeUser},
	{path: ".nvm/versions/node", owner: npm.Name, scope: model.ScopeUser},
}

// Attribute maps a filesystem path to an owning provider.
func (l *Locator) Attribute(path string) (model.Scope, string, bool) {
	clean := filepath.Clean(path)
	for _, r := range attributionRules {
		if r.goos != "" && r.goos != runtime.GOOS {
			continue
		}
		if strings.HasPrefix(r.path, "/") {
			if hasPathPrefix(clean, r.path) {
				return r.scope, r.owner, true
			}
			continue
		}
		if hasPathSegments(clean, r.path, r.partial) {
			return r.scope, r.owner, true
		}
	}
	// npm's global prefix varies by install, so check the live value.
	if prefix := l.npmPrefix(); prefix != "" && hasPathPrefix(clean, prefix) {
		return model.ScopeUser, npm.Name, true
	}
	// uv exposes tool shims in a shared bin directory that other tooling also
	// uses, so a plain prefix match would over-attribute. Confirm the shim
	// actually points into a uv-managed directory.
	if bin := l.uvBinDir(); bin != "" && hasPathPrefix(clean, bin) {
		if l.pointsInto(clean, "/uv/") {
			return model.ScopeUser, uv.Name, true
		}
	}
	if system.OwnsPath(clean) {
		return model.ScopeGlobal, system.Name, true
	}
	return "", "", false
}

// hasPathPrefix reports whether path lies inside dir, comparing whole path
// components so that /usr/local/bin-extra does not match /usr/local/bin.
func hasPathPrefix(path, dir string) bool {
	dir = filepath.Clean(dir)
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

// hasPathSegments reports whether the given slash-separated segments appear as a
// contiguous run anywhere in the path. It lets a rule like ".cargo/bin" match a
// home-relative location on any machine while still comparing whole components,
// so ".car" never matches ".cargo".
func hasPathSegments(path, segments string, partial bool) bool {
	want := strings.Split(filepath.ToSlash(segments), "/")
	parts := strings.Split(filepath.ToSlash(path), "/")
	last := len(want) - 1
	for start := 0; start+len(want) <= len(parts); start++ {
		match := true
		for i, seg := range want {
			got := parts[start+i]
			if i == last && partial {
				if !strings.HasPrefix(got, seg) {
					match = false
				}
			} else if got != seg {
				match = false
			}
			if !match {
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func (l *Locator) npmPrefix() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	for _, prefix := range []string{
		filepath.Join(home, ".npm-global"),
		filepath.Join(home, ".nvm", "versions", "node"),
	} {
		if _, err := l.stat(filepath.Join(prefix, "bin")); err == nil {
			return prefix
		}
	}
	return ""
}

func (l *Locator) uvBinDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".local", "bin")
}

// pointsInto reports whether the executable at path is a symlink whose target
// contains the marker. uv's shims link into its data directory.
func (l *Locator) pointsInto(path, marker string) bool {
	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	return strings.Contains(filepath.ToSlash(target), marker)
}
