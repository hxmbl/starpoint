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

	"github.com/hxmbl/starpoint/internal/align"
	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/color"
	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/elevation"
	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/history"
	"github.com/hxmbl/starpoint/internal/locator"
	"github.com/hxmbl/starpoint/internal/lock"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/project"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers"
	"github.com/hxmbl/starpoint/internal/providers/system"
	"github.com/hxmbl/starpoint/internal/recovery"
	"github.com/hxmbl/starpoint/internal/report"
	"github.com/hxmbl/starpoint/internal/resolve"
	"github.com/hxmbl/starpoint/internal/retention"
	"github.com/hxmbl/starpoint/internal/tracking"
)

// Version is the build version, overridable at link time.
var Version = "0.2.0"

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
	// NewRegistry builds the providers for a run whose privileges are
	// decided, when Registry is nil. Nil means providers.New, the built-in
	// set. The root tests use it to run a real provider against a fake tool.
	NewRegistry func(pv *providers.Privileges) *provider.Registry
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
	// Verbose lists every effect of an action in human output, not only the
	// destructive ones. Set from --verbose.
	Verbose bool
	// Elevation reports whether the process runs elevated. Nil means the
	// real check; tests replace it.
	Elevation func() elevation.Status
	// jsonMode is set for --json, so prompts keep off stdout.
	jsonMode bool
	// colorMode is --color: when human output is colorized. The palettes are
	// resolved per writer, because stdout and stderr can be redirected
	// separately — a terminal stdout with a piped stderr should color one and
	// not the other.
	colorMode color.Mode
	palOut    color.Palette
	palErr    color.Palette
	palPrompt color.Palette
	// Interactive reports whether a person at a terminal can answer
	// questions. Nil means "Confirm is set": tests that wire a Confirm
	// function stand for a user at a terminal. The program sets it from
	// whether stdin is a terminal, so a piped answer is not mistaken for a
	// person who has read the list of changes.
	Interactive func() bool
	// allowDestructive is --allow-destructive: an unattended run may delete
	// or replace things.
	allowDestructive bool
	// allowRoot is --allow-root: a changing command may run as root or
	// elevated.
	allowRoot bool
	// rootApproved is set when --allow-root was given or the invoking user
	// confirmed an elevated run interactively.
	rootApproved bool
	// elevation caches the detected status for one Run.
	elevation *elevation.Status
	// SetHome points the process at another home directory. Nil means
	// setting HOME in the process environment, which is what the providers,
	// their subprocesses and the default configuration path all read. Tests
	// replace it so the test binary's own HOME is never changed.
	SetHome func(home string) error
	// RootHome reports root's own home directory, for the commands that stay
	// root in an elevated run acting for its invoking user. Nil means the
	// user database's entry for the effective user. Tests replace it.
	RootHome func() string
	// target is the invoking user's environment an elevated run acts on, if
	// any.
	target *elevation.Target
	// alignment is the ownership and permission policy of an elevated run.
	alignment *align.Policy
	// AdjustAlignment, when set, sees the ownership and permission policy of
	// an elevated run before it is used. Tests that cannot change owners use
	// it to reassign from their own uid and record the chowns instead.
	AdjustAlignment func(p *align.Policy)
	// started is when this Run began; an elevated run only reassigns what
	// changed after it.
	started time.Time
	// DropProbe checks that commands can really be started as the invoking
	// user before an elevated run relies on it. Nil means privdrop.Probe,
	// which starts `id -u` as the user; tests that pretend to be elevated
	// replace it.
	DropProbe func(ctx context.Context, id *exec.Identity) error
	// Environ is the environment the invoking user's environment is built
	// from. Nil means os.Environ.
	Environ func() []string
	// privileges decides which account each provider's commands run as in
	// this Run.
	privileges *providers.Privileges
	// identity is the invoking user's identity when this Run drops
	// privileges for user-level providers.
	identity *exec.Identity
	// blockCode and blockMessage say why an elevated run for an invoking
	// user cannot act for them; changing commands are refused.
	blockCode    string
	blockMessage string
	// runCtx is this Run's context, for lookups made while reporting.
	runCtx context.Context
	// reg is the registry built for this Run.
	reg *provider.Registry
}

// setColor resolves the run's palettes from --color and the writers the run
// was given. It is called once per Run, before anything is printed.
func (e *Env) setColor(mode color.Mode, asJSON bool) {
	e.colorMode = mode
	e.palOut = color.New(mode, e.Stdout)
	e.palErr = color.New(mode, e.Stderr)
	// Prompts go to stdout normally and to stderr under --json, so they follow
	// whichever stream they will actually land on.
	e.palPrompt = e.palOut
	if asJSON {
		e.palPrompt = e.palErr
	}
}

// elevationStatus reports the privileges of this run, detected once per Run.
func (e *Env) elevationStatus() elevation.Status {
	if e.elevation != nil {
		return *e.elevation
	}
	detect := e.Elevation
	if detect == nil {
		detect = elevation.Detect
	}
	s := detect()
	e.elevation = &s
	return s
}

// elevationWarning is the warning for an elevated run, or nil.
func (e *Env) elevationWarning() *model.Warning {
	return e.elevationStatus().Warning()
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
		Effects:  t.Effects,
		RunAs:    t.RunAs,
		Destructive: func() bool {
			for _, eff := range t.Effects {
				if eff.Destructive {
					return true
				}
			}
			return false
		}(),
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
				outcome = string(a.Outcome)
			}
			if outcome == "" {
				outcome = string(model.OutcomeApplied)
			}
			detail := a.Note
			if a.Error != "" {
				detail = strings.TrimSpace(detail + "; " + a.Error)
			}
			entry := auditlog.Entry{
				Event:       auditlog.EventAction,
				Command:     command,
				DryRun:      dryRun,
				Action:      string(a.Action),
				Package:     a.State.Package.Name,
				Provider:    a.State.Res.Provider,
				Result:      outcome,
				Detail:      detail,
				Destructive: a.Destructive(),
			}
			// What actually changed is the point of the log; predictions are
			// left to the dry run's own output.
			if !dryRun {
				entry.Effects = a.Effects
			}
			entry.RunAs = e.actionRunAs(a)
			e.audit(entry)
		}
	}
}

func (e *Env) registry() *provider.Registry {
	if e.Registry != nil {
		return e.Registry
	}
	if e.reg == nil {
		build := e.NewRegistry
		if build == nil {
			build = providers.New
		}
		e.reg = build(e.privileges)
	}
	return e.reg
}

