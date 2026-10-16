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
}

// New builds a Writer.
func New(out io.Writer, asJSON bool) *Writer {
	return &Writer{out: out, asJSON: asJSON}
}

// JSON reports whether machine-readable output is requested.
func (w *Writer) JSON() bool { return w.asJSON }

// SchemaVersion is the version of the machine-readable response envelope. It is
// independent of the TOML configuration `schema` field.
//
// Version 2 flattens every command payload into the top-level object.
// Version 1 documented that same flat shape, but the implementation nested some
// commands under their own key ("sync", "doctor", "explain") and others under
// "data", so no consumer could rely on it. Version 2 makes the documented shape
// the actual shape.
const SchemaVersion = 2

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
		fmt.Fprintln(w.out, "No software in the configuration yet. Add some with: starpoint install <package>")
		return nil
	}
	if len(items) > 0 {
		rows := make([][]string, 0, len(items)+1)
		rows = append(rows, []string{"PACKAGE", "LOCATION", "SCOPE", "PROVIDER", "VERSION", "STATUS"})
		for _, it := range items {
			rows = append(rows, []string{
				it.Name, dash(it.Path), it.Scope, dash(it.Provider), dash(it.Version), it.Status,
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
		fmt.Fprintln(w.out, "\nPATH executables with unknown ownership")
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
	fmt.Fprintf(&b, "%s\n\n", p.Describe())
	fmt.Fprintf(&b, "  Installed:         %s\n", pathOrNotInstalled(st))
	fmt.Fprintf(&b, "  Version:           %s\n", dash(firstNonEmpty(versionOf(st.Desired), versionOf(st.Current))))
	fmt.Fprintf(&b, "  Provider:          %s\n", dash(providerOf(st)))
	fmt.Fprintf(&b, "  Desired provider:  %s\n", dash(st.Res.Provider))
	if p.Provider != "" {
		fmt.Fprintf(&b, "  Configured:        %s\n", p.Provider)
	} else {
		fmt.Fprintf(&b, "  Configured:        (not recorded yet)\n")
	}
	fmt.Fprintf(&b, "  Status:            %s\n", st.Status)
	if p.Version != "" {
		fmt.Fprintf(&b, "  Pinned version:    %s\n", p.Version)
	}
	if p.Channel != "" {
		fmt.Fprintf(&b, "  Channel:           %s\n", p.Channel)
	}
	if st.Res.Reason != "" {
		fmt.Fprintf(&b, "  Why this provider: %s\n", st.Res.Reason)
	}
	if p.Origin.Path != "" {
		kind := "global"
		if p.Origin.Project {
			kind = "project"
		}
		fmt.Fprintf(&b, "  Declared in:       %s (%s)\n", p.Origin.Path, kind)
	}
	for _, e := range st.Elsewhere {
		fmt.Fprintf(&b, "  Also provided by:  %s at %s\n", e.Provider, dash(e.Path))
	}
	for _, n := range st.Notes {
		fmt.Fprintf(&b, "  Note:              %s\n", n)
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
	Warnings      []model.Warning    `json:"warnings,omitempty"`
	Problems      []string           `json:"problems,omitempty"`
	Project       *project.Info      `json:"project,omitempty"`
}

// ActionResult is one applied or planned action.
type ActionResult struct {
	Action   string `json:"action"`
	Package  string `json:"package"`
	Provider string `json:"provider,omitempty"`
	Note     string `json:"note,omitempty"`
	Result   string `json:"result,omitempty"`
}

// Sync renders the outcome of a sync.
func (w *Writer) Sync(res *engine.Result, plan model.Plan, proj *project.Info) error {
	actions := []ActionResult{}
	seen := map[string]bool{}
	for _, a := range plan.Actions {
		actions = append(actions, actionResult(a))
		seen[a.State.Package.Name] = true
	}
	for _, a := range res.Applied {
		if seen[a.State.Package.Name] {
			continue
		}
		actions = append(actions, actionResult(a))
	}
	transitions := append(append([]model.Transition{}, plan.Transitions...), res.Transitions...)
	out := SyncResult{
		DryRun:        res.DryRun,
		ConfigChanged: res.ConfigChanged,
		ConfigPath:    res.ConfigPath,
		Actions:       actions,
		Transitions:   dedupeTransitions(transitions),
		States:        plan.States,
		Warnings:      res.Warnings,
		Project:       proj,
	}
	// Converged means every desired package is actually satisfied, which is
	// stricter than having produced no actions.
	out.Converged = plan.Empty()
	for _, st := range plan.States {
		if !st.Status.Healthy() {
			out.Converged = false
		}
	}

	if w.asJSON {
		return w.respond(flatten(out))
	}

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
			fmt.Fprintf(w.out, "In sync. %d package(s) match the configuration.\n", len(plan.States))
		} else {
			fmt.Fprintf(w.out,
				"Nothing to do. %d of %d package(s) match the configuration, and Starpoint has no way to change the rest.\n",
				len(plan.States)-unhealthy, len(plan.States))
		}
		w.writeWarnings(res.Warnings)
		return nil
	}
	verb := "will"
	if res.DryRun {
		verb = "would"
	}
	fmt.Fprintf(w.out, "%d change(s) %s be made:\n", len(actions), verb)
	for _, a := range actions {
		target := a.Package
		if a.Provider != "" {
			target += " via " + a.Provider
		}
		line := fmt.Sprintf("  %-10s %s", a.Action, target)
		if a.Note != "" {
			line += " (" + a.Note + ")"
		}
		fmt.Fprintln(w.out, line)
	}
	if res.DryRun {
		fmt.Fprintln(w.out, "\nDry run: nothing was changed.")
	}
	if len(out.Transitions) > 0 {
		w.writeTransitions(out.Transitions)
	}
	if res.ConfigChanged {
		fmt.Fprintf(w.out, "\nConfiguration updated: %s\n", res.ConfigPath)
	}
	w.writeWarnings(res.Warnings)
	return nil
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
		fmt.Fprintln(w.out, "No provider transitions are pending.")
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

// writeTransitions prints transitions with the evidence behind each decision. The
// phase and the verification are the point: a user deciding whether to trust a
// change of ownership needs to see why Starpoint believes it worked.
func (w *Writer) writeTransitions(ts []model.Transition) {
	fmt.Fprintln(w.out, "\nTransitions")
	for _, t := range ts {
		from := t.From.Name
		if from == "" {
			from = "nothing"
		}
		fmt.Fprintf(w.out, "  %s: %s -> %s [%s]\n",
			t.Package.Name, from, t.To.Name, t.Phase)
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
	fmt.Fprintf(&b, "\n  Transition:     %s -> %s\n", from, t.To.Name)
	fmt.Fprintf(&b, "  Phase:          %s\n", t.Phase)
	if t.Previous != nil {
		fmt.Fprintf(&b, "  Previous:       %s at %s\n", t.Previous.Provider, dash(t.Previous.Path))
	}
	if t.Installed != nil {
		fmt.Fprintf(&b, "  New copy:       %s\n", dash(t.Installed.Path))
	}
	fmt.Fprintf(&b, "  Verified:       %s\n", t.Verification.Reason())
	if t.OldRemoved {
		fmt.Fprintf(&b, "  Previous copy:  removed\n")
	} else if t.LeftBehindReason != "" {
		fmt.Fprintf(&b, "  Previous copy:  still present (%s)\n", t.LeftBehindReason)
	}
	if t.Recoverable {
		fmt.Fprintf(&b, "  Recoverable:    yes\n")
	}
	if t.Advice != "" {
		fmt.Fprintf(&b, "  Next:           %s\n", t.Advice)
	}
	_, err := io.WriteString(w.out, b.String())
	return err
}

func actionResult(a model.PlannedAction) ActionResult {
	return ActionResult{
		Action:   string(a.Action),
		Package:  a.State.Package.Name,
		Provider: a.State.Res.Provider,
		Note:     a.Note,
	}
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
	fmt.Fprintln(w.out, "Starpoint")
	fmt.Fprintf(w.out, "  Version:  %s\n", rep.Starpoint.Version)
	if rep.Starpoint.Path != "" {
		fmt.Fprintf(w.out, "  Binary:   %s\n", rep.Starpoint.Path)
	}
	if rep.Starpoint.Provider != "" {
		fmt.Fprintf(w.out, "  Installed by: %s\n", rep.Starpoint.Provider)
	}

	fmt.Fprintf(w.out, "\nConfiguration\n")
	fmt.Fprintf(w.out, "  Path:     %s (%s)\n", rep.Config.Path, existsLabel(rep.Config.Exists))
	fmt.Fprintf(w.out, "  Schema:   %d\n", rep.Config.Schema)
	fmt.Fprintf(w.out, "  Packages: %d\n", rep.Config.PackageCount)
	if rep.Config.Writable {
		fmt.Fprintln(w.out, "  Writable: yes")
	} else {
		fmt.Fprintln(w.out, "  Writable: no")
	}
	for _, f := range rep.Config.Files {
		fmt.Fprintf(w.out, "  Includes: %s\n", f)
	}

	fmt.Fprintf(w.out, "\nProviders\n")
	rows := [][]string{{"PROVIDER", "KIND", "AVAILABLE", "VERSION"}}
	for _, p := range rep.Providers {
		rows = append(rows, []string{
			p.Name, string(p.Kind), yesNo(p.Available), dash(p.Version),
		})
	}
	if err := w.table(rows); err != nil {
		return err
	}
	for _, p := range rep.Providers {
		if !p.Available && p.Reason != "" {
			fmt.Fprintf(w.out, "    %s: %s\n", p.Name, p.Reason)
		}
	}

	if len(rep.Ownership) > 0 {
		fmt.Fprintf(w.out, "\nOwnership\n")
		orows := [][]string{{"PACKAGE", "PROVIDER", "DESIRED", "STATUS"}}
		for _, st := range rep.Ownership {
			orows = append(orows, []string{
				st.Package.Name, providerOf(st), st.Res.Provider, string(st.Status),
			})
		}
		if err := w.table(orows); err != nil {
			return err
		}
	}
	if rep.ShowAllPATH {
		fmt.Fprintf(w.out, "\nPATH inventory (%d executable entries)\n", len(rep.PATHInventory))
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
		fmt.Fprintf(w.out, "\nProblems\n")
		for _, p := range rep.Problems {
			label := string(p.Severity)
			if p.Package != "" {
				label += " (" + p.Package + ")"
			}
			fmt.Fprintf(w.out, "  [%s] %s\n", label, p.Message)
		}
	}
	if rep.Healthy {
		fmt.Fprintln(w.out, "\nNo problems found.")
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
	fmt.Fprintf(w.out, "\nPATH inventory\n")
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
		fmt.Fprintf(w.out, "  %s (%d):\n", group.title, len(group.rows))
		if err := w.pathRows(rows); err != nil {
			return err
		}
		if more := len(group.rows) - len(rows); more > 0 {
			fmt.Fprintf(w.out, "    ... and %d more\n", more)
		}
	}
	fmt.Fprintln(w.out, "  Run 'starpoint doctor --all' for every entry; --json always includes the full inventory.")
	return nil
}

// pathRows renders inventory rows as a table.
func (w *Writer) pathRows(found []locator.Found) error {
	rows := [][]string{{"BINARY", "LOCATION", "OWNER", "DETAIL"}}
	for _, f := range found {
		owner, detail := "unknown", f.Reason
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
		actions := make([]ActionResult, 0, len(res.Applied))
		for _, a := range res.Applied {
			actions = append(actions, actionResult(a))
		}
		return w.respond(map[string]any{
			"operation":      op,
			"dry_run":        res.DryRun,
			"config_changed": changed,
			"config_path":    res.ConfigPath,
			"actions":        actions,
			"warnings":       res.Warnings,
		})
	}
	for _, a := range res.Applied {
		pkg := a.State.Package.Name
		if a.State.Res.Provider != "" {
			pkg += " via " + a.State.Res.Provider
		}
		verb := op
		switch a.Action {
		case model.ActionAdopt:
			verb = "record"
		case model.ActionRemove:
			verb = "remove"
		case model.ActionUpdate:
			verb = "update"
		}
		if res.DryRun {
			// "would installed" is not English, and "installed" would be a lie
			// about the machine: nothing happened.
			verb = "would " + verb
		} else {
			verb = pastTense(verb)
		}
		line := fmt.Sprintf("%s %s", verb, pkg)
		if a.Note != "" && !strings.HasPrefix(a.Note, "provider ") {
			line += " (" + a.Note + ")"
		}
		fmt.Fprintln(w.out, line)
	}
	if changed {
		fmt.Fprintf(w.out, "Configuration updated: %s\n", res.ConfigPath)
	} else if res.DryRun {
		fmt.Fprintln(w.out, "Dry run: nothing was changed.")
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(w.out, "warning: %s\n", warn.Message)
	}
	return nil
}

// Update renders the machine-readable result of `starpoint update`. It shares
// install and remove's action shape and adds the pin conflicts that update can
// report but the others cannot.
func (w *Writer) Update(res *engine.Result) error {
	if !w.asJSON {
		return nil
	}
	actions := make([]ActionResult, 0, len(res.Applied))
	for _, a := range res.Applied {
		actions = append(actions, actionResult(a))
	}
	return w.respond(map[string]any{
		"operation":      "update",
		"dry_run":        res.DryRun,
		"config_changed": res.ConfigChanged && !res.DryRun,
		"config_path":    res.ConfigPath,
		"actions":        actions,
		"warnings":       res.Warnings,
		"pin_conflicts":  res.PinConflicts,
	})
}

// Prune renders `starpoint prune`.
//
// A dry run is only useful if it says what would go, so both the human and the
// machine forms list the candidates. Reporting an empty result was how a user
// could be told "nothing to do" and still have the next real run delete things.
func (w *Writer) Prune(res *engine.Result, dryRun bool) error {
	actions := make([]ActionResult, 0, len(res.Applied))
	for _, a := range res.Applied {
		actions = append(actions, actionResult(a))
	}
	if w.asJSON {
		return w.respond(map[string]any{
			"operation": "prune",
			"dry_run":   dryRun,
			"actions":   actions,
			"warnings":  res.Warnings,
		})
	}
	for _, warn := range res.Warnings {
		fmt.Fprintf(w.out, "  %s\n", warn.Message)
	}
	if len(actions) == 0 {
		if len(res.Warnings) == 0 {
			fmt.Fprintln(w.out, "No orphaned software found.")
		}
		return nil
	}
	verb := "Removed"
	if dryRun {
		verb = "Would remove"
	}
	for _, a := range actions {
		pkg := a.Package
		if a.Provider != "" {
			pkg += " via " + a.Provider
		}
		fmt.Fprintf(w.out, "%s %s\n", verb, pkg)
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
		fmt.Fprintf(w.out, "Replaced %s\n", path)
	case created:
		fmt.Fprintf(w.out, "Created %s\n", path)
	default:
		fmt.Fprintf(w.out, "%s already exists; use --force to replace it\n", path)
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
		fmt.Fprintf(w.out, "warning: %s\n", msg)
	}
}

func (w *Writer) writeWarnings(warns []model.Warning) {
	for _, warn := range warns {
		if warn.Severity == model.SeverityInfo {
			continue
		}
		fmt.Fprintf(w.out, "  [%s] %s\n", warn.Severity, warn.Message)
	}
}

func (w *Writer) emit(v any) error {
	enc := json.NewEncoder(w.out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// table prints aligned columns. Columns are sized to content, which keeps output
// readable without hardcoding widths that break on long paths.
func (w *Writer) table(rows [][]string) error {
	if len(rows) == 0 {
		return nil
	}
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}
	for _, row := range rows {
		var b strings.Builder
		for i, cell := range row {
			if i > 0 {
				b.WriteString("  ")
			}
			if i == len(row)-1 {
				b.WriteString(cell)
			} else {
				b.WriteString(cell + strings.Repeat(" ", widths[i]-len(cell)))
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

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func existsLabel(b bool) string {
	if b {
		return "exists"
	}
	return "missing"
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
	fmt.Fprintf(w.out, "Package: %s\n\n", expl.Package.Describe())
	fmt.Fprintf(w.out, "Selected provider: %s\n", expl.Final.Provider)
	fmt.Fprintf(w.out, "Confidence: %s\n", expl.Final.Confidence)
	if expl.Final.Ambiguous {
		fmt.Fprintf(w.out, "Ambiguous: yes\n")
		if len(expl.Final.Alternatives) > 0 {
			fmt.Fprintf(w.out, "Alternatives: %s\n", strings.Join(expl.Final.Alternatives, ", "))
		}
	}
	fmt.Fprintf(w.out, "Reason: %s\n\n", expl.Final.Reason)
	fmt.Fprintln(w.out, "Resolution reasoning:")
	for _, step := range expl.Steps {
		marker := "  "
		if step.Applied {
			marker = "→ "
		}
		fmt.Fprintf(w.out, "%s%s: %s", marker, step.Rule, step.Outcome)
		if step.Provider != "" {
			fmt.Fprintf(w.out, " (%s)", step.Provider)
		}
		fmt.Fprintln(w.out)
		if step.Reason != "" {
			fmt.Fprintf(w.out, "      %s\n", step.Reason)
		}
	}
	return nil
}

// writeLookup renders the provider survey that explained an unmanaged package.
// The JSON form is folded into Info's response.
func (w *Writer) writeLookup(s engine.Survey) error {
	fmt.Fprintln(w.out, "\n  Provider lookup")
	owners := s.Owners()
	if len(owners) > 0 {
		for _, o := range owners {
			version := ""
			if o.Installation != nil && o.Installation.Version != "" {
				version = " " + o.Installation.Version
			}
			fmt.Fprintf(w.out, "    %s%s\n", o.Provider, version)
		}
	} else {
		fmt.Fprintln(w.out, "    none found on installed providers")
	}
	if unaskable := s.Unaskable(); len(unaskable) > 0 {
		fmt.Fprintf(w.out, "    not asked: %s\n", strings.Join(unaskable, ", "))
	}
	if absent := s.NotInstalled(); len(absent) > 0 {
		fmt.Fprintf(w.out, "    not installed: %s\n", strings.Join(absent, ", "))
	}
	fmt.Fprintf(w.out, "    (%d provider(s) asked in %.0fms)\n", len(s.Findings), s.DurationMS)
	return nil
}
