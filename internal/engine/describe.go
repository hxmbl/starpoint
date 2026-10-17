package engine

import (
	"context"
	"errors"
	"fmt"
	"github.com/hxmbl/starpoint/internal/exec"
	"path/filepath"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
)

// describePlanned fills in what a planned action would do: the effects it is
// expected to have, the configuration entries it would edit, and the provider
// or version moves it brings about.
//
// It only predicts. When the action runs, Apply replaces the prediction with
// what was observed.
func (e *Engine) describePlanned(ctx context.Context, doc *config.Document, a model.PlannedAction) model.PlannedAction {
	st := a.State
	p, _ := e.reg.Get(st.Res.Provider)
	switch a.Action {
	case model.ActionInstall:
		effects, warns := e.plannedInstall(ctx, p, st.Package)
		a.Effects = model.MergeEffects(a.Effects, effects...)
		a.Warnings = append(a.Warnings, warns...)
	case model.ActionUpdate:
		from := versionOf(st.Desired)
		a.Effects = model.MergeEffects(a.Effects,
			packageEffect(model.EffectModify, st.Res.Provider, st.Package.Name, st.Desired, from, st.Package.Version, model.EvidencePlanned))
		a.Changes = append(a.Changes, model.ActionTransition{Kind: model.ChangeVersion, From: from, To: st.Package.Version})
	case model.ActionRelocate:
		if a.Transition != nil {
			effects, warns := e.plannedTransition(ctx, *a.Transition)
			a.Effects = model.MergeEffects(a.Effects, effects...)
			a.Warnings = append(a.Warnings, warns...)
			a.Changes = append(a.Changes, providerChange(*a.Transition))
		}
	}
	if a.ConfigChange != nil {
		if cc := configChangeFor(doc, a.ConfigChange); cc != nil {
			a.ConfigChanges = append(a.ConfigChanges, *cc)
			a.Effects = model.MergeEffects(a.Effects, configEffect(*cc, model.EvidencePlanned))
		}
	}
	model.SortEffects(a.Effects)
	return a
}

// providerChange is the action-level summary of a provider move.
func providerChange(t model.Transition) model.ActionTransition {
	return model.ActionTransition{Kind: model.ChangeProvider, From: t.From.Name, To: t.To.Name, Phase: t.Phase}
}

// plannedTransition predicts what moving a package between providers would
// touch, including the case where both providers write the same executable.
func (e *Engine) plannedTransition(ctx context.Context, t model.Transition) ([]model.Effect, []model.Warning) {
	if t.Phase == model.TransitionCompleted || t.Phase == model.TransitionBlocked {
		return nil, nil
	}
	pkg := t.Package
	target, _ := e.reg.Get(t.To.Name)
	old, hasOld := e.reg.Get(t.From.Name)
	var effects []model.Effect
	var warnings []model.Warning

	if t.Phase == model.TransitionPlanned && target != nil {
		if shared, ok := e.sharedExecutable(ctx, t, pkg, target); ok {
			from := ""
			if t.Previous != nil {
				from = t.Previous.Version
			}
			entry := readEntry(shared)
			effects = append(effects,
				packageEffect(model.EffectDelete, t.From.Name, pkg.Name, t.Previous, from, "", model.EvidencePlanned),
				packageEffect(model.EffectCreate, t.To.Name, pkg.Name, nil, "", pkg.Version, model.EvidencePlanned),
				model.Effect{
					Op: model.EffectReplace, Kind: entry.kind, Path: shared, From: entry.target,
					Destructive: true, Environment: e.onPath(filepath.Dir(shared)), Evidence: model.EvidencePlanned,
					Detail: fmt.Sprintf("%s and %s both install %s; the %s copy is removed first, and reinstalled if %s cannot take over",
						t.From.Name, t.To.Name, shared, t.From.Name, t.To.Name),
				})
			warnings = append(warnings, sharedWarning(t, shared))
			return effects, warnings
		}
		installEffects, installWarnings := e.plannedInstall(ctx, target, pkg)
		effects = append(effects, installEffects...)
		warnings = append(warnings, installWarnings...)
	}
	if hasOld && old.Capabilities().Remove && t.From.Name != "" && t.From.Available {
		removal := e.plannedRemove(ctx, old, pkg, t.Previous)
		for i := range removal {
			removal[i].Detail = joinDetail(removal[i].Detail,
				"only once the "+t.To.Name+" copy is confirmed working")
		}
		effects = model.MergeEffects(effects, removal...)
	}
	return effects, warnings
}

// sharedWarning explains a hand-over between providers that write the same
// executable path.
func sharedWarning(t model.Transition, shared string) model.Warning {
	return model.Warning{
		Severity: model.SeverityWarn,
		Code:     model.WarnSharedExecutable,
		Package:  t.Package.Name,
		Provider: t.To.Name,
		Message: fmt.Sprintf("%s and %s both write %s, so the %s copy has to be removed before %s can install; "+
			"if %s fails, the %s copy is reinstalled", t.From.Name, t.To.Name, shared, t.From.Name, t.To.Name, t.To.Name, t.From.Name),
	}
}

// configChangeFor describes a planned configuration edit against the entry it
// would replace.
func configChangeFor(doc *config.Document, edit *model.ConfigEdit) *model.ConfigChange {
	if doc == nil || edit == nil {
		return nil
	}
	f := targetFile(doc, edit.File)
	if edit.Remove != "" {
		before := entryIn(f, edit.Remove)
		if before == nil {
			return nil
		}
		return model.DiffEntry(f.Path, before, nil)
	}
	if edit.Upsert == nil {
		return nil
	}
	cc := model.DiffEntry(f.Path, entryIn(f, edit.Upsert.Name), edit.Upsert)
	if cc != nil {
		cc.CreatesFile = !f.Exists
	}
	return cc
}

