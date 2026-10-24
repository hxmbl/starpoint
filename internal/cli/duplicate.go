package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/providers/system"
)

// Decision refusals (exit 5) added by the duplicate and ambiguity guards.
const (
	// CodeDuplicateInstall: an unattended install would put a second copy
	// next to one another provider (or the operating system) already has.
	CodeDuplicateInstall = "duplicate-install"
	// CodeProviderAmbiguous: several providers could own a package that is
	// not installed yet, and nobody was there to choose.
	CodeProviderAmbiguous = "provider-ambiguous"
)

// otherCopies reports who else has the software than target: the managing
// providers, and whether the operating system has a copy of its own.
func otherCopies(target string, survey engine.Survey) (others []string, systemHas bool) {
	for _, f := range survey.Owners() {
		switch f.Provider {
		case target:
		case system.Name:
			systemHas = true
		default:
			others = append(others, f.Provider)
		}
	}
	sort.Strings(others)
	return others, systemHas
}

// duplicateWarning is what the confirmation shows when an install would add
// a second copy, so a person at a terminal sees it before saying yes.
func duplicateWarning(name, target string, survey engine.Survey) *model.Warning {
	others, systemHas := otherCopies(target, survey)
	var msg string
	switch {
	case len(others) > 0:
		msg = fmt.Sprintf("%s is already installed through %s; this installs a second copy through %s. "+
			"'starpoint adopt %s %s' records the existing copy instead, and 'starpoint transition' moves it",
			name, strings.Join(others, " and "), target, name, others[0])
	case systemHas:
		msg = fmt.Sprintf("the operating system already provides %s; this installs a managed copy through %s beside it", name, target)
	default:
		return nil
	}
	return &model.Warning{Severity: model.SeverityWarn, Code: CodeDuplicateInstall, Package: name, Provider: target, Message: msg}
}

// guardDuplicate refuses an unattended install that would silently create a
// second copy.
//
// Another provider already having the software is refused whoever chose the
// provider: naming uv for something Homebrew has is the duplicate-install
// mistake itself. A copy only the operating system has is refused only when
// Starpoint picked the provider; naming one is the explicit request for a
// managed copy. --allow-duplicate allows both. A person at a terminal is
// not refused: the confirmation shows the duplicate-install warning first.
func (e *Env) guardDuplicate(pkg model.Package, target string, survey engine.Survey, preview model.PlannedAction,
	opts engine.Options, allow bool) error {
	if opts.DryRun || allow || !e.unattended(opts) {
		return nil
	}
	others, systemHas := otherCopies(target, survey)
	explicit := pkg.Provider != "" && pkg.Provider == target
	var msg string
	var candidates []string
	switch {
	case len(others) > 0:
		candidates = others
		msg = fmt.Sprintf("refusing to install a second copy of %s through %s unattended: %s already has it. "+
			"Record that copy with 'starpoint adopt %s %s', move it with 'starpoint transition', "+
			"or pass --allow-duplicate if two copies are what you want. Nothing was changed",
			pkg.Name, target, strings.Join(others, " and "), pkg.Name, others[0])
	case systemHas && !explicit:
		candidates = []string{system.Name}
		msg = fmt.Sprintf("refusing to install %s through %s unattended: the operating system already provides it. "+
			"Name the provider ('starpoint install %s %s') to ask for a managed copy, or pass --allow-duplicate. Nothing was changed",
			pkg.Name, target, pkg.Name, target)
	default:
		return nil
	}
	actions := changeActions(model.ActionInstall, []model.PlannedAction{preview})
	e.audit(auditlog.Entry{Event: auditlog.EventGuard, Command: "install", Result: "refused",
		Detail: CodeDuplicateInstall + ": " + msg, Effects: plannedEffects(actions)})
	return &RefusalError{Code: CodeDuplicateInstall, Exit: ExitRefusedDecision, Message: msg, Command: "install",
		Actions: actions, Package: pkg.Name, Candidates: candidates}
}

// guardAmbiguous refuses an unattended run that would install something
// whose provider Starpoint could only guess at. At a terminal the
// confirmation names the guess; with nobody to read it, picking one is a
// decision nobody made.
func (e *Env) guardAmbiguous(command string, opts engine.Options, actions []model.PlannedAction) error {
	if opts.DryRun || !e.unattended(opts) {
		return nil
	}
	var names []string
	var first *model.PlannedAction
	for i, a := range actions {
		if a.Action != model.ActionInstall || !a.State.Res.Ambiguous || a.State.Package.Provider != "" {
			continue
		}
		if first == nil {
			first = &actions[i]
		}
		names = append(names, a.State.Package.Name)
	}
	if first == nil {
		return nil
	}
	candidates := append([]string{first.State.Res.Provider}, first.State.Res.Alternatives...)
	msg := fmt.Sprintf("refusing to %s unattended: nothing says which provider should own %s (%s could). "+
		"Set provider in the configuration or install it with 'starpoint install <package> <provider>'. Nothing was changed",
		command, strings.Join(names, ", "), strings.Join(candidates, ", "))
	e.audit(auditlog.Entry{Event: auditlog.EventGuard, Command: command, Result: "refused", Detail: CodeProviderAmbiguous + ": " + msg})
	return &RefusalError{Code: CodeProviderAmbiguous, Exit: ExitRefusedDecision, Message: msg, Command: command,
		Actions: changeActions(model.ActionInstall, actions), Package: first.State.Package.Name, Candidates: candidates}
}

