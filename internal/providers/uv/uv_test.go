package uv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

const toolListOutput = `ruff v0.5.4
- ruff
python v3.12.3
- python
`

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("uv")
	p := New(r, "/home/u")
	r.Set("uv 0.4.20 (abc1234 2024-10-01)", "uv", "--version")
	r.Set(toolListOutput, "uv", "tool", "list")
	return p, r
}

func TestHealth(t *testing.T) {
	p, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("uv should be available: %s", h.Reason)
	}
	if h.Version != "0.4.20" {
		t.Errorf("version parsed as %q", h.Version)
	}
	if !h.Capabilities.PinVersion {
		t.Error("uv can install an exact version and should say so")
	}
}

func TestDiscoverParsesToolBlocks(t *testing.T) {
	p, _ := newProvider(t)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 tools, got %d: %+v", len(insts), insts)
	}
	if insts[0].Name != "ruff" || insts[0].Version != "0.5.4" {
		t.Errorf("unexpected first tool: %+v", insts[0])
	}
	if insts[0].Path != "ruff" {
		t.Errorf("the executable name should be recorded, got %q", insts[0].Path)
	}
	if insts[1].Name != "python" || insts[1].Version != "3.12.3" {
		t.Errorf("unexpected second tool: %+v", insts[1])
	}
}

func TestInstalledResolvesSingleTool(t *testing.T) {
	p, _ := newProvider(t)
	inst, err := p.Installed(context.Background(), model.Package{Name: "ruff"})
	if err != nil {
		t.Fatalf("installed: %v", err)
	}
	if inst == nil {
		t.Fatal("ruff should have been found")
	}
	if inst.Provider != Name {
		t.Errorf("attribution wrong: %+v", inst)
	}
	missing, err := p.Installed(context.Background(), model.Package{Name: "nope"})
	if err != nil || missing != nil {
		t.Fatalf("absence must be (nil, nil), got (%+v, %v)", missing, err)
	}
}

func TestInstallUsesPEP440Pin(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "uv", "tool", "install", "ruff==0.6.0")
	_, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "ruff", Version: "0.6.0"},
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("uv", "tool", "install", "ruff==0.6.0") {
		t.Errorf("expected a PEP 440 pin, calls: %v", r.Calls)
	}
}

func TestInstallNormalisesDoubleEqualsPin(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "uv", "tool", "install", "ruff==0.6.0")
	_, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "ruff", Version: "==0.6.0"},
	})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("uv", "tool", "install", "ruff==0.6.0") {
		t.Errorf("a '==' prefix must not be doubled, calls: %v", r.Calls)
	}
}

func TestInstallWithoutPin(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "uv", "tool", "install", "ruff")
	if _, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "ruff"}}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("uv", "tool", "install", "ruff") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestRemoveAndUpdateCommands(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "uv", "tool", "uninstall", "ruff")
	r.Set("", "uv", "tool", "upgrade", "ruff")
	if err := p.Remove(context.Background(), model.Package{Name: "ruff"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("uv", "tool", "uninstall", "ruff") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Update(context.Background(), model.Package{Name: "ruff"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("uv", "tool", "upgrade", "ruff") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestDryRunDoesNotInstall(t *testing.T) {
	p, r := newProvider(t)
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "ruff"}, DryRun: true,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.Ran("uv", "tool", "install", "ruff") {
		t.Error("a dry run must not invoke uv")
	}
}

func TestUnknownPackageIsTypedNotFound(t *testing.T) {
	p, r := newProvider(t)
	r.SetFail(1, "error: Failed to parse: package `nope` was not found in the registry", "uv", "tool", "install", "nope")
	_, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "nope"}})
	pe, ok := provider.AsProviderError(err)
	if !ok {
		t.Fatalf("expected a provider error, got %v", err)
	}
	if pe.Kind != provider.ErrNotFound {
		t.Errorf("classified as %q, want %q", pe.Kind, provider.ErrNotFound)
	}
}

func TestEmptyOutputIsHandled(t *testing.T) {
	p, _ := newProvider(t)
	insts := p.parseToolList("")
	if len(insts) != 0 {
		t.Errorf("expected no tools, got %+v", insts)
	}
}

func TestExistingExecutableIsAConflictThatNamesTheFile(t *testing.T) {
	// uv prints progress first. The error used to be reported as "Resolved 1
	// package in 1ms", which is the one line that explains nothing.
	p, r := newProvider(t)
	r.SetFail(2, "Resolved 1 package in 1ms\nerror: Executable already exists: cowsay (use `--force` to overwrite)",
		"uv", "tool", "install", "cowsay")
	_, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "cowsay"}})
	pe, ok := provider.AsProviderError(err)
	if !ok {
		t.Fatalf("expected a provider error, got %v", err)
	}
	if pe.Kind != provider.ErrConflict {
		t.Errorf("classified as %q, want conflict", pe.Kind)
	}
	if !strings.Contains(err.Error(), "Executable already exists: cowsay") {
		t.Errorf("the error must carry uv's explanation, got %q", err.Error())
	}
}

func TestOtherFailuresCarryTheMeaningfulStderr(t *testing.T) {
	p, r := newProvider(t)
	r.SetFail(2, "Resolved 3 packages in 2ms\nerror: Failed to install entrypoint\n  Caused by: Permission denied (os error 13)",
		"uv", "tool", "install", "cowsay")
	_, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "cowsay"}})
	if err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("the cause should be in the error, got %v", err)
	}
}

func TestToolBinPathIgnoresFilesUvDidNotLink(t *testing.T) {
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	t.Setenv("UV_TOOL_BIN_DIR", "")
	t.Setenv("XDG_BIN_HOME", "")
	testsupport.WriteFile(t, filepath.Join(bin, "cowsay"), "#!/bin/sh\n")
	p := New(testsupport.NewFakeRunner(), home)
	if got := p.toolBinPath("cowsay"); got != "" {
		t.Errorf("a plain file (pip's console script) is not uv's, got %q", got)
	}

	toolExe := filepath.Join(home, ".local", "share", "uv", "tools", "black", "bin", "black")
	testsupport.WriteFile(t, toolExe, "#!/bin/sh\n")
	if err := os.Symlink(toolExe, filepath.Join(bin, "black")); err != nil {
		t.Fatal(err)
	}
	if got := p.toolBinPath("black"); got != filepath.Join(bin, "black") {
		t.Errorf("a link into uv's tool directory is uv's, got %q", got)
	}
	if got := p.ExecutableDir(context.Background()); got != bin {
		t.Errorf("ExecutableDir = %q, want %q", got, bin)
	}
	t.Setenv("UV_TOOL_BIN_DIR", "/custom/bin")
	if got := p.ExecutableDir(context.Background()); got != "/custom/bin" {
		t.Errorf("UV_TOOL_BIN_DIR should win, got %q", got)
	}
}
