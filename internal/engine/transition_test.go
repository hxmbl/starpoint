package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/starpoint/starpoint/internal/locator"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/apt"
	"github.com/starpoint/starpoint/internal/providers/brew"
	"github.com/starpoint/starpoint/internal/providers/system"
	"github.com/starpoint/starpoint/internal/providers/uv"
	"github.com/starpoint/starpoint/internal/testsupport"
)

// withLocator gives the fixture a locator with scripted PATH resolution, so
// verification is exercised without depending on the host's PATH.
func (f *fixture) withLocator(paths map[string]string) {
	f.eng = New(f.reg, &locator.Locator{
		LookPath: func(name string) (string, error) {
			if p, ok := paths[name]; ok {
				return p, nil
			}
			return "", errors.New("not on PATH")
		},
	})
}

// staged sets up a coherent move of "ruff" from brew to uv: brew has it, uv will
// install it at /uv/bin, and PATH currently resolves the command to /brew/bin.
func (f *fixture) staged() {
	f.brew.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: "/brew/bin/ruff"})
	f.brew.InstallPath = "/brew/bin"
	f.uv.InstallPath = "/uv/bin"
	f.withLocator(map[string]string{"ruff": "/brew/bin/ruff"})
}

// rebased points PATH at the target, which is what a user does after installing.
func (f *fixture) rebased() {
	f.withLocator(map[string]string{"ruff": "/uv/bin/ruff"})
}

// transitionState returns the state a transition would act on.
func (f *fixture) transitionState(name string) model.State {
	f.t.Helper()
	if name == "" {
		name = "ruff"
	}
	return f.state(f.plan(Options{}), name)
}

// stagedConfig declares ruff as owned by uv while brew still has it.
const stagedConfig = "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n"

func TestTransitionCompletesWhenTargetConfirmsAndOldIsRemoved(t *testing.T) {
	f := newFixture(t, stagedConfig)
	f.staged()

	plan := f.plan(Options{DryRun: true})
	if len(plan.Transitions) != 1 {
		t.Fatalf("a provider move must appear as a transition in the plan, got %d", len(plan.Transitions))
	}
	if plan.Transitions[0].From.Name != brew.Name || plan.Transitions[0].To.Name != uv.Name {
		t.Fatalf("wrong transition: %+v", plan.Transitions[0])
	}

	f.rebased()
	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState(""), Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if tr.Phase != model.TransitionCompleted {
		t.Fatalf("expected completed, got %q (%s)", tr.Phase, tr.Note)
	}
	if !tr.OldRemoved {
		t.Error("the incumbent copy should have been removed")
	}
	if len(f.brew.RemoveCalls) != 1 {
		t.Errorf("expected one removal from brew, got %d", len(f.brew.RemoveCalls))
	}
	if len(f.uv.InstallCalls) != 1 {
		t.Errorf("expected one install into uv, got %d", len(f.uv.InstallCalls))
	}
	if !tr.Verification.OK() {
		t.Errorf("the new copy should be verified: %s", tr.Verification.Reason())
	}
	if tr.Recoverable {
		t.Error("a completed transition needs no recovery")
	}
}

func TestTransitionInstallsEvenWhenPathStillFavoursTheIncumbent(t *testing.T) {
	// The new copy is installed but PATH still points at the old one. The
	// transition must stop before removing the incumbent.
	f := newFixture(t, stagedConfig)
	f.staged()

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState(""), Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("an unusable new copy is not a hard error: %v", err)
	}
	if len(f.uv.InstallCalls) != 1 {
		t.Fatalf("the target should still have been installed, calls=%d", len(f.uv.InstallCalls))
	}
	if len(f.brew.RemoveCalls) != 0 {
		t.Fatal("the incumbent must not be removed while the new copy is not the one PATH resolves")
	}
	if tr.Phase != model.TransitionInstalled {
		t.Fatalf("expected the transition to stop at installed, got %q", tr.Phase)
	}
	if !tr.Recoverable {
		t.Error("it should be resumable once PATH is fixed")
	}
	if !strings.Contains(tr.Advice, "still works") {
		t.Errorf("the advice should say the command is safe, got %q", tr.Advice)
	}
	if _, still := f.brew.Items["ruff"]; !still {
		t.Error("the incumbent installation must still be present")
	}
}

