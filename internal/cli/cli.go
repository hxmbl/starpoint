// Package cli implements the Starpoint command line.
//
// The commands are thin: they parse arguments, load configuration, call the
// engine, and render the result. No package-manager reasoning lives here.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/history"
	"github.com/hxmbl/starpoint/internal/locator"
	"github.com/hxmbl/starpoint/internal/lock"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/project"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers"
	"github.com/hxmbl/starpoint/internal/recovery"
	"github.com/hxmbl/starpoint/internal/report"
	"github.com/hxmbl/starpoint/internal/resolve"
	"github.com/hxmbl/starpoint/internal/retention"
	"github.com/hxmbl/starpoint/internal/tracking"
)

// Version is the build version, overridable at link time.
var Version = "0.1.0"

// ErrUsage signals that usage should be printed.
var ErrUsage = errors.New("usage")

// Env is the command's environment. Everything the CLI touches goes through it
// so tests can drive commands without a real terminal or a real machine.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	// WorkDir is the directory commands run in.
	WorkDir string
	// GlobalConfig overrides ~/.starpoint.toml.
	GlobalConfig string
	// Registry supplies providers. Nil means the built-in set.
	Registry *provider.Registry
	// Locator resolves paths. Nil means a real host locator.
	Locator *locator.Locator
	// Confirm asks the user a yes/no question. Nil means no interaction.
	Confirm func(question string) (bool, error)
	// Choose asks the user to pick from a list. Nil means no interaction.
	Choose func(question string, options []string) (string, error)
	// restrict narrows provider resolution, set from --provider.
	restrict []string
	// lastConfirm remembers the answer to the most recent Confirm call so a
	// caller can honour a refusal without being handed it.
	lastConfirm bool
	// confirmAsked records whether a confirmation actually happened. Without it a
	// command that was told not to ask would read as one that asked and was
	// refused, and would then decline to do the work it was told to do.
	confirmAsked bool
	// RunInDir runs a command in a directory with output streamed to the
	// given writers. Nil means the real command. Tests replace it so
	// `install .` can be exercised without npm or cargo.
	RunInDir func(ctx context.Context, dir string, stdout, stderr io.Writer, name string, args ...string) error
	// AuditLog is where Starpoint records what it did. Nil means the default
	// location under ~/.starpoint/logs. Tests point it at a temporary directory
	// so they can assert on what a command logged.
	AuditLog *auditlog.Log
	// newEngine is overridden in tests to inject a tracker
	newEngineFn func() *engine.Engine
}

// stateDir is where this run keeps its records: the audit log, the transition
// history, the run history and the mutation locks. It follows the global
// configuration path, so pointing Starpoint at a different configuration also
// points its records somewhere else.
func (e *Env) stateDir() string {
	path, err := e.globalPath()
	if err != nil {
		return ""
	}
	dir, err := config.StateDir(path)
	if err != nil {
		return ""
	}
	return dir
}

// auditLog is the log this run writes to.
func (e *Env) auditLog() *auditlog.Log {
	if e.AuditLog != nil {
		return e.AuditLog
	}
	dir := e.stateDir()
	if dir == "" {
		if home, err := auditlog.DefaultDir(); err == nil {
			dir = filepath.Dir(home)
		}
	}
	return auditlog.New(auditlog.PathIn(dir))
}

// audit records one entry in Starpoint's own log of what it did.
//
// It is best effort by design. A log that cannot be written is a problem worth
// reporting, but it must never be the reason Starpoint refuses to install or
// remove software: refusing the user's work because the audit trail was
// unwritable would make the tool worse at its actual job.
func (e *Env) audit(entry auditlog.Entry) {
	if entry.Directory == "" {
		entry.Directory = e.WorkDir
	}
	_ = e.auditLog().Record(entry)
}

// auditTransition records one ownership move, which is the operation that
// touches two providers at once and can therefore leave the machine mid-way.
func (e *Env) auditTransition(t model.Transition, err error) {
	result := string(t.Phase)
	if err != nil {
		result = "failed"
	}
	e.audit(auditlog.Entry{
		Event:    auditlog.EventAction,
		Command:  "transition",
		Action:   "relocate",
		Package:  t.Package.Name,
		Provider: t.To.Name,
		Result:   result,
		Detail:   errorDetail(err),
	})
}

// auditActions records the changes a command carried out. The engine's result
// already holds them, so this is a translation rather than a second record of
// the same events.
func (e *Env) auditActions(command string, dryRun bool, results ...*engine.Result) {
	result := ""
	if dryRun {
		result = "planned"
	}
	for _, r := range results {
		if r == nil {
			continue
		}
		for _, a := range r.Applied {
			outcome := result
			if outcome == "" {
				outcome = string(a.Action) + "d"
			}
			e.audit(auditlog.Entry{
				Event:    auditlog.EventAction,
				Command:  command,
				DryRun:   dryRun,
				Action:   string(a.Action),
				Package:  a.State.Package.Name,
				Provider: a.State.Res.Provider,
				Result:   outcome,
				Detail:   a.Note,
			})
		}
	}
}

func (e *Env) registry() *provider.Registry {
	if e.Registry != nil {
		return e.Registry
	}
	return providers.DefaultRegistry()
}

func (e *Env) loc() *locator.Locator {
	if e.Locator != nil {
		return e.Locator
	}
	return locator.New()
}

func (e *Env) newEngine() *engine.Engine {
	if e.newEngineFn != nil {
		return e.newEngineFn()
	}
	eng := engine.New(e.registry(), e.loc(), engine.WithStateDir(e.stateDir()))
	// Tracker will be set by tests if needed
	if err := eng.HistoryError(); err != nil {
		// Reporting this matters: without history, `transition --reverse` cannot
		// see what happened and no transition is recorded for later reversal.
		fmt.Fprintf(e.Stderr,
			"warning: transition history is unavailable (%v); transitions will not be recorded\n", err)
	}
	return eng
}

func (e *Env) globalPath() (string, error) {
	if e.GlobalConfig != "" {
		return e.GlobalConfig, nil
	}
	return config.DefaultGlobalPath()
}

func (e *Env) acquireMutationLock(path, operation string, allowUnsafeRecovery ...bool) (func(), error) {
	m, err := lock.New(filepath.Join(filepath.Dir(path), ".starpoint", "locks"))
	if err != nil {
		return nil, err
	}
	l, err := m.AcquirePath(path, operation)
	if err != nil {
		return nil, err
	}
	detector := recovery.ForConfig(path)
	ops, err := detector.Detect()
	if err != nil {
		_ = l.Release()
		return nil, fmt.Errorf("checking for interrupted operations: %w", err)
	}
	if unsafe := recovery.Unrecoverable(ops); len(unsafe) > 0 && (len(allowUnsafeRecovery) == 0 || !allowUnsafeRecovery[0]) {
		_ = l.Release()
		var details []string
		for _, op := range unsafe {
			details = append(details, fmt.Sprintf("%s: %s", op.Path, op.Reason))
		}
		return nil, fmt.Errorf("cannot safely continue until interrupted files are inspected:\n  %s", strings.Join(details, "\n  "))
	}
	if len(ops) > 0 {
		// Sole copies are excluded: a file that is the only copy of a user's
		// configuration is never cleaned up automatically.
		if safe := recovery.Discardable(recovery.Recoverable(ops)); len(safe) > 0 {
			if _, _, err := detector.Cleanup(safe); err != nil {
				_ = l.Release()
				return nil, fmt.Errorf("recovering interrupted operations: %w", err)
			}
		}
	}
	return func() { _ = l.Release() }, nil
}

// Run dispatches a command and returns its exit code.
func Run(ctx context.Context, env *Env, args []string) int {
	if len(args) == 0 {
		printUsage(env.Stderr)
		return 2
	}

	// Global flags are accepted anywhere on the line, because users write
	// `starpoint sync --json` and `starpoint --json sync` interchangeably and
	// neither should be a syntax error.
	var (
		asJSON     bool
		force      bool
		providers  []string
		contextDir string
	)
	rest, flagErr := extractGlobalFlags(args, &asJSON, &force, &providers, &contextDir)
	if contextDir != "" {
		env.WorkDir = contextDir
	}
	if flagErr != nil {
		fmt.Fprintf(env.Stderr, "error: %s\n", flagErr)
		printUsage(env.Stderr)
		return 2
	}
	if len(rest) == 0 {
		printUsage(env.Stdout)
		return 0
	}

	// Help and version only apply before the subcommand. After it they belong to
	// the subcommand: `install --version 1.2.3` pins a version, it does not
	// print Starpoint's own.
	for _, a := range leadingArgs(rest) {
		switch a {
		case "--help", "-h":
			printUsage(env.Stdout)
			return 0
		case "--version", "-V":
			fmt.Fprintf(env.Stdout, "starpoint %s\n", Version)
			return 0
		}
	}

	cmd, cmdArgs := rest[0], rest[1:]
	w := report.New(env.Stdout, asJSON)

	// `starpoint <command> --help` is help for that command, wherever the flag
	// sits. It must be decided before dispatch: a command that took it as an
	// argument would treat it as a package name.
	if canonicalCommand(cmd) != "" && wantsHelp(cmdArgs) {
		printCommandHelp(env.Stdout, cmd)
		return 0
	}

	var err error
	env.begin(cmd, cmdArgs)
	switch cmd {
	case "init":
		err = cmdInit(ctx, env, w, cmdArgs, force)
	case "sync":
		err = cmdSync(ctx, env, w, cmdArgs, providers)
	case "list", "ls":
		err = cmdList(ctx, env, w, cmdArgs)
	case "info", "show":
		err = cmdInfo(ctx, env, w, cmdArgs)
	case "explain":
		// explain reads the restriction from the environment, the way the
		// resolver input is built for every other command.
		env.restrict = append(env.restrict, providers...)
		err = cmdExplain(ctx, env, w, cmdArgs)
	case "doctor":
		err = cmdDoctor(ctx, env, w, cmdArgs)
	case "install", "add", "i":
		err = cmdInstall(ctx, env, w, cmdArgs, providers)
	case "remove", "uninstall", "rm":
		err = cmdRemove(ctx, env, w, cmdArgs, providers)
	case "transition", "move":
		err = cmdTransition(ctx, env, w, cmdArgs, providers)
	case "update", "upgrade":
		err = cmdUpdate(ctx, env, w, cmdArgs, providers)
	case "runs", "history":
		err = cmdRuns(ctx, env, w, cmdArgs)
	case "prune":
		err = cmdPrune(ctx, env, w, cmdArgs, providers)
	case "recover":
		err = cmdRecover(env, w, cmdArgs)
	case "logging", "logs":
		err = cmdLogging(env, w, cmdArgs)
	case "help":
		if len(cmdArgs) > 0 && canonicalCommand(cmdArgs[0]) != "" {
			printCommandHelp(env.Stdout, cmdArgs[0])
			return 0
		}
		printUsage(env.Stdout)
		return 0
	default:
		fmt.Fprintf(env.Stderr, "unknown command %q\n\n", cmd)
		printUsage(env.Stderr)
		return 2
	}

	if err != nil {
		if errors.Is(err, ErrUsage) {
			fmt.Fprintf(env.Stderr, "error: %s\n\n", err)
			printCommandHelp(env.Stderr, cmd)
			return env.finish(canonicalCommand(cmd), cmdArgs, 2, err)
		}
		if asJSON {
			enc := json.NewEncoder(env.Stderr)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]any{"schema_version": report.SchemaVersion, "error": err.Error()})
		} else {
			fmt.Fprintf(env.Stderr, "error: %s\n", err)
		}
		return env.finish(canonicalCommand(cmd), cmdArgs, 1, err)
	}
	return env.finish(canonicalCommand(cmd), cmdArgs, 0, nil)
}

