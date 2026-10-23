package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/color"
	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/report"
)

// Exit codes. 0, 1 and 2 are the conventional success, failure and usage
// error. The refusals have codes of their own so a script can tell "Starpoint
// would not do this without being told it may" from "it tried and failed".
const (
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
	// ExitRefusedDestructive: an unattended run would delete or replace
	// something and --allow-destructive was not given. Nothing was changed.
	ExitRefusedDestructive = 3
	// ExitRefusedElevated: a changing command ran as root or elevated and
	// neither --allow-root nor an interactive confirmation allowed it.
	// Nothing was changed.
	ExitRefusedElevated = 4
	// ExitRefusedDecision: the command needs a decision only the user can
	// make (which of several providers owns something, whether a second
	// copy is wanted) and there was no one to ask. The refusal names the
	// candidates and the flag or argument that makes the decision. Nothing
	// was changed.
	ExitRefusedDecision = 5
)

// Error codes carried by a refusal, in JSON (`code`) and in human output.
const (
	CodeDestructiveUnattended = "destructive-unattended"
	CodeElevatedRefused       = "elevated-refused"
	// CodeOwnerAmbiguous: more than one provider has the software and
	// adopt was not told which owns it.
	CodeOwnerAmbiguous = "owner-ambiguous"
)

// RefusalError is a command Starpoint declined to run because a safety rule
// needs an explicit flag. It carries the plan it refused, so the caller sees
// exactly what would have happened; nothing was changed.
type RefusalError struct {
	Code    string
	Exit    int
	Message string
	// Command is the canonical command refused.
	Command string
	// Actions are the planned actions, with their effects and configuration
	// changes, that the command would have carried out.
	Actions []model.PlannedAction
	// Package and Candidates are the decision a decision refusal (exit 5)
	// needs: the software, and the providers to choose from.
	Package    string
	Candidates []string
}

func (e *RefusalError) Error() string { return e.Message }

// interactive reports whether someone at a terminal can answer questions.
//
// The program's main sets Interactive from whether stdin is a terminal. When
// it is nil (tests that wire a Confirm function), a configured Confirm stands
// for a user at a terminal.
func (e *Env) interactive() bool {
	if e.Interactive != nil {
		return e.Interactive()
	}
	return e.Confirm != nil
}

// unattended reports whether nobody will review this run's changes: --yes or
// --no-confirm (or no_confirm in the configuration) was given, or there is no
// terminal. A scripted answer piped into stdin is unattended too: it says yes
// without having seen the list.
func (e *Env) unattended(opts engine.Options) bool {
	return opts.NoConfirm || !e.interactive()
}

