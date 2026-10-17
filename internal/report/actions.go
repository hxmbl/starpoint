package report

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/color"
	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/model"
)

// ActionResult is one applied or planned action, in the one shape every
// command that changes anything reports.
//
// The first five fields are the version 4 shape. Version 5 adds what the
// action did or would do: its outcome, the provider and version moves it
// brings about, the configuration entries it edits, every effect on the
// machine, and the warnings that concern it. Destructive is true when any
// effect or configuration change removes or overwrites something.
type ActionResult struct {
	Action   string `json:"action"`
	Package  string `json:"package"`
	Provider string `json:"provider,omitempty"`
	Note     string `json:"note,omitempty"`
	// Result is the action's outcome: planned, applied, failed, reported or
	// not_run.
	Result      string `json:"result,omitempty"`
	DryRun      bool   `json:"dry_run"`
	Destructive bool   `json:"destructive"`
	Error       string `json:"error,omitempty"`
	// Transitions are the provider and version moves, in compact form.
	Transitions   []model.ActionTransition `json:"transitions"`
	ConfigChanges []model.ConfigChange     `json:"config_changes"`
	Effects       []model.Effect           `json:"effects"`
	Warnings      []model.Warning          `json:"warnings"`
	// RunAs (version 7) is the account each provider involved ran as, or
	// will run as: user, uid, gid, whether Starpoint was elevated, whether
	// it dropped privileges for the provider, and why. Empty for an action
	// that runs no provider, such as recording a package that is already
	// installed.
	RunAs []model.RunAs `json:"run_as"`
}

// Summary condenses an operation's actions. It holds counts and names only;
// the paths and details live in the actions, once.
type Summary struct {
	Actions  int            `json:"actions"`
	ByAction map[string]int `json:"by_action"`
	ByResult map[string]int `json:"by_result"`
	Effects  int            `json:"effects"`
	// Destructive is true when any action is.
	Destructive        bool `json:"destructive"`
	DestructiveEffects int  `json:"destructive_effects"`
	// DestructivePackages names the packages whose actions are destructive.
	DestructivePackages []string `json:"destructive_packages"`
	// Warnings counts the top-level warnings by severity.
	Warnings map[string]int `json:"warnings"`
	// ConfigFiles lists the configuration files the actions edit.
	ConfigFiles []string `json:"config_files"`
}

func warningKey(w model.Warning) string {
	return strings.Join([]string{w.Code, string(w.Severity), w.Package, w.Provider, w.Message}, "\x00")
}

// mergeWarnings appends warnings not already present.
func mergeWarnings(dst []model.Warning, more ...model.Warning) []model.Warning {
	seen := map[string]bool{}
	for _, w := range dst {
		seen[warningKey(w)] = true
	}
	for _, w := range more {
		if seen[warningKey(w)] {
			continue
		}
		seen[warningKey(w)] = true
		dst = append(dst, w)
	}
	return dst
}

// buildActions turns engine actions into ActionResults and the operation's
// warnings.
//
// The invariant a consumer can rely on: every warning on an action also
// appears in the top-level list, and an action's list holds every top-level
// warning that names its package.
func buildActions(dryRun bool, global []model.Warning, actions []model.PlannedAction, runAs RunAsFunc) ([]ActionResult, []model.Warning) {
	all := mergeWarnings(nil, global...)
	for _, a := range actions {
		all = mergeWarnings(all, a.Warnings...)
	}
	out := make([]ActionResult, 0, len(actions))
	for _, a := range actions {
		ar := actionResult(a)
		ar.DryRun = dryRun
		if runAs != nil {
			ar.RunAs = append(ar.RunAs, runAs(a)...)
		}
		if ar.Result == "" {
			ar.Result = string(model.OutcomeApplied)
			if dryRun {
				ar.Result = string(model.OutcomePlanned)
			}
		}
		ws := mergeWarnings([]model.Warning{}, a.Warnings...)
		for _, w := range all {
			if w.Package != "" && w.Package == a.State.Package.Name {
				ws = mergeWarnings(ws, w)
			}
		}
		ar.Warnings = ws
		out = append(out, ar)
	}
	return out, all
}

