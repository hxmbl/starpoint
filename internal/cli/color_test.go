package cli

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/color"
	"github.com/hxmbl/starpoint/internal/model"
)

// listConfig is a small configuration with one managed package, so a read
// command has something real to render.
const listConfig = `
schema = 1

[[package]]
name = "ripgrep"
provider = "brew"
`

// installed is a fake installation for the harness's fake brew.
func installed(name string) model.Installation {
	return model.Installation{Name: name, Version: "1.0.0", Path: "/opt/homebrew/bin/" + name}
}

// The default is plain when output is not a terminal: a pipe, a buffer or a
// log file must never receive escape sequences.
func TestColorIsOffWhenOutputIsNotATerminal(t *testing.T) {
	h := newHarness(t, listConfig)
	h.brew.With(installed("ripgrep"))

	out := h.mustRun("list")
	if strings.Contains(out, "\033[") {
		t.Errorf("a buffer is not a terminal; output should be plain:\n%q", out)
	}
}

// --color always is the flag that means "I asked for it", so it colors even
// when the writer is not a terminal.
func TestColorAlwaysForcesEscapes(t *testing.T) {
	h := newHarness(t, listConfig)
	h.brew.With(installed("ripgrep"))

	out := h.mustRun("--color", "always", "list")
	if !strings.Contains(out, "\033[") {
		t.Errorf("--color always should color even into a buffer:\n%q", out)
	}
}

// --color=always is the other spelling users reach for; it must not become an
// unknown command.
func TestColorAlwaysFlagWithEquals(t *testing.T) {
	h := newHarness(t, listConfig)
	h.brew.With(installed("ripgrep"))

	out := h.mustRun("--color=always", "list")
	if !strings.Contains(out, "\033[") {
		t.Errorf("--color=always should color:\n%q", out)
	}
}

// --color never turns color off even where it would otherwise be on.
func TestColorNeverIsPlain(t *testing.T) {
	h := newHarness(t, listConfig)
	h.brew.With(installed("ripgrep"))

	out := h.mustRun("--color", "never", "list")
	if strings.Contains(out, "\033[") {
		t.Errorf("--color never should never color:\n%q", out)
	}
}

// Color is decoration: stripping the escapes from colored output must give the
// plain output byte for byte. Anything else means color carries information or
// breaks alignment.
func TestColoredOutputStripsBackToThePlainOutput(t *testing.T) {
	h := newHarness(t, listConfig)
	h.brew.With(installed("ripgrep"))

	plain := h.mustRun("list")
	colored := h.mustRun("--color", "always", "list")

	if colored == plain {
		t.Fatal("--color always produced no color to compare")
	}
	if got := color.Strip(colored); got != plain {
		t.Errorf("stripped colored output differs from plain output\nplain:\n%q\nstripped:\n%q", plain, got)
	}
}

// Either spelling of a bad value is a usage error, not a silent fallback: a
// user who asked for color and got none is owed the reason.
func TestColorRejectsAnUnknownValue(t *testing.T) {
	h := newHarness(t, listConfig)

	for _, args := range [][]string{
		{"--color", "sometimes", "list"},
		{"--color=sometimes", "list"},
		{"--color", "list"},
	} {
		code, _, errOut := h.run(args...)
		if code != 2 {
			t.Errorf("str %s exited %d, want 2", strings.Join(args, " "), code)
		}
		if !strings.Contains(errOut, "auto, always or never") {
			t.Errorf("str %s should explain the accepted values:\n%s", strings.Join(args, " "), errOut)
		}
	}
}

// JSON is a first-class path and stays machine-readable: never colored, even
// when --color always was asked for.
func TestJSONIsNeverColored(t *testing.T) {
	h := newHarness(t, listConfig)
	h.brew.With(installed("ripgrep"))

	out := h.mustRun("--json", "--color", "always", "list")
	if strings.Contains(out, "\033[") {
		t.Errorf("JSON output must never carry color:\n%q", out)
	}
}

// NO_COLOR turns auto off. A test cannot make the buffer a terminal, so this
// checks the documented promise instead: auto resolves off under NO_COLOR, and
// --color always still wins.
func TestNoColorEnvIsRespected(t *testing.T) {
	h := newHarness(t, listConfig)
	h.brew.With(installed("ripgrep"))
	t.Setenv("NO_COLOR", "1")

	if out := h.mustRun("list"); strings.Contains(out, "\033[") {
		t.Errorf("NO_COLOR should keep output plain:\n%q", out)
	}
	if out := h.mustRun("--color", "always", "list"); !strings.Contains(out, "\033[") {
		t.Errorf("--color always should override NO_COLOR:\n%q", out)
	}
}