func TestTransitionResumesOncePathIsRebased(t *testing.T) {
	f := newFixture(t, stagedConfig)
	f.staged()
	st := f.transitionState("")

	// First attempt: installs, cannot verify, stops.
	if _, err := f.eng.ExecuteTransition(context.Background(), st, Options{NoConfirm: true}); err != nil {
		t.Fatalf("first attempt: %v", err)
	}
	stalled := f.eng.TransitionFor(context.Background(), st, Options{})
	if stalled.Phase != model.TransitionInstalled {
		t.Fatalf("expected installed, got %q", stalled.Phase)
	}

	// The user fixes PATH.
	f.rebased()
	tr, err := f.eng.ExecuteTransition(context.Background(), st, Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}
	if tr.Phase != model.TransitionCompleted {
		t.Fatalf("expected completed once the new copy wins on PATH, got %q (%s)", tr.Phase, tr.Note)
	}
	if len(f.uv.InstallCalls) != 1 {
		t.Errorf("resuming must not reinstall; installs=%d", len(f.uv.InstallCalls))
	}
	if len(f.brew.RemoveCalls) != 1 {
		t.Errorf("resuming should perform the pending removal, got %d", len(f.brew.RemoveCalls))
	}
}

func TestTransitionDerivesVerifiedFromTheMachineAlone(t *testing.T) {
	// The shape of an interrupted transition: a run installed into uv and stopped
	// before removing brew. Both copies are present and uv wins on PATH, so the
	// phase is derived as verified — from the machine, with nothing remembered.
	f := newFixture(t, stagedConfig)
	f.staged()
	st := f.transitionState("")
	f.uv.With(model.Installation{Name: "ruff", Version: "0.6.0", Path: "/uv/bin/ruff"})
	f.rebased()

	derived := f.eng.TransitionFor(context.Background(), st, Options{})
	if derived.Phase != model.TransitionVerified {
		t.Fatalf("expected verified, got %q (%s)", derived.Phase, derived.Note)
	}
	if !derived.Recoverable {
		t.Error("a verified-but-unfinished transition must be recoverable")
	}
	if !strings.Contains(derived.Note, "not been removed") {
		t.Errorf("the note should say what is pending, got %q", derived.Note)
	}
}

func TestTransitionLeavesIncumbentWhenTargetInstallFails(t *testing.T) {
	// The core safety property: a failed install must not cost a working command.
	f := newFixture(t, stagedConfig)
	f.staged()
	f.rebased()
	f.uv.InstallError = &provider.Error{Provider: uv.Name, Kind: provider.ErrNetwork, Message: "registry unreachable"}

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState(""), Options{NoConfirm: true})
	if err == nil {
		t.Fatal("the failure must be reported")
	}
	if tr.Phase != model.TransitionFailed {
		t.Errorf("expected failed, got %q", tr.Phase)
	}
	if !tr.Recoverable {
		t.Error("a failed install is recoverable by running it again")
	}
	if len(f.brew.RemoveCalls) != 0 {
		t.Fatal("the incumbent copy must survive a failed target install")
	}
	if _, still := f.brew.Items["ruff"]; !still {
		t.Error("the incumbent installation must still be present")
	}
	if !strings.Contains(tr.Advice, "left untouched") {
		t.Errorf("the advice should say the incumbent is safe, got %q", tr.Advice)
	}
}

func TestTransitionStopsAtVerifiedWhenRemovalFails(t *testing.T) {
	f := newFixture(t, stagedConfig)
	f.staged()
	f.rebased()
	f.brew.RemoveError = &provider.Error{Provider: brew.Name, Kind: provider.ErrPermission, Message: "read-only filesystem"}

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState(""), Options{NoConfirm: true})
	if err == nil {
		t.Fatal("a failed removal must be reported")
	}
	if tr.Phase != model.TransitionFailed {
		t.Errorf("expected failed, got %q", tr.Phase)
	}
	if !tr.Recoverable {
		t.Error("the user can retry the removal")
	}
	if !strings.Contains(tr.Advice, "both copies are present") {
		t.Errorf("the advice should explain the safe state, got %q", tr.Advice)
	}
	if _, still := f.brew.Items["ruff"]; !still {
		t.Error("the incumbent must still be there")
	}
	if !f.eng.Verify(context.Background(), "ruff", f.uv).OK() {
		t.Error("the new installation should be usable")
	}
}

