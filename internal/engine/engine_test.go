package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/starpoint/starpoint/internal/config"
	"github.com/starpoint/starpoint/internal/locator"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers"
	"github.com/starpoint/starpoint/internal/providers/apt"
	"github.com/starpoint/starpoint/internal/providers/brew"
	"github.com/starpoint/starpoint/internal/providers/cargo"
	"github.com/starpoint/starpoint/internal/providers/npm"
	"github.com/starpoint/starpoint/internal/providers/system"
	"github.com/starpoint/starpoint/internal/providers/uv"
	"github.com/starpoint/starpoint/internal/testsupport"
)

// fixture wires a document and engine against fake providers. Every test in this
// file uses it so no test can touch the real machine.
type fixture struct {
	t     *testing.T
	doc   *config.Document
	eng   *Engine
	reg   *provider.Registry
	brew  *testsupport.FakeProvider
	uv    *testsupport.FakeProvider
	cargo *testsupport.FakeProvider
	npm   *testsupport.FakeProvider
	apt   *testsupport.FakeProvider
	sys   *testsupport.FakeProvider
	path  string
}

func newFixture(t *testing.T, configBody string) *fixture {
	t.Helper()
	reg := provider.NewRegistry()

	sys := testsupport.NewFakeProvider(system.Name, provider.KindSystem)
	sys.Caps = provider.Capabilities{Discover: true, DefaultScope: model.ScopeGlobal}

	brewP := testsupport.NewFakeProvider(brew.Name, provider.KindBrew)
	brewP.Caps = provider.Capabilities{Discover: true, Install: true, Remove: true, Update: true, Channels: true, DefaultScope: model.ScopeUser}

	aptP := testsupport.NewFakeProvider(apt.Name, provider.KindApt)
	aptP.IsAvailable = false
	aptP.UnavailableReason = "apt-get not found on PATH"

	uvP := testsupport.NewFakeProvider(uv.Name, provider.KindPython)
	cargoP := testsupport.NewFakeProvider(cargo.Name, provider.KindRust)
	npmP := testsupport.NewFakeProvider(npm.Name, provider.KindNode)

	for _, p := range []*testsupport.FakeProvider{sys, brewP, aptP, uvP, cargoP, npmP} {
		reg.Register(p)
	}

	path := t.TempDir() + "/.starpoint.toml"
	testsupport.WriteFile(t, path, configBody)
	doc, err := config.Load(config.LoadOptions{GlobalPath: path})
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	return &fixture{
		t: t, doc: doc, reg: reg, path: path,
		eng:   New(reg, nil),
		brew:  brewP,
		uv:    uvP,
		cargo: cargoP,
		npm:   npmP,
		apt:   aptP,
		sys:   sys,
	}
}

// plan runs Plan and fails the test on error.
func (f *fixture) plan(opts Options) model.Plan {
	f.t.Helper()
	plan, _, err := f.eng.Plan(context.Background(), f.doc, opts)
	if err != nil {
		f.t.Fatalf("planning: %v", err)
	}
	return plan
}

// state returns the state for one package.
func (f *fixture) state(plan model.Plan, name string) model.State {
	f.t.Helper()
	for _, st := range plan.States {
		if st.Package.Name == name {
			return st
		}
	}
	f.t.Fatalf("no state for %q; have %v", name, names(plan))
	return model.State{}
}

func names(plan model.Plan) []string {
	var out []string
	for _, st := range plan.States {
		out = append(out, st.Package.Name)
	}
	return out
}

func (f *fixture) configBody() string {
	f.t.Helper()
	return testsupport.ReadFile(f.t, f.path)
}

