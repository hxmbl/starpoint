package engine

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

// These tests cover the watched set growing to Cargo's and Go's executable
// directories, and what that changes for transitions: a provider that reports
// an ExecutableDir makes the file already at that path part of the
// shared-executable decision.

func TestTooBroadToWatch(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "u")
	cases := []struct {
		dir  string
		want bool
	}{
		{home, true},
		{filepath.Dir(home), true},
		{string(filepath.Separator), true},
		{"relative/bin", true},
		{filepath.Join(home, ".cargo", "bin"), false},
		{filepath.Join(home, "go", "bin"), false},
		{filepath.Join(string(filepath.Separator), "usr", "local", "bin"), false},
		{filepath.Join(string(filepath.Separator), "home", "u2"), false},
	}
	for _, tc := range cases {
		if got := tooBroadToWatch(tc.dir, home); got != tc.want {
			t.Errorf("tooBroadToWatch(%q) = %v, want %v", tc.dir, got, tc.want)
		}
	}
}

func TestObservationNeverWatchesTheHomeDirectory(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	home := t.TempDir()
	f.eng = New(f.reg, nil, WithHome(home))
	f.cargo.ExeDir = home // GOBIN or CARGO_HOME misconfigured to $HOME
	testsupport.WriteFile(t, filepath.Join(home, "notes.txt"), "mine\n")

	o := f.eng.observe(context.Background(), f.cargo, model.Package{Name: "x"})
	if o.watchedDirs[filepath.Clean(home)] {
		t.Fatal("the home directory must never be snapshotted")
	}
	testsupport.WriteFile(t, filepath.Join(home, "notes.txt"), "edited by the user\n")
	for _, eff := range o.finish(context.Background(), model.EffectCreate) {
		if strings.HasSuffix(eff.Path, "notes.txt") {
			t.Errorf("an unwatched home file was reported: %+v", eff)
		}
	}
}