func TestTransitionReportsWhenIncumbentCannotRemove(t *testing.T) {
	f := newFixture(t, stagedConfig)
	f.staged()
	f.rebased()
	f.brew.Caps.Remove = false

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState(""), Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("a leftover is not a failure: %v", err)
	}
	if tr.Phase != model.TransitionCompleted {
		t.Fatalf("ownership did move, so the phase should be completed, got %q", tr.Phase)
	}
	if tr.LeftBehindReason == "" {
		t.Error("the leftover copy must be reported")
	}
	if len(f.brew.RemoveCalls) != 0 {
		t.Error("a provider that cannot remove must not be asked to")
	}
	if !strings.Contains(tr.Advice, "leftover") {
		t.Errorf("the advice should mention the leftover, got %q", tr.Advice)
	}
}

func TestTransitionNeverRemovesAnOperatingSystemCopy(t *testing.T) {
	// The OS copy is never Starpoint's to remove, and /usr/bin will win on PATH
	// for as long as the user keeps it there — so the transition stops and says so
	// rather than deleting something the machine needs.
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"python\"\nprovider = \"uv\"\n")
	f.sys.With(model.Installation{Name: "python", Path: "/usr/bin/python3", Scope: model.ScopeGlobal})
	f.uv.InstallPath = "/uv/bin"
	f.withLocator(map[string]string{"python": "/usr/bin/python3"})

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState("python"), Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if len(f.sys.RemoveCalls) != 0 {
		t.Fatal("Starpoint must never remove operating-system software")
	}
	if tr.OldRemoved {
		t.Error("nothing may be removed while the new copy is not the one PATH resolves")
	}
	if tr.Phase != model.TransitionInstalled {
		t.Errorf("expected the transition to stop at installed, got %q (%s)", tr.Phase, tr.Note)
	}
	if tr.Verification.PathOwner != system.Name {
		t.Errorf("the OS copy should be named as the winner on PATH, got %q", tr.Verification.PathOwner)
	}
}

func TestTransitionIsBlockedWhenTargetCannotInstall(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"apt\"\n")
	f.apt.Caps.Install = false
	f.brew.With(model.Installation{Name: "ruff", Path: "/brew/bin/ruff"})
	f.withLocator(map[string]string{"ruff": "/brew/bin/ruff"})

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState(""), Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("a blocked transition is not an error: %v", err)
	}
	if tr.Phase != model.TransitionBlocked {
		t.Fatalf("expected blocked, got %q (%s)", tr.Phase, tr.Note)
	}
	if len(f.brew.RemoveCalls) != 0 {
		t.Fatal("a blocked transition must not touch anything")
	}
	if len(f.apt.InstallCalls) != 0 {
		t.Fatal("a provider that cannot install must not be asked to")
	}
}

func TestTransitionIsBlockedWhenTargetIsUnavailable(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"apt\"\n")
	f.brew.With(model.Installation{Name: "ruff", Path: "/brew/bin/ruff"})
	f.withLocator(map[string]string{"ruff": "/brew/bin/ruff"})

	tr, _ := f.eng.ExecuteTransition(context.Background(), f.transitionState(""), Options{NoConfirm: true})
	if tr.Phase != model.TransitionBlocked {
		t.Fatalf("expected blocked, got %q (%s)", tr.Phase, tr.Note)
	}
	if !tr.Recoverable {
		t.Error("installing the provider later should let it proceed")
	}
	if !strings.Contains(tr.Advice, apt.Name) {
		t.Errorf("the advice should name what is missing, got %q", tr.Advice)
	}
}

