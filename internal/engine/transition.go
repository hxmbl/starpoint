package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/starpoint/starpoint/internal/config"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/system"
)

// Verify asks a provider whether an installation actually works.
//
// Two kinds of evidence are combined, because either alone is misleading. A
// provider's inventory answers "did I install it"; PATH answers "would the
// command run it". When they disagree, the software is installed but not in
// effect, and treating that as success is how a transition breaks a working
// command.
func (e *Engine) Verify(ctx context.Context, name string, p provider.Provider) model.Verification {
	if p == nil {
		return model.Verification{Method: "none", Note: "no provider was available to ask"}
	}
	v := model.Verification{Provider: p.Name(), Method: p.Name() + " inventory"}
	inst, err := p.Installed(ctx, model.Package{Name: name})
	if err != nil || inst == nil {
		if err != nil {
			v.Note = err.Error()
		} else {
			v.Note = p.Name() + " does not report it"
		}
		return v
	}
	v.Present = true
	if inst.Version != "" {
		v.Version = inst.Version
	}

	if e.locator == nil || !executableShaped(name) {
		// There is no command by this name to look up — a cask, a library, or a
		// crate whose binary has another name. The inventory is the only evidence
		// available, and it is accepted with a note rather than treated as failure.
		if inst.Path != "" {
			v.Resolved = true
			v.Method = p.Name() + " inventory and install path"
		}
		return v
	}
	if verifier, ok := p.(provider.Verifier); ok && p.Capabilities().Verify {
		if err := verifier.Verify(ctx, model.Package{Name: inst.Name}); err != nil {
			if pe, ok := provider.AsProviderError(err); !ok || pe.Kind != provider.ErrUnsupported {
				v.Note = "provider verification failed: " + err.Error()
				return v
			}
		}
	}

	found := e.locator.Locate(name)
	if found.Path == "" {
		v.Note = "no executable by this name is on PATH"
		return v
	}
	v.OnPath = found.Path
	v.Method = p.Name() + " inventory and PATH"
	if found.Installation != nil {
		v.PathOwner = found.Installation.Provider
	}
	v.Resolved = v.PathOwner == p.Name() || ownsPath(found.Path, inst.Path)
	if !v.Resolved && v.PathOwner == "" {
		// PATH resolved the name but nothing claims it. If the provider reported
		// the same directory, that still counts.
		v.Resolved = ownsPath(found.Path, inst.Path)
	}
	if v.Resolved && v.PathOwner == "" {
		v.Note = "PATH resolves inside the provider's own install directory"
	}
	return v
}

// executableShaped reports whether a name could be run as a command, and so
// worth corroborating on PATH.
func executableShaped(name string) bool {
	return name != "" && !strings.ContainsAny(name, "/\\")
}

