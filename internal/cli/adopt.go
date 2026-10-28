package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/system"
	"github.com/hxmbl/starpoint/internal/report"
)

// adoptSkip is software adopt looked at and left unmanaged, with the reason.
type adoptSkip struct {
	Package  string `json:"package"`
	Provider string `json:"provider,omitempty"`
	Reason   string `json:"reason"`
}

// adoptLeft counts, per provider, what `adopt --all` does not take because
// the provider does not say it was installed on purpose.
type adoptLeft struct {
	Provider   string `json:"provider"`
	Dependency int    `json:"dependency"`
	Unknown    int    `json:"unknown"`
}

// cmdAdopt records the owner of software that is already installed.
//
// Adopting is ownership bookkeeping, nothing else: it never installs,
// uninstalls or moves anything. The real provider keeps the lifecycle and
// Starpoint writes down whose the software is, so the next sync agrees with
// the machine instead of fighting it.
func cmdAdopt(ctx context.Context, env *Env, w *report.Writer, args []string, allowed []string) error {
	named, args, err := env.splitProviderArg(args, allowed)
	if err != nil {
		return err
	}
	var (
		dryRun, noConfirm, all bool
		names                  []string
	)
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dryRun = true
		case "--no-confirm", "-y", "--yes":
			noConfirm = true
		case "--all", "-a":
			all = true
		default:
			if err := rejectOption("adopt", a); err != nil {
				return err
			}
			names = append(names, a)
		}
	}
	switch {
	case all && len(names) > 0:
		return fmt.Errorf("%w: adopt takes package names or --all, not both", ErrUsage)
	case !all && len(names) == 0:
		return fmt.Errorf("%w: adopt needs a package name, or --all", ErrUsage)
	}

	doc, eng, opts, _, err := env.setup(ctx, allowed)
	if err != nil {
		return err
	}
	if !dryRun {
		path, _ := env.globalPath()
		unlock, lockErr := env.acquireMutationLock(path, "adopt")
		if lockErr != nil {
			return lockErr
		}
		defer unlock()
	}
	opts.DryRun = dryRun
	opts.NoConfirm = noConfirm || doc.Settings().NoConfirm
	if named != "" {
		opts.Allowed = append(opts.Allowed, named)
	}

	var (
		picks    []model.Package
		skipped  []adoptSkip
		left     []adoptLeft
		warnings []model.Warning
	)
	if all {
		picks, skipped, left, warnings, err = env.adoptAll(ctx, eng, doc, opts)
	} else {
		for _, name := range names {
			pkg, skip, perr := env.adoptOne(ctx, eng, doc, opts, name, singleProvider(opts.Allowed))
			if perr != nil {
				return perr
			}
			if skip != nil {
				skipped = append(skipped, *skip)
				continue
			}
			picks = append(picks, pkg)
		}
	}
	if err != nil {
		return err
	}

	adoptOpts := opts
	adoptOpts.AdoptOnly = true
	var changes []change
	var keep []model.Package
	for _, pkg := range picks {
		preview, perr := eng.AddPreview(ctx, doc, pkg, adoptOpts)
		if perr != nil {
			if all && provider.IsNotFound(perr) {
				skipped = append(skipped, adoptSkip{Package: pkg.Name, Provider: pkg.Provider, Reason: perr.Error()})
				continue
			}
			return perr
		}
		keep = append(keep, pkg)
		changes = append(changes, change{
			verb:     "Record",
			target:   pkg.Name,
			via:      pkg.Provider,
			detail:   pkg.Reason,
			after:    "nothing is installed, removed or moved; " + pkg.Provider + " keeps managing it",
			effects:  preview.Effects,
			warnings: preview.Warnings,
		})
	}

	if err := env.confirmChanges(changes, opts); err != nil {
		return err
	}
	if env.declined() {
		fmt.Fprintln(env.Stdout, env.palOut.Dim("Nothing was changed."))
		return nil
	}

	merged := &engine.Result{DryRun: dryRun, Warnings: warnings}
	for _, pkg := range keep {
		res, aerr := eng.Add(ctx, doc, pkg, adoptOpts)
		if res != nil {
			merged.Applied = append(merged.Applied, res.Applied...)
			merged.States = append(merged.States, res.States...)
			merged.Warnings = append(merged.Warnings, res.Warnings...)
			merged.ConfigChanged = merged.ConfigChanged || res.ConfigChanged
			if res.ConfigPath != "" {
				merged.ConfigPath = res.ConfigPath
			}
		}
		if aerr != nil {
			env.auditActions("adopt", dryRun, merged)
			_ = reportAdopt(env, w, dryRun, merged, skipped, left)
			return aerr
		}
	}
	env.auditActions("adopt", dryRun, merged)
	return reportAdopt(env, w, dryRun, merged, skipped, left)
}

