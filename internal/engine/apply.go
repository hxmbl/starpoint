package engine

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/system"
)

// Apply carries out a plan.
//
// Ordering is deliberate. Ownership decisions are recorded before the machine is
// touched, so an interrupted run still leaves the configuration describing the
// user's intent. Installs happen before removals, so a relocation never leaves
// the machine with nothing installed in between. A failure in one package does
// not stop the others: partial progress is real progress, and the caller is told
// exactly what failed.
func (e *Engine) Apply(ctx context.Context, doc *config.Document, plan model.Plan, opts Options) (*Result, error) {
	res := &Result{
		States:        plan.States,
		Warnings:      plan.Warnings,
		DryRun:        opts.DryRun,
		Applied:       []model.PlannedAction{},
		ConfigChanged: false,
	}
	if opts.DryRun {
		res.Applied = plan.Actions
		return res, nil
	}

	// The caller's plan stays the plan; what happened is recorded on a copy.
	plan.Actions = append([]model.PlannedAction(nil), plan.Actions...)
	var failures []string

	// Phase 1: persist provider decisions for everything we are about to install,
	// so ownership survives an interruption.
	decided := false
	for _, action := range plan.Actions {
		if action.ConfigChange == nil {
			continue
		}
		if err := applyEdit(doc, action.ConfigChange); err != nil {
			return res, err
		}
		decided = true
	}
	if decided {
		if err := doc.Save(); err != nil {
			return res, fmt.Errorf("saving provider decisions: %w", err)
		}
		res.ConfigChanged = true
		res.ConfigPath = doc.Global.Path
		for i := range plan.Actions {
			if plan.Actions[i].ConfigChange != nil {
				plan.Actions[i] = appliedConfig(plan.Actions[i])
			}
		}
	}

	// Phase 2: installs.
	for _, action := range plan.Actions {
		if action.Action != model.ActionInstall {
			continue
		}
		p, ok := e.reg.Get(action.State.Res.Provider)
		if !ok {
			err := fmt.Errorf("provider %q is unknown", action.State.Res.Provider)
			res.Applied = append(res.Applied, failed(action, err))
			failures = append(failures, fmt.Sprintf("%s: %s", action.State.Package.Name, err))
			continue
		}
		obs := e.observe(ctx, p, action.State.Package, action.State.Current)
		inst, err := p.Install(ctx, provider.Request{
			Package: action.State.Package,
			Scope:   action.State.Package.Scope,
			Dir:     opts.Dir,
		})
		observed := obs.finish(ctx, model.EffectCreate, inst)
		if err != nil {
			action = finishEffects(failed(annotate(action, err.Error()), err), observed)
			res.Applied = append(res.Applied, action)
			res.Warnings = append(res.Warnings, action.Warnings...)
			failures = append(failures, fmt.Sprintf("%s: %s", action.State.Package.Name, err.Error()))
			continue
		}
		observed = append(observed, packageEffect(model.EffectCreate, p.Name(), action.State.Package.Name,
			inst, "", versionOf(inst), model.EvidenceReported))
		action = finishEffects(annotate(action, installPath(inst)), observed)
		action.Outcome = model.OutcomeApplied
		if warning := verifyInstalled(ctx, p, action.State.Package); warning != nil {
			action.Warnings = append(action.Warnings, *warning)
		}
		res.Applied = append(res.Applied, action)
		res.Warnings = append(res.Warnings, action.Warnings...)
		// Record execution in tracking system
		if e.tracker != nil {
			_ = e.tracker.Record(action.State.Package.Name, opts.Dir, time.Now())
		}
	}

	// Phase 3: relocations. Install the wanted copy first, verify, then remove
	// the copy the configuration no longer wants.
	for _, action := range plan.Actions {
		if action.Action != model.ActionRelocate {
			continue
		}
		// A transition installs, verifies, and only then removes the incumbent,
		// stopping at the first phase that is not justified. Its record replaces
		// the flat note so the caller can see the phase and the evidence.
		t, err := e.ExecuteTransition(ctx, action.State, opts)
		action.Transition = &t
		switch {
		case t.Note != "":
			action.Note = t.Note
		case err != nil:
			action.Note = err.Error()
		}
		if t.Advice != "" {
			action.Note = strings.TrimSpace(action.Note + "; " + t.Advice)
		}
		kept := []model.Effect{}
		for _, eff := range action.Effects {
			if eff.Kind == model.EffectConfig {
				kept = append(kept, eff)
			}
		}
		action.Effects = model.MergeEffects(kept, t.Effects...)
		model.SortEffects(action.Effects)
		action.Changes = []model.ActionTransition{providerChange(t)}
		action.Warnings = append(action.Warnings, t.Warnings...)
		action.Warnings = append(action.Warnings, transitionWarnings(t)...)
		res.Warnings = append(res.Warnings, action.Warnings...)
		if err != nil || t.Phase == model.TransitionFailed {
			action = failed(action, err)
		} else {
			action.Outcome = model.OutcomeApplied
		}
		res.Applied = append(res.Applied, action)
		res.Transitions = append(res.Transitions, t)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", action.State.Package.Name, err.Error()))
		}
	}

	// Phase 4: version reconciliation.
	for _, action := range plan.Actions {
		if action.Action != model.ActionUpdate {
			continue
		}
		p, ok := e.reg.Get(action.State.Res.Provider)
		if !ok {
			err := fmt.Errorf("provider %q is unknown", action.State.Res.Provider)
			res.Applied = append(res.Applied, failed(action, err))
			failures = append(failures, fmt.Sprintf("%s: %s", action.State.Package.Name, err))
			continue
		}
		req := provider.Request{Package: action.State.Package, Dir: opts.Dir}
		obs := e.observe(ctx, p, action.State.Package, action.State.Desired)
		var (
			inst *model.Installation
			err  error
		)
		if p.Capabilities().Update {
			inst, err = p.Update(ctx, action.State.Package)
		} else {
			// A provider that cannot update in place still honours a pin by
			// installing that exact version.
			inst, err = p.Install(ctx, req)
		}
		observed := obs.finish(ctx, model.EffectModify, inst)
		if err != nil {
			action = finishEffects(failed(annotate(action, err.Error()), err), observed)
			res.Applied = append(res.Applied, action)
			res.Warnings = append(res.Warnings, action.Warnings...)
			failures = append(failures, fmt.Sprintf("%s: %s", action.State.Package.Name, err.Error()))
			continue
		}
		from := versionOf(action.State.Desired)
		observed = append(observed, packageEffect(model.EffectModify, p.Name(), action.State.Package.Name,
			inst, from, versionOf(inst), model.EvidenceReported))
		action = finishEffects(annotate(action, installPath(inst)), observed)
		action.Changes = []model.ActionTransition{{Kind: model.ChangeVersion, From: from, To: versionOf(inst)}}
		action.Outcome = model.OutcomeApplied
		if warning := verifyInstalled(ctx, p, action.State.Package); warning != nil {
			action.Warnings = append(action.Warnings, *warning)
		}
		res.Applied = append(res.Applied, action)
		res.Warnings = append(res.Warnings, action.Warnings...)
		// Record execution in tracking system
		if e.tracker != nil {
			_ = e.tracker.Record(action.State.Package.Name, opts.Dir, time.Now())
		}
	}

	// Phase 5: conflicts are reported, never resolved by editing the source of
	// truth.
	// A record with a configuration change wrote that change in phase 1.
	for _, action := range plan.Actions {
		if action.Action == model.ActionRecord {
			action.Outcome = model.OutcomeReported
			if action.ConfigChange != nil {
				action.Outcome = model.OutcomeApplied
			}
			res.Applied = append(res.Applied, action)
		}
	}

	if len(failures) > 0 {
		return res, fmt.Errorf("some operations did not complete:\n  %s", strings.Join(failures, "\n  "))
	}
	return res, nil
}

