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
//     but uv is the right home.
//
// When those rules do not produce a clear winner the resolver reports ambiguity
// rather than guessing, so the CLI can ask a useful question.
package resolve

import (
	"fmt"
	"sort"
	"strings"

	"github.com/starpoint/starpoint/internal/locator"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/system"
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
	provider.KindPython: {kind: provider.KindPython, preferred: []string{"uv", "pipx"}},
	provider.KindRust:   {kind: provider.KindRust, preferred: []string{"cargo"}},
	provider.KindNode:   {kind: provider.KindNode, preferred: []string{"npm"}},
	provider.KindGo:     {kind: provider.KindGo, preferred: []string{"go"}},
}

// systemManagers are general-purpose providers, used as a fallback when no
// ecosystem-specific provider claims the software.
var systemManagers = []string{"brew", "apt", "dnf", "pacman", "nix", "winget", "choco"}

// nameHints map well-known software names to the ecosystem that should own them.
// This is intentionally small: it covers cases where the name itself implies a
// language, not an attempt to catalogue every package.
var nameHints = map[string]provider.Kind{
	"python":  provider.KindPython,
	"python3": provider.KindPython,
	"pip":     provider.KindPython,
	"pipx":    provider.KindPython,
	"uv":      provider.KindPython,
	"ruff":    provider.KindPython,
	"black":   provider.KindPython,
	"poetry":  provider.KindPython,
	"rye":     provider.KindPython,

	"rustc":  provider.KindRust,
	"cargo":  provider.KindRust,
	"rustup": provider.KindRust,
	"rg":     provider.KindRust,

	"node": provider.KindNode,
	"npm":  provider.KindNode,
	"pnpm": provider.KindNode,
	"yarn": provider.KindNode,
	"bun":  provider.KindNode,
	"deno": provider.KindNode,

	"go":            provider.KindGo,
	"gopls":         provider.KindGo,
	"golangci-lint": provider.KindGo,
}

// Resolver makes provider decisions.
type Resolver struct {
	reg *provider.Registry
}

// New builds a Resolver over a registry.
func New(reg *provider.Registry) *Resolver { return &Resolver{reg: reg} }

// Resolve decides who should own the package.
func (r *Resolver) Resolve(in Input) model.Resolution {
	// Rule 1: explicit configuration.
	if name := in.Package.Provider; name != "" {
		if _, ok := in.Available[name]; !ok {
			return model.Resolution{
				Provider:   name,
				Reason:     "configuration requests this provider, but it is not available",
				Confidence: model.ConfidenceCertain,
				Forced:     true,
			}
		}
		return model.Resolution{
			Provider:   name,
			Reason:     "configuration explicitly selects this provider",
			Confidence: model.ConfidenceCertain,
			Forced:     true,
		}
	}

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
		if existing.Provider != system.Name {
			res := model.Resolution{
				Provider:   existing.Provider,
				Reason:     fmt.Sprintf("already installed at %s", existing.Path),
				Confidence: model.ConfidenceCertain,
			}
			return res
		}
	}

	candidates := r.candidates(in)
	if len(candidates) == 0 {
		// Nothing is available. Report the system provider so the caller can
		// still describe intent, and let the state comparison produce a clear
		// "cannot install" message.
		return model.Resolution{
			Provider:   system.Name,
			Reason:     "no available provider can install this software",
			Confidence: model.ConfidenceSpeculat,
		}
	}

	// allowedSet is empty when the user expressed no preference, in which case
	// every available provider is a candidate.
	allowedSet := map[string]bool{}
	for _, n := range in.Allowed {
		allowedSet[n] = true
	}
	allowed := func(name string) bool { return len(allowedSet) == 0 || allowedSet[name] }

	kind := hintKind(in.Package.Name)
	if kind != "" {
		if eco, ok := ecosystems[kind]; ok {
			for _, name := range eco.preferred {
				if !allowed(name) || !available(name, in.Available) {
					continue
				}
				reason := fmt.Sprintf("%s is the natural home for %s software", name, kindName(kind))
				return model.Resolution{
					Provider:     name,
					Reason:       reason,
					Confidence:   model.ConfidenceHigh,
					Alternatives: r.others(candidates, name),
				}
			}
		}
	}

	// The project's ecosystem is the next signal. Running inside a Rust project
	// is good evidence that a Rust binary is wanted, and it costs nothing to use
	// it — but it stays below an explicit provider and an existing owner.
	if in.Ecosystem != "" {
		if eco, ok := ecosystems[in.Ecosystem]; ok {
			for _, name := range eco.preferred {
				if !allowed(name) || !available(name, in.Available) {
					continue
				}
				return model.Resolution{
					Provider:     name,
					Reason:       fmt.Sprintf("the current project is a %s project", kindName(in.Ecosystem)),
					Confidence:   model.ConfidenceLow,
					Alternatives: r.others(candidates, name),
				}
			}
		}
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
		return model.Resolution{
			Provider:     systemCandidates[0],
			Reason:       "only one package manager is available",
			Confidence:   model.ConfidenceHigh,
			Alternatives: r.others(candidates, systemCandidates[0]),
		}
	}
	if len(systemCandidates) > 1 {
		return model.Resolution{
			Provider:     systemCandidates[0],
			Reason:       "several package managers are available and none is clearly better",
			Confidence:   model.ConfidenceLow,
			Ambiguous:    true,
			Alternatives: systemCandidates[1:],
		}
	}

	// Fall back to whatever is available at all.
	return model.Resolution{
		Provider:     candidates[0],
		Reason:       "no package manager matched; using an available provider",
		Confidence:   model.ConfidenceLow,
		Ambiguous:    len(candidates) > 1,
		Alternatives: r.others(candidates, candidates[0]),
	}
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
