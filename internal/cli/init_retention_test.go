package cli

import (
	"strings"
	"testing"
)

// init --force keeps an explicit retention_days = 0, and a fresh init does
// not invent one.
func TestInitKeepsAnExplicitZeroRetentionAndInventsNone(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[settings]\nretention_days = 0\n")
	h.mustRun("init", "--force")
	if !strings.Contains(h.configBody(), "retention_days = 0") {
		t.Errorf("an explicit retention_days = 0 was dropped:\n%s", h.configBody())
	}

	h2 := newHarness(t, "schema = 1\n")
	h2.mustRun("init", "--force")
	if strings.Contains(h2.configBody(), "retention_days") {
		t.Errorf("init wrote a retention_days nobody set:\n%s", h2.configBody())
	}
	h2.mustRun("install", "ripgrep", "brew")
	if strings.Contains(h2.configBody(), "retention_days") {
		t.Errorf("a config write invented retention_days:\n%s", h2.configBody())
	}
}