// verifyInstalled runs an optional provider executable check after a successful
// install or update. Verification failures are warnings because the provider's
// own inventory and install operation already succeeded; unsupported package
// identifiers are simply left to inventory and PATH checks.
func verifyInstalled(ctx context.Context, p provider.Provider, pkg model.Package) *model.Warning {
	if p == nil || !p.Capabilities().Verify {
		return nil
	}
	verifier, ok := p.(provider.Verifier)
	if !ok {
		return nil
	}
	if err := verifier.Verify(ctx, pkg); err != nil {
		if pe, ok := provider.AsProviderError(err); ok && pe.Kind == provider.ErrUnsupported {
			return nil
		}
		return &model.Warning{
			Severity: model.SeverityWarn,
			Code:     model.WarnVerificationFailed,
			Package:  pkg.Name,
			Provider: p.Name(),
			Message:  "provider installed the package but executable verification failed: " + err.Error(),
		}
	}
	return nil
}

// applyEdit mutates a document to match a planned configuration edit.
func applyEdit(doc *config.Document, edit *model.ConfigEdit) error {
	if edit == nil {
		return nil
	}
	if edit.Remove != "" {
		removeEntry(&doc.Global, edit.Remove)
		for i := range doc.Projects {
			removeEntry(&doc.Projects[i], edit.Remove)
		}
		return nil
	}
	if edit.Upsert == nil {
		return nil
	}
	f, _ := doc.Find(edit.Upsert.Name)
	if f.Path == "" || f.Path == edit.File {
		f = doc.Global
	}
	if f.Path != edit.File && edit.File != "" {
		// The entry belongs to a project file; find it again.
		for i := range doc.Projects {
			if doc.Projects[i].Path == edit.File {
				f = doc.Projects[i]
			}
		}
	}
	if contains(f.Raw.Packages, edit.Upsert.Name) {
		replaceEntry(&f.Raw.Packages, *edit.Upsert)
	} else {
		f.Raw.Packages = append(f.Raw.Packages, *edit.Upsert)
	}
	if f.Path == doc.Global.Path {
		doc.Global = f
	} else {
		for i := range doc.Projects {
			if doc.Projects[i].Path == f.Path {
				doc.Projects[i] = f
			}
		}
	}
	return nil
}