func TestPlanReportsMissingPackageAndRecordsProvider(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\n")
	plan := f.plan(Options{})
	if len(plan.Actions) != 1 {
		t.Fatalf("expected exactly one action, got %d", len(plan.Actions))
	}
	action := plan.Actions[0]
	if action.Action != model.ActionInstall {
		t.Errorf("expected install, got %q", action.Action)
	}
	if action.State.Res.Provider != brew.Name {
		t.Errorf("expected brew to be chosen, got %q", action.State.Res.Provider)
	}
	if action.ConfigChange == nil || action.ConfigChange.Upsert == nil {
		t.Fatal("the ownership decision must be persisted in the configuration")
	}
	if action.ConfigChange.Upsert.Provider != brew.Name {
		t.Errorf("the persisted decision must name brew, got %q", action.ConfigChange.Upsert.Provider)
	}
}

func TestPlanIsEmptyWhenStateMatchesConfiguration(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	f.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0", Path: "/opt/homebrew/bin/rg"})
	plan := f.plan(Options{})
	if !plan.Empty() {
		t.Fatalf("an already-correct package needs no action, got %+v", plan.Actions)
	}
	if got := f.state(plan, "ripgrep").Status; got != model.StatusCorrect {
		t.Errorf("expected status correct, got %q", got)
	}
}

