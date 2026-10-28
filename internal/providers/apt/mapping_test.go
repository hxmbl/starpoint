package apt

import (
	"context"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

// Debian ships three common tools under executable names that differ from
// their packages. The map goes executable → package, and a package name must
// never stand in for an executable.
func TestExecutableMappingsUseDebianExecutableNames(t *testing.T) {
	p, _ := newProvider(t)
	m := p.ExecutableToPackage()
	for exe, pkg := range map[string]string{"rg": "ripgrep", "fdfind": "fd-find", "batcat": "bat"} {
		if got := m[exe]; got != pkg {
			t.Errorf("executable %s → package %q, want %q", exe, got, pkg)
		}
		if got, ok := p.ExecutableForPackage(pkg); !ok || got != exe {
			t.Errorf("package %s → executable %q (%v), want %q", pkg, got, ok, exe)
		}
	}
	// Package names, or names apt does not install, are not executables of
	// these packages: a `ripgrep`, `fd` or `bat` on PATH is not apt's.
	for _, notExe := range []string{"ripgrep", "fd-find", "fd", "bat"} {
		if pkg, ok := m[notExe]; ok {
			t.Errorf("%q is mapped as an executable (to %q); apt installs no such executable for it", notExe, pkg)
		}
	}
}

// Verification runs the executable the package really installs.
func TestVerifyRunsTheMappedExecutable(t *testing.T) {
	for pkg, exe := range map[string]string{"ripgrep": "rg", "fd-find": "fdfind", "bat": "batcat"} {
		p, r := newProvider(t)
		r.Set(exe+" 1.0.0", exe, "--version")
		if err := p.Verify(context.Background(), model.Package{Name: pkg}); err != nil {
			t.Errorf("verify %s: %v", pkg, err)
		}
	}
}