func annotate(action model.PlannedAction, note string) model.PlannedAction {
	note = strings.TrimSpace(note)
	if note == "" {
		return action
	}
	if action.Note == "" {
		action.Note = note
	} else if !strings.Contains(action.Note, note) {
		action.Note = action.Note + "; " + note
	}
	return action
}

func installPath(i *model.Installation) string {
	if i == nil {
		return ""
	}
	if i.Path == "" {
		return i.Version
	}
	return i.Path
}

// Remove uninstalls a package and deletes it from the configuration.
func (e *Engine) Remove(ctx context.Context, doc *config.Document, name string, opts Options) (*Result, error) {
	d, err := e.describeRemove(ctx, doc, name)
	if err != nil {
		return nil, err
	}
	action := d.action
	res := &Result{DryRun: opts.DryRun}

	if !opts.DryRun {
		obs := e.observe(ctx, d.provider, d.pkg, d.installed)
		err := d.provider.Remove(ctx, d.pkg)
		observed := obs.finish(ctx, model.EffectDelete)
		if err != nil {
			// The configuration is left alone, but a removal that failed part-way
			// can still have deleted files, and those are reported.
			action = finishEffects(failed(action, err), observed)
			action.ConfigChanges = nil
			action.Effects = withoutConfig(action.Effects)
			res.Applied = []model.PlannedAction{action}
			res.Warnings = action.Warnings
			return res, err
		}
		from := versionOf(d.installed)
		observed = append(observed, packageEffect(model.EffectDelete, d.provider.Name(), d.pkg.Name,
			d.installed, from, "", model.EvidenceReported))
		action = finishEffects(action, observed)
		action.Outcome = model.OutcomeApplied
	}
	res.Warnings = action.Warnings

	f, found := doc.Find(name)
	if !found {
		res.Applied = []model.PlannedAction{action}
		return res, nil
	}
	removeEntry(&doc.Global, name)
	for i := range doc.Projects {
		if doc.Projects[i].Path == f.Path {
			removeEntry(&doc.Projects[i], name)
		}
	}
	if !opts.DryRun {
		if err := doc.Save(); err != nil {
			res.Applied = []model.PlannedAction{action}
			return res, fmt.Errorf("updating configuration: %w", err)
		}
		action = appliedConfig(action)
	}
	res.Applied = []model.PlannedAction{action}
	res.ConfigChanged = true
	res.ConfigPath = f.Path
	return res, nil
}

// removeDescription is what Remove will do, worked out before it does it.
type removeDescription struct {
	action    model.PlannedAction
	pkg       model.Package
	provider  provider.Provider
	installed *model.Installation
}

// RemovePreview describes what Remove would do, without uninstalling anything
// or touching the configuration.
func (e *Engine) RemovePreview(ctx context.Context, doc *config.Document, name string) (model.PlannedAction, error) {
	d, err := e.describeRemove(ctx, doc, name)
	if err != nil {
		return model.PlannedAction{}, err
	}
	return d.action, nil
}

// describeRemove checks a removal is allowed and describes it: the package,
// every file Starpoint can see that would go with it, and the configuration
// entry that would be dropped.
func (e *Engine) describeRemove(ctx context.Context, doc *config.Document, name string) (*removeDescription, error) {
	pkg, ok := doc.Package(name)
	if !ok {
		return nil, fmt.Errorf("%s is not in the configuration; nothing to remove", name)
	}
	available := e.health(ctx)

	provName := pkg.Provider
	if provName == "" {
		if existing := e.findExisting(ctx, pkg, available); existing != nil {
			provName = existing.Provider
		}
	}
	if provName == "" {
		return nil, fmt.Errorf("%s has no recorded provider and Starpoint cannot find it on this machine; set provider in the configuration first", name)
	}
	p, ok := e.reg.Get(provName)
	if !ok {
		return nil, fmt.Errorf("provider %q is not known to this build", provName)
	}
	if !available[provName].Available {
		return nil, fmt.Errorf("provider %q is not available right now (%s), so %s cannot be removed",
			provName, available[provName].Reason, name)
	}
	if provName == system.Name {
		return nil, fmt.Errorf("%s is provided by the operating system; Starpoint will not remove operating-system software", name)
	}
	if !p.Capabilities().Remove {
		return nil, fmt.Errorf("provider %q cannot remove packages", provName)
	}

	installed, _ := e.providerInstallation(ctx, p, pkg)
	installed = e.withPath(installed)
	state := model.State{Package: pkg, Res: model.Resolution{Provider: provName}, Current: installed}
	action := model.PlannedAction{Action: model.ActionRemove, State: state, Outcome: model.OutcomePlanned}
	action.Effects = e.plannedRemove(ctx, p, pkg, installed)
	if installed == nil {
		action.Warnings = append(action.Warnings, model.Warning{
			Severity: model.SeverityInfo, Code: model.WarnNotInstalled, Package: name, Provider: provName,
			Message: provName + " does not report " + name + " as installed; only the configuration entry is removed",
		})
	}
	if f, found := doc.Find(name); found {
		if cc := model.DiffEntry(f.Path, entryIn(f, name), nil); cc != nil {
			action.ConfigChanges = append(action.ConfigChanges, *cc)
			action.Effects = append(action.Effects, configEffect(*cc, model.EvidencePlanned))
		}
	}
	model.SortEffects(action.Effects)
	return &removeDescription{action: action, pkg: pkg, provider: p, installed: installed}, nil
}