// guardDestructive refuses an unattended run that would delete or replace
// anything, unless --allow-destructive was given.
//
// An interactive run is not guarded here: its confirmation step already lists
// every destructive effect, marked !, before asking. A dry run changes
// nothing and is never refused.
func (e *Env) guardDestructive(command string, opts engine.Options, actions []model.PlannedAction) error {
	if opts.DryRun || e.allowDestructive || !e.unattended(opts) {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	for _, a := range actions {
		if !a.Destructive() {
			continue
		}
		name := a.State.Package.Name
		if name == "" {
			name = string(a.Action)
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	why := "--yes (or --no-confirm) was given"
	if !opts.NoConfirm {
		why = "there is no terminal to confirm on"
	}
	hint := "rerun with --allow-destructive"
	if !opts.NoConfirm && e.Confirm == nil {
		hint += " --yes"
	}
	msg := fmt.Sprintf("refusing to %s unattended: it would delete or replace something (%s) and %s. "+
		"Nothing was changed. Review the planned changes, then %s to allow it",
		command, strings.Join(names, ", "), why, hint)
	e.audit(auditlog.Entry{
		Event:       auditlog.EventGuard,
		Command:     command,
		Result:      "refused",
		Detail:      CodeDestructiveUnattended + ": " + msg,
		Destructive: true,
		Effects:     plannedEffects(actions),
	})
	return &RefusalError{
		Code:    CodeDestructiveUnattended,
		Exit:    ExitRefusedDestructive,
		Message: msg,
		Command: command,
		Actions: actions,
	}
}

// plannedEffects flattens the effects of a set of planned actions.
func plannedEffects(actions []model.PlannedAction) []model.Effect {
	var out []model.Effect
	for _, a := range actions {
		out = append(out, a.Effects...)
	}
	return out
}

// changeActions turns the change descriptions a command shows before asking
// into planned actions, for the guard and for a refusal's report.
func changeActions(action model.Action, previews []model.PlannedAction) []model.PlannedAction {
	out := make([]model.PlannedAction, 0, len(previews))
	for _, p := range previews {
		if p.Action == "" {
			p.Action = action
		}
		p.Outcome = model.OutcomePlanned
		out = append(out, p)
	}
	return out
}

// transitionActions describes the transitions a command is about to perform
// as planned actions, with the effects the confirmation step would show.
func transitionActions(ctx context.Context, eng *engine.Engine, ts []model.Transition) []model.PlannedAction {
	out := make([]model.PlannedAction, 0, len(ts))
	for _, t := range ts {
		a := engine.TransitionAction(t, nil, true)
		effects, warnings := eng.PlannedTransitionEffects(ctx, t)
		a.Effects = model.MergeEffects(a.Effects, effects...)
		// "stopped at planned" describes a move that ran and stalled; for a
		// plan it is noise.
		kept := a.Warnings[:0]
		for _, w := range a.Warnings {
			if w.Code != model.WarnTransitionIncomplete {
				kept = append(kept, w)
			}
		}
		a.Warnings = append(kept, warnings...)
		out = append(out, a)
	}
	return out
}

// reportRefusal writes a refusal to stderr: the coded error and, for a
// command that had a plan, every planned action and effect. Under --json it
// is one document in the error envelope, with the plan in the ActionResult
// shape every other command uses.
func reportRefusal(w io.Writer, p color.Palette, asJSON bool, r *RefusalError, runAs report.RunAsFunc) {
	if asJSON {
		doc := map[string]any{
			"schema_version": report.SchemaVersion,
			"error":          r.Message,
			"code":           r.Code,
			"exit_code":      r.Exit,
		}
		if r.Candidates != nil {
			doc["package"] = r.Package
			doc["candidates"] = r.Candidates
		}
		if r.Actions != nil {
			refused := report.RefusedPlan(r.Actions, runAs)
			refused["operation"] = r.Command
			doc["refused"] = refused
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(doc)
		return
	}
	fmt.Fprintf(w, "%s [%s]: %s\n", p.BoldRed("error"), p.BoldRed(r.Code), r.Message)
	if len(r.Candidates) > 0 {
		fmt.Fprintf(w, "  candidates for %s: %s\n", r.Package, strings.Join(r.Candidates, ", "))
	}
	if len(r.Actions) == 0 {
		return
	}
	fmt.Fprintln(w, "\n"+p.Bold("Planned changes (none were made; ! removes or overwrites something):"))
	for _, a := range r.Actions {
		line := fmt.Sprintf("  %s %s", p.Yellow(fmt.Sprintf("%-10s", a.Action)), a.State.Package.Name)
		if a.State.Res.Provider != "" {
			line += " via " + a.State.Res.Provider
		}
		fmt.Fprintln(w, line)
		report.WriteEffects(w, p, a.Effects, true, true, "      ")
	}
}

// asRefusal unwraps a refusal from an error chain.
func asRefusal(err error) (*RefusalError, bool) {
	var r *RefusalError
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}

// packageDelete is the planned record of uninstalling a package whose files
// could not be described in advance.
func packageDelete(name, providerName string) model.Effect {
	return model.Effect{
		Op: model.EffectDelete, Kind: model.EffectPackage, Name: name, Provider: providerName,
		Destructive: true, Evidence: model.EvidencePlanned,
		Detail: "uninstalls " + name,
	}
}
