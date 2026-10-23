package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

type decisionRefusal struct {
	Code       string   `json:"code"`
	ExitCode   int      `json:"exit_code"`
	Package    string   `json:"package"`
	Candidates []string `json:"candidates"`
}

func decodeDecision(t *testing.T, errOut string) decisionRefusal {
	t.Helper()
	var r decisionRefusal
	if err := json.Unmarshal([]byte(errOut), &r); err != nil {
		t.Fatalf("stderr must be one JSON document: %v\n%s", err, errOut)
	}
	return r
}

// Naming a provider other than the one that has the software is the
// duplicate-install mistake: unattended it is refused with a code, and only
// --allow-duplicate makes the second copy.
func TestInstallRefusesASecondCopyUnattended(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.With(model.Installation{Name: "ruff", Version: "0.5.0"})
	h.unattended()
	before := h.configBody()
	code, _, errOut := h.run("install", "ruff", "uv", "--yes", "--json")
	if code != ExitRefusedDecision {
		t.Fatalf("exit = %d, want %d\n%s", code, ExitRefusedDecision, errOut)
	}
	r := decodeDecision(t, errOut)
	if r.Code != CodeDuplicateInstall || r.Package != "ruff" || strings.Join(r.Candidates, ",") != "brew" {
		t.Errorf("refusal = %+v", r)
	}
	if len(h.uv.InstallCalls) != 0 || h.configBody() != before {
		t.Error("a refusal changes nothing")
	}

	h.mustRun("install", "ruff", "uv", "--yes", "--allow-duplicate")
	if len(h.uv.InstallCalls) != 1 {
		t.Errorf("--allow-duplicate installs the second copy, got %d installs", len(h.uv.InstallCalls))
	}
}

// At a terminal the duplicate is shown before the question, not refused.
func TestInstallWarnsAboutASecondCopyAtATerminal(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.With(model.Installation{Name: "ruff", Version: "0.5.0"})
	h.ask(false)
	_, out, errOut := h.run("install", "ruff", "uv")
	if !strings.Contains(out+errOut, "second copy") {
		t.Errorf("the confirmation should warn about the second copy:\n%s\n%s", out, errOut)
	}
	if len(h.uv.InstallCalls) != 0 {
		t.Error("declining installs nothing")
	}
}

// Q4: only the operating system has it. Unattended, Starpoint does not add a
// managed copy on its own initiative; naming the provider is the request.
func TestInstallBesideASystemCopyNeedsTheProviderNamedUnattended(t *testing.T) {
	h2 := newHarness(t, "schema = 1\n")
	h2.sys.With(model.Installation{Name: "zsh", Path: "/usr/bin/zsh"})
	h2.uv.IsAvailable = false
	h2.cargo.IsAvailable = false
	h2.npm.IsAvailable = false
	h2.unattended()
	code, _, errOut := h2.run("install", "zsh", "--yes", "--json")
	if code != ExitRefusedDecision || decodeDecision(t, errOut).Code != CodeDuplicateInstall {
		t.Fatalf("expected duplicate-install, got %d\n%s", code, errOut)
	}
	if len(h2.brew.InstallCalls) != 0 {
		t.Error("nothing may be installed")
	}
	h2.mustRun("install", "zsh", "brew", "--yes")
	if len(h2.brew.InstallCalls) != 1 {
		t.Errorf("naming the provider asks for the managed copy, got %d installs", len(h2.brew.InstallCalls))
	}
}

// An unattended sync does not pick a provider nobody chose.
func TestSyncRefusesAnAmbiguousChoiceUnattended(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\n")
	before := h.configBody()
	code, _, errOut := h.run("sync", "--no-confirm", "--json")
	if code != ExitRefusedDecision {
		t.Fatalf("exit = %d, want %d\n%s", code, ExitRefusedDecision, errOut)
	}
	r := decodeDecision(t, errOut)
	if r.Code != CodeProviderAmbiguous || r.Package != "ripgrep" || len(r.Candidates) < 2 {
		t.Errorf("refusal = %+v", r)
	}
	if len(h.brew.InstallCalls) != 0 || h.configBody() != before {
		t.Error("a refusal changes nothing")
	}
	// A recorded provider is a decision; the same sync then runs.
	h2 := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	h2.mustRun("sync", "--no-confirm")
	if len(h2.brew.InstallCalls) != 1 {
		t.Error("a configured provider is not ambiguous")
	}
}
