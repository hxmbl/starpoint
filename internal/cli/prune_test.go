package cli

import (
	"encoding/json"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/report"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// prune must refuse when the configuration declares nothing. With no source of
// truth, every user-scoped package looks orphaned; acting on that wipes the
// machine. This is the regression test for exactly that accident.
func TestPruneRefusesWhenNothingIsDeclared(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.With(model.Installation{Name: "orphan", Version: "1.0"})

	code, out, errOut := h.run("prune", "--no-confirm")
	if code == 0 {
		t.Fatalf("prune must refuse an empty configuration\nstdout:\n%s", out)
	}
	if !strings.Contains(errOut, "declares no packages") {
		t.Errorf("the refusal should explain itself: %s", errOut)
	}
	if len(h.brew.RemoveCalls) != 0 {
		t.Errorf("nothing may be removed: %v", h.brew.RemoveCalls)
	}
}

// A dry run has to name the candidates. Reporting nothing was how a user could
// be told "nothing to do" and still have the next real run delete things.
func TestPruneDryRunListsCandidatesInJSON(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	h.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0"})
	h.brew.With(model.Installation{Name: "orphan", Version: "1.0"})

	out := h.mustRun("prune", "--dry-run", "--json")
	var payload struct {
		Operation string `json:"operation"`
		DryRun    bool   `json:"dry_run"`
		Actions   []struct {
			Action  string `json:"action"`
			Package string `json:"package"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("stdout must be one JSON document: %v\n%s", err, out)
	}
	if payload.Operation != "prune" || !payload.DryRun {
		t.Fatalf("unexpected prune JSON: %s", out)
	}
	if len(payload.Actions) != 1 {
		t.Fatalf("the dry run should list exactly one candidate: %+v", payload.Actions)
	}
	if payload.Actions[0].Package != "orphan" || payload.Actions[0].Action != "remove" {
		t.Errorf("the configured package is not a candidate: %+v", payload.Actions[0])
	}
}

func TestPruneDryRunNamesWhatWouldGo(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	h.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0"})
	h.brew.With(model.Installation{Name: "orphan", Version: "1.0"})

	out := h.mustRun("prune", "--dry-run")
	if !strings.Contains(out, "Would remove orphan") {
		t.Errorf("the dry run should name the candidate:\n%s", out)
	}
	if strings.Contains(out, "ripgrep") {
		t.Errorf("configured software is not a candidate:\n%s", out)
	}
}

// info used to print its package state and then a second document for the
// provider survey, which is not valid JSON for a consumer to parse.
func TestInfoJSONIsOneDocument(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.sys.With(model.Installation{Name: "git", Version: "2.43.0", Path: "/usr/bin/git"})
	h.apt.IsAvailable = true

	out := h.mustRun("info", "git", "--json")
	dec := json.NewDecoder(strings.NewReader(out))
	var first map[string]any
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("info --json must emit JSON: %v\n%s", err, out)
	}
	var extra map[string]any
	if err := dec.Decode(&extra); err != io.EOF {
		t.Errorf("info --json emitted more than one JSON document: %v\n%s", err, out)
	}
	if _, ok := first["package"]; !ok {
		t.Errorf("the package state should be in the document:\n%s", out)
	}
	if _, ok := first["lookup"]; !ok {
		t.Errorf("the provider survey should be folded into the same document:\n%s", out)
	}
	if v, ok := first["schema_version"].(float64); !ok || v != report.SchemaVersion {
		t.Errorf("schema_version = %v, want %d", first["schema_version"], report.SchemaVersion)
	}
}

// Prune removes only what lives in the user's home. A provider whose global
// prefix is a system location (npm under /usr, a Python image's
// site-packages, CARGO_HOME under /usr/local) lists software that belongs to
// the machine; running prune as root over SSH must not uninstall it.
func TestPruneLeavesSoftwareOutsideTheHomeAlone(t *testing.T) {
	h, bin := binHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
	mine := filepath.Join(bin, "mine")
	testsupport.WriteFile(t, mine, "#!/bin/sh\n")
	machine := filepath.Join(t.TempDir(), "usr", "lib", "node_modules", "machine")
	testsupport.WriteFile(t, machine, "#!/bin/sh\n")
	h.uv.With(model.Installation{Name: "mine", Version: "1.0", Path: mine})
	h.uv.With(model.Installation{Name: "machine", Version: "1.0", Path: machine})

	op := decodeOperation(t, h.mustRun("prune", "--dry-run", "--json"))
	if len(op.Actions) != 1 || op.Actions[0].Package != "mine" {
		t.Fatalf("only the package in the home is a candidate: %+v", op.Actions)
	}

	h.mustRun("prune", "--yes", "--allow-destructive")
	if len(h.uv.RemoveCalls) != 1 || h.uv.RemoveCalls[0].Name != "mine" {
		t.Errorf("prune removed %v; software outside the home must never be removed", h.uv.RemoveCalls)
	}
}

// A package manager is never an orphan: npm lists npm and corepack among its
// own globals, and uv or pnpm may have been installed through pip or npm.
// Starpoint itself is one too, under either executable name.
// Pruning them would take a provider away.
func TestPruneNeverRemovesAPackageManager(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
	for _, name := range []string{"npm", "corepack", "uv", "str", "starpoint", "orphan"} {
		h.uv.With(model.Installation{Name: name, Version: "1.0"})
	}

	op := decodeOperation(t, h.mustRun("prune", "--dry-run", "--json"))
	if len(op.Actions) != 1 || op.Actions[0].Package != "orphan" {
		t.Fatalf("package managers are not prune candidates: %+v", op.Actions)
	}
}

// Prune names every orphan it leaves alone, with the reason, in human output
// and in JSON. Skipping software outside the home or a package manager
// without a word left a user wondering why prune "missed" it.
func TestPruneFlagsWhatItSkips(t *testing.T) {
	h, bin := binHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
	mine := filepath.Join(bin, "mine")
	testsupport.WriteFile(t, mine, "#!/bin/sh\n")
	machine := filepath.Join(t.TempDir(), "usr", "lib", "node_modules", "machine")
	testsupport.WriteFile(t, machine, "#!/bin/sh\n")
	h.uv.With(model.Installation{Name: "mine", Version: "1.0", Path: mine})
	h.uv.With(model.Installation{Name: "machine", Version: "1.0", Path: machine})
	h.uv.With(model.Installation{Name: "npm", Version: "10.0"})

	op := decodeOperation(t, h.mustRun("prune", "--dry-run", "--json"))
	if len(op.Actions) != 1 || op.Actions[0].Package != "mine" {
		t.Fatalf("only the package in the home is a candidate: %+v", op.Actions)
	}
	reasons := map[string]string{}
	for _, w := range op.Warnings {
		if w.Code == model.WarnPruneSkipped {
			if w.Provider != "uv" {
				t.Errorf("a skipped candidate names its provider: %+v", w)
			}
			reasons[w.Package] = w.Message
		}
	}
	if !strings.Contains(reasons["machine"], "outside your home directory") || !strings.Contains(reasons["machine"], machine) {
		t.Errorf("software outside the home is flagged with where it is: %q", reasons["machine"])
	}
	if !strings.Contains(reasons["npm"], "package manager") {
		t.Errorf("a package manager is flagged as one: %q", reasons["npm"])
	}
	if len(reasons) != 2 {
		t.Errorf("exactly the two skipped candidates are flagged: %v", reasons)
	}
	if hasCode(op.Actions[0].Warnings, model.WarnPruneSkipped) {
		t.Errorf("a skip warning does not belong to a candidate that is removed: %+v", op.Actions[0].Warnings)
	}

	for _, args := range [][]string{{"prune", "--dry-run"}, {"prune", "--yes", "--allow-destructive"}} {
		out := h.mustRun(args...)
		for _, want := range []string{"[prune-skipped]", "not removing machine (uv)", "not removing npm (uv)"} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: human output lists what was left alone (%q):\n%s", args, want, out)
			}
		}
	}
	if len(h.uv.RemoveCalls) != 1 || h.uv.RemoveCalls[0].Name != "mine" {
		t.Errorf("prune removed %v", h.uv.RemoveCalls)
	}
}

// With nothing left to remove, what was skipped is still listed rather than
// hidden behind "nothing to do".
func TestPruneListsSkipsWhenNothingIsRemoved(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
	h.uv.With(model.Installation{Name: "corepack", Version: "0.29"})

	out := h.mustRun("prune", "--dry-run")
	if !strings.Contains(out, "No orphaned software found.") || !strings.Contains(out, "not removing corepack (uv)") {
		t.Errorf("an empty prune still lists what it left alone:\n%s", out)
	}
}
