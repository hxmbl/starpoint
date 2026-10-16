package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

const pinnedRuff = "schema = 1\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\nversion = \"0.5.0\"\n"

func pinnedHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, pinnedRuff)
	h.uv.With(model.Installation{Name: "ruff", Version: "0.5.0"})
	h.uv.Latest = map[string]string{"ruff": "0.6.0"}
	return h
}

// The spec's example: "!!! There is a new version available for python, but
// config says 1.3 / Update anyway?"
func TestUpdateExplainsAPinConflictAndUpdatesWhenTold(t *testing.T) {
	h := pinnedHarness(t)
	h.ask(true)
	out := h.mustRun("update")
	if !strings.Contains(out, "!!! There is a new version available for ruff (0.6.0), but config says 0.5.0") {
		t.Errorf("the conflict should be explained:\n%s", out)
	}
	found := false
	for _, q := range h.questions {
		if q == "Update anyway?" {
			found = true
		}
	}
	if !found {
		t.Errorf("the user should be asked, got %v", h.questions)
	}
	if len(h.uv.UpdateCalls) != 1 || h.uv.UpdateCalls[0].Version != "0.6.0" {
		t.Errorf("ruff should be updated to 0.6.0, got %+v", h.uv.UpdateCalls)
	}
	if !strings.Contains(h.configBody(), `version = "0.6.0"`) {
		t.Errorf("the pin should be rewritten:\n%s", h.configBody())
	}
}

func TestUpdateKeepsThePinWhenTheAnswerIsNo(t *testing.T) {
	h := pinnedHarness(t)
	h.ask(false)
	out := h.mustRun("update")
	if len(h.uv.UpdateCalls) != 0 {
		t.Error("a refused conflict must not update")
	}
	if !strings.Contains(h.configBody(), `version = "0.5.0"`) {
		t.Errorf("the pin must stand:\n%s", h.configBody())
	}
	if !strings.Contains(out, "the pin was kept") {
		t.Errorf("the outcome should be stated:\n%s", out)
	}
}

func TestUpdateWithNoConfirmNeverMovesAPin(t *testing.T) {
	h := pinnedHarness(t)
	out := h.mustRun("update", "--no-confirm", "--json")
	if len(h.questions) != 0 || len(h.uv.UpdateCalls) != 0 {
		t.Errorf("--no-confirm must neither ask nor update: questions=%v updates=%d", h.questions, len(h.uv.UpdateCalls))
	}
	if !strings.Contains(h.configBody(), `version = "0.5.0"`) {
		t.Errorf("the pin must stand:\n%s", h.configBody())
	}
	var payload struct {
		PinConflicts []model.PinConflict `json:"pin_conflicts"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decoding: %v\n%s", err, out)
	}
	if len(payload.PinConflicts) != 1 {
		t.Fatalf("the conflict should be in the JSON: %s", out)
	}
	c := payload.PinConflicts[0]
	if c.Package != "ruff" || c.Pinned != "0.5.0" || c.Available != "0.6.0" || c.Resolution != "kept" {
		t.Errorf("unexpected conflict %+v", c)
	}
}

func TestUpdateDryRunReportsAPendingConflict(t *testing.T) {
	h := pinnedHarness(t)
	out := h.mustRun("update", "--dry-run")
	if len(h.uv.UpdateCalls) != 0 || len(h.questions) != 0 {
		t.Error("a dry run neither updates nor asks")
	}
	if !strings.Contains(out, "would ask") {
		t.Errorf("the dry run should say a question is coming:\n%s", out)
	}
}

func TestUpdateWithASatisfiedPinAndNothingNewerIsQuiet(t *testing.T) {
	h := pinnedHarness(t)
	h.uv.Latest = map[string]string{"ruff": "0.5.0"}
	out := h.mustRun("update")
	if strings.Contains(out, "!!!") || len(h.questions) != 0 {
		t.Errorf("no conflict, no question:\n%s", out)
	}
	if !strings.Contains(out, "pinned to 0.5.0; left unchanged") {
		t.Errorf("the pin should be acknowledged:\n%s", out)
	}
}
