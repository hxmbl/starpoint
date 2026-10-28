package cli

import (
	"strings"
	"testing"
)

// With no terminal to ask on, an install whose provider is ambiguous is the
// provider-ambiguous decision refusal (exit 5, with the candidates), like an
// unattended sync, not a bare error. Nothing is asked, installed or recorded.
func TestInstallRefusesAnAmbiguousProviderWithoutATerminal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *harness)
	}{
		// No confirmation function at all: the harness's unattended mode.
		{"no prompt", func(h *harness) { h.unattended() }},
		// The binary's own wiring: prompts exist, but stdin is not a
		// terminal (a pipe, a CI job, an agent over SSH).
		{"stdin is not a terminal", func(h *harness) { h.env.Interactive = func() bool { return false } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "schema = 1\n")
			tc.setup(h)
			before := h.configBody()
			code, _, errOut := h.run("install", "ripgrep", "--json")
			if code != ExitRefusedDecision {
				t.Fatalf("exit = %d, want %d\n%s", code, ExitRefusedDecision, errOut)
			}
			r := decodeDecision(t, errOut)
			if r.Code != CodeProviderAmbiguous || r.Package != "ripgrep" || len(r.Candidates) < 2 || r.ExitCode != ExitRefusedDecision {
				t.Errorf("refusal = %+v", r)
			}
			if len(h.choices) != 0 {
				t.Errorf("nobody can answer, so nothing may be asked: %v", h.choices)
			}
			if len(h.brew.InstallCalls)+len(h.cargo.InstallCalls) != 0 || h.configBody() != before {
				t.Error("a refusal changes nothing")
			}
			var logged bool
			for _, e := range h.auditEntries() {
				if e.Command == "install" && e.Result == "refused" && strings.Contains(e.Detail, CodeProviderAmbiguous) {
					logged = true
				}
			}
			if !logged {
				t.Error("the refusal is written to the audit log")
			}
		})
	}
}

// Naming the provider is the decision the refusal asks for, and a dry run,
// which changes nothing, is not refused.
func TestInstallAmbiguityWithoutATerminalIsSettledByNamingTheProvider(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.unattended()
	if code, out, errOut := h.run("install", "ripgrep", "--dry-run"); code != 0 {
		t.Fatalf("a dry run is not refused, got %d\n%s\n%s", code, out, errOut)
	}
	h.mustRun("install", "ripgrep", "brew", "--yes")
	if len(h.brew.InstallCalls) != 1 {
		t.Errorf("a named provider installs, got %d installs", len(h.brew.InstallCalls))
	}
}
