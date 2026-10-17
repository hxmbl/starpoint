package cargo

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

const installListOutput = `cargo-edit v0.12.7:
    cargo-add
    cargo-rm
ripgrep v14.1.0:
    rg
`

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("cargo")
	p := New(r, "/home/u")
	r.Set("cargo 1.83.0 (a8a5f3d5b 2024-11-20)", "cargo", "--version")
	r.Set(installListOutput, "cargo", "install", "--list")
	return p, r
}

func TestHealth(t *testing.T) {
	p, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("cargo should be available: %s", h.Reason)
	}
	if h.Version != "1.83.0" {
		t.Errorf("version parsed as %q", h.Version)
	}
}

func TestDiscoverParsesCrateBlocks(t *testing.T) {
	p, _ := newProvider(t)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 crates, got %d: %+v", len(insts), insts)
	}
	if insts[0].Name != "cargo-edit" || insts[0].Version != "0.12.7" {
		t.Errorf("unexpected first crate: %+v", insts[0])
	}
	if insts[0].Path == "" {
		t.Error("the crate's executable should be recorded")
	}
	if insts[1].Name != "ripgrep" || insts[1].Version != "14.1.0" {
		t.Errorf("unexpected second crate: %+v", insts[1])
	}
}

func TestInstalled(t *testing.T) {
	p, _ := newProvider(t)
	inst, err := p.Installed(context.Background(), model.Package{Name: "ripgrep"})
	if err != nil || inst == nil {
		t.Fatalf("expected ripgrep to be found, got (%+v, %v)", inst, err)
	}
	if inst.Provider != Name {
		t.Errorf("attribution wrong: %+v", inst)
	}
	if absent, err := p.Installed(context.Background(), model.Package{Name: "nope"}); err != nil || absent != nil {
		t.Errorf("absence must be (nil, nil), got (%+v, %v)", absent, err)
	}
}

func TestInstallWithVersionPin(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "cargo", "install", "ripgrep", "--version", "14.1.0")
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "ripgrep", Version: "14.1.0"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("cargo", "install", "ripgrep", "--version", "14.1.0") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestUpdateForcesReinstall(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "cargo", "install", "--force", "ripgrep")
	if _, err := p.Update(context.Background(), model.Package{Name: "ripgrep"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("cargo", "install", "--force", "ripgrep") {
		t.Errorf("cargo needs --force to upgrade an installed crate, calls: %v", r.Calls)
	}
}

func TestUpdateHonoursPin(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "cargo", "install", "--version", "14.1.0", "--force", "ripgrep")
	if _, err := p.Update(context.Background(), model.Package{Name: "ripgrep", Version: "14.1.0"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("cargo", "install", "--version", "14.1.0", "--force", "ripgrep") {
		t.Errorf("a pin must be passed through on update, calls: %v", r.Calls)
	}
}

func TestRemoveAndDryRun(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "cargo", "uninstall", "ripgrep")
	if err := p.Remove(context.Background(), model.Package{Name: "ripgrep"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("cargo", "uninstall", "ripgrep") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "ripgrep"}, DryRun: true,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.Ran("cargo", "install", "ripgrep") {
		t.Error("a dry run must not run cargo install")
	}
}

func TestUnknownCrateIsTypedNotFound(t *testing.T) {
	p, r := newProvider(t)
	r.SetFail(101, "error: no matching package named `nope` found", "cargo", "install", "nope")
	_, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "nope"}})
	pe, ok := provider.AsProviderError(err)
	if !ok || pe.Kind != provider.ErrNotFound {
		t.Fatalf("expected a not-found provider error, got %v", err)
	}
}

func TestBinPathLeavesNameWhenNotInCargoBin(t *testing.T) {
	p := New(testsupport.NewFakeRunner(), "/nonexistent-home")
	if got := p.binPath("rg"); got != "rg" {
		t.Errorf("a name with no file on disk should be returned unchanged, got %q", got)
	}
	if got := p.binPath(""); got != "" {
		t.Errorf("empty name should stay empty, got %q", got)
	}
}

func TestExecutableDirFollowsCargosPrecedence(t *testing.T) {
	p, _ := newProvider(t)
	t.Setenv("CARGO_INSTALL_ROOT", "")
	t.Setenv("CARGO_HOME", "")
	if got, want := p.ExecutableDir(context.Background()), filepath.Join("/home/u", ".cargo", "bin"); got != want {
		t.Errorf("default: got %q, want %q", got, want)
	}
	t.Setenv("CARGO_HOME", "/opt/cargo")
	if got, want := p.ExecutableDir(context.Background()), filepath.Join("/opt/cargo", "bin"); got != want {
		t.Errorf("CARGO_HOME: got %q, want %q", got, want)
	}
	t.Setenv("CARGO_INSTALL_ROOT", "/srv/tools")
	if got, want := p.ExecutableDir(context.Background()), filepath.Join("/srv/tools", "bin"); got != want {
		t.Errorf("CARGO_INSTALL_ROOT wins: got %q, want %q", got, want)
	}
}

func TestExecutableDirIsUnknownWithoutAHome(t *testing.T) {
	t.Setenv("CARGO_INSTALL_ROOT", "")
	t.Setenv("CARGO_HOME", "")
	p := New(testsupport.NewFakeRunner(), "")
	if got := p.ExecutableDir(context.Background()); got != "" {
		t.Errorf("no home and no CARGO_HOME means unknown, got %q", got)
	}
}

func TestExecutableForPackage(t *testing.T) {
	p, _ := newProvider(t)
	if exe, ok := p.ExecutableForPackage("ripgrep"); !ok || exe != "rg" {
		t.Errorf("ripgrep installs rg, known before anything is installed: %q %v", exe, ok)
	}
	if exe, ok := p.ExecutableForPackage("fd-find"); !ok || exe != "fd" {
		t.Errorf("fd-find installs fd: %q %v", exe, ok)
	}
	if _, ok := p.ExecutableForPackage("cargo-edit"); ok {
		t.Error("an unknown crate is not guessed before the inventory is read")
	}
	if _, err := p.Discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if exe, ok := p.ExecutableForPackage("cargo-edit"); !ok || exe != "cargo-add" {
		t.Errorf("an installed crate's command comes from cargo install --list (the first one listed): %q %v", exe, ok)
	}
	if _, ok := p.ExecutableForPackage("hyperfine"); ok {
		t.Error("a crate neither installed nor known is not guessed")
	}
}

func TestAMultiBinaryCratesPathIsItsFirstExecutable(t *testing.T) {
	p, _ := newProvider(t)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(insts[0].Path) != "cargo-add" {
		t.Errorf("cargo-edit's path is its first command, not its last: %s", insts[0].Path)
	}
}
