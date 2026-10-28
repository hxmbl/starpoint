// Package resolve decides which provider should own a package.
//
// Resolution is where Starpoint's central claim lives: the user describes what
// they want, not how to get it, and Starpoint works out the placement. Three
// rules, in order of authority:
//
//  1. An explicit provider in the configuration is a decision, not a hint.
//  2. If the software is already installed through some provider, that provider
//     keeps ownership. Replacing a working installation because a scoring
//     function prefers another one is exactly the duplicate-install failure mode
//     this project exists to avoid.
//  3. Otherwise, prefer the ecosystem-native provider over a general-purpose
//     system package manager. `ruff` is a Python tool; brew has a copy of it,
//     but uv is the right home. A name that identifies its own ecosystem by
//     shape (a Flatpak reverse-DNS application ID) is the same kind of signal.
//     The ecosystem of the project Starpoint runs in is a weaker one: it only
//     counts for a name that could belong to that ecosystem (a Go module path in
//     a Go project), never for a bare name like neofetch, which says nothing
//     about the project it was typed in.
//
// When those rules do not produce a clear winner, the user's configured
// provider preference (`[settings] providers`, best first) breaks the tie if
// one of the tied providers is on it and can honour the entry. Otherwise the
// resolver reports ambiguity rather than guessing, so the CLI can ask a
// useful question.
package resolve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/locator"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/flatpak"
	"github.com/hxmbl/starpoint/internal/providers/golang"
	"github.com/hxmbl/starpoint/internal/providers/rustup"
	"github.com/hxmbl/starpoint/internal/providers/system"
)

// Input is everything the resolver needs to make a decision.
type Input struct {
	// Package is the desired entry.
	Package model.Package
	// Existing is where the software is already installed, if anywhere. A nil
	// Existing means the software is not on the machine.
	Existing *model.Installation
	// Available maps provider name to its health.
	Available map[string]provider.Health
	// Allowed optionally restricts candidates to these provider names.
	Allowed []string
	// Ecosystem is the ecosystem of the project Starpoint is running in, if any.
	// It is a signal, not a decision: being inside a Rust project makes cargo
	// more plausible than brew for an unrecognised name, but an explicit
	// provider or an existing installation still wins.
	Ecosystem provider.Kind
	// Locator is used to inspect PATH when Existing is not already known.
	Locator *locator.Locator
	// Preferred is `[settings] providers`: the user's providers, best first.
	// It is a tie-breaker, not a decision. It never outranks an explicit
	// provider, an existing owner or a known owner, never widens the
	// candidates (a preferred provider must be one the resolver already
	// found able to install the package), and never picks a provider that
	// cannot honour the entry's version pin, channel or scope. It only
	// settles a choice the resolver would otherwise have to ask about.
	Preferred []string
}

// ecosystem describes the affinity a provider has for a class of software.
type ecosystem struct {
	kind      provider.Kind
	preferred []string
}

// ecosystems maps provider kind to the providers that specialize in it, best
// first. This is the table that keeps Homebrew from being treated as a Python
// package manager.
var ecosystems = map[provider.Kind]ecosystem{
	provider.KindPython: {kind: provider.KindPython, preferred: []string{"uv", "pip"}},
	provider.KindRust:   {kind: provider.KindRust, preferred: []string{"cargo"}},
	provider.KindNode:   {kind: provider.KindNode, preferred: []string{"npm", "pnpm", "yarn"}},
	provider.KindGo:     {kind: provider.KindGo, preferred: []string{"go"}},
	provider.KindRuby:   {kind: provider.KindRuby, preferred: []string{"gem"}},
}

// systemManagers are general-purpose providers, used as a fallback when no
// ecosystem-specific provider claims the software. Only providers Starpoint
// actually implements belong here: naming one it does not have would produce a
// resolution it cannot carry out.
//
// Flatpak and Snap are deliberately absent. They are application ecosystems, not
// general-purpose package managers: making every unrecognised name a three-way
// choice between apt, flatpak and snap would turn a decision Starpoint can make
// into a question it asks on almost every install. They remain reachable by
// naming them explicitly.
var systemManagers = []string{"brew", "apt", "dnf", "pacman", "nix"}

