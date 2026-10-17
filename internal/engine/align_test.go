package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hxmbl/starpoint/internal/align"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// An elevated run's ownership and permission fixes are part of the action that
// caused them: each one is an effect, and a failed one is a warning.

func skipUnlessAligning(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("alignment runs on Linux and macOS")
	}
}

func TestInstallReportsOwnershipFixesUnderTheTargetHome(t *testing.T) {
	skipUnlessAligning(t)
	f := newFixture(t, "schema = 1\n")
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	f.uv.ExeDir = bin
	f.uv.InstallPath = bin
	f.uv.InstallHook = func(req provider.Request) {
		testsupport.WriteExecutable(t, filepath.Join(bin, req.Package.Name), "#!/bin/sh\n")
	}
	var given []string
	f.eng = New(f.reg, nil, WithHome(home), WithAlignment(&align.Policy{
		Home: home, Owner: &align.Owner{UID: 4242, GID: 4242}, User: "alice",
		FromUID: os.Getuid(), Since: time.Now().Add(-time.Minute),
		Chown: func(path string, uid, gid int) error { given = append(given, path); return nil },
	}))

	res, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "ruff", Provider: "uv"}, Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	exe := filepath.Join(bin, "ruff")
	var fix *model.Effect
	for _, a := range res.Applied {
		for _, eff := range a.Effects {
			eff := eff
			if eff.Op == model.EffectChown && eff.Path == exe {
				fix = &eff
			}
		}
	}
	if fix == nil {
		t.Fatalf("the chown of the new executable must be an effect of the install: %+v", res.Applied)
	}
	if fix.Provider != "uv" || fix.Evidence != model.EvidenceObserved || fix.To != "4242:4242" || fix.Destructive {
		t.Errorf("wrong effect: %+v", fix)
	}
	var sawExe bool
	for _, p := range given {
		if p == exe {
			sawExe = true
		}
	}
	if !sawExe {
		t.Errorf("the executable was not given to the user: %v", given)
	}
}

func TestInstallWidensWhatItCreatesInASharedLocation(t *testing.T) {
	skipUnlessAligning(t)
	f := newFixture(t, "schema = 1\n")
	home := t.TempDir()
	shared := filepath.Join(t.TempDir(), "usr", "local", "bin")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	f.uv.ExeDir = shared
	f.uv.InstallPath = shared
	f.uv.InstallHook = func(req provider.Request) {
		path := filepath.Join(shared, req.Package.Name)
		testsupport.WriteFile(t, path, "#!/bin/sh\n")
		_ = os.Chmod(path, 0o700) // what umask 077 leaves
	}
	f.eng = New(f.reg, nil, WithHome(home), WithAlignment(&align.Policy{
		Home: home, FromUID: os.Getuid(), Since: time.Now().Add(-time.Minute),
	}))

	res, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "tool", Provider: "uv"}, Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	exe := filepath.Join(shared, "tool")
	info, err := os.Stat(exe)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Fatalf("the executable should be 0755 now: %v %v", info.Mode(), err)
	}
	var found bool
	for _, a := range res.Applied {
		for _, eff := range a.Effects {
			if eff.Op == model.EffectChmod && eff.Path == exe && eff.From == "0700" && eff.To == "0755" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("the chmod must be an effect of the install: %+v", res.Applied)
	}
}

func TestAFailedOwnershipFixRaisesAWarning(t *testing.T) {
	skipUnlessAligning(t)
	f := newFixture(t, "schema = 1\n")
	home := t.TempDir()
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	f.uv.ExeDir = bin
	f.uv.InstallPath = bin
	f.uv.InstallHook = func(req provider.Request) {
		testsupport.WriteExecutable(t, filepath.Join(bin, req.Package.Name), "#!/bin/sh\n")
	}
	f.eng = New(f.reg, nil, WithHome(home), WithAlignment(&align.Policy{
		Home: home, Owner: &align.Owner{UID: 4242, GID: 4242}, FromUID: os.Getuid(), Since: time.Now().Add(-time.Minute),
		Chown: func(string, int, int) error { return errors.New("operation not permitted") },
	}))
	res, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "ruff", Provider: "uv"}, Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("a failed fix does not fail the install: %v", err)
	}
	for _, a := range res.Applied {
		for _, w := range a.Warnings {
			if w.Code == model.WarnPermissionFixFailed {
				return
			}
		}
	}
	t.Fatalf("a failed fix must raise %s: %+v", model.WarnPermissionFixFailed, res.Applied)
}

// droppedRunAs says every provider ran as alice with privileges dropped, as
// the CLI's decisions do for user-level providers under sudo.
func droppedRunAs(ctx context.Context, p provider.Provider) model.RunAs {
	return model.RunAs{Provider: p.Name(), User: "alice", UID: 4242, GID: 4242, Elevated: true, Dropped: true, Privilege: "user"}
}

