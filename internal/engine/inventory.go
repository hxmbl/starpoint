package engine

import (
	"context"
	"sort"
	"sync"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
)

// InventoryItem is one package a provider reports as installed.
type InventoryItem struct {
	// Provider is the provider that reported it.
	Provider string `json:"provider"`
	// Name is the provider's own name for it.
	Name string `json:"name"`
	// Version is the version the provider reported, verbatim.
	Version string `json:"version,omitempty"`
	// Path is where it lives, when the provider or PATH attribution says.
	Path string `json:"path,omitempty"`
	// Scope is the placement the provider reported.
	Scope model.Scope `json:"scope,omitempty"`
	// Managed is true when the configuration has an entry with this name
	// and this provider. A configuration entry for the same name with
	// another provider, or with no provider yet, does not make it managed.
	Managed bool `json:"managed"`
}

// InventoryProvider says what one provider contributed to an inventory.
type InventoryProvider struct {
	Name string `json:"name"`
	// Available is the provider's health.
	Available bool `json:"available"`
	// Reason explains an unavailable provider.
	Reason string `json:"reason,omitempty"`
	// Listed is true when the provider was asked for its packages and
	// answered. It is false for an unavailable provider, one that cannot
	// enumerate, and one whose listing failed.
	Listed bool `json:"listed"`
	// Count is how many packages it reported.
	Count int `json:"count"`
	// Error is why listing failed, when it did.
	Error string `json:"error,omitempty"`
}

// Inventory is everything the providers on this machine report as
// installed, matched against the configuration.
type Inventory struct {
	Providers []InventoryProvider `json:"providers"`
	Items     []InventoryItem     `json:"packages"`
	Warnings  []model.Warning     `json:"warnings"`
}

// Inventory asks every available provider (or only the named ones) what it
// has installed, and marks each package managed or not.
//
// It is observation only: providers are probed for health and asked to
// Discover, nothing else. It installs nothing, records nothing and never
// consults the resolver, so it answers "what is on this machine, and whose
// is it" without deciding anything. A provider that fails to list is
// reported with a discover-failed warning rather than stopping the others.
func (e *Engine) Inventory(ctx context.Context, doc *config.Document, only []string) Inventory {
	available := e.health(ctx)
	want := map[string]bool{}
	for _, n := range only {
		want[n] = true
	}
	managed := map[[2]string]bool{}
	if doc != nil {
		for _, p := range doc.Packages() {
			if p.Provider != "" {
				managed[[2]string{p.Provider, p.Name}] = true
			}
		}
	}

	var selected []provider.Provider
	for _, p := range e.reg.All() {
		if len(want) == 0 || want[p.Name()] {
			selected = append(selected, p)
		}
	}

	type answer struct {
		items []model.Installation
		err   error
		asked bool
	}
	answers := make([]answer, len(selected))
	var wg sync.WaitGroup
	for i, p := range selected {
		if !available[p.Name()].Available || !p.Capabilities().Discover {
			continue
		}
		wg.Add(1)
		go func(i int, p provider.Provider) {
			defer wg.Done()
			items, err := p.Discover(ctx)
			answers[i] = answer{items: items, err: err, asked: true}
		}(i, p)
	}
	wg.Wait()

	inv := Inventory{Providers: []InventoryProvider{}, Items: []InventoryItem{}, Warnings: []model.Warning{}}
	for i, p := range selected {
		h := available[p.Name()]
		row := InventoryProvider{Name: p.Name(), Available: h.Available, Reason: h.Reason}
		a := answers[i]
		switch {
		case !h.Available:
		case !p.Capabilities().Discover:
			row.Reason = "cannot list what it has installed"
		case a.err != nil:
			row.Error = a.err.Error()
			inv.Warnings = append(inv.Warnings, model.Warning{
				Severity: model.SeverityError, Code: model.WarnDiscoverFailed, Provider: p.Name(),
				Message: "could not list what " + p.Name() + " has installed, so none of it is shown: " + a.err.Error(),
			})
		case a.asked:
			row.Listed = true
			seen := map[string]bool{}
			for _, inst := range a.items {
				inst := inst
				// Identity is (provider, name). A provider that reports one
				// name twice (the system provider reading /bin and /usr/bin
				// on a merged-/usr machine) is one package, not two.
				if seen[inst.Name] {
					continue
				}
				seen[inst.Name] = true
				if inst.Provider == "" {
					inst.Provider = p.Name()
				}
				located := e.withPath(&inst)
				inv.Items = append(inv.Items, InventoryItem{
					Provider: p.Name(),
					Name:     inst.Name,
					Version:  inst.Version,
					Path:     located.Path,
					Scope:    inst.Scope,
					Managed:  managed[[2]string{p.Name(), inst.Name}],
				})
				row.Count++
			}
		}
		inv.Providers = append(inv.Providers, row)
	}
	order := map[string]int{}
	for i, n := range e.reg.Names() {
		order[n] = i
	}
	sort.SliceStable(inv.Items, func(i, j int) bool {
		a, b := inv.Items[i], inv.Items[j]
		if a.Provider != b.Provider {
			return order[a.Provider] < order[b.Provider]
		}
		return a.Name < b.Name
	})
	return inv
}
