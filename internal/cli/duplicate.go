package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/auditlog"
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
