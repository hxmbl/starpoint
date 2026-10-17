package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/report"
	"github.com/hxmbl/starpoint/internal/retention"
)

// twoPackages is a configuration with enough in it for a bulk removal to mean
// something.
const twoPackages = "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n\n[[package]]\nname = \"cowsay\"\nprovider = \"brew\"\n"

func stateDirOf(t *testing.T, h *harness) string {
	t.Helper()
	dir, err := config.StateDir(h.config)
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// Every command leaves a record, whether it succeeded or not. A log that only
// captures the successes is not a record of what the tool did.
func TestEveryCommandIsRecorded(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.mustRun("list")

	entries := h.auditEntries()
	if len(entries) < 2 {
		t.Fatalf("expected a start and a finish entry, got %+v", entries)
	}
	start, finish := entries[0], entries[len(entries)-1]
	if start.Event != auditlog.EventCommandStart || start.Command != "list" {
		t.Errorf("first entry should be the start of list: %+v", start)
	}
	if finish.Event != auditlog.EventCommandFinish || finish.Exit != 0 {
		t.Errorf("last entry should be a successful finish: %+v", finish)
	}
	if start.Directory == "" {
		t.Error("an entry without a directory cannot answer where it happened")
	}
}

// A command that fails is still recorded, with the failure.
func TestAFailedCommandIsRecorded(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	if code, _, _ := h.run("info", "definitely-not-a-package-xyz"); code == 0 {
		t.Fatal("expected the command to fail")
	}
	finishes := h.withEvent(auditlog.EventCommandFinish)
	if len(finishes) == 0 {
		t.Fatal("a failed command left no record")
	}
	last := finishes[len(finishes)-1]
	if last.Result != "failed" || last.Exit == 0 || last.Detail == "" {
		t.Errorf("the failure should be recorded: %+v", last)
	}
}

// What Starpoint did to the machine is the point of the log.
func TestAnInstallIsRecordedAsAnAction(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.mustRun("install", "ripgrep", "--no-confirm")

	actions := h.withEvent(auditlog.EventAction)
	if len(actions) != 1 {
		t.Fatalf("expected one action, got %+v", actions)
	}
	a := actions[0]
	if a.Package != "ripgrep" || a.Provider != "brew" || a.Result == "" {
		t.Errorf("the action should say what happened to what: %+v", a)
	}
}

func TestARemovalIsRecordedAsAnAction(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	h.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0"})
	h.mustRun("remove", "ripgrep", "--no-confirm", "--allow-destructive")

	actions := h.withEvent(auditlog.EventAction)
	if len(actions) != 1 || actions[0].Package != "ripgrep" {
		t.Errorf("the removal should be recorded: %+v", actions)
	}
	if !strings.Contains(actions[0].Action, "remove") {
		t.Errorf("the action should be a removal: %+v", actions[0])
	}
}

// The whole point of the guard: a bulk removal that empties the configuration is
// refused, and the refusal is itself on the record.
func TestRemovingEveryConfiguredPackageIsRefused(t *testing.T) {
	h := newHarness(t, twoPackages)
	h.brew.With(model.Installation{Name: "ripgrep"})
	h.brew.With(model.Installation{Name: "cowsay"})

	code, _, errOut := h.run("remove", "ripgrep", "cowsay", "--no-confirm")
	if code == 0 {
		t.Fatal("removing every configured package should be refused")
	}
	if !strings.Contains(errOut, "--allow-empty") {
		t.Errorf("the refusal should say how to mean it: %s", errOut)
	}
	if len(h.brew.RemoveCalls) != 0 {
		t.Errorf("nothing may be removed: %v", h.brew.RemoveCalls)
	}
	if !strings.Contains(h.configBody(), "ripgrep") || !strings.Contains(h.configBody(), "cowsay") {
		t.Errorf("the configuration should be untouched:\n%s", h.configBody())
	}

	guards := h.withEvent(auditlog.EventGuard)
	if len(guards) != 1 || guards[0].Result != "refused" {
		t.Errorf("the refusal should be on the record: %+v", guards)
	}
}

func TestAllowEmptyMeansIt(t *testing.T) {
	h := newHarness(t, twoPackages)
	h.brew.With(model.Installation{Name: "ripgrep"})
	h.brew.With(model.Installation{Name: "cowsay"})

	h.mustRun("remove", "ripgrep", "cowsay", "--allow-empty", "--no-confirm", "--allow-destructive")
	if len(h.brew.RemoveCalls) != 2 {
		t.Errorf("expected both removals, got %v", h.brew.RemoveCalls)
	}
	if len(h.withEvent(auditlog.EventGuard)) != 0 {
		t.Error("an explicit --allow-empty is not a refusal")
	}
	// Having meant it, the removals are still on the record.
	if actions := h.withEvent(auditlog.EventAction); len(actions) != 2 {
		t.Errorf("expected both removals recorded: %+v", actions)
	}
}

// The guard is about bulk removal, not about the configuration ever becoming
// empty. These two are ordinary and must keep working.
func TestOrdinaryRemovalsAreNotRefused(t *testing.T) {
	t.Run("one of several", func(t *testing.T) {
		h := newHarness(t, twoPackages)
		h.brew.With(model.Installation{Name: "ripgrep"})
		h.brew.With(model.Installation{Name: "cowsay"})
		h.mustRun("remove", "ripgrep", "--no-confirm", "--allow-destructive")
		if len(h.brew.RemoveCalls) != 1 {
			t.Errorf("expected one removal, got %v", h.brew.RemoveCalls)
		}
	})
	t.Run("the only package", func(t *testing.T) {
		h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
		h.brew.With(model.Installation{Name: "ripgrep"})
		h.mustRun("remove", "ripgrep", "--no-confirm", "--allow-destructive")
		if len(h.brew.RemoveCalls) != 1 {
			t.Errorf("expected one removal, got %v", h.brew.RemoveCalls)
		}
	})
}

// A dry run changes nothing, so it must be allowed to describe the change.
func TestABulkRemovalCanStillBePreviewed(t *testing.T) {
	h := newHarness(t, twoPackages)
	h.brew.With(model.Installation{Name: "ripgrep"})
	h.brew.With(model.Installation{Name: "cowsay"})

	out := h.mustRun("remove", "ripgrep", "cowsay", "--dry-run")
	if !strings.Contains(out, "ripgrep") || !strings.Contains(out, "cowsay") {
		t.Errorf("the preview should describe both removals:\n%s", out)
	}
	if len(h.brew.RemoveCalls) != 0 {
		t.Errorf("a dry run removes nothing: %v", h.brew.RemoveCalls)
	}
}

// Retention is not a command anybody has to remember. It runs on every command.
func TestRetentionRunsWithoutBeingAsked(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	stale := filepath.Join(stateDirOf(t, h), "logs", "audit-2020-01-01.jsonl")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.mustRun("list")

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("an expired log should have been deleted by an ordinary command")
	}
}