// removeEntry deletes a package from a raw configuration file.
func removeEntry(f *config.File, name string) {
	out := f.Raw.Packages[:0]
	for _, p := range f.Raw.Packages {
		if p.Name != name {
			out = append(out, p)
		}
	}
	f.Raw.Packages = out
}

// Add installs a package and records it, plus the resolved provider, in the
// configuration.
//
// The configuration is written even when the package was already installed,
// because the ownership decision is the durable artifact. If the provider is
// forced but its tooling is missing, the decision is recorded anyway: the
// configuration is the source of truth, and `str sync` will converge once
// the provider exists. With AdoptOnly, nothing is installed at all.
func (e *Engine) Add(ctx context.Context, doc *config.Document, pkg model.Package, opts Options) (*Result, error) {
	d, err := e.describeAdd(ctx, doc, pkg, opts)
	if err != nil {
		return nil, err
	}
	action := d.action
	res := &Result{DryRun: opts.DryRun, Warnings: append([]model.Warning(nil), action.Warnings...)}

	if d.install && !opts.DryRun {
		obs := e.observe(ctx, d.provider, d.resolved, d.existing)
		inst, err := d.provider.Install(ctx, provider.Request{Package: d.resolved, Dir: opts.Dir})
		observed := obs.finish(ctx, model.EffectCreate, inst)
		if err != nil {
			// Nothing was recorded, but whatever the failed install left behind
			// is still reported: a half-finished install is exactly what a user
			// needs to see.
			action = finishEffects(failed(action, err), observed)
			action.ConfigChanges = nil
			action.Effects = withoutConfig(action.Effects)
			res.Applied = []model.PlannedAction{action}
			res.States = []model.State{action.State}
			res.Warnings = action.Warnings
			return res, err
		}
		observed = append(observed, packageEffect(model.EffectCreate, d.provider.Name(), d.resolved.Name,
			inst, "", versionOf(inst), model.EvidenceReported))
		action = finishEffects(action, observed)
		action.State.Desired = inst
		action.State.Status = model.StatusCorrect
		res.Warnings = action.Warnings
		// Record execution in tracking system
		if e.tracker != nil {
			_ = e.tracker.Record(pkg.Name, opts.Dir, time.Now())
		}
	}

	target := d.target
	if contains(target.Raw.Packages, pkg.Name) {
		replaceEntry(&target.Raw.Packages, d.resolved)
	} else {
		target.Raw.Packages = append(target.Raw.Packages, d.resolved)
	}
	storeFile(doc, target)

	if !opts.DryRun {
		if err := doc.Save(); err != nil {
			return nil, fmt.Errorf("recording %s in %s: %w", pkg.Name, target.Path, err)
		}
		action = appliedConfig(action)
		if action.Outcome != model.OutcomeFailed {
			action.Outcome = model.OutcomeApplied
		}
	}
	res.States = []model.State{action.State}
	res.Applied = []model.PlannedAction{action}
	res.ConfigChanged = true
	res.ConfigPath = target.Path
	return res, nil
}

// addDescription is what Add will do, worked out before it does any of it.
type addDescription struct {
	action   model.PlannedAction
	resolved model.Package
	provider provider.Provider
	existing *model.Installation
	target   config.File
	// install is true when the provider will be asked to install.
	install bool
}

// AddPreview describes what Add would do, without installing anything or
// touching the configuration. The confirmation step shows it, so the question
// and the change come from one derivation.
func (e *Engine) AddPreview(ctx context.Context, doc *config.Document, pkg model.Package, opts Options) (model.PlannedAction, error) {
	d, err := e.describeAdd(ctx, doc, pkg, opts)
	if err != nil {
		return model.PlannedAction{}, err
	}
	return d.action, nil
}

