package brew

import (
	"context"
	"testing"

	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/testsupport"
)

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("brew")
	p := New(r)
	r.Set("Homebrew 4.3.5\nHomebrew/homebrew-core (git revision abc123)", "brew", "--version")
	// Successful mutations produce no output; the follow-up query does.
	r.Set("", "brew", "install", "ripgrep")
	r.Set("", "brew", "install", "--cask", "firefox")
	r.Set("", "brew", "uninstall", "--cask", "firefox")
	r.Set("", "brew", "upgrade", "--cask", "firefox")
	return p, r
}

func TestHealth(t *testing.T) {
	p, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("brew should be available: %s", h.Reason)
	}
	if h.Version != "4.3.5" {
		t.Errorf("version parsed as %q", h.Version)
	}
	if h.Binary == "" {
		t.Error("the resolved binary path should be reported")
	}
	if !h.Capabilities.Install || !h.Capabilities.Update {
		t.Error("brew can install and update")
	}
	if h.Capabilities.PinVersion {
		t.Error("brew cannot install an arbitrary version; it must not claim to")
	}
}

func TestHealthWhenAbsent(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	h := p.Health(context.Background())
	if h.Available {
		t.Fatal("brew is not on the fake PATH and must be reported unavailable")
	}
	if h.Reason == "" {
		t.Error("an unavailable provider must say why")
	}
}

func TestDiscoverParsesFormulaeAndCasks(t *testing.T) {
	p, r := newProvider(t)
	r.Set("ripgrep 14.1.0\npython@3.12 3.12.2_1\n", "brew", "list", "--versions", "--formula")
	r.Set("firefox 121.0\n", "brew", "list", "--versions", "--cask")

	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 3 {
		t.Fatalf("expected 3 installations, got %d: %+v", len(insts), insts)
	}
	if insts[0].Name != "ripgrep" || insts[0].Version != "14.1.0" {
		t.Errorf("unexpected first entry: %+v", insts[0])
	}
	if insts[1].Version != "3.12.2_1" {
		t.Errorf("multiple installed versions should still report one version: %+v", insts[1])
	}
	if insts[2].Name != "firefox" {
		t.Errorf("unexpected cask entry: %+v", insts[2])
	}
	for _, i := range insts {
		if i.Provider != Name {
			t.Errorf("entry not attributed to brew: %+v", i)
		}
	}
}

func TestInstalledReturnsNilWhenAbsent(t *testing.T) {
	p, r := newProvider(t)
	r.SetFail(1, "", "brew", "list", "--versions", "nope")
	inst, err := p.Installed(context.Background(), model.Package{Name: "nope"})
	if err != nil {
		t.Fatalf("absence must not be an error: %v", err)
	}
	if inst != nil {
		t.Fatalf("expected nil, got %+v", inst)
	}
}

func TestInstallBuildsFormulaCommand(t *testing.T) {
	p, r := newProvider(t)
	r.Set("ripgrep 14.1.0", "brew", "list", "--versions", "ripgrep")
	if _, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "ripgrep"}}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("brew", "install", "ripgrep") {
		t.Errorf("expected 'brew install ripgrep', calls: %v", r.Calls)
	}
}

func TestInstallBuildsCaskCommand(t *testing.T) {
	p, r := newProvider(t)
	r.Set("firefox 121.0", "brew", "list", "--versions", "--cask", "firefox")
	pkg := model.Package{Name: "firefox", Channel: ChannelCask}
	if _, err := p.Install(context.Background(), provider.Request{Package: pkg}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("brew", "install", "--cask", "firefox") {
		t.Errorf("expected 'brew install --cask firefox', calls: %v", r.Calls)
	}
}

func TestInstallRefusesVersionPin(t *testing.T) {
	p, r := newProvider(t)
	_, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "ripgrep", Version: "14.1.0"}})
	if err == nil {
		t.Fatal("brew cannot honour a version pin; it must say so rather than install the wrong thing")
	}
	pe, ok := provider.AsProviderError(err)
	if !ok || pe.Kind != provider.ErrUnsupported {
		t.Fatalf("expected an unsupported error, got %v", err)
	}
	if r.Ran("brew", "install", "ripgrep") {
		t.Error("no command should have been run")
	}
}

func TestDryRunDoesNotInstall(t *testing.T) {
	p, r := newProvider(t)
	_, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "ripgrep"}, DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.Ran("brew", "install", "ripgrep") {
		t.Error("a dry run must not run brew install")
	}
}

func TestClassifyErrors(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   provider.ErrorKind
	}{
		{"missing", "Error: No available formula with the name \"zzz\".", provider.ErrNotFound},
		{"permission", "Error: Permission denied @ rb_sysopen", provider.ErrPermission},
		{"network", "curl: (6) Could not resolve host: formulae.brew.sh", provider.ErrNetwork},
		{"other", "Error: something entirely new", provider.ErrFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classify(Name, "install", resultWithStderr(tc.stderr))
			pe, ok := provider.AsProviderError(err)
			if !ok {
				t.Fatalf("expected a provider error, got %T", err)
			}
			if pe.Kind != tc.want {
				t.Errorf("classified as %q, want %q (message: %s)", pe.Kind, tc.want, pe.Message)
			}
		})
	}
}

func TestRemoveAndUpdateUseCaskFlag(t *testing.T) {
	p, r := newProvider(t)
	pkg := model.Package{Name: "firefox", Channel: ChannelCask}
	r.Set("firefox 121.0", "brew", "list", "--versions", "--cask", "firefox")

	if err := p.Remove(context.Background(), pkg); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("brew", "uninstall", "--cask", "firefox") {
		t.Errorf("expected 'brew uninstall --cask firefox', calls: %v", r.Calls)
	}
	if _, err := p.Update(context.Background(), pkg); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("brew", "upgrade", "--cask", "firefox") {
		t.Errorf("expected 'brew upgrade --cask firefox', calls: %v", r.Calls)
	}
}

// resultWithStderr builds a failed command result.
func resultWithStderr(stderr string) exec.Result {
	return exec.Result{Stderr: stderr, ExitCode: 1}
}
