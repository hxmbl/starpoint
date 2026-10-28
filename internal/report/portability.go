package report

import (
	"fmt"

	"github.com/hxmbl/starpoint/internal/engine"
)

// Portability writes the portability summary: how much of the selected
// software on this machine Starpoint could reproduce elsewhere, and why the
// rest could not be. It is always labelled an estimate.
func (w *Writer) Portability(p engine.Portability) error {
	if w.asJSON {
		return w.respond(map[string]any{"portability": p, "warnings": p.Warnings})
	}
	w.writeContextWarnings()
	fmt.Fprintln(w.out, w.p.Bold("Portability estimate"))
	fmt.Fprintln(w.out, p.Note)
	fmt.Fprintln(w.out)
	counts := func(label string, c engine.PortabilityCounts) {
		fmt.Fprintf(w.out, "%-34s %d: %d reproducible, %d unknown, %d unsupported\n",
			label, c.Total, c.Reproducible, c.Unknown, c.Unsupported)
	}
	counts("Declared in the configuration", p.Declared)
	counts("Discovered, not declared", p.Discovered)
	fmt.Fprintf(w.out, "%-34s %d dependencies, %d of the operating system's own\n", "Left out", p.Dependencies, p.System)
	fmt.Fprintln(w.out)

	rows := [][]string{{"PACKAGE", "SOURCE", "PROVIDER", "STATUS", "WHY"}}
	for _, en := range p.Entries {
		if en.Status == engine.PortableYes {
			continue
		}
		status := w.p.Yellow(en.Status)
		if en.Status == engine.PortableUnsupported {
			status = w.p.Red(en.Status)
		}
		rows = append(rows, []string{en.Name, en.Source, dash(en.Provider), status, "[" + en.Code + "] " + en.Reason})
	}
	if len(rows) > 1 {
		if err := w.table(rows); err != nil {
			return err
		}
		fmt.Fprintln(w.out)
	}
	if p.Overall.Total == 0 {
		fmt.Fprintln(w.out, "Nothing selected to estimate: no declared packages and nothing a provider says was installed on request. Nothing was changed.")
	} else {
		fmt.Fprintf(w.out, "About %d of %d selected package(s) (%d%%) look reproducible. Nothing was changed.\n",
			p.Overall.Reproducible, p.Overall.Total, p.Overall.Reproducible*100/p.Overall.Total)
	}
	if p.Discovered.Total > 0 {
		fmt.Fprintln(w.out, "Discovered software is only reproduced once it is declared: 'starpoint inventory --format toml' captures it for review.")
	}
	if len(p.Warnings) > 0 {
		fmt.Fprintln(w.out, "Incomplete: a provider could not list, so what it has is not counted.")
		w.writeWarnings(p.Warnings)
	}
	return nil
}