// describeAdd resolves a package and describes recording it: the action, the
// effects it is expected to have, the configuration edit, and any warning.
func (e *Engine) describeAdd(ctx context.Context, doc *config.Document, pkg model.Package, opts Options) (*addDescription, error) {
	if pkg.Name == "" {
		return nil, fmt.Errorf("a package name is required")
	}
	plan, err := e.plan(ctx, pkg, opts)
	if err != nil {
		return nil, err
	}
	available := plan.available
	resolution := plan.resolution
	resolved := plan.resolved
	resolved.Provider = resolution.Provider
	// A reason supplied by the caller (an interactive choice, for example) is a
	// human decision and outranks the resolver's summary.
	if resolved.Reason == "" {
		resolved.Reason = resolution.Reason
	}

	state := model.State{Package: resolved, Res: resolution, Status: model.StatusCorrect}
	// adopted records that the software was already there. Recording ownership
	// is not the same act as installing, and the output should not claim it was.
	adopted := plan.adopted
	p, known := plan.provider, plan.known
	state.Desired = plan.desired
	if plan.existing != nil && (state.Desired == nil || plan.existing.Provider != state.Desired.Provider) {
		state.Current = plan.existing
	}

	d := &addDescription{resolved: resolved, provider: p, existing: plan.existing}
	var warnings []model.Warning
	var effects []model.Effect
	if state.Desired == nil {
		switch {
		case !known:
			return nil, fmt.Errorf("provider %q is not known to this build", resolution.Provider)
		case !available[resolution.Provider].Available:
			// Record the intent, report the blocker.
			warnings = append(warnings, model.Warning{
				Severity: model.SeverityError,
				Code:     model.WarnRecordedNotInstalled,
				Package:  pkg.Name,
				Provider: resolution.Provider,
				Message:  "recorded in the configuration but not installed: " + available[resolution.Provider].Reason,
			})
			state.Status = model.StatusMissing
		case !p.Capabilities().Install:
			return nil, fmt.Errorf("provider %q cannot install packages", resolution.Provider)
		case opts.AdoptOnly && (plan.existing == nil || plan.existing.Provider != resolution.Provider):
			// Adopting records what is already there. Software the chosen
			// provider does not have, and that is not attributed to it on
			// PATH either, is not there to adopt: recording it anyway would
			// put an entry in the source of truth that the next sync turns
			// into an install nobody asked for.
			where := "not installed"
			if plan.existing != nil {
				where = "installed through " + plan.existing.Provider + ", not " + resolution.Provider
			}
			return nil, &provider.Error{Provider: resolution.Provider, Op: "adopt", Kind: provider.ErrNotFound,
				Message: pkg.Name + " is " + where + "; adopting only records software that is already there, so nothing was recorded"}
		case opts.AdoptOnly:
			warnings = append(warnings, model.Warning{
				Severity: model.SeverityWarn,
				Code:     model.WarnRecordedNotInstalled,
				Package:  pkg.Name,
				Provider: resolution.Provider,
				Message: "recorded in the configuration but not installed: " +
					adoptionReason(plan.existing, opts),
			})
		default:
			state.Status = model.StatusMissing
			d.install = true
			planned, plannedWarnings := e.plannedInstall(ctx, p, resolved)
			effects = append(effects, planned...)
			warnings = append(warnings, plannedWarnings...)
		}
	}

	verb := model.ActionInstall
	note := "provider " + resolution.Provider
	switch {
	case opts.AdoptOnly:
		verb = model.ActionAdopt
		note = "recorded ownership with " + resolution.Provider + " without installing"
	case adopted:
		verb = model.ActionAdopt
		note = "already installed; recorded ownership with " + resolution.Provider
	}

	target := doc.Global
	if opts.Dir != "" {
		if f, ok := doc.ProjectFileFor(opts.Dir); ok {
			target = f
		}
	}
	d.target = target

	action := model.PlannedAction{
		Action:   verb,
		State:    state,
		Note:     note,
		Outcome:  model.OutcomePlanned,
		Warnings: warnings,
	}
	before := entryIn(target, pkg.Name)
	if cc := model.DiffEntry(target.Path, before, &resolved); cc != nil {
		cc.CreatesFile = !target.Exists
		action.ConfigChanges = append(action.ConfigChanges, *cc)
		effects = append(effects, configEffect(*cc, model.EvidencePlanned))
	}
	if before != nil && before.Provider != "" && before.Provider != resolved.Provider {
		action.Changes = append(action.Changes, model.ActionTransition{Kind: model.ChangeProvider, From: before.Provider, To: resolved.Provider})
	}
	if before != nil && before.Version != resolved.Version && (before.Version != "" || resolved.Version != "") {
		action.Changes = append(action.Changes, model.ActionTransition{Kind: model.ChangeVersion, From: before.Version, To: resolved.Version})
	}
	model.SortEffects(effects)
	action.Effects = effects
	d.action = action
	return d, nil
}

// withoutConfig drops configuration effects, for an action whose
// configuration edit never happened.
func withoutConfig(effects []model.Effect) []model.Effect {
	out := effects[:0:0]
	for _, eff := range effects {
		if eff.Kind != model.EffectConfig {
			out = append(out, eff)
		}
	}
	return out
}

// adoptionReason explains why adoption-only found nothing to adopt.
func adoptionReason(existing *model.Installation, opts Options) string {
	if existing != nil && existing.Path != "" {
		return "present at " + existing.Path + " but not tracked by the chosen provider"
	}
	return "not found on PATH and adopt-only mode forbids installing it"
}

func contains(pkgs []model.Package, name string) bool {
	for _, p := range pkgs {
		if p.Name == name {
			return true
		}
	}
	return false
}

func replaceEntry(pkgs *[]model.Package, pkg model.Package) {
	for i := range *pkgs {
		if (*pkgs)[i].Name == pkg.Name {
			(*pkgs)[i] = pkg
			return
		}
	}
	*pkgs = append(*pkgs, pkg)
}

// storeFile writes a file value back into the document it came from.
func storeFile(doc *config.Document, f config.File) {
	if f.Path == doc.Global.Path {
		doc.Global = f
		return
	}
	for i := range doc.Projects {
		if doc.Projects[i].Path == f.Path {
			doc.Projects[i] = f
		}
	}
}