// adoptOne decides the owner of one named package. It returns the package to
// record, or a skip when there is nothing to do.
func (e *Env) adoptOne(ctx context.Context, eng *engine.Engine, doc *config.Document, opts engine.Options,
	name, explicit string) (model.Package, *adoptSkip, error) {
	pkg := model.Package{Name: name}
	if entry, ok := doc.Package(name); ok {
		pkg = entry
	}
	if explicit == system.Name {
		return pkg, nil, fmt.Errorf("%s: system software belongs to the operating system; Starpoint does not manage it", name)
	}

	owner := explicit
	if owner == "" {
		survey := eng.Survey(ctx, model.Package{Name: name}, opts)
		var owners []string
		systemHas := false
		for _, f := range survey.Owners() {
			if f.Provider == system.Name {
				systemHas = true
				continue
			}
			owners = append(owners, f.Provider)
		}
		switch len(owners) {
		case 0:
			if systemHas {
				return pkg, nil, fmt.Errorf("%s is only provided by the operating system; Starpoint does not manage system software, so nothing was recorded", name)
			}
			return pkg, nil, &provider.Error{Provider: "adopt", Kind: provider.ErrNotFound,
				Message: name + " is not installed through any available provider; adopt only records software that is already there, so nothing was recorded"}
		case 1:
			owner = owners[0]
		default:
			sort.Strings(owners)
			chosen, err := e.chooseOwner(name, owners, opts)
			if err != nil {
				return pkg, nil, err
			}
			owner = chosen
		}
	}

	if pkg.Provider == owner && pkg.Origin.Path != "" {
		return pkg, &adoptSkip{Package: name, Provider: owner, Reason: "already recorded as owned by " + owner}, nil
	}
	if pkg.Provider != "" && pkg.Provider != owner {
		pkg.Reason = "adopted: installed through " + owner + " (the configuration said " + pkg.Provider + ")"
	} else {
		pkg.Reason = "adopted: already installed through " + owner
	}
	pkg.Provider = owner
	return pkg, nil, nil
}

// chooseOwner asks which of several providers owns something. With nobody
// to ask it refuses rather than picking: an owner chosen by registration
// order is a decision nobody made.
func (e *Env) chooseOwner(name string, owners []string, opts engine.Options) (string, error) {
	if e.Choose != nil && e.interactive() && !opts.NoConfirm {
		return e.Choose(fmt.Sprintf("%s is installed through %s. Which one owns it?", name, strings.Join(owners, " and ")), owners)
	}
	return "", &RefusalError{
		Code: CodeOwnerAmbiguous,
		Exit: ExitRefusedDecision,
		Message: fmt.Sprintf("%s is installed through %s and nothing says which one owns it; "+
			"name the owner: 'str adopt %s <provider>'. Nothing was changed", name, strings.Join(owners, " and "), name),
		Command:    "adopt",
		Package:    name,
		Candidates: owners,
	}
}

