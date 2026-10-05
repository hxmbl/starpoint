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
	"sort"
	"strings"

	"github.com/starpoint/starpoint/internal/config"
	"github.com/starpoint/starpoint/internal/engine"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/project"
	"github.com/starpoint/starpoint/internal/provider"
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
func (w *Writer) List(states []model.State) error {
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
		return w.emit(map[string]any{"packages": items})
	}
	if len(items) == 0 {
		fmt.Fprintln(w.out, "No software in the configuration yet. Add some with: starpoint install <package>")
		return nil
	}
	rows := make([][]string, 0, len(items))
	rows = append(rows, []string{"PACKAGE", "LOCATION", "SCOPE", "PROVIDER", "VERSION", "STATUS"})
	for _, it := range items {
		rows = append(rows, []string{
			it.Name, dash(it.Path), it.Scope, dash(it.Provider), dash(it.Version), it.Status,
		})
	}
	return w.table(rows)
}

// Info renders `starpoint info <package>`.
func (w *Writer) Info(st model.State) error {
	if w.asJSON {
		return w.emit(map[string]any{"package": st})
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
		_, err := io.WriteString(w.out, b.String())
		return err
	}
	return nil
}

// SyncResult is the machine-readable shape of `starpoint sync`.
type SyncResult struct {
	DryRun        bool            `json:"dry_run"`
	Converged     bool            `json:"converged"`
	ConfigChanged bool            `json:"config_changed"`
	ConfigPath    string          `json:"config_path,omitempty"`
	Actions       []ActionResult  `json:"actions"`
	States        []model.State   `json:"states"`
	Warnings      []model.Warning `json:"warnings,omitempty"`
	Problems      []string        `json:"problems,omitempty"`
	Project       *project.Info   `json:"project,omitempty"`
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
	out := SyncResult{
		DryRun:        res.DryRun,
		ConfigChanged: res.ConfigChanged,
		ConfigPath:    res.ConfigPath,
		Actions:       actions,
		States:        plan.States,
		Warnings:      res.Warnings,
		Project:       proj,
	}
	out.Converged = plan.Empty()

	if w.asJSON {
		return w.emit(out)
	}

	if len(actions) == 0 {
		fmt.Fprintf(w.out, "In sync. %d package(s) match the configuration.\n", len(plan.States))
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
	if res.ConfigChanged {
		fmt.Fprintf(w.out, "\nConfiguration updated: %s\n", res.ConfigPath)
	}
	w.writeWarnings(res.Warnings)
	return nil
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
	Starpoint StarpointInfo    `json:"starpoint"`
	Config    ConfigInfo       `json:"config"`
	Providers []ProviderHealth `json:"providers"`
	Ownership []model.State    `json:"ownership"`
	Problems  []model.Warning  `json:"problems,omitempty"`
	Healthy   bool             `json:"healthy"`
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
		return w.emit(rep)
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

// Result renders the outcome of install or remove.
func (w *Writer) Result(op string, res *engine.Result) error {
	changed := res.ConfigChanged && !res.DryRun
	if w.asJSON {
		return w.emit(map[string]any{
			"operation":      op,
			"dry_run":        res.DryRun,
			"config_changed": changed,
			"config_path":    res.ConfigPath,
			"actions":        res.Applied,
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
			verb = "recorded"
		case model.ActionRemove:
			verb = "removed"
		case model.ActionUpdate:
			verb = "updated"
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

// Init renders the outcome of `starpoint init`.
func (w *Writer) Init(path string, created, replaced bool) error {
	if w.asJSON {
		return w.emit(map[string]any{"path": path, "created": created, "replaced": replaced})
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

// Render writes arbitrary structured data as JSON.
func (w *Writer) Render(v any) error {
	if !w.asJSON {
		return nil
	}
	return w.emit(v)
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