// begin records that a command started and ages out expired records.
//
// Both happen on every command. Retention is what makes it safe to log
// everything: a log nobody prunes is a log that eventually fills the disk, at
// which point it is the tool's fault and it stops working.
func (e *Env) begin(command string, args []string) {
	canonical := canonicalCommand(command)
	if canonical == "" {
		return
	}
	e.applyRetention()
	e.audit(auditlog.Entry{
		Event:     auditlog.EventCommandStart,
		Command:   canonical,
		Args:      append([]string{}, args...),
		DryRun:    hasFlag(args, "--dry-run", "-n"),
		Directory: e.WorkDir,
	})
}

// finish records how a command ended and returns its exit code.
func (e *Env) finish(command string, args []string, code int, err error) int {
	if command != "" {
		result := "ok"
		if err != nil {
			result = "failed"
		}
		e.audit(auditlog.Entry{
			Event:     auditlog.EventCommandFinish,
			Command:   command,
			Args:      append([]string{}, args...),
			DryRun:    hasFlag(args, "--dry-run", "-n"),
			Exit:      code,
			Result:    result,
			Detail:    errorDetail(err),
			Directory: e.WorkDir,
		})
	}
	return code
}

// errorDetail is the message an audit entry carries, if any.
func errorDetail(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// applyRetention ages every store out by the configured window.
//
// It reads the global configuration only, and treats an unreadable or
// malformed file as "use the default" rather than as a failure: retention must
// never be the reason a command cannot run.
func (e *Env) applyRetention() {
	days := retention.DefaultDays
	if path, err := e.globalPath(); err == nil {
		if doc, err := config.Load(config.LoadOptions{GlobalPath: path}); err == nil {
			days = doc.Global.Raw.Settings.RetentionDays
		}
	}
	dir := e.stateDir()
	hist, _ := history.New(history.PathIn(dir))
	tracker, _ := tracking.New(tracking.PathIn(dir))
	rep := retention.Apply(retention.Options{
		Days:    days,
		Audit:   e.auditLog(),
		History: hist,
		Runs:    tracker,
	})
	if rep.Removed() {
		e.audit(auditlog.Entry{
			Event:   auditlog.EventRetention,
			Command: "retain",
			Result:  "removed",
			Detail:  errorDetail(rep.Error()),
		})
	}
	if rep.Error() != nil {
		// A retention problem is worth saying out loud, but it is a warning:
		// the command the user asked for has already been decided by now.
		e.audit(auditlog.Entry{
			Event:  auditlog.EventRetention,
			Result: "incomplete",
			Detail: errorDetail(rep.Error()),
		})
		fmt.Fprintf(e.Stderr, "warning: %s\n", rep.Error())
	}
}

// hasFlag reports whether args contain one of the given flags.
func hasFlag(args []string, flags ...string) bool {
	for _, a := range args {
		for _, f := range flags {
			if a == f {
				return true
			}
		}
	}
	return false
}

// leadingArgs returns the arguments before the first bare word, which is where
// the subcommand starts.
func leadingArgs(args []string) []string {
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			return args[:i]
		}
	}
	return args
}

// extractGlobalFlags pulls the global options out of an argument list and
// returns what remains: the subcommand and its own arguments.
func extractGlobalFlags(args []string, asJSON, force *bool, providers *[]string, contextDir *string) ([]string, error) {
	var rest []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			*asJSON = true
		case "--force", "-f":
			*force = true
		case "--provider", "-p":
			if i+1 >= len(args) {
				return nil, errors.New("--provider needs a value")
			}
			*providers = append(*providers, args[i+1])
			i++
		case "--context":
			if i+1 >= len(args) {
				return nil, errors.New("--context needs a value")
			}
			*contextDir = args[i+1]
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, nil
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `starpoint — declarative orchestration for the software on your machine

Usage:
  starpoint <command> [options]
  starpoint <command> --help      Help for one command

Commands:
  init [--force]                 Create the global configuration (~/.starpoint.toml)
  sync [--dry-run] [--no-confirm]
                                 Make installed state match the configuration
  list, ls                       Show managed software, where it is, and who owns it
  info, show <package>           Show one package's state in detail
  explain <package>              Show how Starpoint decides which provider owns a package
  doctor [--all]                 Inspect Starpoint, configuration, providers, ownership, PATH
  install, add <package>         Install software and record it in the configuration
  install .                      Install the current project's packages and dependencies
  remove, uninstall <package>    Remove software and delete it from the configuration
  transition, move [pkg...]      Move ownership between providers, or show pending moves
                                 (--reverse undoes the last move of a package)
  update, upgrade [--self]       Update managed software; asks before moving past a pin
                                 (--self also updates Starpoint through its provider)
  runs, history                  Show execution history of package operations
  prune [--dry-run] [--no-confirm]
                                 Remove orphaned user-scoped software
  recover [--discard]            Inspect, and with --discard clear, files left by an
                                 interrupted run
  logging [prune|config]        Show, age out, or configure Starpoint's own logging
                                 (alias: logs)
  help [command]                 Show this help, or help for one command

A provider may be named after the package names, or with --provider:
  starpoint install ripgrep brew
  starpoint install brew ripgrep
  starpoint install ripgrep --provider brew

A version may be written inline, the way package managers write it:
  starpoint install ripgrep@14.1.0 brew
  starpoint install @types/node@5.0.0 npm

Global options:
  --json               Machine-readable output
  --provider <name>    Restrict automatic provider choice (repeatable)
  --force              Used by 'init' to overwrite an existing configuration
  --context <dir>      Override working directory for context detection
  --record             Force recording execution even if auto-detection fails
  --version            Print the version
  --help               Show this help

Anything that installs, removes or updates software asks first. Answer with
--no-confirm to skip the question; a stream that provides no answer at all is
refused rather than assumed. --dry-run shows what would happen and changes
nothing.

Examples:
  starpoint init
  starpoint install ruff
  starpoint install ripgrep@14.1.0 brew
  starpoint install .
  starpoint sync --dry-run
  starpoint update --dry-run
  starpoint transition
  starpoint list --json
  starpoint explain ruff
  starpoint doctor --all
  starpoint runs --package ruff
`)
}

// cmdInit creates the global configuration.
func cmdInit(ctx context.Context, env *Env, w *report.Writer, args []string, force bool) error {
	for _, a := range args {
		return fmt.Errorf("%w: unknown argument %q for init", ErrUsage, a)
	}
	path, err := env.globalPath()
	if err != nil {
		return err
	}
	unlock, err := env.acquireMutationLock(path, "init", force)
	if err != nil {
		return err
	}
	defer unlock()
	existing, _, err := config.LoadFile(path, false)
	if err != nil && !force {
		return err
	}
	if err != nil {
		existing = config.File{Path: path, Raw: config.Raw{Schema: config.SchemaVersion}}
	}
	if existing.Exists && !force {
		// Declining is a failure from the caller's point of view: a script that
		// runs `starpoint init` expecting a fresh configuration must be able to
		// tell that it did not get one.
		return fmt.Errorf("%s already exists; use 'starpoint init --force' to replace it", path)
	}

	raw := existing.Raw
	if raw.Schema == 0 {
		raw.Schema = config.SchemaVersion
	}
	if err := config.Write(path, raw); err != nil {
		return err
	}
	if err := w.Init(path, !existing.Exists, existing.Exists); err != nil {
		return err
	}
	if !w.JSON() {
		// The documentation is printed rather than written into the file. A
		// TOML round trip cannot preserve comments, so a commented file would
		// lose its own documentation the first time Starpoint recorded a
		// provider decision — which is the first thing anyone does with it.
		fmt.Fprint(env.Stdout, "\n"+config.RenderTemplate())
	}
	return nil
}

// cmdSync converges the machine onto the configuration.
func cmdSync(ctx context.Context, env *Env, w *report.Writer, args []string, allowed []string) error {
	var (
		dryRun    bool
		noConfirm bool
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run", "-n":
			dryRun = true
		case "--no-confirm", "-y":
			noConfirm = true
		default:
			return fmt.Errorf("%w: unknown option %q for sync", ErrUsage, args[i])
		}
	}

	doc, eng, opts, proj, err := env.setup(ctx, allowed)
	if err != nil {
		return err
	}
	if !dryRun {
		path, _ := env.globalPath()
		unlock, lockErr := env.acquireMutationLock(path, "sync")
		if lockErr != nil {
			return lockErr
		}
		defer unlock()
	}
	if !doc.Global.Exists && len(doc.Global.Raw.Packages) == 0 {
		w.Warn("no configuration found; run 'starpoint init' first")
	}

	plan, _, err := eng.Plan(ctx, doc, opts)
	if err != nil {
		return err
	}

	if !dryRun && !noConfirm && len(plan.Actions) > 0 && env.Confirm != nil {
		ok, err := confirmPlan(env, plan)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(env.Stdout, "Nothing was changed.")
			return w.Sync(&engine.Result{DryRun: true, States: plan.States}, plan, proj)
		}
		noConfirm = true
	}
	opts.NoConfirm = true
	opts.DryRun = dryRun

	res, applyErr := eng.Apply(ctx, doc, plan, opts)
	if applyErr != nil {
		// Still report what did happen; partial progress is real progress.
		_ = w.Sync(res, plan, proj)
		return applyErr
	}
	return w.Sync(res, plan, proj)
}