func (e *Env) loc() *locator.Locator {
	if e.Locator != nil {
		return e.Locator
	}
	return locator.New()
}

func (e *Env) newEngine() *engine.Engine {
	if e.newEngineFn != nil {
		eng := e.newEngineFn()
		if p := e.alignPolicy(); p != nil {
			eng.SetAlignment(p)
		}
		eng.SetRunAs(e.providerRunAs)
		return eng
	}
	eng := engine.New(e.registry(), e.loc(), engine.WithStateDir(e.stateDir()), engine.WithAlignment(e.alignPolicy()),
		engine.WithRunAs(e.providerRunAs))
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
		colorMode  color.Mode
	)
	rest, flagErr := extractGlobalFlags(args, &asJSON, &force, &providers, &contextDir, &colorMode)
	env.setColor(colorMode, asJSON)
	rest = extractVerbose(rest, &env.Verbose)
	rest = extractBool(rest, "--allow-destructive", &env.allowDestructive)
	rest = extractBool(rest, "--allow-root", &env.allowRoot)
	if contextDir != "" {
		env.WorkDir = contextDir
	}
	if flagErr != nil {
		fmt.Fprintf(env.Stderr, "%s %s\n", env.palErr.BoldRed("error:"), flagErr)
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
	env.elevation = nil
	env.alignment = nil
	env.started = time.Now()
	env.runCtx = ctx
	env.reg = nil
	env.retarget(ctx)
	w := report.New(env.Stdout, asJSON)
	w.SetColor(env.colorMode)
	env.jsonMode = asJSON
	w.SetVerbose(env.Verbose)
	w.SetRunAs(env.actionRunAs)
	if warn := env.elevationWarning(); warn != nil {
		w.SetContextWarnings(*warn)
	}
	if warn := env.dropWarning(); warn != nil {
		w.SetContextWarnings(*warn)
	}

	// `starpoint <command> --help` is help for that command, wherever the flag
	// sits. It must be decided before dispatch: a command that took it as an
	// argument would treat it as a package name.
	if canonicalCommand(cmd) != "" && wantsHelp(cmdArgs) {
		printCommandHelp(env.Stdout, cmd)
		return 0
	}

	var err error
	env.begin(cmd, cmdArgs)
	if err = env.guardElevated(cmd, cmdArgs); err == nil {
		err = env.dispatch(ctx, w, cmd, cmdArgs, force, providers)
	}
	if err == errHelpShown {
		if !writesNothing(canonicalCommand(cmd)) {
			env.alignOwnFiles()
		}
		return 0
	}
	if err == errUnknownCommand {
		fmt.Fprintf(env.Stderr, "%s %q\n\n", env.palErr.BoldRed("unknown command"), cmd)
		printUsage(env.Stderr)
		return 2
	}

	if err != nil {
		if refusal, ok := asRefusal(err); ok {
			reportRefusal(env.Stderr, env.palErr, asJSON, refusal, env.actionRunAs)
			return env.finish(canonicalCommand(cmd), cmdArgs, refusal.Exit, err)
		}
		if errors.Is(err, ErrUsage) {
			fmt.Fprintf(env.Stderr, "%s %s\n\n", env.palErr.BoldRed("error:"), err)
			printCommandHelp(env.Stderr, cmd)
			return env.finish(canonicalCommand(cmd), cmdArgs, 2, err)
		}
		if asJSON {
			enc := json.NewEncoder(env.Stderr)
			enc.SetIndent("", "  ")
			_ = enc.Encode(map[string]any{"schema_version": report.SchemaVersion, "error": err.Error()})
		} else {
			fmt.Fprintf(env.Stderr, "%s %s\n", env.palErr.BoldRed("error:"), err)
		}
		return env.finish(canonicalCommand(cmd), cmdArgs, 1, err)
	}
	return env.finish(canonicalCommand(cmd), cmdArgs, 0, nil)
}

// errHelpShown and errUnknownCommand let dispatch end a command without an
// error being reported.
var (
	errHelpShown      = errors.New("help shown")
	errUnknownCommand = errors.New("unknown command")
)

// dispatch runs one subcommand.
func (env *Env) dispatch(ctx context.Context, w *report.Writer, cmd string, cmdArgs []string, force bool, providers []string) error {
	var err error
	switch cmd {
	case "init":
		err = cmdInit(ctx, env, w, cmdArgs, force)
	case "sync":
		err = cmdSync(ctx, env, w, cmdArgs, providers)
	case "list", "ls":
		err = cmdList(ctx, env, w, cmdArgs)
	case "inventory":
		err = cmdInventory(ctx, env, w, cmdArgs, providers)
	case "adopt":
		err = cmdAdopt(ctx, env, w, cmdArgs, providers)
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
			return errHelpShown
		}
		printUsage(env.Stdout)
		return errHelpShown
	default:
		return errUnknownCommand
	}
	return err
}

// begin records that a command started and ages out expired records.
//
// Both happen on every command. Retention is what makes it safe to log
// everything: a log nobody prunes is a log that eventually fills the disk, at
// which point it is the tool's fault and it stops working.
func (e *Env) begin(command string, args []string) {
	canonical := canonicalCommand(command)
	if canonical == "" || writesNothing(canonical) {
		return
	}
	e.applyRetention()
	entry := auditlog.Entry{
		Event:     auditlog.EventCommandStart,
		Command:   canonical,
		Args:      append([]string{}, args...),
		DryRun:    hasFlag(args, "--dry-run", "-n"),
		Directory: e.WorkDir,
	}
	if warn := e.elevationWarning(); warn != nil {
		// Who the command ran as decides where its effects landed, so the log
		// says so on the entry that opens the command.
		entry.Elevated = true
		entry.Detail = warn.Message
	}
	e.audit(entry)
}

