package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/providers/system"
)

// How one manifest entry compares with the target machine.
const (
	// ComparePresent: the provider the manifest names has it here, at the
	// pinned version when the entry pins one.
	ComparePresent = "present"
	// CompareDrift: it is here, but not as the manifest asks: another
	// version than the pin, or through another provider.
	CompareDrift = "drift"
	// CompareMissing: nothing here has it, and the provider the manifest
	// names can install it as asked. Missing entries are the plan.
	CompareMissing = "missing"
	// CompareUnsupported: nothing here has it as asked, and Starpoint cannot
	// install it as asked on this machine.
	CompareUnsupported = "unsupported"
	// CompareUnknown: what the manifest means, or what this machine has,
	// cannot be said safely, so nothing is planned for it.
	CompareUnknown = "unknown"
)

// Why an entry is not simply present or missing. The portability codes
// (unknown-provider, ambiguous-identity, unsupported-provider,
// pin-unavailable, channel-unsupported) keep their meaning here.
const (
	// CmpVersionDrift: the manifest pins a version and the provider here
	// has another one.
	CmpVersionDrift = "version-drift"
	// CmpProviderDrift: the provider the manifest names does not have it,
	// another provider here does.
	CmpProviderDrift = "provider-drift"
	// CmpProviderUnavailable: the provider is one Starpoint has, but it is
	// not usable on this machine (not installed, or failing its health check).
	CmpProviderUnavailable = "provider-unavailable"
	// CmpNotListed: the provider is usable but its listing failed or was not
	// asked for, so whether it already has the package is not known.
	CmpNotListed = "not-listed"
	// CmpScopeUnplannable: the entry asks for a placement other than the
	// provider's default, which 'str install' cannot ask for.
	CmpScopeUnplannable = "scope-unplannable"
)

// CompareNote is the caveat every comparison carries.
const CompareNote = "A comparison of the manifest with what the providers on this machine report. " +
	"Nothing was installed, removed or recorded. The plan lists the commands that would install what is missing; " +
	"each one confirms first, records the package in this machine's configuration, and is refused rather than " +
	"make a second copy. Whether a provider can supply a package is only known when it tries."

// TargetCopy is one copy of a package the target machine has.
type TargetCopy struct {
	Provider string `json:"provider"`
	Version  string `json:"version,omitempty"`
}

// CompareEntry is one manifest entry compared with the target.
type CompareEntry struct {
	Name string `json:"name"`
	// Provider is the provider the manifest names; empty when it names
	// none. Resolved is the provider found here for an entry that names
	// none, when exactly one has it.
	Provider string      `json:"provider,omitempty"`
	Resolved string      `json:"resolved_provider,omitempty"`
	Version  string      `json:"version,omitempty"`
	Channel  string      `json:"channel,omitempty"`
	Scope    model.Scope `json:"scope,omitempty"`
	// Status is ComparePresent, CompareDrift, CompareMissing,
	// CompareUnsupported or CompareUnknown.
	Status string `json:"status"`
	// Code says why it is not present; empty when it is.
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
	// Target lists every copy this machine has under that name, the
	// operating system's included.
	Target []TargetCopy `json:"target"`
	// Candidates are the providers that have it, for an ambiguous entry.
	Candidates []string `json:"candidates,omitempty"`
	// Commands are what settles a drift by hand; nothing runs them.
	Commands []string `json:"commands,omitempty"`
}

// PlanStep is one install the recovery plan proposes.
type PlanStep struct {
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Version  string `json:"version,omitempty"`
	Channel  string `json:"channel,omitempty"`
	// Command is the exact command that carries the step out. It is shown,
	// never run: running it goes through install's own confirmation,
	// duplicate check and recording.
	Command string `json:"command"`
}

// CompareCounts tallies the entries by status.
type CompareCounts struct {
	Total       int `json:"total"`
	Present     int `json:"present"`
	Drift       int `json:"drift"`
	Missing     int `json:"missing"`
	Unsupported int `json:"unsupported"`
	Unknown     int `json:"unknown"`
}

func (c *CompareCounts) add(status string) {
	c.Total++
	switch status {
	case ComparePresent:
		c.Present++
	case CompareDrift:
		c.Drift++
	case CompareMissing:
		c.Missing++
	case CompareUnsupported:
		c.Unsupported++
	case CompareUnknown:
		c.Unknown++
	}
}

// Comparison is a manifest compared with this machine, and the recovery
// plan that follows from it.
type Comparison struct {
	Note     string         `json:"note"`
	Manifest string         `json:"manifest"`
	Counts   CompareCounts  `json:"counts"`
	Entries  []CompareEntry `json:"entries"`
	// Plan installs every missing entry, ordered by provider then name.
	Plan []PlanStep `json:"plan"`
	// Warnings are the inventory's: a provider that could not list makes
	// the comparison incomplete.
	Warnings []model.Warning `json:"warnings"`
}