// besideSystem takes out of a sync plan every entry with no provider whose
// only copy is the operating system's, as the install of a managed copy it
// would take to converge it. Left in, it would be reported as provider
// drift naming a configuration choice nobody wrote down.
func besideSystem(ctx context.Context, eng *engine.Engine, doc *config.Document, plan model.Plan) (model.Plan, []model.PlannedAction) {
	var beside []model.PlannedAction
	kept := plan.Actions[:0:0]
	for _, a := range plan.Actions {
		if install, ok := eng.InstallBesideSystem(ctx, doc, a); ok {
			beside = append(beside, install)
			continue
		}
		kept = append(kept, a)
	}
	plan.Actions = kept
	return plan, beside
}

// systemPath says where the operating system's copy is, for messages.
func systemPath(a model.PlannedAction) string {
	if c := a.State.Current; c != nil && c.Path != "" {
		return " (" + c.Path + ")"
	}
	return ""
}

// besideSystemWarning is the duplicate-install warning a sync puts on such
// an install, so whoever reads the plan sees the system's copy first.
func besideSystemWarning(a model.PlannedAction) model.Warning {
	name, target := a.State.Package.Name, a.State.Res.Provider
	return model.Warning{Severity: model.SeverityWarn, Code: CodeDuplicateInstall, Package: name, Provider: target,
		Message: fmt.Sprintf("the operating system already provides %s%s and the configuration names no provider; "+
			"sync asks before installing a managed copy through %s beside it, and refuses unattended", name, systemPath(a), target)}
}

// guardBesideSystem refuses an unattended sync that would add a managed copy
// of software only the operating system has, for an entry that names no
// provider. It is install's rule, with install's code: naming the provider
// is the request for a managed copy, and without one nobody made it.
func (e *Env) guardBesideSystem(command string, opts engine.Options, beside []model.PlannedAction) error {
	if len(beside) == 0 || opts.DryRun || !e.unattended(opts) {
		return nil
	}
	names := make([]string, 0, len(beside))
	for _, a := range beside {
		names = append(names, a.State.Package.Name)
	}
	first := beside[0]
	name, target := first.State.Package.Name, first.State.Res.Provider
	msg := fmt.Sprintf("refusing to %s unattended: only the operating system provides %s, and the configuration names no provider, "+
		"so converging would install a managed copy beside the system's that nobody asked for. "+
		"Run 'starpoint install %s %s' for a managed copy, or set provider = \"system\" on the entry to keep the operating system's. Nothing was changed",
		command, strings.Join(names, ", "), name, target)
	actions := changeActions(model.ActionInstall, beside)
	e.audit(auditlog.Entry{Event: auditlog.EventGuard, Command: command, Result: "refused",
		Detail: CodeDuplicateInstall + ": " + msg, Effects: plannedEffects(actions)})
	return &RefusalError{Code: CodeDuplicateInstall, Exit: ExitRefusedDecision, Message: msg, Command: command,
		Actions: actions, Package: name, Candidates: []string{system.Name}}
}

// decideBesideSystem settles each managed copy a sync would add beside the
// operating system's. A dry run shows it as planned, with the warning. At a
// terminal each one is its own question, default no; a no leaves the
// system's copy alone, installs nothing, and says how to get one later.
func (e *Env) decideBesideSystem(opts engine.Options, plan model.Plan, beside []model.PlannedAction) (model.Plan, error) {
	for _, a := range beside {
		name, target := a.State.Package.Name, a.State.Res.Provider
		a.Warnings = append(a.Warnings, besideSystemWarning(a))
		if opts.DryRun {
			a.Note = joinNote("asks first: the operating system already provides "+name, a.Note)
			plan.Actions = append(plan.Actions, a)
			continue
		}
		yes := false
		if e.Confirm != nil {
			ok, err := e.Confirm(fmt.Sprintf("The operating system already provides %s%s and the configuration names no provider. "+
				"Install a managed copy through %s beside it?", name, systemPath(a), target))
			if err != nil {
				return plan, err
			}
			yes = ok
		}
		if yes {
			a.Note = joinNote("a managed copy beside the operating system's, which is left alone", a.Note)
			plan.Actions = append(plan.Actions, a)
			continue
		}
		plan.Warnings = append(plan.Warnings, model.Warning{Severity: model.SeverityWarn, Code: CodeDuplicateInstall,
			Package: name, Provider: target,
			Message: fmt.Sprintf("left %s to the operating system%s; no managed copy was installed. "+
				"'starpoint install %s %s' adds one, and provider = \"system\" on the entry keeps the system's without asking again",
				name, systemPath(a), name, target)})
	}
	return plan, nil
}

// joinNote puts note before an action's existing note, if it has one.
func joinNote(note, existing string) string {
	if existing == "" {
		return note
	}
	return note + "; " + existing
}