func TestObservationSeesWhatCargoWritesIntoItsBinDirectory(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	bin := filepath.Join(t.TempDir(), ".cargo", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	f.cargo.ExeDir = bin

	o := f.eng.observe(context.Background(), f.cargo, model.Package{Name: "rg"})
	testsupport.WriteExecutable(t, filepath.Join(bin, "rg"), "#!/bin/sh\n")
	effects := o.finish(context.Background(), model.EffectCreate)
	var found bool
	for _, eff := range effects {
		if eff.Path == filepath.Join(bin, "rg") {
			found = true
			if eff.Op != model.EffectCreate || eff.Evidence != model.EvidenceObserved {
				t.Errorf("expected an observed create, got %+v", eff)
			}
		}
	}
	if !found {
		t.Fatalf("a file cargo writes into its bin directory must be observed: %+v", effects)
	}
}

// cargoMove stages a move of name from incumbent to cargo, with cargo's bin
// directory at <tmp>/.cargo/bin so the locator attributes files there the way
// it does on a real machine. Operations are logged in order.
type cargoMove struct {
	bin string
	log []string
}

func (f *fixture) cargoMove(name string, incumbent *testsupport.FakeProvider) *cargoMove {
	f.t.Helper()
	root := f.t.TempDir()
	m := &cargoMove{bin: filepath.Join(root, ".cargo", "bin")}
	if err := os.MkdirAll(m.bin, 0o755); err != nil {
		f.t.Fatal(err)
	}
	f.cargo.ExeDir = m.bin
	f.cargo.InstallPath = filepath.Join(root, "cargo-install")
	for _, p := range []*testsupport.FakeProvider{incumbent, f.cargo} {
		p := p
		p.InstallHook = func(req provider.Request) {
			m.log = append(m.log, p.Name()+" install")
			if p == f.cargo {
				testsupport.WriteExecutable(f.t, filepath.Join(f.cargo.InstallPath, req.Package.Name), "#!/bin/sh\n")
			}
		}
		p.RemoveHook = func(model.Package) { m.log = append(m.log, p.Name()+" remove") }
	}
	f.withLocator(map[string]string{})
	return m
}

func TestTransitionIntoCargoIgnoresAThirdProvidersFileAtTheTarget(t *testing.T) {
	// ~/.cargo/bin/rustfmt is rustup's proxy. Moving rustfmt from Homebrew to
	// Cargo must not remove Homebrew's copy first on the theory that it is in
	// the way: it is not Homebrew's file, and removing it would not make room.
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"rustfmt\"\nprovider = \"cargo\"\n")
	m := f.cargoMove("rustfmt", f.brew)
	toolchain := filepath.Join(filepath.Dir(filepath.Dir(m.bin)), ".rustup", "toolchains", "stable", "bin")
	testsupport.WriteExecutable(t, filepath.Join(toolchain, "rustfmt"), "#!/bin/sh\n")
	if err := os.Symlink(filepath.Join(toolchain, "rustfmt"), filepath.Join(m.bin, "rustfmt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	f.brew.With(model.Installation{Name: "rustfmt", Version: "1.7.0", Path: "/brew/bin/rustfmt"})

	st := f.transitionState("rustfmt")
	tr := f.eng.TransitionFor(context.Background(), st, Options{})
	if shared, ok := f.eng.sharedExecutable(context.Background(), tr, st.Package, f.cargo); ok {
		t.Fatalf("rustup's proxy is not Homebrew's executable, but %s was treated as shared", shared)
	}
	_, _ = f.eng.ExecuteTransition(context.Background(), st, Options{NoConfirm: true})
	if len(m.log) == 0 || m.log[0] != "cargo install" {
		t.Errorf("the new copy must be installed before the old one is touched, got %v", m.log)
	}
}

func TestTransitionIntoCargoHandsOverAFileTheIncumbentOwns(t *testing.T) {
	// GOBIN and Cargo's install root can point at the same directory. A file
	// there attributed to the incumbent is the incumbent's, so the old copy has
	// to go first.
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"tool\"\nprovider = \"cargo\"\n")
	goP := testsupport.NewFakeProvider("go", provider.KindGo)
	f.reg.Register(goP)
	m := f.cargoMove("tool", goP)
	// Re-root cargo's bin into a go/bin directory, which the locator
	// attributes to go.
	gobin := filepath.Join(t.TempDir(), "go", "bin")
	f.cargo.ExeDir = gobin
	testsupport.WriteExecutable(t, filepath.Join(gobin, "tool"), "#!/bin/sh\n")
	goP.With(model.Installation{Name: "tool"}) // go cannot say where its copy is

	st := f.transitionState("tool")
	tr := f.eng.TransitionFor(context.Background(), st, Options{})
	shared, ok := f.eng.sharedExecutable(context.Background(), tr, st.Package, f.cargo)
	if !ok || shared != filepath.Join(gobin, "tool") {
		t.Fatalf("a file attributed to the incumbent must be treated as shared, got %q %v", shared, ok)
	}
	_, _ = f.eng.ExecuteTransition(context.Background(), st, Options{NoConfirm: true})
	if len(m.log) == 0 || m.log[0] != "go remove" {
		t.Errorf("a shared path is handed over old-first, got %v", m.log)
	}
}

func TestTransitionIntoCargoIgnoresAnOccupantWhenTheIncumbentLivesElsewhere(t *testing.T) {
	// The incumbent reports its copy in its own directory; an unattributed file
	// at Cargo's path is something else (a leftover, a hand-copied binary).
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"tool\"\nprovider = \"cargo\"\n")
	m := f.cargoMove("tool", f.brew)
	plain := t.TempDir()
	f.cargo.ExeDir = plain // a directory no attribution rule claims
	testsupport.WriteExecutable(t, filepath.Join(plain, "tool"), "#!/bin/sh\n")
	f.brew.With(model.Installation{Name: "tool", Version: "1.0", Path: "/brew/bin/tool"})

	st := f.transitionState("tool")
	tr := f.eng.TransitionFor(context.Background(), st, Options{})
	if shared, ok := f.eng.sharedExecutable(context.Background(), tr, st.Package, f.cargo); ok {
		t.Fatalf("the incumbent's copy is at /brew/bin/tool, so %s is not shared with it", shared)
	}
	_ = m
}

func TestTransitionIntoCargoAssumesSharedWhenNothingSaysOtherwise(t *testing.T) {
	// Nothing attributes the occupant and the incumbent cannot say where its
	// copy is: the conservative reading (hand over, restore on failure) holds.
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"tool\"\nprovider = \"cargo\"\n")
	_ = f.cargoMove("tool", f.brew)
	plain := t.TempDir()
	f.cargo.ExeDir = plain
	testsupport.WriteExecutable(t, filepath.Join(plain, "tool"), "#!/bin/sh\n")
	f.brew.With(model.Installation{Name: "tool", Version: "1.0"})

	st := f.transitionState("tool")
	tr := f.eng.TransitionFor(context.Background(), st, Options{})
	if _, ok := f.eng.sharedExecutable(context.Background(), tr, st.Package, f.cargo); !ok {
		t.Fatal("an unexplained occupant with an incumbent of unknown location must be treated as shared")
	}
}