func actionResult(a model.PlannedAction) ActionResult {
	ar := ActionResult{
		Action:        string(a.Action),
		Package:       a.State.Package.Name,
		Provider:      a.State.Res.Provider,
		Note:          a.Note,
		Result:        string(a.Outcome),
		Error:         a.Error,
		Destructive:   a.Destructive(),
		Transitions:   append([]model.ActionTransition{}, a.Changes...),
		RunAs:         []model.RunAs{},
		ConfigChanges: append([]model.ConfigChange{}, a.ConfigChanges...),
		Effects:       append([]model.Effect{}, a.Effects...),
		Warnings:      append([]model.Warning{}, a.Warnings...),
	}
	return ar
}

// summarize counts actions and warnings.
func summarize(actions []ActionResult, warnings []model.Warning) Summary {
	s := Summary{
		ByAction:            map[string]int{},
		ByResult:            map[string]int{},
		Warnings:            map[string]int{},
		DestructivePackages: []string{},
		ConfigFiles:         []string{},
	}
	destructive := map[string]bool{}
	files := map[string]bool{}
	for _, a := range actions {
		s.Actions++
		s.ByAction[a.Action]++
		s.ByResult[a.Result]++
		s.Effects += len(a.Effects)
		for _, e := range a.Effects {
			if e.Destructive {
				s.DestructiveEffects++
			}
		}
		if a.Destructive {
			s.Destructive = true
			destructive[a.Package] = true
		}
		for _, c := range a.ConfigChanges {
			files[c.Path] = true
		}
	}
	for name := range destructive {
		s.DestructivePackages = append(s.DestructivePackages, name)
	}
	for f := range files {
		s.ConfigFiles = append(s.ConfigFiles, f)
	}
	sort.Strings(s.DestructivePackages)
	sort.Strings(s.ConfigFiles)
	for _, w := range warnings {
		s.Warnings[string(w.Severity)]++
	}
	return s
}

// collect gathers the actions and warnings of several results.
func collect(results []*engine.Result) ([]model.PlannedAction, []model.Warning) {
	var actions []model.PlannedAction
	var warnings []model.Warning
	for _, r := range results {
		if r == nil {
			continue
		}
		actions = append(actions, r.Applied...)
		warnings = mergeWarnings(warnings, r.Warnings...)
	}
	return actions, warnings
}

// operationFields is the response shared by every command that changes
// anything: what it did, what it warned about, and the summary.
func (w *Writer) operationFields(op string, dryRun bool, results []*engine.Result) map[string]any {
	actions, warnings := collect(results)
	warnings = mergeWarnings(append([]model.Warning{}, w.context...), warnings...)
	ars, all := buildActions(dryRun, warnings, actions, w.runAs)
	changed := false
	path := ""
	for _, r := range results {
		if r != nil && r.ConfigChanged && !dryRun {
			changed = true
			if path == "" {
				path = r.ConfigPath
			}
		}
	}
	if path == "" {
		for _, r := range results {
			if r != nil && r.ConfigPath != "" {
				path = r.ConfigPath
				break
			}
		}
	}
	if all == nil {
		all = []model.Warning{}
	}
	return map[string]any{
		"operation":      op,
		"dry_run":        dryRun,
		"config_changed": changed,
		"config_path":    path,
		"actions":        ars,
		"warnings":       all,
		"summary":        summarize(ars, all),
	}
}

// ActionsFromResults flattens the engine results from a multi-step command
// into the documented action list.
func ActionsFromResults(results ...*engine.Result) []ActionResult {
	actions, warnings := collect(results)
	dryRun := false
	for _, r := range results {
		if r != nil && r.DryRun {
			dryRun = true
		}
	}
	ars, _ := buildActions(dryRun, warnings, actions, nil)
	return ars
}

