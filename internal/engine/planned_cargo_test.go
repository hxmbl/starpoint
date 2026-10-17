package engine

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/cargo"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// A planned cargo install names the command the crate installs (ripgrep
// installs rg), not a file named after the crate.
func TestPlannedCargoInstallUsesTheCommandName(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CARGO_INSTALL_ROOT", "")
	t.Setenv("CARGO_HOME", "")
	r := testsupport.NewFakeRunner().WithBin("cargo")
	r.Set("", "cargo", "install", "--list")
	reg := provider.NewRegistry()
	c := cargo.New(r, home)
	reg.Register(c)
	e := New(reg, nil, WithHome(home))
	bin := filepath.Join(home, ".cargo", "bin")

	effects, _ := e.plannedInstall(context.Background(), c, model.Package{Name: "ripgrep"})
	if eff := effectAt(effects, filepath.Join(bin, "rg")); eff == nil || !strings.Contains(eff.Detail, "installs the command rg") {
		t.Errorf("the planned path is ~/.cargo/bin/rg: %+v", effects)
	}
	if eff := effectAt(effects, filepath.Join(bin, "ripgrep")); eff != nil {
		t.Errorf("no file named after the crate is predicted: %+v", eff)
	}

	effects, _ = e.plannedInstall(context.Background(), c, model.Package{Name: "hyperfine"})
	eff := effectAt(effects, filepath.Join(bin, "hyperfine"))
	if eff == nil || eff.Evidence != model.EvidencePlanned || !strings.Contains(eff.Detail, "assumes the command is named after hyperfine") {
		t.Errorf("an unknown crate keeps its name and says it is an assumption: %+v", effects)
	}
}
