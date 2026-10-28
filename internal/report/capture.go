package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/engine"
)

// CaptureTOML writes a captured manifest: valid Starpoint configuration,
// preceded by comments that say what it is, what it left out and why. The
// comments are for the person reviewing it; a TOML parser ignores them, so
// the output parses back to exactly the entries shown. Nothing in it depends
// on the time or the order providers answered in, so capturing the same
// machine twice gives the same text.
func (w *Writer) CaptureTOML(c engine.Capture) error {
	body, err := config.EncodeManifest(c.Manifest.Packages)
	if err != nil {
		return err
	}
	var b strings.Builder
	line := func(format string, args ...any) {
		if format == "" {
			b.WriteString("#\n")
			return
		}
		fmt.Fprintf(&b, "# "+format+"\n", args...)
	}
	line("Starpoint manifest captured by 'starpoint inventory --format toml'.")
	line("Review it before using it as desired state: every entry names the provider that")
	line("reports the package on this machine. Capturing changed nothing on this machine.")
	line("")
	line("%d package(s) captured.", len(c.Manifest.Packages))
	var left, partial []engine.CaptureNote
	for _, n := range c.Notes {
		if n.Captured {
			partial = append(partial, n)
		} else {
			left = append(left, n)
		}
	}
	if len(left) > 0 || c.Dependencies > 0 || c.System > 0 {
		line("Not captured:")
		for _, n := range left {
			line("  %s [%s]: %s", noteName(n), n.Code, n.Message)
		}
		if c.Dependencies > 0 {
			line("  %d package(s) their provider installed as dependencies", c.Dependencies)
		}
		if c.System > 0 {
			line("  %d package(s) of the operating system's own", c.System)
		}
	}
	if len(partial) > 0 {
		line("Captured with a caveat:")
		for _, n := range partial {
			line("  %s [%s]: %s", noteName(n), n.Code, n.Message)
		}
	}
	if len(c.Warnings) > 0 {
		warnings := append(c.Warnings[:0:0], c.Warnings...)
		sort.SliceStable(warnings, func(i, j int) bool { return warnings[i].Provider < warnings[j].Provider })
		line("Incomplete:")
		for _, wn := range warnings {
			line("  [%s] %s", wn.Code, wn.Message)
		}
	}
	b.WriteString("\n")
	b.Write(body)
	_, err = fmt.Fprint(w.out, b.String())
	return err
}

func noteName(n engine.CaptureNote) string {
	if n.Provider == "" {
		return n.Name
	}
	return n.Provider + "/" + n.Name
}