// Update upgrades Starpoint-managed software.
//
// A version pinned in the configuration is never moved. When a provider would
// only offer a version that contradicts the pin, that is reported as a conflict;
// the configuration is not edited to make the problem disappear.
func (e *Engine) Update(ctx context.Context, doc *config.Document, opts Options) (*Result, error) {
	available := e.health(ctx)
	res := &Result{DryRun: opts.DryRun}

	var (
		unchanged []model.Warning
		failures  []string
	)
	skip := func(code string, pkg model.Package, providerName, msg string) {
		unchanged = append(unchanged, model.Warning{
			Severity: model.SeverityInfo, Code: code, Package: pkg.Name, Provider: providerName, Message: msg,
		})
	}
	for _, pkg := range doc.Packages() {
		resolution := e.resolver.Resolve(e.resolveInput(pkg, e.findExisting(ctx, pkg, available), available, opts))
		state := model.State{
			Package: pkg,
			Res:     resolution,
			Desired: nil,
			Status:  model.StatusCorrect,
		}
		res.States = append(res.States, state)

		p, ok := e.reg.Get(resolution.Provider)
		if !ok || !available[resolution.Provider].Available {
			continue
		}
		if pkg.Version != "" {
			inst, err := p.Installed(ctx, pkg)
			if err == nil && inst != nil && !model.VersionMatches(pkg.Version, inst.Version) {
				failures = append(failures, fmt.Sprintf(
					"%s is pinned to %s but %s has %s; the configuration was not changed",
					pkg.Name, pkg.Version, resolution.Provider, shortOr(inst.Version, "an unknown version")))
				continue
			}
			obs := e.observe(ctx, p, pkg, inst)
			conflict, note, err := e.pinConflict(ctx, doc, p, pkg, resolution.Provider, opts)
			switch {
			case err != nil:
				failures = append(failures, fmt.Sprintf("%s: %s", pkg.Name, err.Error()))
			case conflict == nil:
				skip(model.WarnPinKept, pkg, resolution.Provider, note)
			case conflict.Resolution == "pending" && opts.UpdatePinned[pkg.Name]:
				// A preview of a pin the user agreed to move: list the update
				// it turns into, with the configuration edit, so the caller
				// confirms (and guards) what will actually run.
				res.PinConflicts = append(res.PinConflicts, *conflict)
				res.Applied = append(res.Applied, e.plannedPinMove(doc, state, inst, pkg, conflict))
			default:
				res.PinConflicts = append(res.PinConflicts, *conflict)
				if conflict.Resolution == "updated" {
					res.ConfigChanged = true
					res.ConfigPath = conflict.ConfigPath
					action := model.PlannedAction{
						Action:  model.ActionUpdate,
						State:   state,
						Outcome: model.OutcomeApplied,
						Note: fmt.Sprintf("%s -> %s; pin rewritten in %s",
							pkg.Version, conflict.Available, conflict.ConfigPath),
						Changes: []model.ActionTransition{{Kind: model.ChangeVersion, From: pkg.Version, To: conflict.Available}},
					}
					observed := obs.finish(ctx, model.EffectModify)
					observed = append(observed, packageEffect(model.EffectModify, resolution.Provider, pkg.Name,
						inst, pkg.Version, conflict.Available, model.EvidenceReported))
					after := pkg
					after.Version = conflict.Available
					if cc := model.DiffEntry(conflict.ConfigPath, &pkg, &after); cc != nil {
						cc.Applied = true
						action.ConfigChanges = append(action.ConfigChanges, *cc)
						observed = append(observed, configEffect(*cc, model.EvidenceObserved))
					}
					res.Applied = append(res.Applied, finishEffects(action, observed))
				} else {
					unchanged = append(unchanged, model.Warning{
						Severity: model.SeverityWarn, Code: model.WarnPinConflict, Package: pkg.Name,
						Provider: resolution.Provider, Message: note,
					})
				}
			}
			continue
		}
		if !p.Capabilities().Update {
			skip(model.WarnUpdateUnsupported, pkg, resolution.Provider,
				fmt.Sprintf("%s: provider %s cannot update", pkg.Name, resolution.Provider))
			continue
		}
		before, err := p.Installed(ctx, pkg)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", pkg.Name, err.Error()))
			continue
		}
		if before == nil {
			skip(model.WarnNotInstalled, pkg, resolution.Provider,
				fmt.Sprintf("%s is not installed; run 'str sync'", pkg.Name))
			continue
		}
		before = e.withPath(before)
		state.Current = before
		from := shortOr(versionOf(before), "unknown")
		if opts.DryRun {
			res.Applied = append(res.Applied, model.PlannedAction{
				Action:  model.ActionUpdate,
				State:   state,
				Outcome: model.OutcomePlanned,
				Note:    fmt.Sprintf("currently %s", shortOr(before.Version, "an unknown version")),
				Changes: []model.ActionTransition{{Kind: model.ChangeVersion, From: versionOf(before), To: "newest"}},
				Effects: []model.Effect{packageEffect(model.EffectModify, resolution.Provider, pkg.Name,
					before, versionOf(before), "newest", model.EvidencePlanned)},
			})
			continue
		}
		obs := e.observe(ctx, p, pkg, before)
		after, err := p.Update(ctx, pkg)
		observed := obs.finish(ctx, model.EffectModify, after)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", pkg.Name, err.Error()))
			if len(observed) > 0 {
				// A failed upgrade that still changed files is reported as an
				// action, so those changes are not invisible.
				res.Applied = append(res.Applied, finishEffects(failed(model.PlannedAction{
					Action: model.ActionUpdate, State: state, Note: err.Error(),
				}, err), observed))
			}
			continue
		}
		to := shortOr(versionOf(after), "unknown")
		if from == to && len(observed) == 0 {
			skip(model.WarnAlreadyCurrent, pkg, resolution.Provider,
				fmt.Sprintf("%s is already current (%s)", pkg.Name, to))
			continue
		}
		observed = append(observed, packageEffect(model.EffectModify, resolution.Provider, pkg.Name,
			after, versionOf(before), versionOf(after), model.EvidenceReported))
		action := finishEffects(model.PlannedAction{
			Action:  model.ActionUpdate,
			State:   state,
			Outcome: model.OutcomeApplied,
			Note:    fmt.Sprintf("%s -> %s", from, to),
			Changes: []model.ActionTransition{{Kind: model.ChangeVersion, From: versionOf(before), To: versionOf(after)}},
		}, observed)
		res.Applied = append(res.Applied, action)
		res.Warnings = append(res.Warnings, action.Warnings...)
		// Record execution in tracking system
		if e.tracker != nil {
			_ = e.tracker.Record(pkg.Name, opts.Dir, time.Now())
		}
	}

	res.Warnings = append(res.Warnings, unchanged...)
	if len(failures) > 0 {
		return res, fmt.Errorf("update finished with problems:\n  %s", strings.Join(failures, "\n  "))
	}
	return res, nil
}

