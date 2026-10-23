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
	// Requested says whether the user asked for it: RequestedYes,
	// RequestedDependency, or RequestedUnknown when the provider cannot say.
	Requested string `json:"requested"`
}

// What an inventory knows about whether a package was asked for.
const (
	// RequestedYes: the provider says the user installed it on purpose.
	RequestedYes = "requested"
	// RequestedDependency: the provider says it came in as a dependency.
	RequestedDependency = "dependency"
	// RequestedUnknown: the provider cannot tell, or failed to say.
	RequestedUnknown = "unknown"
)

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
	// Requested is how the provider tells requested software from
	// dependencies: "reported" (it keeps a record and was asked),
	// "listing" (what it lists is only what was asked for), or "unknown".
	Requested string `json:"requested,omitempty"`
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
// Package managers can hang (a lock held by another process, a prompt
// nobody sees, a network mirror that never answers), so every provider's
// answer is bounded by the survey timeout (SetDiscoverTimeout overrides it).
// A provider that has not answered by then is reported as failed with
// discover-failed, saying it timed out, and the others are listed as usual:
// one stuck package manager costs the timeout, never the whole command.
func (e *Engine) Inventory(ctx context.Context, doc *config.Document, only []string) Inventory {
	timeout := e.DiscoverTimeout()
	hctx, hcancel := context.WithTimeout(ctx, timeout)
	available := e.health(hctx)
	hcancel()
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
		items     []model.Installation
		err       error
		asked     bool
		timedOut  bool
		requested map[string]bool
		source    string
		reqErr    error
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
			// Listing and the requested query share the deadline: both are
			// the same package manager, and either can hang.
			items, err := p.Discover(dctx)
			a := answer{items: items, err: err, asked: true, timedOut: err != nil && dctx.Err() != nil, source: "unknown"}
			if err == nil {
				a.requested, a.source, a.reqErr = e.requestedBy(dctx, p, items)
				if a.reqErr != nil && dctx.Err() != nil {
					a.reqErr = fmt.Errorf("timed out after %s", timeout)
				}
			}
			results <- result{i: i, a: a}
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
			row.Requested = a.source
			if a.reqErr != nil {
				inv.Warnings = append(inv.Warnings, model.Warning{
					Severity: model.SeverityWarn, Code: model.WarnRequestedUnknown, Provider: p.Name(),
					Message: "could not ask " + p.Name() + " which packages were installed on purpose, so all of them are shown as unknown: " + a.reqErr.Error(),
				})
			}
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
					Provider:  p.Name(),
					Name:      inst.Name,
					Version:   inst.Version,
					Path:      located.Path,
					Scope:     inst.Scope,
					Managed:   managed[[2]string{p.Name(), inst.Name}],
					Requested: requestedLabel(a.requested, inst.Name),
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

// requestedBy asks a provider which of the packages it listed were installed
// on purpose. The source is "reported" for a provider that keeps that
// record, "listing" for one whose list holds nothing else, and "unknown".
//
// A package manager is never called requested by its listing: npm lists npm
// and corepack among its globals because Node installed them, not the user.
func (e *Engine) requestedBy(ctx context.Context, p provider.Provider, items []model.Installation) (map[string]bool, string, error) {
	if r, ok := p.(provider.RequestedReporter); ok {
		got, err := r.Requested(ctx, items)
		if err != nil {
			return nil, "unknown", err
		}
		return got, "reported", nil
	}
	if c, ok := p.(provider.RequestedByConstruction); ok && c.DiscoverListsOnlyRequested() {
		managers := e.packageManagerNames()
		out := make(map[string]bool, len(items))
		for _, inst := range items {
			if !managers[inst.Name] {
				out[inst.Name] = true
			}
		}
		return out, "listing", nil
	}
	return nil, "unknown", nil
}

// requestedLabel renders one package's answer.
func requestedLabel(m map[string]bool, name string) string {
	v, ok := m[name]
	switch {
	case !ok:
		return RequestedUnknown
	case v:
		return RequestedYes
	default:
		return RequestedDependency
	}
}

// packageManagerNames are names that are never an ordinary user package:
// every registered provider, plus corepack and Starpoint itself.
func (e *Engine) packageManagerNames() map[string]bool {
	out := map[string]bool{"corepack": true, "starpoint": true}
	for _, q := range e.reg.All() {
		out[q.Name()] = true
	}
	return out
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
