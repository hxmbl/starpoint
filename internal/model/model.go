// Package model holds the core vocabulary of Starpoint: desired state (what the
// configuration asks for), current state (what providers report), the
// relationship between them, and the resulting plan.
//
// Everything in this package is provider-agnostic data. It deliberately keeps
// provider-specific fields (channel, args) as opaque hints rather than trying to
// flatten every package manager into one shape.
package model

import (
	"fmt"
	"sort"
	"strings"
)

// Scope describes where software is expected to live. It is a placement
// concept, not a provider concept: several providers can serve the same scope.
type Scope string

const (
	// ScopeGlobal is machine-wide and managed by an administrator-level provider
	// (for example /usr/bin/git from the operating system).
	ScopeGlobal Scope = "global"
	// ScopeUser is per-user and lives under the invoking user's home directory.
	ScopeUser Scope = "user"
	// ScopeProject is relative to the project directory Starpoint was run in.
	ScopeProject Scope = "project"
)

func (s Scope) String() string { return string(s) }

// Valid reports whether the scope is one Starpoint understands.
func (s Scope) Valid() bool {
	switch s {
	case ScopeGlobal, ScopeUser, ScopeProject:
		return true
	}
	return false
}

// Package is a single entry in the source-of-truth configuration: something the
// user wants to exist.
type Package struct {
	// Name is the software name as the owning provider knows it.
	Name string `toml:"name" json:"name"`
	// Version is an explicit version constraint. Empty means "any version the
	// provider currently offers". A non-empty value is a hard pin: Starpoint
	// will not move it and will report conflicts instead of silently rewriting it.
	Version string `toml:"version,omitempty" json:"version,omitempty"`
	// Provider is an explicit provider override. Empty means "let Starpoint
	// decide"; once decided, the resolved name is written back here.
	Provider string `toml:"provider,omitempty" json:"provider,omitempty"`
	// Scope is the desired placement. Empty means "unconstrained".
	Scope Scope `toml:"scope,omitempty" json:"scope,omitempty"`
	// Channel is an opaque provider-specific hint, e.g. a Homebrew cask name, a
	// Rust toolchain, or an npm tag. Starpoint stores and forwards it but does
	// not interpret it.
	Channel string `toml:"channel,omitempty" json:"channel,omitempty"`
	// Reason records why Starpoint chose this provider. Informational.
	Reason string `toml:"reason,omitempty" json:"reason,omitempty"`
	// Origin records which configuration file the entry came from. Not serialized.
	Origin Origin `toml:"-" json:"origin,omitempty"`
}

// Origin says which file a desired package came from.
type Origin struct {
	// Path is the configuration file the entry was read from.
	Path string `json:"path,omitempty"`
	// Project is true when the entry came from a project-local configuration.
	Project bool `json:"project,omitempty"`
}

func (p Package) Key() string { return p.Name }

// Describe renders a short human description used in doctor output.
func (p Package) Describe() string {
	var b strings.Builder
	b.WriteString(p.Name)
	if p.Version != "" {
		fmt.Fprintf(&b, "==%s", p.Version)
	}
	if p.Channel != "" {
		fmt.Fprintf(&b, " [%s]", p.Channel)
	}
	return b.String()
}

// Installation is an observed piece of software: something a provider says is
// present, and where.
type Installation struct {
	// Name is the provider's name for the software.
	Name string `json:"name"`
	// Version is the observed version string, verbatim from the provider.
	Version string `json:"version,omitempty"`
	// Path is the resolved executable or install location.
	Path string `json:"path,omitempty"`
	// Provider is the provider that owns this installation.
	Provider string `json:"provider"`
	// Scope is the observed placement.
	Scope Scope `json:"scope,omitempty"`
	// Pinned reports whether the provider is holding this at an explicitly
	// requested version.
	Pinned bool `json:"pinned,omitempty"`
}

// Exists reports whether an installation record is present.
func (i *Installation) Exists() bool { return i != nil && i.Name != "" }

// Status is the verdict comparing desired state with current state.
type Status string

const (
	// StatusCorrect means the desired package is present via the desired
	// provider at an acceptable version.
	StatusCorrect Status = "correct"
	// StatusMissing means the desired package is not present anywhere.
	StatusMissing Status = "missing"
	// StatusProviderDiffers means the software exists, but a different provider
	// owns it than the configuration wants. This is a transition, not a plain
	// install: Starpoint must move it rather than install a second copy.
	StatusProviderDiffers Status = "provider-differs"
	// StatusVersionDiffers means the software is present through the right
	// provider but at a version that does not match an explicit pin.
	StatusVersionDiffers Status = "version-differs"
	// StatusUnpinnedConflict means the software is present through the desired
	// provider but the requested version cannot be enforced by that provider.
	StatusUnpinnedConflict Status = "version-unsatisfiable"
)

// Healthy reports whether the status needs no action.
func (s Status) Healthy() bool { return s == StatusCorrect }