// pinConflict checks a satisfied pin against the newest version the provider
// would install.
//
// The configuration is the source of truth, so a newer version is a conflict
// to explain, not a reason to move. Only a package the user approved in
// opts.UpdatePinned is updated, and then the pin is rewritten to the version
// actually installed so the configuration keeps describing the machine.
func (e *Engine) pinConflict(ctx context.Context, doc *config.Document, p provider.Provider, pkg model.Package,
	providerName string, opts Options) (*model.PinConflict, string, error) {
	kept := fmt.Sprintf("%s is pinned to %s; left unchanged", pkg.Name, pkg.Version)
	lp, ok := p.(provider.LatestVersionProvider)
	if !ok {
		return nil, kept + " (" + providerName + " cannot report newer versions)", nil
	}
	latest, err := lp.LatestVersion(ctx, pkg)
	if err != nil || latest == "" || model.VersionMatches(pkg.Version, latest) ||
		model.CompareVersions(latest, pkg.Version) <= 0 {
		return nil, kept, nil
	}
	conflict := &model.PinConflict{
		Package:    pkg.Name,
		Provider:   providerName,
		Pinned:     pkg.Version,
		Available:  latest,
		Resolution: "kept",
	}
	note := fmt.Sprintf("there is a new version available for %s (%s), but the configuration says %s; left unchanged",
		pkg.Name, latest, pkg.Version)
	if opts.DryRun {
		conflict.Resolution = "pending"
		return conflict, note, nil
	}
	if !opts.UpdatePinned[pkg.Name] {
		return conflict, note, nil
	}

	target := pkg
	target.Version = latest
	var inst *model.Installation
	if p.Capabilities().Update {
		inst, err = p.Update(ctx, target)
	} else {
		inst, err = p.Install(ctx, provider.Request{Package: target, Dir: opts.Dir})
	}
	if err != nil {
		return nil, "", fmt.Errorf("updating past the pin: %w", err)
	}
	newPin := latest
	if inst != nil && inst.Version != "" {
		newPin = inst.Version
	}
	conflict.Available = newPin
	path, err := rewritePin(doc, pkg.Name, newPin)
	if err != nil {
		return nil, "", fmt.Errorf("%s was updated to %s but the pin could not be rewritten: %w", pkg.Name, newPin, err)
	}
	conflict.Resolution = "updated"
	conflict.ConfigPath = path
	if e.tracker != nil {
		_ = e.tracker.Record(pkg.Name, opts.Dir, time.Now())
	}
	return conflict, "", nil
}

// rewritePin sets a package's version in the file that declares it and saves.
func rewritePin(doc *config.Document, name, version string) (string, error) {
	f, ok := doc.Find(name)
	if !ok {
		return "", fmt.Errorf("%s is not declared in any configuration file", name)
	}
	for i := range f.Raw.Packages {
		if f.Raw.Packages[i].Name == name {
			f.Raw.Packages[i].Version = version
		}
	}
	storeFile(doc, f)
	if err := doc.Save(); err != nil {
		return "", err
	}
	return f.Path, nil
}

func shortOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func versionOf(i *model.Installation) string {
	if i == nil {
		return ""
	}
	return i.Version
}