// RefusedPlan is the plan of a command Starpoint refused to run: the actions
// it would have carried out, in the ActionResult shape, with the warnings and
// summary an operation carries. Every action's result is "planned".
func RefusedPlan(actions []model.PlannedAction, runAs RunAsFunc) map[string]any {
	ars, ws := buildActions(true, nil, actions, runAs)
	if ws == nil {
		ws = []model.Warning{}
	}
	return map[string]any{
		"actions":  ars,
		"warnings": ws,
		"summary":  summarize(ars, ws),
	}
}

// Operation renders the result of a command that changes the machine:
// install, remove and install . share it, and extra carries what only one of
// them has (the provider lookup, the dependency step).
func (w *Writer) Operation(op string, dryRun bool, results []*engine.Result, extra map[string]any) error {
	if w.asJSON {
		fields := w.operationFields(op, dryRun, results)
		for k, v := range extra {
			fields[k] = v
		}
		return w.respond(fields)
	}
	for _, r := range results {
		if r == nil {
			continue
		}
		if err := w.Result(op, r); err != nil {
			return err
		}
	}
	return nil
}

// SetContextWarnings adds warnings about the run itself, such as running
// elevated, to every operation response.
func (w *Writer) SetContextWarnings(ws ...model.Warning) { w.context = append(w.context, ws...) }

// RunAsFunc reports the accounts an action's providers run as.
type RunAsFunc func(a model.PlannedAction) []model.RunAs

// SetRunAs makes every action report the accounts its providers ran as.
func (w *Writer) SetRunAs(f RunAsFunc) { w.runAs = f }

// SetVerbose asks the human renderer to list every effect, not only the
// destructive ones.
func (w *Writer) SetVerbose(v bool) { w.verbose = v }

// Verbose reports whether every effect is listed.
func (w *Writer) Verbose() bool { return w.verbose }

// writeContextWarnings prints warnings about the run itself, once.
func (w *Writer) writeContextWarnings() {
	if w.contextShown {
		return
	}
	w.contextShown = true
	for _, warn := range w.context {
		fmt.Fprintf(w.out, "%s [%s]: %s\n", w.severity(warn.Severity), warn.Code, warn.Message)
	}
}

// WriteEffects prints an action's effects, indented, for humans.
//
// What is shown scales with what matters: destructive effects are always
// listed, because a deletion or overwrite is never something to discover
// later. With all set (a dry run, whose whole job is showing the effect, or
// --verbose) everything is listed; otherwise the rest is counted. planned
// words the lines as predictions ("would delete").
//
// p styles the destructive marker. It is a palette rather than a resolved
// mode so the confirmation prompts, the refusals and the results themselves
// can agree on whether color is in effect; a palette that is off writes
// exactly what it wrote before.
func WriteEffects(out interface{ Write([]byte) (int, error) }, p color.Palette, effects []model.Effect, planned, all bool, indent string) {
	hidden := 0
	for _, e := range effects {
		if !e.Destructive && !all {
			hidden++
			continue
		}
		marker := "  "
		if e.Destructive {
			marker = p.BoldRed("! ")
		}
		fmt.Fprintf(out, "%s%s%s\n", indent, marker, EffectLine(e, planned))
	}
	if hidden > 0 {
		fmt.Fprintf(out, "%s  %s\n", indent,
			p.Dim(fmt.Sprintf("(%d other change(s); --verbose lists them)", hidden)))
	}
}

