package locator

import (
	"errors"
	"os"
	"runtime"
	"testing"

	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/providers/npm"
)

// pathLocator builds a locator with a scripted PATH lookup and no filesystem
// dependency, so attribution tests are deterministic on any host.
func pathLocator(paths map[string]string) *Locator {
	return &Locator{
		LookPath: func(name string) (string, error) {
			if p, ok := paths[name]; ok {
				return p, nil
			}
			return "", errors.New("not found")
		},
		Stat: func(string) (os.FileInfo, error) { return nil, errors.New("no stat") },
	}
}

func TestLocateAttributesBrewInstalls(t *testing.T) {
	loc := pathLocator(map[string]string{"rg": "/opt/homebrew/bin/rg"})
	found := loc.Locate("rg")
	if found.Installation == nil {
		t.Fatalf("should have attributed the path: %+v", found)
	}
	if found.Installation.Provider != "brew" {
		t.Errorf("expected brew, got %q", found.Installation.Provider)
	}
	if found.Installation.Scope != model.ScopeUser {
		t.Errorf("expected user scope, got %q", found.Installation.Scope)
	}
}

func TestLocateAttributesSystemInstalls(t *testing.T) {
	loc := pathLocator(map[string]string{"git": "/usr/bin/git"})
	found := loc.Locate("git")
	if found.Installation == nil || found.Installation.Provider != "system" {
		t.Fatalf("expected system ownership, got %+v", found)
	}
	if found.Installation.Scope != model.ScopeGlobal {
		t.Errorf("expected global scope, got %q", found.Installation.Scope)
	}
}

func TestLocateAttributesCargoAndGoBinaries(t *testing.T) {
	cases := map[string]string{
		"/home/u/.cargo/bin/rg":                       "cargo",
		"/home/u/go/bin/gopls":                        "go",
		"/home/u/.local/share/uv/tools/ruff/bin/ruff": "uv",
	}
	for path, want := range cases {
		loc := pathLocator(map[string]string{"tool": path})
		found := loc.Locate("tool")
		if found.Installation == nil || found.Installation.Provider != want {
			t.Errorf("%s attributed to %+v, want %q", path, found.Installation, want)
		}
	}
}

func TestLocateUnknownPathIsReportedNotAttributed(t *testing.T) {
	loc := pathLocator(map[string]string{"weird": "/opt/whatever/weird"})
	found := loc.Locate("weird")
	if found.Attributed {
		t.Error("an unknown location must not be attributed to a provider")
	}
	if found.Path == "" {
		t.Error("the path should still be reported")
	}
	if found.Reason == "" {
		t.Error("an unattributed path needs a reason")
	}
}

func TestLocateMissingExecutable(t *testing.T) {
	loc := pathLocator(nil)
	found := loc.Locate("absent")
	if found.Installation != nil {
		t.Errorf("nothing should be found, got %+v", found.Installation)
	}
	if found.Reason != "not on PATH" {
		t.Errorf("unexpected reason: %q", found.Reason)
	}
}

func TestLocateEmptyName(t *testing.T) {
	loc := pathLocator(nil)
	found := loc.Locate("")
	if found.Installation != nil || found.Reason == "" {
		t.Errorf("expected an empty result with a reason, got %+v", found)
	}
}

func TestHasPathPrefixComparesWholeComponents(t *testing.T) {
	if !hasPathPrefix("/usr/local/bin/rg", "/usr/local/bin") {
		t.Error("a path inside the prefix should match")
	}
	if hasPathPrefix("/usr/local/bin-extra/rg", "/usr/local/bin") {
		t.Error("a shared string prefix is not a path prefix")
	}
	if !hasPathPrefix("/usr/local/bin", "/usr/local/bin") {
		t.Error("the directory itself should match")
	}
}

func TestSpecificPrefixWinsOverGeneric(t *testing.T) {
	// /opt/homebrew/bin must not be attributed to some broader prefix just
	// because the string starts similarly.
	loc := pathLocator(map[string]string{"rg": "/opt/homebrew/Cellar/ripgrep/14.1.0/bin/rg"})
	found := loc.Locate("rg")
	if found.Installation == nil || found.Installation.Provider != "brew" {
		t.Fatalf("Cellar paths belong to brew, got %+v", found)
	}
}

