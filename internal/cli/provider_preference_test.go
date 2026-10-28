package cli

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

const preferCargo = "schema = 1\n\n[settings]\nproviders = [\"cargo\"]\n"

// [settings] providers answers the provider question ahead of time: nothing
// is asked, the unattended run is not refused, and the recorded reason says
// the setting decided.
func TestTheProviderPreferenceSettlesAnInstallNobodyIsAskedAbout(t *testing.T) {
	h := newHarness(t, preferCargo)
	h.unattended()
	h.mustRun("install", "ripgrep", "--yes")
	if len(h.choices) != 0 {
		t.Errorf("the preference answers the question: %v", h.choices)
	}
	if len(h.cargo.InstallCalls) != 1 || len(h.brew.InstallCalls) != 0 {
		t.Fatalf("cargo is preferred: cargo=%d brew=%d", len(h.cargo.InstallCalls), len(h.brew.InstallCalls))
	}
	body := h.configBody()
	if !strings.Contains(body, `provider = "cargo"`) || !strings.Contains(body, "[settings] providers") {
		t.Errorf("the decision and its reason are recorded:\n%s", body)
	}
}

// The same preference lets an unattended sync converge an entry with no
// provider instead of refusing it as provider-ambiguous.
func TestTheProviderPreferenceSettlesAnUnattendedSync(t *testing.T) {
	h := newHarness(t, preferCargo+"\n[[package]]\nname = \"ripgrep\"\n")
	h.mustRun("sync", "--yes")
	if len(h.cargo.InstallCalls) != 1 {
		t.Fatalf("sync installs through the preferred provider, got %d", len(h.cargo.InstallCalls))
	}
}

// An explicit provider on the command line, a recorded provider, and an
// existing installation all outrank the preference.
func TestTheProviderPreferenceNeverOverridesADecision(t *testing.T) {
	h := newHarness(t, preferCargo)
	h.mustRun("install", "ripgrep", "brew", "--yes")
	if len(h.brew.InstallCalls) != 1 || len(h.cargo.InstallCalls) != 0 {
		t.Errorf("the named provider wins: brew=%d cargo=%d", len(h.brew.InstallCalls), len(h.cargo.InstallCalls))
	}

	h2 := newHarness(t, preferCargo+"\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	h2.mustRun("sync", "--yes")
	if len(h2.brew.InstallCalls) != 1 || len(h2.cargo.InstallCalls) != 0 {
		t.Errorf("the recorded provider wins")
	}

	h3 := newHarness(t, preferCargo)
	h3.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0"})
	h3.mustRun("install", "ripgrep", "--yes")
	if len(h3.cargo.InstallCalls) != 0 {
		t.Error("an existing brew copy keeps ownership; the preference must not add a second copy")
	}
	if !strings.Contains(h3.configBody(), `provider = "brew"`) {
		t.Errorf("the existing owner is recorded:\n%s", h3.configBody())
	}
}

// A preferred provider that is not available leaves the question in place;
// unattended, that is still the provider-ambiguous refusal.
func TestAnUnavailablePreferredProviderLeavesTheQuestion(t *testing.T) {
	h := newHarness(t, preferCargo)
	h.cargo.IsAvailable = false
	h.unattended()
	h.env.Choose = nil
	code, _, errOut := h.run("install", "ripgrep", "--yes", "--json")
	if code == 0 {
		t.Fatalf("an unavailable preference must not decide:\n%s", errOut)
	}
	if len(h.brew.InstallCalls)+len(h.cargo.InstallCalls) != 0 {
		t.Error("nothing may be installed")
	}
}

// explain shows the preference as a rule.
func TestExplainShowsTheConfiguredPreference(t *testing.T) {
	h := newHarness(t, preferCargo)
	out := h.mustRun("explain", "ripgrep")
	if !strings.Contains(out, "configured preference") || !strings.Contains(out, "cargo") {
		t.Errorf("explain names the preference step:\n%s", out)
	}
}
