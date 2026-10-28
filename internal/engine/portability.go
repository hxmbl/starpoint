package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/providers/system"
)

// How reproducible one entry of a portability summary looks.
const (
	// PortableYes: a provider Starpoint has can install it, and it honours
	// whatever the entry asks for (pin, channel).
	PortableYes = "reproducible"
	// PortableUnknown: who owns it, or whether it was wanted, cannot be said
	// from what the providers report.
	PortableUnknown = "unknown"
	// PortableUnsupported: its provider cannot install it on another
	// machine as asked.
	PortableUnsupported = "unsupported"
)

// Why an entry is not reproducible.
const (
	// PortUnknownProvider: the configuration names no provider and no
	// provider on this machine reports it.
	PortUnknownProvider = "unknown-provider"
	// PortAmbiguous: several providers report it and nothing records which
	// copy is the one wanted.
	PortAmbiguous = CaptureAmbiguous
	// PortMissingMetadata: the provider cannot say whether it was installed
	// on purpose, so it is not known to be part of the setup.
	PortMissingMetadata = "missing-metadata"
	// PortUnsupportedProvider: a provider Starpoint does not have, or one
	// that cannot install (the operating system's own software).
	PortUnsupportedProvider = "unsupported-provider"
	// PortPinUnavailable: the entry pins a version its provider cannot
	// install on request.
	PortPinUnavailable = "pin-unavailable"
	// PortChannelUnsupported: the entry names a channel and its provider
	// has none.
	PortChannelUnsupported = "channel-unsupported"
)

// Where a portability entry came from.
const (
	// PortSourceDeclared: an entry in the configuration.
	PortSourceDeclared = "declared"
	// PortSourceDiscovered: software a provider reports that the
	// configuration does not declare.
	PortSourceDiscovered = "discovered"
)

// PortabilityNote is the caveat every portability summary carries.
const PortabilityNote = "An estimate from what the providers on this machine report and what Starpoint's providers can do. " +
	"It is not a guarantee that another machine can install everything: its operating system, architecture, " +
	"package sources and available versions decide that."

// PortabilityEntry is one package the summary considered.
type PortabilityEntry struct {
	Name string `json:"name"`
	// Provider is the provider the entry would be reproduced with: the one
	// the configuration records, or the one that reports it here. Empty
	// when that cannot be said.
	Provider string `json:"provider,omitempty"`
	// Source is PortSourceDeclared or PortSourceDiscovered.
	Source string `json:"source"`
	// Status is PortableYes, PortableUnknown or PortableUnsupported.
	Status string `json:"status"`
	// Code says why it is not reproducible; empty when it is.
	Code string `json:"code,omitempty"`
	// Reason is the same in words.
	Reason string `json:"reason,omitempty"`
	// Candidates are the providers that report it, for an ambiguous one.
	Candidates []string `json:"candidates,omitempty"`
}

// PortabilityCounts tallies one group of entries by status.
type PortabilityCounts struct {
	Total        int `json:"total"`
	Reproducible int `json:"reproducible"`
	Unknown      int `json:"unknown"`
	Unsupported  int `json:"unsupported"`
}

func (c *PortabilityCounts) add(status string) {
	c.Total++
	switch status {
	case PortableYes:
		c.Reproducible++
	case PortableUnknown:
		c.Unknown++
	case PortableUnsupported:
		c.Unsupported++
	}
}

// Portability says how much of this machine's selected software Starpoint
// could describe and reproduce elsewhere.
type Portability struct {
	// Note is PortabilityNote: this is an estimate.
	Note string `json:"note"`
	// Declared counts the configuration's entries.
	Declared PortabilityCounts `json:"declared"`
	// Discovered counts software providers report that the configuration
	// does not declare: what they say was installed on request, and what
	// they cannot classify (unknown, missing metadata).
	Discovered PortabilityCounts `json:"discovered"`
	// Overall is Declared and Discovered together.
	Overall PortabilityCounts `json:"overall"`
	// Dependencies is how many packages providers reported as dependencies;
	// they are not part of the selected setup.
	Dependencies int `json:"dependencies"`
	// System is how many packages are the operating system's own.
	System int `json:"system"`
	// Entries lists every package considered, declared first, by name.
	Entries []PortabilityEntry `json:"entries"`
	// Warnings are the inventory's: a provider that could not list makes
	// the summary incomplete.
	Warnings []model.Warning `json:"warnings"`
}