// addPlan is everything Add decides before it changes anything.
type addPlan struct {
	available  map[string]provider.Health
	existing   *model.Installation
	resolution model.Resolution
	resolved   model.Package
	provider   provider.Provider
	known      bool
	desired    *model.Installation
	adopted    bool
}

// plan resolves a package and works out whether recording it would install
// anything, without installing anything.
//
// Preview and Add share this so the question asked before a change and the change
// itself can never disagree. Two separate derivations would be two chances to
// describe an operation that then did something else.
func (e *Engine) plan(ctx context.Context, pkg model.Package, opts Options) (*addPlan, error) {
	available := e.health(ctx)
	existing := e.findExisting(ctx, pkg, available)
	resolution := e.resolver.Resolve(e.resolveInput(pkg, existing, available, opts))
	if pkg.Reason != "" && resolution.Forced {
		// The provider came from the caller with its own reason (named on the
		// command line, or picked at a prompt); report that rather than the
		// resolver's generic "configuration selects it".
		resolution.Reason = pkg.Reason
	}

	resolved := pkg
	resolved.Provider = resolution.Provider
	// A reason supplied by the caller (an interactive choice, for example) is a
	// human decision and outranks the resolver's summary.
	if resolved.Reason == "" {
		resolved.Reason = resolution.Reason
	}

	p, known := e.reg.Get(resolution.Provider)
	out := &addPlan{
		available:  available,
		existing:   existing,
		resolution: resolution,
		resolved:   resolved,
		provider:   p,
		known:      known,
	}

	if known && available[resolution.Provider].Available && p.Capabilities().Discover {
		if inst, err := p.Installed(ctx, resolved); err == nil && inst != nil {
			out.desired = inst
			// Adoption means "this is already what was asked for". With an
			// explicit pin that is only true when the installed version is the
			// pinned one: adopting a 0.5.0 install of something pinned to 0.6.0
			// would record the pin as satisfied when it is not, and the next
			// sync would be the first to notice.
			out.adopted = model.VersionMatches(resolved.Version, inst.Version)
			if !out.adopted {
				out.desired = nil
			}
		}
	}
	return out, nil
}

// Preview reports what recording a package would do, without doing it.
//
// It returns the provider that would be used and the installation that would be
// adopted, if any. A non-nil installation means nothing on the machine changes —
// the software is already there and only the configuration would be written —
// which is a materially different thing to ask about and deserves to be shown
// before the question rather than discovered after it.
func (e *Engine) Preview(ctx context.Context, doc *config.Document, pkg model.Package, opts Options) (model.Resolution, *model.Installation, error) {
	if pkg.Name == "" {
		return model.Resolution{}, nil, fmt.Errorf("a package name is required")
	}
	plan, err := e.plan(ctx, pkg, opts)
	if err != nil {
		return model.Resolution{}, nil, err
	}
	var adopted *model.Installation
	if plan.adopted {
		adopted = plan.desired
	}
	return plan.resolution, adopted, nil
}

// UpdatePlan reports what Update would do, without doing it.
//
// It is Update with dry-run forced on, rather than a second derivation. The two
// would otherwise be free to disagree, and a preview that lists one set of
// packages while the command updates another is worse than no preview at all.
func (e *Engine) UpdatePlan(ctx context.Context, doc *config.Document, opts Options) ([]model.PlannedAction, error) {
	res, err := e.UpdatePreview(ctx, doc, opts)
	return res.Applied, err
}

// UpdatePreview is Update with dry-run forced on, returning the whole result so
// the caller can see pin conflicts as well as the updates.
func (e *Engine) UpdatePreview(ctx context.Context, doc *config.Document, opts Options) (*Result, error) {
	preview := opts
	preview.DryRun = true
	res, _ := e.Update(ctx, doc, preview)
	return res, nil
}

// plannedPinMove is the update a pin the user agreed to move becomes, as a
// planned action: the package moving past the pin and the pin rewritten in the
// file that declares it.
func (e *Engine) plannedPinMove(doc *config.Document, state model.State, inst *model.Installation,
	pkg model.Package, conflict *model.PinConflict) model.PlannedAction {
	state.Current = e.withPath(inst)
	path := ""
	if f, ok := doc.Find(pkg.Name); ok {
		path = f.Path
	}
	action := model.PlannedAction{
		Action:  model.ActionUpdate,
		State:   state,
		Outcome: model.OutcomePlanned,
		Note:    fmt.Sprintf("%s -> %s; pin would be rewritten in %s", pkg.Version, conflict.Available, shortOr(path, "the configuration")),
		Changes: []model.ActionTransition{{Kind: model.ChangeVersion, From: pkg.Version, To: conflict.Available}},
		Effects: []model.Effect{packageEffect(model.EffectModify, conflict.Provider, pkg.Name,
			state.Current, pkg.Version, conflict.Available, model.EvidencePlanned)},
	}
	after := pkg
	after.Version = conflict.Available
	if cc := model.DiffEntry(path, &pkg, &after); cc != nil {
		action.ConfigChanges = append(action.ConfigChanges, *cc)
		action.Effects = append(action.Effects, configEffect(*cc, model.EvidencePlanned))
	}
	model.SortEffects(action.Effects)
	return action
}