// nameHints map well-known software names to the ecosystem that should own them.
// This is intentionally small: it covers cases where the name itself implies a
// language, not an attempt to catalogue every package.
//
// Names whose owner is a specific provider are deliberately absent: they belong in
// ownerHints, which states the owner instead of hinting at an ecosystem.
var nameHints = map[string]provider.Kind{
	"python":  provider.KindPython,
	"python3": provider.KindPython,
	"pip":     provider.KindPython,
	"uv":      provider.KindPython,
	"ruff":    provider.KindPython,
	"black":   provider.KindPython,
	"poetry":  provider.KindPython,
	"rye":     provider.KindPython,

	// "rg" appears here because its name says "Rust binary". "ripgrep" does not,
	// because `brew install ripgrep` is what almost everyone means and there is a
	// better default available. An unrecognised name is asked about rather than
	// guessed at.
	"rg": provider.KindRust,

	"node": provider.KindNode,
	"npm":  provider.KindNode,
	"pnpm": provider.KindNode,
	"yarn": provider.KindNode,
	"bun":  provider.KindNode,
	"deno": provider.KindNode,

	"go":            provider.KindGo,
	"gopls":         provider.KindGo,
	"golangci-lint": provider.KindGo,

	"ruby":   provider.KindRuby,
	"bundle": provider.KindRuby,
	"rake":   provider.KindRuby,
}

// ownerHint returns the provider that definitively owns a name, if one does.
//
// This exists because "Rust" is two different jobs: rustup provides the
// toolchain and `cargo install` provides crates, and treating them as one
// provider is how a machine ends up with two compilers. The vocabulary comes from
// the provider that owns it, so the two cannot drift apart.
//
// An owner is authoritative. If rustup owns a name and rustup is missing, the
// answer is still rustup: falling through to the generic Rust provider would mean
// running `cargo install rustc`, which is not a thing anyone wants.
func ownerHint(name string) (string, bool) {
	if rustup.IsToolchainBinary(name) || name == rustup.Name {
		return rustup.Name, true
	}
	return "", false
}

// Resolver makes provider decisions.
type Resolver struct {
	reg *provider.Registry
}

// New builds a Resolver over a registry.
func New(reg *provider.Registry) *Resolver { return &Resolver{reg: reg} }

// Step is one rule the resolver evaluated.
type Step struct {
	// Rule is the name of the rule that was evaluated.
	Rule string `json:"rule"`
	// Outcome is what the rule concluded.
	Outcome string `json:"outcome"`
	// Provider is the provider the rule selected, if any.
	Provider string `json:"provider,omitempty"`
	// Reason explains the rule's decision.
	Reason string `json:"reason,omitempty"`
	// Applied is true when this rule's decision was used.
	Applied bool `json:"applied"`
}

// Explanation is the full reasoning chain for a resolution.
type Explanation struct {
	// Package is the package being resolved.
	Package model.Package `json:"package"`
	// Final is the final resolution.
	Final model.Resolution `json:"final"`
	// Steps is the ordered list of rules evaluated.
	Steps []Step `json:"steps"`
}

// KnownProviders names every provider the resolver's own tables refer to.
//
// It exists so a table can be checked against the registry. Naming a provider
// that does not exist produces a resolution Starpoint cannot carry out, and that
// failure is silent until someone tries it.
func (r *Resolver) KnownProviders() []string {
	seen := map[string]bool{}
	var out []string
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, e := range ecosystems {
		for _, name := range e.preferred {
			add(name)
		}
	}
	for _, name := range systemManagers {
		add(name)
	}
	sort.Strings(out)
	return out
}

// Resolve decides who should own the package.
func (r *Resolver) Resolve(in Input) model.Resolution {
	expl := r.Explain(in)
	return expl.Final
}