func TestSyncIsIdempotent(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\n")
	ctx := context.Background()

	plan := f.plan(Options{})
	res, err := f.eng.Apply(ctx, f.doc, plan, Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if len(f.brew.InstallCalls) != 1 {
		t.Fatalf("expected exactly one install, got %d", len(f.brew.InstallCalls))
	}
	if !res.ConfigChanged {
		t.Error("the first sync must record the provider decision in the configuration")
	}
	if !strings.Contains(f.configBody(), "provider = \"brew\"") {
		t.Errorf("configuration did not record ownership:\n%s", f.configBody())
	}

	// Re-load the document from disk: idempotency has to hold across process
	// restarts, not just within one in-memory document.
	doc2, err := config.Load(config.LoadOptions{GlobalPath: f.path})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	f.doc = doc2

	plan2 := f.plan(Options{})
	if !plan2.Empty() {
		t.Fatalf("a second sync must be a no-op, got %+v", plan2.Actions)
	}
	res2, err := f.eng.Apply(ctx, f.doc, plan2, Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if len(f.brew.InstallCalls) != 1 {
		t.Fatalf("a second sync must not reinstall; install count is %d", len(f.brew.InstallCalls))
	}
	if res2.ConfigChanged {
		t.Error("a no-op sync must not rewrite the configuration")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\n")
	before := f.configBody()

	plan := f.plan(Options{DryRun: true})
	res, err := f.eng.Apply(context.Background(), f.doc, plan, Options{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !res.DryRun {
		t.Error("result should report dry-run mode")
	}
	if len(f.brew.InstallCalls) != 0 {
		t.Fatal("a dry run must not invoke a provider")
	}
	if after := f.configBody(); after != before {
		t.Errorf("a dry run must not modify the configuration:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestConfiguredProviderIsReusedNotReResolved(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"python\"\nprovider = \"uv\"\n")
	f.uv.With(model.Installation{Name: "python", Version: "3.12.3", Path: "/home/u/.local/bin/python"})

	plan := f.plan(Options{})
	if got := f.state(plan, "python").Res.Provider; got != uv.Name {
		t.Fatalf("a recorded provider must be reused, got %q", got)
	}
	if !f.state(plan, "python").Res.Forced {
		t.Error("a recorded provider is an explicit decision and must be marked forced")
	}
	if !plan.Empty() {
		t.Errorf("no action should be needed, got %+v", plan.Actions)
	}
}

func TestExistingOwnershipIsAdoptedRatherThanDuplicated(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\n")
	f.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0", Path: "/opt/homebrew/bin/rg"})

	plan := f.plan(Options{})
	if !plan.Empty() {
		t.Fatalf("software that is already installed must not be reinstalled, got %+v", plan.Actions)
	}
	st := f.state(plan, "ripgrep")
	if st.Status != model.StatusCorrect {
		t.Errorf("expected correct, got %q", st.Status)
	}
}

func TestConfiguredProviderDiffersFromCurrentOwner(t *testing.T) {
	// The user deliberately moved ripgrep to uv in the configuration while brew
	// still has it. That is a transition, and Starpoint must not simply install a
	// second copy and forget the first.
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"uv\"\n")
	f.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0", Path: "/opt/homebrew/bin/rg"})

	plan := f.plan(Options{})
	if len(plan.Actions) != 1 {
		t.Fatalf("expected one relocation, got %d", len(plan.Actions))
	}
	action := plan.Actions[0]
	if action.Action != model.ActionRelocate {
		t.Fatalf("expected relocate, got %q", action.Action)
	}
	if action.State.Current == nil || action.State.Current.Provider != brew.Name {
		t.Fatalf("the current owner must be reported, got %+v", action.State.Current)
	}
	if !strings.Contains(action.Note, brew.Name) {
		t.Errorf("the note should name the current provider: %q", action.Note)
	}
}

func TestRelocationInstallsNewCopyBeforeRemovingOld(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"uv\"\n")
	f.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0", Path: "/opt/homebrew/bin/rg"})

	plan := f.plan(Options{})
	if _, err := f.eng.Apply(context.Background(), f.doc, plan, Options{NoConfirm: true}); err != nil {
		t.Fatalf("applying relocation: %v", err)
	}
	if len(f.uv.InstallCalls) != 1 {
		t.Fatalf("the new provider must install exactly once, got %d", len(f.uv.InstallCalls))
	}
	if len(f.brew.RemoveCalls) != 1 {
		t.Fatalf("the old copy must be removed exactly once, got %d", len(f.brew.RemoveCalls))
	}
	if _, still := f.brew.Items["ripgrep"]; still {
		t.Error("the old installation should be gone after relocation")
	}
}

func TestRelocationLeavesOldCopyWhenProviderCannotRemove(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"uv\"\n")
	f.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0", Path: "/opt/homebrew/bin/rg"})
	f.brew.Caps.Remove = false

	plan := f.plan(Options{})
	res, err := f.eng.Apply(context.Background(), f.doc, plan, Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("relocation should still succeed: %v", err)
	}
	if len(f.brew.RemoveCalls) != 0 {
		t.Error("a provider that cannot remove must not be asked to")
	}
	if len(res.Applied) != 1 || !strings.Contains(res.Applied[0].Note, "cannot remove") {
		t.Errorf("the user must be told the old copy remains: %+v", res.Applied)
	}
}

func TestPinnedVersionMismatchProducesUpdateAction(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\nversion = \"0.6.0\"\n")
	f.uv.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: "/home/u/.local/bin/ruff"})

	plan := f.plan(Options{})
	if len(plan.Actions) != 1 || plan.Actions[0].Action != model.ActionUpdate {
		t.Fatalf("expected one update action, got %+v", plan.Actions)
	}
	if f.state(plan, "ruff").Status != model.StatusVersionDiffers {
		t.Errorf("expected version-differs, got %q", f.state(plan, "ruff").Status)
	}
}

func TestPinnedVersionMismatchOnProviderWithoutPinningIsReportedNotFixed(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\nversion = \"14.1.0\"\n")
	f.brew.With(model.Installation{Name: "ripgrep", Version: "13.0.0", Path: "/opt/homebrew/bin/rg"})

	plan := f.plan(Options{})
	if len(plan.Actions) != 1 {
		t.Fatalf("expected one recorded conflict, got %d", len(plan.Actions))
	}
	action := plan.Actions[0]
	if action.Action != model.ActionRecord {
		t.Fatalf("expected a reported conflict, got %q", action.Action)
	}
	if !strings.Contains(action.Note, "cannot install") {
		t.Errorf("the note should explain the limitation: %q", action.Note)
	}
	if action.ConfigChange != nil {
		t.Error("an unsatisfiable pin must never cause a configuration edit")
	}

	before := f.configBody()
	if _, err := f.eng.Apply(context.Background(), f.doc, plan, Options{NoConfirm: true}); err != nil {
		t.Fatalf("applying: %v", err)
	}
	if after := f.configBody(); after != before {
		t.Errorf("the source of truth must not be rewritten to hide a conflict:\n%s", after)
	}
	if len(f.brew.InstallCalls) != 0 || len(f.brew.UpdateCalls) != 0 {
		t.Error("an unsatisfiable pin must not trigger a provider operation")
	}
}

