package color

import (
	"bytes"
	"os"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		in   string
		want Mode
	}{
		{"", Auto},
		{"auto", Auto},
		{"always", Always},
		{"never", Never},
		{"ALWAYS", Always},
		{"  Never ", Never},
		{"garbage", Auto},
	}
	for _, c := range cases {
		if got := ParseMode(c.in); got != c.want {
			t.Errorf("ParseMode(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestValid(t *testing.T) {
	for _, ok := range []string{"auto", "always", "never", " Always "} {
		if !Valid(ok) {
			t.Errorf("Valid(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "yes", "1", "autoo"} {
		if Valid(bad) {
			t.Errorf("Valid(%q) = true, want false", bad)
		}
	}
}

func TestEnabledModes(t *testing.T) {
	var buf bytes.Buffer
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")

	if !Enabled(Always, &buf) {
		t.Error("always should color even into a buffer, even under NO_COLOR")
	}
	if Enabled(Never, &buf) {
		t.Error("never should never color")
	}
	if Enabled(Auto, &buf) {
		t.Error("auto should respect NO_COLOR")
	}
}

func TestEnabledAutoWithoutNOColor(t *testing.T) {
	os.Unsetenv("NO_COLOR")
	t.Setenv("TERM", "xterm-256color")

	// A buffer is not a terminal: auto must not write escapes into a pipe.
	if Enabled(Auto, &bytes.Buffer{}) {
		t.Error("auto should not color a non-terminal writer")
	}
	// Always still colors there, which is the entire point of the flag.
	if !Enabled(Always, &bytes.Buffer{}) {
		t.Error("always should color a non-terminal writer")
	}
}

func TestEnabledAutoNeedsATerminal(t *testing.T) {
	os.Unsetenv("NO_COLOR")
	for _, term := range []string{"dumb", ""} {
		t.Setenv("TERM", term)
		if Enabled(Auto, &bytes.Buffer{}) {
			t.Errorf("TERM=%q should disable color in auto mode", term)
		}
	}
}

func TestPaletteWrapping(t *testing.T) {
	on := New(Always, nil)
	off := Off()

	if got := on.Red("x"); got != ansiRed+"x"+ansiReset {
		t.Errorf("on.Red = %q", got)
	}
	if got := off.Red("x"); got != "x" {
		t.Errorf("off.Red should pass through, got %q", got)
	}
	if got := on.Bold(""); got != "" {
		t.Errorf("an empty string should not be wrapped, got %q", got)
	}
	if off.Enabled() {
		t.Error("Off() should report itself as disabled")
	}
	if !on.Enabled() {
		t.Error("an always palette should report itself as enabled")
	}
}

func TestStripAndVisibleLen(t *testing.T) {
	styled := New(Always, nil).Green("ok")
	if got := Strip(styled); got != "ok" {
		t.Errorf("Strip(%q) = %q, want %q", styled, got, "ok")
	}
	if got := VisibleLen(styled); got != 2 {
		t.Errorf("VisibleLen(%q) = %d, want 2", styled, got)
	}
	// Plain text is measured as itself, so alignment is unchanged by a palette
	// that is off.
	if got := VisibleLen("hello"); got != 5 {
		t.Errorf("VisibleLen(plain) = %d, want 5", got)
	}
	// Ordinary text with an escape-looking prefix is not eaten: only CSI
	// sequences are removed.
	if got := Strip("1 \033[32m+ 1\033[0m = 2"); got != "1 + 1 = 2" {
		t.Errorf("Strip of mixed text = %q", got)
	}
}