// ownsPath reports whether a resolved path lies inside an installed location. It
// compares whole path components and follows symlinks, so a shim and the binary
// behind it compare equal.
func ownsPath(resolved, installed string) bool {
	if installed == "" {
		return false
	}
	if resolved == installed {
		return true
	}
	if samePath(resolved, installed) {
		return true
	}
	rel, err := filepath.Rel(filepath.Clean(installed), filepath.Clean(resolved))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// providerRef describes a provider's role in a transition.
func (e *Engine) providerRef(available map[string]provider.Health, name string) model.ProviderRef {
	ref := model.ProviderRef{Name: name}
	if h, ok := available[name]; ok {
		ref.Available = h.Available
		ref.Reason = h.Reason
	}
	if p, ok := e.reg.Get(name); ok {
		ref.CanInstall = p.Capabilities().Install
		ref.CanRemove = p.Capabilities().Remove
	}
	return ref
}

// TransitionFor works out the current state of a package's move between
// providers, entirely from what is on the machine.
//
// Nothing is remembered, which is what makes an interrupted transition
// recoverable: re-deriving gives the same answer whether the transition was
// interrupted a second or two years ago, and there is no journal that can drift
// from reality.
func (e *Engine) TransitionFor(ctx context.Context, st model.State, opts Options) model.Transition {
	return e.transitionFor(ctx, st, nil, opts)
}

// transitionFor derives a transition, optionally with an incumbent the caller
// already knows about.
//
// The override matters for the leftover case: once the target provider has the
// software the status reads "correct", so the state no longer carries the
// incumbent, yet the duplicate is still there and still needs reporting.
func (e *Engine) transitionFor(ctx context.Context, st model.State, incumbent *model.Installation, opts Options) model.Transition {
	available := e.health(ctx)
	pkg := st.Package
	to := st.Res.Provider
	from := ""
	if st.Current != nil {
		from = st.Current.Provider
	}
	if incumbent != nil && incumbent.Provider != "" {
		from = incumbent.Provider
	}
	if from == to {
		from = ""
	}
	// The incumbent is re-queried rather than taken from the state. A state is a
	// snapshot, and deriving a transition from a stale one would report a copy as
	// still present after it has been removed — which is exactly the confusion
	// that makes an interrupted transition impossible to reason about.
	previous := st.Current
	if from != "" {
		if old, ok := e.reg.Get(from); ok && available[from].Available {
			if inst, err := old.Installed(ctx, st.Package); err == nil {
				previous = inst
			}
		}
	}
	target, hasTarget := e.reg.Get(to)
	var installed *model.Installation
	var v model.Verification
	if hasTarget && available[to].Available {
		v = e.Verify(ctx, pkg.Name, target)
		if v.Present {
			inst, err := target.Installed(ctx, pkg)
			if err == nil && inst != nil {
				copied := *inst
				installed = &copied
			}
		}
	} else {
		v = model.Verification{Provider: to, Method: "unavailable"}
		v.Note = "the provider is not available on this machine"
	}

	canRemoveOld := false
	if from != "" {
		if old, ok := e.reg.Get(from); ok && old.Capabilities().Remove && from != system.Name {
			canRemoveOld = true
		}
	}

	t := model.DeriveTransition(pkg,
		e.providerRef(available, from), e.providerRef(available, to),
		previous, installed, v, canRemoveOld)
	if t.Phase == model.TransitionVerified {
		t.Recoverable = true
	}
	return t
}

// Transitions returns the provider transitions a document implies.
//
// Two situations produce one. The obvious one is a package whose owner differs
// from the configuration. The less obvious one is a package that is already
// correct but also exists through another provider: that is what an interrupted
// transition looks like once the new copy was installed, and it would otherwise
// read as "correct" while a duplicate sat on disk.
func (e *Engine) Transitions(ctx context.Context, doc *config.Document, opts Options) []model.Transition {
	plan, _, err := e.Plan(ctx, doc, opts)
	if err != nil {
		return nil
	}
	byName := map[string]model.State{}
	for _, st := range plan.States {
		byName[st.Package.Name] = st
	}
	moving := map[string]bool{}
	var out []model.Transition
	for _, st := range plan.States {
		if st.Status != model.StatusProviderDiffers {
			continue
		}
		moving[st.Package.Name] = true
		out = append(out, e.TransitionFor(ctx, st, opts))
	}
	for _, dup := range e.Duplicates(ctx, doc, opts) {
		if moving[dup.Package] {
			continue
		}
		st, ok := byName[dup.Package]
		if !ok {
			continue
		}
		extra := dup.Extra
		out = append(out, e.transitionFor(ctx, st, &extra, opts))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Package.Name < out[j].Package.Name })
	return out
}

// IncompleteTransitions returns transitions that are not in a resting state, which
// is what doctor needs to report and what the transition command resumes.
func (e *Engine) IncompleteTransitions(ctx context.Context, doc *config.Document, opts Options) []model.Transition {
	var out []model.Transition
	for _, t := range e.Transitions(ctx, doc, opts) {
		if t.NeedsWork() {
			out = append(out, t)
		}
	}
	return out
}

