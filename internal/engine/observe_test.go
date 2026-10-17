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

// effectAt finds the filesystem effect at a path; package effects can carry
// the same path and are skipped.
func effectAt(effects []model.Effect, path string) *model.Effect {
	for i, e := range effects {
		if e.Path == path && e.Kind != model.EffectPackage {
			return &effects[i]
		}
	}
	return nil
}

// The observation reports what changed on disk between before and after,
// classified by what happened to each path.
func TestObservationClassifiesEveryChange(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	home := t.TempDir()
	f.eng = New(f.reg, nil, WithHome(home))
	bin := t.TempDir()
	f.uv.ExeDir = bin

	deleted := filepath.Join(bin, "gone")
	modified := filepath.Join(bin, "edited")
	replaced := filepath.Join(bin, "swapped")
	relinked := filepath.Join(bin, "link")
	created := filepath.Join(bin, "new")
	untouched := filepath.Join(bin, "same")
	for _, p := range []string{deleted, modified, replaced, untouched} {
		testsupport.WriteFile(t, p, "one\n")
	}
	if err := os.Symlink("/somewhere/uv/tools/a", relinked); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	profile := filepath.Join(home, ".bashrc")
	testsupport.WriteFile(t, profile, "# rc\n")

	obs := f.eng.observe(context.Background(), f.uv, model.Package{Name: "x"})
	_ = os.Remove(deleted)
	if err := os.WriteFile(modified, []byte("one and more\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(replaced)
	testsupport.WriteFile(t, replaced, "two\n")
	_ = os.Remove(relinked)
	_ = os.Symlink("/elsewhere/pip/b", relinked)
	testsupport.WriteFile(t, created, "new\n")
	if err := os.WriteFile(profile, []byte("# rc\nexport PATH=$HOME/.x/bin:$PATH\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	effects := obs.finish(context.Background(), model.EffectCreate)

	for _, tc := range []struct {
		path        string
		op          model.EffectOp
		kind        model.EffectKind
		destructive bool
	}{
		{deleted, model.EffectDelete, model.EffectFile, true},
		{modified, model.EffectModify, model.EffectFile, true},

		{relinked, model.EffectReplace, model.EffectSymlink, true},
		{created, model.EffectCreate, model.EffectFile, false},
		{profile, model.EffectModify, model.EffectShellProfile, false},
	} {
		e := effectAt(effects, tc.path)
		if e == nil {
			t.Errorf("%s: no effect reported in %+v", tc.path, effects)
			continue
		}
		if e.Op != tc.op || e.Kind != tc.kind || e.Destructive != tc.destructive {
			t.Errorf("%s: got %s %s destructive=%v, want %s %s destructive=%v",
				tc.path, e.Op, e.Kind, e.Destructive, tc.op, tc.kind, tc.destructive)
		}
		if e.Evidence != model.EvidenceObserved || e.Provider != "uv" {
			t.Errorf("%s: observed effects carry their evidence and provider: %+v", tc.path, e)
		}
	}
	if e := effectAt(effects, replaced); e == nil || !rewritten(e) {
		t.Errorf("a file deleted and written again is a destructive rewrite: %+v", e)
	}
	if e := effectAt(effects, relinked); e != nil && (e.From != "/somewhere/uv/tools/a" || e.To != "/elsewhere/pip/b") {
		t.Errorf("a relinked symlink reports both targets: %+v", e)
	}
	if effectAt(effects, untouched) != nil {
		t.Error("an untouched file is not an effect")
	}
	if !effectAt(effects, profile).Environment {
		t.Error("a shell profile change is environment-relevant")
	}
	ws := effectWarnings("x", effects)
	if len(ws) != 1 || ws[0].Code != model.WarnShellProfileChanged {
		t.Errorf("a changed shell profile is warned about: %+v", ws)
	}
}

// A path the provider reports in a place nobody watched is still listed, with
// evidence that says the prior state is unknown.
func TestObservationListsReportedPathsItCouldNotWatch(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	f.eng = New(f.reg, nil, WithHome(t.TempDir()))
	obs := f.eng.observe(context.Background(), f.brew, model.Package{Name: "x"})
	elsewhere := filepath.Join(t.TempDir(), "x")
	testsupport.WriteFile(t, elsewhere, "bin\n")
	effects := obs.finish(context.Background(), model.EffectCreate, &model.Installation{Name: "x", Path: elsewhere})
	e := effectAt(effects, elsewhere)
	if e == nil || e.Evidence != model.EvidenceReported || e.Op != model.EffectCreate {
		t.Fatalf("expected a reported create: %+v", effects)
	}
}

func TestRemoveRecordsObservedDeletionsAndTheConfigEdit(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n")
	f.eng = New(f.reg, nil, WithHome(t.TempDir()))
	bin := t.TempDir()
	exe := filepath.Join(bin, "ruff")
	testsupport.WriteFile(t, exe, "#!/bin/sh\n")
	f.uv.ExeDir = bin
	f.uv.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: exe})
	f.uv.RemoveHook = func(model.Package) { _ = os.Remove(exe) }

	preview, err := f.eng.RemovePreview(context.Background(), f.doc, "ruff")
	if err != nil {
		t.Fatal(err)
	}
	if e := effectAt(preview.Effects, exe); e == nil || e.Op != model.EffectDelete || e.Evidence != model.EvidencePlanned {
		t.Errorf("the preview names the file it would delete: %+v", preview.Effects)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatal("a preview must not delete anything")
	}

	res, err := f.eng.Remove(context.Background(), f.doc, "ruff", Options{})
	if err != nil {
		t.Fatal(err)
	}
	a := res.Applied[0]
	if a.Outcome != model.OutcomeApplied || !a.Destructive() {
		t.Errorf("expected an applied, destructive removal: %+v", a)
	}
	if e := effectAt(a.Effects, exe); e == nil || e.Evidence != model.EvidenceObserved {
		t.Errorf("the deletion is observed, replacing the prediction: %+v", a.Effects)
	}
	if len(a.ConfigChanges) != 1 || !a.ConfigChanges[0].Applied {
		t.Errorf("the config edit is recorded as applied: %+v", a.ConfigChanges)
	}
}

func TestFailedInstallStillReportsWhatItLeftBehind(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	f.eng = New(f.reg, nil, WithHome(t.TempDir()))
	bin := t.TempDir()
	f.uv.ExeDir = bin
	f.uv.InstallGuard = func(req provider.Request) error {
		testsupport.WriteFile(t, filepath.Join(bin, "half"), "partial\n")
		return &provider.Error{Provider: "uv", Kind: provider.ErrFailed, Message: "build failed"}
	}
	res, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "half", Provider: "uv"}, Options{})
	if err == nil {
		t.Fatal("the failure is returned")
	}
	if res == nil || len(res.Applied) != 1 {
		t.Fatalf("a failed install still reports an action: %+v", res)
	}
	a := res.Applied[0]
	if a.Outcome != model.OutcomeFailed || !strings.Contains(a.Error, "build failed") {
		t.Errorf("the action is failed with its reason: %+v", a)
	}
	if effectAt(a.Effects, filepath.Join(bin, "half")) == nil {
		t.Errorf("the file the failed install wrote is reported: %+v", a.Effects)
	}
	if len(a.ConfigChanges) != 0 {
		t.Errorf("nothing was recorded, so no config change: %+v", a.ConfigChanges)
	}
}

