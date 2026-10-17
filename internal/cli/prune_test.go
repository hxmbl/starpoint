package cli

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/report"
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
