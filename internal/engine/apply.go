package engine

import (
	"context"
	"fmt"
	"strings"

	"github.com/starpoint/starpoint/internal/config"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/system"
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
	}

	// Phase 2: installs.
	for _, action := range plan.Actions {
		if action.Action != model.ActionInstall {
			continue
		}
		p, ok := e.reg.Get(action.State.Res.Provider)
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: provider %q is unknown",
				action.State.Package.Name, action.State.Res.Provider))
			continue
		}
		inst, err := p.Install(ctx, provider.Request{
			Package: action.State.Package,
			Scope:   action.State.Package.Scope,
			Dir:     opts.Dir,
		})
		if err != nil {
			res.Applied = append(res.Applied, annotate(action, err.Error()))
			failures = append(failures, fmt.Sprintf("%s: %s", action.State.Package.Name, err.Error()))
			continue
		}
		res.Applied = append(res.Applied, annotate(action, installPath(inst)))
	}

	// Phase 3: relocations. Install the wanted copy first, verify, then remove
	// the copy the configuration no longer wants.
	for _, action := range plan.Actions {
		if action.Action != model.ActionRelocate {
			continue
		}
		if done, err := e.relocate(ctx, action, opts); err != nil {
			res.Applied = append(res.Applied, annotate(action, err.Error()))
			failures = append(failures, fmt.Sprintf("%s: %s", action.State.Package.Name, err.Error()))
		} else {
			res.Applied = append(res.Applied, annotate(action, done))
		}
	}

	// Phase 4: version reconciliation.
	for _, action := range plan.Actions {
		if action.Action != model.ActionUpdate {
			continue
		}
		p, ok := e.reg.Get(action.State.Res.Provider)
		if !ok {
			failures = append(failures, fmt.Sprintf("%s: provider %q is unknown",
				action.State.Package.Name, action.State.Res.Provider))
			continue
		}
		req := provider.Request{Package: action.State.Package, Dir: opts.Dir}
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
		if err != nil {
			res.Applied = append(res.Applied, annotate(action, err.Error()))
			failures = append(failures, fmt.Sprintf("%s: %s", action.State.Package.Name, err.Error()))
			continue
		}
		res.Applied = append(res.Applied, annotate(action, installPath(inst)))
	}

	// Phase 5: conflicts are reported, never resolved by editing the source of
	// truth.
	for _, action := range plan.Actions {
		if action.Action == model.ActionRecord {
			res.Applied = append(res.Applied, action)
		}
	}

	if len(failures) > 0 {
		return res, fmt.Errorf("some operations did not complete:\n  %s", strings.Join(failures, "\n  "))
	}
	return res, nil
}

