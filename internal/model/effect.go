package model

import (
	"sort"
	"strings"
)

// EffectOp is what happened, or would happen, to one thing on the machine.
type EffectOp string

const (
	// EffectCreate means the thing did not exist before and does afterwards.
	EffectCreate EffectOp = "create"
	// EffectModify means the thing existed before and afterwards and changed in
	// place: a file's content, a package's version, a configuration entry.
	EffectModify EffectOp = "modify"
	// EffectReplace means the thing at a path was swapped for something
	// different: a symlink into one tool's directory became another tool's
	// script, or a link now points somewhere else. Whatever was there before is
	// gone, so a replacement is always destructive.
	EffectReplace EffectOp = "replace"
	// EffectDelete means the thing existed before and does not afterwards.
	EffectDelete EffectOp = "delete"
	// EffectChown means Starpoint gave a path to the user an elevated run
	// acted for, so it is not left owned by root. From and To are uid:gid.
	EffectChown EffectOp = "chown"
	// EffectChmod means Starpoint widened the permissions of a path a
	// provider created in a shared location, so the elevated account's
	// umask does not leave it usable only by root. From and To are octal
	// modes. Bits are only ever added.
	EffectChmod EffectOp = "chmod"
)

// EffectKind says what sort of thing an effect touched.
type EffectKind string

const (
	// EffectPackage is a package as its provider knows it, whether or not the
	// provider reports a path for it.
	EffectPackage EffectKind = "package"
	// EffectExecutable is a regular executable file.
	EffectExecutable EffectKind = "executable"
	// EffectSymlink is a symbolic link, such as the shims uv and Homebrew put on
	// PATH.
	EffectSymlink EffectKind = "symlink"
	// EffectFile is any other regular file.
	EffectFile EffectKind = "file"
	// EffectDirectory is a directory, such as a tool environment or a
	// toolchain.
	EffectDirectory EffectKind = "directory"
	// EffectConfig is one of Starpoint's own configuration files.
	EffectConfig EffectKind = "config"
	// EffectCommand is what a command name resolves to on PATH. It is the
	// environment as a shell sees it, which is what a user actually runs.
	EffectCommand EffectKind = "command"
	// EffectShellProfile is a shell start-up file (~/.profile, ~/.zshrc, ...)
	// that can change PATH or the environment for every future shell.
	EffectShellProfile EffectKind = "shell_profile"
)

// Evidence says how Starpoint knows about an effect.
type Evidence string

const (
	// EvidenceObserved means Starpoint looked before and after the step and saw
	// the difference on disk.
	EvidenceObserved Evidence = "observed"
	// EvidenceReported means the provider said so (for example the path its
	// install returned) but the location was not watched before the step, so
	// Starpoint cannot confirm from the disk what was there before.
	EvidenceReported Evidence = "reported"
	// EvidencePlanned means the effect is a prediction: a dry run, or the plan
	// shown before asking for confirmation. Nothing has happened yet.
	EvidencePlanned Evidence = "planned"
)

// Effect is one concrete change to the machine, or to Starpoint's own
// configuration, made or planned by an action.
//
// Effects are captured at the point they happen rather than inferred from
// what an action is called. A removal reports the files that disappeared, not
// the files a provider is assumed to own, and a step whose effects were not
// observed says so through Evidence instead of guessing.
type Effect struct {
	Op   EffectOp   `json:"op"`
	Kind EffectKind `json:"kind"`
	// Path is the filesystem path affected. A package without a known path, or
	// a command that no longer resolves, still has an effect with an empty Path
	// and a Name.
	Path string `json:"path,omitempty"`
	// Name is the package or command name, for effects that are about a name
	// rather than (or as well as) a path.
	Name string `json:"name,omitempty"`
	// From and To describe the change: versions for a package, link targets for
	// a symlink, resolved paths for a command.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Provider is the provider whose operation produced the effect.
	Provider string `json:"provider,omitempty"`
	// Destructive marks an effect that removes or overwrites something that
	// existed before: a deletion, a replacement, a command that stops
	// resolving, or a configuration value that is overwritten or dropped.
	Destructive bool `json:"destructive"`
	// Environment marks an effect that changes what a shell sees: a file in a
	// PATH directory, a command's resolution, or a shell start-up file.
	Environment bool     `json:"environment,omitempty"`
	Evidence    Evidence `json:"evidence"`
	// Detail is a short human explanation.
	Detail string `json:"detail,omitempty"`
	// Error says why an ownership or permission fix did not (fully) succeed.
	// Such an effect also raises a permission-fix-failed warning.
	Error string `json:"error,omitempty"`
	// Unexpected marks an ownership fix that should not have been needed:
	// something owned by root turned up in the invoking user's home after a
	// provider ran as that user. It is still fixed, and it raises a
	// root-owned-after-drop warning, because it means a bug or a provider
	// that escalated on its own.
	Unexpected bool `json:"unexpected,omitempty"`
}

// Key identifies an effect for de-duplication.
func (e Effect) Key() string {
	return strings.Join([]string{string(e.Op), string(e.Kind), e.Path, e.Name, e.Provider}, "\x00")
}

