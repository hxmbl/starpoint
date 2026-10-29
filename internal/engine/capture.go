package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/providers/system"
)

// Why an inventoried package was left out of a captured manifest, or what
// was left out of an entry that is in it.
const (
	// CaptureRequestedUnknown: the provider cannot say whether the package
	// was installed on purpose, so it is not captured unless named.
	CaptureRequestedUnknown = "requested-unknown"
	// CaptureAmbiguous: more than one provider reports the name and nothing
	// records which copy is the one wanted.
	CaptureAmbiguous = "ambiguous-identity"
	// CaptureSystem: the operating system's software. Starpoint never
	// manages it, and another machine's operating system decides what it
	// ships, so it is not captured.
	CaptureSystem = "system-software"
	// CaptureNotFound: a name asked for that no listed provider reports.
	CaptureNotFound = "not-found"
	// CapturePinUnsupported: --pin-versions was asked for, but the provider
	// cannot install a chosen version, so the entry is captured unpinned.
	CapturePinUnsupported = "pin-unsupported"
	// CaptureVersionUnknown: --pin-versions was asked for, but the provider
	// reported no version, so the entry is captured unpinned.
	CaptureVersionUnknown = "version-unknown"
	// CaptureDependency: named on purpose, so captured, but the provider
	// says it came in as a dependency of something else.
	CaptureDependency = "dependency"
)

// CaptureOptions selects what a capture takes from an inventory.
type CaptureOptions struct {
	// Names, when given, is the deliberate selection: exactly these
	// packages, whatever the provider says about why they are installed.
	// Empty means the default policy: what the configuration already
	// manages, and what a provider says was installed on request.
	Names []string
	// PinVersions writes each captured package's observed version as a hard
	// pin, for providers that can install a chosen version. Off by default:
	// an observed version is a fact about this machine, and a pin is a
	// decision that stops updates.
	PinVersions bool
}