// confirmPlan shows the user exactly what is about to happen.
func confirmPlan(env *Env, plan model.Plan) (bool, error) {
	fmt.Fprintln(env.Stdout, "The following changes will be made:")
	for _, a := range plan.Actions {
		line := fmt.Sprintf("  %-10s %s", a.Action, a.State.Package.Name)
		if a.State.Res.Provider != "" {
			line += " via " + a.State.Res.Provider
		}
		fmt.Fprintln(env.Stdout, line)
		if a.Note != "" {
			fmt.Fprintf(env.Stdout, "             %s\n", a.Note)
		}
	}
	return env.Confirm("Proceed?")
}

// cmdList shows managed software.
func cmdList(ctx context.Context, env *Env, w *report.Writer, args []string) error {
	var filter string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--provider":
			if i+1 >= len(args) {
				return fmt.Errorf("%w: --provider needs a value", ErrUsage)
			}
			filter = args[i+1]
			i++
		case "--managed", "-m":
			filter = "__managed__"
		default:
			return fmt.Errorf("%w: unknown option %q for list", ErrUsage, args[i])
		}
	}
	doc, eng, opts, _, err := env.setup(ctx, nil)
	if err != nil {
		return err
	}
	plan, _, err := eng.Plan(ctx, doc, opts)
	if err != nil {
		return err
	}
	states := plan.States
	if filter == "__managed__" {
		states = filterManaged(states)
	} else if filter != "" {
		states = filterProvider(states, filter)
	}
	return w.List(states, env.loc().OnPath())
}

func filterManaged(states []model.State) []model.State {
	var out []model.State
	for _, st := range states {
		if st.Res.Forced || st.Package.Provider != "" {
			out = append(out, st)
		}
	}
	return out
}

func filterProvider(states []model.State, name string) []model.State {
	var out []model.State
	for _, st := range states {
		if st.Res.Provider == name {
			out = append(out, st)
		}
	}
	return out
}

// cmdInfo shows one package in detail.
func cmdInfo(ctx context.Context, env *Env, w *report.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: info needs a package name", ErrUsage)
	}
	name := args[0]
	if err := rejectOption("info", name); err != nil {
		return err
	}
	doc, eng, opts, _, err := env.setup(ctx, nil)
	if err != nil {
		return err
	}
	plan, _, err := eng.Plan(ctx, doc, opts)
	if err != nil {
		return err
	}
	for _, st := range plan.States {
		if st.Package.Name != name {
			continue
		}
		// One response, not several: a pending ownership move is the most
		// important thing to know about a package, so it travels alongside the
		// package rather than as a second JSON document.
		extra := map[string]any{}
		if st.Status == model.StatusProviderDiffers {
			extra["transition"] = eng.TransitionFor(ctx, st, opts)
		}
		return w.Info(st, extra)
	}
	// The software may be on the machine without being in the configuration.
	// Answering that is more useful than failing.
	//
	// Every provider is asked, not just the first one to answer. A sequential
	// lookup that stops at the first hit would name one owner of software that
	// two providers have, which is the same blindness `install` used to have and
	// is exactly what a user asking "where did this come from?" needs to avoid.
	survey := eng.Survey(ctx, model.Package{Name: name}, opts)
	owners := survey.Owners()
	// The desired provider is what `starpoint add` would record, which is the
	// same decision explain shows. Reporting the current owner here instead
	// made info and explain contradict each other.
	desired := eng.Decide(ctx, doc, model.Package{Name: name}, opts)
	adopt := "run 'starpoint add " + name + "' to record it"
	if desired.Provider != "" {
		adopt += " (it would be recorded with " + desired.Provider + ")"
	}
	if len(owners) > 0 {
		current := *owners[0].Installation
		st := model.State{
			Package: model.Package{Name: name},
			Res:     desired,
			Current: &current,
			Status:  model.StatusUnmanaged,
			Notes: []string{
				"installed but not managed by Starpoint",
				adopt,
			},
		}
		if len(owners) > 1 {
			st.Notes = append(st.Notes, "also present via "+joinProviders(owners[1:]))
		}
		return w.Info(st, map[string]any{"lookup": survey})
	}

	// A provider inventory came up empty; path attribution is the last word.
	found := env.loc().Locate(name)
	if found.Path != "" {
		return w.Info(model.State{
			Package: model.Package{Name: name},
			Res:     desired,
			Current: &model.Installation{Name: name, Path: found.Path},
			Status:  model.StatusUnmanaged,
			Notes: []string{
				"present at " + found.Path + " but Starpoint cannot tell which provider installed it",
				"run 'starpoint add " + name + " --provider <name>' to record ownership",
			},
		}, map[string]any{"lookup": survey})
	}

	// Nothing anywhere. The refusal says which providers were asked, because
	// "not found" from a tool that could not ask some of its own providers is a
	// different claim from "not found".
	line, _ := surveyNote(survey)
	return fmt.Errorf("%s is not in the configuration and %s", name, line)
}

// joinProviders names a run of providers.
func joinProviders(fs []engine.Finding) string {
	names := make([]string, 0, len(fs))
	for _, f := range fs {
		names = append(names, f.Provider)
	}
	return strings.Join(names, ", ")
}