func TestTransitionDryRunTouchesNothing(t *testing.T) {
	f := newFixture(t, stagedConfig)
	f.staged()
	f.rebased()

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState(""), Options{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(f.uv.InstallCalls) != 0 {
		t.Error("a dry run must not install")
	}
	if len(f.brew.RemoveCalls) != 0 {
		t.Error("a dry run must not remove")
	}
	if tr.Phase == model.TransitionCompleted {
		t.Error("a dry run cannot complete a transition")
	}
	if tr.Note == "" {
		t.Error("a dry run should still describe what it would do")
	}
}

func TestTransitionsOnlyCoversProviderMoves(t *testing.T) {
	f := newFixture(t, `
schema = 1

[[package]]
name = "ruff"
provider = "uv"

[[package]]
name = "alreadyfine"
provider = "uv"

[[package]]
name = "nothinghere"
provider = "uv"
`)
	f.staged()
	f.uv.With(model.Installation{Name: "alreadyfine", Version: "0.5.0", Path: "/uv/bin/alreadyfine"})

	got := f.eng.Transitions(context.Background(), f.doc, Options{})
	if len(got) != 1 {
		t.Fatalf("only the provider move is a transition, got %d", len(got))
	}
	if got[0].Package.Name != "ruff" {
		t.Errorf("wrong transition: %+v", got[0])
	}
	if got[0].From.Name != brew.Name {
		t.Errorf("the incumbent should be brew, got %q", got[0].From.Name)
	}
}

func TestIncompleteTransitionsAreReported(t *testing.T) {
	f := newFixture(t, stagedConfig)
	f.staged()

	got := f.eng.IncompleteTransitions(context.Background(), f.doc, Options{})
	if len(got) != 1 {
		t.Fatalf("a stalled transition must be reported, got %d", len(got))
	}
	if !got[0].NeedsWork() {
		t.Errorf("expected unfinished work, got %q", got[0].Phase)
	}
}

func TestCompletedTransitionIsNotReportedAsIncomplete(t *testing.T) {
	f := newFixture(t, stagedConfig)
	f.uv.With(model.Installation{Name: "ruff", Version: "0.6.0", Path: "/uv/bin/ruff"})
	f.withLocator(map[string]string{"ruff": "/uv/bin/ruff"})

	if got := f.eng.IncompleteTransitions(context.Background(), f.doc, Options{}); len(got) != 0 {
		t.Errorf("a settled machine has no incomplete transitions, got %+v", got)
	}
}

func TestPlanCarriesTheTransitionRecord(t *testing.T) {
	f := newFixture(t, stagedConfig)
	f.staged()
	f.rebased()

	plan := f.plan(Options{DryRun: true})
	var relocate model.PlannedAction
	for _, a := range plan.Actions {
		if a.Action == model.ActionRelocate {
			relocate = a
		}
	}
	if relocate.Transition == nil {
		t.Fatal("a relocate action must carry its transition record")
	}
	if relocate.Transition.To.Name != uv.Name || relocate.Transition.From.Name != brew.Name {
		t.Errorf("wrong transition attached: %+v", relocate.Transition)
	}
}

func TestApplyRunsTransitionsAndReportsThem(t *testing.T) {
	f := newFixture(t, stagedConfig)
	f.staged()
	f.rebased()

	res, err := f.eng.Apply(context.Background(), f.doc, f.plan(Options{}), Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(res.Transitions) != 1 {
		t.Fatalf("expected one transition in the result, got %d", len(res.Transitions))
	}
	if res.Transitions[0].Phase != model.TransitionCompleted || !res.Transitions[0].OldRemoved {
		t.Errorf("expected a completed transition, got %+v", res.Transitions[0])
	}
}

func TestApplyReportsAFailedTransitionWithoutStoppingOthers(t *testing.T) {
	f := newFixture(t, `
schema = 1

[[package]]
name = "ruff"
provider = "uv"

[[package]]
name = "firefox"
provider = "uv"
`)
	f.apt.IsAvailable = true
	f.apt.UnavailableReason = ""
	f.brew.With(model.Installation{Name: "ruff", Path: "/brew/bin/ruff"})
	f.apt.With(model.Installation{Name: "firefox", Path: "/apt/bin/firefox"})
	f.uv.InstallPath = "/uv/bin"
	f.withLocator(map[string]string{"ruff": "/uv/bin/ruff", "firefox": "/uv/bin/firefox"})
	f.uv.InstallError = &provider.Error{Provider: uv.Name, Kind: provider.ErrNetwork, Message: "offline"}

	res, err := f.eng.Apply(context.Background(), f.doc, f.plan(Options{}), Options{NoConfirm: true})
	if err == nil {
		t.Fatal("the failure must be reported")
	}
	if len(res.Transitions) != 2 {
		t.Fatalf("both transitions should be reported, got %d", len(res.Transitions))
	}
	for _, tr := range res.Transitions {
		if tr.Phase != model.TransitionFailed {
			t.Errorf("%s should have failed, got %q", tr.Package.Name, tr.Phase)
		}
	}
	if len(f.brew.RemoveCalls) != 0 || len(f.apt.RemoveCalls) != 0 {
		t.Error("a failed transition must leave every incumbent in place")
	}
}

func TestVerifyAcceptsInventoryOnlyWhenThereIsNoExecutableName(t *testing.T) {
	// A Go module path is not a command, so PATH cannot corroborate it. The
	// provider's inventory is accepted with a note rather than treated as failure.
	f := newFixture(t, "schema = 1\n")
	f.brew.With(model.Installation{Name: "example.com/tool", Version: "1.0.0", Path: ""})

	v := f.eng.Verify(context.Background(), "example.com/tool", f.brew)
	if !v.Present {
		t.Fatal("the provider reports it, so it is present")
	}
	if !v.OK() {
		t.Errorf("software with no executable name must be accepted on inventory alone: %s", v.Reason())
	}
	if v.OnPath != "" {
		t.Errorf("a module path should never be looked up on PATH, got %q", v.OnPath)
	}
}

func TestVerifyUsesTheProviderInstallPathWhenPathIsUnattributable(t *testing.T) {
	// A custom prefix the locator has no rule for: nothing claims the path, but it
	// lies inside the directory the provider said it installs into, which is
	// enough to confirm and worth explaining.
	f := newFixture(t, "schema = 1\n")
	f.brew.With(model.Installation{Name: "ruff", Version: "0.6.0", Path: "/srv/tools/ruff-0.6.0"})
	f.withLocator(map[string]string{"ruff": "/srv/tools/ruff-0.6.0/bin/ruff"})

	v := f.eng.Verify(context.Background(), "ruff", f.brew)
	if !v.OK() {
		t.Errorf("a path inside the provider's install directory confirms: %s", v.Reason())
	}
	if v.PathOwner != "" {
		t.Errorf("nothing should have claimed the path, got %q", v.PathOwner)
	}
	if v.Note == "" {
		t.Error("unattributed-but-inside should be explained")
	}
}

func TestVerifyRejectsWhenAbsent(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	v := f.eng.Verify(context.Background(), "nothing", f.brew)
	if v.OK() {
		t.Error("absent software must not verify")
	}
	if !strings.Contains(v.Reason(), "does not report") {
		t.Errorf("unexpected reason: %q", v.Reason())
	}
}

func TestVerifyRejectsWhenPathResolvesToAnotherProvider(t *testing.T) {
	// Installed but not in effect: the command still resolves to brew's copy.
	f := newFixture(t, "schema = 1\n")
	f.uv.With(model.Installation{Name: "ruff", Path: "/uv/bin/ruff"})
	f.withLocator(map[string]string{"ruff": "/opt/homebrew/bin/ruff"})

	v := f.eng.Verify(context.Background(), "ruff", f.uv)
	if v.OK() {
		t.Fatal("a command that resolves to another provider's copy is not a working install")
	}
	if v.PathOwner != brew.Name {
		t.Errorf("the actual owner should be recorded, got %q", v.PathOwner)
	}
	if !strings.Contains(v.Reason(), brew.Name) {
		t.Errorf("the reason should name the winner, got %q", v.Reason())
	}
}

func TestVerifyAcceptsWhenPathResolvesInsideTheInstallDirectory(t *testing.T) {
	// The locator cannot attribute the path to a provider, but it lies inside the
	// provider's own bin directory, which is enough evidence.
	f := newFixture(t, "schema = 1\n")
	f.uv.With(model.Installation{Name: "ruff", Path: "/uv/libexec"})
	f.withLocator(map[string]string{"ruff": "/uv/libexec/ruff"})

	v := f.eng.Verify(context.Background(), "ruff", f.uv)
	if !v.OK() {
		t.Errorf("a path inside the provider's install directory is confirmation: %s", v.Reason())
	}
	if v.Note == "" {
		t.Error("unattributed-but-inside should be explained")
	}
}

func TestVerifyReportsWhenNoExecutableIsOnPath(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	exe := filepath.Join(t.TempDir(), "ruff")
	testsupport.WriteFile(t, exe, "#!/bin/sh\n")
	f.uv.With(model.Installation{Name: "ruff", Version: "0.6.0", Path: exe})
	f.withLocator(map[string]string{})

	v := f.eng.Verify(context.Background(), "ruff", f.uv)
	if !v.OK() {
		t.Errorf("an installed tool that is simply not on PATH is still present: %s", v.Reason())
	}
	if !strings.Contains(v.Note, "no executable") {
		t.Errorf("the note should explain the gap, got %q", v.Note)
	}
	if v.OnDisk != exe {
		t.Errorf("the file that confirmed it should be recorded, got %q", v.OnDisk)
	}
}

func TestVerifyRejectsAnInventoryEntryWithNoExecutableOnDisk(t *testing.T) {
	// The audit case: pip listed cowsay, nothing was on PATH, and pip reported no
	// path. Verification passed, the old copy was removed, and the transition
	// reported success over a command that no longer existed.
	f := newFixture(t, "schema = 1\n")
	f.uv.With(model.Installation{Name: "cowsay", Version: "6.1"})
	f.withLocator(map[string]string{})

	v := f.eng.Verify(context.Background(), "cowsay", f.uv)
	if v.OK() {
		t.Fatal("a command with no executable anywhere must not verify")
	}
	if !v.Missing {
		t.Error("the missing executable should be recorded")
	}
	if !strings.Contains(v.Reason(), "not on PATH") {
		t.Errorf("the reason should explain what is missing, got %q", v.Reason())
	}

	// A path that is reported but not on disk is no better than none.
	f.uv.With(model.Installation{Name: "cowsay", Version: "6.1", Path: filepath.Join(t.TempDir(), "gone")})
	if f.eng.Verify(context.Background(), "cowsay", f.uv).OK() {
		t.Error("a reported path that does not exist must not verify")
	}
}

func TestVerifyFindsTheExecutableInTheProviderBinDirectory(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	dir := t.TempDir()
	testsupport.WriteFile(t, filepath.Join(dir, "cowsay"), "#!/bin/sh\n")
	f.uv.ExeDir = dir
	f.uv.With(model.Installation{Name: "cowsay", Version: "6.1"})
	f.withLocator(map[string]string{})

	v := f.eng.Verify(context.Background(), "cowsay", f.uv)
	if !v.OK() {
		t.Fatalf("an executable in the provider's bin directory confirms it: %s", v.Reason())
	}
}

func TestVerifyHandlesANilProvider(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	v := f.eng.Verify(context.Background(), "x", nil)
	if v.OK() {
		t.Error("a nil provider verifies nothing")
	}
	if v.Provider != "" {
		t.Errorf("no provider was asked, got %q", v.Provider)
	}
}

func TestVerifySurvivesAProviderError(t *testing.T) {
	f := newFixture(t, "schema = 1\n")
	f.brew.InstalledError = errors.New("brew exploded")
	v := f.eng.Verify(context.Background(), "x", f.brew)
	if v.OK() {
		t.Error("a provider error is not a confirmation")
	}
	if v.Note == "" {
		t.Error("the error should be captured in a note")
	}
}

func TestExecutableShaped(t *testing.T) {
	if !executableShaped("ruff") {
		t.Error("a plain name is executable-shaped")
	}
	if executableShaped("golang.org/x/tools/gopls") {
		t.Error("a module path is not a command name")
	}
	if executableShaped("") {
		t.Error("an empty name is not a command name")
	}
}

func TestOwnsPath(t *testing.T) {
	cases := []struct {
		resolved, installed string
		want                bool
	}{
		{"/uv/bin/ruff", "/uv/bin/ruff", true},
		{"/uv/bin/ruff", "/uv/bin", true},
		{"/uv/bin/ruff", "/uv/lib", false},
		{"/uv/bin/ruff", "", false},
		{"/uv/bin-extra/ruff", "/uv/bin", false},
		{"/usr/bin/rg", "/opt/homebrew/bin/rg", false},
	}
	for _, tc := range cases {
		if got := ownsPath(tc.resolved, tc.installed); got != tc.want {
			t.Errorf("ownsPath(%q, %q) = %v, want %v", tc.resolved, tc.installed, got, tc.want)
		}
	}
}

// sharedBin models uv and pip --user writing the same ~/.local/bin: a fake
// "pip" is registered beside uv, both report the same executable directory,
// installing writes the file, removing deletes it, and uv refuses to overwrite
// a file it did not create. The order of operations is recorded.
type sharedBin struct {
	dir string
	exe string
	pip *testsupport.FakeProvider
	log []string
}

func (f *fixture) sharedBin(name string) *sharedBin {
	f.t.Helper()
	sb := &sharedBin{dir: f.t.TempDir()}
	sb.exe = filepath.Join(sb.dir, name)
	sb.pip = testsupport.NewFakeProvider("pip", provider.KindPython)
	f.reg.Register(sb.pip)
	for _, p := range []*testsupport.FakeProvider{f.uv, sb.pip} {
		p := p
		p.ExeDir = sb.dir
		p.InstallPath = sb.dir
		p.InstallHook = func(req provider.Request) {
			sb.log = append(sb.log, p.Name()+" install")
			testsupport.WriteFile(f.t, filepath.Join(sb.dir, req.Package.Name), "#!/bin/sh\n# "+p.Name()+"\n")
		}
		p.RemoveHook = func(pkg model.Package) {
			sb.log = append(sb.log, p.Name()+" remove")
			_ = os.Remove(filepath.Join(sb.dir, pkg.Name))
		}
	}
	f.uv.InstallGuard = func(req provider.Request) error {
		if _, err := os.Stat(filepath.Join(sb.dir, req.Package.Name)); err == nil {
			return &provider.Error{Provider: uv.Name, Kind: provider.ErrConflict,
				Message: "error: Executable already exists: " + req.Package.Name}
		}
		return nil
	}
	f.withLocator(map[string]string{})
	return sb
}

func (sb *sharedBin) present() bool {
	_, err := os.Stat(sb.exe)
	return err == nil
}

func TestTransitionUvToPipKeepsTheSharedExecutable(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"cowsay\"\nprovider = \"pip\"\n")
	sb := f.sharedBin("cowsay")
	f.uv.With(model.Installation{Name: "cowsay", Version: "6.0", Path: sb.exe})
	testsupport.WriteFile(t, sb.exe, "#!/bin/sh\n# uv\n")

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState("cowsay"), Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if tr.Phase != model.TransitionCompleted || !tr.OldRemoved {
		t.Fatalf("expected a completed move, got %q (%s)", tr.Phase, tr.Note)
	}
	if !sb.present() {
		t.Fatal("the executable must still exist after the move")
	}
	if want := []string{"uv remove", "pip install"}; strings.Join(sb.log, ",") != strings.Join(want, ",") {
		t.Errorf("a shared path must be handed over old-first, got %v", sb.log)
	}
	if !strings.Contains(tr.Note, sb.exe) {
		t.Errorf("the note should name the shared path, got %q", tr.Note)
	}
}

func TestTransitionPipToUvHandsOverInsteadOfColliding(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"cowsay\"\nprovider = \"uv\"\n")
	sb := f.sharedBin("cowsay")
	sb.pip.With(model.Installation{Name: "cowsay", Version: "6.0", Path: sb.exe})
	testsupport.WriteFile(t, sb.exe, "#!/bin/sh\n# pip\n")

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState("cowsay"), Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("uv must not be asked to overwrite pip's executable: %v", err)
	}
	if tr.Phase != model.TransitionCompleted {
		t.Fatalf("expected completed, got %q (%s)", tr.Phase, tr.Note)
	}
	if !sb.present() {
		t.Fatal("the executable must exist after the move")
	}
	if _, still := sb.pip.Items["cowsay"]; still {
		t.Error("pip's copy should be gone")
	}
}