// CaptureNote is one thing a capture says about a package: why it was left
// out, or what was left out of its entry.
type CaptureNote struct {
	Provider string `json:"provider,omitempty"`
	Name     string `json:"name"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	// Captured is true when the package is in the manifest anyway.
	Captured bool `json:"captured"`
	// Candidates are the providers that report the name, for an ambiguous
	// identity.
	Candidates []string `json:"candidates,omitempty"`
}

// Capture is a manifest built from an inventory, with an account of what it
// left out and why.
type Capture struct {
	// Manifest is valid Starpoint configuration: schema and packages only.
	Manifest config.Raw `json:"-"`
	// Notes are per-package: left out, or captured with something missing.
	Notes []CaptureNote `json:"notes"`
	// Dependencies is how many packages providers reported as dependencies
	// and the default policy therefore left out.
	Dependencies int `json:"dependencies"`
	// System is how many packages belong to the operating system.
	System int `json:"system"`
	// Warnings are the inventory's own: providers that could not list, or
	// could not say what was requested. What they have is missing.
	Warnings []model.Warning `json:"warnings"`
}

// Capture turns an inventory into a manifest a person can review, commit and
// use as desired state.
//
// It decides nothing on the user's behalf and touches nothing: no provider
// is asked anything, no resolution runs, and the configuration is only read.
// Each entry names the provider that reports the package, so the manifest
// records where the software came from rather than where Starpoint would put
// it today. What cannot be captured faithfully is reported instead of
// guessed: a package nobody can say was asked for, a name two providers
// report, and the operating system's own software.
//
// An entry the configuration already manages is copied from the
// configuration, pin, channel and scope included, because those were
// written down on purpose. Anything else carries the provider, the scope
// only when it differs from where that provider installs, and a version only
// with PinVersions and only when the provider can install it.
func (e *Engine) Capture(inv Inventory, doc *config.Document, opts CaptureOptions) Capture {
	out := Capture{
		Manifest: config.Raw{Schema: config.SchemaVersion},
		Notes:    []CaptureNote{},
		Warnings: inv.Warnings,
	}
	if out.Warnings == nil {
		out.Warnings = []model.Warning{}
	}
	configured := map[[2]string]model.Package{}
	if doc != nil {
		for _, p := range doc.Packages() {
			if p.Provider != "" {
				p.Origin = model.Origin{}
				configured[[2]string{p.Provider, p.Name}] = p
			}
		}
	}
	named := map[string]bool{}
	for _, n := range opts.Names {
		named[n] = true
	}
	selecting := len(named) > 0

	// Group by name: identity is the name across providers, since the
	// manifest can hold one entry per name.
	byName := map[string][]InventoryItem{}
	var names []string
	for _, it := range inv.Items {
		if _, ok := byName[it.Name]; !ok {
			names = append(names, it.Name)
		}
		byName[it.Name] = append(byName[it.Name], it)
	}
	sort.Strings(names)

	for _, name := range names {
		if selecting && !named[name] {
			continue
		}
		var keep []InventoryItem
		for _, it := range byName[name] {
			if it.Provider == system.Name {
				out.System++
				if selecting {
					out.Notes = append(out.Notes, CaptureNote{Provider: it.Provider, Name: name, Code: CaptureSystem,
						Message: "the operating system's own copy: Starpoint does not manage it, and another machine's operating system decides what it ships"})
				}
				continue
			}
			if !selecting && !it.Managed {
				switch it.Requested {
				case RequestedDependency:
					out.Dependencies++
					continue
				case RequestedUnknown:
					out.Notes = append(out.Notes, CaptureNote{Provider: it.Provider, Name: name, Code: CaptureRequestedUnknown,
						Message: it.Provider + " cannot say whether it was installed on purpose; name it to capture it"})
					continue
				}
			}
			keep = append(keep, it)
		}
		if len(keep) == 0 {
			continue
		}
		chosen, ok := pickIdentity(keep)
		if !ok {
			providers := make([]string, 0, len(keep))
			for _, it := range keep {
				providers = append(providers, it.Provider)
			}
			out.Notes = append(out.Notes, CaptureNote{Name: name, Code: CaptureAmbiguous, Candidates: providers,
				Message: fmt.Sprintf("installed through %s, and nothing records which copy is wanted; record one with 'str adopt %s <provider>' or add the entry by hand",
					strings.Join(providers, " and "), name)})
			continue
		}
		if p, ok := configured[[2]string{chosen.Provider, name}]; ok && chosen.Managed {
			out.Manifest.Packages = append(out.Manifest.Packages, p)
			continue
		}
		out.Manifest.Packages = append(out.Manifest.Packages, e.captureEntry(chosen, opts, &out))
	}

	for _, n := range opts.Names {
		if _, ok := byName[n]; !ok {
			out.Notes = append(out.Notes, CaptureNote{Name: n, Code: CaptureNotFound,
				Message: "no listed provider reports it"})
		}
	}
	sort.SliceStable(out.Notes, func(i, j int) bool { return out.Notes[i].Name < out.Notes[j].Name })
	return out
}

// pickIdentity chooses the one copy a manifest entry can describe: the only
// one, or the one the configuration already records. Several unrecorded
// copies are ambiguous.
func pickIdentity(items []InventoryItem) (InventoryItem, bool) {
	if len(items) == 1 {
		return items[0], true
	}
	var managed []InventoryItem
	for _, it := range items {
		if it.Managed {
			managed = append(managed, it)
		}
	}
	if len(managed) == 1 {
		return managed[0], true
	}
	return InventoryItem{}, false
}

// captureEntry is the manifest entry for one unmanaged package.
func (e *Engine) captureEntry(it InventoryItem, opts CaptureOptions, out *Capture) model.Package {
	p := model.Package{Name: it.Name, Provider: it.Provider}
	var caps struct {
		pin   bool
		scope model.Scope
	}
	if prov, ok := e.reg.Get(it.Provider); ok {
		c := prov.Capabilities()
		caps.pin, caps.scope = c.PinVersion, c.DefaultScope
	}
	switch it.Requested {
	case RequestedYes:
		p.Reason = "captured from inventory: " + it.Provider + " reports it was installed on request"
	case RequestedDependency:
		p.Reason = "captured from inventory by name"
		out.Notes = append(out.Notes, CaptureNote{Provider: it.Provider, Name: it.Name, Code: CaptureDependency, Captured: true,
			Message: it.Provider + " says it came in as a dependency; captured because it was named"})
	default:
		p.Reason = "captured from inventory by name"
	}
	if it.Scope != "" && it.Scope != caps.scope {
		p.Scope = it.Scope
	}
	if opts.PinVersions {
		switch {
		case it.Version == "":
			out.Notes = append(out.Notes, CaptureNote{Provider: it.Provider, Name: it.Name, Code: CaptureVersionUnknown, Captured: true,
				Message: it.Provider + " reported no version, so it is captured unpinned"})
		case !caps.pin:
			out.Notes = append(out.Notes, CaptureNote{Provider: it.Provider, Name: it.Name, Code: CapturePinUnsupported, Captured: true,
				Message: fmt.Sprintf("%s cannot install a chosen version, so %s is captured unpinned (observed %s)", it.Provider, it.Name, it.Version)})
		default:
			p.Version = it.Version
		}
	}
	return p
}