func TestRootOwnedFilesAfterADroppedRunAreFixedAndWarnedAbout(t *testing.T) {
	skipUnlessAligning(t)
	f := newFixture(t, "schema = 1\n")
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	f.uv.ExeDir = bin
	f.uv.InstallPath = bin
	// The fake writes as the suite's own uid, which the policy treats as
	// "root": exactly what a provider that escalated on its own would leave.
	f.uv.InstallHook = func(req provider.Request) {
		testsupport.WriteExecutable(t, filepath.Join(bin, req.Package.Name), "#!/bin/sh\n")
	}
	var given []string
	f.eng = New(f.reg, nil, WithHome(home), WithRunAs(droppedRunAs), WithAlignment(&align.Policy{
		Home: home, Owner: &align.Owner{UID: 4242, GID: 4242}, User: "alice",
		FromUID: os.Getuid(), Since: time.Now().Add(-time.Minute),
		Chown: func(path string, uid, gid int) error { given = append(given, path); return nil },
	}))
	res, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "ruff", Provider: "uv"}, Options{NoConfirm: true})
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(bin, "ruff")
	var fix *model.Effect
	var warned bool
	for _, a := range res.Applied {
		for _, eff := range a.Effects {
			eff := eff
			if eff.Op == model.EffectChown && eff.Path == exe {
				fix = &eff
			}
		}
		for _, w := range a.Warnings {
			if w.Code == model.WarnRootOwnedAfterDrop && strings.Contains(w.Message, exe) {
				warned = true
			}
		}
	}
	if fix == nil || !fix.Unexpected || !strings.Contains(fix.Detail, "unexpected") {
		t.Fatalf("the fix is still made and reported, marked unexpected: %+v", fix)
	}
	if !warned {
		t.Errorf("an unexpected fix raises %s: %+v", model.WarnRootOwnedAfterDrop, res.Applied)
	}
	if len(given) == 0 {
		t.Error("the file is still given to the user")
	}
}

func TestADroppedRunLeavesSharedModesAlone(t *testing.T) {
	skipUnlessAligning(t)
	f := newFixture(t, "schema = 1\n")
	home := t.TempDir()
	shared := filepath.Join(t.TempDir(), "opt", "homebrew", "bin")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	f.uv.ExeDir = shared
	f.uv.InstallPath = shared
	f.uv.InstallHook = func(req provider.Request) {
		path := filepath.Join(shared, req.Package.Name)
		testsupport.WriteFile(t, path, "#!/bin/sh\n")
		_ = os.Chmod(path, 0o700) // the user's own umask
	}
	f.eng = New(f.reg, nil, WithHome(home), WithRunAs(droppedRunAs), WithAlignment(&align.Policy{
		Home: home, Owner: &align.Owner{UID: 4242, GID: 4242}, FromUID: os.Getuid(), Since: time.Now().Add(-time.Minute),
		Chown: func(string, int, int) error { return nil },
	}))
	res, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "tool", Provider: "uv"}, Options{NoConfirm: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range res.Applied {
		for _, eff := range a.Effects {
			if eff.Op == model.EffectChmod {
				t.Errorf("what the user wrote as themselves keeps the user's modes: %+v", eff)
			}
		}
	}
	if info, _ := os.Stat(filepath.Join(shared, "tool")); info.Mode().Perm() != 0o700 {
		t.Errorf("mode changed to %04o", info.Mode().Perm())
	}
}

func TestARootRunWidensThePackageDirectoryItNamed(t *testing.T) {
	skipUnlessAligning(t)
	f := newFixture(t, "schema = 1\n")
	home := t.TempDir()
	prefix := filepath.Join(t.TempDir(), "usr")
	modules := filepath.Join(prefix, "lib", "node_modules")
	if err := os.MkdirAll(modules, 0o755); err != nil {
		t.Fatal(err)
	}
	npmP, _ := f.reg.Get("npm")
	fake := npmP.(*testsupport.FakeProvider)
	fake.InstallDirsFor = func(pkg model.Package) []string { return []string{filepath.Join(modules, pkg.Name)} }
	fake.InstallHook = func(req provider.Request) {
		dir := filepath.Join(modules, req.Package.Name)
		if err := os.MkdirAll(filepath.Join(dir, "lib"), 0o700); err != nil {
			t.Fatal(err)
		}
		testsupport.WriteFile(t, filepath.Join(dir, "lib", "index.js"), "module.exports = 1\n")
		_ = os.Chmod(filepath.Join(dir, "lib", "index.js"), 0o600)
		_ = os.Chmod(dir, 0o700)
	}
	f.eng = New(f.reg, nil, WithHome(home), WithAlignment(&align.Policy{
		Home: home, FromUID: os.Getuid(), Since: time.Now().Add(-time.Minute),
	}))
	res, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "prettier", Provider: "npm"}, Options{NoConfirm: true})
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(modules, "prettier")
	for path, want := range map[string]os.FileMode{dir: 0o755, filepath.Join(dir, "lib"): 0o755, filepath.Join(dir, "lib", "index.js"): 0o644} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v, want %04o", path, info.Mode().Perm(), want)
		}
	}
	var found bool
	for _, a := range res.Applied {
		for _, eff := range a.Effects {
			if eff.Op == model.EffectChmod && eff.Path == dir {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("widening the package directory is an effect: %+v", res.Applied)
	}
}