// EffectLine renders one effect as a phrase.
func EffectLine(e model.Effect, planned bool) string {
	verb := func(past, future string) string {
		if planned {
			return "would " + future
		}
		return past
	}
	var line string
	switch {
	case e.Op == model.EffectChown:
		line = fmt.Sprintf("%s %s %s to %s", verb("gave", "give"), strings.ReplaceAll(string(e.Kind), "_", " "), tildePath(e.Path), e.To)
		if e.From != "" {
			line += " (was " + e.From + ")"
		}
		return withFixDetail(line, e)
	case e.Op == model.EffectChmod:
		line = fmt.Sprintf("%s permissions of %s %s", verb("widened", "widen"), strings.ReplaceAll(string(e.Kind), "_", " "), tildePath(e.Path))
		if e.From != "" {
			line += " from " + e.From + " to " + e.To
		}
		return withFixDetail(line, e)
	}
	switch e.Kind {
	case model.EffectPackage:
		version := firstNonEmpty(e.From, e.To)
		subject := e.Name
		if version != "" && e.Op != model.EffectModify {
			subject += " " + version
		}
		switch e.Op {
		case model.EffectCreate:
			line = verb("installed", "install") + " " + subject
		case model.EffectDelete:
			line = verb("uninstalled", "uninstall") + " " + subject
		default:
			line = verb("changed", "change") + " " + subject + " " + dash(e.From) + " → " + dash(e.To)
		}
		if e.Provider != "" {
			line += " (" + e.Provider + ")"
		}
		if e.Path != "" {
			line += " at " + tildePath(e.Path)
		}
	case model.EffectCommand:
		switch e.Op {
		case model.EffectCreate:
			line = fmt.Sprintf("%s %s resolve to %s", verb("made", "make"), e.Name, tildePath(e.To))
		case model.EffectDelete:
			if planned {
				line = fmt.Sprintf("%s would stop resolving to %s", e.Name, tildePath(e.From))
			} else {
				line = fmt.Sprintf("%s no longer resolves on PATH (was %s)", e.Name, tildePath(e.From))
			}
		default:
			line = fmt.Sprintf("%s %s from %s to %s", verb("moved", "move"), e.Name, tildePath(e.From), tildePath(e.To))
		}
	case model.EffectConfig:
		line = fmt.Sprintf("%s %s: %s", verb("edited", "edit"), tildePath(e.Path), e.Detail)
	default:
		var v string
		switch e.Op {
		case model.EffectCreate:
			v = verb("created", "create")
		case model.EffectDelete:
			v = verb("deleted", "delete")
		case model.EffectReplace:
			v = verb("replaced", "replace")
		default:
			v = verb("modified", "modify")
		}
		line = fmt.Sprintf("%s %s %s", v, strings.ReplaceAll(string(e.Kind), "_", " "), tildePath(e.Path))
		if e.Kind == model.EffectSymlink {
			if t := firstNonEmpty(e.To, e.From); t != "" {
				line += " → " + tildePath(t)
			}
		}
	}
	if e.Evidence == model.EvidenceReported && e.Kind != model.EffectPackage {
		line += " (reported by the provider)"
	}
	return line
}

// withFixDetail adds how many entries an ownership or permission fix covered,
// and why it failed, to its line.
func withFixDetail(line string, e model.Effect) string {
	if i := strings.Index(e.Detail, "; "); i >= 0 {
		line += " (" + strings.TrimPrefix(e.Detail[i:], "; ") + ")"
	}
	if e.Error != "" {
		line += " — failed: " + e.Error
	}
	return line
}

// tildePath shortens the home directory to ~ for display.
func tildePath(p string) string {
	if p == "" {
		return "-"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" || home == string(filepath.Separator) {
		return p
	}
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(filepath.Separator)) {
		return "~" + p[len(home):]
	}
	return p
}

// writeActionWarnings prints the warnings that concern one action.
func (w *Writer) writeActionWarnings(ws []model.Warning, indent string) {
	for _, warn := range ws {
		if warn.Severity == model.SeverityInfo && !w.verbose {
			continue
		}
		code := ""
		if warn.Code != "" {
			code = " [" + warn.Code + "]"
		}
		fmt.Fprintf(w.out, "%s%s%s: %s\n", indent, w.severity(warn.Severity), code, warn.Message)
	}
}