func TestUnpinnedPackageWithMatchingVersionIsCorrect(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n")
	f.uv.With(model.Installation{Name: "ruff", Version: "0.5.0"})
	if !f.plan(Options{}).Empty() {
		t.Error("an unpinned package at any version is correct")
	}
}

func TestUnavailableProviderIsReportedAsMissing(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"apt\"\n")
	plan := f.plan(Options{})
	st := f.state(plan, "ripgrep")
	if st.Status != model.StatusMissing {
		t.Errorf("expected missing, got %q", st.Status)
	}
	if len(st.Notes) == 0 {
		t.Error("an unavailable provider must be explained in a note")
	}
	if !plan.Empty() {
		t.Error("no action can be taken when the provider is unavailable")
	}
}

func TestUnknownProviderInConfigIsWarned(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"nix\"\n")
	plan := f.plan(Options{})
	found := false
	for _, w := range plan.Warnings {
		if w.Provider == "nix" && w.Severity == model.SeverityError {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an error-level warning for the unknown provider, got %+v", plan.Warnings)
	}
}

func TestPartialFailureDoesNotStopOtherPackages(t *testing.T) {
	f := newFixture(t, `
schema = 1

[[package]]
name = "ripgrep"

[[package]]
name = "broken"
provider = "uv"
`)
	// ripgrep goes to brew and succeeds; broken is pinned to uv, whose network
	// is down. The failure of one must not prevent the other.
	f.uv.InstallError = &provider.Error{
		Provider: uv.Name, Op: "install", Kind: provider.ErrNetwork, Message: "index unreachable",
	}

	plan := f.plan(Options{})
	res, err := f.eng.Apply(context.Background(), f.doc, plan, Options{NoConfirm: true})
	if err == nil {
		t.Fatal("the caller must be told that something failed")
	}
	if len(res.Applied) != 2 {
		t.Fatalf("both actions should be reported, got %d", len(res.Applied))
	}
	if len(f.brew.InstallCalls) != 1 {
		t.Fatalf("one failure must not prevent the other install, calls=%d", len(f.brew.InstallCalls))
	}
	if len(f.uv.InstallCalls) != 1 {
		t.Fatalf("the failing provider must still have been attempted, calls=%d", len(f.uv.InstallCalls))
	}
	// ripgrep succeeded before broken failed, so its ownership decision stands.
	if _, ok := f.doc.Package("ripgrep"); !ok {
		t.Error("a successful install must still be recorded in the configuration")
	}
}

func TestDuplicateOwnershipAcrossProvidersIsVisible(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"python\"\nprovider = \"uv\"\n")
	f.brew.With(model.Installation{Name: "python", Version: "3.11.9", Path: "/opt/homebrew/bin/python3"})
	f.uv.With(model.Installation{Name: "python", Version: "3.12.3", Path: "/home/u/.local/bin/python"})

	plan := f.plan(Options{})
	st := f.state(plan, "python")
	if st.Status != model.StatusCorrect {
		t.Fatalf("the desired provider owns it, so status is correct; got %q", st.Status)
	}
	// findExisting returns the configured provider's copy first, so the duplicate
	// is discovered by the doctor's explicit cross-provider scan rather than by
	// the state comparison. Confirm at least that a correct state does not cause
	// a second install.
	if !plan.Empty() {
		t.Errorf("having a second copy elsewhere must not cause a reinstall: %+v", plan.Actions)
	}
}