// Compare reads a portable manifest against this machine's inventory and
// says, for each entry, whether it is already here, here but different,
// missing and installable, or something Starpoint cannot plan safely.
//
// Like Portability it only reads: the inventory is the observation, provider
// capabilities decide what could be installed, and nothing is resolved,
// installed or written. It never maps a name to another provider or another
// name: an entry is installed through the provider the manifest names, or
// not planned. An entry that names no provider is matched only when exactly
// one provider here has it, and is never planned, because choosing a
// provider would be a guess.
func (e *Engine) Compare(inv Inventory, manifest string, pkgs []model.Package) Comparison {
	out := Comparison{Note: CompareNote, Manifest: manifest, Entries: []CompareEntry{}, Plan: []PlanStep{}, Warnings: inv.Warnings}
	if out.Warnings == nil {
		out.Warnings = []model.Warning{}
	}
	byName := map[string][]InventoryItem{}
	for _, it := range inv.Items {
		byName[it.Name] = append(byName[it.Name], it)
	}
	provs := map[string]InventoryProvider{}
	for _, p := range inv.Providers {
		provs[p.Name] = p
	}
	sorted := append([]model.Package(nil), pkgs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return sorted[i].Provider < sorted[j].Provider
	})
	for _, p := range sorted {
		en := e.compareOne(p, byName[p.Name], provs)
		out.Counts.add(en.Status)
		out.Entries = append(out.Entries, en)
		if en.Status == CompareMissing {
			out.Plan = append(out.Plan, PlanStep{
				Name: p.Name, Provider: p.Provider, Version: p.Version, Channel: p.Channel,
				Command: installCommand(p),
			})
		}
	}
	sort.SliceStable(out.Plan, func(i, j int) bool {
		if out.Plan[i].Provider != out.Plan[j].Provider {
			return out.Plan[i].Provider < out.Plan[j].Provider
		}
		return out.Plan[i].Name < out.Plan[j].Name
	})
	return out
}

func (e *Engine) compareOne(p model.Package, seen []InventoryItem, provs map[string]InventoryProvider) CompareEntry {
	en := CompareEntry{Name: p.Name, Provider: p.Provider, Version: p.Version, Channel: p.Channel, Scope: p.Scope, Target: []TargetCopy{}}
	for _, it := range seen {
		en.Target = append(en.Target, TargetCopy{Provider: it.Provider, Version: it.Version})
	}
	sort.SliceStable(en.Target, func(i, j int) bool { return en.Target[i].Provider < en.Target[j].Provider })

	if p.Provider == "" {
		return compareUnnamed(en, seen)
	}
	if p.Provider == system.Name {
		return cmpStatus(en, CompareUnsupported, PortUnsupportedProvider,
			"it is the operating system's own; Starpoint never installs it, and this machine's operating system decides what it ships")
	}
	prov, ok := e.reg.Get(p.Provider)
	if !ok {
		return cmpStatus(en, CompareUnsupported, PortUnsupportedProvider,
			fmt.Sprintf("%q is not a provider Starpoint has; Starpoint does not map it to another one", p.Provider))
	}

	var same *InventoryItem
	var others []InventoryItem
	for i, it := range seen {
		if it.Provider == p.Provider {
			same = &seen[i]
		} else {
			others = append(others, it)
		}
	}
	if same != nil {
		if p.Version != "" && same.Version != p.Version {
			en = cmpStatus(en, CompareDrift, CmpVersionDrift, fmt.Sprintf(
				"the manifest pins %s and %s has %s here; changing it would replace the installed version, so it is not planned",
				p.Version, p.Provider, orUnknown(same.Version)))
			en.Commands = []string{"declare the pin in this machine's configuration and run 'str sync', which shows and confirms the change first"}
			return en
		}
		en.Status = ComparePresent
		return en
	}

	info, known := provs[p.Provider]
	if len(others) > 0 && known && info.Available && !info.Listed {
		// The named provider is usable but did not list: it may have it too.
		return cmpStatus(en, CompareUnknown, CmpNotListed, fmt.Sprintf(
			"%s did not list its packages, so whether it already has %s is not known", p.Provider, p.Name))
	}
	if len(others) > 0 {
		var names []string
		for _, it := range others {
			names = append(names, it.Provider)
		}
		owner := strings.Join(names, " and ")
		en = cmpStatus(en, CompareDrift, CmpProviderDrift, fmt.Sprintf(
			"%s has it here, not %s; installing it through %s would make a second copy, so it is not planned",
			owner, p.Provider, p.Provider))
		if len(others) == 1 && others[0].Provider != system.Name {
			en.Commands = append(en.Commands, fmt.Sprintf("str adopt %s %s", shellWord(p.Name), others[0].Provider))
		}
		en.Commands = append(en.Commands, fmt.Sprintf("str install %s %s --allow-duplicate", shellWord(p.Name), p.Provider))
		return en
	}
	switch {
	case known && !info.Available:
		reason := p.Provider + " is not usable on this machine"
		if info.Reason != "" {
			reason += " (" + info.Reason + ")"
		}
		return cmpStatus(en, CompareUnsupported, CmpProviderUnavailable, reason)
	case !known || !info.Listed:
		return cmpStatus(en, CompareUnknown, CmpNotListed, fmt.Sprintf(
			"%s did not list its packages, so whether it already has %s is not known", p.Provider, p.Name))
	}
	caps := prov.Capabilities()
	switch {
	case !caps.Install:
		return cmpStatus(en, CompareUnsupported, PortUnsupportedProvider, p.Provider+" cannot install software")
	case p.Version != "" && !caps.PinVersion:
		return cmpStatus(en, CompareUnsupported, PortPinUnavailable, fmt.Sprintf(
			"pinned to %s, and %s cannot install a chosen version; it is not offered unpinned", p.Version, p.Provider))
	case p.Channel != "" && !caps.Channels:
		return cmpStatus(en, CompareUnsupported, PortChannelUnsupported, fmt.Sprintf(
			"asks for channel %q, and %s has no channels", p.Channel, p.Provider))
	case p.Scope != "" && p.Scope != caps.DefaultScope:
		return cmpStatus(en, CompareUnsupported, CmpScopeUnplannable, fmt.Sprintf(
			"asks for %s placement, which 'str install' cannot ask %s for; add the entry to this machine's configuration as written and run 'str sync'",
			p.Scope, p.Provider))
	}
	en.Status = CompareMissing
	en.Reason = "nothing here has it; " + p.Provider + " can install it as the manifest asks"
	return en
}

