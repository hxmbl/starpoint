package cli

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/providers/uv"
)

func TestInstallSaysWhenNoProviderHasTheSoftware(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	out := h.mustRun("install", "ruff")
	if !strings.Contains(out, "none found on installed providers") {
		t.Errorf("the lookup result should be reported:\n%s", out)
	}
	if len(h.uv.InstallCalls) != 1 {
		t.Errorf("not being present yet is normal; it should still install: %d", len(h.uv.InstallCalls))
	}
}

// A provider that is installed but cannot enumerate its inventory weakens the
// claim, and the wording has to admit that rather than overstate it.
func TestTheNotFoundMessageAdmitsWhatCouldNotBeAsked(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)
	// apt is present in the harness registry and can be made to be installed yet
	// unable to enumerate its inventory, which is exactly the case where "nobody
	// has it" is not the whole truth.
	h.apt.IsAvailable = true
	h.apt.Caps.Discover = false

	out := h.mustRun("install", "ruff")
	if !strings.Contains(out, "not asked: apt") {
		t.Errorf("the message should name what could not be asked:\n%s", out)
	}
}

func TestInstallNamesEveryProviderThatHasTheSoftware(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)
	// Two providers hold copies. That is worth saying: the configuration is about
	// to pick one, and the user may not know about the other.
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0", Path: "/uv/bin/ruff"})
	h.brew.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: "/brew/bin/ruff"})

	out := h.mustRun("install", "ruff", uv.Name)
	if !strings.Contains(out, "also present via brew") {
		t.Errorf("the duplicate should be reported:\n%s", out)
	}
}

// When software is already there through the named provider, nothing is installed
// — and the confirmation has to say that, because "Record" and "Install" are very
// different things to be asked about.
func TestRecordingExistingSoftwareIsNotPresentedAsAnInstall(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0", Path: "/uv/bin/ruff"})

	out := h.mustRun("install", "ruff", uv.Name)
	if !strings.Contains(out, "About to change your machine") {
		t.Fatalf("a change must still be described:\n%s", out)
	}
	if !strings.Contains(out, "Record ruff") {
		t.Errorf("adoption should read as a record, not an install:\n%s", out)
	}
	if !strings.Contains(out, "no software is installed or removed") {
		t.Errorf("the absence of a mutation should be explicit:\n%s", out)
	}
	if len(h.uv.InstallCalls) != 0 {
		t.Error("nothing should have been installed")
	}
}

// A pin that the installed copy does not satisfy is a real install, and must not
// be described as a record.
func TestAPinThatIsNotSatisfiedIsPresentedAsAnInstall(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)
	h.uv.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: "/uv/bin/ruff"})

	out := h.mustRun("install", "ruff@0.6.0", uv.Name)
	if !strings.Contains(out, "Install ruff@0.6.0") {
		t.Errorf("an unsatisfied pin is an install:\n%s", out)
	}
	if len(h.uv.InstallCalls) != 1 {
		t.Errorf("expected the install, got %d", len(h.uv.InstallCalls))
	}
}

func TestInstallAsksBeforeChangingTheMachine(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(false)

	out := h.mustRun("install", "ruff", uv.Name)
	if !h.asked() {
		t.Fatal("installing must ask")
	}
	if !strings.Contains(out, "Nothing was changed") {
		t.Errorf("declining should say so:\n%s", out)
	}
	if len(h.uv.InstallCalls) != 0 {
		t.Error("declining must change nothing")
	}
	if strings.Contains(h.stdout.String(), "installed ruff via") {
		t.Errorf("nothing may be reported as installed:\n%s", h.stdout.String())
	}
}

func TestInstallProceedsWhenConfirmed(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	h.mustRun("install", "ruff", uv.Name)
	if len(h.uv.InstallCalls) != 1 {
		t.Errorf("confirming must apply the change, got %d installs", len(h.uv.InstallCalls))
	}
}

func TestInstallDoesNotAskWithYes(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	h.mustRun("install", "--yes", "ruff", uv.Name)
	if h.asked() {
		t.Error("--yes means the answer was already given")
	}
	if len(h.uv.InstallCalls) != 1 {
		t.Error("the change must still happen")
	}
}

