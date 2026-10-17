package cli

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/report"
)

// change is one thing a command is about to do to the machine.
//
// Anything that touches the machine goes through this type. That is the rule: a
// command that installs, removes or updates software describes what it will do
// and waits for an answer, unless the answer was already given by --no-confirm or
// the configuration.
//
// A dry run never asks, because there is nothing to decline.
type change struct {
	// verb is what will happen, in the imperative: "Install".
	verb string
	// target is the package or software being changed.
	target string
	// via is the provider that will be asked.
	via string
	// detail is the one-line "why this provider" or version justification.
	detail string
	// after is what will have happened when it is done.
	after string
	// effects are what the change is expected to do to the machine. The
	// destructive ones are always shown before asking.
	effects []model.Effect
	// warnings concern this change.
	warnings []model.Warning
}

// confirmChanges describes changes and asks whether to proceed.
//
// The three outcomes are deliberately different:
//
//   - Not asking at all, because a dry run or --no-confirm. Nothing to do.
//   - Declining: nothing is changed, and the command says so rather than
//     reporting success.
//   - An inability to ask, because there is no terminal. Anything that modifies
//     the machine then fails instead of proceeding silently, which is the whole
//     point: a piped install must say it is proceeding unattended, not do it
//     quietly.
func (e *Env) confirmChanges(changes []change, opts engine.Options) error {
	if len(changes) == 0 {
		return nil
	}
	if opts.DryRun || opts.NoConfirm {
		return nil
	}
	if e.Confirm == nil {
		return fmt.Errorf(
			"%w: this would %s %s, but there is no terminal to confirm on; "+
				"pass --yes to proceed unattended",
			ErrUsage, strings.ToLower(changes[0].verb), changes[0].target)
	}

	fmt.Fprintln(e.promptOut(), e.palPrompt.Bold("About to change your machine:"))
	for _, c := range changes {
		fmt.Fprintf(e.promptOut(), "  %s %s", e.palPrompt.Bold(c.verb), c.target)
		if c.via != "" {
			fmt.Fprintf(e.promptOut(), " (%s)", c.via)
		}
		fmt.Fprintln(e.promptOut(), "")
		if c.detail != "" {
			fmt.Fprintf(e.promptOut(), "      %s\n", e.palPrompt.Dim(c.detail))
		}
		if c.after != "" {
			fmt.Fprintf(e.promptOut(), "      then: %s\n", e.palPrompt.Dim(c.after))
		}
		report.WriteEffects(e.promptOut(), e.palPrompt, c.effects, true, e.Verbose, "      ")
		for _, w := range c.warnings {
			if w.Severity == model.SeverityInfo && !e.Verbose {
				continue
			}
			fmt.Fprintf(e.promptOut(), "      %s [%s]: %s\n", report.SeverityLabel(e.palPrompt, w.Severity), w.Code, w.Message)
		}
	}
	if n := destructiveCount(changes); n > 0 {
		fmt.Fprintf(e.promptOut(), "\n%s\n",
			e.palPrompt.BoldYellow(fmt.Sprintf("%d change(s) marked ! remove or overwrite something that is there now.", n)))
	}
	fmt.Fprintln(e.promptOut(), "")

	ok, err := e.Confirm("Proceed?")
	e.confirmAsked = true
	e.lastConfirm = ok
	if err != nil {
		return err
	}
	if !ok {
		fmt.Fprintln(e.promptOut(), e.palPrompt.Dim("Nothing was changed."))
	}
	return nil
}

// promptOut is where questions and the descriptions before them go: stdout
// normally, stderr under --json so stdout stays one JSON document.
func (e *Env) promptOut() io.Writer {
	if e.jsonMode {
		return e.Stderr
	}
	return e.Stdout
}

// destructiveCount counts the destructive effects across changes.
func destructiveCount(changes []change) int {
	n := 0
	for _, c := range changes {
		for _, eff := range c.effects {
			if eff.Destructive {
				n++
			}
		}
	}
	return n
}

