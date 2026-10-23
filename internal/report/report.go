// Package report renders results for humans and for machines.
//
// Machine-readable output is a first-class path, not a wrapper around the human
// text. Another tool that wants to know what Starpoint thinks should read the
// same structured data the engine produced, never parse a table.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/color"
	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/locator"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/project"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/resolve"
)

// Writer renders results.
type Writer struct {
	out    io.Writer
	asJSON bool
	// p is the palette human output is styled with. It starts resolved for
	// auto against out, so a Writer used without a --color flag still colors a
	// terminal and still writes nothing extra into a pipe. SetColor re-resolves
	// it from the flag.
	p color.Palette
	// verbose lists every effect in human output, not only destructive ones.
	verbose bool
	// runAs reports the accounts an action's providers ran as.
	runAs RunAsFunc
	// context are warnings about the run itself (running elevated), carried
	// by every operation response.
	context      []model.Warning
	contextShown bool
	// dryRunTransitions renders transition effects as predictions.
	dryRunTransitions bool
	// transitionDetail lists each transition's effects and warnings, for the
	// transition command; sync shows them under its actions instead.
	transitionDetail bool
}

// New builds a Writer.
func New(out io.Writer, asJSON bool) *Writer {
	return &Writer{out: out, asJSON: asJSON, p: color.New(color.Auto, out)}
}

// SetColor resolves the color mode against this Writer's output. It is how
// --color reaches the human renderer; without it a Writer colors a terminal
// and nothing else.
func (w *Writer) SetColor(mode color.Mode) { w.p = color.New(mode, w.out) }

// Palette is the palette human output is styled with. The confirmation prompts
// and the error paths outside this package ask for it so they match the result
// they sit next to.
func (w *Writer) Palette() color.Palette { return w.p }

// severity colors a warning severity. Error is the one that means the machine
// or the configuration is in a bad state, warning is the one asking for a
// decision, and info is context.
func (w *Writer) severity(s model.Severity) string { return SeverityLabel(w.p, s) }

// colored colors a line by severity, for text that already carries its meaning
// as words.
func (w *Writer) colored(s model.Severity, text string) string {
	return SeverityText(w.p, s, text)
}

// SeverityLabel renders a warning severity in the color it is reported with
// everywhere else. The confirmation prompts, the refusals and the update
// command print warnings too, and they should not disagree with the results
// about what a warning looks like.
func SeverityLabel(p color.Palette, s model.Severity) string {
	return SeverityText(p, s, string(s))
}

// SeverityText colors text by the severity it reports.
func SeverityText(p color.Palette, s model.Severity, text string) string {
	switch s {
	case model.SeverityError:
		return p.BoldRed(text)
	case model.SeverityWarn:
		return p.BoldYellow(text)
	default:
		return p.Cyan(text)
	}
}

// status colors a package's status. Green has to mean "matches the
// configuration" and nothing else, or it stops meaning that.
func (w *Writer) status(s model.Status) string {
	switch s {
	case model.StatusCorrect:
		return w.p.Green(string(s))
	case model.StatusUnpinnedConflict:
		return w.p.BoldRed(string(s))
	case model.StatusUnmanaged:
		return w.p.Cyan(string(s))
	case model.StatusMissing, model.StatusProviderDiffers, model.StatusVersionDiffers:
		return w.p.Yellow(string(s))
	default:
		return string(s)
	}
}

// phase colors how far a transition has got.
func (w *Writer) phase(p model.TransitionPhase) string { return PhaseLabel(w.p, p) }

// PhaseLabel colors how far a transition has got, for callers outside the
// report package — the confirmation prompts and the reverse listing.
func PhaseLabel(p color.Palette, phase model.TransitionPhase) string {
	switch phase {
	case model.TransitionCompleted, model.TransitionVerified:
		return p.Green(string(phase))
	case model.TransitionFailed:
		return p.BoldRed(string(phase))
	case model.TransitionBlocked:
		return p.BoldYellow(string(phase))
	default:
		return p.Yellow(string(phase))
	}
}

// outcome colors a line by how its action went. The color repeats what the
// word already says, so a reader who cannot see it still has the word.
func (w *Writer) outcome(line, result string) string {
	switch result {
	case string(model.OutcomeFailed), string(model.OutcomeNotRun):
		return w.p.Red(line)
	case string(model.OutcomePlanned):
		return w.p.Yellow(line)
	default:
		return w.p.Green(line)
	}
}