// Explain decides who should own the package and returns the full reasoning chain.
func (r *Resolver) Explain(in Input) Explanation {
	var steps []Step
	var final model.Resolution

	record := func(rule, outcome, provider, reason string, applied bool) {
		steps = append(steps, Step{
			Rule:     rule,
			Outcome:  outcome,
			Provider: provider,
			Reason:   reason,
			Applied:  applied,
		})
	}

	// Rule 1: explicit configuration.
	if name := in.Package.Provider; name != "" {
		if _, ok := in.Available[name]; !ok {
			final = model.Resolution{
				Provider:   name,
				Reason:     "configuration requests this provider, but it is not available",
				Confidence: model.ConfidenceCertain,
				Forced:     true,
			}
			record("explicit configuration", "selected", name, final.Reason, true)
		} else {
			final = model.Resolution{
				Provider:   name,
				Reason:     "configuration explicitly selects this provider",
				Confidence: model.ConfidenceCertain,
				Forced:     true,
			}
			record("explicit configuration", "selected", name, final.Reason, true)
		}
		return Explanation{Package: in.Package, Final: final, Steps: steps}
	}
	record("explicit configuration", "skipped", "", "no explicit provider in configuration", false)

	existing := in.Existing
	if existing == nil && in.Locator != nil && isExecutableName(in.Package.Name) {
		found := in.Locator.Locate(in.Package.Name)
		if found.Installation != nil {
			existing = found.Installation
		}
	}

	// Rule 2: keep existing ownership.
	if existing != nil && existing.Provider != "" {
		// A system-owned copy is not an interesting incumbent: Starpoint does
		// not manage the operating system's software, so a user asking to
		// install python is asking for a managed copy, not to adopt
		// /usr/bin/python3.
		//
		// The exception is a name with a single known owner. `rustc` is rustup's
		// even when Homebrew also happens to ship one; adopting whichever copy
		// was found first would silently record the wrong owner and bury a real
		// duplicate behind a decision nobody made. The extra copy is reported
		// by `doctor` instead.
		if existing.Provider != system.Name {
			if owner, known := ownerHint(strings.ToLower(in.Package.Name)); !known || owner == existing.Provider {
				final = model.Resolution{
					Provider:   existing.Provider,
					Reason:     existingReason(existing),
					Confidence: model.ConfidenceCertain,
				}
				record("existing ownership", "selected", existing.Provider, final.Reason, true)
				return Explanation{Package: in.Package, Final: final, Steps: steps}
			}
		}
		record("existing ownership", "skipped", "", "existing provider is system-owned or has known single owner", false)
	} else {
		record("existing ownership", "skipped", "", "no existing installation found", false)
	}

	// allowedSet is empty when the user expressed no preference, in which case
	// every available provider is a candidate. An explicit restriction is a
	// decision, so it outranks every hint below.
	allowedSet := map[string]bool{}
	for _, n := range in.Allowed {
		allowedSet[n] = true
	}
	allowed := func(name string) bool { return len(allowedSet) == 0 || allowedSet[name] }

	// Rule 3: a name with a single known owner. Unlike the ecosystem rules below
	// this does not require the provider to be available, because guessing a
	// different owner for `rustc` would be worse than reporting that rustup is
	// missing.
	if owner, ok := ownerHint(strings.ToLower(in.Package.Name)); ok && allowed(owner) {
		if !available(owner, in.Available) {
			final = model.Resolution{
				Provider:   owner,
				Reason:     fmt.Sprintf("%s is owned by %s, which is not available here", in.Package.Name, owner),
				Confidence: model.ConfidenceHigh,
			}
			record("known owner", "selected", owner, final.Reason, true)
			return Explanation{Package: in.Package, Final: final, Steps: steps}
		}
		final = model.Resolution{
			Provider:   owner,
			Reason:     fmt.Sprintf("%s is owned by %s", in.Package.Name, owner),
			Confidence: model.ConfidenceCertain,
		}
		record("known owner", "selected", owner, final.Reason, true)
		return Explanation{Package: in.Package, Final: final, Steps: steps}
	}
	record("known owner", "skipped", "", "no single known owner for this package name", false)

	candidates := r.candidates(in)
	if len(candidates) == 0 {
		// Nothing is available. Report the system provider so the caller can
		// still describe intent, and let the state comparison produce a clear
		// "cannot install" message.
		final = model.Resolution{
			Provider:   system.Name,
			Reason:     "no available provider can install this software",
			Confidence: model.ConfidenceSpeculat,
		}
		record("available providers", "selected", system.Name, final.Reason, true)
		return Explanation{Package: in.Package, Final: final, Steps: steps}
	}
	record("available providers", "found", "", fmt.Sprintf("%d candidate(s): %s", len(candidates), strings.Join(candidates, ", ")), false)

	// Flatpak names applications with a reverse-DNS ID such as org.gimp.GIMP,
	// and nothing else Starpoint installs uses that shape. Without this rule an
	// app ID on a machine with a system package manager resolves to that
	// manager, which cannot install it and never offers Flatpak at all.
	if isAppID(in.Package.Name) {
		if allowed(flatpak.Name) && available(flatpak.Name, in.Available) {
			reason := "the name is a reverse-DNS application ID, which is how Flatpak names apps"
			final = model.Resolution{
				Provider:     flatpak.Name,
				Reason:       reason,
				Confidence:   model.ConfidenceHigh,
				Alternatives: r.others(candidates, flatpak.Name),
			}
			record("application ID", "selected", flatpak.Name, reason, true)
			return Explanation{Package: in.Package, Final: final, Steps: steps}
		}
		record("application ID", "skipped", "", "the name looks like an application ID, but flatpak is not available", false)
	} else {
		record("application ID", "skipped", "", "the package name is not a reverse-DNS application ID", false)
	}

	kind := hintKind(in.Package.Name)
	if kind != "" {
		if eco, ok := ecosystems[kind]; ok {
			for _, name := range eco.preferred {
				if !allowed(name) || !available(name, in.Available) {
					continue
				}
				reason := fmt.Sprintf("%s is the natural home for %s software", name, kindName(kind))
				final = model.Resolution{
					Provider:     name,
					Reason:       reason,
					Confidence:   model.ConfidenceHigh,
					Alternatives: r.others(candidates, name),
				}
				record("ecosystem hint", "selected", name, reason, true)
				return Explanation{Package: in.Package, Final: final, Steps: steps}
			}
		}
	}
	if kind != "" {
		record("ecosystem hint", "skipped", "", fmt.Sprintf("package name suggests %s ecosystem but no preferred provider available", kindName(kind)), false)
	} else {
		record("ecosystem hint", "skipped", "", "package name does not suggest a specific ecosystem", false)
	}

	// The project's ecosystem is the next signal, and a weak one. Being inside a
	// Go repository says what the repository is written in, not where the tools
	// a person installs from it should come from: `str install neofetch`
	// typed in a Go project is a request for neofetch, and `go install neofetch`
	// is not even a valid command. So the project only decides for a name that
	// is plausible in its ecosystem — one that has the ecosystem's own shape.
	// Every other name falls through to the system package manager below,
	// exactly as it would outside the project. It stays below an explicit
	// provider and an existing owner either way.
	if in.Ecosystem != "" && !plausibleIn(in.Ecosystem, in.Package.Name) {
		record("project ecosystem", "skipped", "", fmt.Sprintf("the current project is a %s project, but %s is not a %s",
			kindName(in.Ecosystem), in.Package.Name, ecosystemShape(in.Ecosystem)), false)
	} else if in.Ecosystem != "" {
		if eco, ok := ecosystems[in.Ecosystem]; ok {
			for _, name := range eco.preferred {
				if !allowed(name) || !available(name, in.Available) {
					continue
				}
				final = model.Resolution{
					Provider:     name,
					Reason:       fmt.Sprintf("the current project is a %s project", kindName(in.Ecosystem)),
					Confidence:   model.ConfidenceLow,
					Alternatives: r.others(candidates, name),
				}
				record("project ecosystem", "selected", name, final.Reason, true)
				return Explanation{Package: in.Package, Final: final, Steps: steps}
			}
		}
		record("project ecosystem", "skipped", "", fmt.Sprintf("project is %s but no preferred provider available", kindName(in.Ecosystem)), false)
	} else {
		record("project ecosystem", "skipped", "", "no project ecosystem detected", false)
	}

	// Rule 3, general case: a system package manager, but if more than one is
	// available the choice is genuinely ambiguous and must be asked about.
	var systemCandidates []string
	for _, name := range systemManagers {
		if allowed(name) && available(name, in.Available) {
			systemCandidates = append(systemCandidates, name)
		}
	}
	if len(systemCandidates) == 1 {
		// One system package manager is not the same as one viable provider.
		// When language providers could also install an unrecognised name, the
		// choice between "the OS package" and "the PyPI/npm/crates package" is
		// exactly the ambiguity the spec says to ask about, so it is reported
		// as such with the system manager as the default answer.
		languages := preferProject(languageCandidates(in.Package.Name, candidates, systemCandidates), in.Ecosystem)
		if len(languages) > 0 {
			final = model.Resolution{
				Provider: systemCandidates[0],
				Reason: fmt.Sprintf("%s is the only system package manager, but %s could also provide it and nothing identifies which ecosystem it belongs to",
					systemCandidates[0], strings.Join(languages, ", ")),
				Confidence:   model.ConfidenceLow,
				Ambiguous:    true,
				Alternatives: languages,
			}
			return r.settle(in, final, "system package manager", steps)
		}
		final = model.Resolution{
			Provider:     systemCandidates[0],
			Reason:       "only one system package manager is available",
			Confidence:   model.ConfidenceHigh,
			Alternatives: r.others(candidates, systemCandidates[0]),
		}
		record("system package manager", "selected", systemCandidates[0], final.Reason, true)
		return Explanation{Package: in.Package, Final: final, Steps: steps}
	}
	if len(systemCandidates) > 1 {
		final = model.Resolution{
			Provider:     systemCandidates[0],
			Reason:       "several package managers are available and none is clearly better",
			Confidence:   model.ConfidenceLow,
			Ambiguous:    true,
			Alternatives: systemCandidates[1:],
		}
		return r.settle(in, final, "system package manager", steps)
	}
	record("system package manager", "skipped", "", "no system package managers available", false)

	// Nothing specific matched. Only language providers are left, and an
	// unrecognised name says nothing about which ecosystem it belongs to. With
	// more than one available the choice is a real coin flip, so it is asked
	// about rather than settled by registration order — which would otherwise
	// make the answer depend on an arbitrary detail of how providers are wired.
	if len(candidates) > 1 {
		final = model.Resolution{
			Provider:     candidates[0],
			Reason:       "nothing identifies which ecosystem this software belongs to",
			Confidence:   model.ConfidenceLow,
			Ambiguous:    true,
			Alternatives: r.others(candidates, candidates[0]),
		}
		return r.settle(in, final, "fallback", steps)
	}
	final = model.Resolution{
		Provider:   candidates[0],
		Reason:     "only one available provider can install this software",
		Confidence: model.ConfidenceLow,
	}
	record("fallback", "selected", candidates[0], final.Reason, true)
	return Explanation{Package: in.Package, Final: final, Steps: steps}
}