// FieldChange is one field of a configuration entry that an action changes.
type FieldChange struct {
	Field string `json:"field"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

// ConfigChange is an edit to one package entry in one configuration file.
type ConfigChange struct {
	// Path is the configuration file.
	Path string `json:"path"`
	// Change is "add", "remove" or "update".
	Change string `json:"change"`
	// Package is the entry's name.
	Package string `json:"package"`
	// Fields lists what changed. For "add" it is every field written, for
	// "remove" every field dropped.
	Fields []FieldChange `json:"fields,omitempty"`
	// CreatesFile is true when the file did not exist and is written fresh.
	CreatesFile bool `json:"creates_file,omitempty"`
	// Destructive is true when the edit drops an entry or overwrites a value
	// the file already held, such as a version pin.
	Destructive bool `json:"destructive"`
	// Applied is false for a dry run or a plan.
	Applied bool `json:"applied"`
}

// Config change kinds.
const (
	ConfigAdd    = "add"
	ConfigRemove = "remove"
	ConfigUpdate = "update"
)

// DiffEntry compares a configuration entry before and after an edit. A nil
// before is an addition and a nil after a removal. It returns nil when nothing
// would change.
func DiffEntry(path string, before, after *Package) *ConfigChange {
	if before == nil && after == nil {
		return nil
	}
	var b, a Package
	if before != nil {
		b = *before
	}
	if after != nil {
		a = *after
	}
	fields := []FieldChange{}
	destructive := false
	for _, f := range []struct{ name, from, to string }{
		{"provider", b.Provider, a.Provider},
		{"version", b.Version, a.Version},
		{"scope", string(b.Scope), string(a.Scope)},
		{"channel", b.Channel, a.Channel},
		{"reason", b.Reason, a.Reason},
	} {
		if f.from == f.to {
			continue
		}
		fields = append(fields, FieldChange{Field: f.name, From: f.from, To: f.to})
		// Filling in a value the file did not have adds information; replacing
		// or dropping one takes information away. The reason is commentary and
		// is not counted.
		if f.from != "" && f.name != "reason" {
			destructive = true
		}
	}
	c := &ConfigChange{Path: path, Fields: fields}
	switch {
	case before == nil:
		c.Change = ConfigAdd
		c.Package = a.Name
		c.Destructive = false
	case after == nil:
		c.Change = ConfigRemove
		c.Package = b.Name
		c.Destructive = true
	default:
		if len(fields) == 0 {
			return nil
		}
		c.Change = ConfigUpdate
		c.Package = a.Name
		c.Destructive = destructive
	}
	return c
}

// ChangeKind classifies an action-level transition.
type ChangeKind string

const (
	// ChangeProvider is a move of ownership from one provider to another.
	ChangeProvider ChangeKind = "provider"
	// ChangeVersion is a move from one version to another.
	ChangeVersion ChangeKind = "version"
)

// ActionTransition is one before-and-after an action brings about: a package
// moving from uv to pip, or from 0.5.0 to 0.6.0. The full provider-move record,
// with phases and verification, is the separate Transition type.
type ActionTransition struct {
	Kind ChangeKind `json:"kind"`
	From string     `json:"from,omitempty"`
	To   string     `json:"to,omitempty"`
	// Phase is how far a provider move got, when the action is one.
	Phase TransitionPhase `json:"phase,omitempty"`
}

// Outcome is what became of an action.
type Outcome string

const (
	// OutcomePlanned is a dry run or a plan: nothing was done.
	OutcomePlanned Outcome = "planned"
	// OutcomeApplied means the action was carried out.
	OutcomeApplied Outcome = "applied"
	// OutcomeFailed means the action was attempted and did not complete. Its
	// effects still list whatever did change.
	OutcomeFailed Outcome = "failed"
	// OutcomeReported means there was nothing to do to the machine: a
	// conflict Starpoint reports but will not resolve on its own.
	OutcomeReported Outcome = "reported"
	// OutcomeNotRun means the action was planned but never attempted, because
	// an earlier step stopped the command.
	OutcomeNotRun Outcome = "not_run"
)

// Destructive reports whether any effect or configuration change of an action
// removes or overwrites something.
func (a PlannedAction) Destructive() bool {
	for _, e := range a.Effects {
		if e.Destructive {
			return true
		}
	}
	for _, c := range a.ConfigChanges {
		if c.Destructive {
			return true
		}
	}
	return false
}

// MergeEffects appends effects that are not already present, keeping order.
func MergeEffects(dst []Effect, more ...Effect) []Effect {
	seen := map[string]bool{}
	for _, e := range dst {
		seen[e.Key()] = true
	}
	for _, e := range more {
		if seen[e.Key()] {
			continue
		}
		seen[e.Key()] = true
		dst = append(dst, e)
	}
	return dst
}

// SortEffects orders effects for stable output: packages and configuration
// first, then paths, then command resolution.
func SortEffects(effects []Effect) {
	rank := func(k EffectKind) int {
		switch k {
		case EffectPackage:
			return 0
		case EffectConfig:
			return 1
		case EffectCommand:
			return 3
		case EffectShellProfile:
			return 4
		default:
			return 2
		}
	}
	sort.SliceStable(effects, func(i, j int) bool {
		ri, rj := rank(effects[i].Kind), rank(effects[j].Kind)
		if ri != rj {
			return ri < rj
		}
		return effects[i].Path < effects[j].Path
	})
}