func TestTransitionHandOverRestoresTheOldCopyWhenTheNewInstallFails(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"cowsay\"\nprovider = \"pip\"\n")
	sb := f.sharedBin("cowsay")
	f.uv.With(model.Installation{Name: "cowsay", Version: "6.0", Path: sb.exe})
	testsupport.WriteFile(t, sb.exe, "#!/bin/sh\n# uv\n")
	sb.pip.InstallError = &provider.Error{Provider: "pip", Kind: provider.ErrNetwork, Message: "could not reach PyPI"}

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState("cowsay"), Options{NoConfirm: true})
	if err == nil {
		t.Fatal("the failed install must be reported")
	}
	if tr.Phase != model.TransitionFailed || !tr.Recoverable {
		t.Errorf("expected a recoverable failure, got %q recoverable=%v", tr.Phase, tr.Recoverable)
	}
	if !sb.present() {
		t.Fatal("the old copy must be reinstalled so the command still works")
	}
	if len(f.uv.InstallCalls) != 1 || f.uv.InstallCalls[0].Package.Version != "6.0" {
		t.Errorf("the old copy should be reinstalled at its previous version, calls=%+v", f.uv.InstallCalls)
	}
	if !strings.Contains(tr.Advice, "reinstalled") {
		t.Errorf("the advice should say the old copy is back, got %q", tr.Advice)
	}
}

