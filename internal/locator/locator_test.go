package locator

import (
	"errors"
	"os"
	"testing"

	"github.com/starpoint/starpoint/internal/model"
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
