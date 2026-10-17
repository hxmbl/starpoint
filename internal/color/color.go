// Package color decides when output is colorized and applies the color.
//
// Color is decoration, never information: every line reads the same with the
// escapes stripped, so a reader in a pipe, a log file or a screen reader loses
// nothing. The rules are the conventional ones — --color always/never wins,
// NO_COLOR and a dumb or absent TERM keep color off, and otherwise color
// follows the writer: a terminal gets it, a pipe or a file does not.
package color

import (
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

// Mode says when output is colorized.
type Mode string

const (
	// Auto colorizes only when the environment and the writer allow it.
	Auto Mode = "auto"
	// Always colorizes everywhere, including into a pipe.
	Always Mode = "always"
	// Never never colorizes, whatever the environment says.
	Never Mode = "never"
)

// ParseMode reads a --color value. An unknown value means auto, so a typo
// falls back to the sane behavior instead of failing the command or forcing
// escape sequences into a redirected file.
func ParseMode(s string) Mode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(Always):
		return Always
	case string(Never):
		return Never
	default:
		return Auto
	}
}

// Valid reports whether s is a value --color accepts.
func Valid(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(Auto), string(Always), string(Never):
		return true
	default:
		return false
	}
}

// Enabled resolves mode against w and the environment.
//
// Always and Never are the whole of their contract: an explicit --color always
// colors even under NO_COLOR, because the user asked for it in the same breath.
// Auto is where the environment speaks.
func Enabled(mode Mode, w io.Writer) bool {
	switch mode {
	case Always:
		return true
	case Never:
		return false
	}
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	// TERM is unset or dumb: the terminal cannot be trusted to render escapes.
	// Asking the terminal is the point of the check, so an unknown TERM is not
	// treated as a failure — only a terminal that says it cannot render.
	t := strings.ToLower(os.Getenv("TERM"))
	if t == "" || t == "dumb" {
		return false
	}
	return isTerminal(w)
}

// isTerminal reports whether w is a character terminal.
//
// The file mode is not enough: it would call /dev/null a terminal, and a
// program piping output through a file would still be colored. An ioctl is the
// only reliable answer, so this uses the same library the confirmation prompts
// use.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiCyan   = "\033[36m"
	ansiGray   = "\033[90m"
)

// Palette applies color when it is in effect and passes text through when it
// is not. Callers style text without asking whether styling happened, which is
// what keeps the plain and the colored renderings the same code.
type Palette struct{ enabled bool }

// New resolves a palette for w under mode.
func New(mode Mode, w io.Writer) Palette { return Palette{enabled: Enabled(mode, w)} }

// Off returns a palette that writes no escape sequences.
func Off() Palette { return Palette{} }

// Enabled reports whether this palette colors.
func (p Palette) Enabled() bool { return p.enabled }

func (p Palette) wrap(code, s string) string {
	if !p.enabled || s == "" {
		return s
	}
	return code + s + ansiReset
}

// Every method is a no-op on an off palette, which is what lets a caller style
// text without first asking whether styling will happen.
func (p Palette) Bold(s string) string       { return p.wrap(ansiBold, s) }
func (p Palette) Dim(s string) string        { return p.wrap(ansiDim, s) }
func (p Palette) Red(s string) string        { return p.wrap(ansiRed, s) }
func (p Palette) Green(s string) string      { return p.wrap(ansiGreen, s) }
func (p Palette) Yellow(s string) string     { return p.wrap(ansiYellow, s) }
func (p Palette) Cyan(s string) string       { return p.wrap(ansiCyan, s) }
func (p Palette) Gray(s string) string       { return p.wrap(ansiGray, s) }
func (p Palette) BoldRed(s string) string    { return p.wrap("\033[1;31m", s) }
func (p Palette) BoldYellow(s string) string { return p.wrap("\033[1;33m", s) }
func (p Palette) BoldGreen(s string) string  { return p.wrap("\033[1;32m", s) }

// Strip removes ANSI escape sequences from s.
//
// Alignment and comparison both need the text a human actually reads: a column
// padded with len() of styled text would line up only in a terminal that
// ignores the escapes.
func Strip(s string) string {
	if !strings.Contains(s, "\033") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '\033' {
			b.WriteByte(s[i])
			i++
			continue
		}
		// CSI: ESC [ parameters... final byte. Anything else is left alone
		// rather than eaten, so stripping never eats ordinary text.
		if i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				i = j + 1
				continue
			}
		}
		i++
	}
	return b.String()
}

// VisibleLen is the width of s as a terminal shows it, ignoring escapes.
func VisibleLen(s string) int { return len(Strip(s)) }
