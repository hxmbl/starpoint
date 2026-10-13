package dnf

import (
	"context"
	"testing"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	// Discovery and version checks read the rpm database, so a working dnf
	// without a working rpm is not a usable provider.
	r := testsupport.NewFakeRunner().WithBin("dnf").WithBin("rpm")
	p := New(r)
	p.platform = func() bool { return true }
	r.Set("4.19.1", "dnf", "--version")
	// Successful mutations produce no output.
	r.Set("", "dnf", "install", "-y", "git-2.43.0-1")
	r.Set("", "dnf", "remove", "-y", "git")
	r.Set("", "dnf", "upgrade", "-y", "git")
	return p, r
}

func TestHealth(t *testing.T) {
	p, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("dnf should be available: %s", h.Reason)
	}
	if h.Version != "4.19.1" {
		t.Errorf("version parsed as %q", h.Version)
	}
	if !h.Capabilities.PinVersion {
		t.Error("dnf can install pkg-version and should advertise it")
	}
	if h.Capabilities.DefaultScope != model.ScopeGlobal {
		t.Errorf("dnf installs system-wide, got scope %q", h.Capabilities.DefaultScope)
	}
}

func TestDiscoverParsesRpmOutput(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git\t2.43.0-1.fc39\nripgrep\t13.0.0-3.fc39\n", "rpm", "-qa", "--queryformat="+rpmFormat)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(insts))
	}
	if insts[0].Name != "git" || insts[0].Version != "2.43.0-1.fc39" {
		t.Errorf("version parsing failed, got %+v", insts[0])
	}
	if insts[0].Scope != model.ScopeGlobal {
		t.Errorf("dnf packages are global, got %q", insts[0].Scope)
	}
}

func TestInstalledUsesVersionAsPresenceSignal(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git\t2.43.0-1.fc39\n", "rpm", "-q", "--queryformat="+rpmFormat, "git")
	inst, err := p.Installed(context.Background(), model.Package{Name: "git"})
	if err != nil || inst == nil {
		t.Fatalf("git should be reported installed, got (%+v, %v)", inst, err)
	}

	r.Set("", "rpm", "-q", "--queryformat="+rpmFormat, "oldpkg")
	absent, err := p.Installed(context.Background(), model.Package{Name: "oldpkg"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if absent != nil {
		t.Errorf("an absent package must not count as installed, got %+v", absent)
	}
}

func TestInstallWithVersionPin(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git\t2.43.0-1.fc39\n", "rpm", "-q", "--queryformat="+rpmFormat, "git")
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "git", Version: "2.43.0-1"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("dnf", "install", "-y", "git-2.43.0-1") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestRemoveAndUpgradeCommands(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "dnf", "remove", "-y", "git")
	r.Set("", "dnf", "upgrade", "-y", "git")
	r.Set("git\t2.44.0-1.fc39\n", "rpm", "-q", "--queryformat="+rpmFormat, "git")

	if err := p.Remove(context.Background(), model.Package{Name: "git"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("dnf", "remove", "-y", "git") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Update(context.Background(), model.Package{Name: "git"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("dnf", "upgrade", "-y", "git") {
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
	if r.Ran("dnf", "install", "-y", "git") {
		t.Error("a dry run must not run dnf install")
	}
}

func TestClassifyErrors(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   provider.ErrorKind
	}{
		{"missing", "No match for argument: zzz", provider.ErrNotFound},
		{"permission", "Error: This command has to be run with superuser privileges", provider.ErrPermission},
		{"network", "Failed to synchronize cache for repo 'fedora'", provider.ErrNetwork},
		{"conflict", "transaction check error: dependency conflict", provider.ErrConflict},
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