// settle finishes an ambiguous resolution. The rule that found the tie is
// recorded as ambiguous; then the configured preference is consulted. The
// first preferred provider that is one of the tied providers, is available
// and allowed, and can honour the entry's pin, channel and scope decides.
// Without one, the ambiguity stands and the CLI asks, or refuses unattended,
// exactly as before.
func (r *Resolver) settle(in Input, tied model.Resolution, rule string, steps []Step) Explanation {
	steps = append(steps, Step{Rule: rule, Outcome: "ambiguous", Provider: tied.Provider, Reason: tied.Reason, Applied: len(in.Preferred) == 0})
	if len(in.Preferred) == 0 {
		return Explanation{Package: in.Package, Final: tied, Steps: steps}
	}
	pool := append([]string{tied.Provider}, tied.Alternatives...)
	inPool := map[string]bool{}
	for _, name := range pool {
		inPool[name] = true
	}
	allowedSet := map[string]bool{}
	for _, n := range in.Allowed {
		allowedSet[n] = true
	}
	var passed []string
	for _, name := range in.Preferred {
		if !inPool[name] {
			continue
		}
		if len(allowedSet) > 0 && !allowedSet[name] {
			passed = append(passed, name+" (not allowed for this command)")
			continue
		}
		h, ok := in.Available[name]
		if !ok || !h.Available {
			passed = append(passed, name+" (not available)")
			continue
		}
		if why := unsupported(in.Package, h.Capabilities); why != "" {
			passed = append(passed, name+" ("+why+")")
			continue
		}
		reason := fmt.Sprintf("%s is the first provider in [settings] providers (%s) that can install it; %s",
			name, strings.Join(in.Preferred, ", "), lowerFirst(tied.Reason))
		final := model.Resolution{
			Provider:     name,
			Reason:       reason,
			Confidence:   model.ConfidenceHigh,
			Alternatives: r.others(pool, name),
		}
		steps = append(steps, Step{Rule: "configured preference", Outcome: "selected", Provider: name, Reason: reason, Applied: true})
		return Explanation{Package: in.Package, Final: final, Steps: steps}
	}
	why := fmt.Sprintf("none of [settings] providers (%s) is one of the tied providers (%s)",
		strings.Join(in.Preferred, ", "), strings.Join(pool, ", "))
	if len(passed) > 0 {
		why = fmt.Sprintf("no preferred provider can settle it: %s", strings.Join(passed, "; "))
	}
	steps[len(steps)-1].Applied = true
	steps = append(steps, Step{Rule: "configured preference", Outcome: "skipped", Reason: why, Applied: false})
	return Explanation{Package: in.Package, Final: tied, Steps: steps}
}

