package cli

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

// stagedMove sets up "ruff" as owned by uv in the configuration while brew still
// has it, with PATH resolved to whichever provider currently wins.
func (h *harness) stagedMove(onPath string) {
	h.brew.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: "/brew/bin/ruff"})
	h.brew.InstallPath = "/brew/bin"
	h.uv.InstallPath = "/uv/bin"
	h.setLocator(map[string]string{"ruff": onPath})
}

const stagedConfig = "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n"

func TestTransitionListsPendingMovesWithoutChangingAnything(t *testing.T) {
	h := newHarness(t, stagedConfig)
	h.stagedMove("/brew/bin/ruff")

	out := h.mustRun("transition")
	if !strings.Contains(out, "ruff") {
		t.Errorf("the pending move should be listed:\n%s", out)
	}
	if !strings.Contains(out, "brew") || !strings.Contains(out, "uv") {
		t.Errorf("both providers should be named:\n%s", out)
	}
	if len(h.uv.InstallCalls) != 0 || len(h.brew.RemoveCalls) != 0 {
		t.Error("listing transitions must change nothing")
	}
}

func TestTransitionReportsNothingWhenTheMachineIsSettled(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0", Path: "/uv/bin/ruff"})
	h.setLocator(map[string]string{"ruff": "/uv/bin/ruff"})

	out := h.mustRun("transition")
	if !strings.Contains(out, "No provider transitions are pending") {
		t.Errorf("a settled machine should say so:\n%s", out)
	}
}

func TestTransitionDryRunDescribesWithoutActing(t *testing.T) {
	h := newHarness(t, stagedConfig)
	h.stagedMove("/uv/bin/ruff")

	out := h.mustRun("transition", "--dry-run", "ruff")
	if !strings.Contains(out, "ruff") {
		t.Errorf("the move should be described:\n%s", out)
	}
	if len(h.uv.InstallCalls) != 0 {
		t.Error("a dry run must not install")
	}
	if len(h.brew.RemoveCalls) != 0 {
		t.Error("a dry run must not remove")
	}
}

func TestTransitionPerformsTheMove(t *testing.T) {
	h := newHarness(t, stagedConfig)
	h.stagedMove("/uv/bin/ruff")

	out := h.mustRun("transition", "--no-confirm", "--allow-destructive", "ruff")
	if !strings.Contains(out, "completed") {
		t.Errorf("the final phase should be reported:\n%s", out)
	}
	if len(h.uv.InstallCalls) != 1 {
		t.Errorf("expected one install into uv, got %d", len(h.uv.InstallCalls))
	}
	if len(h.brew.RemoveCalls) != 1 {
		t.Errorf("expected one removal from brew, got %d", len(h.brew.RemoveCalls))
	}
}

func TestTransitionLeavesTheIncumbentWhenTheTargetCannotBeVerified(t *testing.T) {
	h := newHarness(t, stagedConfig)
	h.stagedMove("/brew/bin/ruff")

	out := h.mustRun("transition", "--no-confirm", "--allow-destructive", "ruff")
	if !strings.Contains(out, "installed") {
		t.Errorf("the stalled phase should be reported:\n%s", out)
	}
	if len(h.brew.RemoveCalls) != 0 {
		t.Fatal("the incumbent must not be removed while the new copy is not in effect")
	}
	if !strings.Contains(out, "still works") {
		t.Errorf("the user should be told the command is safe:\n%s", out)
	}
}

func TestTransitionAsksBeforeActing(t *testing.T) {
	h := newHarness(t, stagedConfig)
	h.stagedMove("/uv/bin/ruff")
	var question string
	h.env.Confirm = func(q string) (bool, error) {
		question = q
		return false, nil
	}

	out := h.mustRun("transition", "ruff")
	if question == "" {
		t.Fatal("a transition must ask before changing ownership")
	}
	if !strings.Contains(out, "confirmed working") {
		t.Errorf("the confirmation should explain the safety rule:\n%s", out)
	}
	if len(h.uv.InstallCalls) != 0 || len(h.brew.RemoveCalls) != 0 {
		t.Error("declining must change nothing")
	}
}

func TestTransitionProceedsWhenConfirmed(t *testing.T) {
	h := newHarness(t, stagedConfig)
	h.stagedMove("/uv/bin/ruff")
	h.env.Confirm = func(string) (bool, error) { return true, nil }

	h.mustRun("transition", "ruff")
	if len(h.brew.RemoveCalls) != 1 {
		t.Error("accepting the plan must apply it")
	}
}

func TestTransitionRejectsAnUnknownPackage(t *testing.T) {
	h := newHarness(t, stagedConfig)
	code, _, errOut := h.run("transition", "nothing-moves-here")
	if code != 1 {
		t.Fatalf("expected a failure, got %d", code)
	}
	if !strings.Contains(errOut, "no pending provider transition") {
		t.Errorf("the refusal should be clear: %s", errOut)
	}
}