func TestConfiguredRetentionIsHonoured(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[settings]\nretention_days = 7\n")
	stale := filepath.Join(stateDirOf(t, h), "logs", "audit-"+time.Now().Add(-10*24*time.Hour).UTC().Format("2006-01-02")+".jsonl")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Inside the configured window, so it must survive even though it is older
	// than the default 30 days.
	keep := filepath.Join(stateDirOf(t, h), "logs", "audit-"+time.Now().Add(-5*24*time.Hour).UTC().Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(keep, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.mustRun("list")

	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("10 days is outside a 7-day window and should have been deleted")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("5 days is inside a 7-day window and must survive: %v", err)
	}
}

func TestLoggingStatusReportsTheWindowAndTheSystemLogger(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[settings]\nretention_days = 45\n")
	out := h.mustRun("--json", "logging")

	var payload struct {
		SchemaVersion   int    `json:"schema_version"`
		StateDir        string `json:"state_dir"`
		AuditLogDir     string `json:"audit_log_dir"`
		RetentionDays   int    `json:"retention_days"`
		RetentionSource string `json:"retention_source"`
		OS              struct {
			Supported bool   `json:"supported"`
			Mechanism string `json:"mechanism"`
			Path      string `json:"path"`
		} `json:"os"`
	}
	h.decode(out, &payload)

	if payload.SchemaVersion != report.SchemaVersion {
		t.Errorf("schema_version = %d", payload.SchemaVersion)
	}
	if payload.RetentionDays != 45 || payload.RetentionSource != "config" {
		t.Errorf("retention = %d (%s)", payload.RetentionDays, payload.RetentionSource)
	}
	if payload.StateDir == "" || payload.AuditLogDir == "" {
		t.Errorf("the state directory should be reported: %+v", payload)
	}
	if !payload.OS.Supported {
		t.Skip("this platform has no system log configuration")
	}
	if payload.OS.Mechanism != "logrotate" && payload.OS.Mechanism != "newsyslog" {
		t.Errorf("mechanism = %q", payload.OS.Mechanism)
	}
	if filepath.Dir(payload.OS.Path) != filepath.Join(stateDirOf(t, h), "logging") {
		t.Errorf("Starpoint must write the configuration inside its own state directory: %q", payload.OS.Path)
	}
}

func TestLoggingConfigStaysInItsOwnDirectory(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	out := h.mustRun("--json", "logging", "config")

	var payload struct {
		Operation string `json:"operation"`
		Path      string `json:"path"`
		Written   bool   `json:"written"`
		System    string `json:"system_path"`
		Install   string `json:"install"`
	}
	h.decode(out, &payload)
	if !payload.Written || payload.Path == "" {
		t.Fatalf("nothing was written: %s", out)
	}
	want := filepath.Join(stateDirOf(t, h), "logging")
	if filepath.Dir(payload.Path) != want {
		t.Errorf("path = %q, want it under %q", payload.Path, want)
	}
	body, err := os.ReadFile(payload.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !auditLogGenerated(body) {
		t.Errorf("the generated file should identify itself:\n%s", body)
	}
	// The install hint tells the user the privileged step; Starpoint takes it
	// itself.
	if payload.System == "" || !strings.HasPrefix(payload.Install, "sudo cp ") {
		t.Errorf("the install hint should be an explicit privileged step: %+v", payload)
	}
}

// Running it twice must not rewrite or duplicate anything.
func TestLoggingConfigIsIdempotent(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.mustRun("logging", "config")
	second := h.mustRun("--json", "logging", "config")
	var payload struct {
		Written bool `json:"written"`
	}
	h.decode(second, &payload)
	if payload.Written {
		t.Error("an unchanged configuration should not be rewritten")
	}
}

func TestLoggingConfigWillNotOverwriteAFileItDidNotWrite(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	dir := filepath.Join(stateDirOf(t, h), "logging")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, "starpoint.logrotate")
	original := "this belongs to the user\n"
	if err := os.WriteFile(foreign, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errOut := h.run("logging", "config"); code != 0 {
		t.Fatalf("logging config failed: %s", errOut)
	}
	body, err := os.ReadFile(foreign)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != original {
		t.Errorf("a file Starpoint did not write was overwritten:\n%s", body)
	}
}

func TestLoggingPruneReportsWhatItRemoved(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	stale := filepath.Join(stateDirOf(t, h), "logs", "audit-2019-05-05.jsonl")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.mustRun("list") // ages it out, so prune has nothing left to report

	out := h.mustRun("--json", "logging", "prune")
	var payload struct {
		Operation string `json:"operation"`
		Days      int    `json:"retention_days"`
		Removed   int    `json:"audit_logs_removed"`
	}
	h.decode(out, &payload)
	if payload.Operation != "logging.prune" {
		t.Errorf("operation = %q", payload.Operation)
	}
	if payload.Days != retention.DefaultDays {
		t.Errorf("retention_days = %d, want the %d-day default", payload.Days, retention.DefaultDays)
	}
	if payload.Removed != 0 {
		t.Errorf("the expired log was already gone: %+v", payload)
	}
}

func TestLoggingRejectsWhatItDoesNotUnderstand(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	if code, _, _ := h.run("logging", "frobnicate"); code == 0 {
		t.Error("an unknown subcommand should be a usage error")
	}
	if code, _, _ := h.run("logging", "prune", "config"); code == 0 {
		t.Error("two subcommands should be a usage error")
	}
}

// auditLogGenerated reports whether a body identifies itself as ours. It
// duplicates the check Starpoint makes, deliberately, so a change to that check
// has to break this test too.
func auditLogGenerated(body []byte) bool {
	return strings.Contains(string(body), "generated by 'starpoint logging config'")
}
