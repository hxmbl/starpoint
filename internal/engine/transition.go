package engine

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/system"
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
		// Not on PATH is common (a fresh ~/.local/bin, an isolated HOME) and is
		// not a failure in itself. But "the provider says so" is not evidence
		// that the command exists: uv and pip can both list a tool whose
		// executable another tool has just deleted. Something has to be on disk.
		for _, candidate := range e.executableCandidates(ctx, name, inst, p) {
			if e.locator.Exists(candidate) {
				v.Resolved = true
				v.OnDisk = candidate
				v.Method = p.Name() + " inventory and file on disk"
				v.Note = "no executable by this name is on PATH, but it exists at " + candidate
				return v
			}
		}
		v.Missing = true
		v.Note = "no executable by this name is on PATH, and " + p.Name() +
			" reported no install path that exists on disk"
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

// executableCandidates lists the places an installation's executable could be,
// in order of authority: the path the provider reported, then the provider's
// executable directory. A path another provider owns is not a candidate.
func (e *Engine) executableCandidates(ctx context.Context, name string, inst *model.Installation, p provider.Provider) []string {
	var out []string
	if inst != nil && inst.Path != "" {
		out = append(out, inst.Path)
	}
	if dp, ok := p.(provider.ExecutableDirProvider); ok {
		if dir := dp.ExecutableDir(ctx); dir != "" {
			candidate := filepath.Join(dir, name)
			if _, owner, attributed := e.locator.AttributeExecutable(candidate); !attributed ||
				owner == p.Name() || owner == system.Name {
				out = append(out, candidate)
			}
		}
	}
	return out
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
// The one exception is two providers that write the same executable path (uv
// and pip --user both use ~/.local/bin). There "install, then remove" destroys
// the command: the install overwrites the old copy's file, or refuses to, and
// the removal then deletes the new one. For that case the old copy is removed
// first and reinstalled if the new provider cannot take over, so every failure
// still ends with a working command. See handOver.
//
// The phases are derived rather than assumed, so calling this on an interrupted
// transition resumes it instead of starting over.
func (e *Engine) ExecuteTransition(ctx context.Context, st model.State, opts Options) (model.Transition, error) {
	start := e.TransitionFor(ctx, st, opts)
	if opts.DryRun {
		t, err := e.executeTransition(ctx, st, opts)
		t.Effects, t.Warnings = e.plannedTransition(ctx, start)
		model.SortEffects(t.Effects)
		t.RunAs = e.runAsFor(ctx, start.To.Name, start.From.Name)
		return t, err
	}
	target := e.providerOrNil(start.To.Name)
	old := e.providerOrNil(start.From.Name)
	obs := e.observeAll(ctx, []provider.Provider{target, old}, st.Package, start.Previous, start.Installed)
	var shared string
	if start.Phase == model.TransitionPlanned && target != nil {
		shared, _ = e.sharedExecutable(ctx, start, st.Package, target)
	}

	t, err := e.executeTransition(ctx, st, opts)

	var reported []*model.Installation
	if t.Installed != nil {
		reported = append(reported, t.Installed)
	}
	effects := obs.finish(ctx, model.EffectCreate, reported...)
	// The package-level record of the move: what each provider now holds that
	// it did not, or no longer holds that it did.
	if start.Previous != nil && t.OldRemoved && start.From.Name != "" {
		effects = append(effects, packageEffect(model.EffectDelete, start.From.Name, st.Package.Name,
			start.Previous, start.Previous.Version, "", model.EvidenceReported))
	}
	if start.Installed == nil && t.Installed != nil {
		effects = append(effects, packageEffect(model.EffectCreate, t.To.Name, st.Package.Name,
			t.Installed, "", t.Installed.Version, model.EvidenceReported))
	}
	model.SortEffects(effects)
	t.Effects = effects
	if shared != "" {
		t.Warnings = append(t.Warnings, sharedWarning(start, shared))
	}
	t.Warnings = append(t.Warnings, effectWarnings(st.Package.Name, effects)...)
	t.RunAs = e.runAsFor(ctx, start.To.Name, start.From.Name)
	return t, err
}

// runAsFor reports the accounts the named providers' commands run as, in
// order and without repeats, when the engine was told.
func (e *Engine) runAsFor(ctx context.Context, names ...string) []model.RunAs {
	if e.runAs == nil {
		return nil
	}
	var out []model.RunAs
	seen := map[string]bool{}
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if p, ok := e.reg.Get(name); ok {
			out = append(out, e.runAs(ctx, p))
		}
	}
	return out
}