// unsupported says why a provider cannot honour what the entry asks for, or
// "" when it can: a version pin needs PinVersion, a channel needs Channels,
// project scope needs installation in a directory, and global or user scope
// must be where the provider installs.
func unsupported(pkg model.Package, caps provider.Capabilities) string {
	switch {
	case !caps.Install:
		return "cannot install"
	case pkg.Version != "" && !caps.PinVersion:
		return "cannot pin version " + pkg.Version
	case pkg.Channel != "" && !caps.Channels:
		return "has no channels"
	case pkg.Scope == model.ScopeProject && !caps.ProjectInstall && !caps.CanInstallInContext:
		return "cannot install into a project"
	case (pkg.Scope == model.ScopeGlobal || pkg.Scope == model.ScopeUser) && caps.DefaultScope != "" && caps.DefaultScope != pkg.Scope:
		return fmt.Sprintf("installs at %s scope, not %s", caps.DefaultScope, pkg.Scope)
	}
	return ""
}

// lowerFirst lowercases the first byte of a reason so it reads on after a
// semicolon.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// candidates lists the providers that can act, filtered by availability and the
// user's restrictions, preferring providers that can actually install.
func (r *Resolver) candidates(in Input) []string {
	allowed := map[string]bool{}
	for _, n := range in.Allowed {
		allowed[n] = true
	}
	var installable, other []string
	for _, name := range r.reg.Names() {
		if len(allowed) > 0 && !allowed[name] {
			continue
		}
		h, ok := in.Available[name]
		if !ok || !h.Available {
			continue
		}
		if h.Capabilities.Install {
			installable = append(installable, name)
		} else {
			other = append(other, name)
		}
	}
	if len(installable) > 0 {
		return installable
	}
	return other
}

