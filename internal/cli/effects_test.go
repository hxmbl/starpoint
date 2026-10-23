package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/elevation"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// operation is the response every changing command shares.
type operation struct {
	SchemaVersion int             `json:"schema_version"`
	Operation     string          `json:"operation"`
	DryRun        bool            `json:"dry_run"`
	ConfigChanged bool            `json:"config_changed"`
	ConfigPath    string          `json:"config_path"`
	Actions       []actionJSON    `json:"actions"`
	Warnings      []model.Warning `json:"warnings"`
	Summary       struct {
		Actions             int            `json:"actions"`
		ByAction            map[string]int `json:"by_action"`
		ByResult            map[string]int `json:"by_result"`
		Effects             int            `json:"effects"`
		Destructive         bool           `json:"destructive"`
		DestructiveEffects  int            `json:"destructive_effects"`
		DestructivePackages []string       `json:"destructive_packages"`
		Warnings            map[string]int `json:"warnings"`
		ConfigFiles         []string       `json:"config_files"`
	} `json:"summary"`
}

type actionJSON struct {
	Action        string                   `json:"action"`
	Package       string                   `json:"package"`
	Provider      string                   `json:"provider"`
	Result        string                   `json:"result"`
	DryRun        bool                     `json:"dry_run"`
	Destructive   bool                     `json:"destructive"`
	Error         string                   `json:"error"`
	Transitions   []model.ActionTransition `json:"transitions"`
	ConfigChanges []model.ConfigChange     `json:"config_changes"`
	Effects       []model.Effect           `json:"effects"`
	Warnings      []model.Warning          `json:"warnings"`
	RunAs         []model.RunAs            `json:"run_as"`
}

func decodeOperation(t *testing.T, out string) operation {
	t.Helper()
	var op operation
	if err := json.Unmarshal([]byte(out), &op); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, out)
	}
	return op
}

func findEffect(effects []model.Effect, op model.EffectOp, kind model.EffectKind, path string) *model.Effect {
	for i, e := range effects {
		if e.Op == op && e.Kind == kind && (path == "" || e.Path == path) {
			return &effects[i]
		}
	}
	return nil
}

func hasCode(ws []model.Warning, code string) bool {
	for _, w := range ws {
		if w.Code == code {
			return true
		}
	}
	return false
}

// binHarness gives uv a real executable directory: installing writes the
// command there, removing deletes it.
func binHarness(t *testing.T, config string) (*harness, string) {
	t.Helper()
	h := newHarness(t, config)
	// uv's executable directory is ~/.local/bin, inside the user's home:
	// prune only ever removes software found there.
	bin := filepath.Join(h.home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	h.uv.ExeDir = bin
	h.uv.InstallPath = bin
	h.uv.InstallHook = func(req provider.Request) {
		testsupport.WriteFile(t, filepath.Join(bin, req.Package.Name), "#!/bin/sh\n# uv\n")
	}
	h.uv.RemoveHook = func(pkg model.Package) { _ = os.Remove(filepath.Join(bin, pkg.Name)) }
	return h, bin
}

const ruffUV = "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n"

func TestRemoveJSONListsEveryDeletedPathAndMarksItDestructive(t *testing.T) {
	h, bin := binHarness(t, ruffUV)
	exe := filepath.Join(bin, "ruff")
	testsupport.WriteFile(t, exe, "#!/bin/sh\n")
	if err := os.Chmod(exe, 0o755); err != nil {
		t.Fatal(err)
	}
	h.uv.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: exe})

	op := decodeOperation(t, h.mustRun("remove", "ruff", "--yes", "--allow-destructive", "--json"))
	if op.Operation != "remove" || op.DryRun || !op.ConfigChanged || op.ConfigPath != h.config {
		t.Errorf("wrong top level: %+v", op)
	}
	if len(op.Actions) != 1 {
		t.Fatalf("expected one action: %+v", op.Actions)
	}
	a := op.Actions[0]
	if a.Result != "applied" || !a.Destructive {
		t.Errorf("a completed removal is applied and destructive: %+v", a)
	}
	file := findEffect(a.Effects, model.EffectDelete, model.EffectExecutable, exe)
	if file == nil {
		t.Fatalf("the deleted executable must be listed: %+v", a.Effects)
	}
	if !file.Destructive || file.Evidence != model.EvidenceObserved || file.Provider != "uv" {
		t.Errorf("the deletion was observed, by uv, and is destructive: %+v", file)
	}
	pkg := findEffect(a.Effects, model.EffectDelete, model.EffectPackage, "")
	if pkg == nil || pkg.Name != "ruff" || pkg.From != "0.5.0" || !pkg.Destructive {
		t.Errorf("the uninstalled package must be listed with its version: %+v", pkg)
	}
	if len(a.ConfigChanges) != 1 || a.ConfigChanges[0].Change != model.ConfigRemove ||
		!a.ConfigChanges[0].Destructive || !a.ConfigChanges[0].Applied || a.ConfigChanges[0].Path != h.config {
		t.Errorf("dropping the entry is a destructive, applied config change: %+v", a.ConfigChanges)
	}
	if findEffect(a.Effects, model.EffectModify, model.EffectConfig, h.config) == nil {
		t.Errorf("the configuration file is a path that changed: %+v", a.Effects)
	}
	if !op.Summary.Destructive || strings.Join(op.Summary.DestructivePackages, ",") != "ruff" ||
		op.Summary.DestructiveEffects < 2 || op.Summary.ByResult["applied"] != 1 {
		t.Errorf("the summary must aggregate the destructive action: %+v", op.Summary)
	}
	if op.Warnings == nil || a.Warnings == nil || a.Transitions == nil {
		t.Error("collections are arrays, never null")
	}
}