// executeTransition is ExecuteTransition without the observation around it.
func (e *Engine) executeTransition(ctx context.Context, st model.State, opts Options) (model.Transition, error) {
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

	// Two providers that would write the same executable cannot both hold the
	// software at once, so the usual order does not apply.
	if t.Phase == model.TransitionPlanned {
		if shared, ok := e.sharedExecutable(ctx, t, pkg, target); ok {
			if opts.DryRun {
				t.Note = fmt.Sprintf("%s and %s both install %s; the %s copy will be removed first and reinstalled if %s cannot take over",
					t.From.Name, t.To.Name, shared, t.From.Name, t.To.Name)
				return t, nil
			}
			return e.handOver(ctx, st, t, shared, opts)
		}
	}

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
	removed := false
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
		removed = true
	}

	from := t.From.Name
	t = e.TransitionFor(ctx, st, opts)

	// Phase 4: verify again. Removing the old copy can take the new copy's
	// executable with it when an undetected path is shared, and reporting
	// "completed" over a missing command is the failure this guards against.
	repaired := false
	if removed && !t.Verification.OK() {
		var err error
		t, err = e.repairTarget(ctx, st, target, opts)
		if err != nil || !t.Verification.OK() {
			t.Phase = model.TransitionFailed
			t.OldRemoved = true
			t.Recoverable = true
			t.Note = fmt.Sprintf("the %s copy was removed, but the %s copy no longer works: %s",
				from, t.To.Name, t.Verification.Reason())
			t.Advice = fmt.Sprintf("reinstall %s with 'starpoint sync' or with %s directly", pkg.Name, t.To.Name)
			if err == nil {
				err = fmt.Errorf("%s: %s", pkg.Name, t.Note)
			}
			return t, err
		}
		repaired = true
	}

	if t.OldRemoved {
		t = e.complete(t, opts)
		if repaired {
			t.Note += "; " + t.To.Name + " was reinstalled because removing the " + from + " copy deleted its executable"
		}
	} else if t.Phase != model.TransitionCompleted && t.Phase != model.TransitionBlocked {
		t.Phase = model.TransitionVerified
		t.Recoverable = true
		t.Note = "the new installation is confirmed; the previous copy is still present"
	}
	return t, nil
}

// complete marks a transition finished and records it for later reversal.
func (e *Engine) complete(t model.Transition, opts Options) model.Transition {
	t.Phase = model.TransitionCompleted
	t.OldRemoved = true
	t.Recoverable = false
	t.Note = "ownership moved from " + t.From.Name + " to " + t.To.Name
	t.Timestamp = time.Now().Unix()
	t.Reversible = t.From.Name != "" && t.To.Name != ""
	if e.history != nil {
		if err := e.history.Record(t, opts.ConfigPath); err != nil {
			t.Note = strings.TrimSpace(t.Note + "; transition completed but history could not be saved: " + err.Error())
		}
	}
	return t
}

// sharedExecutable reports whether the target provider would write its
// executable to a path the incumbent already occupies.
//
// It looks for the file the target would create in the target's executable
// directory. Either the incumbent reported that exact path, or something is
// already there; in both cases installing first would collide with it.
func (e *Engine) sharedExecutable(ctx context.Context, t model.Transition, pkg model.Package, target provider.Provider) (string, bool) {
	if e.locator == nil || t.From.Name == "" || t.Previous == nil || !executableShaped(pkg.Name) {
		return "", false
	}
	if t.From.Name == system.Name || !t.From.Available {
		return "", false
	}
	old, ok := e.reg.Get(t.From.Name)
	if !ok || !old.Capabilities().Remove {
		return "", false
	}
	dp, ok := target.(provider.ExecutableDirProvider)
	if !ok {
		return "", false
	}
	dir := dp.ExecutableDir(ctx)
	if dir == "" {
		return "", false
	}
	candidate := filepath.Join(dir, pkg.Name)
	prev := t.Previous.Path
	if prev != "" && (filepath.Clean(prev) == candidate || samePath(prev, candidate)) {
		return candidate, true
	}
	if !e.locator.Exists(candidate) {
		return "", false
	}
	return candidate, e.occupiedByIncumbent(candidate, t.From.Name, t.To.Name, prev)
}

