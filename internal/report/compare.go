package report

import (
	"fmt"

	"github.com/hxmbl/starpoint/internal/engine"
)

// Compare prints a manifest compared with this machine and the recovery plan
// that follows. The plan is a list of commands to review; nothing is run.
func (w *Writer) Compare(c engine.Comparison) error {
	if w.asJSON {
		return w.respond(map[string]any{"comparison": c, "warnings": c.Warnings})
	}
	w.writeContextWarnings()
	fmt.Fprintln(w.out, w.p.Bold("Manifest compared with this machine"))
	fmt.Fprintln(w.out, c.Manifest)
	fmt.Fprintln(w.out)
	n := c.Counts
	fmt.Fprintf(w.out, "%d entr%s: %d present, %d drift, %d missing, %d unsupported, %d unknown\n",
		n.Total, plural(n.Total, "y", "ies"), n.Present, n.Drift, n.Missing, n.Unsupported, n.Unknown)
	fmt.Fprintln(w.out)

	rows := [][]string{{"PACKAGE", "PROVIDER", "STATUS", "HERE", "WHY"}}
	for _, en := range c.Entries {
		if en.Status == engine.ComparePresent || en.Status == engine.CompareMissing {
			continue
		}
		status := w.p.Yellow(en.Status)
		if en.Status == engine.CompareUnsupported {
			status = w.p.Red(en.Status)
		}
		rows = append(rows, []string{en.Name, dash(en.Provider), status, here(en.Target), "[" + en.Code + "] " + en.Reason})
	}
	if len(rows) > 1 {
		fmt.Fprintln(w.out, w.p.Bold("Not planned"))
		if err := w.table(rows); err != nil {
			return err
		}
		for _, en := range c.Entries {
			for _, cmd := range en.Commands {
				fmt.Fprintf(w.out, "  %s: %s\n", en.Name, cmd)
			}
		}
		fmt.Fprintln(w.out)
	}

	if len(c.Plan) == 0 {
		fmt.Fprintln(w.out, "Recovery plan: nothing to install.")
	} else {
		fmt.Fprintf(w.out, "%s (%d step%s, not run)\n", w.p.Bold("Recovery plan"), len(c.Plan), plural(len(c.Plan), "", "s"))
		for i, st := range c.Plan {
			fmt.Fprintf(w.out, "  %d. %s\n", i+1, st.Command)
		}
		fmt.Fprintln(w.out, "Each step confirms before it changes anything, records the package in this machine's configuration,")
		fmt.Fprintln(w.out, "and is refused (duplicate-install) rather than make a second copy. Add --dry-run to see one first.")
	}
	fmt.Fprintln(w.out, "Nothing was changed.")
	if len(c.Warnings) > 0 {
		fmt.Fprintln(w.out, "Incomplete: a provider could not list, so entries it might have are not counted as present.")
		w.writeWarnings(c.Warnings)
	}
	return nil
}

func here(copies []engine.TargetCopy) string {
	if len(copies) == 0 {
		return "-"
	}
	s := ""
	for i, c := range copies {
		if i > 0 {
			s += ", "
		}
		s += c.Provider
		if c.Version != "" {
			s += " " + c.Version
		}
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