// adoptAll picks everything a provider says was installed on purpose and
// that no configuration entry accounts for.
//
// Only what the provider reports as requested is taken: a dependency, or a
// package whose provider cannot tell, stays unmanaged and is counted. The
// system provider is never adopted, and neither is a name the configuration
// already records with another provider (that is a decision for transition
// or a named adopt) or one two providers both claim, which is asked about
// at a terminal and otherwise left out with an owner-ambiguous warning.
func (e *Env) adoptAll(ctx context.Context, eng *engine.Engine, doc *config.Document, opts engine.Options) (
	[]model.Package, []adoptSkip, []adoptLeft, []model.Warning, error) {
	inv := eng.Inventory(ctx, doc, opts.Allowed)
	warnings := append([]model.Warning{}, inv.Warnings...)
	leftBy := map[string]*adoptLeft{}
	var leftOrder []string
	byName := map[string][]engine.InventoryItem{}
	var order []string
	for _, it := range inv.Items {
		if it.Provider == system.Name || it.Managed {
			continue
		}
		if it.Requested != engine.RequestedYes {
			l := leftBy[it.Provider]
			if l == nil {
				l = &adoptLeft{Provider: it.Provider}
				leftBy[it.Provider] = l
				leftOrder = append(leftOrder, it.Provider)
			}
			if it.Requested == engine.RequestedDependency {
				l.Dependency++
			} else {
				l.Unknown++
			}
			continue
		}
		if _, seen := byName[it.Name]; !seen {
			order = append(order, it.Name)
		}
		byName[it.Name] = append(byName[it.Name], it)
	}

	var picks []model.Package
	var skipped []adoptSkip
	for _, name := range order {
		items := byName[name]
		if entry, ok := doc.Package(name); ok && entry.Provider != "" {
			skipped = append(skipped, adoptSkip{Package: name, Provider: items[0].Provider,
				Reason: "the configuration records it with " + entry.Provider + "; run 'str transition " + name +
					"' to move it there, or 'str adopt " + name + " " + items[0].Provider + "' to record this copy"})
			continue
		}
		owner := items[0].Provider
		if len(items) > 1 {
			var owners []string
			for _, it := range items {
				owners = append(owners, it.Provider)
			}
			chosen, err := e.chooseOwner(name, owners, opts)
			if err != nil {
				if r, ok := asRefusal(err); ok {
					warnings = append(warnings, model.Warning{Severity: model.SeverityWarn, Code: CodeOwnerAmbiguous,
						Package: name, Message: r.Message})
					skipped = append(skipped, adoptSkip{Package: name, Reason: "installed through " + strings.Join(owners, " and ") + "; name the owner with 'str adopt " + name + " <provider>'"})
					continue
				}
				return nil, nil, nil, nil, err
			}
			owner = chosen
		}
		pkg := model.Package{Name: name, Provider: owner, Reason: "adopted: installed on request through " + owner}
		if entry, ok := doc.Package(name); ok {
			entry.Provider, entry.Reason = pkg.Provider, pkg.Reason
			pkg = entry
		}
		picks = append(picks, pkg)
	}
	left := make([]adoptLeft, 0, len(leftOrder))
	for _, p := range leftOrder {
		left = append(left, *leftBy[p])
	}
	return picks, skipped, left, warnings, nil
}

// reportAdopt renders what adopt recorded, what it left alone and why.
func reportAdopt(env *Env, w *report.Writer, dryRun bool, res *engine.Result, skipped []adoptSkip, left []adoptLeft) error {
	if skipped == nil {
		skipped = []adoptSkip{}
	}
	if left == nil {
		left = []adoptLeft{}
	}
	if w.JSON() {
		return w.Operation("adopt", dryRun, []*engine.Result{res}, map[string]any{"skipped": skipped, "left_unmanaged": left})
	}
	if len(res.Applied) == 0 {
		fmt.Fprintln(env.Stdout, "Nothing to adopt.")
	} else if err := w.Result("adopt", res); err != nil {
		return err
	}
	for _, s := range skipped {
		fmt.Fprintf(env.Stdout, "%s %s: %s\n", env.palOut.Dim("left alone"), s.Package, s.Reason)
	}
	for _, l := range left {
		fmt.Fprintf(env.Stdout, "%s %s: %d installed as dependencies, %d it cannot say were installed on purpose\n",
			env.palOut.Dim("left unmanaged"), l.Provider, l.Dependency, l.Unknown)
	}
	return nil
}