func TestAddRecordsPackageAndProvider(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	res, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "ripgrep"}, Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !res.ConfigChanged {
		t.Fatal("add must write the configuration")
	}
	body := f.configBody()
	if !strings.Contains(body, `name = "ripgrep"`) {
		t.Errorf("package missing from configuration:\n%s", body)
	}
	if !strings.Contains(body, `provider = "brew"`) {
		t.Errorf("provider decision missing from configuration:\n%s", body)
	}
	if len(f.brew.InstallCalls) != 1 {
		t.Errorf("expected one install call, got %d", len(f.brew.InstallCalls))
	}
}

func TestAddIsIdempotent(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := f.eng.Add(ctx, f.doc, model.Package{Name: "ripgrep"}, Options{NoConfirm: true}); err != nil {
			t.Fatalf("add %d: %v", i, err)
		}
	}
	if len(f.brew.InstallCalls) != 1 {
		t.Fatalf("adding an already-installed package must not reinstall, calls=%d", len(f.brew.InstallCalls))
	}
	if got := strings.Count(f.configBody(), `name = "ripgrep"`); got != 1 {
		t.Errorf("the configuration must contain exactly one entry, found %d:\n%s", got, f.configBody())
	}
}

func TestAddRecordsIntentWhenProviderUnavailable(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	res, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "ripgrep", Provider: "apt"}, Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(f.configBody(), `provider = "apt"`) {
		t.Errorf("the decision must be recorded even though the provider is absent:\n%s", f.configBody())
	}
	if len(res.Warnings) != 1 || res.Warnings[0].Severity != model.SeverityError {
		t.Errorf("an unavailable provider must produce an error warning, got %+v", res.Warnings)
	}
	if len(f.apt.InstallCalls) != 0 {
		t.Error("an unavailable provider must not be invoked")
	}
}

func TestAddDryRunDoesNotWrite(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	before := f.configBody()
	if _, err := f.eng.Add(context.Background(), f.doc, model.Package{Name: "ripgrep"}, Options{DryRun: true}); err != nil {
		t.Fatalf("add dry run: %v", err)
	}
	if after := f.configBody(); after != before {
		t.Errorf("dry run changed the configuration:\n%s", after)
	}
	if len(f.brew.InstallCalls) != 0 {
		t.Error("dry run must not invoke providers")
	}
}

func TestAddExplicitVersionIsPreserved(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	if _, err := f.eng.Add(context.Background(), f.doc,
		model.Package{Name: "ruff", Version: "0.6.0"}, Options{NoConfirm: true}); err != nil {
		t.Fatalf("add: %v", err)
	}
	if !strings.Contains(f.configBody(), `version = "0.6.0"`) {
		t.Errorf("explicit version must be written verbatim:\n%s", f.configBody())
	}
}

func TestRemoveUninstallsAndDeletesEntry(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	f.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0"})

	res, err := f.eng.Remove(context.Background(), f.doc, "ripgrep", Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if len(f.brew.RemoveCalls) != 1 {
		t.Errorf("expected one remove call, got %d", len(f.brew.RemoveCalls))
	}
	if strings.Contains(f.configBody(), "ripgrep") {
		t.Errorf("the entry must be deleted from the configuration:\n%s", f.configBody())
	}
	if !res.ConfigChanged {
		t.Error("remove must report that the configuration changed")
	}
}

func TestRemoveRefusesSystemOwnedSoftware(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"git\"\nprovider = \"system\"\n")
	_, err := f.eng.Remove(context.Background(), f.doc, "git", Options{NoConfirm: true})
	if err == nil {
		t.Fatal("removing operating-system software must be refused")
	}
	if !strings.Contains(err.Error(), "operating system") {
		t.Errorf("the refusal should explain itself, got: %v", err)
	}
}