// compareUnnamed matches an entry that names no provider. It is present
// when exactly one provider other than the operating system has it here;
// otherwise it is unknown and never planned.
func compareUnnamed(en CompareEntry, seen []InventoryItem) CompareEntry {
	var owners []InventoryItem
	for _, it := range seen {
		if it.Provider != system.Name {
			owners = append(owners, it)
		}
	}
	switch len(owners) {
	case 0:
		reason := "the manifest names no provider and none here has it; Starpoint will not guess which provider to install it with"
		if len(seen) > 0 {
			reason = "the manifest names no provider and only the operating system has it here; Starpoint will not guess whether that copy is the one meant"
		}
		en = cmpStatus(en, CompareUnknown, PortUnknownProvider, reason)
		en.Commands = []string{fmt.Sprintf("str install %s <provider>", shellWord(en.Name))}
		return en
	case 1:
		en.Resolved = owners[0].Provider
		if en.Version != "" && owners[0].Version != en.Version {
			en = cmpStatus(en, CompareDrift, CmpVersionDrift, fmt.Sprintf(
				"the manifest pins %s and %s has %s here; changing it would replace the installed version, so it is not planned",
				en.Version, owners[0].Provider, orUnknown(owners[0].Version)))
			return en
		}
		en.Status = ComparePresent
		en.Reason = "the manifest names no provider; " + owners[0].Provider + " is the only one that has it here"
		return en
	}
	for _, it := range owners {
		en.Candidates = append(en.Candidates, it.Provider)
	}
	sort.Strings(en.Candidates)
	return cmpStatus(en, CompareUnknown, PortAmbiguous, fmt.Sprintf(
		"the manifest names no provider and %s all have it here", strings.Join(en.Candidates, ", ")))
}

func cmpStatus(en CompareEntry, status, code, reason string) CompareEntry {
	en.Status, en.Code, en.Reason = status, code, reason
	return en
}

// installCommand is the install that carries out one plan step.
func installCommand(p model.Package) string {
	target := p.Name
	if p.Version != "" {
		target += "@" + p.Version
	}
	cmd := "str install " + shellWord(target) + " " + shellWord(p.Provider)
	if p.Channel != "" {
		cmd += " --channel " + shellWord(p.Channel)
	}
	return cmd
}

// shellWord quotes a word for a POSIX shell when it needs it.
func shellWord(s string) string {
	if s == "" {
		return "''"
	}
	for _, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("@%+=:,./_-", r)
		if !ok {
			return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
		}
	}
	return s
}

func orUnknown(s string) string {
	if s == "" {
		return "an unknown version"
	}
	return s
}