// padTo right-pads styled text to a width measured without its escapes.
func padTo(s string, width int) string {
	if n := width - color.VisibleLen(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

// label renders a two-column field label, bold so the shape of a block of
// output reads at a glance.
func (w *Writer) label(s string) string { return w.p.Bold(s) }

// yesNo renders a boolean as a word, colored so a column of them scans.
func (w *Writer) yesNo(b bool) string {
	if b {
		return w.p.Green("yes")
	}
	return w.p.Red("no")
}

// existsLabel renders whether the configuration file is there.
func (w *Writer) existsLabel(b bool) string {
	if b {
		return w.p.Green("exists")
	}
	return w.p.Red("missing")
}

// section renders a section heading.
func (w *Writer) section(s string) string { return w.p.Bold(s) }

// field writes one row of a two-column detail block: a bold label padded to
// the block's shared column, then the value. The padding is measured without
// the escapes, so the column holds with color on and is unchanged with it off.
func (w *Writer) field(b *strings.Builder, label, value string) {
	w.fieldAt(b, 19, label, value)
}

// fieldAt is field for a block whose label column is a different width.
func (w *Writer) fieldAt(b *strings.Builder, width int, label, value string) {
	fmt.Fprintf(b, "  %s%s\n", padTo(w.label(label), width), value)
}

// JSON reports whether machine-readable output is requested.
func (w *Writer) JSON() bool { return w.asJSON }

// SchemaVersion is the version of the machine-readable response envelope. It is
// independent of the TOML configuration `schema` field.
//
// Version 3 adds the logging surface: `logging`, `logging prune` and
// `logging config`. Every existing command's payload is unchanged, so this bump
// is conservative rather than forced: it is a new command, not a reshaped one.
// A consumer that pins the version should widen its check rather than change how
// it reads existing commands.
//
// Version 2 flattens every command payload into the top-level object.
// Version 1 documented that same flat shape, but the implementation nested some
// commands under their own key ("sync", "doctor", "explain") and others under
// "data", so no consumer could rely on it. Version 2 makes the documented shape
// the actual shape.
//
// Version 3 added the logging surface: `logging`, `logging prune` and
// `logging config`. Every existing command's payload was unchanged, so this bump
// was conservative rather than forced: it is a new command, not a reshaped one.
// A consumer that pins the version should widen its check rather than change how
// it reads existing commands.
//
// Version 4 unifies the action vocabulary. `install`, `remove` and
// `install .` used to expose the engine's own `Result` under an `operations`
// key, with each entry carrying the full `State` (desired, current, resolution,
// status). `sync`, `update` and `prune` already emitted the documented
// `ActionResult` shape under `actions`. The two described the same kind of thing
// differently, so a consumer had to know which command it was reading. Version 4
// makes every command that performed changes emit `actions` as a list of
// `ActionResult`, and drops `operations` and the nested `applied` entirely.
// `install .` flattens the several results it runs into one list rather than
// nesting them. This is a breaking change; nothing here is in wide use yet.
//
// Version 5 makes every action say what it did. Each ActionResult gains its
// outcome, its provider and version transitions, the configuration entries it
// edits, every effect on the machine (paths created, modified, replaced or
// deleted, packages installed or uninstalled, commands that start or stop
// resolving, shell profiles that changed) with a destructive flag, and the
// structured warnings that concern it. Warnings gain a stable code. Every
// command that changes anything carries the same top-level operation fields
// (operation, dry_run, config_changed, config_path, actions, warnings,
// summary), so install and remove report warnings and dry runs again, and
// transition reports actions. Old fields keep their meaning except `result`,
// which was always empty and now carries the outcome.
//
// Version 6 adds refusals. A command refused by a safety rule (an unattended
// run that would delete or replace something without --allow-destructive, a
// changing command run as root without --allow-root) exits 3 or 4 and its
// error document gains a stable `code`, the `exit_code`, and, when it had a
// plan, `refused`: the operation and the actions it would have carried out,
// in the ActionResult shape with every planned effect. Effects gain the `chown`
// and `chmod` operations for the ownership and permission fixes an elevated
// run makes. Every existing field keeps its meaning.
//
// Version 7 says who each action ran as. Every ActionResult gains `run_as`, a
// list with one entry per provider the action runs (the target provider of a
// relocation first, then the one it moves away from): provider, user, uid,
// gid, elevated, dropped, and for an elevated run on behalf of an invoking
// user the provider's privilege ("user" or "root") and the reason. A
// transition gains the same `run_as`. Effects gain `unexpected`, set on an
// ownership fix that should not have been needed because the provider ran as
// the invoking user; it raises the `root-owned-after-drop` warning. Refusals
// gain the codes `invoking-user-unresolved` and `privilege-drop-failed`.
// Every existing field keeps its meaning.
//
// Version 8 says what prune left alone and when a pip install is
// destructive. Prune reports every orphan it does not remove (one outside
// the home directory, or a package manager) as a `prune-skipped` warning
// naming the package, its provider and the reason, where version 7 dropped
// them silently. `install .` reports a requirements.txt install with pip as a
// planned effect: inside a virtualenv a `modify` of that environment, which
// the dependency command names as `virtualenv`; outside one a destructive
// `replace` of Python packages with a `pip-outside-virtualenv` warning, which
// an unattended run refuses without --allow-destructive. Every existing field
// keeps its meaning.
const SchemaVersion = 8

// respond emits one flat JSON object: schema_version plus the given fields.
//
// Every command response goes through here so the envelope cannot drift from
// command to command, which is what made the version 1 contract unusable.
func (w *Writer) respond(fields map[string]any) error {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["schema_version"] = SchemaVersion
	return w.emit(fields)
}

// flatten exposes a value's JSON fields as a map so a struct payload can be
// merged into the response envelope. Values that do not marshal to an object
// (a bare slice, a scalar) are nested under "data" rather than dropped.
func flatten(v any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{"data": v}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{"data": v}
	}
	return m
}

// ListItem is one row of `starpoint list`.
type ListItem struct {
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	Scope    string `json:"scope"`
	Provider string `json:"provider"`
	Version  string `json:"version,omitempty"`
	Status   string `json:"status"`
	Desired  bool   `json:"desired"`
	Pinned   bool   `json:"pinned,omitempty"`
	Channel  string `json:"channel,omitempty"`
}

// List renders `starpoint list`.
func (w *Writer) List(states []model.State, pathInventory ...[]locator.Found) error {
	var unaccounted []locator.Found
	if len(pathInventory) > 0 {
		for _, f := range pathInventory[0] {
			if !f.Attributed {
				unaccounted = append(unaccounted, f)
			}
		}
	}
	items := make([]ListItem, 0, len(states))
	for _, st := range states {
		item := ListItem{
			Name:     st.Package.Name,
			Scope:    engine.ScopeOf(st),
			Provider: st.Res.Provider,
			Status:   string(st.Status),
			Desired:  true,
			Pinned:   st.Package.Version != "",
			Channel:  st.Package.Channel,
		}
		if st.Desired != nil {
			item.Version = st.Desired.Version
			if st.Desired.Path != "" {
				item.Path = st.Desired.Path
			}
			item.Provider = st.Desired.Provider
		} else if st.Current != nil {
			item.Version = st.Current.Version
			item.Path = st.Current.Path
			item.Provider = st.Current.Provider
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })

	if w.asJSON {
		payload := map[string]any{"packages": items}
		payload["unaccounted_path"] = unaccounted
		return w.respond(payload)
	}
	if len(items) == 0 {
		fmt.Fprintln(w.out, w.p.Dim("No software in the configuration yet. Add some with: starpoint install <package>"))
		return nil
	}
	if len(items) > 0 {
		rows := make([][]string, 0, len(items)+1)
		rows = append(rows, []string{"PACKAGE", "LOCATION", "SCOPE", "PROVIDER", "VERSION", "STATUS"})
		for _, it := range items {
			rows = append(rows, []string{
				it.Name, dash(it.Path), it.Scope, dash(it.Provider), dash(it.Version),
				w.status(model.Status(it.Status)),
			})
		}
		if err := w.table(rows); err != nil {
			return err
		}
	}
	if len(unaccounted) > 0 {
		if len(items) == 0 {
			fmt.Fprintln(w.out, "\nNo software in the configuration yet. Add some with: starpoint install <package>")
		}
		fmt.Fprintln(w.out, "\n"+w.section("PATH executables with unknown ownership"))
		rows := [][]string{{"BINARY", "LOCATION", "DETAIL"}}
		for _, f := range unaccounted {
			detail := f.Reason
			if f.Duplicate {
				detail = strings.TrimSpace(detail + "; shadowed by an earlier PATH entry")
			}
			if f.Symlink {
				detail = strings.TrimSpace(detail + "; symlink → " + f.Target)
			}
			rows = append(rows, []string{filepath.Base(f.Path), f.Path, dash(detail)})
		}
		return w.table(rows)
	}
	return nil
}

// Info renders `starpoint info <package>`.
//
// extra carries the optional companions of a package: a pending "transition"
// when ownership is moving, or the provider "lookup" survey that explained an
// unmanaged package. Folding them into one response is what keeps info a single
// JSON document; emitting them separately produced concatenated objects.
func (w *Writer) Info(st model.State, extra map[string]any) error {
	if w.asJSON {
		fields := map[string]any{"package": st}
		for k, v := range extra {
			fields[k] = v
		}
		return w.respond(fields)
	}
	var b strings.Builder
	p := st.Package
	fmt.Fprintf(&b, "%s\n\n", w.p.Bold(p.Describe()))
	w.field(&b, "Installed:", pathOrNotInstalled(st))
	w.field(&b, "Version:", dash(firstNonEmpty(versionOf(st.Desired), versionOf(st.Current))))
	w.field(&b, "Provider:", dash(providerOf(st)))
	w.field(&b, "Desired provider:", dash(st.Res.Provider))
	if p.Provider != "" {
		w.field(&b, "Configured:", p.Provider)
	} else {
		w.field(&b, "Configured:", "(not recorded yet)")
	}
	w.field(&b, "Status:", w.status(st.Status))
	if p.Version != "" {
		w.field(&b, "Pinned version:", p.Version)
	}
	if p.Channel != "" {
		w.field(&b, "Channel:", p.Channel)
	}
	if st.Res.Reason != "" {
		w.field(&b, "Why this provider:", st.Res.Reason)
	}
	if p.Origin.Path != "" {
		kind := "global"
		if p.Origin.Project {
			kind = "project"
		}
		w.field(&b, "Declared in:", fmt.Sprintf("%s (%s)", p.Origin.Path, kind))
	}
	for _, e := range st.Elsewhere {
		w.field(&b, "Also provided by:", fmt.Sprintf("%s at %s", e.Provider, dash(e.Path)))
	}
	for _, n := range st.Notes {
		w.field(&b, "Note:", n)
	}
	if len(b.String()) > 0 {
		if _, err := io.WriteString(w.out, b.String()); err != nil {
			return err
		}
	}
	if t, ok := extra["transition"].(model.Transition); ok {
		return w.writeTransitionDetail(t)
	}
	if s, ok := extra["lookup"].(engine.Survey); ok {
		return w.writeLookup(s)
	}
	return nil
}

// SyncResult is the machine-readable shape of `starpoint sync`.
type SyncResult struct {
	DryRun        bool               `json:"dry_run"`
	Converged     bool               `json:"converged"`
	ConfigChanged bool               `json:"config_changed"`
	ConfigPath    string             `json:"config_path,omitempty"`
	Actions       []ActionResult     `json:"actions"`
	Transitions   []model.Transition `json:"transitions,omitempty"`
	States        []model.State      `json:"states"`
	Warnings      []model.Warning    `json:"warnings"`
	Problems      []string           `json:"problems,omitempty"`
	Project       *project.Info      `json:"project,omitempty"`
	Summary       Summary            `json:"summary"`
}

// Sync renders the outcome of a sync.
func (w *Writer) Sync(res *engine.Result, plan model.Plan, proj *project.Info) error {
	engineActions := syncActions(res, plan)
	warnings := mergeWarnings(append([]model.Warning{}, w.context...), res.Warnings...)
	actions, warnings := buildActions(res.DryRun, warnings, engineActions, w.runAs)
	if warnings == nil {
		warnings = []model.Warning{}
	}
	transitions := append(append([]model.Transition{}, plan.Transitions...), res.Transitions...)
	// A dry run describes the machine as it is. A real run that changed
	// something describes it as the run left it: the caller re-plans and puts
	// the fresh states in res.States, and any action that did not apply
	// means the machine has not converged whatever the states say.
	states := plan.States
	converged := plan.Empty()
	if !res.DryRun && len(res.Applied) > 0 && res.States != nil {
		states = res.States
		converged = true
		for _, a := range actions {
			if a.Result != string(model.OutcomeApplied) {
				converged = false
			}
		}
	}
	out := SyncResult{
		DryRun:        res.DryRun,
		ConfigChanged: res.ConfigChanged,
		ConfigPath:    res.ConfigPath,
		Actions:       actions,
		Transitions:   dedupeTransitions(transitions),
		States:        states,
		Warnings:      warnings,
		Project:       proj,
		Summary:       summarize(actions, warnings),
	}
	// Converged means every desired package is actually satisfied, which is
	// stricter than having produced no actions.
	out.Converged = converged
	for _, st := range states {
		if !st.Status.Healthy() {
			out.Converged = false
		}
	}

	if w.asJSON {
		return w.respond(flatten(out))
	}
	w.writeContextWarnings()

	if len(actions) == 0 {
		// "No actions" is not the same claim as "everything is right". A package
		// whose provider cannot install it produces no action and is still not
		// converged, so the message has to tell those apart.
		unhealthy := 0
		for _, st := range plan.States {
			if !st.Status.Healthy() {
				unhealthy++
			}
		}
		if unhealthy == 0 {
			fmt.Fprintf(w.out, "%s %d package(s) match the configuration.\n",
				w.p.Green("In sync."), len(plan.States))
		} else {
			fmt.Fprintf(w.out, "%s %d of %d package(s) match the configuration, and Starpoint has no way to change the rest.\n",
				w.p.Yellow("Nothing to do."), len(plan.States)-unhealthy, len(plan.States))
		}
		w.writeWarnings(res.Warnings)
		return nil
	}
	verb := "were made"
	if res.DryRun {
		verb = "would be made"
	}
	fmt.Fprintln(w.out, w.section(fmt.Sprintf("%d change(s) %s:", len(actions), verb)))
	for _, a := range actions {
		target := a.Package
		if a.Provider != "" {
			target += " via " + a.Provider
		}
		line := "  " + padTo(w.outcome(a.Action, a.Result), 10) + " " + target
		if a.Note != "" {
			line += " (" + a.Note + ")"
		}
		if a.Result == string(model.OutcomeFailed) || a.Result == string(model.OutcomeNotRun) {
			line += " " + w.p.Red("["+a.Result+"]")
		}
		fmt.Fprintln(w.out, line)
		WriteEffects(w.out, w.p, a.Effects, res.DryRun, res.DryRun || w.verbose, "      ")
		w.writeRunAs(a.RunAs, "      ")
	}
	if res.DryRun {
		fmt.Fprintln(w.out, "\n"+w.p.Yellow("Dry run: nothing was changed."))
	}
	if len(out.Transitions) > 0 {
		w.writeTransitions(out.Transitions)
	}
	if res.ConfigChanged {
		fmt.Fprintf(w.out, "\n%s %s\n", w.p.Green("Configuration updated:"), res.ConfigPath)
	}
	w.writeWarnings(warnings)
	return nil
}

// syncActions lines up what sync planned with what it did. An applied action
// replaces its plan entry, so the outcome and the observed effects are what
// is reported; a planned action that never ran is reported as not run.
func syncActions(res *engine.Result, plan model.Plan) []model.PlannedAction {
	key := func(a model.PlannedAction) string { return string(a.Action) + "\x00" + a.State.Package.Name }
	applied := map[string]model.PlannedAction{}
	for _, a := range res.Applied {
		applied[key(a)] = a
	}
	used := map[string]bool{}
	var out []model.PlannedAction
	for _, a := range plan.Actions {
		k := key(a)
		if done, ok := applied[k]; ok {
			out = append(out, done)
			used[k] = true
			continue
		}
		if !res.DryRun {
			a.Outcome = model.OutcomeNotRun
		}
		out = append(out, a)
	}
	for _, a := range res.Applied {
		if !used[key(a)] {
			out = append(out, a)
		}
	}
	return out
}

// dedupeTransitions keeps one record per package, preferring the later phase. The
// plan describes what is about to happen and the result describes what did, so a
// package that was transitioned appears once, in its final state.
func dedupeTransitions(in []model.Transition) []model.Transition {
	index := map[string]int{}
	var out []model.Transition
	for _, t := range in {
		if i, ok := index[t.Package.Name]; ok {
			out[i] = t
			continue
		}
		index[t.Package.Name] = len(out)
		out = append(out, t)
	}
	return out
}

// Transitions renders `starpoint transition`.
func (w *Writer) Transitions(ts []model.Transition, dryRun bool) error {
	if w.asJSON {
		return w.respond(map[string]any{
			"dry_run":     dryRun,
			"transitions": ts,
			"pending":     countPending(ts),
		})
	}
	if len(ts) == 0 {
		fmt.Fprintln(w.out, w.p.Green("No provider transitions are pending."))
		return nil
	}
	w.writeTransitions(ts)
	return nil
}

func countPending(ts []model.Transition) int {
	n := 0
	for _, t := range ts {
		if t.NeedsWork() {
			n++
		}
	}
	return n
}

// TransitionsApplied renders transitions the transition command performed (or,
// in a dry run, would perform). The JSON form carries the transitions as
// before, plus the same operation fields every changing command reports, so
// a provider move's effects read exactly like a sync's.
func (w *Writer) TransitionsApplied(op string, ts []model.Transition, errs []error, dryRun bool) error {
	if w.asJSON {
		res := &engine.Result{DryRun: dryRun}
		for i, t := range ts {
			var err error
			if i < len(errs) {
				err = errs[i]
			}
			a := engine.TransitionAction(t, err, dryRun)
			res.Applied = append(res.Applied, a)
		}
		fields := w.operationFields(op, dryRun, []*engine.Result{res})
		delete(fields, "config_changed")
		delete(fields, "config_path")
		if ts == nil {
			ts = []model.Transition{}
		}
		fields["transitions"] = ts
		return w.respond(fields)
	}
	w.writeContextWarnings()
	w.dryRunTransitions = dryRun
	w.transitionDetail = true
	defer func() { w.dryRunTransitions, w.transitionDetail = false, false }()
	return w.Transitions(ts, dryRun)
}

// writeTransitions prints transitions with the evidence behind each decision. The
// phase and the verification are the point: a user deciding whether to trust a
// change of ownership needs to see why Starpoint believes it worked.
func (w *Writer) writeTransitions(ts []model.Transition) {
	fmt.Fprintln(w.out, "\n"+w.section("Transitions"))
	for _, t := range ts {
		from := t.From.Name
		if from == "" {
			from = "nothing"
		}
		fmt.Fprintf(w.out, "  %s: %s -> %s [%s]\n",
			w.p.Bold(t.Package.Name), from, t.To.Name, w.phase(t.Phase))
		if t.Note != "" {
			fmt.Fprintf(w.out, "      %s\n", t.Note)
		}
		// The note often already states the verification outcome; printing the
		// reason again would just repeat it.
		if reason := t.Verification.Reason(); reason != "" && !strings.Contains(t.Note, reason) {
			fmt.Fprintf(w.out, "      %s\n", reason)
		}
		if t.LeftBehindReason != "" {
			fmt.Fprintf(w.out, "      left behind: %s\n", t.LeftBehindReason)
		}
		if t.Advice != "" {
			fmt.Fprintf(w.out, "      next: %s\n", t.Advice)
		}
		if !w.transitionDetail {
			// Sync lists these under its actions already.
			continue
		}
		planned := w.dryRunTransitions
		for _, eff := range t.Effects {
			if eff.Evidence == model.EvidencePlanned {
				planned = true
				break
			}
		}
		WriteEffects(w.out, w.p, t.Effects, planned, w.dryRunTransitions || w.verbose, "      ")
		w.writeRunAs(t.RunAs, "      ")
		for _, warn := range t.Warnings {
			fmt.Fprintf(w.out, "      %s [%s]: %s\n", w.severity(warn.Severity), warn.Code, warn.Message)
		}
	}
}

// writeTransitionDetail renders the transition section of `starpoint info`
// when a pending ownership move applies, showing the phases and the evidence
// rather than a bare status. The JSON form is folded into Info's response.
func (w *Writer) writeTransitionDetail(t model.Transition) error {
	from := t.From.Name
	if from == "" {
		from = "nothing"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n  %s %s -> %s\n", w.label("Transition:"), from, t.To.Name)
	w.fieldAt(&b, 16, "Phase:", w.phase(t.Phase))
	if t.Previous != nil {
		w.fieldAt(&b, 16, "Previous:", fmt.Sprintf("%s at %s", t.Previous.Provider, dash(t.Previous.Path)))
	}
	if t.Installed != nil {
		w.fieldAt(&b, 16, "New copy:", dash(t.Installed.Path))
	}
	w.fieldAt(&b, 16, "Verified:", t.Verification.Reason())
	if t.OldRemoved {
		w.fieldAt(&b, 16, "Previous copy:", w.p.Green("removed"))
	} else if t.LeftBehindReason != "" {
		w.fieldAt(&b, 16, "Previous copy:", w.p.Yellow("still present ("+t.LeftBehindReason+")"))
	}
	if t.Recoverable {
		w.fieldAt(&b, 16, "Recoverable:", w.p.Green("yes"))
	}
	if t.Advice != "" {
		w.fieldAt(&b, 16, "Next:", t.Advice)
	}
	_, err := io.WriteString(w.out, b.String())
	return err
}

// ProviderHealth is the machine-readable shape of the provider section of doctor.
type ProviderHealth struct {
	Name         string                `json:"name"`
	Description  string                `json:"description"`
	Kind         provider.Kind         `json:"kind"`
	Available    bool                  `json:"available"`
	Reason       string                `json:"reason,omitempty"`
	Binary       string                `json:"binary,omitempty"`
	Version      string                `json:"version,omitempty"`
	Capabilities provider.Capabilities `json:"capabilities"`
}

// DoctorReport is the machine-readable shape of `starpoint doctor`.
type DoctorReport struct {
	Starpoint     StarpointInfo    `json:"starpoint"`
	Config        ConfigInfo       `json:"config"`
	Providers     []ProviderHealth `json:"providers"`
	Ownership     []model.State    `json:"ownership"`
	PATHInventory []locator.Found  `json:"path_inventory"`
	// PATHSummary counts the inventory; the full list stays in PATHInventory.
	PATHSummary locator.PATHSummary `json:"path_summary"`
	// ShowAllPATH asks the human renderer for every inventory row instead of
	// the summary. JSON always carries the full inventory.
	ShowAllPATH bool            `json:"-"`
	Problems    []model.Warning `json:"problems,omitempty"`
	Healthy     bool            `json:"healthy"`
}

// StarpointInfo describes the running binary.
type StarpointInfo struct {
	Version  string `json:"version"`
	Path     string `json:"path,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// ConfigInfo describes the configuration.
type ConfigInfo struct {
	Path         string   `json:"path"`
	Exists       bool     `json:"exists"`
	Schema       int      `json:"schema"`
	PackageCount int      `json:"package_count"`
	Files        []string `json:"files,omitempty"`
	ProjectFile  string   `json:"project_file,omitempty"`
	Writable     bool     `json:"writable"`
}

// Doctor renders `starpoint doctor`.
func (w *Writer) Doctor(rep DoctorReport) error {
	if w.asJSON {
		return w.respond(flatten(rep))
	}
	fmt.Fprintln(w.out, w.section("Starpoint"))
	fmt.Fprintf(w.out, "  Version:  %s\n", rep.Starpoint.Version)
	if rep.Starpoint.Path != "" {
		fmt.Fprintf(w.out, "  Binary:   %s\n", rep.Starpoint.Path)
	}
	if rep.Starpoint.Provider != "" {
		fmt.Fprintf(w.out, "  Installed by: %s\n", rep.Starpoint.Provider)
	}

	fmt.Fprintf(w.out, "\n%s\n", w.section("Configuration"))
	fmt.Fprintf(w.out, "  Path:     %s (%s)\n", rep.Config.Path, w.existsLabel(rep.Config.Exists))
	fmt.Fprintf(w.out, "  Schema:   %d\n", rep.Config.Schema)
	fmt.Fprintf(w.out, "  Packages: %d\n", rep.Config.PackageCount)
	if rep.Config.Writable {
		fmt.Fprintln(w.out, "  Writable: "+w.p.Green("yes"))
	} else {
		fmt.Fprintln(w.out, "  Writable: "+w.p.Red("no"))
	}
	for _, f := range rep.Config.Files {
		fmt.Fprintf(w.out, "  Includes: %s\n", f)
	}

	fmt.Fprintf(w.out, "\n%s\n", w.section("Providers"))
	rows := [][]string{{"PROVIDER", "KIND", "AVAILABLE", "VERSION"}}
	for _, p := range rep.Providers {
		rows = append(rows, []string{
			p.Name, string(p.Kind), w.yesNo(p.Available), dash(p.Version),
		})
	}
	if err := w.table(rows); err != nil {
		return err
	}
	for _, p := range rep.Providers {
		if !p.Available && p.Reason != "" {
			fmt.Fprintf(w.out, "    %s: %s\n", p.Name, w.p.Dim(p.Reason))
		}
	}

	if len(rep.Ownership) > 0 {
		fmt.Fprintf(w.out, "\n%s\n", w.section("Ownership"))
		orows := [][]string{{"PACKAGE", "PROVIDER", "DESIRED", "STATUS"}}
		for _, st := range rep.Ownership {
			orows = append(orows, []string{
				st.Package.Name, providerOf(st), st.Res.Provider, w.status(st.Status),
			})
		}
		if err := w.table(orows); err != nil {
			return err
		}
	}
	if rep.ShowAllPATH {
		fmt.Fprintf(w.out, "\n%s (%d executable entries)\n", w.section("PATH inventory"), len(rep.PATHInventory))
		if len(rep.PATHInventory) > 0 {
			if err := w.pathRows(rep.PATHInventory); err != nil {
				return err
			}
		} else {
			fmt.Fprintln(w.out, "  No executable entries found on PATH.")
		}
	} else if err := w.pathSummary(rep); err != nil {
		return err
	}

	if len(rep.Problems) > 0 {
		fmt.Fprintf(w.out, "\n%s\n", w.section("Problems"))
		for _, p := range rep.Problems {
			label := w.severity(p.Severity)
			if p.Package != "" {
				label += " (" + p.Package + ")"
			}
			fmt.Fprintf(w.out, "  [%s] %s\n", label, p.Message)
		}
	}
	if rep.Healthy {
		fmt.Fprintln(w.out, "\n"+w.p.Green("No problems found."))
	}
	return nil
}

// pathSummaryLimit caps each list in the summarised PATH inventory.
const pathSummaryLimit = 15

// pathSummary renders the PATH inventory as counts plus the rows worth reading:
// names shadowed by an earlier PATH entry, then executables nobody owns.
func (w *Writer) pathSummary(rep DoctorReport) error {
	sum := rep.PATHSummary
	if sum.Executables == 0 && len(rep.PATHInventory) > 0 {
		sum = locator.Summarize(rep.PATHInventory)
	}
	fmt.Fprintf(w.out, "\n%s\n", w.section("PATH inventory"))
	if sum.Executables == 0 {
		fmt.Fprintln(w.out, "  No executable entries found on PATH.")
		return nil
	}
	fmt.Fprintf(w.out, "  %d executables in %d directories; %d shadowed by an earlier PATH entry; %d with unknown owner\n",
		sum.Executables, sum.Directories, sum.Shadowed, sum.Unknown)
	var shadowed, unknown []locator.Found
	for _, f := range rep.PATHInventory {
		if f.Duplicate {
			shadowed = append(shadowed, f)
		} else if !f.Attributed {
			unknown = append(unknown, f)
		}
	}
	for _, group := range []struct {
		title string
		rows  []locator.Found
	}{
		{"Shadowed", shadowed},
		{"Unknown owner", unknown},
	} {
		if len(group.rows) == 0 {
			continue
		}
		rows := group.rows
		if len(rows) > pathSummaryLimit {
			rows = rows[:pathSummaryLimit]
		}
		fmt.Fprintf(w.out, "  %s (%d):\n", w.section(group.title), len(group.rows))
		if err := w.pathRows(rows); err != nil {
			return err
		}
		if more := len(group.rows) - len(rows); more > 0 {
			fmt.Fprintf(w.out, "    ... and %d more\n", more)
		}
	}
	fmt.Fprintln(w.out, w.p.Dim("  Run 'starpoint doctor --all' for every entry; --json always includes the full inventory."))
	return nil
}

// pathRows renders inventory rows as a table.
func (w *Writer) pathRows(found []locator.Found) error {
	rows := [][]string{{"BINARY", "LOCATION", "OWNER", "DETAIL"}}
	for _, f := range found {
		owner, detail := w.p.Yellow("unknown"), f.Reason
		if f.Installation != nil {
			owner = f.Installation.Provider
		}
		if f.Duplicate {
			detail = strings.TrimSpace(detail + " duplicate name; shadowed by an earlier PATH entry")
		}
		if f.Symlink {
			detail = strings.TrimSpace(detail + " symlink → " + f.Target)
		}
		rows = append(rows, []string{filepath.Base(f.Path), f.Path, owner, dash(detail)})
	}
	return w.table(rows)
}

// Result renders the outcome of install or remove.
func (w *Writer) Result(op string, res *engine.Result) error {
	changed := res.ConfigChanged && !res.DryRun
	if w.asJSON {
		return w.respond(w.operationFields(op, res.DryRun, []*engine.Result{res}))
	}
	w.writeContextWarnings()
	actions, warnings := buildActions(res.DryRun, res.Warnings, res.Applied, w.runAs)
	for _, a := range actions {
		pkg := a.Package
		if a.Provider != "" {
			pkg += " via " + a.Provider
		}
		verb := op
		switch model.Action(a.Action) {
		case model.ActionAdopt:
			verb = "record"
		case model.ActionRemove:
			verb = "remove"
		case model.ActionUpdate:
			verb = "update"
		}
		switch {
		case res.DryRun:
			// "would installed" is not English, and "installed" would be a lie
			// about the machine: nothing happened.
			verb = "would " + verb
		case a.Result == string(model.OutcomeFailed):
			verb = "failed to " + verb
		default:
			verb = pastTense(verb)
		}
		line := w.outcome(verb, a.Result) + " " + pkg
		if a.Note != "" && !strings.HasPrefix(a.Note, "provider ") {
			line += " (" + a.Note + ")"
		}
		fmt.Fprintln(w.out, line)
		WriteEffects(w.out, w.p, a.Effects, res.DryRun, res.DryRun || w.verbose, "")
		w.writeRunAs(a.RunAs, "  ")
		w.writeActionWarnings(a.Warnings, "  ")
	}
	if changed {
		fmt.Fprintf(w.out, "%s %s\n", w.p.Green("Configuration updated:"), res.ConfigPath)
	} else if res.DryRun {
		fmt.Fprintln(w.out, w.p.Yellow("Dry run: nothing was changed."))
	}
	// Warnings that name no package belong to no action line.
	for _, warn := range warnings {
		if warn.Package != "" {
			continue
		}
		if warn.Severity == model.SeverityInfo && !w.verbose {
			continue
		}
		fmt.Fprintf(w.out, "%s %s\n", w.p.Yellow("warning:"), warn.Message)
	}
	return nil
}

// Update renders the machine-readable result of `starpoint update`. It shares
// install and remove's operation fields and adds the pin conflicts that update
// can report but the others cannot.
func (w *Writer) Update(res *engine.Result) error {
	if !w.asJSON {
		return nil
	}
	fields := w.operationFields("update", res.DryRun, []*engine.Result{res})
	conflicts := res.PinConflicts
	if conflicts == nil {
		conflicts = []model.PinConflict{}
	}
	fields["pin_conflicts"] = conflicts
	return w.respond(fields)
}

// Prune renders `starpoint prune`.
//
// A dry run is only useful if it says what would go, so both the human and the
// machine forms list the candidates, with every file that would go with them.
// Reporting an empty result was how a user could be told "nothing to do" and
// still have the next real run delete things.
func (w *Writer) Prune(res *engine.Result, dryRun bool) error {
	if w.asJSON {
		return w.respond(w.operationFields("prune", dryRun, []*engine.Result{res}))
	}
	w.writeContextWarnings()
	actions, warnings := buildActions(dryRun, res.Warnings, res.Applied, w.runAs)
	var skipped []model.Warning
	others := 0
	for _, warn := range warnings {
		if warn.Code == model.WarnPruneSkipped {
			skipped = append(skipped, warn)
			continue
		}
		others++
		if warn.Package == "" {
			fmt.Fprintf(w.out, "  %s\n", w.colored(warn.Severity, warn.Message))
		}
	}
	// What prune left alone is always listed, so a candidate outside the
	// home or a package manager is never skipped without a word.
	defer func() {
		for _, warn := range skipped {
			fmt.Fprintf(w.out, "%s [%s]: %s\n", w.severity(warn.Severity), warn.Code, warn.Message)
		}
	}()
	if len(actions) == 0 {
		if others == 0 {
			fmt.Fprintln(w.out, w.p.Green("No orphaned software found."))
		}
		return nil
	}
	for _, a := range actions {
		verb := "Removed"
		switch {
		case dryRun:
			verb = "Would remove"
		case a.Result == string(model.OutcomeFailed):
			verb = "Failed to remove"
		}
		pkg := a.Package
		if a.Provider != "" {
			pkg += " via " + a.Provider
		}
		line := w.outcome(verb, a.Result) + " " + pkg
		if a.Error != "" {
			line += " (" + a.Error + ")"
		}
		fmt.Fprintln(w.out, line)
		WriteEffects(w.out, w.p, a.Effects, dryRun, dryRun || w.verbose, "")
		w.writeRunAs(a.RunAs, "  ")
		var ws []model.Warning
		for _, warn := range a.Warnings {
			// A skipped candidate of the same name from another provider
			// is listed with the others, not under this action.
			if warn.Code != model.WarnPruneSkipped {
				ws = append(ws, warn)
			}
		}
		w.writeActionWarnings(ws, "  ")
	}
	return nil
}

// Init renders the outcome of `starpoint init`.
func (w *Writer) Init(path string, created, replaced bool) error {
	if w.asJSON {
		return w.respond(map[string]any{"path": path, "created": created, "replaced": replaced})
	}
	switch {
	case replaced:
		fmt.Fprintf(w.out, "%s %s\n", w.p.Green("Replaced"), path)
	case created:
		fmt.Fprintf(w.out, "%s %s\n", w.p.Green("Created"), path)
	default:
		fmt.Fprintf(w.out, "%s; use --force to replace it\n", w.p.Yellow(path+" already exists"))
	}
	return nil
}

// Render writes structured data as a single flat JSON response. A map or struct
// contributes its fields to the top level; anything else is nested under "data".
func (w *Writer) Render(v any) error {
	if !w.asJSON {
		return nil
	}
	return w.respond(flatten(v))
}

// Warn writes a warning line in human mode.
func (w *Writer) Warn(msg string) {
	if !w.asJSON {
		fmt.Fprintf(w.out, "%s %s\n", w.p.Yellow("warning:"), msg)
	}
}

func (w *Writer) writeWarnings(warns []model.Warning) {
	for _, warn := range warns {
		if warn.Severity == model.SeverityInfo {
			continue
		}
		fmt.Fprintf(w.out, "  [%s] %s\n", w.severity(warn.Severity), warn.Message)
	}
}

func (w *Writer) emit(v any) error {
	enc := json.NewEncoder(w.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// table prints aligned columns. Columns are sized to content, which keeps output
// readable without hardcoding widths that break on long paths.
//
// The first row is the header and is printed bold. Widths are measured without
// escape sequences, so a colored cell lines up with the plain one beside it.
func (w *Writer) table(rows [][]string) error {
	if len(rows) == 0 {
		return nil
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && color.VisibleLen(cell) > widths[i] {
				widths[i] = color.VisibleLen(cell)
			}
		}
	}
	for r, row := range rows {
		var b strings.Builder
		for i, cell := range row {
			if i > 0 {
				b.WriteString("  ")
			}
			if r == 0 {
				cell = w.section(cell)
			}
			if i == len(row)-1 {
				b.WriteString(cell)
			} else {
				b.WriteString(cell + strings.Repeat(" ", widths[i]-color.VisibleLen(cell)))
			}
		}
		if _, err := fmt.Fprintln(w.out, strings.TrimRight(b.String(), " ")); err != nil {
			return err
		}
	}
	return nil
}

// pastTense converts the base verb a command is named after ("install") into the
// form a completed action reads in ("installed").
func pastTense(op string) string {
	switch op {
	case "install":
		return "installed"
	case "remove":
		return "removed"
	case "update":
		return "updated"
	case "record":
		return "recorded"
	default:
		return op
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func versionOf(i *model.Installation) string {
	if i == nil {
		return ""
	}
	return i.Version
}

func providerOf(st model.State) string {
	if st.Desired != nil && st.Desired.Provider != "" {
		return st.Desired.Provider
	}
	if st.Current != nil && st.Current.Provider != "" {
		return st.Current.Provider
	}
	return ""
}

func pathOrNotInstalled(st model.State) string {
	if st.Desired != nil && st.Desired.Path != "" {
		return st.Desired.Path
	}
	if st.Current != nil && st.Current.Path != "" {
		return st.Current.Path
	}
	if st.Desired != nil || st.Current != nil {
		return "installed (path unknown)"
	}
	return "not installed"
}

// DocumentInfo is a helper for doctor: it summarizes a loaded document.
func DocumentInfo(doc *config.Document, globalPath string, writable bool) ConfigInfo {
	info := ConfigInfo{
		Path:         globalPath,
		Exists:       doc.Global.Exists,
		Schema:       doc.Global.Raw.Schema,
		PackageCount: len(doc.Packages()),
		Writable:     writable,
	}
	for _, f := range doc.Files() {
		if f.Project {
			info.Files = append(info.Files, f.Path)
		}
	}
	return info
}

// Lookup renders a provider survey: what has the software, what was asked, and
// what could not be.
//
// The last part is the reason this exists. "No provider has it" and "no provider
// *was able to say*" are different claims, and a report that conflates them
// teaches the user to trust a weaker guarantee than the one being offered.

// Explain renders the provider-resolution reasoning chain.
func (w *Writer) Explain(expl resolve.Explanation) error {
	if w.asJSON {
		return w.respond(flatten(expl))
	}
	fmt.Fprintf(w.out, "Package: %s\n\n", w.p.Bold(expl.Package.Describe()))
	fmt.Fprintf(w.out, "Selected provider: %s\n", w.p.Green(expl.Final.Provider))
	fmt.Fprintf(w.out, "Confidence: %s\n", expl.Final.Confidence)
	if expl.Final.Ambiguous {
		fmt.Fprintf(w.out, "%s\n", w.p.Yellow("Ambiguous: yes"))
		if len(expl.Final.Alternatives) > 0 {
			fmt.Fprintf(w.out, "Alternatives: %s\n", strings.Join(expl.Final.Alternatives, ", "))
		}
	}
	fmt.Fprintf(w.out, "Reason: %s\n\n", expl.Final.Reason)
	fmt.Fprintln(w.out, w.section("Resolution reasoning:"))
	for _, step := range expl.Steps {
		marker := "  "
		if step.Applied {
			marker = w.p.Green("→ ")
		}
		fmt.Fprintf(w.out, "%s%s: %s", marker, w.p.Bold(step.Rule), step.Outcome)
		if step.Provider != "" {
			fmt.Fprintf(w.out, " (%s)", step.Provider)
		}
		fmt.Fprintln(w.out)
		if step.Reason != "" {
			fmt.Fprintf(w.out, "      %s\n", w.p.Dim(step.Reason))
		}
	}
	return nil
}

// writeLookup renders the provider survey that explained an unmanaged package.
// The JSON form is folded into Info's response.
func (w *Writer) writeLookup(s engine.Survey) error {
	fmt.Fprintln(w.out, "\n  "+w.section("Provider lookup"))
	owners := s.Owners()
	if len(owners) > 0 {
		for _, o := range owners {
			version := ""
			if o.Installation != nil && o.Installation.Version != "" {
				version = " " + o.Installation.Version
			}
			fmt.Fprintf(w.out, "    %s%s\n", o.Provider, w.p.Dim(version))
		}
	} else {
		fmt.Fprintln(w.out, "    "+w.p.Yellow("none found on installed providers"))
	}
	if unaskable := s.Unaskable(); len(unaskable) > 0 {
		fmt.Fprintf(w.out, "    %s\n", w.p.Dim("not asked: "+strings.Join(unaskable, ", ")))
	}
	if absent := s.NotInstalled(); len(absent) > 0 {
		fmt.Fprintf(w.out, "    %s\n", w.p.Dim("not installed: "+strings.Join(absent, ", ")))
	}
	fmt.Fprintf(w.out, "    %s\n", w.p.Dim(fmt.Sprintf("(%d provider(s) asked in %.0fms)", len(s.Findings), s.DurationMS)))
	return nil
}

// writeRunAs prints, with --verbose, the account each provider of an action
// ran as. An ordinary run says nothing new by listing its own user, so only
// an elevated run's identities are printed.
func (w *Writer) writeRunAs(runAs []model.RunAs, indent string) {
	if !w.verbose {
		return
	}
	for _, ra := range runAs {
		if !ra.Elevated {
			continue
		}
		fmt.Fprintf(w.out, "%s%s\n", indent, RunAsLine(ra))
	}
}

// RunAsLine describes one provider's identity for humans, as in
// "uv ran as alice (uid 1000, gid 1000), privileges dropped: uv installs ...".
func RunAsLine(ra model.RunAs) string {
	who := ra.User
	if who == "" {
		who = fmt.Sprintf("uid %d", ra.UID)
	}
	line := fmt.Sprintf("%s runs as %s (uid %d, gid %d)", orName(ra.Provider), who, ra.UID, ra.GID)
	if ra.Dropped {
		line += ", privileges dropped"
	} else if ra.Elevated {
		line += ", with full privileges"
	}
	if ra.Reason != "" {
		line += ": " + ra.Reason
	}
	return line
}

func orName(name string) string {
	if name == "" {
		return "the provider"
	}
	return name
}