// finish records how a command ended and returns its exit code.
func (e *Env) finish(command string, args []string, code int, err error) int {
	// Starpoint's own records go back to the user an elevated run acts for
	// before the last entry is appended; appending keeps the owner. A command
	// that writes nothing has nothing of its own to give back, and must not
	// chown what earlier runs left either: "writes nothing" includes
	// ownership.
	if !writesNothing(command) {
		e.alignOwnFiles()
	}
	if command != "" && !writesNothing(command) {
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

// writesNothing reports whether a command is observation only, down to
// Starpoint's own records: inventory leaves no audit entry and ages nothing
// out, so it can be run on a machine Starpoint has never touched.
func writesNothing(command string) bool { return command == "inventory" }

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
			days = doc.Global.Raw.Settings.Retention()
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
		fmt.Fprintf(e.Stderr, "%s %s\n", e.palErr.Yellow("warning:"), rep.Error())
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

// extractVerbose removes --verbose and -v wherever they appear. They are
// global like --json: `starpoint remove ruff -v` and `starpoint -v remove ruff`
// mean the same thing.
func extractVerbose(args []string, verbose *bool) []string {
	out := args[:0:0]
	for _, a := range args {
		if a == "--verbose" || a == "-v" {
			*verbose = true
			continue
		}
		out = append(out, a)
	}
	return out
}

// extractBool removes a global boolean flag wherever it appears.
func extractBool(args []string, flag string, value *bool) []string {
	out := args[:0:0]
	for _, a := range args {
		if a == flag {
			*value = true
			continue
		}
		out = append(out, a)
	}
	return out
}

// extractGlobalFlags pulls the global options out of an argument list and
// returns what remains: the subcommand and its own arguments.
func extractGlobalFlags(args []string, asJSON, force *bool, providers *[]string, contextDir *string, colorMode *color.Mode) ([]string, error) {
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
		case "--color":
			if i+1 >= len(args) {
				return nil, errors.New("--color needs a value: auto, always or never")
			}
			mode, err := parseColorMode(args[i+1])
			if err != nil {
				return nil, err
			}
			*colorMode = mode
			i++
		default:
			// `--color=never` is the other spelling users reach for, and it
			// should not become an unknown command.
			if value, ok := strings.CutPrefix(args[i], "--color="); ok {
				mode, err := parseColorMode(value)
				if err != nil {
					return nil, err
				}
				*colorMode = mode
				continue
			}
			rest = append(rest, args[i])
		}
	}
	return rest, nil
}

