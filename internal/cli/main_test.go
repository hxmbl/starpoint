package cli

import (
	"strings"
	"testing"
)

func TestVersionNamesStr(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	if out := h.mustRun("--version"); !strings.HasPrefix(out, "str ") {
		t.Errorf("str --version should start with 'str ': %q", out)
	}
}