// ExecuteTransition performs the move, one phase at a time, and stops the moment
// the next phase is not justified.
//
// The ordering is the safety property: the incumbent copy is only removed once the
// target's copy is confirmed working. Every early exit leaves both the old command
// and the configuration intact, so the transition can simply be run again.
//
// The phases are derived rather than assumed, so calling this on an interrupted
// transition resumes it instead of starting over.
func (e *Engine) ExecuteTransition(ctx context.Context, st model.State, opts Options) (model.Transition, error) {
	t := e.TransitionFor(ctx, st, opts)
	pkg := st.Package

	// Phase 0: refuse before touching anything.
	if t.To.Available && !t.To.CanInstall && t.Phase == model.TransitionPlanned {
		t.Phase = model.TransitionBlocked
		t.Note = t.To.Name + " cannot install software"
		t.Advice = "choose a provider that can install it, or install it yourself"
		return t, nil
	}
	if !t.To.Available {
		t.Phase = model.TransitionBlocked
		t.Recoverable = true
		t.Advice = "install " + t.To.Name + " and run this again"
		return t, nil
	}

	target, _ := e.reg.Get(t.To.Name)

	// Phase 1: install into the target. Skipped when it already has the software,
	// which is what makes a resumed transition idempotent.
	if t.Phase == model.TransitionPlanned && !opts.DryRun {
		inst, err := target.Install(ctx, provider.Request{Package: pkg, Dir: opts.Dir})
		if err != nil {
			t.Phase = model.TransitionFailed
			t.Recoverable = true
			t.Advice = "the " + t.To.Name + " install failed; the " + t.From.Name + " copy was left untouched"
			return t, err
		}
		if inst != nil {
			copied := *inst
			t.Installed = &copied
		}
		// Re-derive from the machine rather than trusting the provider's return
		// value: verification is the whole point of this operation.
		t = e.TransitionFor(ctx, st, opts)
	}

	if t.Phase == model.TransitionPlanned && opts.DryRun {
		return t, nil
	}

	// Phase 2: verify. The old copy is untouchable until this passes.
	if !t.Verification.OK() {
		if t.Phase == model.TransitionCompleted || t.Phase == model.TransitionBlocked {
			return t, nil
		}
		t.Recoverable = true
		t.Advice = "the " + t.From.Name + " copy was left in place, so " + pkg.Name + " still works"
		return t, nil
	}
	if opts.DryRun {
		return t, nil
	}

	// Phase 3: remove the incumbent.
	if t.From.Name != "" {
		old, ok := e.reg.Get(t.From.Name)
		if !ok || !old.Capabilities().Remove || t.From.Name == system.Name {
			t.Phase = model.TransitionCompleted
			t.LeftBehindReason = t.From.Name + " cannot remove software"
			t.Note = t.To.Name + " now provides it; the " + t.From.Name + " copy is still there"
			t.Advice = "remove the leftover " + t.From.Name + " installation yourself if you no longer want it"
			return t, nil
		}
		if err := old.Remove(ctx, pkg); err != nil {
			t.Phase = model.TransitionFailed
			t.Recoverable = true
			t.Advice = fmt.Sprintf(
				"could not remove the %s copy; both copies are present and %s is working. Run this again to finish.",
				t.From.Name, pkg.Name)
			return t, err
		}
		t.OldRemoved = true
	}

	t = e.TransitionFor(ctx, st, opts)
	if t.OldRemoved {
		t.Phase = model.TransitionCompleted
		t.Recoverable = false
		t.Note = "ownership moved from " + t.From.Name + " to " + t.To.Name
		t.Timestamp = time.Now().Unix()
		t.Reversible = t.From.Name != "" && t.To.Name != ""
		// Record in history
		if e.history != nil {
			if err := e.history.Record(t, opts.ConfigPath); err != nil {
				t.Note = strings.TrimSpace(t.Note + "; transition completed but history could not be saved: " + err.Error())
			}
		}
	} else if t.Phase != model.TransitionCompleted && t.Phase != model.TransitionBlocked {
		t.Phase = model.TransitionVerified
		t.Recoverable = true
		t.Note = "the new installation is confirmed; the previous copy is still present"
	}
	return t, nil
}