// languageCandidates returns the candidates that are neither system package
// managers nor a toolchain-only owner, sorted. rustup installs toolchains, not
// arbitrary software, so it is never a plausible home for an unrecognised name;
// nor is go for anything but a module path, because `go install neofetch` is
// not a command Go can carry out.
func languageCandidates(name string, candidates, system []string) []string {
	skip := map[string]bool{rustup.Name: true}
	if !golang.LooksLikeModulePath(name) {
		skip[golang.Name] = true
	}
	for _, s := range system {
		skip[s] = true
	}
	var out []string
	for _, c := range candidates {
		if !skip[c] {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// preferProject moves the current project's own providers to the front of a
// list of alternatives, best first, keeping the rest in order. The project's
// ecosystem does not decide for a name it cannot vouch for, but when the user
// is asked anyway, the providers of the project they are standing in are the
// likeliest second choice and are offered first.
func preferProject(alternatives []string, kind provider.Kind) []string {
	eco, ok := ecosystems[kind]
	if !ok || len(alternatives) < 2 {
		return alternatives
	}
	out := make([]string, 0, len(alternatives))
	taken := map[string]bool{}
	for _, name := range eco.preferred {
		for _, alt := range alternatives {
			if alt == name && !taken[alt] {
				out = append(out, alt)
				taken[alt] = true
			}
		}
	}
	for _, alt := range alternatives {
		if !taken[alt] {
			out = append(out, alt)
		}
	}
	return out
}

// plausibleIn reports whether a package name could belong to an ecosystem by
// its shape alone, which is the only evidence the project's ecosystem can add
// to a name nothing else recognises.
//
// Only Go and Node have a shape to check: a Go module path
// (golang.org/x/tools/gopls) and an npm scoped package (@scope/name) cannot be
// anything else. A bare name in a Rust, Python or Ruby project could just as
// well be a system tool, and none of those providers can confirm a name exists
// in its registry without a network round trip, so the project alone does not
// settle it.
func plausibleIn(kind provider.Kind, name string) bool {
	switch kind {
	case provider.KindGo:
		return golang.LooksLikeModulePath(name)
	case provider.KindNode:
		return isScopedNPMName(name)
	}
	return false
}

// ecosystemShape names the shape plausibleIn looks for, for the explanation of
// a skipped project rule.
func ecosystemShape(kind provider.Kind) string {
	switch kind {
	case provider.KindGo:
		return "Go module path (such as golang.org/x/tools/gopls)"
	case provider.KindNode:
		return "scoped npm package (such as @scope/name)"
	}
	return "name only " + kindName(kind) + " could provide"
}

// isScopedNPMName reports whether a name is an npm scoped package, @scope/name.
func isScopedNPMName(name string) bool {
	if !strings.HasPrefix(name, "@") {
		return false
	}
	scope, pkg, ok := strings.Cut(name[1:], "/")
	return ok && scope != "" && pkg != "" && !strings.ContainsAny(pkg, "/ ")
}

// existingReason explains who already has the software. Some providers report a
// name and version but no location, and a reason that trails off into a bare
// "installed at " says nothing.
func existingReason(existing *model.Installation) string {
	if existing.Path != "" {
		return "already installed at " + existing.Path
	}
	return "already installed through " + existing.Provider
}

// available reports whether a provider is usable on this machine.
func available(name string, health map[string]provider.Health) bool {
	h, ok := health[name]
	return ok && h.Available
}

func (r *Resolver) others(candidates []string, exclude string) []string {
	var out []string
	for _, c := range candidates {
		if c != exclude {
			out = append(out, c)
		}
	}
	sort.Strings(out)
	return out
}

// hintKind returns the ecosystem a name implies, if any.
func hintKind(name string) provider.Kind {
	return nameHints[strings.ToLower(name)]
}

// isAppID reports whether a name has the shape Flatpak requires for an
// application ID: at least three dot-separated elements, each made of letters,
// digits, underscores or hyphens, as in org.gimp.GIMP.
//
// The shape is a reliable signal rather than a guess: language packages use
// dashes (or a slash for a scope), and no system package manager names anything
// this way. Flatpak itself enforces the same form, so a name that matches it is
// going to Flatpak or nowhere.
func isAppID(name string) bool {
	parts := strings.Split(name, ".")
	if len(parts) < 3 {
		return false
	}
	for _, part := range parts {
		if part == "" {
			return false
		}
		for i := 0; i < len(part); i++ {
			c := part[i]
			switch {
			case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
				// Valid anywhere in an element.
			case c == '-' || (c >= '0' && c <= '9'):
				// Valid after the first byte: an element may not start
				// with a digit or a hyphen.
				if i == 0 {
					return false
				}
			default:
				return false
			}
		}
	}
	return true
}

// kindName renders a kind for humans.
func kindName(k provider.Kind) string {
	switch k {
	case provider.KindPython:
		return "Python"
	case provider.KindRust:
		return "Rust"
	case provider.KindNode:
		return "Node.js"
	case provider.KindGo:
		return "Go"
	default:
		return string(k)
	}
}

// isExecutableName reports whether a name could plausibly be an executable,
// which is the condition for consulting PATH.
func isExecutableName(name string) bool {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return false
	}
	return true
}
