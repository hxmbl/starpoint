package pacman

import (
	"context"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("pacman")
	p := New(r)
	p.platform = func() bool { return true }
	r.Set(" .--                  Pacman v6.0.2 - libalpm v14.0.2", "pacman", "--version")
	// Successful mutations produce no output.
	r.Set("", "pacman", "-S", "--noconfirm", "git")
	r.Set("", "pacman", "-R", "--noconfirm", "git")
	return p, r
}

func TestHealth(t *testing.T) {
	p, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("pacman should be available: %s", h.Reason)
	}
	if h.Version != "6.0.2" {
		t.Errorf("version parsed as %q", h.Version)
	}
	// Arch repositories only serve current versions, so advertising pinning
	// would let the engine promise something Install cannot deliver.
	if h.Capabilities.PinVersion {
		t.Error("pacman cannot honour a version pin and must not advertise one")
	}
	if h.Capabilities.DefaultScope != model.ScopeGlobal {
		t.Errorf("pacman installs system-wide, got scope %q", h.Capabilities.DefaultScope)
	}
}

func TestDiscoverParsesPacmanOutput(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git 2.43.0-1\nripgrep 13.0.0-1\n", "pacman", "-Q")
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(insts))
	}
	if insts[0].Name != "git" || insts[0].Version != "2.43.0-1" {
		t.Errorf("version parsing failed, got %+v", insts[0])
	}
	if insts[0].Scope != model.ScopeGlobal {
		t.Errorf("pacman packages are global, got %q", insts[0].Scope)
	}
}

func TestInstalledUsesVersionAsPresenceSignal(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git 2.43.0-1", "pacman", "-Q", "git")
	inst, err := p.Installed(context.Background(), model.Package{Name: "git"})
	if err != nil || inst == nil {
		t.Fatalf("git should be reported installed, got (%+v, %v)", inst, err)
	}

	r.Set("", "pacman", "-Q", "oldpkg")
	absent, err := p.Installed(context.Background(), model.Package{Name: "oldpkg"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if absent != nil {
		t.Errorf("an absent package must not count as installed, got %+v", absent)
	}
}

func TestInstall(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git 2.43.0-1", "pacman", "-Q", "git")
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "git"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("pacman", "-S", "--noconfirm", "git") {
		t.Errorf("calls: %v", r.Calls)
	}
}

// A version pin must be refused up front, not turned into a `pacman -U
// name=version` call, which cannot work: -U takes a file or URL.
func TestInstallRefusesVersionPin(t *testing.T) {
	p, r := newProvider(t)
	_, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "git", Version: "2.43.0-1"},
	})
	if err == nil {
		t.Fatal("installing a pinned version should fail rather than run a broken command")
	}
	for _, call := range r.Calls {
		if strings.Contains(call, "git=2.43.0-1") || strings.Contains(call, "-U") {
			t.Fatalf("built an APT-style target pacman cannot install: %s", call)
		}
	}
}

func TestRemoveAndUpgradeCommands(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "pacman", "-R", "--noconfirm", "git")
	r.Set("", "pacman", "-S", "--noconfirm", "git")
	r.Set("git 2.44.0-1", "pacman", "-Q", "git")

	if err := p.Remove(context.Background(), model.Package{Name: "git"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("pacman", "-R", "--noconfirm", "git") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Update(context.Background(), model.Package{Name: "git"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("pacman", "-S", "--noconfirm", "git") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestDryRunDoesNotInstall(t *testing.T) {
	p, r := newProvider(t)
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "git"}, DryRun: true,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.Ran("pacman", "-S", "--noconfirm", "git") {
		t.Error("a dry run must not run pacman -S")
	}
}

func TestClassifyErrors(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   provider.ErrorKind
	}{
		{"missing", "error: target not found: zzz", provider.ErrNotFound},
		{"permission", "error: you cannot perform this operation unless you are root", provider.ErrPermission},
		{"network", "error: failed to synchronize all databases", provider.ErrNetwork},
		{"conflict", "error: unresolvable package conflicts", provider.ErrConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classify("install", exec.Result{Stderr: tc.stderr, ExitCode: 1})
			pe, ok := provider.AsProviderError(err)
			if !ok || pe.Kind != tc.want {
				t.Fatalf("classified as %v, want %q", err, tc.want)
			}
		})
	}
}

func TestExecutableMapping(t *testing.T) {
	p, _ := newProvider(t)
	mapping := p.ExecutableToPackage()
	if mapping == nil {
		t.Fatal("ExecutableToPackage should not return nil")
	}
	// Test a few known mappings
	if pkg, ok := mapping["git"]; !ok || pkg != "git" {
		t.Errorf("git mapping incorrect, got %q", pkg)
	}
	// Arch's package is python3; "python" is a virtual provide with no
	// /usr/bin/python to verify against.
	if pkg, ok := mapping["python3"]; !ok || pkg != "python3" {
		t.Errorf("python3 mapping incorrect, got %q", pkg)
	}
	if pkg, ok := mapping["node"]; !ok || pkg != "nodejs" {
		t.Errorf("node mapping incorrect, got %q", pkg)
	}
}

func TestVerify(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git version 2.43.0", "git", "--version")
	if err := p.Verify(context.Background(), model.Package{Name: "git"}); err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !r.Ran("git", "--version") {
		t.Errorf("calls: %v", r.Calls)
	}
}