func TestLinuxHomebrewPrefix(t *testing.T) {
	loc := pathLocator(map[string]string{"rg": "/home/linuxbrew/.linuxbrew/bin/rg"})
	found := loc.Locate("rg")
	if found.Installation == nil || found.Installation.Provider != "brew" {
		t.Fatalf("Linuxbrew paths belong to brew, got %+v", found)
	}
}

func TestRustupToolchainDirectoryIsOwnedByRustup(t *testing.T) {
	// ~/.cargo/bin holds shims and cargo-installed crates; the toolchain itself
	// lives under ~/.rustup. Getting this backwards would report rustc as
	// Cargo's.
	loc := pathLocator(map[string]string{
		"rustc": "/Users/u/.rustup/toolchains/stable-x86_64-apple-darwin/bin/rustc",
		"cargo": "/Users/u/.cargo/bin/cargo",
		"rg":    "/Users/u/.cargo/bin/rg",
	})
	if got := loc.Locate("rustc"); got.Installation == nil || got.Installation.Provider != "rustup" {
		t.Errorf("a toolchain binary belongs to rustup, got %+v", got)
	}
	if got := loc.Locate("cargo"); got.Installation == nil || got.Installation.Provider != "cargo" {
		t.Errorf("~/.cargo/bin still belongs to cargo, got %+v", got)
	}
	if got := loc.Locate("rg"); got.Installation == nil || got.Installation.Provider != "cargo" {
		t.Errorf("a cargo-installed crate belongs to cargo, got %+v", got)
	}
}

func TestZeroValueLocatorUsesTheHost(t *testing.T) {
	// A Locator with no hooks must still work rather than panicking: the engine
	// and its tests construct one directly.
	l := &Locator{}
	if _, err := l.lookPath("sh"); err != nil && runtime.GOOS != "windows" {
		t.Errorf("the host PATH should be used by default: %v", err)
	}
	if _, err := l.stat("/"); err != nil {
		t.Errorf("the host filesystem should be used by default: %v", err)
	}
}

func TestPartiallyConstructedLocatorDoesNotPanic(t *testing.T) {
	// Only LookPath overridden: the other hooks must fall back, not nil-panic.
	l := &Locator{LookPath: func(string) (string, error) { return "/usr/bin/git", nil }}
	found := l.Locate("git")
	if found.Installation == nil || found.Installation.Provider != "system" {
		t.Errorf("attribution should still work, got %+v", found)
	}
}

func TestHostProviderLocationsAreAttributed(t *testing.T) {
	// Every package manager on the host has to be attributable, or Starpoint
	// cannot say where software came from.
	cases := map[string]string{
		"/Users/u/Library/Python/3.14/lib/python/site-packages/pillow": "pip",
		"/Users/u/.local/lib/python3.12/site-packages/pillow":          "pip",
		"/Users/u/Library/pnpm/global/v11/node_modules/vite":           "pnpm",
		"/Users/u/.local/share/pnpm/store/v3/files/x":                  "pnpm",
		"/Users/u/.config/yarn/global/node_modules/tsc":                "yarn",
		"/Library/Ruby/Gems/2.6.0/gems/minitest-5.11.3":                "gem",
		"/Users/u/.gem/ruby/3.2.0/gems/rake-13.0.6":                    "gem",
	}
	for path, want := range cases {
		loc := pathLocator(map[string]string{"tool": path})
		found := loc.Locate("tool")
		if found.Installation == nil {
			t.Errorf("%s was not attributed at all", path)
			continue
		}
		if found.Installation.Provider != want {
			t.Errorf("%s attributed to %q, want %q", path, found.Installation.Provider, want)
		}
	}
}

func TestNpmGlobalStillAttributed(t *testing.T) {
	loc := pathLocator(map[string]string{
		"tsc": "/Users/u/.nvm/versions/node/v22.0.0/lib/node_modules/typescript/bin/tsc",
	})
	found := loc.Locate("tsc")
	if found.Installation == nil || found.Installation.Provider != npm.Name {
		t.Errorf("npm's global packages must still be attributed to npm, got %+v", found)
	}
}