// cmdDoctor inspects everything.
func cmdDoctor(ctx context.Context, env *Env, w *report.Writer, args []string) error {
	showAll := false
	for _, a := range args {
		switch a {
		case "--all", "-a":
			showAll = true
		default:
			return fmt.Errorf("%w: unknown option %q for doctor", ErrUsage, a)
		}
	}
	globalPath, err := env.globalPath()
	if err != nil {
		return err
	}
	doc, err := config.Load(config.LoadOptions{GlobalPath: globalPath, Dir: env.WorkDir, DiscoverProjects: true})
	if err != nil {
		return err
	}
	reg := env.registry()
	eng := engine.New(reg, env.loc(), engine.WithStateDir(env.stateDir()))

	rep := report.DoctorReport{
		Starpoint:     report.StarpointInfo{Version: Version},
		Providers:     []report.ProviderHealth{},
		PATHInventory: env.loc().OnPath(),
		ShowAllPATH:   showAll,
		Healthy:       true,
	}
	rep.PATHSummary = locator.Summarize(rep.PATHInventory)
	rep.Config = report.DocumentInfo(doc, globalPath, isWritable(globalPath))
	for _, file := range doc.Projects {
		rep.Config.Files = append(rep.Config.Files, file.Path)
	}
	if detector := recovery.ForConfig(globalPath); detector != nil {
		ops, detectErr := detector.Detect()
		if detectErr != nil {
			rep.Problems = append(rep.Problems, model.Warning{Severity: model.SeverityWarn, Message: "could not inspect interrupted operations: " + detectErr.Error()})
		} else {
			for _, op := range ops {
				severity, advice := model.SeverityInfo, "run a normal Starpoint operation to clean it up"
				if !op.Recoverable {
					severity, advice = model.SeverityWarn, "preserve this file and inspect it before manual recovery"
				}
				rep.Problems = append(rep.Problems, model.Warning{Severity: severity, Message: fmt.Sprintf("interrupted %s at %s: %s; %s", op.Type, op.Path, op.Reason, advice)})
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		rep.Starpoint.Path = exe
		// Report who installed Starpoint, so `doctor` can point at the right
		// provider for `starpoint update --self`.
		if found := env.loc().Locate(filepath.Base(exe)); found.Installation != nil {
			rep.Starpoint.Provider = found.Installation.Provider
		}
	}

	for _, h := range reg.Available(ctx) {
		ph := report.ProviderHealth{
			Name:         h.Name,
			Description:  h.Description,
			Kind:         h.Kind,
			Available:    h.Available,
			Reason:       h.Reason,
			Binary:       h.Binary,
			Version:      h.Version,
			Capabilities: h.Capabilities,
		}
		rep.Providers = append(rep.Providers, ph)
	}
	sort.Slice(rep.Providers, func(i, j int) bool { return rep.Providers[i].Name < rep.Providers[j].Name })

	writable := isWritable(globalPath)
	rep.Config = report.DocumentInfo(doc, globalPath, writable)
	if len(doc.Projects) > 0 {
		rep.Config.ProjectFile = doc.Projects[0].Path
	}
	if !doc.Global.Exists {
		rep.Problems = append(rep.Problems, model.Warning{
			Severity: model.SeverityWarn,
			Message:  "no global configuration found at " + globalPath + "; run 'starpoint init'",
		})
	}
	if !writable {
		rep.Problems = append(rep.Problems, model.Warning{
			Severity: model.SeverityError,
			Message:  "the configuration file is not writable: " + globalPath,
		})
	}
	for _, warn := range doc.Warnings {
		rep.Problems = append(rep.Problems, model.Warning{Severity: model.SeverityWarn, Message: warn})
	}

	if len(doc.Packages()) > 0 {
		plan, warnings, err := eng.Plan(ctx, doc, engine.Options{Dir: env.WorkDir})
		if err != nil {
			return err
		}
		rep.Ownership = plan.States
		for _, warn := range warnings {
			if warn.Severity == model.SeverityError || warn.Severity == model.SeverityWarn {
				rep.Problems = append(rep.Problems, warn)
			}
		}
		// Duplicated ownership is a real inconsistency worth naming: the same
		// software provided by more than one provider is how a machine ends up
		// with three Pythons and no clear owner.
		for _, dup := range eng.Duplicates(ctx, doc, engine.Options{Dir: env.WorkDir}) {
			rep.Problems = append(rep.Problems, model.Warning{
				Severity: model.SeverityWarn,
				Package:  dup.Package,
				Provider: dup.Owner,
				Message: fmt.Sprintf("%s is also provided by %s at %s; decide which copy to keep",
					dup.Package, dup.Extra.Provider, dup.Extra.Path),
			})
		}
		for _, t := range eng.IncompleteTransitions(ctx, doc, engine.Options{Dir: env.WorkDir}) {
			severity := model.SeverityWarn
			if t.Phase == model.TransitionFailed {
				severity = model.SeverityError
			}
			rep.Problems = append(rep.Problems, model.Warning{
				Severity: severity,
				Package:  t.Package.Name,
				Provider: t.To.Name,
				Message: fmt.Sprintf("provider transition is %s: %s. %s%s",
					t.Phase, t.Package.Name, t.Note, nextClause(t.Advice)),
			})
		}
		for _, t := range eng.Transitions(ctx, doc, engine.Options{Dir: env.WorkDir}) {
			if t.Phase != model.TransitionCompleted || t.LeftBehindReason == "" {
				continue
			}
			rep.Problems = append(rep.Problems, model.Warning{
				Severity: model.SeverityWarn,
				Package:  t.Package.Name,
				Provider: t.From.Name,
				Message: fmt.Sprintf("ownership moved to %s but the %s copy was left in place (%s). %s",
					t.To.Name, t.From.Name, t.LeftBehindReason, nextClause(t.Advice)),
			})
		}
		for _, shadow := range eng.Shadowed(ctx, doc, engine.Options{Dir: env.WorkDir}) {
			rep.Problems = append(rep.Problems, model.Warning{
				Severity: model.SeverityError,
				Package:  shadow.Package,
				Provider: shadow.Managed.Provider,
				Message: fmt.Sprintf(
					"%s resolves to %s on PATH, but the %s copy Starpoint manages is %s; running %s will use the other one",
					shadow.Package, shadow.OnPath, shadow.Managed.Provider, shadow.Managed.Path, shadow.Package),
			})
		}
		for _, st := range rep.Ownership {
			switch st.Status {
			case model.StatusMissing:
				rep.Problems = append(rep.Problems, model.Warning{
					Severity: model.SeverityWarn,
					Package:  st.Package.Name,
					Message:  "declared in the configuration but not installed",
				})
			case model.StatusUnpinnedConflict:
				rep.Problems = append(rep.Problems, model.Warning{
					Severity: model.SeverityError,
					Package:  st.Package.Name,
					Provider: st.Res.Provider,
					Message: fmt.Sprintf("configuration pins %s but provider %s cannot install that version",
						st.Package.Version, st.Res.Provider),
				})
			}
		}
	}
	for _, p := range rep.Problems {
		if p.Severity != model.SeverityInfo {
			rep.Healthy = false
		}
	}
	return w.Doctor(rep)
}

// cmdExplain shows the provider-resolution reasoning chain for a package.
//
// It asks the engine for the same decision install and info use, so the three
// can never disagree about which provider a package belongs to.
func cmdExplain(ctx context.Context, env *Env, w *report.Writer, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: explain needs a package name", ErrUsage)
	}
	name := args[0]
	if err := rejectOption("explain", name); err != nil {
		return err
	}
	doc, eng, opts, _, err := env.setup(ctx, nil)
	if err != nil {
		return err
	}
	pkg, inConfig := doc.Package(name)
	if !inConfig {
		pkg = model.ParseSpec(name)
	}
	return w.Explain(eng.Explain(ctx, doc, pkg, opts))
}

// looksLikeVersion reports whether a name is a bare version number such as
// "1.50.0". No package manager has a package called that, and sending one to a
// package manager produces a confusing failure instead of an obvious one.
func looksLikeVersion(name string) bool {
	if name == "" || strings.ContainsAny(name, "/-_@") {
		return false
	}
	for _, part := range strings.Split(name, ".") {
		if part == "" {
			return false
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

// nextClause turns advice into a sentence fragment, or nothing.
func nextClause(advice string) string {
	if advice == "" {
		return ""
	}
	return "Next: " + advice + "."
}

// projectEcosystem maps a detected project to the provider ecosystem that owns
// its tooling. This is the only place the project layer and the provider layer
// meet.
func projectEcosystem(kind project.Kind) provider.Kind {
	switch kind {
	case project.KindGo:
		return provider.KindGo
	case project.KindRust:
		return provider.KindRust
	case project.KindNode:
		return provider.KindNode
	case project.KindPython:
		return provider.KindPython
	case project.KindRuby:
		return provider.KindRuby
	default:
		return ""
	}
}

func isWritable(path string) bool {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		if os.IsNotExist(err) {
			dir := filepath.Dir(path)
			info, derr := os.Stat(dir)
			if derr != nil {
				return false
			}
			return info.IsDir()
		}
		return false
	}
	f.Close()
	return true
}

// cmdInstall installs software and records it.
func cmdInstall(ctx context.Context, env *Env, w *report.Writer, args []string, allowed []string) error {
	// A bare provider may sit at either end of the argument list; `--provider`
	// remains the unambiguous spelling. This runs before the flag loop so the
	// provider is never mistaken for a package name.
	named, args, err := env.splitProviderArg(args, allowed)
	if err != nil {
		return err
	}

	var (
		dryRun    bool
		noConfirm bool
		version   string
		channel   string
		asProject bool
	)
	var names []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run", "-n":
			dryRun = true
		case "--no-confirm", "-y", "--yes":
			noConfirm = true
		case "--version", "-V":
			if i+1 >= len(args) {
				return fmt.Errorf("%w: --version needs a value", ErrUsage)
			}
			version = args[i+1]
			i++
		case "--channel":
			if i+1 >= len(args) {
				return fmt.Errorf("%w: --channel needs a value", ErrUsage)
			}
			channel = args[i+1]
			i++
		case "--project":
			asProject = true
		default:
			if err := rejectOption("install", args[i]); err != nil {
				return err
			}
			names = append(names, args[i])
		}
	}

	if len(names) == 0 {
		return fmt.Errorf("%w: install needs a package name", ErrUsage)
	}

	doc, eng, opts, proj, err := env.setup(ctx, allowed)
	if err != nil {
		return err
	}
	if !dryRun {
		path, _ := env.globalPath()
		unlock, lockErr := env.acquireMutationLock(path, "install")
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

	// `starpoint install .` is "install what this project needs", and it has no
	// package name to look up or ask about.
	if len(names) == 1 && (names[0] == "." || names[0] == "./") && named == "" {
		res, deps, err := installProject(ctx, env, eng, doc, opts, proj, version, channel, w.JSON())
		if err != nil {
			if w.JSON() && res != nil {
				_ = w.Render(map[string]any{"operations": []*engine.Result{res}, "dependencies": deps})
			}
			return err
		}
		env.auditActions("install", dryRun, res)
		if w.JSON() {
			return w.Render(map[string]any{"operations": []*engine.Result{res}, "dependencies": deps})
		}
		return reportInstall(w, []*engine.Result{res}, nil)
	}

	type job struct {
		pkg    model.Package
		change change
		survey engine.Survey
	}
	var jobs []job

	for _, raw := range names {
		if looksLikeVersion(raw) {
			return fmt.Errorf(
				"%s looks like a version number, not a package name. "+
					"Name the software and pin the version instead, for example: "+
					"'starpoint install <package> --version %s', or 'starpoint install stable --version %s' "+
					"for a release channel's toolchain", raw, raw, raw)
		}

		// The inline form the package managers use: `fastfetch@1.2.3`, or
		// `@types/node@5.0.0`. An explicit --version still wins.
		pkg := model.ParseSpec(raw)
		if version != "" {
			pkg.Version = version
		}
		pkg.Channel = channel
		pkg.Provider = singleProvider(opts.Allowed)
		if pkg.Provider != "" {
			// The user named the provider for this command. That is a human
			// decision and is recorded as one, not as something the
			// configuration said.
			pkg.Reason = "chosen by the user on the command line"
		}
		if asProject {
			pkg.Scope = model.ScopeProject
		}

		// Ask everyone at once. The point is not speed: it is that "no provider
		// claims this" and "one provider claims it" are different answers, and only
		// asking everyone tells them apart.
		survey := env.lookup(ctx, eng, pkg, opts, w.JSON())

		// An ambiguous provider choice is worth one question, not a guess. With no
		// way to ask, the command fails rather than picking one silently.
		if pkg.Provider == "" {
			chosen, err := env.askProvider(ctx, eng, doc, opts, pkg)
			if err != nil {
				return err
			}
			if chosen != "" {
				pkg.Provider = chosen
				pkg.Reason = "provider chosen interactively"
			}
		}

		resolution, adopted, err := eng.Preview(ctx, doc, pkg, opts)
		if err != nil {
			return err
		}
		jobs = append(jobs, job{pkg: pkg, change: installChange(pkg, resolution, adopted), survey: survey})
	}

	// One question for the whole command, describing every change it would make.
	// Asking per package would make `install a b c` three interruptions.
	changes := make([]change, 0, len(jobs))
	for _, j := range jobs {
		changes = append(changes, j.change)
	}
	if err := env.confirmChanges(changes, opts); err != nil {
		return err
	}
	if env.declined() {
		return nil
	}

	var results []*engine.Result
	surveys := make([]engine.Survey, 0, len(jobs))
	for _, j := range jobs {
		surveys = append(surveys, j.survey)
		res, err := eng.Add(ctx, doc, j.pkg, opts)
		if err != nil {
			return err
		}
		// Record execution in tracking system
		provider := j.pkg.Provider
		if provider == "" && len(res.Applied) > 0 {
			provider = res.Applied[0].State.Res.Provider
		}
		version := j.pkg.Version
		if version == "" && len(res.Applied) > 0 && res.Applied[0].State.Current != nil {
			version = res.Applied[0].State.Current.Version
		}
		// Tracking is now handled in the engine
		results = append(results, res)
	}
	env.auditActions("install", dryRun, results...)
	return reportInstall(w, results, surveys)
}

// singleProvider returns the one provider an option set names, or empty when the
// choice is still open.
func singleProvider(allowed []string) string {
	if len(allowed) == 1 {
		return allowed[0]
	}
	return ""
}

// reportInstall renders install results, with the provider lookup that preceded
// them. The lookup travels with the results because "which providers have this"
// is the evidence behind the choice, and a JSON consumer has no other way to see
// it.
func reportInstall(w *report.Writer, results []*engine.Result, surveys []engine.Survey) error {
	if w.JSON() {
		return w.Render(map[string]any{"operations": results, "lookup": surveys})
	}
	for _, r := range results {
		if err := w.Result("install", r); err != nil {
			return err
		}
	}
	return nil
}

// askProvider presents the ambiguous candidates to the user.
func (e *Env) askProvider(ctx context.Context, eng *engine.Engine, doc *config.Document, opts engine.Options, pkg model.Package) (string, error) {
	dec := eng.Decide(ctx, doc, pkg, opts)
	if !dec.Ambiguous || len(dec.Alternatives) == 0 {
		// Nothing worth asking about: leave the decision to the resolver so the
		// recorded reason explains itself.
		return "", nil
	}
	options := append([]string{dec.Provider}, dec.Alternatives...)
	if e.Choose == nil {
		return "", fmt.Errorf(
			"%s could be provided by %s and there is no terminal to ask on; rerun with --provider <name>",
			pkg.Name, strings.Join(options, " or "))
	}
	question := fmt.Sprintf("%s could be provided by %s. Which provider should own it?",
		pkg.Name, strings.Join(options, ", "))
	return e.Choose(question, options)
}

// dependencyReport is what `install .` did about the project's dependencies.
type dependencyReport struct {
	// Command is the ecosystem command chosen, nil when none applies.
	Command *project.DependencyInstall `json:"command,omitempty"`
	// Skipped explains why nothing ran.
	Skipped string `json:"skipped,omitempty"`
	// Ran reports whether the command was run and succeeded.
	Ran bool `json:"ran"`
	// DryRun echoes the mode.
	DryRun bool `json:"dry_run"`
}

// installProject handles `starpoint install .`.
//
// "Install what this project needs" is three things, in order:
//
//  1. The packages the project's own .starpoint.toml declares.
//  2. The toolchain the ecosystem expects (node, cargo, go, ...). It is recorded
//     when its owner can be identified, never installed: inferring "this is a
//     Rust project, so install rustc" from a Cargo.toml is a decision with
//     consequences far beyond the project. It is recorded in the project's
//     .starpoint.toml (created if needed), because the project is what needs
//     it; the global configuration stays the user's own statement of intent.
//  3. The project's dependencies, through the ecosystem's own installer chosen
//     from the files the project has (npm ci, pnpm install, uv sync, cargo
//     fetch, ...). Starpoint does not resolve dependencies itself.
//
// The dependency command is described and confirmed before anything changes,
// so declining leaves the machine and every configuration file untouched.
func installProject(ctx context.Context, env *Env, eng *engine.Engine, doc *config.Document, opts engine.Options,
	proj *project.Info, version, channel string, asJSON bool) (*engine.Result, *dependencyReport, error) {
	if proj == nil || proj.Kind == project.KindUnknown {
		return nil, nil, fmt.Errorf("no project detected here; run 'starpoint install <package>' to name software explicitly")
	}
	// Narration goes to stderr under --json so stdout stays one JSON document.
	out := env.Stdout
	if asJSON {
		out = env.Stderr
	}
	fmt.Fprintf(out, "Project: %s project at %s (via %s)\n", proj.Kind, proj.Root, proj.Marker)

	result := &engine.Result{DryRun: opts.DryRun}
	projectConfig := filepath.Join(proj.Root, config.ProjectFileName)

	deps, why := proj.Dependencies(osexec.LookPath)
	report := &dependencyReport{Command: deps, Skipped: why, DryRun: opts.DryRun}
	if deps != nil {
		if err := env.confirmChanges([]change{{
			verb:   "Run",
			target: deps.String(),
			detail: "in " + deps.Dir + " (" + deps.Reason + ")",
			after:  "the project's dependencies are installed by " + deps.Tool + "; the toolchain is recorded in " + projectConfig,
		}}, opts); err != nil {
			return nil, nil, err
		}
		if env.declined() {
			report.Skipped = "declined"
			return result, report, nil
		}
	}

	// Everything the project has declared is what it wants. Install it.
	declared, _ := doc.ProjectFileFor(proj.Root)
	if !proj.HasProjectConfig {
		fmt.Fprintf(out, "No %s yet; add entries to declare what this project needs.\n", projectConfig)
	} else {
		for _, pkg := range declared.Raw.Packages {
			if version != "" {
				pkg.Version = version
			}
			if channel != "" {
				pkg.Channel = channel
			}
			res, err := eng.Add(ctx, doc, pkg, opts)
			if err != nil {
				return result, report, err
			}
			result.Applied = append(result.Applied, res.Applied...)
			result.ConfigChanged = result.ConfigChanged || res.ConfigChanged
			result.ConfigPath = res.ConfigPath
		}
		if len(declared.Raw.Packages) == 0 {
			fmt.Fprintf(out, "%s declares no packages yet.\n", projectConfig)
		}
	}

	// The toolchain the ecosystem expects is reported, and adopted into the
	// project's configuration when its owner can be identified.
	for _, tool := range proj.ExpectedTools() {
		found := env.loc().Locate(tool.Name)
		switch {
		case found.Installation == nil && found.Path != "":
			// Present but unattributable. Starpoint records what it can attribute;
			// claiming ownership of an unexplained binary would put a lie in the
			// source of truth.
			fmt.Fprintf(out, "  %s: %s (owner unknown; add it with --provider if you want it managed)\n",
				tool.Name, found.Path)
		case found.Installation != nil:
			fmt.Fprintf(out, "  %s: %s (%s)\n", tool.Name, found.Installation.Provider, found.Path)
			if _, managed := doc.Package(tool.Name); managed {
				continue
			}
			if opts.DryRun {
				fmt.Fprintf(out, "    would be recorded in %s\n", projectConfig)
				continue
			}
			// AdoptOnly matters: a language toolchain is never in its own
			// provider's package list, so an install attempt here would be both
			// wrong and slow.
			doc.EnsureProjectFile(proj.Root)
			projectOpts := opts
			projectOpts.Dir = proj.Root
			projectOpts.AdoptOnly = true
			res, err := eng.Add(ctx, doc, model.Package{
				Name:     tool.Name,
				Provider: found.Installation.Provider,
				Scope:    found.Installation.Scope,
				Reason:   "adopted from " + proj.Marker + " in " + proj.Root,
			}, projectOpts)
			if err != nil {
				return result, report, err
			}
			result.Applied = append(result.Applied, res.Applied...)
			result.ConfigChanged = result.ConfigChanged || res.ConfigChanged
			result.ConfigPath = res.ConfigPath
		default:
			msg := tool.Name + " is not on PATH"
			if tool.Bootstrap {
				msg += "; it is this ecosystem's toolchain, so Starpoint will not install it for you"
			}
			fmt.Fprintf(out, "  %s\n", msg)
			result.Warnings = append(result.Warnings, model.Warning{
				Severity: model.SeverityWarn,
				Package:  tool.Name,
				Message:  msg,
			})
		}
	}

	// Anything else the ecosystem implies is a proposal, never an action.
	for _, req := range proj.Requirements() {
		if _, ok := doc.Package(req.Name); ok {
			continue
		}
		fmt.Fprintf(out, "  %s is implied by %s but not declared; add it to %s to manage it\n",
			req.Name, proj.Marker, projectConfig)
	}

	// Finally, the dependencies themselves.
	if deps == nil {
		fmt.Fprintf(out, "Dependencies: %s\n", why)
		return result, report, nil
	}
	if opts.DryRun {
		fmt.Fprintf(out, "Would run: %s (in %s; %s)\n", deps.String(), deps.Dir, deps.Reason)
		return result, report, nil
	}
	fmt.Fprintf(out, "Running: %s (in %s)\n", deps.String(), deps.Dir)
	if err := env.runInDir(ctx, deps.Dir, out, env.Stderr, deps.Tool, deps.Args...); err != nil {
		return result, report, fmt.Errorf("installing dependencies with '%s' in %s: %w", deps.String(), deps.Dir, err)
	}
	report.Ran = true
	return result, report, nil
}

// runInDir runs a command in a directory with its output streamed, which is
// what a dependency install needs: it can take minutes and its progress is the
// user's only sign of life.
func (e *Env) runInDir(ctx context.Context, dir string, stdout, stderr io.Writer, name string, args ...string) error {
	if e.RunInDir != nil {
		return e.RunInDir(ctx, dir, stdout, stderr, name, args...)
	}
	if _, err := osexec.LookPath(name); err != nil {
		return fmt.Errorf("%s is not on PATH; install it (for example 'starpoint install %s') and run this again", name, name)
	}
	cmd := osexec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

func cmdRemove(ctx context.Context, env *Env, w *report.Writer, args []string, allowed []string) error {
	named, args, err := env.splitProviderArg(args, allowed)
	if err != nil {
		return err
	}

	var (
		dryRun     bool
		noConfirm  bool
		allowEmpty bool
	)
	var names []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run", "-n":
			dryRun = true
		case "--no-confirm", "-y", "--yes":
			noConfirm = true
		case "--allow-empty":
			allowEmpty = true
		default:
			if err := rejectOption("remove", args[i]); err != nil {
				return err
			}
			names = append(names, args[i])
		}
	}
	if len(names) == 0 {
		return fmt.Errorf("%w: remove needs a package name", ErrUsage)
	}
	doc, eng, opts, _, err := env.setup(ctx, allowed)
	if err != nil {
		return err
	}

	// Removing every configured package in one go is the one removal with no undo
	// and no partial success: one invocation, every managed tool gone. It takes
	// an explicit --allow-empty to mean it. A dry run is allowed through so the
	// shape of the change can still be inspected.
	if !dryRun && !allowEmpty {
		if emptied := emptiesConfiguration(doc, names); len(emptied) > 0 {
			e := fmt.Errorf("refusing to remove every configured package (%s) in one command: that would leave Starpoint managing nothing. "+
				"Remove them one at a time, or pass --allow-empty to mean it", strings.Join(emptied, ", "))
			env.audit(auditlog.Entry{
				Event:   auditlog.EventGuard,
				Command: "remove",
				Result:  "refused",
				Detail:  e.Error(),
			})
			return e
		}
	}
	if !dryRun {
		path, _ := env.globalPath()
		unlock, lockErr := env.acquireMutationLock(path, "remove")
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

	// Removing uninstalls software. It is the one operation here that can take
	// something away from the user, so it is described before it happens.
	var changes []change
	for _, raw := range names {
		pkg := model.ParseSpec(raw)
		resolution := env.planRemoval(ctx, eng, doc, opts, pkg)
		changes = append(changes, removeChange(pkg, resolution))
	}
	if err := env.confirmChanges(changes, opts); err != nil {
		return err
	}
	if env.declined() {
		return nil
	}

	var results []*engine.Result
	for _, raw := range names {
		res, err := eng.Remove(ctx, doc, raw, opts)
		if err != nil {
			return err
		}
		results = append(results, res)
	}
	env.auditActions("remove", dryRun, results...)
	if w.JSON() {
		return w.Render(map[string]any{"operations": results})
	}
	for _, r := range results {
		if err := w.Result("remove", r); err != nil {
			return err
		}
	}
	return nil
}

// emptiesConfiguration reports the configured packages that removing every one
// of these names would remove, when that set is all of them and there is more
// than one.
//
// It is deliberately about bulk rather than about "Starpoint would manage
// nothing afterwards". Removing one named package is an ordinary, deliberate
// act — people remove the last tool they declared all the time — and a guard
// that fired on it would be noise. Removing every configured package in one
// invocation is a different shape: no undo, no partial success, every managed
// tool gone at once, which is exactly how a mistyped argument list becomes a
// wiped machine.
func emptiesConfiguration(doc *config.Document, names []string) []string {
	// The document can name the same package from the global and a project file,
	// so compare over distinct names.
	configured := map[string]bool{}
	for _, pkg := range doc.Packages() {
		configured[pkg.Name] = true
	}
	if len(configured) < 2 {
		return nil
	}
	wanted := map[string]bool{}
	for _, raw := range names {
		wanted[model.ParseSpec(raw).Name] = true
	}
	var gone []string
	for name := range configured {
		if wanted[name] {
			gone = append(gone, name)
		}
	}
	if len(gone) != len(configured) {
		return nil
	}
	sort.Strings(gone)
	return gone
}

// planRemoval resolves who would be asked to uninstall a package, for the sake of
// describing the change before it is made.
func (e *Env) planRemoval(ctx context.Context, eng *engine.Engine, doc *config.Document,
	opts engine.Options, pkg model.Package) model.Resolution {
	resolution, _, err := eng.Preview(ctx, doc, pkg, opts)
	if err != nil || resolution.Provider == "" {
		// Remove also accepts software that is configured but not installed, in
		// which case the provider is whatever the configuration recorded.
		return model.Resolution{Provider: singleProvider(opts.Allowed)}
	}
	return resolution
}

// cmdTransition inspects and performs provider transitions.
//
// With no arguments it is a read-only report of every pending move, because a
// user deciding whether to trust a change of ownership should be able to see the
// phases and the evidence before anything happens. With package names it performs
// those moves, one phase at a time, and stops at the first phase that is not
// justified.
func cmdTransition(ctx context.Context, env *Env, w *report.Writer, args []string, allowed []string) error {
	var (
		dryRun    bool
		noConfirm bool
		all       bool
		reverse   bool
	)
	var names []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run", "-n":
			dryRun = true
		case "--no-confirm", "-y":
			noConfirm = true
		case "--all", "-a":
			all = true
		case "--reverse", "-r":
			reverse = true
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("%w: unknown option %q for transition", ErrUsage, args[i])
			}
			names = append(names, args[i])
		}
	}

	doc, eng, opts, _, err := env.setup(ctx, allowed)
	if err != nil {
		return err
	}
	if !dryRun && len(names) > 0 {
		path, _ := env.globalPath()
		unlock, lockErr := env.acquireMutationLock(path, "transition")
		if lockErr != nil {
			return lockErr
		}
		defer unlock()
	}
	opts.DryRun = dryRun
	opts.NoConfirm = noConfirm

	// Handle --reverse flag
	if reverse {
		return cmdTransitionReverse(ctx, env, w, eng, doc, opts, names, dryRun, noConfirm)
	}

	plan, _, err := eng.Plan(ctx, doc, opts)
	if err != nil {
		return err
	}
	transitions := eng.Transitions(ctx, doc, opts)
	if len(names) == 0 {
		if !all {
			// Inspection shows every move that needs work plus any left behind, so
			// a completed-but-messy transition stays visible.
			transitions = visibleTransitions(transitions)
		}
		return w.Transitions(transitions, dryRun)
	}

	selected := selectTransitions(plan.States, transitions, names)
	if len(selected.states) == 0 {
		return fmt.Errorf("no pending provider transition for %s", strings.Join(names, ", "))
	}

	if !dryRun && !noConfirm && env.Confirm != nil {
		if err := confirmTransitions(env, selected); err != nil {
			return err
		}
		if !env.lastConfirm {
			// A refusal is a decision to change nothing. Falling through here
			// would perform the very work the user just declined.
			fmt.Fprintln(env.Stdout, "Nothing was changed.")
			return w.Transitions(selected.transitions, true)
		}
		opts.NoConfirm = true
	}

	var applied []model.Transition
	var failures []string
	for _, st := range selected.states {
		t, err := eng.ExecuteTransition(ctx, st, opts)
		applied = append(applied, t)
		env.auditTransition(t, err)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", t.Package.Name, err))
		}
	}
	if w.JSON() {
		if err := w.Render(map[string]any{
			"operation":   "transition",
			"dry_run":     dryRun,
			"transitions": applied,
		}); err != nil {
			return err
		}
	} else {
		w.Transitions(applied, dryRun)
	}
	if len(failures) > 0 {
		return fmt.Errorf("transitions did not complete:\n  %s", strings.Join(failures, "\n  "))
	}
	return nil
}