// parseColorMode validates a --color value rather than silently treating a
// typo as auto: a user who asked for color and got none should be told why.
func parseColorMode(value string) (color.Mode, error) {
	if !color.Valid(value) {
		return color.Auto, fmt.Errorf("--color must be auto, always or never, not %q", value)
	}
	return color.ParseMode(value), nil
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `starpoint — declarative orchestration for the software on your machine

Usage:
  starpoint <command> [options]
  starpoint <command> --help      Help for one command

Commands:
  init [--force]                 Create the global configuration (~/.starpoint.toml)
  sync [--dry-run] [--no-confirm]
                                 Make installed state match the configuration;
                                 never removes or moves software
  list, ls                       Show managed software, where it is, and who owns it
  inventory [--provider <name>]  List what every provider has installed, managed or not;
                                 changes and records nothing
  info, show <package>           Show one package's state in detail
  explain <package>              Show how Starpoint decides which provider owns a package
  doctor [--all]                 Inspect Starpoint, configuration, providers, ownership, PATH
  adopt <package> [provider]     Record the owner of software that is already installed;
  adopt --all                    never installs, removes or moves anything (--all: everything
                                 a provider says was installed on request)
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
  --verbose, -v        List every effect of a change, not only what it
                       removes or overwrites
  --color <when>       Colorize human output: auto (the default), always or
                       never. auto colors a terminal and nothing else, and
                       NO_COLOR turns it off.
  --allow-destructive  Let an unattended run (--yes, or no terminal) delete
                       or replace things
  --allow-root         Let a changing command run as root or elevated
  --version            Print the version
  --help               Show this help

Anything that installs, removes or updates software asks first. Answer with
--no-confirm to skip the question; a stream that provides no answer at all is
refused rather than assumed. --dry-run shows what would happen, with every
file, package and configuration entry it would touch, and changes nothing.
Anything a change would delete or overwrite is marked ! and always shown;
--verbose lists the rest. --json reports all of it on every action.

An unattended run (--yes, --no-confirm, or no terminal to ask on) that would
delete or replace anything is refused unless --allow-destructive is given; the
refusal lists every planned change.

Run as root or elevated, a command that changes anything is refused unless
--allow-root is given or, under sudo or doas at a terminal, you confirm. Under
sudo or doas Starpoint acts on your own environment (your home, configuration
and ~/.local/bin) and gives you what it creates there; what it installs in
shared locations is made readable and executable by everyone. Read-only
commands and doctor always run.

Exit status: 0 success, 1 failure, 2 usage error, 3 refused because an
unattended run would delete or replace something (--allow-destructive), 4
refused because a changing command ran as root (--allow-root), 5 refused
because a decision only you can make was needed and there was no terminal to
ask on (the refusal names the candidates). Nothing is changed by a refusal.

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
		case "--no-confirm", "-y", "--yes":
			noConfirm = true
		default:
			return fmt.Errorf("%w: unknown option %q for sync", ErrUsage, args[i])
		}
	}

	doc, eng, opts, proj, err := env.setup(ctx, allowed)
	if err != nil {
		return err
	}
	// no_confirm in the configuration means what --no-confirm means, as it
	// does for install, remove, update and prune. It makes the run
	// unattended, so the unattended guards (destructive, ambiguous provider,
	// managed copy beside the system's) apply to it exactly as to --yes.
	noConfirm = noConfirm || doc.Settings().NoConfirm
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
	plan, beside := besideSystem(ctx, eng, doc, plan)
	plan = withoutMoves(plan)

	guardOpts := opts
	guardOpts.DryRun = dryRun
	guardOpts.NoConfirm = noConfirm
	if err := env.guardDestructive("sync", guardOpts, plan.Actions); err != nil {
		return err
	}
	if err := env.guardAmbiguous("sync", guardOpts, plan.Actions); err != nil {
		return err
	}
	if err := env.guardBesideSystem("sync", guardOpts, beside); err != nil {
		return err
	}
	if plan, err = env.decideBesideSystem(guardOpts, plan, beside); err != nil {
		return err
	}
	if !dryRun && !noConfirm && len(plan.Actions) > 0 && env.Confirm != nil {
		ok, err := confirmPlan(env, plan)
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(env.Stdout, env.palOut.Dim("Nothing was changed."))
			return w.Sync(&engine.Result{DryRun: true, States: plan.States}, plan, proj)
		}
	}
	opts.NoConfirm = true
	opts.DryRun = dryRun

	res, applyErr := eng.Apply(ctx, doc, plan, opts)
	if !dryRun && len(res.Applied) > 0 {
		// What the machine looks like now, not what it looked like before
		// the run: `states` and `converged` describe the result. A sync that
		// converged must not tell a caller it has not.
		if after, _, err := eng.Plan(ctx, doc, opts); err == nil {
			res.States = withoutMoves(after).States
		}
	}
	if applyErr != nil {
		// Still report what did happen; partial progress is real progress.
		_ = w.Sync(res, plan, proj)
		return applyErr
	}
	return w.Sync(res, plan, proj)
}

// withoutMoves takes provider moves out of a sync plan.
//
// sync never removes software. Software the configuration gives to one
// provider while another has it is drift: the real provider still owns its
// lifecycle, and moving it (install the new copy, remove the old) is a
// decision `transition` makes, one package at a time, with its phases and
// its reversal. sync reports each one as a provider-drift warning naming the
// two commands that settle it, and the package stays unconverged.
func withoutMoves(plan model.Plan) model.Plan {
	kept := plan.Actions[:0:0]
	for _, a := range plan.Actions {
		if a.Action != model.ActionRelocate {
			kept = append(kept, a)
			continue
		}
		st := a.State
		current := ""
		where := ""
		if st.Current != nil {
			current = st.Current.Provider
			if st.Current.Path != "" {
				where = " at " + st.Current.Path
			}
		}
		plan.Warnings = append(plan.Warnings, model.Warning{
			Severity: model.SeverityWarn,
			Code:     model.WarnProviderDrift,
			Package:  st.Package.Name,
			Provider: st.Res.Provider,
			Message: fmt.Sprintf("the configuration gives %s to %s, but %s has it%s; sync does not move or remove software. "+
				"Run 'starpoint transition %s' to move it to %s, or 'starpoint adopt %s %s' to record %s as its owner",
				st.Package.Name, st.Res.Provider, current, where, st.Package.Name, st.Res.Provider, st.Package.Name, current, current),
		})
	}
	plan.Actions = kept
	plan.Transitions = nil
	return plan
}

// confirmPlan shows the user exactly what is about to happen.
func confirmPlan(env *Env, plan model.Plan) (bool, error) {
	fmt.Fprintln(env.promptOut(), env.palPrompt.Bold("The following changes will be made:"))
	for _, a := range plan.Actions {
		line := "  " + env.palPrompt.Yellow(fmt.Sprintf("%-10s", a.Action)) + " " + a.State.Package.Name
		if a.State.Res.Provider != "" {
			line += " via " + a.State.Res.Provider
		}
		fmt.Fprintln(env.promptOut(), line)
		if a.Note != "" {
			fmt.Fprintf(env.promptOut(), "             %s\n", env.palPrompt.Dim(a.Note))
		}
		// Every effect: the question must not hide a change behind a count.
		report.WriteEffects(env.promptOut(), env.palPrompt, a.Effects, true, true, "             ")
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

// cmdInventory lists what every provider reports as installed, managed or
// not. It only observes: providers are asked for their health and their
// package lists, and nothing is resolved, installed, recorded or logged.
func cmdInventory(ctx context.Context, env *Env, w *report.Writer, args []string, only []string) error {
	if len(args) > 0 {
		return fmt.Errorf("%w: unknown argument %q for inventory", ErrUsage, args[0])
	}
	reg := env.registry()
	for _, name := range only {
		if _, ok := reg.Get(name); !ok {
			return fmt.Errorf("%w: unknown provider %q", ErrUsage, name)
		}
	}
	path, err := env.globalPath()
	if err != nil {
		return err
	}
	// A missing configuration is not an error here: inventory is how a
	// machine with no configuration yet is looked at.
	doc, err := config.Load(config.LoadOptions{GlobalPath: path, Dir: env.WorkDir, DiscoverProjects: true})
	if err != nil {
		return err
	}
	for _, warn := range doc.Warnings {
		env.warn(warn)
	}
	inv := env.newEngine().Inventory(ctx, doc, only)
	return w.Inventory(inv)
}

// unmanagedCounts is doctor's drift hint: per provider, what it has that no
// configuration entry records with it. The operating system's software is
// left out, since Starpoint never manages it.
func unmanagedCounts(inv engine.Inventory) []report.UnmanagedCount {
	out := []report.UnmanagedCount{}
	index := map[string]int{}
	for _, it := range inv.Items {
		if it.Managed || it.Provider == system.Name {
			continue
		}
		i, ok := index[it.Provider]
		if !ok {
			i = len(out)
			index[it.Provider] = i
			out = append(out, report.UnmanagedCount{Provider: it.Provider})
		}
		out[i].Count++
		if it.Requested == engine.RequestedYes {
			out[i].Requested++
		}
	}
	return out
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
	return fmt.Errorf("%s is not in the configuration and no provider has it installed%s", name, unaskableNote(survey))
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
		Unmanaged:     []report.UnmanagedCount{},
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
	rep.Unmanaged = unmanagedCounts(eng.Inventory(ctx, doc, nil))

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
				Code:     model.WarnDuplicateCopy,
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
				Code:     model.WarnTransitionIncomplete,
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
				Code:     model.WarnTransitionLeftBehind,
				Package:  t.Package.Name,
				Provider: t.From.Name,
				Message: fmt.Sprintf("ownership moved to %s but the %s copy was left in place (%s). %s",
					t.To.Name, t.From.Name, t.LeftBehindReason, nextClause(t.Advice)),
			})
		}
		for _, shadow := range eng.Shadowed(ctx, doc, engine.Options{Dir: env.WorkDir}) {
			rep.Problems = append(rep.Problems, model.Warning{
				Severity: model.SeverityError,
				Code:     model.WarnShadowed,
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
					Code:     model.WarnMissing,
					Package:  st.Package.Name,
					Message:  "declared in the configuration but not installed",
				})
			case model.StatusUnpinnedConflict:
				rep.Problems = append(rep.Problems, model.Warning{
					Severity: model.SeverityError,
					Code:     model.WarnVersionUnsatisfiable,
					Package:  st.Package.Name,
					Provider: st.Res.Provider,
					Message: fmt.Sprintf("configuration pins %s but provider %s cannot install that version",
						st.Package.Version, st.Res.Provider),
				})
			}
		}
	}
	if warn := env.elevationWarning(); warn != nil {
		rep.Problems = append(rep.Problems, *warn)
	}
	if warn := env.dropWarning(); warn != nil {
		rep.Problems = append(rep.Problems, *warn)
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
		dryRun         bool
		noConfirm      bool
		version        string
		channel        string
		asProject      bool
		allowDuplicate bool
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
		case "--allow-duplicate":
			allowDuplicate = true
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
		if res != nil {
			env.auditActions("install", dryRun, res)
			// Partial progress is reported, not swallowed by the error.
			if rerr := w.Operation("install", dryRun, []*engine.Result{res}, map[string]any{"dependencies": deps}); rerr != nil && err == nil {
				err = rerr
			}
		}
		return err
	}

	type job struct {
		pkg     model.Package
		change  change
		survey  engine.Survey
		preview model.PlannedAction
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

		// overrode is whether the provider is the user's call against
		// Starpoint's own: named on the command line, or picked at the question
		// instead of the offered default.
		overrode := pkg.Provider != ""
		// An ambiguous provider choice is worth one question, not a guess. With no
		// way to ask, the command fails rather than picking one silently.
		if pkg.Provider == "" {
			chosen, offered, err := env.askProvider(ctx, eng, doc, opts, pkg)
			if err != nil {
				return err
			}
			if chosen != "" {
				pkg.Provider = chosen
				pkg.Reason = "provider chosen interactively"
				overrode = chosen != offered
			}
		}

		// The preview is what the question shows: every effect the install is
		// expected to have, from the same derivation the install itself uses.
		// The resolution and any adoption come from the same preview rather
		// than a second derivation: each one asks every provider in turn
		// whether it has the package, and that is most of an install's wait.
		preview, err := eng.AddPreview(ctx, doc, pkg, opts)
		if err != nil {
			return err
		}
		resolution := preview.State.Res
		var adopted *model.Installation
		if preview.Action == model.ActionAdopt {
			adopted = preview.State.Desired
		}
		if adopted == nil && preview.Action == model.ActionInstall {
			if err := env.guardDuplicate(pkg, resolution.Provider, survey, preview, opts, allowDuplicate); err != nil {
				return err
			}
			if w := duplicateWarning(pkg.Name, resolution.Provider, survey); w != nil {
				preview.Warnings = append(preview.Warnings, *w)
			}
		}
		jobs = append(jobs, job{pkg: pkg, change: installChange(pkg, resolution, adopted, preview, overrode), survey: survey, preview: preview})
	}

	// One question for the whole command, describing every change it would make.
	// Asking per package would make `install a b c` three interruptions.
	changes := make([]change, 0, len(jobs))
	previews := make([]model.PlannedAction, 0, len(jobs))
	for _, j := range jobs {
		changes = append(changes, j.change)
		previews = append(previews, j.preview)
	}
	if err := env.guardDestructive("install", opts, changeActions(model.ActionInstall, previews)); err != nil {
		return err
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
			// Report what this command already did, and whatever the failed
			// step left behind, before the error.
			if res != nil {
				results = append(results, res)
			}
			env.auditActions("install", dryRun, results...)
			if len(results) > 0 {
				_ = w.Operation("install", dryRun, results, map[string]any{"lookup": surveys})
			}
			return err
		}
		// Tracking is handled in the engine.
		results = append(results, res)
	}
	env.auditActions("install", dryRun, results...)
	return reportInstall(w, dryRun, results, surveys)
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
//
// The action list is the documented ActionResult shape, shared with sync and
// update, rather than the engine's own Result. A consumer should not have to
// know which command produced a document to read its actions.
func reportInstall(w *report.Writer, dryRun bool, results []*engine.Result, surveys []engine.Survey) error {
	return w.Operation("install", dryRun, results, map[string]any{"lookup": surveys})
}

// askProvider presents the ambiguous candidates to the user. It returns the
// provider chosen and the default that was offered, so the caller can tell
// accepting Starpoint's suggestion from overruling it.
func (e *Env) askProvider(ctx context.Context, eng *engine.Engine, doc *config.Document, opts engine.Options, pkg model.Package) (string, string, error) {
	dec := eng.Decide(ctx, doc, pkg, opts)
	if !dec.Ambiguous || len(dec.Alternatives) == 0 {
		// Nothing worth asking about: leave the decision to the resolver so the
		// recorded reason explains itself.
		return "", "", nil
	}
	options := append([]string{dec.Provider}, dec.Alternatives...)
	if e.Choose == nil || !e.interactive() {
		if opts.DryRun {
			// A dry run changes nothing, so it shows the resolver's own
			// choice, marked ambiguous, rather than refusing.
			return "", "", nil
		}
		return "", "", e.refuseAmbiguousInstall(pkg, options)
	}
	question := fmt.Sprintf("%s could be provided by %s. Which provider should own it?",
		pkg.Name, strings.Join(options, ", "))
	chosen, err := e.Choose(question, options)
	return chosen, dec.Provider, err
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
	// Effects are what the dependency installer changed in the project
	// directory and the shell profiles, observed around the run; a planned
	// effect in a dry run.
	Effects []model.Effect `json:"effects"`
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
	pal := env.palOut
	if asJSON {
		out = env.Stderr
		pal = env.palErr
	}
	fmt.Fprintf(out, "%s %s project at %s (via %s)\n", pal.Bold("Project:"), proj.Kind, proj.Root, proj.Marker)

	result := &engine.Result{DryRun: opts.DryRun}
	projectConfig := filepath.Join(proj.Root, config.ProjectFileName)

	deps, why := proj.Dependencies(osexec.LookPath)
	depReport := &dependencyReport{Command: deps, Skipped: why, DryRun: opts.DryRun, Effects: []model.Effect{}}

	// The packages the project declares are installed below, and the
	// dependency installer runs after them; an unattended run that would
	// replace or overwrite anything doing either is refused first.
	var guarded []model.PlannedAction
	if proj.HasProjectConfig {
		if declared, ok := doc.ProjectFileFor(proj.Root); ok {
			var previews []model.PlannedAction
			for _, pkg := range declared.Raw.Packages {
				if version != "" {
					pkg.Version = version
				}
				if channel != "" {
					pkg.Channel = channel
				}
				if preview, err := eng.AddPreview(ctx, doc, pkg, opts); err == nil {
					previews = append(previews, preview)
				}
			}
			guarded = append(guarded, changeActions(model.ActionInstall, previews)...)
		}
	}
	replaced := dependencyReplacements(deps)
	pipEffects, pipWarning := pipEnvironment(deps)
	if pipWarning != nil {
		result.Warnings = append(result.Warnings, *pipWarning)
	}
	if planned := append(append([]model.Effect{}, replaced...), destructiveOnly(pipEffects)...); len(planned) > 0 {
		guarded = append(guarded, model.PlannedAction{
			Action:  model.ActionInstall,
			State:   model.State{Package: model.Package{Name: deps.String()}},
			Note:    "in " + deps.Dir,
			Outcome: model.OutcomePlanned,
			Effects: planned,
		})
	}
	if err := env.guardDestructive("install", opts, guarded); err != nil {
		return nil, nil, err
	}
	if deps != nil {
		if err := env.confirmChanges([]change{{
			verb:    "Run",
			target:  deps.String(),
			detail:  "in " + deps.Dir + " (" + deps.Reason + ")",
			after:   "the project's dependencies are installed by " + deps.Tool + "; the toolchain is recorded in " + projectConfig,
			effects: append(append([]model.Effect{}, pipEffects...), replaced...),
		}}, opts); err != nil {
			return nil, nil, err
		}
		if env.declined() {
			depReport.Skipped = "declined"
			return result, depReport, nil
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
				return result, depReport, err
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
				return result, depReport, err
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
				Code:     model.WarnToolchainMissing,
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
		return result, depReport, nil
	}
	if opts.DryRun {
		fmt.Fprintf(out, "Would run: %s (in %s; %s)\n", deps.String(), deps.Dir, deps.Reason)
		depReport.Effects = append(depReport.Effects, model.Effect{
			Op: model.EffectModify, Kind: model.EffectDirectory, Path: deps.Dir, Evidence: model.EvidencePlanned,
			Detail: deps.Tool + " writes the project's dependencies under this directory",
		})
		depReport.Effects = append(depReport.Effects, pipEffects...)
		depReport.Effects = append(depReport.Effects, replaced...)
		report.WriteEffects(out, pal, depReport.Effects, true, true, "  ")
		return result, depReport, nil
	}
	fmt.Fprintf(out, "Running: %s (in %s)\n", deps.String(), deps.Dir)
	watch := eng.WatchDirs(deps.Dir)
	before := statAll(replaced)
	runErr := env.runInDir(ctx, deps.Dir, out, env.Stderr, deps.Tool, deps.Args...)
	depReport.Effects = append(depReport.Effects, watch.Finish(ctx)...)
	depReport.Effects = append(depReport.Effects, observedReplacements(replaced, before)...)
	report.WriteEffects(out, pal, depReport.Effects, false, env.Verbose, "  ")
	// pip writes to site-packages outside the project, where nothing is
	// watched; what it was going to change is reported as planned (evidence
	// "planned"), not printed as something observed.
	depReport.Effects = append(depReport.Effects, pipEffects...)
	if runErr != nil {
		return result, depReport, fmt.Errorf("installing dependencies with '%s' in %s: %w", deps.String(), deps.Dir, runErr)
	}
	depReport.Ran = true
	return result, depReport, nil
}

// dependencyReplacements is what a dependency install will delete or rewrite
// wholesale, as destructive planned effects.
func dependencyReplacements(deps *project.DependencyInstall) []model.Effect {
	if deps == nil {
		return nil
	}
	var out []model.Effect
	for _, r := range deps.Replaces() {
		out = append(out, model.Effect{
			Op: model.EffectReplace, Kind: model.EffectDirectory, Path: r.Path,
			Destructive: true, Evidence: model.EvidencePlanned, Detail: r.Detail,
		})
	}
	return out
}

// pipEnvironment is where a pip dependency install writes, as a planned
// effect, and the warning an install outside any virtualenv carries.
//
// Inside a virtualenv pip only changes that environment, an ordinary effect.
// Outside one it installs into the Python environment the whole account
// shares (or, as root, the machine's) and can upgrade or downgrade packages
// other software depends on. That is a destructive planned effect, so an
// unattended run needs --allow-destructive; a virtualenv is suggested, never
// required.
func pipEnvironment(deps *project.DependencyInstall) ([]model.Effect, *model.Warning) {
	if deps == nil || !deps.Pip() {
		return nil, nil
	}
	if deps.Virtualenv != "" {
		return []model.Effect{{
			Op: model.EffectModify, Kind: model.EffectDirectory, Path: deps.Virtualenv, Evidence: model.EvidencePlanned,
			Detail: "pip installs requirements.txt into this virtualenv",
		}}, nil
	}
	effect := model.Effect{
		Op: model.EffectReplace, Kind: model.EffectPackage, Name: "the Python packages requirements.txt names",
		Destructive: true, Evidence: model.EvidencePlanned,
		Detail: "outside a virtualenv pip installs into the user's site-packages (as root, the system's) " +
			"and may upgrade or downgrade packages already installed there",
	}
	warning := &model.Warning{
		Severity: model.SeverityWarn, Code: model.WarnPipOutsideVirtualenv,
		Message: "'" + deps.String() + "' runs outside a virtualenv: it installs into your user site-packages " +
			"(as root, the system's) and may upgrade or downgrade packages other software depends on. " +
			"Create one first (python3 -m venv .venv) and Starpoint installs into it instead.",
	}
	return []model.Effect{effect}, warning
}

// destructiveOnly keeps the destructive effects.
func destructiveOnly(effects []model.Effect) []model.Effect {
	var out []model.Effect
	for _, e := range effects {
		if e.Destructive {
			out = append(out, e)
		}
	}
	return out
}

// replacedBefore is what a planned replacement's path held before the run.
type replacedBefore struct {
	info    os.FileInfo
	entries []os.DirEntry
}

// statAll records what each planned replacement's path is before the run.
func statAll(effects []model.Effect) []replacedBefore {
	out := make([]replacedBefore, len(effects))
	for i, eff := range effects {
		out[i].info, _ = os.Lstat(eff.Path)
		out[i].entries, _ = os.ReadDir(eff.Path)
	}
	return out
}

// maxListedRemovals bounds how many removed entries of one replaced
// directory are listed one by one.
const maxListedRemovals = 20

// observedReplacements reports which planned replacements happened: the
// directory is gone, a different one now stands at its path, or entries it
// held are gone (npm ci empties node_modules in place).
func observedReplacements(planned []model.Effect, before []replacedBefore) []model.Effect {
	var out []model.Effect
	for i, eff := range planned {
		if before[i].info == nil {
			continue
		}
		eff.Evidence = model.EvidenceObserved
		after, err := os.Lstat(eff.Path)
		switch {
		case err != nil:
			eff.Op = model.EffectDelete
			out = append(out, eff)
			continue
		case !os.SameFile(before[i].info, after):
			eff.Op = model.EffectReplace
			out = append(out, eff)
			continue
		}
		var gone []os.DirEntry
		for _, entry := range before[i].entries {
			if _, err := os.Lstat(filepath.Join(eff.Path, entry.Name())); err != nil {
				gone = append(gone, entry)
			}
		}
		for j, entry := range gone {
			if j == maxListedRemovals {
				out = append(out, model.Effect{Op: model.EffectModify, Kind: model.EffectDirectory, Path: eff.Path,
					Destructive: true, Evidence: model.EvidenceObserved,
					Detail: fmt.Sprintf("%d more entries were removed from this directory", len(gone)-maxListedRemovals)})
				break
			}
			kind := model.EffectFile
			if entry.IsDir() {
				kind = model.EffectDirectory
			}
			out = append(out, model.Effect{Op: model.EffectDelete, Kind: kind,
				Path: filepath.Join(eff.Path, entry.Name()), Destructive: true, Evidence: model.EvidenceObserved,
				Detail: "removed by the dependency installer: " + eff.Detail})
		}
	}
	return out
}

// runInDir runs a command in a directory with its output streamed, which is
// what a dependency install needs: it can take minutes and its progress is the
// user's only sign of life.
func (e *Env) runInDir(ctx context.Context, dir string, stdout, stderr io.Writer, name string, args ...string) error {
	if e.RunInDir != nil {
		return e.RunInDir(ctx, dir, stdout, stderr, name, args...)
	}
	id := e.projectIdentity(dir)
	cmd, err := exec.Command(ctx, id, name, args...)
	if err != nil {
		if exec.IsDropError(err) {
			return err
		}
		return fmt.Errorf("%s is not on PATH; install it (for example 'starpoint install %s') and run this again", name, name)
	}
	if id == nil && e.privileges != nil && e.privileges.RootEnv != nil {
		// The installer stays root (the project is not the invoking user's),
		// so it keeps its caches in root's home, not the user's.
		cmd.Env = append([]string(nil), e.privileges.RootEnv...)
	}
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
	var previews []model.PlannedAction
	for _, raw := range names {
		pkg := model.ParseSpec(raw)
		resolution := env.planRemoval(ctx, eng, doc, opts, pkg)
		// A removal that would be refused is refused by Remove itself, with
		// its own message; the preview only adds what it can describe.
		preview, previewErr := eng.RemovePreview(ctx, doc, raw)
		changes = append(changes, removeChange(pkg, resolution, preview))
		if previewErr == nil {
			if preview.State.Package.Name == "" {
				preview.State = model.State{Package: pkg, Res: resolution}
			}
			if len(preview.Effects) == 0 {
				// Nothing could be described, but removing is still removing.
				preview.Effects = []model.Effect{packageDelete(pkg.Name, resolution.Provider)}
			}
			previews = append(previews, preview)
		}
	}
	if err := env.guardDestructive("remove", opts, changeActions(model.ActionRemove, previews)); err != nil {
		return err
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
			if res != nil {
				results = append(results, res)
			}
			env.auditActions("remove", dryRun, results...)
			if len(results) > 0 {
				_ = w.Operation("remove", dryRun, results, nil)
			}
			return err
		}
		results = append(results, res)
	}
	env.auditActions("remove", dryRun, results...)
	return w.Operation("remove", dryRun, results, nil)
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
		case "--no-confirm", "-y", "--yes":
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

	if err := env.guardDestructive("transition", opts, transitionActions(ctx, eng, selected.transitions)); err != nil {
		return err
	}
	if !dryRun && !noConfirm && env.Confirm != nil {
		if err := confirmTransitions(ctx, env, eng, selected); err != nil {
			return err
		}
		if !env.lastConfirm {
			// A refusal is a decision to change nothing. Falling through here
			// would perform the very work the user just declined.
			fmt.Fprintln(env.Stdout, env.palOut.Dim("Nothing was changed."))
			return w.Transitions(selected.transitions, true)
		}
		opts.NoConfirm = true
	}

	var applied []model.Transition
	var errs []error
	var failures []string
	for _, st := range selected.states {
		t, err := eng.ExecuteTransition(ctx, st, opts)
		applied = append(applied, t)
		errs = append(errs, err)
		env.auditTransition(t, err)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", t.Package.Name, err))
		}
	}
	if err := w.TransitionsApplied("transition", applied, errs, dryRun); err != nil {
		return err
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
func confirmTransitions(ctx context.Context, env *Env, eng *engine.Engine, sel transitionSelection) error {
	fmt.Fprintln(env.promptOut(), env.palPrompt.Bold("These provider transitions will be performed:"))
	for _, t := range sel.transitions {
		from := t.From.Name
		if from == "" {
			from = "nothing"
		}
		fmt.Fprintf(env.promptOut(), "  %s: %s -> %s (currently %s)\n",
			env.palPrompt.Bold(t.Package.Name), from, t.To.Name, report.PhaseLabel(env.palPrompt, t.Phase))
		if t.Advice != "" {
			fmt.Fprintf(env.promptOut(), "      %s\n", env.palPrompt.Dim(t.Advice))
		}
		effects, warnings := eng.PlannedTransitionEffects(ctx, t)
		// Every effect: the question must not hide a change behind a count.
		report.WriteEffects(env.promptOut(), env.palPrompt, effects, true, true, "      ")
		for _, w := range warnings {
			fmt.Fprintf(env.promptOut(), "      %s [%s]: %s\n", report.SeverityLabel(env.palPrompt, w.Severity), w.Code, w.Message)
		}
	}
	fmt.Fprintln(env.promptOut(), "\nThe previous installation is only removed once the new one is confirmed working.")
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
	// update must not do, and so does a run without a terminal: a `yes` piped
	// into stdin answers without having read the question, and an empty stdin
	// has no answer to give.
	askPins := !opts.DryRun && !opts.NoConfirm && env.Confirm != nil && env.interactive()
	if askPins {
		for _, c := range previewRes.PinConflicts {
			fmt.Fprintf(env.promptOut(), "%s\n", env.palPrompt.Yellow(fmt.Sprintf(
				"!!! There is a new version available for %s (%s), but config says %s",
				c.Package, c.Available, c.Pinned)))
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
		// A pin the user agreed to move turns into an update, which replaces
		// the installed copy and rewrites the configuration. The preview was
		// taken before that answer, so it is taken again: the confirmation and
		// the destructive guard below must see the update that will run.
		if len(opts.UpdatePinned) > 0 {
			if previewRes, err = eng.UpdatePreview(ctx, doc, opts); err != nil {
				return err
			}
		}
	}

	var preview []change
	for _, a := range previewRes.Applied {
		preview = append(preview, change{
			verb:     "Update",
			target:   a.State.Package.Spec(),
			via:      a.State.Res.Provider,
			detail:   a.Note,
			effects:  a.Effects,
			warnings: a.Warnings,
		})
	}
	if self {
		preview = append(preview, change{
			verb:   "Update",
			target: "Starpoint itself",
			detail: "reinstalls the running version of Starpoint",
		})
	}
	if err := env.guardDestructive("update", opts, changeActions(model.ActionUpdate, previewRes.Applied)); err != nil {
		return err
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
	if warn := env.elevationWarning(); warn != nil && !w.JSON() {
		fmt.Fprintf(env.Stdout, "%s [%s]: %s\n", report.SeverityLabel(env.palOut, warn.Severity), warn.Code, warn.Message)
	}
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
		fmt.Fprintf(env.Stdout, "%s\n", env.palOut.Yellow(fmt.Sprintf("!!! There is a new version available for %s (%s), but config says %s; %s",
			c.Package, c.Available, c.Pinned, outcome)))
	}
	for _, a := range res.Applied {
		verb := "updated"
		switch {
		case dryRun:
			verb = "would update"
		case a.Outcome == model.OutcomeFailed:
			verb = "failed to update"
		}
		line := strings.TrimSpace(actionLine(verb, a))
		switch {
		case dryRun:
			line = env.palOut.Yellow(line)
		case a.Outcome == model.OutcomeFailed:
			line = env.palOut.Red(line)
		default:
			line = env.palOut.Green(line)
		}
		fmt.Fprintln(env.Stdout, line)
		report.WriteEffects(env.Stdout, env.palOut, a.Effects, dryRun, dryRun || env.Verbose, "")
	}
	for _, warn := range res.Warnings {
		if warn.Code == model.WarnPinConflict {
			continue // already shown as a pin conflict above
		}
		fmt.Fprintf(env.Stdout, "  %s\n", report.SeverityText(env.palOut, warn.Severity, warn.Message))
	}
	if len(res.Applied) == 0 && len(res.Warnings) == 0 {
		fmt.Fprintln(env.Stdout, env.palOut.Dim("Nothing to update."))
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
		return nil, fmt.Errorf("starpoint is provided by %q, which this build cannot update", found.Installation.Provider)
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
	fmt.Fprintf(e.Stderr, "%s %s\n", e.palErr.Yellow("warning:"), msg)
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
		fmt.Fprintln(env.Stdout, env.palOut.Dim("No execution history found."))
		return nil
	}

	fmt.Fprintln(env.Stdout, env.palOut.Bold("Execution History:"))
	fmt.Fprintln(env.Stdout, "")
	fmt.Fprintf(env.Stdout, "%-20s %-15s %-20s\n",
		env.palOut.Bold(fmt.Sprintf("%-20s", "Package")),
		env.palOut.Bold(fmt.Sprintf("%-15s", "Directory")),
		env.palOut.Bold("Timestamp"))
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
	if err := env.guardDestructive("prune", opts, changeActions(model.ActionRemove, preview.Applied)); err != nil {
		return err
	}
	if !dryRun && len(preview.Applied) > 0 && !opts.NoConfirm {
		if env.Confirm == nil {
			return errors.New("prune requires confirmation; use --no-confirm to proceed")
		}
		// Every candidate, and everything that would go with it, is listed
		// before the question. A count alone is not consent to a deletion.
		// Under --json the list goes to stderr so stdout stays one document.
		listing := env.promptOut()
		fmt.Fprintln(listing, env.palPrompt.Bold("These packages are not declared in any configuration and would be removed:"))
		for _, a := range preview.Applied {
			fmt.Fprintf(listing, "  %s via %s\n", a.State.Package.Name, a.State.Res.Provider)
			report.WriteEffects(listing, env.palPrompt, a.Effects, true, true, "    ")
		}
		for _, warn := range preview.Warnings {
			if warn.Code == model.WarnPruneSkipped {
				fmt.Fprintf(listing, "  %s\n", env.palPrompt.Dim(warn.Message))
			}
		}
		ok, err := env.Confirm(fmt.Sprintf("Remove %d orphaned user-scoped package(s)?", len(preview.Applied)))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(env.Stdout, env.palOut.Dim("Nothing was changed."))
			return nil
		}
	}
	res := preview
	var pruneErr error
	if !dryRun {
		res, pruneErr = eng.Prune(ctx, doc, opts)
		if res == nil {
			return pruneErr
		}
	}
	env.auditActions("prune", dryRun, res)
	// What was removed before a failure is still reported.
	if err := w.Prune(res, dryRun); err != nil {
		return err
	}
	return pruneErr
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
			fmt.Fprintln(env.Stdout, env.palOut.Dim("Nothing was changed."))
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

	var reversing []model.Transition
	for i := range selected {
		reversing = append(reversing, eng.TransitionFor(ctx, selected[i], opts))
	}
	if err := env.guardDestructive("transition", opts, transitionActions(ctx, eng, reversing)); err != nil {
		return err
	}

	// Show what will be reversed
	fmt.Fprintln(env.Stdout, env.palOut.Bold("The following transitions will be reversed:"))
	for i, t := range toReverse {
		fmt.Fprintf(env.Stdout, "  %s: %s -> %s (reversing transition from %s)\n",
			env.palOut.Bold(t.Package.Name), t.To.Name, t.From.Name,
			time.Unix(t.Timestamp, 0).Format("2006-01-02 15:04:05"))
		if t.Note != "" {
			fmt.Fprintf(env.Stdout, "      %s\n", env.palOut.Dim(t.Note))
		}
		effects, warnings := eng.PlannedTransitionEffects(ctx, reversing[i])
		// The list the reversal is confirmed against, so it is never collapsed
		// into a count.
		report.WriteEffects(env.Stdout, env.palOut, effects, true, true, "      ")
		for _, w := range warnings {
			fmt.Fprintf(env.Stdout, "      %s [%s]: %s\n", report.SeverityLabel(env.palOut, w.Severity), w.Code, w.Message)
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
			fmt.Fprintln(env.Stdout, env.palOut.Dim("Nothing was changed."))
			return nil
		}
	}

	// Perform the reverse transitions
	var applied []model.Transition
	var errs []error
	var failures []string
	for _, st := range selected {
		t, err := eng.ExecuteTransition(ctx, st, opts)
		applied = append(applied, t)
		errs = append(errs, err)
		if !dryRun {
			env.auditTransition(t, err)
		}
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s", t.Package.Name, err))
		}
	}

	if err := w.TransitionsApplied("transition-reverse", applied, errs, dryRun); err != nil {
		return err
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