// relocate moves a package from one owning provider to another.
func (e *Engine) relocate(ctx context.Context, action model.PlannedAction, opts Options) (string, error) {
	pkg := action.State.Package
	target, ok := e.reg.Get(action.State.Res.Provider)
	if !ok {
		return "", fmt.Errorf("provider %q is unknown", action.State.Res.Provider)
	}
	inst, err := target.Install(ctx, provider.Request{Package: pkg, Dir: opts.Dir})
	if err != nil {
		return "", err
	}
	// Only remove the old copy once the new one is verifiably present.
	verify, err := target.Installed(ctx, pkg)
	if err != nil || verify == nil {
		if inst != nil {
			return installPath(inst), nil
		}
		return "", fmt.Errorf("could not verify the new installation before removing the old one")
	}

	var notes []string
	notes = append(notes, "now at "+installPath(inst))
	if old := action.State.Current; old != nil && old.Provider != "" && old.Provider != target.Name() {
		if oldProv, ok := e.reg.Get(old.Provider); ok && oldProv.Capabilities().Remove {
			if err := oldProv.Remove(ctx, pkg); err != nil {
				notes = append(notes, "could not remove the "+old.Provider+" copy: "+err.Error())
			} else {
				notes = append(notes, "removed the "+old.Provider+" copy")
			}
		} else {
			notes = append(notes, "left the "+old.Provider+" copy in place: that provider cannot remove it")
		}
	}
	return strings.Join(notes, "; "), nil
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

	res := &Result{DryRun: opts.DryRun}
	state := model.State{Package: pkg, Res: model.Resolution{Provider: provName}}
	action := model.PlannedAction{Action: model.ActionRemove, State: state}

	if !opts.DryRun {
		if err := p.Remove(ctx, pkg); err != nil {
			return nil, err
		}
	}
	res.Applied = []model.PlannedAction{action}

	f, found := doc.Find(name)
	if !found {
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
			return res, fmt.Errorf("updating configuration: %w", err)
		}
	}
	res.ConfigChanged = true
	res.ConfigPath = f.Path
	return res, nil
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
// configuration is the source of truth, and `starpoint sync` will converge once
// the provider exists. With AdoptOnly, nothing is installed at all.
func (e *Engine) Add(ctx context.Context, doc *config.Document, pkg model.Package, opts Options) (*Result, error) {
	if pkg.Name == "" {
		return nil, fmt.Errorf("a package name is required")
	}
	available := e.health(ctx)
	existing := e.findExisting(ctx, pkg, available)
	resolution := e.resolver.Resolve(e.resolveInput(pkg, existing, available, opts))

	resolved := pkg
	resolved.Provider = resolution.Provider
	// A reason supplied by the caller (an interactive choice, for example) is a
	// human decision and outranks the resolver's summary.
	if resolved.Reason == "" {
		resolved.Reason = resolution.Reason
	}

	res := &Result{DryRun: opts.DryRun}
	state := model.State{Package: resolved, Res: resolution, Status: model.StatusCorrect}
	// adopted records that the software was already there. Recording ownership
	// is not the same act as installing, and the output should not claim it was.
	adopted := false

	p, known := e.reg.Get(resolution.Provider)
	if known && available[resolution.Provider].Available && p.Capabilities().Discover {
		if inst, err := p.Installed(ctx, resolved); err == nil && inst != nil {
			state.Desired = inst
			adopted = true
		}
	}

	if state.Desired == nil {
		switch {
		case !known:
			return nil, fmt.Errorf("provider %q is not known to this build", resolution.Provider)
		case !available[resolution.Provider].Available:
			// Record the intent, report the blocker.
			res.Warnings = append(res.Warnings, model.Warning{
				Severity: model.SeverityError,
				Package:  pkg.Name,
				Provider: resolution.Provider,
				Message:  "recorded in the configuration but not installed: " + available[resolution.Provider].Reason,
			})
			state.Status = model.StatusMissing
		case !p.Capabilities().Install:
			return nil, fmt.Errorf("provider %q cannot install packages", resolution.Provider)
		case opts.AdoptOnly:
			res.Warnings = append(res.Warnings, model.Warning{
				Severity: model.SeverityWarn,
				Package:  pkg.Name,
				Provider: resolution.Provider,
				Message: "recorded in the configuration but not installed: " +
					adoptionReason(existing, opts),
			})
		default:
			state.Status = model.StatusMissing
			if !opts.DryRun {
				inst, err := p.Install(ctx, provider.Request{Package: resolved, Dir: opts.Dir})
				if err != nil {
					return nil, err
				}
				state.Desired = inst
				state.Status = model.StatusCorrect
			}
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
	res.States = []model.State{state}
	res.Applied = []model.PlannedAction{{
		Action: verb,
		State:  state,
		Note:   note,
	}}

	target := doc.Global
	if opts.Dir != "" {
		if f, ok := doc.ProjectFileFor(opts.Dir); ok {
			target = f
		}
	}
	if contains(target.Raw.Packages, pkg.Name) {
		replaceEntry(&target.Raw.Packages, resolved)
	} else {
		target.Raw.Packages = append(target.Raw.Packages, resolved)
	}
	storeFile(doc, target)

	if !opts.DryRun {
		if err := doc.Save(); err != nil {
			return nil, fmt.Errorf("recording %s in %s: %w", pkg.Name, target.Path, err)
		}
	}
	res.ConfigChanged = true
	res.ConfigPath = target.Path
	return res, nil
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
		unchanged []string
		failures  []string
	)
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
			unchanged = append(unchanged, fmt.Sprintf("%s is pinned to %s; left unchanged",
				pkg.Name, pkg.Version))
			continue
		}
		if !p.Capabilities().Update {
			unchanged = append(unchanged, fmt.Sprintf("%s: provider %s cannot update", pkg.Name, resolution.Provider))
			continue
		}
		before, err := p.Installed(ctx, pkg)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", pkg.Name, err.Error()))
			continue
		}
		if before == nil {
			unchanged = append(unchanged, fmt.Sprintf("%s is not installed; run 'starpoint sync'", pkg.Name))
			continue
		}
		if opts.DryRun {
			res.Applied = append(res.Applied, model.PlannedAction{
				Action: model.ActionUpdate,
				State:  state,
				Note:   fmt.Sprintf("currently %s", shortOr(before.Version, "an unknown version")),
			})
			continue
		}
		after, err := p.Update(ctx, pkg)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", pkg.Name, err.Error()))
			continue
		}
		from := shortOr(versionOf(before), "unknown")
		to := shortOr(versionOf(after), "unknown")
		if from == to {
			unchanged = append(unchanged, fmt.Sprintf("%s is already current (%s)", pkg.Name, to))
			continue
		}
		res.Applied = append(res.Applied, model.PlannedAction{
			Action: model.ActionUpdate,
			State:  state,
			Note:   fmt.Sprintf("%s -> %s", from, to),
		})
	}

	for _, u := range unchanged {
		res.Warnings = append(res.Warnings, model.Warning{Severity: model.SeverityInfo, Message: u})
	}
	if len(failures) > 0 {
		return res, fmt.Errorf("update finished with problems:\n  %s", strings.Join(failures, "\n  "))
	}
	return res, nil
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