// transitionSelection pairs the transitions to perform with the states they come
// from, so execution needs no second lookup.
type transitionSelection struct {
	transitions []model.Transition
	states      []model.State
}

// selectTransitions resolves package names to the states a transition would act
// on, in configuration order.
func selectTransitions(states []model.State, transitions []model.Transition, names []string) transitionSelection {
	byName := map[string]model.State{}
	for _, st := range states {
		byName[st.Package.Name] = st
	}
	known := map[string]bool{}
	for _, t := range transitions {
		known[t.Package.Name] = true
	}
	var sel transitionSelection
	seen := map[string]bool{}
	for _, name := range names {
		st, ok := byName[name]
		if !ok || !known[name] || seen[name] {
			continue
		}
		seen[name] = true
		sel.states = append(sel.states, st)
		for _, t := range transitions {
			if t.Package.Name == name {
				sel.transitions = append(sel.transitions, t)
			}
		}
	}
	return sel
}

// visibleTransitions keeps the transitions a user still cares about: anything
// unfinished, and anything that left a duplicate behind.
func visibleTransitions(ts []model.Transition) []model.Transition {
	var out []model.Transition
	for _, t := range ts {
		if t.NeedsWork() || t.LeftBehindReason != "" {
			out = append(out, t)
		}
	}
	return out
}

