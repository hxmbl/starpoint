package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

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
//
// Health checks run concurrently, each under its own bound, before the
// listing round. One that times out or is cancelled is reported the same
// way (an error on its row and a discover-failed warning), never as a
// silently unavailable provider, and is not remembered as unavailable.
//
// Package managers can hang (a lock held by another process, a prompt
// nobody sees, a network mirror that never answers), so every provider's
// answer is bounded by the survey timeout (SetDiscoverTimeout overrides it).
// A provider that has not answered by then is reported as failed with
// discover-failed, saying it timed out, and the others are listed as usual:
// one stuck package manager costs the timeout, never the whole command.
func (e *Engine) Inventory(ctx context.Context, doc *config.Document, only []string) Inventory {
	timeout := e.DiscoverTimeout()
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

	// Health first, concurrently and each provider under its own bound: a
	// slow health check must not spend the others' time, and one that does
	// not finish is a failed provider (warned below), not a quiet
	// "unavailable".
	available, healthFailed := e.healthWithin(ctx, selected, timeout)

	type answer struct {
		items    []model.Installation
		err      error
		asked    bool
		timedOut bool
	}
	answers := make([]answer, len(selected))
	type result struct {
		i int
		a answer
	}
	// One deadline for the round. The channel is buffered to the number of
	// providers, so one that answers after the deadline can still send and
	// exit; nothing here blocks on it.
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	results := make(chan result, len(selected))
	pending := 0
	for i, p := range selected {
		if !available[p.Name()].Available || !p.Capabilities().Discover {
			continue
		}
		pending++
		answers[i] = answer{asked: true, timedOut: true}
		go func(i int, p provider.Provider) {
			items, err := p.Discover(dctx)
			results <- result{i: i, a: answer{items: items, err: err, asked: true, timedOut: err != nil && dctx.Err() != nil}}
		}(i, p)
	}
	collect := func(r result) {
		answers[r.i] = r.a
		pending--
	}
wait:
	for pending > 0 {
		select {
		case r := <-results:
			collect(r)
		case <-dctx.Done():
			// A provider whose command was cancelled returns soon after (the
			// runner kills it within its own grace), so give the stragglers
			// a short grace to report, then stop waiting: a provider that
			// ignores cancellation must not hold the others.
			grace := time.NewTimer(min(timeout, discoverGrace))
			defer grace.Stop()
			for pending > 0 {
				select {
				case r := <-results:
					collect(r)
				case <-grace.C:
					break wait
				}
			}
		}
	}
	timedOutMsg := fmt.Sprintf("timed out after %s", timeout)

	inv := Inventory{Providers: []InventoryProvider{}, Items: []InventoryItem{}, Warnings: []model.Warning{}}
	for i, p := range selected {
		h := available[p.Name()]
		row := InventoryProvider{Name: p.Name(), Available: h.Available, Reason: h.Reason}
		a := answers[i]
		switch {
		case healthFailed[p.Name()] != "":
			msg := healthFailed[p.Name()]
			row.Error = msg
			inv.Warnings = append(inv.Warnings, model.Warning{
				Severity: model.SeverityError, Code: model.WarnDiscoverFailed, Provider: p.Name(),
				Message: "could not check whether " + p.Name() + " is usable, so none of what it has installed is shown: " + msg,
			})
		case !h.Available:
		case !p.Capabilities().Discover:
			row.Reason = "cannot list what it has installed"
		case a.asked && (a.timedOut || a.err != nil):
			// A provider that answered only because the deadline cancelled
			// it timed out, whatever error the cancellation surfaced as.
			msg := timedOutMsg
			if !a.timedOut {
				msg = a.err.Error()
			}
			row.Error = msg
			inv.Warnings = append(inv.Warnings, model.Warning{
				Severity: model.SeverityError, Code: model.WarnDiscoverFailed, Provider: p.Name(),
				Message: "could not list what " + p.Name() + " has installed, so none of it is shown: " + msg,
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

// discoverGrace is how long Inventory still waits, after the deadline, for
// providers to report how they ended. It is a little over the runner's own
// kill grace, so a provider whose subprocess was killed still reports, and
// never more than the timeout itself.
const discoverGrace = 3 * time.Second

// DiscoverTimeout is the bound on one provider's answer to Inventory: the
// survey timeout unless SetDiscoverTimeout changed it.
func (e *Engine) DiscoverTimeout() time.Duration {
	if e.discoverTimeout > 0 {
		return e.discoverTimeout
	}
	return SurveyTimeout
}

// SetDiscoverTimeout changes the bound on each provider's answer to
// Inventory. Zero restores the survey timeout. Tests use it to keep a
// hanging provider short.
func (e *Engine) SetDiscoverTimeout(d time.Duration) { e.discoverTimeout = d }
