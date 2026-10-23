package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/report"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// refusalDoc is the JSON a refused command writes to stderr.
type refusalDoc struct {
	SchemaVersion int    `json:"schema_version"`
	Error         string `json:"error"`
	Code          string `json:"code"`
	ExitCode      int    `json:"exit_code"`
	Refused       *struct {
		Operation string                `json:"operation"`
		Actions   []report.ActionResult `json:"actions"`
		Summary   report.Summary        `json:"summary"`
	} `json:"refused"`
}

func decodeRefusal(t *testing.T, s string) refusalDoc {
	t.Helper()
	var d refusalDoc
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		t.Fatalf("not one JSON document: %v\n%s", err, s)
	}
	return d
}

// ruffOnDisk stages ruff installed by uv with its executable in the harness's
// bin directory.
func ruffOnDisk(t *testing.T) (*harness, string) {
	t.Helper()
	h, bin := binHarness(t, ruffUV)
	exe := filepath.Join(bin, "ruff")
	testsupport.WriteExecutable(t, exe, "#!/bin/sh\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: exe})
	return h, exe
}

func TestUnattendedDestructiveRunIsRefusedWithThePlan(t *testing.T) {
	h, exe := ruffOnDisk(t)
	before := h.configBody()

	code, out, errOut := h.run("remove", "ruff", "--yes")
	if code != ExitRefusedDestructive {
		t.Fatalf("expected exit %d, got %d\n%s%s", ExitRefusedDestructive, code, out, errOut)
	}
	for _, want := range []string{
		"error [" + CodeDestructiveUnattended + "]",
		"--allow-destructive",
		"Nothing was changed",
		"Planned changes",
		"! would delete executable " + exe,
		"! would uninstall ruff 0.5.0 (uv)",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("the refusal should say %q:\n%s", want, errOut)
		}
	}
	if len(h.uv.RemoveCalls) != 0 {
		t.Error("a refused removal must not ask the provider to remove anything")
	}
	if _, err := os.Stat(exe); err != nil {
		t.Error("a refused removal must leave the file alone")
	}
	if h.configBody() != before {
		t.Error("a refused removal must not edit the configuration")
	}
}

func TestRefusalJSONCarriesTheCodeAndEveryPlannedEffect(t *testing.T) {
	h, exe := ruffOnDisk(t)

	code, out, errOut := h.run("remove", "ruff", "--yes", "--json")
	if code != ExitRefusedDestructive {
		t.Fatalf("expected exit %d, got %d", ExitRefusedDestructive, code)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("an error goes to stderr, like every other error document; stdout got:\n%s", out)
	}
	d := decodeRefusal(t, errOut)
	if d.SchemaVersion != report.SchemaVersion || d.Code != CodeDestructiveUnattended || d.ExitCode != ExitRefusedDestructive {
		t.Errorf("wrong envelope: %+v", d)
	}
	if d.Refused == nil || d.Refused.Operation != "remove" || len(d.Refused.Actions) != 1 {
		t.Fatalf("the refused plan must be present: %s", errOut)
	}
	a := d.Refused.Actions[0]
	if a.Package != "ruff" || a.Result != string(model.OutcomePlanned) || !a.Destructive {
		t.Errorf("the planned action is wrong: %+v", a)
	}
	if findEffect(a.Effects, model.EffectDelete, model.EffectExecutable, exe) == nil {
		t.Errorf("the refused plan must list the file it would delete: %+v", a.Effects)
	}
	if len(a.ConfigChanges) == 0 || !a.ConfigChanges[0].Destructive || a.ConfigChanges[0].Applied {
		t.Errorf("the refused plan must list the unapplied config edit: %+v", a.ConfigChanges)
	}
	if !d.Refused.Summary.Destructive || d.Refused.Summary.DestructivePackages[0] != "ruff" {
		t.Errorf("the summary must say what is destructive: %+v", d.Refused.Summary)
	}
}

func TestAllowDestructiveLetsAnUnattendedRunProceed(t *testing.T) {
	h, exe := ruffOnDisk(t)
	h.mustRun("remove", "ruff", "--yes", "--allow-destructive")
	if _, err := os.Stat(exe); err == nil {
		t.Error("the removal should have happened")
	}
	// The flag is global: it may sit anywhere on the line.
	h2, _ := ruffOnDisk(t)
	h2.mustRun("--allow-destructive", "remove", "--no-confirm", "ruff")
}

func TestUnattendedRunsThatDestroyNothingAreNotRefused(t *testing.T) {
	h, _ := binHarness(t, ruffUV)
	h.unattended()
	out := h.mustRun("install", "cowsay", "--provider", "uv", "--yes")
	if strings.Contains(out, CodeDestructiveUnattended) {
		t.Errorf("an install into an empty path is not destructive:\n%s", out)
	}
	if len(h.uv.InstallCalls) != 1 {
		t.Errorf("the install should have run: %+v", h.uv.InstallCalls)
	}
}