// confirmTransitions shows exactly what a transition will do and whether it is
// still recoverable.
func confirmTransitions(env *Env, sel transitionSelection) error {
	fmt.Fprintln(env.Stdout, "These provider transitions will be performed:")
	for _, t := range sel.transitions {
		from := t.From.Name
		if from == "" {
			from = "nothing"
		}
		fmt.Fprintf(env.Stdout, "  %s: %s -> %s (currently %s)\n", t.Package.Name, from, t.To.Name, t.Phase)
		if t.Advice != "" {
			fmt.Fprintf(env.Stdout, "      %s\n", t.Advice)
		}
	}
	fmt.Fprintln(env.Stdout, "\nThe previous installation is only removed once the new one is confirmed working.")
	ok, err := env.Confirm("Proceed?")
	env.lastConfirm = ok
	return err
}

// cmdUpdate upgrades managed software.
func cmdUpdate(ctx context.Context, env *Env, w *report.Writer, args []string, allowed []string) error {
	var (
		dryRun    bool
		noConfirm bool
		self      bool
	)
	for _, a := range args {
		switch a {
		case "--dry-run", "-n":
			dryRun = true
		case "--no-confirm", "-y", "--yes":
			noConfirm = true
		case "--self":
			self = true
		default:
			return fmt.Errorf("%w: unknown option %q for update", ErrUsage, a)
		}
	}
	doc, eng, opts, _, err := env.setup(ctx, allowed)
	if err != nil {
		return err
	}
	if !dryRun {
		path, _ := env.globalPath()
		unlock, lockErr := env.acquireMutationLock(path, "update")
		if lockErr != nil {
			return lockErr
		}
		defer unlock()
	}
	opts.DryRun = dryRun
	opts.NoConfirm = noConfirm || doc.Settings().NoConfirm

	// Updating changes the machine without being asked about a specific package,
	// so the set of packages it would touch is shown first. A plan with nothing in
	// it is not worth a question.
	previewRes, err := eng.UpdatePreview(ctx, doc, opts)
	if err != nil {
		return err
	}

	// A pin holding a package back is the user's decision to revisit, not
	// Starpoint's. Each one is explained and asked about; anything but an
	// explicit yes keeps the pin. --no-confirm keeps every pin, because
	// silently rewriting the source of truth is the one thing an unattended
	// update must not do.
	if !opts.DryRun && !opts.NoConfirm && env.Confirm != nil {
		for _, c := range previewRes.PinConflicts {
			fmt.Fprintf(env.Stdout, "!!! There is a new version available for %s (%s), but config says %s\n",
				c.Package, c.Available, c.Pinned)
			ok, err := env.Confirm("Update anyway?")
			if err != nil {
				return err
			}
			if ok {
				if opts.UpdatePinned == nil {
					opts.UpdatePinned = map[string]bool{}
				}
				opts.UpdatePinned[c.Package] = true
			}
		}
	}

	var preview []change
	for _, a := range previewRes.Applied {
		preview = append(preview, change{
			verb:   "Update",
			target: a.State.Package.Spec(),
			via:    a.State.Res.Provider,
			detail: a.Note,
		})
	}
	if self {
		preview = append(preview, change{
			verb:   "Update",
			target: "Starpoint itself",
			detail: "reinstalls the running version of Starpoint",
		})
	}
	if err := env.confirmChanges(preview, opts); err != nil {
		return err
	}
	if env.declined() {
		return nil
	}

	res, updateErr := eng.Update(ctx, doc, opts)
	if self {
		selfRes, err := updateSelf(ctx, env, opts)
		if err != nil {
			return err
		}
		res.Applied = append(res.Applied, selfRes...)
	}
	env.auditActions("update", dryRun, res)
	if w.JSON() {
		_ = w.Update(res)
		return updateErr
	}
	for _, c := range res.PinConflicts {
		if c.Resolution == "updated" {
			continue // reported below as an applied update
		}
		outcome := "the pin was kept"
		if c.Resolution == "pending" {
			outcome = "update would ask whether to move the pin"
		}
		fmt.Fprintf(env.Stdout, "!!! There is a new version available for %s (%s), but config says %s; %s\n",
			c.Package, c.Available, c.Pinned, outcome)
	}
	for _, a := range res.Applied {
		fmt.Fprintf(env.Stdout, "%s", strings.TrimSpace(actionLine("updated", a)))
		fmt.Fprintln(env.Stdout)
	}
	for _, warn := range res.Warnings {
		if strings.HasPrefix(warn.Message, "there is a new version available") {
			continue // already shown as a pin conflict above
		}
		fmt.Fprintf(env.Stdout, "  %s\n", warn.Message)
	}
	if len(res.Applied) == 0 && len(res.Warnings) == 0 {
		fmt.Fprintln(env.Stdout, "Nothing to update.")
	}
	return updateErr
}