// occupiedByIncumbent decides whether the file already at the path the target
// would write belongs to the provider being moved away from.
//
// Only then does removing the old copy first make room. A file another
// provider owns would still be in the way after the incumbent is gone, so
// handing over would delete the user's working copy for nothing; the install
// is left to collide (or overwrite, which the plan marks destructive) instead.
// With cargo's and go's bin directories watched, that distinction matters:
// ~/.cargo/bin holds rustup's proxies and GOBIN is often shared with
// ~/.local/bin.
func (e *Engine) occupiedByIncumbent(candidate, from, to, prev string) bool {
	_, owner, attributed := e.locator.AttributeExecutable(candidate)
	switch {
	case attributed && owner == from:
		return true
	case attributed && owner != to && owner != system.Name:
		// A third provider's file.
		return false
	case prev != "":
		// The incumbent says its copy lives somewhere else, so whatever is at
		// the candidate is not it.
		return false
	default:
		// Nothing says whose it is, and the incumbent cannot say where its own
		// copy is. Assume the worst: treating it as shared removes the old
		// copy first and restores it if the new install fails.
		return true
	}
}

// handOver moves software between two providers that write the same
// executable path.
//
// The old copy is removed first, because neither order of "install, remove"
// survives a shared path. That opens a window with no command, so every failure
// after it reinstalls the old copy (at its previous version when the provider
// can pin) before returning: the user ends up either with the new copy verified
// on disk, or with the old one back.
func (e *Engine) handOver(ctx context.Context, st model.State, t model.Transition, shared string, opts Options) (model.Transition, error) {
	pkg := st.Package
	from, to := t.From.Name, t.To.Name
	old, _ := e.reg.Get(from)
	target, _ := e.reg.Get(to)
	previous := t.Previous

	if err := old.Remove(ctx, pkg); err != nil {
		t.Phase = model.TransitionFailed
		t.Recoverable = true
		t.Advice = fmt.Sprintf("could not remove the %s copy, which %s has to replace at %s; nothing was changed", from, to, shared)
		return t, err
	}

	restore := func(cause error) (model.Transition, error) {
		rollbackErr := e.reinstall(ctx, old, pkg, previous, opts)
		r := e.TransitionFor(ctx, st, opts)
		r.Phase = model.TransitionFailed
		r.OldRemoved = false
		r.Recoverable = true
		if rollbackErr != nil {
			r.OldRemoved = true
			r.Recoverable = false
			r.Advice = fmt.Sprintf("%s could not take over %s and reinstalling the %s copy also failed (%v); reinstall %s with %s",
				to, shared, from, rollbackErr, pkg.Name, from)
			return r, errors.Join(cause, fmt.Errorf("restoring the %s copy of %s: %w", from, pkg.Name, rollbackErr))
		}
		r.Note = fmt.Sprintf("%s and %s both install %s; the %s copy was restored", from, to, shared, from)
		r.Advice = fmt.Sprintf("%s could not take over %s, so the %s copy was reinstalled and nothing else changed", to, shared, from)
		return r, cause
	}

	if _, err := target.Install(ctx, provider.Request{Package: pkg, Dir: opts.Dir}); err != nil {
		return restore(err)
	}
	after := e.TransitionFor(ctx, st, opts)
	if !after.Verification.OK() {
		// The new copy is not usable. Take it out again so the old copy can
		// have its path back.
		_ = target.Remove(ctx, pkg)
		return restore(fmt.Errorf("%s installed %s but it could not be verified: %s",
			to, pkg.Name, after.Verification.Reason()))
	}
	after = e.complete(after, opts)
	after.Note += fmt.Sprintf("; both install %s, so the %s copy was removed first", shared, from)
	return after, nil
}

// reinstall puts a previous installation back, pinned to the version it had
// when the provider can honour a pin.
func (e *Engine) reinstall(ctx context.Context, p provider.Provider, pkg model.Package, previous *model.Installation, opts Options) error {
	restore := pkg
	restore.Provider = p.Name()
	restore.Version = ""
	if previous != nil && previous.Version != "" && p.Capabilities().PinVersion {
		restore.Version = previous.Version
	}
	_, err := p.Install(ctx, provider.Request{Package: restore, Dir: opts.Dir})
	return err
}

// repairTarget reinstalls the target's copy from scratch. A plain install is
// not enough: a package manager that still lists the package considers it
// installed and will not recreate an executable something else deleted.
func (e *Engine) repairTarget(ctx context.Context, st model.State, target provider.Provider, opts Options) (model.Transition, error) {
	pkg := st.Package
	if err := target.Remove(ctx, pkg); err != nil {
		return e.TransitionFor(ctx, st, opts), err
	}
	if _, err := target.Install(ctx, provider.Request{Package: pkg, Dir: opts.Dir}); err != nil {
		return e.TransitionFor(ctx, st, opts), err
	}
	return e.TransitionFor(ctx, st, opts), nil
}