func TestInteractiveRunsKeepTheConfirmStep(t *testing.T) {
	h, exe := ruffOnDisk(t)
	h.mustRun("remove", "ruff")
	if !h.asked() {
		t.Fatal("an interactive removal is confirmed, not refused")
	}
	if _, err := os.Stat(exe); err == nil {
		t.Error("the confirmed removal should have happened")
	}
}

func TestAPipedAnswerIsUnattended(t *testing.T) {
	// stdin is not a terminal, but it does say yes. That answer was given
	// without seeing the list, so it does not count as reviewing a deletion.
	h, exe := ruffOnDisk(t)
	h.env.Interactive = func() bool { return false }

	code, _, errOut := h.run("remove", "ruff")
	if code != ExitRefusedDestructive {
		t.Fatalf("expected a refusal, got %d\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "no terminal to confirm on") {
		t.Errorf("the refusal should say why the run is unattended:\n%s", errOut)
	}
	if _, err := os.Stat(exe); err != nil {
		t.Fatal("nothing may be removed")
	}
	h.mustRun("remove", "ruff", "--allow-destructive")
	if _, err := os.Stat(exe); err == nil {
		t.Error("with --allow-destructive the piped yes is honoured")
	}
}

func TestNoTerminalRefusalNamesBothFlags(t *testing.T) {
	h, _ := ruffOnDisk(t)
	h.unattended()
	code, _, errOut := h.run("remove", "ruff")
	if code != ExitRefusedDestructive {
		t.Fatalf("expected a refusal, got %d\n%s", code, errOut)
	}
	if !strings.Contains(errOut, "--allow-destructive --yes") {
		t.Errorf("with no terminal at all, both flags are needed:\n%s", errOut)
	}
}

func TestDryRunsAreNeverRefused(t *testing.T) {
	h, exe := ruffOnDisk(t)
	h.unattended()
	h.mustRun("remove", "ruff", "--yes", "--dry-run")
	if _, err := os.Stat(exe); err != nil {
		t.Fatal("a dry run changes nothing")
	}
}

func TestRefusalIsRecordedInTheAuditLog(t *testing.T) {
	h, exe := ruffOnDisk(t)
	h.run("remove", "ruff", "--yes")
	var guard *auditlog.Entry
	for _, e := range h.withEvent(auditlog.EventGuard) {
		e := e
		if e.Command == "remove" && e.Result == "refused" {
			guard = &e
		}
	}
	if guard == nil {
		t.Fatal("the refusal must be logged")
	}
	if !strings.Contains(guard.Detail, CodeDestructiveUnattended) || findEffect(guard.Effects, model.EffectDelete, model.EffectExecutable, exe) == nil {
		t.Errorf("the log should carry the code and the planned effects: %+v", guard)
	}
	finishes := h.withEvent(auditlog.EventCommandFinish)
	if len(finishes) == 0 || finishes[len(finishes)-1].Exit != ExitRefusedDestructive {
		t.Errorf("the command's exit code is logged: %+v", finishes)
	}
}

func TestSyncTransitionAndPruneAreGuardedToo(t *testing.T) {
	t.Run("sync", func(t *testing.T) {
		h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"uv\"\n")
		h.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0", Path: "/opt/homebrew/bin/rg"})
		// sync never moves software, so an unattended sync has nothing
		// destructive to refuse: it reports the drift and changes nothing.
		code, out, errOut := h.run("sync", "--yes")
		if code != 0 || !strings.Contains(out, "starpoint transition ripgrep") {
			t.Fatalf("an unattended sync reports drift and succeeds, got %d\n%s\n%s", code, out, errOut)
		}
		if len(h.brew.RemoveCalls)+len(h.uv.InstallCalls) != 0 {
			t.Error("nothing may run")
		}
	})
	t.Run("transition", func(t *testing.T) {
		h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\n")
		h.brew.With(model.Installation{Name: "ruff", Version: "0.5.0", Path: "/brew/bin/ruff"})
		code, _, errOut := h.run("transition", "ruff", "--yes")
		if code != ExitRefusedDestructive {
			t.Fatalf("expected a refusal, got %d\n%s", code, errOut)
		}
		if strings.Contains(errOut, "stopped at planned") {
			t.Errorf("a plan is not an incomplete move:\n%s", errOut)
		}
	})
	t.Run("prune", func(t *testing.T) {
		h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
		h.brew.With(model.Installation{Name: "jq", Version: "1.7"})
		h.uv.With(model.Installation{Name: "orphan", Version: "1.0"})
		code, _, errOut := h.run("prune", "--yes")
		if code != ExitRefusedDestructive || !strings.Contains(errOut, "orphan") {
			t.Fatalf("an unattended prune is refused, got %d\n%s", code, errOut)
		}
		if len(h.uv.RemoveCalls) != 0 {
			t.Error("nothing may be pruned")
		}
	})
}