// actionLine renders one applied action as a line of text.
func actionLine(verb string, a model.PlannedAction) string {
	pkg := a.State.Package.Name
	if a.State.Res.Provider != "" {
		pkg += " via " + a.State.Res.Provider
	}
	line := verb + " " + pkg
	if a.Note != "" {
		line += " (" + a.Note + ")"
	}
	return line
}

// updateSelf asks the provider that installed Starpoint to upgrade it.
//
// This is the one place Starpoint manages itself, and it does so through the
// same provider machinery as everything else rather than a bespoke downloader.
func updateSelf(ctx context.Context, env *Env, opts engine.Options) ([]model.PlannedAction, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	found := env.loc().Locate(filepath.Base(resolved))
	if found.Installation == nil || found.Installation.Provider == "" {
		return nil, fmt.Errorf("could not determine which provider installed Starpoint (%s); update it manually", resolved)
	}
	reg := env.registry()
	p, ok := reg.Get(found.Installation.Provider)
	if !ok {
		return nil, fmt.Errorf("Starpoint is provided by %q, which this build cannot update", found.Installation.Provider)
	}
	if !p.Capabilities().Update {
		return nil, fmt.Errorf("provider %q cannot update software; update Starpoint manually", p.Name())
	}
	pkg := model.Package{Name: found.Installation.Name}
	if opts.DryRun {
		return []model.PlannedAction{{
			Action: model.ActionUpdate,
			State:  model.State{Package: pkg, Res: model.Resolution{Provider: p.Name()}},
			Note:   "would update Starpoint through " + p.Name(),
		}}, nil
	}
	inst, err := p.Update(ctx, pkg)
	if err != nil {
		return nil, err
	}
	note := "updated through " + p.Name()
	if inst != nil && inst.Version != "" {
		note = "updated to " + inst.Version + " through " + p.Name()
	}
	return []model.PlannedAction{{
		Action: model.ActionUpdate,
		State:  model.State{Package: pkg, Res: model.Resolution{Provider: p.Name()}},
		Note:   note,
	}}, nil
}

// setup loads configuration and builds the shared engine options.
func (e *Env) setup(ctx context.Context, allowed []string) (*config.Document, *engine.Engine, engine.Options, *project.Info, error) {
	path, err := e.globalPath()
	if err != nil {
		return nil, nil, engine.Options{}, nil, err
	}
	doc, err := config.Load(config.LoadOptions{
		GlobalPath:       path,
		Dir:              e.WorkDir,
		DiscoverProjects: true,
	})
	if err != nil {
		return nil, nil, engine.Options{}, nil, err
	}
	if detector := recovery.ForConfig(path); detector != nil {
		ops, detectErr := detector.Detect()
		if detectErr != nil {
			e.warn("could not inspect interrupted operations: " + detectErr.Error())
		} else {
			for _, op := range ops {
				e.warn(fmt.Sprintf("interrupted %s at %s: %s", op.Type, op.Path, op.Reason))
			}
		}
	}
	opts := engine.Options{
		Allowed:    append(append([]string{}, e.restrict...), allowed...),
		Dir:        e.WorkDir,
		ConfigPath: path,
	}
	proj := project.Detect(e.WorkDir)
	opts.Ecosystem = projectEcosystem(proj.Kind)
	for _, warn := range doc.Warnings {
		e.warn(warn)
	}
	return doc, e.newEngine(), opts, &proj, nil
}

// warn surfaces configuration problems even in JSON mode, because silently
// ignoring a malformed file would be worse than a noisy log.
func (e *Env) warn(msg string) {
	if e.Stderr == nil {
		return
	}
	fmt.Fprintf(e.Stderr, "warning: %s\n", msg)
}

// cmdRuns shows execution history of package operations.
func cmdRuns(ctx context.Context, env *Env, w *report.Writer, args []string) error {
	var (
		filterPackage   string
		filterDirectory string
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--package":
			if i+1 >= len(args) {
				return fmt.Errorf("%w: --package needs a value", ErrUsage)
			}
			filterPackage = args[i+1]
			i++
		case "--directory":
			if i+1 >= len(args) {
				return fmt.Errorf("%w: --directory needs a value", ErrUsage)
			}
			filterDirectory = args[i+1]
			i++
		default:
			return fmt.Errorf("%w: unknown option %q for runs", ErrUsage, args[i])
		}
	}

	// The run history lives beside the configuration, like every other record
	// Starpoint keeps, rather than at a hard-coded path in the home directory.
	tracker, err := tracking.New(tracking.PathIn(env.stateDir()))
	if err != nil {
		return fmt.Errorf("loading execution tracker: %w", err)
	}

	// Filter the execution history
	filtered := []tracking.RunRecord{}
	for _, rec := range tracker.AllRecords() {
		if filterPackage != "" && rec.PackageName != filterPackage {
			continue
		}
		if filterDirectory != "" && rec.Directory != filterDirectory {
			continue
		}
		filtered = append(filtered, rec)
	}

	if w.JSON() {
		return w.Render(map[string]any{"executions": filtered})
	}

	// Human-readable table format
	if len(filtered) == 0 {
		fmt.Fprintln(env.Stdout, "No execution history found.")
		return nil
	}

	fmt.Fprintln(env.Stdout, "Execution History:")
	fmt.Fprintln(env.Stdout, "")
	fmt.Fprintf(env.Stdout, "%-20s %-15s %-20s\n",
		"Package", "Directory", "Timestamp")
	fmt.Fprintln(env.Stdout, strings.Repeat("-", 60))

	for _, rec := range filtered {
		shortDir := rec.Directory
		if len(shortDir) > 15 {
			shortDir = "..." + shortDir[len(shortDir)-12:]
		}
		fmt.Fprintf(env.Stdout, "%-20s %-15s %-20s\n",
			rec.PackageName,
			shortDir,
			rec.Timestamp.Format("2006-01-02 15:04:05"))
	}
	return nil
}

// cmdPrune removes orphaned software from Starpoint-managed providers.
func cmdPrune(ctx context.Context, env *Env, w *report.Writer, args []string, allowed []string) error {
	var (
		dryRun    bool
		noConfirm bool
	)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run", "-n":
			dryRun = true
		case "--no-confirm", "-y", "--yes":
			noConfirm = true
		default:
			return fmt.Errorf("%w: unknown option %q for prune", ErrUsage, args[i])
		}
	}

	doc, eng, opts, _, err := env.setup(ctx, allowed)
	if err != nil {
		return err
	}
	opts.DryRun = dryRun
	opts.NoConfirm = noConfirm || doc.Settings().NoConfirm
	if !dryRun {
		path, _ := env.globalPath()
		unlock, lockErr := env.acquireMutationLock(path, "prune")
		if lockErr != nil {
			return lockErr
		}
		defer unlock()
	}
	previewOpts := opts
	previewOpts.DryRun = true
	preview, err := eng.Prune(ctx, doc, previewOpts)
	if err != nil {
		return err
	}
	if !dryRun && len(preview.Applied) > 0 && !opts.NoConfirm {
		if env.Confirm == nil {
			return errors.New("prune requires confirmation; use --no-confirm to proceed")
		}
		ok, err := env.Confirm(fmt.Sprintf("Remove %d orphaned user-scoped package(s)?", len(preview.Applied)))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(env.Stdout, "Nothing was changed.")
			return nil
		}
	}
	res := preview
	if !dryRun {
		res, err = eng.Prune(ctx, doc, opts)
		if err != nil {
			return err
		}
	}
	env.auditActions("prune", dryRun, res)

	return w.Prune(res, dryRun)
}