func TestTransitionJSONExposesPhasesAndEvidence(t *testing.T) {
	h := newHarness(t, stagedConfig)
	h.stagedMove("/brew/bin/ruff")

	out := h.mustRun("--json", "transition")
	var payload struct {
		DryRun      bool `json:"dry_run"`
		Pending     int  `json:"pending"`
		Transitions []struct {
			Package struct {
				Name string `json:"name"`
			} `json:"package"`
			From struct {
				Name string `json:"name"`
			} `json:"from"`
			To struct {
				Name string `json:"name"`
			} `json:"to"`
			Phase        string `json:"phase"`
			Recoverable  bool   `json:"recoverable"`
			Verification struct {
				Provider  string `json:"provider"`
				Present   bool   `json:"present"`
				PathOwner string `json:"path_owner"`
				Method    string `json:"method"`
			} `json:"verification"`
			Advice string `json:"advice"`
		} `json:"transitions"`
	}
	h.decode(out, &payload)
	if len(payload.Transitions) != 1 {
		t.Fatalf("expected one transition, got %+v", payload)
	}
	tr := payload.Transitions[0]
	if tr.Package.Name != "ruff" || tr.From.Name != "brew" || tr.To.Name != "uv" {
		t.Errorf("wrong transition in JSON: %+v", tr)
	}
	if tr.Phase == "" {
		t.Error("the phase must be machine-readable")
	}
	if tr.Verification.Provider == "" || tr.Verification.Method == "" {
		t.Errorf("the evidence must be machine-readable: %+v", tr.Verification)
	}
	if payload.Pending != 1 {
		t.Errorf("pending count wrong: %d", payload.Pending)
	}
}

func TestTransitionJSONReportsTheFinalPhase(t *testing.T) {
	h := newHarness(t, stagedConfig)
	h.stagedMove("/uv/bin/ruff")

	out := h.mustRun("--json", "transition", "--no-confirm", "--allow-destructive", "ruff")
	var payload struct {
		Transitions []struct {
			Phase      string `json:"phase"`
			OldRemoved bool   `json:"old_removed"`
		} `json:"transitions"`
		Data struct {
			Transitions []struct {
				Phase      string `json:"phase"`
				OldRemoved bool   `json:"old_removed"`
			} `json:"transitions"`
		} `json:"data"`
	}
	h.decode(out, &payload)
	if len(payload.Transitions) == 0 {
		payload.Transitions = payload.Data.Transitions
	}
	if len(payload.Transitions) != 1 {
		t.Fatalf("expected one transition, got %+v", payload)
	}
	if payload.Transitions[0].Phase != "completed" {
		t.Errorf("expected completed, got %q", payload.Transitions[0].Phase)
	}
	if !payload.Transitions[0].OldRemoved {
		t.Error("the removal should be reported")
	}
}

// A move half done is still drift to sync: it names both providers and the
// command that finishes the move, and leaves the move to transition.
func TestSyncPointsAtTransitionsInsteadOfMoving(t *testing.T) {
	h := newHarness(t, stagedConfig)
	h.stagedMove("/uv/bin/ruff")

	out := h.mustRun("sync", "--dry-run")
	if !strings.Contains(out, "str transition ruff") {
		t.Errorf("sync should point at transition:\n%s", out)
	}
	if !strings.Contains(out, "brew") || !strings.Contains(out, "uv") {
		t.Errorf("both providers should be named:\n%s", out)
	}
}

func TestSyncJSONCarriesNoTransitions(t *testing.T) {
	h := newHarness(t, stagedConfig)
	h.stagedMove("/uv/bin/ruff")

	out := h.mustRun("--json", "sync", "--dry-run")
	var payload struct {
		Sync struct {
			Transitions []struct {
				Package struct {
					Name string `json:"name"`
				} `json:"package"`
				Phase string `json:"phase"`
			} `json:"transitions"`
		} `json:"sync"`
		Transitions []struct {
			Package struct {
				Name string `json:"name"`
			} `json:"package"`
			Phase string `json:"phase"`
		} `json:"transitions"`
		Data struct {
			Transitions []struct {
				Package struct {
					Name string `json:"name"`
				} `json:"package"`
				Phase string `json:"phase"`
			} `json:"transitions"`
		} `json:"data"`
	}
	h.decode(out, &payload)
	if len(payload.Transitions) == 0 {
		payload.Transitions = payload.Sync.Transitions
	}
	if len(payload.Transitions) == 0 {
		payload.Transitions = payload.Data.Transitions
	}
	if len(payload.Transitions) != 0 {
		t.Fatalf("sync plans no transitions; transition does: %+v", payload)
	}
}

func TestInfoShowsAPendingTransition(t *testing.T) {
	h := newHarness(t, stagedConfig)
	h.stagedMove("/brew/bin/ruff")

	out := h.mustRun("info", "ruff")
	for _, want := range []string{"Transition:", "brew -> uv", "Phase:", "Verified:", "Recoverable:    yes"} {
		if !strings.Contains(out, want) {
			t.Errorf("info should show %q:\n%s", want, out)
		}
	}
}

func TestDoctorReportsAnIncompleteTransition(t *testing.T) {
	h := newHarness(t, stagedConfig)
	h.stagedMove("/brew/bin/ruff")

	out := h.mustRun("doctor")
	if !strings.Contains(out, "provider transition is") {
		t.Errorf("doctor should report the stalled transition:\n%s", out)
	}
}

func TestDoctorReportsALeftoverAfterACompletedMove(t *testing.T) {
	// brew cannot remove, so ownership moves and the duplicate stays behind.
	h := newHarness(t, stagedConfig)
	h.stagedMove("/uv/bin/ruff")
	h.brew.Caps.Remove = false

	h.mustRun("transition", "--no-confirm", "ruff")
	out := h.mustRun("doctor")
	if !strings.Contains(out, "left in place") {
		t.Errorf("doctor should report the leftover copy:\n%s", out)
	}
}