func TestTransitionRepairsAnExecutableTheOldRemovalDeleted(t *testing.T) {
	// The target cannot say where it puts executables, so the collision is not
	// seen in advance. The removal of the old copy deletes the new copy's file;
	// verification afterwards must notice and reinstall rather than report
	// success over a missing command.
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"cowsay\"\nprovider = \"pip\"\n")
	sb := f.sharedBin("cowsay")
	sb.pip.ExeDir = ""
	f.uv.With(model.Installation{Name: "cowsay", Version: "6.0"})
	f.uv.InstallGuard = nil

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState("cowsay"), Options{NoConfirm: true})
	if err != nil {
		t.Fatalf("transition: %v", err)
	}
	if tr.Phase != model.TransitionCompleted {
		t.Fatalf("expected completed after repair, got %q (%s)", tr.Phase, tr.Note)
	}
	if !sb.present() {
		t.Fatal("the executable must be restored")
	}
	if !strings.Contains(tr.Note, "reinstalled") {
		t.Errorf("the repair should be reported, got %q", tr.Note)
	}
	if got := strings.Join(sb.log, ","); got != "pip install,uv remove,pip remove,pip install" {
		t.Errorf("unexpected order: %s", got)
	}
}

func TestTransitionFailsWhenTheExecutableCannotBeRepaired(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"cowsay\"\nprovider = \"pip\"\n")
	sb := f.sharedBin("cowsay")
	sb.pip.ExeDir = ""
	f.uv.With(model.Installation{Name: "cowsay", Version: "6.0"})
	f.uv.InstallGuard = nil
	// The second pip install (the repair) leaves no file behind.
	installs := 0
	sb.pip.InstallHook = func(req provider.Request) {
		installs++
		if installs == 1 {
			testsupport.WriteFile(t, sb.exe, "#!/bin/sh\n")
		}
	}

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState("cowsay"), Options{NoConfirm: true})
	if err == nil {
		t.Fatal("a move that ends without a working command is a failure")
	}
	if tr.Phase == model.TransitionCompleted {
		t.Fatal("completed must never be reported over a missing executable")
	}
}

func TestTransitionDryRunExplainsASharedPath(t *testing.T) {
	f := newFixture(t, "schema = 1\n\n[[package]]\nname = \"cowsay\"\nprovider = \"pip\"\n")
	sb := f.sharedBin("cowsay")
	f.uv.With(model.Installation{Name: "cowsay", Version: "6.0", Path: sb.exe})
	testsupport.WriteFile(t, sb.exe, "#!/bin/sh\n")

	tr, err := f.eng.ExecuteTransition(context.Background(), f.transitionState("cowsay"), Options{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if len(sb.log) != 0 {
		t.Errorf("a dry run must not touch anything: %v", sb.log)
	}
	if !strings.Contains(tr.Note, "removed first") {
		t.Errorf("the plan should explain the hand-over, got %q", tr.Note)
	}
}