// The uv -> pip move in a shared ~/.local/bin: the plan says the shared file
// is replaced and warns, and the executed move reports the replacement it saw.
func TestTransitionEffectsForTheSharedExecutable(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"cowsay\"\nprovider = \"pip\"\n")
	sb := f.sharedBin("cowsay")
	f.eng.home = t.TempDir()
	f.uv.With(model.Installation{Name: "cowsay", Version: "6.0", Path: sb.exe})
	testsupport.WriteFile(t, sb.exe, "#!/bin/sh\n# uv\n")

	planned, err := f.eng.ExecuteTransition(context.Background(), f.transitionState("cowsay"), Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if e := effectAt(planned.Effects, sb.exe); e == nil || e.Op != model.EffectReplace || !e.Destructive || e.Evidence != model.EvidencePlanned {
		t.Errorf("the plan says the shared file is replaced: %+v", planned.Effects)
	}
	if len(planned.Warnings) != 1 || planned.Warnings[0].Code != model.WarnSharedExecutable {
		t.Errorf("the plan warns about the shared path: %+v", planned.Warnings)
	}

	done, err := f.eng.ExecuteTransition(context.Background(), f.transitionState("cowsay"), Options{NoConfirm: true})
	if err != nil {
		t.Fatal(err)
	}
	// Deleted and rewritten: a replacement, or a modification when the
	// filesystem reuses the inode (ext4 does). Destructive either way.
	e := effectAt(done.Effects, sb.exe)
	if e == nil || !rewritten(e) || e.Evidence != model.EvidenceObserved {
		t.Errorf("the executed move reports the rewrite it observed: %+v", done.Effects)
	}
	var uninstalled, installed bool
	for _, eff := range done.Effects {
		if eff.Kind == model.EffectPackage && eff.Op == model.EffectDelete && eff.Provider == "uv" {
			uninstalled = true
		}
		if eff.Kind == model.EffectPackage && eff.Op == model.EffectCreate && eff.Provider == "pip" {
			installed = true
		}
	}
	if !uninstalled || !installed {
		t.Errorf("the move uninstalls from uv and installs into pip: %+v", done.Effects)
	}
}

// When pip cannot take over, the uv copy is restored: the net effect on the
// package is nothing, and no uninstall is reported.
func TestTransitionRestoredHandOverReportsNoUninstall(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"cowsay\"\nprovider = \"pip\"\n")
	sb := f.sharedBin("cowsay")
	f.eng.home = t.TempDir()
	f.uv.With(model.Installation{Name: "cowsay", Version: "6.0", Path: sb.exe})
	testsupport.WriteFile(t, sb.exe, "#!/bin/sh\n# uv\n")
	sb.pip.InstallError = &provider.Error{Provider: "pip", Kind: provider.ErrNetwork, Message: "offline"}

	done, _ := f.eng.ExecuteTransition(context.Background(), f.transitionState("cowsay"), Options{NoConfirm: true})
	for _, eff := range done.Effects {
		if eff.Kind == model.EffectPackage && eff.Op == model.EffectDelete {
			t.Errorf("the uv copy was restored, so it was not uninstalled: %+v", done.Effects)
		}
	}
	if e := effectAt(done.Effects, sb.exe); e == nil || !rewritten(e) {
		t.Errorf("the shared file was still rewritten, and that is reported: %+v", done.Effects)
	}
}

// rewritten reports a destructive replacement or in-place rewrite.
func rewritten(e *model.Effect) bool {
	return (e.Op == model.EffectReplace || e.Op == model.EffectModify) && e.Destructive
}
