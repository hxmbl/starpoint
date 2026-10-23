package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

func (h *harness) noInstallsOrRemovals() {
	h.t.Helper()
	for _, p := range []*testsupport.FakeProvider{h.brew, h.uv, h.npm, h.cargo, h.rustup} {
		if len(p.InstallCalls)+len(p.RemoveCalls)+len(p.UpdateCalls) != 0 {
			h.t.Errorf("adopt must never install, remove or update (%s did)", p.Name())
		}
	}
}

func TestAdoptRecordsTheOwnerOfInstalledSoftware(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.With(model.Installation{Name: "jq", Version: "1.7"})
	out := h.mustRun("adopt", "jq")
	if !strings.Contains(h.configBody(), "name = \"jq\"") || !strings.Contains(h.configBody(), `provider = "brew"`) {
		t.Errorf("jq should be recorded with brew:\n%s", h.configBody())
	}
	if !h.asked() {
		t.Error("adopt changes the configuration, so it asks first")
	}
	if !strings.Contains(out, "recorded") {
		t.Errorf("output should say it recorded jq:\n%s", out)
	}
	h.noInstallsOrRemovals()
}

func TestAdoptDryRunChangesNothing(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.With(model.Installation{Name: "jq"})
	before := h.configBody()
	out := h.mustRun("adopt", "jq", "--dry-run")
	if h.configBody() != before {
		t.Errorf("a dry run must not write:\n%s", h.configBody())
	}
	if !strings.Contains(out, "would record") {
		t.Errorf("the preview should say what would be recorded:\n%s", out)
	}
}

func TestAdoptNeverRecordsSomethingThatIsNotInstalled(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	before := h.configBody()
	if code, _, _ := h.run("adopt", "nosuchtool"); code == 0 {
		t.Error("adopting something nothing has must fail")
	}
	if code, _, _ := h.run("adopt", "nosuchtool", "brew"); code == 0 {
		t.Error("adopting from a provider that does not have it must fail")
	}
	if h.configBody() != before {
		t.Errorf("nothing may be recorded:\n%s", h.configBody())
	}
	h.noInstallsOrRemovals()
}

func TestAdoptLeavesSystemSoftwareUnmanaged(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.sys.With(model.Installation{Name: "git", Path: "/usr/bin/git"})
	before := h.configBody()
	code, _, errOut := h.run("adopt", "git")
	if code == 0 || !strings.Contains(errOut, "operating system") {
		t.Errorf("system software is not adopted (exit %d): %s", code, errOut)
	}
	if h.configBody() != before {
		t.Error("nothing may be recorded")
	}
}

// Two providers have it: at a terminal Starpoint asks; unattended it refuses
// with a code and the candidates, rather than picking one.
func TestAdoptAsksOrRefusesWhenTwoProvidersHaveIt(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.With(model.Installation{Name: "ruff"})
	h.uv.With(model.Installation{Name: "ruff"})
	h.env.Choose = func(q string, options []string) (string, error) {
		h.choices = append(h.choices, q)
		return "uv", nil
	}
	h.mustRun("adopt", "ruff")
	if len(h.choices) != 1 || !strings.Contains(h.configBody(), `provider = "uv"`) {
		t.Errorf("the owner should be asked for and recorded (asked %d):\n%s", len(h.choices), h.configBody())
	}

	h2 := newHarness(t, "schema = 1\n")
	h2.brew.With(model.Installation{Name: "ruff"})
	h2.uv.With(model.Installation{Name: "ruff"})
	h2.unattended()
	h2.env.Choose = nil
	before := h2.configBody()
	code, _, errOut := h2.run("adopt", "ruff", "--yes", "--json")
	if code != ExitRefusedDecision {
		t.Fatalf("exit = %d, want %d: %s", code, ExitRefusedDecision, errOut)
	}
	var doc struct {
		Code       string   `json:"code"`
		Package    string   `json:"package"`
		Candidates []string `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(errOut), &doc); err != nil {
		t.Fatalf("%v: %s", err, errOut)
	}
	if doc.Code != CodeOwnerAmbiguous || doc.Package != "ruff" || strings.Join(doc.Candidates, ",") != "brew,uv" {
		t.Errorf("refusal = %+v", doc)
	}
	if h2.configBody() != before {
		t.Error("a refusal changes nothing")
	}
}

// Recording the copy that is really there, when the configuration named
// another provider: a configuration change only, nothing moves.
func TestAdoptRerecordsTheRealOwner(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"uv\"\n")
	h.brew.With(model.Installation{Name: "ripgrep"})
	h.mustRun("adopt", "ripgrep", "brew")
	if !strings.Contains(h.configBody(), `provider = "brew"`) || strings.Contains(h.configBody(), `provider = "uv"`) {
		t.Errorf("ripgrep should now be recorded with brew:\n%s", h.configBody())
	}
	h.noInstallsOrRemovals()
}

// --all takes what providers say was installed on request, and nothing else.
func TestAdoptAllTakesOnlyRequestedSoftware(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"black\"\nprovider = \"brew\"\n")
	h.brew.With(model.Installation{Name: "jq"}).With(model.Installation{Name: "oniguruma"}).With(model.Installation{Name: "mystery"})
	h.env.Registry.Register(&testsupport.RequestedFake{FakeProvider: h.brew, Marks: map[string]bool{"jq": true, "oniguruma": false}})
	h.uv.With(model.Installation{Name: "ruff"}).With(model.Installation{Name: "black"})
	h.env.Registry.Register(testsupport.ListingFake{FakeProvider: h.uv})
	h.npm.With(model.Installation{Name: "typescript"})
	h.sys.With(model.Installation{Name: "git", Path: "/usr/bin/git"})

	var doc struct {
		Actions []struct {
			Package  string `json:"package"`
			Provider string `json:"provider"`
		} `json:"actions"`
		Skipped []adoptSkip `json:"skipped"`
		Left    []adoptLeft `json:"left_unmanaged"`
	}
	h.decode(h.mustRun("adopt", "--all", "--yes", "--json"), &doc)
	got := map[string]string{}
	for _, a := range doc.Actions {
		got[a.Package] = a.Provider
	}
	if got["jq"] != "brew" || got["ruff"] != "uv" || len(got) != 2 {
		t.Errorf("adopted %v, want jq via brew and ruff via uv only", got)
	}
	body := h.configBody()
	for _, name := range []string{"oniguruma", "mystery", "typescript", "git"} {
		if strings.Contains(body, `"`+name+`"`) {
			t.Errorf("%s must stay unmanaged:\n%s", name, body)
		}
	}
	if strings.Count(body, `provider = "uv"`) != 1 || strings.Count(body, `provider = "brew"`) != 2 {
		t.Errorf("black is configured with brew and must not be re-pointed by --all:\n%s", body)
	}
	foundBlack := false
	for _, s := range doc.Skipped {
		foundBlack = foundBlack || (s.Package == "black" && strings.Contains(s.Reason, "transition"))
	}
	if !foundBlack {
		t.Errorf("black should be reported as left alone with the command that settles it: %+v", doc.Skipped)
	}
	left := map[string]adoptLeft{}
	for _, l := range doc.Left {
		left[l.Provider] = l
	}
	if left["brew"].Dependency != 1 || left["brew"].Unknown != 1 || left["npm"].Unknown != 1 {
		t.Errorf("left_unmanaged = %+v", doc.Left)
	}
	h.noInstallsOrRemovals()
}