// Resolution is the outcome of provider resolution for one package.
type Resolution struct {
	// Provider is the chosen provider name.
	Provider string `json:"provider"`
	// Reason explains the choice in one line.
	Reason string `json:"reason,omitempty"`
	// Confidence is high for a forced or catalog-backed choice, low when the
	// choice was a coin flip between equally plausible providers.
	Confidence Confidence `json:"confidence"`
	// Ambiguous is true when the choice was not decisive.
	Ambiguous bool `json:"ambiguous"`
	// Alternatives lists other plausible providers, best first. Used to ask the
	// user a useful question instead of a binary one.
	Alternatives []string `json:"alternatives,omitempty"`
	// Forced is true when the configuration pinned the provider explicitly.
	Forced bool `json:"forced,omitempty"`
}

// Confidence ranks how sure the resolver is.
type Confidence string

const (
	ConfidenceCertain  Confidence = "certain"
	ConfidenceHigh     Confidence = "high"
	ConfidenceLow      Confidence = "low"
	ConfidenceSpeculat Confidence = "speculative"
)

// State is the comparison of one desired package with the observed world.
type State struct {
	Package Package       `json:"package"`
	Res     Resolution    `json:"resolution"`
	Desired *Installation `json:"desired,omitempty"`
	Current *Installation `json:"current,omitempty"`
	// Elsewhere lists other providers that already have this software. It is
	// what turns "missing" into "provider-differs", and it is how Starpoint
	// avoids installing duplicates.
	Elsewhere []Installation `json:"elsewhere,omitempty"`
	Status    Status         `json:"status"`
	// Notes carries non-fatal observations worth surfacing.
	Notes []string `json:"notes,omitempty"`
}

// NeedsAction reports whether the state requires a change to converge.
func (s State) NeedsAction() bool { return !s.Status.Healthy() }

// Action is a single convergence step.
type Action string

const (
	ActionInstall   Action = "install"
	ActionRemove    Action = "remove"
	ActionUpdate    Action = "update"
	ActionAdopt     Action = "adopt"
	ActionRelocate  Action = "relocate"
	ActionRecord    Action = "record"
	ActionReinstall Action = "reinstall"
)

// Plan is the ordered set of changes needed to converge current state onto the
// configuration, plus everything that was observed but needs no action.
type Plan struct {
	States  []State         `json:"states"`
	Actions []PlannedAction `json:"actions"`
	// Transitions are the provider moves among the actions, promoted to
	// first-class records because they are the one operation that touches two
	// providers at once and can therefore leave the machine mid-way.
	Transitions []Transition `json:"transitions,omitempty"`
	// Warnings are conditions Starpoint will not resolve on its own.
	Warnings []Warning `json:"warnings,omitempty"`
	// DryRun records whether the plan was produced without applying it.
	DryRun bool `json:"dry_run"`
}

// Empty reports whether there is nothing to do.
func (p Plan) Empty() bool { return len(p.Actions) == 0 }

// PlannedAction is an action together with the state that produced it and
// whether applying it would change the configuration.
type PlannedAction struct {
	Action Action `json:"action"`
	State  State  `json:"state"`
	// ConfigChange describes the edit Starpoint intends to persist. Nil means
	// the action leaves the configuration alone.
	ConfigChange *ConfigEdit `json:"config_change,omitempty"`
	// Transition is the provider move this action performs, when it is one.
	Transition *Transition `json:"transition,omitempty"`
	Note       string      `json:"note,omitempty"`
}

// ConfigEdit describes a pending mutation of the source of truth. Starpoint
// applies these as part of a successful convergence, never speculatively.
type ConfigEdit struct {
	// File is the configuration path to edit.
	File string `json:"file"`
	// Upsert is the entry to write or replace, keyed by name.
	Upsert *Package `json:"upsert,omitempty"`
	// Remove deletes the named entry from the file.
	Remove string `json:"remove,omitempty"`
}

// Severity classifies a warning.
type Severity string

const (
	SeverityInfo  Severity = "info"
	SeverityWarn  Severity = "warning"
	SeverityError Severity = "error"
)

// Warning is a condition the user should know about that is not itself an
// action Starpoint will take.
type Warning struct {
	Severity Severity `json:"severity"`
	Package  string   `json:"package,omitempty"`
	Provider string   `json:"provider,omitempty"`
	Message  string   `json:"message"`
}

func (w Warning) String() string {
	scope := w.Package
	if scope == "" {
		scope = "starpoint"
	}
	return fmt.Sprintf("%s: %s: %s", w.Severity, scope, w.Message)
}

// Version matches reports whether an observed version satisfies an explicit
// configuration pin. Comparison is textual on purpose: package managers version
// their software differently and Starpoint must not invent a lowest common
// denominator ordering. Exact pins must be exact; "latest"-style constraints are
// handled by the caller as unconstrained.
func VersionMatches(pin, observed string) bool {
	if pin == "" {
		return true
	}
	if observed == "" {
		return false
	}
	pin = strings.TrimSpace(pin)
	observed = strings.TrimSpace(observed)
	if pin == observed {
		return true
	}
	// Allow a leading "==" as commonly written in requirements files.
	pin = strings.TrimPrefix(pin, "==")
	pin = strings.TrimPrefix(pin, "=")
	return pin == observed
}

// SortStates orders states for stable output: by package name.
func SortStates(states []State) {
	sort.SliceStable(states, func(i, j int) bool {
		return states[i].Package.Name < states[j].Package.Name
	})
}