// targetFile is the file a configuration edit lands in: the named file when
// the document has it, the global file otherwise. It mirrors applyEdit.
func targetFile(doc *config.Document, path string) config.File {
	if path != "" && path != doc.Global.Path {
		for _, f := range doc.Projects {
			if f.Path == path {
				return f
			}
		}
	}
	return doc.Global
}

// entryIn returns a copy of a file's entry for name, or nil.
func entryIn(f config.File, name string) *model.Package {
	for _, p := range f.Raw.Packages {
		if p.Name == name {
			copied := p
			return &copied
		}
	}
	return nil
}

// configEffect is the filesystem side of a configuration change.
func configEffect(cc model.ConfigChange, evidence model.Evidence) model.Effect {
	op := model.EffectModify
	if cc.CreatesFile {
		op = model.EffectCreate
	}
	return model.Effect{
		Op:          op,
		Kind:        model.EffectConfig,
		Path:        cc.Path,
		Name:        cc.Package,
		Destructive: cc.Destructive,
		Evidence:    evidence,
		Detail:      cc.Change + " " + cc.Package,
	}
}

// applied marks configuration changes as written and their effects as
// observed, once the file has been saved.
func appliedConfig(a model.PlannedAction) model.PlannedAction {
	for i := range a.ConfigChanges {
		a.ConfigChanges[i].Applied = true
	}
	for i := range a.Effects {
		if a.Effects[i].Kind == model.EffectConfig {
			a.Effects[i].Evidence = model.EvidenceObserved
		}
	}
	return a
}

// finishEffects merges observed effects into an action, dropping the
// predictions they supersede, and derives warnings from them.
func finishEffects(a model.PlannedAction, observed []model.Effect) model.PlannedAction {
	kept := []model.Effect{}
	for _, eff := range a.Effects {
		// Configuration effects are Starpoint's own and stay; every other
		// prediction is replaced by what was actually seen.
		if eff.Kind == model.EffectConfig {
			kept = append(kept, eff)
		}
	}
	a.Effects = model.MergeEffects(kept, observed...)
	a.Warnings = append(a.Warnings, effectWarnings(a.State.Package.Name, observed)...)
	model.SortEffects(a.Effects)
	return a
}

// failed marks an action as attempted and not completed.
func failed(a model.PlannedAction, err error) model.PlannedAction {
	a.Outcome = model.OutcomeFailed
	if err != nil {
		a.Error = err.Error()
	}
	var drop *exec.DropError
	if errors.As(err, &drop) {
		a.Warnings = append(a.Warnings, model.Warning{
			Severity: model.SeverityError, Code: model.WarnPrivilegeDropFailed, Package: a.State.Package.Name,
			Provider: a.State.Res.Provider,
			Message:  drop.Error(),
		})
	}
	return a
}

func joinDetail(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

// providerOrNil returns the registered provider for name, or nil.
func (e *Engine) providerOrNil(name string) provider.Provider {
	p, ok := e.reg.Get(name)
	if !ok {
		return nil
	}
	return p
}

// transitionWarnings turns a provider move that did not reach a resting state,
// or left a duplicate behind, into a warning on the action.
func transitionWarnings(t model.Transition) []model.Warning {
	switch {
	case t.Phase == model.TransitionCompleted && t.LeftBehindReason != "":
		return []model.Warning{{
			Severity: model.SeverityWarn, Code: model.WarnTransitionLeftBehind,
			Package: t.Package.Name, Provider: t.From.Name,
			Message: fmt.Sprintf("ownership moved to %s but the %s copy is still there (%s)", t.To.Name, t.From.Name, t.LeftBehindReason),
		}}
	case t.Phase == model.TransitionFailed:
		return []model.Warning{{
			Severity: model.SeverityError, Code: model.WarnTransitionIncomplete,
			Package: t.Package.Name, Provider: t.To.Name,
			Message: joinDetail("the move from "+t.From.Name+" to "+t.To.Name+" failed", t.Advice),
		}}
	case t.NeedsWork():
		return []model.Warning{{
			Severity: model.SeverityWarn, Code: model.WarnTransitionIncomplete,
			Package: t.Package.Name, Provider: t.To.Name,
			Message: joinDetail(fmt.Sprintf("the move from %s to %s stopped at %s", t.From.Name, t.To.Name, t.Phase), t.Advice),
		}}
	}
	return nil
}

// TransitionAction is the action-shaped record of a provider move performed
// (or previewed) by the transition command, so it reports through the same
// action vocabulary as sync, with the same effects and warnings.
func TransitionAction(t model.Transition, err error, dryRun bool) model.PlannedAction {
	a := model.PlannedAction{
		Action:     model.ActionRelocate,
		State:      model.State{Package: t.Package, Res: model.Resolution{Provider: t.To.Name}, Current: t.Previous, Desired: t.Installed},
		Transition: &t,
		Note:       joinDetail(t.Note, t.Advice),
		Changes:    []model.ActionTransition{providerChange(t)},
		Effects:    append([]model.Effect{}, t.Effects...),
		Warnings:   append(append([]model.Warning{}, t.Warnings...), transitionWarnings(t)...),
	}
	switch {
	case dryRun:
		a.Outcome = model.OutcomePlanned
	case err != nil || t.Phase == model.TransitionFailed:
		a = failed(a, err)
	default:
		a.Outcome = model.OutcomeApplied
	}
	return a
}

// PlannedTransitionEffects predicts what performing a transition would touch,
// for the confirmation step.
func (e *Engine) PlannedTransitionEffects(ctx context.Context, t model.Transition) ([]model.Effect, []model.Warning) {
	effects, warnings := e.plannedTransition(ctx, t)
	model.SortEffects(effects)
	return effects, warnings
}