// declined reports whether the user was asked and said no.
//
// "Not asked" is not "refused". A command that was told not to ask — by
// --no-confirm, by the configuration, or because it is a dry run — must still
// carry out the work, so the distinction is recorded explicitly rather than
// inferred from a zero value.
func (e *Env) declined() bool { return e.confirmAsked && !e.lastConfirm }

// installChange describes what installing a package would do.
//
// It is asked for before the install, not after, because the whole reason to ask
// is that the answer might change what Starpoint does. Once the user has seen that
// the software is already present through Homebrew and is simply being recorded,
// the install turns into an adoption with no mutation at all — and it is worth
// telling them that instead of silently doing nothing.
func installChange(pkg model.Package, resolution model.Resolution, adopted *model.Installation, preview model.PlannedAction) change {
	verb := "Install"
	target := pkg.Spec()
	after := ""
	if adopted != nil {
		verb = "Record"
		after = "no software is installed or removed; the existing copy is recorded as owned by " +
			resolution.Provider
	}
	if resolution.Forced {
		after = strings.TrimSpace(after + " " +
			"the provider is the one you asked for, not the one Starpoint would choose")
	}
	return change{
		verb:     verb,
		target:   target,
		via:      resolution.Provider,
		detail:   resolution.Reason,
		after:    strings.TrimSpace(after),
		effects:  preview.Effects,
		warnings: preview.Warnings,
	}
}

// removeChange describes what removing a package would do.
func removeChange(pkg model.Package, resolution model.Resolution, preview model.PlannedAction) change {
	return change{
		verb:   "Remove",
		target: pkg.Spec(),
		via:    resolution.Provider,
		detail: "uninstalls through " + resolution.Provider +
			" and deletes the entry from the configuration",
		effects:  preview.Effects,
		warnings: preview.Warnings,
	}
}

// updateChange describes what updating a package would do.
func updateChange(pkg model.Package, resolution model.Resolution) change {
	return change{
		verb:   "Update",
		target: pkg.Spec(),
		via:    resolution.Provider,
		detail: "asks " + resolution.Provider + " for a newer version",
	}
}

// surveyNote returns the line to print when a survey found nothing, and whether
// it should be printed at all.
//
// "none found on installed providers" is deliberately not an error. A package can
// perfectly well be installable by a provider that does not have it yet — that is
// the normal case — and the honest statement is that no provider claims it, not
// that Starpoint cannot install it.
func surveyNote(s engine.Survey) (string, bool) {
	if s.Any() {
		return "", false
	}
	line := "none found on installed providers"
	if unaskable := s.Unaskable(); len(unaskable) > 0 {
		line += fmt.Sprintf(" (not asked: %s)", strings.Join(unaskable, ", "))
	}
	return line, true
}

// surveyDuplicates names the providers that have the software *other than* the
// one about to be chosen.
//
// The chosen one is excluded by name rather than by position, because the survey
// has no opinion about who should win — it reports what is there.
func surveyDuplicates(s engine.Survey, chosen string) (string, bool) {
	var others []string
	for _, name := range s.OwnerNames() {
		if name != chosen {
			others = append(others, name)
		}
	}
	if len(others) == 0 {
		return "", false
	}
	return "also present via " + strings.Join(others, ", "), true
}

// lookup asks every provider at once and reports what it found.
//
// This runs for an install because a package's real owner is usually something
// nobody asked about: `starpoint install ripgrep` on a machine where Homebrew owns
// it should say so, and asking all the providers at once is the only way to know
// that without waiting for each in turn.
func (e *Env) lookup(ctx context.Context, eng *engine.Engine, pkg model.Package, opts engine.Options, jsonMode bool) engine.Survey {
	s := eng.Survey(ctx, pkg, opts)
	if jsonMode {
		// A JSON consumer reads the survey from the payload; prose above it would
		// just be noise in the middle of a document.
		return s
	}
	if line, show := surveyNote(s); show {
		fmt.Fprintf(e.Stdout, "%s: %s\n", e.palOut.Bold(pkg.Name), e.palOut.Dim(line))
	}
	if line, show := surveyDuplicates(s, pkg.Provider); show {
		fmt.Fprintf(e.Stdout, "%s: %s\n", e.palOut.Bold(pkg.Name), e.palOut.Yellow(line))
	}
	return s
}