func TestRemoveDryRunListsTheFullEffectListAndTouchesNothing(t *testing.T) {
	h, bin := binHarness(t, ruffUV)
	exe := filepath.Join(bin, "ruff")
	testsupport.WriteFile(t, exe, "#!/bin/sh\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: exe})

	op := decodeOperation(t, h.mustRun("remove", "ruff", "--dry-run", "--json"))
	if !op.DryRun || op.ConfigChanged {
		t.Errorf("a dry run changes nothing: %+v", op)
	}
	a := op.Actions[0]
	if a.Result != "planned" || !a.DryRun || !a.Destructive {
		t.Errorf("a planned removal is still destructive: %+v", a)
	}
	file := findEffect(a.Effects, model.EffectDelete, model.EffectFile, exe)
	if file == nil || file.Evidence != model.EvidencePlanned {
		t.Fatalf("the dry run must name the file it would delete: %+v", a.Effects)
	}
	if a.ConfigChanges[0].Applied {
		t.Error("a dry run applies no configuration change")
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatal("a dry run must not delete anything")
	}
	if !strings.Contains(h.configBody(), "ruff") {
		t.Fatal("a dry run must not edit the configuration")
	}

	out := h.mustRun("remove", "ruff", "--dry-run")
	for _, want := range []string{"would remove ruff via uv", "would uninstall ruff 0.5.0 (uv)", "would delete file " + exe, "would edit"} {
		if !strings.Contains(out, want) {
			t.Errorf("human dry run should say %q:\n%s", want, out)
		}
	}
}

func TestRemoveConfirmationListsWhatWillBeDeletedBeforeAsking(t *testing.T) {
	h, bin := binHarness(t, ruffUV)
	exe := filepath.Join(bin, "ruff")
	testsupport.WriteFile(t, exe, "#!/bin/sh\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: exe})
	h.ask(false)

	out := h.mustRun("remove", "ruff")
	confirm := out[:strings.Index(out, "Nothing was changed.")]
	for _, want := range []string{"! would delete file " + exe, "! would uninstall ruff 0.5.0", "remove or overwrite something"} {
		if !strings.Contains(confirm, want) {
			t.Errorf("the question must be preceded by %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatal("declining must leave the file alone")
	}
}

func TestRemoveHumanOutputShowsDestructiveEffectsAndVerboseShowsAll(t *testing.T) {
	h, bin := binHarness(t, ruffUV)
	exe := filepath.Join(bin, "ruff")
	testsupport.WriteFile(t, exe, "#!/bin/sh\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: exe})

	out := h.mustRun("remove", "ruff", "--yes", "--allow-destructive")
	if !strings.Contains(out, "! deleted file "+exe) {
		t.Errorf("a deletion is always shown:\n%s", out)
	}
	if !strings.Contains(out, "! uninstalled ruff 0.5.0 (uv)") {
		t.Errorf("the uninstall is always shown:\n%s", out)
	}

	h2, bin2 := binHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
	_ = bin2
	out = h2.mustRun("install", "ruff", "--provider", "uv", "--yes")
	if strings.Contains(out, "created file") {
		t.Errorf("non-destructive effects are counted, not listed, without --verbose:\n%s", out)
	}
	if !strings.Contains(out, "--verbose lists them") {
		t.Errorf("hidden effects are counted:\n%s", out)
	}
	h3, bin3 := binHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
	out = h3.mustRun("install", "ruff", "--provider", "uv", "--yes", "--verbose")
	if !strings.Contains(out, "created file "+filepath.Join(bin3, "ruff")) {
		t.Errorf("--verbose lists every effect:\n%s", out)
	}
}

func TestInstallJSONCarriesPerPackageWarningsAndConfigChanges(t *testing.T) {
	h, bin := binHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
	op := decodeOperation(t, h.mustRun("install", "ruff", "--provider", "uv", "--yes", "--json"))
	if op.Operation != "install" || op.DryRun || !op.ConfigChanged {
		t.Errorf("install must report dry_run and config_changed again: %+v", op)
	}
	a := op.Actions[0]
	// The executable directory is a temporary one, not on PATH.
	if !hasCode(a.Warnings, model.WarnNotOnPath) {
		t.Errorf("the not-on-PATH warning belongs to the package: %+v", a.Warnings)
	}
	if !hasCode(op.Warnings, model.WarnNotOnPath) {
		t.Errorf("every action warning also appears at the top level: %+v", op.Warnings)
	}
	if findEffect(a.Effects, model.EffectCreate, model.EffectFile, filepath.Join(bin, "ruff")) == nil {
		t.Errorf("the created command is listed: %+v", a.Effects)
	}
	if a.Destructive {
		t.Errorf("a fresh install removes nothing: %+v", a.Effects)
	}
	if len(a.ConfigChanges) != 1 || a.ConfigChanges[0].Change != model.ConfigAdd || a.ConfigChanges[0].Destructive {
		t.Errorf("recording a new entry is a non-destructive add: %+v", a.ConfigChanges)
	}
	fields := map[string]string{}
	for _, f := range a.ConfigChanges[0].Fields {
		fields[f.Field] = f.To
	}
	if fields["provider"] != "uv" {
		t.Errorf("the config change says what was written: %+v", a.ConfigChanges[0].Fields)
	}
}

func TestInstallReportsAnUnavailableProviderAsAStructuredWarning(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	op := decodeOperation(t, h.mustRun("install", "htop", "--provider", "apt", "--yes", "--json"))
	a := op.Actions[0]
	var found *model.Warning
	for i, w := range a.Warnings {
		if w.Code == model.WarnRecordedNotInstalled {
			found = &a.Warnings[i]
		}
	}
	if found == nil || found.Severity != model.SeverityError || found.Package != "htop" || found.Provider != "apt" {
		t.Fatalf("expected a coded, per-package error: %+v", a.Warnings)
	}
	if op.Summary.Warnings["error"] != 1 {
		t.Errorf("the summary counts warnings by severity: %+v", op.Summary.Warnings)
	}
	if findEffect(a.Effects, model.EffectCreate, model.EffectPackage, "") != nil {
		t.Errorf("nothing was installed, so no package effect: %+v", a.Effects)
	}

	out := h.mustRun("install", "tree", "--provider", "apt", "--yes")
	if !strings.Contains(out, "error [recorded-not-installed]:") {
		t.Errorf("human output shows the warning under its package:\n%s", out)
	}
}

func TestInstallChangingTheProviderReportsTheTransition(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"brew\"\nversion = \"0.5.0\"\n\n[[package]]\nname = \"jq\"\n")
	op := decodeOperation(t, h.mustRun("install", "ruff@0.6.0", "--provider", "uv", "--yes", "--allow-destructive", "--json"))
	a := op.Actions[0]
	var provider, version *model.ActionTransition
	for i, tr := range a.Transitions {
		switch tr.Kind {
		case model.ChangeProvider:
			provider = &a.Transitions[i]
		case model.ChangeVersion:
			version = &a.Transitions[i]
		}
	}
	if provider == nil || provider.From != "brew" || provider.To != "uv" {
		t.Errorf("expected a brew -> uv transition: %+v", a.Transitions)
	}
	if version == nil || version.From != "0.5.0" || version.To != "0.6.0" {
		t.Errorf("expected a 0.5.0 -> 0.6.0 transition: %+v", a.Transitions)
	}
	cc := a.ConfigChanges[0]
	if cc.Change != model.ConfigUpdate || !cc.Destructive {
		t.Errorf("overwriting a recorded provider and pin is a destructive update: %+v", cc)
	}
}

func TestPruneReportsEachDeletedPathInJSONAndBeforeAsking(t *testing.T) {
	h, bin := binHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
	exe := filepath.Join(bin, "orphan")
	testsupport.WriteFile(t, exe, "#!/bin/sh\n")
	h.uv.With(model.Installation{Name: "orphan", Version: "1.0", Path: exe})

	op := decodeOperation(t, h.mustRun("prune", "--dry-run", "--json"))
	if len(op.Actions) != 1 || !op.Actions[0].Destructive ||
		findEffect(op.Actions[0].Effects, model.EffectDelete, model.EffectFile, exe) == nil {
		t.Fatalf("the dry run must list the file prune would delete: %+v", op.Actions)
	}

	h.ask(false)
	out := h.mustRun("prune")
	if !strings.Contains(out, "orphan via uv") || !strings.Contains(out, "! would delete file "+exe) {
		t.Errorf("each candidate and its files are listed before the question:\n%s", out)
	}

	h.ask(true)
	op = decodeOperation(t, h.mustRun("prune", "--json"))
	a := op.Actions[0]
	if a.Result != "applied" || findEffect(a.Effects, model.EffectDelete, model.EffectFile, exe) == nil {
		t.Errorf("the real run reports the observed deletion: %+v", a)
	}
}

func TestPruneReportsFailuresAsFailedActions(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
	h.uv.With(model.Installation{Name: "orphan", Version: "1.0"})
	h.uv.RemoveError = &provider.Error{Provider: "uv", Kind: provider.ErrFailed, Message: "boom"}

	code, out, _ := h.run("prune", "--yes", "--allow-destructive", "--json")
	if code == 0 {
		t.Fatal("a failed removal must fail the command")
	}
	op := decodeOperation(t, out)
	if len(op.Actions) != 1 || op.Actions[0].Result != "failed" || !strings.Contains(op.Actions[0].Error, "boom") {
		t.Fatalf("the failure is reported on the action, not swallowed: %+v", op.Actions)
	}
}

func TestUpdateReportsVersionTransitionsAndPinRewrites(t *testing.T) {
	h := pinnedHarness(t)
	h.ask(true)
	op := decodeOperation(t, h.mustRun("update", "--json"))
	var a *actionJSON
	for i := range op.Actions {
		if op.Actions[i].Package == "ruff" {
			a = &op.Actions[i]
		}
	}
	if a == nil {
		t.Fatalf("expected an update of ruff: %+v", op.Actions)
	}
	if len(a.Transitions) != 1 || a.Transitions[0].Kind != model.ChangeVersion || a.Transitions[0].From != "0.5.0" {
		t.Errorf("expected a version transition from the pin: %+v", a.Transitions)
	}
	if len(a.ConfigChanges) != 1 || !a.ConfigChanges[0].Destructive || a.ConfigChanges[0].Fields[0].Field != "version" {
		t.Errorf("rewriting a pin is a destructive config change: %+v", a.ConfigChanges)
	}
}

func TestUpdateSkipsCarryTheirPackageAndCode(t *testing.T) {
	h := pinnedHarness(t)
	op := decodeOperation(t, h.mustRun("update", "--no-confirm", "--json"))
	if !hasCode(op.Warnings, model.WarnPinConflict) {
		t.Fatalf("a kept pin with something newer is a coded warning: %+v", op.Warnings)
	}
	for _, w := range op.Warnings {
		if w.Code == model.WarnPinConflict && w.Package != "ruff" {
			t.Errorf("the warning names its package: %+v", w)
		}
	}
}

// sharedPip registers a pip that writes the same directory as uv, the way
// pip --user and uv share ~/.local/bin on Linux.
func sharedPip(t *testing.T, h *harness, bin string) *testsupport.FakeProvider {
	t.Helper()
	pip := testsupport.NewFakeProvider("pip", provider.KindPython)
	pip.ExeDir = bin
	pip.InstallPath = bin
	pip.InstallHook = func(req provider.Request) {
		testsupport.WriteFile(t, filepath.Join(bin, req.Package.Name), "#!/bin/sh\n# pip\n")
	}
	pip.RemoveHook = func(pkg model.Package) { _ = os.Remove(filepath.Join(bin, pkg.Name)) }
	h.env.Registry.Register(pip)
	h.uv.InstallGuard = func(req provider.Request) error {
		if _, err := os.Stat(filepath.Join(bin, req.Package.Name)); err == nil {
			return &provider.Error{Provider: "uv", Kind: provider.ErrConflict, Message: "Executable already exists"}
		}
		return nil
	}
	return pip
}

// The move itself is transition's (sync never moves software); its plan and
// its run report the shared-bin replacement.
func TestTransitionUvToPipInTheSharedBinReportsTheReplacement(t *testing.T) {
	h, bin := binHarness(t, "schema = 1\n\n[[package]]\nname = \"cowsay\"\nprovider = \"pip\"\n\n[[package]]\nname = \"jq\"\n")
	sharedPip(t, h, bin)
	exe := filepath.Join(bin, "cowsay")
	testsupport.WriteFile(t, exe, "#!/bin/sh\n# uv\n")
	h.uv.With(model.Installation{Name: "cowsay", Version: "6.0", Path: exe})

	op := decodeOperation(t, h.mustRun("transition", "cowsay", "--dry-run", "--json"))
	var a *actionJSON
	for i := range op.Actions {
		if op.Actions[i].Package == "cowsay" {
			a = &op.Actions[i]
		}
	}
	if a == nil || a.Action != "relocate" {
		t.Fatalf("expected a relocation of cowsay: %+v", op.Actions)
	}
	if len(a.Transitions) != 1 || a.Transitions[0].From != "uv" || a.Transitions[0].To != "pip" {
		t.Errorf("expected a uv -> pip transition: %+v", a.Transitions)
	}
	if !hasCode(a.Warnings, model.WarnSharedExecutable) {
		t.Errorf("the shared ~/.local/bin hand-over is a warning on the action: %+v", a.Warnings)
	}
	replace := findEffect(a.Effects, model.EffectReplace, "", exe)
	if replace == nil {
		for _, e := range a.Effects {
			if e.Op == model.EffectReplace && e.Path == exe {
				replace = &e
			}
		}
	}
	if replace == nil || !replace.Destructive || replace.Evidence != model.EvidencePlanned {
		t.Errorf("the plan must say the shared executable is replaced: %+v", a.Effects)
	}
	if findEffect(a.Effects, model.EffectDelete, model.EffectPackage, "") == nil {
		t.Errorf("the plan must say the uv copy is uninstalled: %+v", a.Effects)
	}

	op = decodeOperation(t, h.mustRun("transition", "cowsay", "--no-confirm", "--allow-destructive", "--json"))
	for i := range op.Actions {
		if op.Actions[i].Package == "cowsay" {
			a = &op.Actions[i]
		}
	}
	if a.Result != "applied" || !a.Destructive {
		t.Fatalf("the move ran and replaced a file: %+v", a)
	}
	var observed *model.Effect
	for i, e := range a.Effects {
		if e.Path == exe && e.Evidence == model.EvidenceObserved {
			observed = &a.Effects[i]
		}
	}
	// Deleted and rewritten: a replacement, or a modification when the
	// filesystem hands the new file the old inode. Destructive either way.
	if observed == nil || (observed.Op != model.EffectReplace && observed.Op != model.EffectModify) || !observed.Destructive {
		t.Errorf("the observed effect on the shared path is a destructive rewrite: %+v", a.Effects)
	}
	pkgDelete := findEffect(a.Effects, model.EffectDelete, model.EffectPackage, "")
	pkgCreate := findEffect(a.Effects, model.EffectCreate, model.EffectPackage, "")
	if pkgDelete == nil || pkgDelete.Provider != "uv" || pkgCreate == nil || pkgCreate.Provider != "pip" {
		t.Errorf("the move uninstalls from uv and installs into pip: %+v", a.Effects)
	}
	if !strings.Contains(testsupport.ReadFile(t, exe), "pip") {
		t.Fatal("pip's copy should now be at the shared path")
	}
}

func TestTransitionJSONReportsActionsWithEffects(t *testing.T) {
	h, bin := binHarness(t, "schema = 1\n\n[[package]]\nname = \"cowsay\"\nprovider = \"pip\"\n\n[[package]]\nname = \"jq\"\n")
	sharedPip(t, h, bin)
	exe := filepath.Join(bin, "cowsay")
	testsupport.WriteFile(t, exe, "#!/bin/sh\n# uv\n")
	h.uv.With(model.Installation{Name: "cowsay", Version: "6.0", Path: exe})

	op := decodeOperation(t, h.mustRun("transition", "cowsay", "--no-confirm", "--allow-destructive", "--json"))
	if op.Operation != "transition" || len(op.Actions) != 1 {
		t.Fatalf("transition reports actions like every changing command: %s", h.stdout.String())
	}
	a := op.Actions[0]
	if a.Action != "relocate" || !a.Destructive || !hasCode(a.Warnings, model.WarnSharedExecutable) {
		t.Errorf("expected a destructive relocation with the shared-path warning: %+v", a)
	}
	var payload map[string]any
	_ = json.Unmarshal(h.stdout.Bytes(), &payload)
	if _, ok := payload["transitions"]; !ok {
		t.Error("the full transition records are still reported")
	}
}

func TestElevatedRunsWarnInJSONHumanOutputAndDoctor(t *testing.T) {
	h, bin := binHarness(t, ruffUV)
	exe := filepath.Join(bin, "ruff")
	testsupport.WriteFile(t, exe, "#!/bin/sh\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: exe})
	h.env.Elevation = func() elevation.Status {
		return elevation.Status{Elevated: true, Mechanism: "effective user id 0, root", Home: "/root", InvokingUser: "alice"}
	}

	op := decodeOperation(t, h.mustRun("remove", "ruff", "--dry-run", "--json"))
	if !hasCode(op.Warnings, model.WarnElevated) {
		t.Fatalf("an elevated run carries the warning at the top level: %+v", op.Warnings)
	}

	out := h.mustRun("remove", "ruff", "--dry-run")
	if !strings.Contains(out, "[running-elevated]") || !strings.Contains(out, "/root") {
		t.Errorf("human output names the elevated home:\n%s", out)
	}

	var doctor struct {
		Problems []model.Warning `json:"problems"`
		Healthy  bool            `json:"healthy"`
	}
	if err := json.Unmarshal([]byte(h.mustRun("doctor", "--json")), &doctor); err != nil {
		t.Fatal(err)
	}
	if !hasCode(doctor.Problems, model.WarnElevated) {
		t.Errorf("doctor reports the elevated run: %+v", doctor.Problems)
	}

	starts := h.withEvent("command.start")
	if len(starts) == 0 || !starts[len(starts)-1].Elevated {
		t.Errorf("the audit log records that the command ran elevated: %+v", starts)
	}
	// Report only: the dry run still ran and nothing was refused.
	if _, err := os.Stat(exe); err != nil {
		t.Fatal("reporting elevation must not change behaviour")
	}
}

func TestAuditLogRecordsTheEffectsOfADestructiveAction(t *testing.T) {
	h, bin := binHarness(t, ruffUV)
	exe := filepath.Join(bin, "ruff")
	testsupport.WriteFile(t, exe, "#!/bin/sh\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: exe})
	h.mustRun("remove", "ruff", "--yes", "--allow-destructive")

	for _, e := range h.withEvent("action") {
		if e.Package != "ruff" {
			continue
		}
		if !e.Destructive || e.Result != "applied" {
			t.Errorf("the removal is logged as an applied, destructive action: %+v", e)
		}
		if findEffect(e.Effects, model.EffectDelete, model.EffectFile, exe) == nil {
			t.Errorf("the log records the deleted path: %+v", e.Effects)
		}
		return
	}
	t.Fatal("no action was logged for the removal")
}