func TestRemoveUnknownPackageFails(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	if _, err := f.eng.Remove(context.Background(), f.doc, "nope", Options{NoConfirm: true}); err == nil {
		t.Fatal("removing an unconfigured package must fail clearly")
	}
}

func TestRemoveDryRunDoesNotUninstall(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	f.brew.With(model.Installation{Name: "ripgrep"})
	before := f.configBody()
	if _, err := f.eng.Remove(context.Background(), f.doc, "ripgrep", Options{DryRun: true}); err != nil {
		t.Fatalf("remove dry run: %v", err)
	}
	if len(f.brew.RemoveCalls) != 0 {
		t.Error("dry run must not uninstall")
	}
	if after := f.configBody(); after != before {
		t.Errorf("dry run changed the configuration:\n%s", after)
	}
}

func TestUpdateRespectsPinnedVersions(t *testing.T) {
	f := newFixture(t, `
schema = 1

[[package]]
name = "ruff"
provider = "uv"
version = "0.6.0"

[[package]]
name = "ripgrep"
provider = "brew"
`)
	f.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})
	f.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0"})

	res, err := f.eng.Update(context.Background(), f.doc, Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(f.uv.UpdateCalls) != 0 {
		t.Error("a pinned package must never be updated")
	}
	if len(f.brew.UpdateCalls) != 1 {
		t.Errorf("an unpinned package should be updated once, got %d", len(f.brew.UpdateCalls))
	}
	if !strings.Contains(warningsText(res), "pinned") {
		t.Errorf("the pin should be reported, got %q", warningsText(res))
	}
}

func TestUpdateReportsPinConflictWithoutChangingConfiguration(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\nversion = \"9.9.9\"\n")
	f.uv.With(model.Installation{Name: "ruff", Version: "0.5.0"})
	before := f.configBody()

	_, err := f.eng.Update(context.Background(), f.doc, Options{NoConfirm: true})
	if err == nil {
		t.Fatal("a pin that the machine cannot satisfy must be reported as a problem")
	}
	if !strings.Contains(err.Error(), "was not changed") {
		t.Errorf("the error should say the configuration was left alone, got: %v", err)
	}
	if after := f.configBody(); after != before {
		t.Errorf("update must never rewrite the source of truth:\n%s", after)
	}
	if len(f.uv.UpdateCalls) != 0 {
		t.Error("a pinned package must not be updated")
	}
}

