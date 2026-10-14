package pnpm

import (
	"context"
	"testing"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// lsJSON is the shape `pnpm ls -g --depth 0 --json` prints, captured from pnpm
// 12.9.1.
const lsJSON = `[
  {
    "path": "/Users/u/Library/pnpm/global/v11",
    "private": true,
    "dependencies": {
      "vite": {
        "from": "vite",
        "version": "5.4.10",
        "resolved": "https://registry.npmjs.org/vite/-/vite-5.4.10.tgz"
      },
      "typescript": {
        "from": "typescript",
        "version": "5.6.3",
        "resolved": "https://registry.npmjs.org/typescript/-/typescript-5.6.3.tgz"
      }
    }
  }
]`

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("pnpm")
	p := New(r)
	r.Set("12.9.1\n", "pnpm", "--version")
	r.Set(lsJSON, "pnpm", "ls", "-g", "--depth", "0", "--json")
	return p, r
}

func TestHealth(t *testing.T) {
	p, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("pnpm should be available: %s", h.Reason)
	}
	if h.Version != "12.9.1" {
		t.Errorf("version parsed as %q", h.Version)
	}
	if !h.Capabilities.PinVersion {
		t.Error("pnpm can pin a version")
	}
	if h.Capabilities.DefaultScope != model.ScopeUser {
		t.Errorf("pnpm globals are per-user, got %q", h.Capabilities.DefaultScope)
	}
}

func TestHealthNotesWhenTheGlobalBinDirIsMissingFromPath(t *testing.T) {
	p, r := newProvider(t)
	// The real failure on a default macOS install.
	r.Responses["pnpm bin -g"] = exec.Result{
		Stderr: "Error: ERR_PNPM_GLOBAL_BIN_DIR_NOT_IN_PATH", ExitCode: 1,
	}
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("pnpm is installed even when its bin dir is not on PATH: %s", h.Reason)
	}
	if h.Reason == "" {
		t.Error("the usual reason a pnpm command appears missing should be reported")
	}
}

func TestHealthWhenAbsent(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	h := p.Health(context.Background())
	if h.Available {
		t.Fatal("pnpm is not on the fake PATH")
	}
	if h.Reason == "" {
		t.Error("an unavailable provider must say why")
	}
}

func TestDiscoverParsesTheGlobalProject(t *testing.T) {
	p, _ := newProvider(t)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 global packages, got %+v", insts)
	}
	byName := map[string]model.Installation{}
	for _, i := range insts {
		byName[i.Name] = i
	}
	if byName["vite"].Version != "5.4.10" {
		t.Errorf("unexpected vite entry: %+v", byName["vite"])
	}
	for _, i := range insts {
		if i.Provider != Name {
			t.Errorf("attribution wrong: %+v", i)
		}
	}
	if p.GlobalDir() != "/Users/u/Library/pnpm/global/v11" {
		t.Errorf("the global directory pnpm reported should be kept, got %q", p.GlobalDir())
	}
}

func TestDiscoverTreatsNoPackagesAsEmptyNotFailure(t *testing.T) {
	// pnpm reports an empty global set as an error, not as an empty list.
	p, r := newProvider(t)
	r.Responses["pnpm ls -g --depth 0 --json"] = exec.Result{
		Stderr: "ERR_PNPM_NO_GLOBAL_BIN_DIR  No global packages found", ExitCode: 1,
	}
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("an empty global set is not an error: %v", err)
	}
	if len(insts) != 0 {
		t.Errorf("expected nothing, got %+v", insts)
	}
}

func TestDiscoverToleratesProgressOutput(t *testing.T) {
	p, r := newProvider(t)
	r.Responses["pnpm ls -g --depth 0 --json"] = exec.Result{
		Stdout: "Lockfile is up to date, resolution step is skipped\nProgress: resolved 1, reused 1\n" + lsJSON,
	}
	if _, err := p.Discover(context.Background()); err != nil {
		t.Fatalf("progress chatter around the JSON should be stripped: %v", err)
	}
}

func TestInstallPinAndChannel(t *testing.T) {
	p, r := newProvider(t)
	r.Responses["pnpm add -g vite@5.4.10"] = exec.Result{}
	r.Responses["pnpm add -g vite@next"] = exec.Result{}

	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "vite", Version: "5.4.10"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("pnpm", "add", "-g", "vite@5.4.10") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "vite", Channel: "next"},
	}); err != nil {
		t.Fatalf("install with channel: %v", err)
	}
	if !r.Ran("pnpm", "add", "-g", "vite@next") {
		t.Errorf("a channel becomes a dist-tag, calls: %v", r.Calls)
	}
}

func TestRemoveAndUpdate(t *testing.T) {
	p, r := newProvider(t)
	r.Responses["pnpm remove -g vite"] = exec.Result{}
	r.Responses["pnpm update -g vite"] = exec.Result{}
	r.Responses["pnpm update -g vite@5.4.10"] = exec.Result{}

	if err := p.Remove(context.Background(), model.Package{Name: "vite"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("pnpm", "remove", "-g", "vite") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Update(context.Background(), model.Package{Name: "vite"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("pnpm", "update", "-g", "vite") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Update(context.Background(), model.Package{Name: "vite", Version: "5.4.10"}); err != nil {
		t.Fatalf("update with pin: %v", err)
	}
	if !r.Ran("pnpm", "update", "-g", "vite@5.4.10") {
		t.Errorf("a pin must be passed through on update, calls: %v", r.Calls)
	}
}

func TestDryRunDoesNotInstall(t *testing.T) {
	p, r := newProvider(t)
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "vite"}, DryRun: true,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.Ran("pnpm", "add", "-g", "vite") {
		t.Error("a dry run must not invoke pnpm")
	}
}

func TestAbsentPnpmOwnsNothingAndRunsNothing(t *testing.T) {
	r := testsupport.NewFakeRunner()
	p := New(r)
	if insts, err := p.Discover(context.Background()); err != nil || len(insts) != 0 {
		t.Errorf("an absent provider owns nothing, got (%+v, %v)", insts, err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("no command should run, got %v", r.Calls)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		stderr string
		want   provider.ErrorKind
	}{
		{"ERR_PNPM_PACKAGE_NOT_FOUND  vite@9.9.9 is not in the global directory", provider.ErrNotFound},
		{"ERR_PNPM_CANNOT_OVERWRITE  Cannot overwrite", provider.ErrConflict},
		{"ERR_PNPM_EACCES  permission denied writing to the global dir", provider.ErrPermission},
		{"ERR_PNPM_FETCH_404  registry.npmjs.org: ENOTFOUND", provider.ErrNetwork},
		{"something entirely new", provider.ErrFailed},
	}
	p, _ := newProvider(t)
	for _, tc := range tests {
		err := p.classify("install", exec.Result{Stderr: tc.stderr, ExitCode: 1})
		pe, ok := provider.AsProviderError(err)
		if !ok || pe.Kind != tc.want {
			t.Errorf("%q classified as %v, want %q", tc.stderr, err, tc.want)
		}
	}
}