// Without a terminal there is no way to ask, so the command must refuse rather
// than modify the machine on the assumption that nobody is watching.
func TestInstallRefusesWhenThereIsNoTerminal(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.unattended()

	code, _, errOut := h.run("install", "ruff", uv.Name)
	if code == 0 {
		t.Fatal("a modification with no way to confirm must not proceed")
	}
	if !strings.Contains(errOut, "--yes") {
		t.Errorf("the refusal should say how to proceed: %s", errOut)
	}
	if len(h.uv.InstallCalls) != 0 {
		t.Error("nothing may be installed")
	}
}

func TestDryRunNeverAsks(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	h.mustRun("install", "--dry-run", "ruff", uv.Name)
	if h.asked() {
		t.Error("a dry run has nothing to decline")
	}
}

// One question for the whole command, not one per package.
func TestSeveralPackagesAreConfirmedTogether(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(false)

	out := h.mustRun("install", "alpha", "beta", uv.Name)
	if len(h.questions) != 1 {
		t.Errorf("expected one question for two packages, got %v", h.questions)
	}
	for _, want := range []string{"alpha", "beta"} {
		if !strings.Contains(out, want) {
			t.Errorf("the question should mention %s:\n%s", want, out)
		}
	}
	if len(h.uv.InstallCalls) != 0 {
		t.Error("declining must change nothing")
	}
}

func TestRemoveAsksBeforeUninstalling(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n")
	h.ask(true)
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})

	h.ask(false)
	h.mustRun("remove", "ruff")
	if !h.asked() {
		t.Fatal("removing must ask: it takes software away")
	}
	if len(h.uv.RemoveCalls) != 0 {
		t.Error("declining must not uninstall")
	}
}

func TestRemoveProceedsWhenConfirmed(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})
	h.ask(true)

	h.mustRun("remove", "ruff")
	if len(h.uv.RemoveCalls) != 1 {
		t.Errorf("expected one removal, got %d", len(h.uv.RemoveCalls))
	}
}

func TestRemoveRefusesWithoutATerminal(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})
	h.unattended()

	code, _, errOut := h.run("remove", "ruff")
	if code == 0 {
		t.Fatal("an unattended removal must not proceed")
	}
	if len(h.uv.RemoveCalls) != 0 {
		t.Error("nothing may be uninstalled")
	}
	if !strings.Contains(errOut, "ruff") {
		t.Errorf("the refusal should name what it would have removed: %s", errOut)
	}
}

func TestUpdateAsksBeforeChangingAnything(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.5.0"})

	h.ask(false)
	h.mustRun("update")
	if !h.asked() {
		t.Fatal("updating must ask")
	}
	if len(h.uv.UpdateCalls) != 0 {
		t.Error("declining must not update")
	}
}

func TestUpdateProceedsWhenConfirmed(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.5.0"})
	h.ask(true)

	h.mustRun("update")
	if len(h.uv.UpdateCalls) != 1 {
		t.Errorf("expected one update, got %d", len(h.uv.UpdateCalls))
	}
}

// A plan with nothing in it is not worth interrupting anyone for.
func TestUpdateWithNothingToDoDoesNotAsk(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\nversion = \"0.6.0\"\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})

	h.mustRun("update")
	if h.asked() {
		t.Error("there is nothing to confirm")
	}
}

// The configuration can turn the questions off, which is how a machine intended
// to run unattended says so once instead of on every invocation.
func TestTheConfigurationCanDisableConfirmation(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[settings]\nno_confirm = true\n")
	h.ask(true)

	h.mustRun("install", "ruff", uv.Name)
	if h.asked() {
		t.Error("no_confirm in the configuration should be honoured")
	}
	if len(h.uv.InstallCalls) != 1 {
		t.Error("the change must still happen")
	}
}

func TestSyncStillConfirms(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n")
	h.ask(true)

	h.mustRun("sync")
	if !h.asked() {
		t.Error("sync must keep its confirmation")
	}
}

func TestJSONOutputCarriesNoProse(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.ask(true)

	out := h.mustRun("--json", "install", "ruff", uv.Name)
	// The survey line is human output; a JSON consumer gets the document.
	if strings.Contains(out, "none found on installed providers") {
		t.Errorf("prose must not be mixed into JSON:\n%s", out)
	}
	if !strings.Contains(out, `"actions"`) {
		t.Errorf("the payload should be intact:\n%s", out)
	}
}
