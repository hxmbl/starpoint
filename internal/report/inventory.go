package report

import (
	"fmt"

	"github.com/hxmbl/starpoint/internal/engine"
)

// InventorySummary counts what an inventory found.
type InventorySummary struct {
	Packages  int `json:"packages"`
	Managed   int `json:"managed"`
	Unmanaged int `json:"unmanaged"`
	// Requested counts packages the provider says were installed on purpose.
	Requested int `json:"requested"`
}

// Inventory renders `str inventory`: every package each provider
// reports, and whether the configuration manages it.
func (w *Writer) Inventory(inv engine.Inventory) error {
	sum := InventorySummary{Packages: len(inv.Items)}
	for _, it := range inv.Items {
		if it.Managed {
			sum.Managed++
		} else {
			sum.Unmanaged++
		}
		if it.Requested == engine.RequestedYes {
			sum.Requested++
		}
	}
	if w.asJSON {
		return w.respond(map[string]any{
			"providers": inv.Providers,
			"packages":  inv.Items,
			"warnings":  inv.Warnings,
			"summary":   sum,
		})
	}
	w.writeContextWarnings()
	rows := [][]string{{"PROVIDER", "NAME", "VERSION", "SCOPE", "STATUS", "REQUESTED", "PATH"}}
	for _, it := range inv.Items {
		status := w.p.Dim("unmanaged")
		if it.Managed {
			status = w.p.Green("managed")
		}
		rows = append(rows, []string{it.Provider, it.Name, dash(it.Version), dash(string(it.Scope)), status, it.Requested, dash(it.Path)})
	}
	if len(inv.Items) > 0 {
		if err := w.table(rows); err != nil {
			return err
		}
		fmt.Fprintln(w.out)
	}
	for _, p := range inv.Providers {
		switch {
		case p.Listed:
			continue
		case !p.Available:
			fmt.Fprintf(w.out, "%s %s\n", w.p.Dim(p.Name+":"), w.p.Dim("not available ("+firstNonEmpty(p.Reason, "not found")+")"))
		case p.Error == "" && p.Reason != "":
			fmt.Fprintf(w.out, "%s %s\n", w.p.Dim(p.Name+":"), w.p.Dim(p.Reason))
		}
	}
	fmt.Fprintf(w.out, "%d package(s): %d managed, %d unmanaged; %d installed on request. Nothing was changed.\n",
		sum.Packages, sum.Managed, sum.Unmanaged, sum.Requested)
	w.writeWarnings(inv.Warnings)
	return nil
}