// cmdRecover inspects and clears files left behind by an interrupted run.
//
// Every mutating command refuses to start while an interrupted config write is
// unresolved, because it cannot tell whether the write it is about to make would
// be layered on top of something the user has not seen. That refusal is
// deliberate, but it is only safe if there is a way out. This command is that
// way out: it reports exactly what was left behind and, on request, removes it.
//
// It is read-only unless --discard is given, and it takes the mutation lock
// directly rather than through acquireMutationLock, since the whole point is to
// resolve the state that makes acquireMutationLock refuse.
func cmdRecover(env *Env, w *report.Writer, args []string) error {
	var discard, noConfirm bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--discard", "--clean":
			discard = true
		case "--no-confirm", "-y", "--yes":
			noConfirm = true
		default:
			return fmt.Errorf("%w: unknown option %q for recover", ErrUsage, args[i])
		}
	}

	path, err := env.globalPath()
	if err != nil {
		return err
	}
	detector := recovery.ForConfig(path)
	ops, err := detector.Detect()
	if err != nil {
		return fmt.Errorf("checking for interrupted operations: %w", err)
	}

	if len(ops) == 0 {
		if w.JSON() {
			return w.Render(map[string]any{"operation": "recover", "interrupted": []any{}})
		}
		fmt.Fprintln(env.Stdout, "No interrupted operations found.")
		return nil
	}

	// A temporary file that is the only copy of a configuration is never
	// discarded. It is not an aborted write to roll back; it is the user's
	// entire declared state, waiting for the rename that never happened.
	soleCopies := recovery.SoleCopies(ops)
	discardable := recovery.Discardable(ops)

	if !discard {
		if w.JSON() {
			return w.Render(map[string]any{
				"operation":   "recover",
				"interrupted": ops,
				"sole_copies": soleCopies,
			})
		}
		fmt.Fprintln(env.Stdout, "Interrupted operations found:")
		for _, op := range ops {
			fmt.Fprintf(env.Stdout, "  %s\n    %s\n", op.Path, op.Reason)
		}
		reportSoleCopies(env, soleCopies)
		if len(discardable) > 0 {
			fmt.Fprintf(env.Stdout, "\n%s\n",
				"Starpoint will not modify configuration while these are present.\n"+
					"The configuration itself is intact: it is only replaced by an\n"+
					"atomic rename, so it was never left half-written. Run\n"+
					"'starpoint recover --discard' to remove the leftover files.")
		}
		return nil
	}

	if len(discardable) == 0 {
		// Everything found is irreplaceable. Reporting success here would be a lie.
		if w.JSON() {
			return w.Render(map[string]any{
				"operation":   "recover",
				"cleaned":     []recovery.Operation{},
				"sole_copies": soleCopies,
				"remaining":   soleCopies,
			})
		}
		fmt.Fprintln(env.Stdout, "Nothing was discarded.")
		reportSoleCopies(env, soleCopies)
		return nil
	}

	if !noConfirm {
		// Deleting without the ability to ask is exactly what the confirmation
		// rule exists to prevent: a non-interactive stream must say it is running
		// unattended rather than quietly proceed.
		if env.Confirm == nil {
			return errors.New("recover --discard requires confirmation; use --no-confirm to proceed")
		}
		ok, err := env.Confirm(fmt.Sprintf("Discard %d leftover file(s)?\n%s",
			len(discardable), describePaths(discardable)))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(env.Stdout, "Nothing was changed.")
			return nil
		}
	}

	// Take the lock directly. acquireMutationLock would reject these very files.
	m, err := lock.New(filepath.Join(filepath.Dir(path), ".starpoint", "locks"))
	if err != nil {
		return err
	}
	l, err := m.AcquirePath(path, "recover")
	if err != nil {
		return err
	}
	defer func() { _ = l.Release() }()

	// Cleanup refuses anything it cannot prove safe, which is the right default
	// but the wrong answer for files whose real config is present: --discard is
	// the user making that judgement, in the command they were pointed at for
	// exactly this purpose. Sole copies are excluded above and never reach here.
	authorized := make([]recovery.Operation, len(discardable))
	copy(authorized, discardable)
	for i := range authorized {
		authorized[i].Recoverable = true
	}
	cleaned, failed, cleanErr := detector.Cleanup(authorized)

	if w.JSON() {
		out := map[string]any{
			"operation":   "recover",
			"cleaned":     cleaned,
			"remaining":   failed,
			"sole_copies": soleCopies,
		}
		if err := w.Render(out); err != nil {
			return err
		}
		// A partial cleanup is a failure even in JSON mode: the caller asked for
		// files to be gone and some are still there.
		return cleanErr
	}

	if len(cleaned) > 0 {
		fmt.Fprintf(env.Stdout, "Removed %d leftover file(s).\n", len(cleaned))
	}
	// Report the reason Cleanup actually recorded, which is why the delete
	// failed, rather than the reason Detect gave for leaving the file there.
	if len(failed) > 0 {
		fmt.Fprintln(env.Stdout, "Could not remove:")
		for _, op := range failed {
			fmt.Fprintf(env.Stdout, "  %s: %s\n", op.Path, op.Reason)
		}
	}
	reportSoleCopies(env, soleCopies)
	return cleanErr
}

// reportSoleCopies explains files that must be restored rather than deleted.
func reportSoleCopies(env *Env, ops []recovery.Operation) {
	if len(ops) == 0 {
		return
	}
	fmt.Fprintln(env.Stdout, "\nThese files are the only copy of a configuration and will not be discarded:")
	for _, op := range ops {
		fmt.Fprintf(env.Stdout, "  %s\n", op.Path)
		if op.Restorable() {
			fmt.Fprintf(env.Stdout, "    restore it with: mv %q %q\n", op.Path, op.Target())
		} else {
			fmt.Fprintf(env.Stdout, "    move it to %q by hand\n", op.Target())
		}
	}
}

// describePaths renders operations as an indented list for a confirmation prompt.
func describePaths(ops []recovery.Operation) string {
	var b strings.Builder
	for _, op := range ops {
		fmt.Fprintf(&b, "  %s\n", op.Path)
	}
	return b.String()
}

// cmdTransitionReverse reverses a previous transition for the given packages.
func cmdTransitionReverse(ctx context.Context, env *Env, w *report.Writer, eng *engine.Engine, doc *config.Document, opts engine.Options, names []string, dryRun, noConfirm bool) error {
	if len(names) == 0 {
		return fmt.Errorf("%w: --reverse requires package names", ErrUsage)
	}

	// Get transition history
	hist, err := history.New("")
	if err != nil {
		return fmt.Errorf("loading transition history: %w", err)
	}

	var selected []model.State
	var toReverse []model.Transition

	for _, name := range names {
		// Find the last transition for this package
		last := hist.LastTransition(name)
		if last == nil {
			fmt.Fprintf(env.Stdout, "No transition history found for %s\n", name)
			continue
		}

		if !last.Transition.Reversible {
			fmt.Fprintf(env.Stdout, "Last transition for %s is not reversible\n", name)
			continue
		}
		if last.Transition.Phase != model.TransitionCompleted {
			return fmt.Errorf("cannot reverse %s: the recorded transition did not complete", name)
		}

		pkg := last.Transition.Package
		pkg.Provider = last.Transition.From.Name
		current, _, lookupErr := eng.Preview(ctx, doc, pkg, opts)
		if lookupErr != nil {
			return fmt.Errorf("checking current ownership of %s: %w", name, lookupErr)
		}
		if current.Provider != last.Transition.To.Name {
			return fmt.Errorf("cannot reverse %s: history expects %s to own it, current resolution is %s", name, last.Transition.To.Name, current.Provider)
		}
		from, fromOK := eng.Registry().Get(last.Transition.From.Name)
		if !fromOK || !from.Capabilities().Install {
			return fmt.Errorf("cannot reverse %s: previous provider %s cannot install", name, last.Transition.From.Name)
		}
		if !last.Transition.From.Available {
			return fmt.Errorf("cannot reverse %s: previous provider %s is unavailable", name, last.Transition.From.Name)
		}
		if last.Transition.Installed == nil {
			return fmt.Errorf("cannot reverse %s safely: transition history has no verified installation details", name)
		}

		state := model.State{
			Package: pkg,
			Res: model.Resolution{
				Provider: last.Transition.From.Name,
				Reason:   "reversing previous transition",
			},
			Status: model.StatusProviderDiffers,
		}

		// Mark the current provider as different
		installed, lookupErr := from.Installed(ctx, pkg)
		if lookupErr != nil {
			return fmt.Errorf("checking previous installation of %s: %w", name, lookupErr)
		}
		state.Current = last.Transition.Installed
		if installed == nil {
			return fmt.Errorf("cannot reverse %s safely: %s no longer reports an installed copy", name, last.Transition.From.Name)
		}
		if !last.Transition.Verification.OK() {
			return fmt.Errorf("cannot reverse %s safely: the previous installation was not verified in transition history", name)
		}
		state.Elsewhere = []model.Installation{*installed}

		selected = append(selected, state)
		toReverse = append(toReverse, last.Transition)
	}

	if len(selected) == 0 {
		return fmt.Errorf("no reversible transitions found for %s", strings.Join(names, ", "))
	}

	// Show what will be reversed
	fmt.Fprintln(env.Stdout, "The following transitions will be reversed:")
	for _, t := range toReverse {
		fmt.Fprintf(env.Stdout, "  %s: %s -> %s (reversing transition from %s)\n",
			t.Package.Name, t.To.Name, t.From.Name,
			time.Unix(t.Timestamp, 0).Format("2006-01-02 15:04:05"))
		if t.Note != "" {
			fmt.Fprintf(env.Stdout, "      %s\n", t.Note)
		}
	}

	if !dryRun && !noConfirm {
		if env.Confirm == nil {
			return errors.New("transition reversal requires confirmation; use --no-confirm to proceed")
		}
		ok, err := env.Confirm("Proceed?")
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(env.Stdout, "Nothing was changed.")
			return nil
		}
	}

	// Perform the reverse transitions
	var applied []model.Transition
	var failures []string
	for _, st := range selected {
		t, err := eng.ExecuteTransition(ctx, st, opts)
		applied = append(applied, t)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", t.Package.Name, err))
		}
	}

	if w.JSON() {
		if err := w.Render(map[string]any{
			"operation":   "transition-reverse",
			"dry_run":     dryRun,
			"transitions": applied,
		}); err != nil {
			return err
		}
	}
	if !w.JSON() {
		w.Transitions(applied, dryRun)
	}

	if len(failures) > 0 {
		return fmt.Errorf("reverse transitions did not complete:\n  %s", strings.Join(failures, "\n  "))
	}
	for _, t := range applied {
		if t.Phase != model.TransitionCompleted {
			continue
		}
		pkg := t.Package
		pkg.Provider = t.To.Name
		if f, found := doc.Find(pkg.Name); found {
			var destination *config.File
			if f.Project {
				for i := range doc.Projects {
					if doc.Projects[i].Path == f.Path {
						destination = &doc.Projects[i]
						break
					}
				}
			} else {
				destination = &doc.Global
			}
			if destination == nil {
				return fmt.Errorf("reverse of %s completed but config file is unavailable", pkg.Name)
			}
			for i := range destination.Raw.Packages {
				if destination.Raw.Packages[i].Name == pkg.Name {
					destination.Raw.Packages[i].Provider = t.To.Name
				}
			}
		} else {
			doc.Global.Raw.Packages = append(doc.Global.Raw.Packages, pkg)
		}
		if err := doc.Save(); err != nil {
			return fmt.Errorf("reverse of %s completed but could not persist desired provider: %w", pkg.Name, err)
		}
	}
	return nil
}

var _ = runtime.GOOS
var _ = resolve.New