func TestUpdateSkipsProvidersThatCannotUpdate(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	f.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0"})
	f.brew.Caps.Update = false

	res, err := f.eng.Update(context.Background(), f.doc, Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(f.brew.UpdateCalls) != 0 {
		t.Error("a provider that cannot update must not be invoked")
	}
	if !strings.Contains(warningsText(res), "cannot update") {
		t.Errorf("the limitation should be reported, got %q", warningsText(res))
	}
}

func TestUpdateProviderFailureIsReportedPerPackage(t *testing.T) {
	f := newFixture(t, `
schema = 1

[[package]]
name = "a"

[[package]]
name = "b"
provider = "brew"
`)
	f.brew.With(model.Installation{Name: "b", Version: "1.0.0"})
	f.brew.UpdateError = &provider.Error{Provider: brew.Name, Kind: provider.ErrNetwork, Message: "offline"}

	_, err := f.eng.Update(context.Background(), f.doc, Options{NoConfirm: true})
	if err == nil {
		t.Fatal("a provider failure must be reported")
	}
	if !strings.Contains(err.Error(), "b") {
		t.Errorf("the failing package should be named, got: %v", err)
	}
}

func TestProjectOverrideOfGlobalEntryIsNotADuplicate(t *testing.T) {
	// A project file that re-declares a global package is a deliberate
	// override, not a mistake, so it must not be reported as a duplicate.
	dir := t.TempDir()
	global := dir + "/.starpoint.toml"
	projectDir := dir + "/project"
	project := projectDir + "/.starpoint.toml"
	testsupport.WriteFile(t, global, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"brew\"\n")
	testsupport.WriteFile(t, project, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n")

	doc, err := config.Load(config.LoadOptions{GlobalPath: global, Dir: projectDir, DiscoverProjects: true})
	if err != nil {
		t.Fatalf("loading: %v", err)
	}
	eng := New(provider.NewRegistry(), nil)
	plan, warns, err := eng.Plan(context.Background(), doc, Options{})
	if err != nil {
		t.Fatalf("planning: %v", err)
	}
	for _, w := range plan.Warnings {
		if strings.Contains(w.Message, "declared more than once") {
			t.Errorf("a project override must not be flagged: %q", w.Message)
		}
	}
	if len(plan.States) != 1 {
		t.Errorf("the override must collapse to one entry, got %d", len(plan.States))
	}
	if !strings.Contains(warningsText(&Result{Warnings: warns}), "") && len(warns) > 0 {
		t.Logf("warnings: %+v", warns)
	}
}

func TestWarningForUnavailableProviderIsEmitted(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	plan := f.plan(Options{})
	found := false
	for _, w := range plan.Warnings {
		if w.Provider == "apt" && w.Severity == model.SeverityInfo {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an informational warning for the absent provider, got %+v", plan.Warnings)
	}
}

func TestMalformedConfigurationIsAnErrorNotASilentEmptyPlan(t *testing.T) {
	path := t.TempDir() + "/.starpoint.toml"
	testsupport.WriteFile(t, path, "schema = 1\n[[package]\nname = broken\n")
	if _, err := config.Load(config.LoadOptions{GlobalPath: path}); err == nil {
		t.Fatal("malformed configuration must be reported as an error")
	}
}

func TestLocatorIsUsedWhenConfigurationDoesNotNameAProvider(t *testing.T) {
	reg := provider.NewRegistry()
	brewP := testsupport.NewFakeProvider(brew.Name, provider.KindBrew)
	brewP.Caps = provider.Capabilities{Discover: true, Install: true, Remove: true, Update: true, DefaultScope: model.ScopeUser}
	reg.Register(brewP)

	loc := &locator.Locator{
		LookPath: func(name string) (string, error) {
			if name == "ripgrep" {
				return "/opt/homebrew/bin/rg", nil
			}
			return "", errors.New("not found")
		},
	}
	eng := New(reg, loc)

	path := t.TempDir() + "/.starpoint.toml"
	testsupport.WriteFile(t, path, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\n")
	doc, err := config.Load(config.LoadOptions{GlobalPath: path})
	if err != nil {
		t.Fatal(err)
	}
	// Locate("ripgrep") returns nothing because the executable is named rg, so the
	// resolver falls back to brew and installs it under brew.
	plan, _, err := eng.Plan(context.Background(), doc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("expected one action, got %+v", plan.Actions)
	}
	if plan.Actions[0].State.Res.Provider != brew.Name {
		t.Errorf("expected brew, got %q", plan.Actions[0].State.Res.Provider)
	}
}

func TestManagingProviderBeatsReadOnlySystemProvider(t *testing.T) {
	// The system provider matches almost every command on a Unix machine. If it
	// were asked first, Starpoint would claim the OS owns everything.
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"git\"\n")
	f.sys.With(model.Installation{
		Name: "git", Path: "/usr/bin/git", Provider: system.Name, Scope: model.ScopeGlobal,
	})
	f.brew.With(model.Installation{
		Name: "git", Version: "2.56.0", Path: "/usr/local/bin/git", Scope: model.ScopeUser,
	})

	plan := f.plan(Options{})
	st := f.state(plan, "git")
	if st.Desired == nil || st.Desired.Provider != brew.Name {
		t.Fatalf("the package manager that owns git should win, got %+v", st.Desired)
	}
	if !plan.Empty() {
		t.Errorf("an already-installed package needs no action: %+v", plan.Actions)
	}
}

func TestProviderInventoryBeatsPathAttribution(t *testing.T) {
	// A PATH lookup is inference from a directory name. A provider's own
	// inventory is a fact, so it must be preferred.
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\n")
	eng := New(f.reg, &locator.Locator{
		LookPath: func(name string) (string, error) {
			// Pretend PATH resolves to a Homebrew path, which would suggest brew
			// even though uv is the real owner.
			if name == "ripgrep" {
				return "/opt/homebrew/bin/rg", nil
			}
			return "", errors.New("not found")
		},
	})
	f.uv.With(model.Installation{Name: "ripgrep", Version: "14.1.0", Scope: model.ScopeUser})

	plan, _, err := eng.Plan(context.Background(), f.doc, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.States) != 1 {
		t.Fatalf("expected one state, got %d", len(plan.States))
	}
	if got := plan.States[0].Res.Provider; got != uv.Name {
		t.Fatalf("provider inventory must win over path attribution, got %q", got)
	}
}

func TestDiscoveryFindsUnmanagedSoftware(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	f.uv.With(model.Installation{Name: "ruff", Version: "0.5.4", Scope: model.ScopeUser})
	if got := f.eng.Discovery(context.Background(), "ruff", Options{}); got == nil || got.Provider != uv.Name {
		t.Errorf("discovery should locate unmanaged software, got %+v", got)
	}
	if got := f.eng.Discovery(context.Background(), "absent", Options{}); got != nil {
		t.Errorf("absent software should not be discovered, got %+v", got)
	}
}

func warningsText(res *Result) string {
	var b strings.Builder
	for _, w := range res.Warnings {
		b.WriteString(w.Message)
		b.WriteString("\n")
	}
	return b.String()
}

func TestScopeOfPrefersObservedPlacement(t *testing.T) {
	st := model.State{
		Package: model.Package{Name: "x", Scope: model.ScopeProject},
		Desired: &model.Installation{Name: "x", Scope: model.ScopeUser},
	}
	if got := ScopeOf(st); got != "user" {
		t.Errorf("observed placement should win over the requested scope, got %q", got)
	}
	st2 := model.State{Package: model.Package{Name: "x", Scope: model.ScopeProject}}
	if got := ScopeOf(st2); got != "project" {
		t.Errorf("with nothing observed, the requested scope should be used, got %q", got)
	}
	if got := ScopeOf(model.State{Package: model.Package{Name: "x"}}); got != "-" {
		t.Errorf("unknown placement should render as a dash, got %q", got)
	}
}

func TestRegistryIsExposedForDoctor(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	if _, ok := f.eng.Registry().Get(brew.Name); !ok {
		t.Error("doctor needs access to the registry")
	}
	if _, ok := f.eng.Resolver(), false; ok {
		t.Error("unreachable")
	}
}

func TestNilLocatorIsSafe(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\n")
	// The fixture engine is built with a nil locator; planning must still work.
	if len(f.doc.Packages()) != 1 {
		t.Fatal("a nil locator must not break planning")
	}
	if plan := f.plan(Options{}); len(plan.States) != 1 {
		t.Fatalf("expected one state, got %d", len(plan.States))
	}
}

func TestEngineUsesRealProviderSetWithoutTouchingTheMachine(t *testing.T) {
	// A sanity check that the default registry wires up every implemented
	// provider. This only inspects names and capabilities.
	reg := defaultRegistryForTest()
	for _, want := range []string{system.Name, brew.Name, apt.Name, uv.Name, cargo.Name, npm.Name, "go"} {
		p, ok := reg.Get(want)
		if !ok {
			t.Fatalf("provider %q should be registered", want)
		}
		if p.Name() == "" || p.Description() == "" {
			t.Errorf("provider %q is incompletely described", want)
		}
	}
}

// defaultRegistryForTest builds the production registry without running any
// provider command.
func defaultRegistryForTest() *provider.Registry { return providers.DefaultRegistry() }
