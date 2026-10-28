package cli

import (
	"strings"
	"testing"
)

func TestInvokedNameAnswersToStrAndTheOldName(t *testing.T) {
	cases := map[string]string{
		"str":                         "str",
		"/usr/local/bin/str":          "str",
		"starpoint":                   "starpoint",
		"/opt/homebrew/bin/starpoint": "starpoint",
		"./bin/starpoint":             "starpoint",
		"str-dev":                     "str",
		"":                            "str",
	}
	for arg0, want := range cases {
		if got := invokedName([]string{arg0, "list"}); got != want {
			t.Errorf("invokedName(%q) = %q, want %q", arg0, got, want)
		}
	}
	if got := invokedName(nil); got != "str" {
		t.Errorf("invokedName(nil) = %q, want str", got)
	}
}

func TestLegacyNameNoticeOnlyForPeopleAtATerminal(t *testing.T) {
	if note := legacyNameNotice("starpoint", true); !strings.Contains(note, "'str'") || !strings.Contains(note, ".starpoint.toml") {
		t.Errorf("the old name at a terminal should say the new name and that the config is unchanged: %q", note)
	}
	// Scripts capture stderr; they must see nothing new.
	if note := legacyNameNotice("starpoint", false); note != "" {
		t.Errorf("no notice when stderr is not a terminal, got %q", note)
	}
	if note := legacyNameNotice("str", true); note != "" {
		t.Errorf("no notice under the new name, got %q", note)
	}
}

func TestVersionNamesTheExecutableItWasStartedAs(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	if out := h.mustRun("--version"); !strings.HasPrefix(out, "str ") {
		t.Errorf("str --version should start with 'str ': %q", out)
	}
	h.env.Name = "starpoint"
	if out := h.mustRun("--version"); !strings.HasPrefix(out, "starpoint ") {
		t.Errorf("starpoint --version should keep its old first word for scripts: %q", out)
	}
}