// Portability estimates how much of the selected software on this machine
// Starpoint can faithfully represent and reproduce elsewhere.
//
// Like Capture it is a reading of an inventory and the configuration: no
// provider is asked anything further, nothing is resolved, and nothing is
// written. The selection is the configuration's entries plus what providers
// say was installed on request (and what they cannot classify, as unknown).
// Dependencies and the operating system's own software are counted and left
// out. Each entry is reproducible only when a provider Starpoint has can
// install it and can honour its pin and channel; otherwise it says why.
func (e *Engine) Portability(inv Inventory, doc *config.Document) Portability {
	out := Portability{Note: PortabilityNote, Entries: []PortabilityEntry{}, Warnings: inv.Warnings}
	if out.Warnings == nil {
		out.Warnings = []model.Warning{}
	}

	byName := map[string][]InventoryItem{}
	for _, it := range inv.Items {
		byName[it.Name] = append(byName[it.Name], it)
	}

	declared := map[string]bool{}
	var decl []PortabilityEntry
	if doc != nil {
		for _, p := range doc.Packages() {
			declared[p.Name] = true
			decl = append(decl, e.portDeclared(p, byName[p.Name]))
		}
	}
	sort.SliceStable(decl, func(i, j int) bool { return decl[i].Name < decl[j].Name })

	var names []string
	for n := range byName {
		names = append(names, n)
	}
	sort.Strings(names)
	var disc []PortabilityEntry
	for _, name := range names {
		var keep []InventoryItem
		for _, it := range byName[name] {
			switch {
			case it.Provider == system.Name:
				out.System++
			case declared[name]:
				// Accounted for by its declaration.
			case it.Requested == RequestedDependency:
				out.Dependencies++
			default:
				keep = append(keep, it)
			}
		}
		if len(keep) == 0 {
			continue
		}
		disc = append(disc, e.portDiscovered(name, keep))
	}

	for _, en := range decl {
		out.Declared.add(en.Status)
		out.Overall.add(en.Status)
	}
	for _, en := range disc {
		out.Discovered.add(en.Status)
		out.Overall.add(en.Status)
	}
	out.Entries = append(append(out.Entries, decl...), disc...)
	return out
}

// portDeclared judges one configuration entry. Its provider is the one it
// records, or else the only non-system provider that reports it here.
func (e *Engine) portDeclared(p model.Package, seen []InventoryItem) PortabilityEntry {
	en := PortabilityEntry{Name: p.Name, Provider: p.Provider, Source: PortSourceDeclared}
	if en.Provider == "" {
		var owners []string
		for _, it := range seen {
			if it.Provider != system.Name {
				owners = append(owners, it.Provider)
			}
		}
		switch len(owners) {
		case 0:
			return unknown(en, PortUnknownProvider,
				"the configuration names no provider and no provider here reports it, so where it would come from is unknown")
		case 1:
			en.Provider = owners[0]
		default:
			en.Candidates = owners
			return unknown(en, PortAmbiguous, fmt.Sprintf(
				"the configuration names no provider and %s all report it", strings.Join(owners, ", ")))
		}
	}
	return e.portCapable(en, p.Version, p.Channel)
}

// portDiscovered judges undeclared software one or more providers report.
func (e *Engine) portDiscovered(name string, items []InventoryItem) PortabilityEntry {
	en := PortabilityEntry{Name: name, Source: PortSourceDiscovered}
	if len(items) > 1 {
		for _, it := range items {
			en.Candidates = append(en.Candidates, it.Provider)
		}
		return unknown(en, PortAmbiguous, fmt.Sprintf(
			"installed through %s, and nothing records which copy is wanted", strings.Join(en.Candidates, " and ")))
	}
	it := items[0]
	en.Provider = it.Provider
	if it.Requested != RequestedYes {
		return unknown(en, PortMissingMetadata,
			it.Provider+" cannot say whether it was installed on purpose, so it is not known to be part of the setup")
	}
	// A captured entry is unpinned and channel-less by default, so only the
	// provider itself has to be able to install it.
	return e.portCapable(en, "", "")
}

// portCapable checks the entry's provider can install it as asked.
func (e *Engine) portCapable(en PortabilityEntry, version, channel string) PortabilityEntry {
	if en.Provider == system.Name {
		return unsupportedEntry(en, PortUnsupportedProvider,
			"it is the operating system's own; Starpoint never installs it, and another machine's operating system decides what it ships")
	}
	prov, ok := e.reg.Get(en.Provider)
	if !ok {
		return unsupportedEntry(en, PortUnsupportedProvider, fmt.Sprintf("%q is not a provider Starpoint has", en.Provider))
	}
	caps := prov.Capabilities()
	switch {
	case !caps.Install:
		return unsupportedEntry(en, PortUnsupportedProvider, en.Provider+" cannot install software")
	case version != "" && !caps.PinVersion:
		return unsupportedEntry(en, PortPinUnavailable, fmt.Sprintf(
			"pinned to %s, and %s cannot install a chosen version", version, en.Provider))
	case channel != "" && !caps.Channels:
		return unsupportedEntry(en, PortChannelUnsupported, fmt.Sprintf(
			"asks for channel %q, and %s has no channels", channel, en.Provider))
	}
	en.Status = PortableYes
	return en
}

func unknown(en PortabilityEntry, code, reason string) PortabilityEntry {
	en.Status, en.Code, en.Reason = PortableUnknown, code, reason
	return en
}

func unsupportedEntry(en PortabilityEntry, code, reason string) PortabilityEntry {
	en.Status, en.Code, en.Reason = PortableUnsupported, code, reason
	return en
}
