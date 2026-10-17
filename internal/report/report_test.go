package report

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/color"
	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/locator"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
)

func TestListHumanOutputHasColumns(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, false)
	states := []model.State{
		{
			Package: model.Package{Name: "ripgrep"},
			Res:     model.Resolution{Provider: "brew"},
			Desired: &model.Installation{Name: "ripgrep", Path: "/opt/homebrew/bin/rg", Provider: "brew", Scope: model.ScopeUser, Version: "14.1.0"},
			Status:  model.StatusCorrect,
		},
		{
			Package: model.Package{Name: "python3"},
			Res:     model.Resolution{Provider: "uv"},
			Current: &model.Installation{Name: "python3", Path: "/usr/bin/python3", Provider: "system", Scope: model.ScopeGlobal},
			Status:  model.StatusProviderDiffers,
		},
	}
	if err := w.List(states); err != nil {
		t.Fatalf("list: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"PACKAGE", "LOCATION", "SCOPE", "PROVIDER", "VERSION", "STATUS",
		"ripgrep", "/opt/homebrew/bin/rg", "14.1.0", "correct",
		"python3", "/usr/bin/python3", "system", "provider-differs",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// Columns must line up; a mangled table is worse than none.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected a header and two rows, got %d lines:\n%s", len(lines), out)
	}
	if !strings.HasPrefix(lines[0], "PACKAGE") {
		t.Errorf("header should start the table:\n%s", out)
	}
}

func TestListRendersMissingValuesAsDashes(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, false)
	if err := w.List([]model.State{{
		Package: model.Package{Name: "x"},
		Res:     model.Resolution{Provider: "brew"},
		Status:  model.StatusMissing,
	}}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Count(out, "-") < 2 {
		t.Errorf("absent values should render as dashes:\n%s", out)
	}
}

func TestListJSONIsValidAndComplete(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, true)
	if err := w.List([]model.State{{
		Package: model.Package{Name: "git", Version: "2.43.0"},
		Res:     model.Resolution{Provider: "brew", Reason: "only one package manager"},
		Desired: &model.Installation{Name: "git", Path: "/usr/bin/git", Provider: "brew", Scope: model.ScopeGlobal, Version: "2.43.0"},
		Status:  model.StatusCorrect,
	}}); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Packages []ListItem `json:"packages"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	if len(payload.Packages) != 1 {
		t.Fatalf("expected 1 row, got %+v", payload.Packages)
	}
	p := payload.Packages[0]
	if p.Name != "git" || p.Provider != "brew" || !p.Pinned || p.Version != "2.43.0" {
		t.Errorf("JSON row lost information: %+v", p)
	}
}

func TestInfoShowsTheWholeRelationship(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, false)
	err := w.Info(model.State{
		Package: model.Package{
			Name: "ruff", Version: "0.6.0",
			Origin: model.Origin{Path: "/home/u/.starpoint.toml"},
		},
		Res:     model.Resolution{Provider: "uv", Reason: "Python software"},
		Desired: &model.Installation{Name: "ruff", Path: "/home/u/.local/bin/ruff", Provider: "uv", Version: "0.6.0"},
		Status:  model.StatusCorrect,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"Installed:", "/home/u/.local/bin/ruff",
		"Version:", "0.6.0",
		"Provider:", "uv",
		"Desired provider:", "uv",
		"Configured:", "global",
		"Status:", "correct",
		"Pinned version:", "0.6.0",
		"Why this provider:", "Python software",
		"Declared in:", "/home/u/.starpoint.toml",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("info output missing %q:\n%s", want, out)
		}
	}
}

func TestInfoSaysSoWhenNothingIsInstalled(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, false)
	if err := w.Info(model.State{
		Package: model.Package{Name: "nope"},
		Res:     model.Resolution{Provider: "brew"},
		Status:  model.StatusMissing,
	}, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "not installed") {
		t.Errorf("absent software should say so:\n%s", out)
	}
}

func TestSyncEmptyPlanSaysInSync(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, false)
	plan := model.Plan{States: []model.State{{Package: model.Package{Name: "git"}, Status: model.StatusCorrect}}}
	res := &engine.Result{States: plan.States}
	if err := w.Sync(res, plan, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "In sync") {
		t.Errorf("expected an in-sync message:\n%s", buf.String())
	}
}

func TestSyncDryRunSaysNothingChanged(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, false)
	plan := model.Plan{
		DryRun: true,
		States: []model.State{{Package: model.Package{Name: "ripgrep"}, Res: model.Resolution{Provider: "brew"}}},
		Actions: []model.PlannedAction{{
			Action: model.ActionInstall,
			State:  model.State{Package: model.Package{Name: "ripgrep"}, Res: model.Resolution{Provider: "brew"}},
			Note:   "not recorded yet",
		}},
	}
	if err := w.Sync(&engine.Result{DryRun: true, States: plan.States}, plan, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "would") || !strings.Contains(out, "Dry run") {
		t.Errorf("a dry run must be explicit:\n%s", out)
	}
	if !strings.Contains(out, "not recorded yet") {
		t.Errorf("plan notes must be shown:\n%s", out)
	}
}

func TestDoctorJSONCarriesCapabilitiesAndProblems(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, true)
	rep := DoctorReport{
		Starpoint: StarpointInfo{Version: "0.1.0", Path: "/usr/local/bin/starpoint", Provider: "brew"},
		Config:    ConfigInfo{Path: "/home/u/.starpoint.toml", Exists: true, Schema: 1, PackageCount: 2, Writable: true},
		Providers: []ProviderHealth{{
			Name: "apt", Description: "APT", Kind: provider.KindApt,
			Available: false, Reason: "apt-get not found on PATH",
			Capabilities: provider.Capabilities{Discover: true, PinVersion: true, DefaultScope: model.ScopeGlobal},
		}},
		Ownership: []model.State{{Package: model.Package{Name: "ripgrep"}, Res: model.Resolution{Provider: "brew"}, Status: model.StatusCorrect}},
		Problems:  []model.Warning{{Severity: model.SeverityWarn, Package: "ripgrep", Message: "not installed"}},
		Healthy:   false,
	}
	if err := w.Doctor(rep); err != nil {
		t.Fatal(err)
	}
	var round map[string]any
	if err := json.Unmarshal(buf.Bytes(), &round); err != nil {
		t.Fatalf("not valid JSON: %v\n%s", err, buf.String())
	}
	providers, ok := round["providers"].([]any)
	if !ok || len(providers) != 1 {
		t.Fatalf("providers missing from JSON: %s", buf.String())
	}
	caps := providers[0].(map[string]any)["capabilities"].(map[string]any)
	if caps["PinVersion"] != true {
		t.Errorf("capabilities must be exposed to machines, not just humans: %s", buf.String())
	}
	if round["healthy"] != false {
		t.Error("a report with problems is not healthy")
	}
}

func TestResultUsesTheActionVerb(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, false)
	res := &engine.Result{
		ConfigChanged: true,
		ConfigPath:    "/home/u/.starpoint.toml",
		Applied: []model.PlannedAction{{
			Action: model.ActionAdopt,
			State:  model.State{Package: model.Package{Name: "ruff"}, Res: model.Resolution{Provider: "uv"}},
			Note:   "already installed",
		}},
	}
	if err := w.Result("installed", res); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "recorded ruff via uv") {
		t.Errorf("an adoption must not claim software was installed:\n%s", out)
	}
}

func TestResultDryRunDoesNotClaimAConfigChange(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, true)
	res := &engine.Result{DryRun: true, ConfigChanged: true, ConfigPath: "/x.toml"}
	if err := w.Result("installed", res); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["config_changed"] != false {
		t.Errorf("a dry run cannot have changed anything: %s", buf.String())
	}
	if payload["dry_run"] != true {
		t.Errorf("dry_run flag missing: %s", buf.String())
	}
}

func TestDocumentInfo(t *testing.T) {
	doc := &config.Document{
		Global: config.File{Path: "/home/u/.starpoint.toml", Exists: true, Raw: config.Raw{Schema: 1}},
	}
	info := DocumentInfo(doc, "/home/u/.starpoint.toml", true)
	if !info.Exists || info.Schema != 1 || !info.Writable {
		t.Errorf("document info wrong: %+v", info)
	}
	if info.Path != "/home/u/.starpoint.toml" {
		t.Errorf("path wrong: %+v", info)
	}
}

func TestTableAligns(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, false)
	plan := model.Plan{
		States: []model.State{
			{Package: model.Package{Name: "a"}, Status: model.StatusCorrect, Res: model.Resolution{Provider: "brew"}},
		},
	}
	_ = plan
	if err := w.List([]model.State{
		{Package: model.Package{Name: "short"}, Res: model.Resolution{Provider: "brew"}, Status: model.StatusCorrect},
		{Package: model.Package{Name: "muchlonger"}, Res: model.Resolution{Provider: "apt"}, Status: model.StatusMissing},
	}); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected three lines, got %d", len(lines))
	}
	// The second column must start at the same offset on every row.
	col := strings.Index(lines[1], "brew")
	col2 := strings.Index(lines[2], "apt")
	if col != col2 {
		t.Errorf("columns are not aligned: %d vs %d\n%s", col, col2, buf.String())
	}
}

func TestResultDryRunUsesProspectiveVerbs(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, false)
	res := &engine.Result{
		DryRun: true,
		Applied: []model.PlannedAction{
			{
				Action: model.ActionInstall,
				State:  model.State{Package: model.Package{Name: "stable"}, Res: model.Resolution{Provider: "rustup"}},
			},
			{
				Action: model.ActionAdopt,
				State:  model.State{Package: model.Package{Name: "ruff"}, Res: model.Resolution{Provider: "uv"}},
			},
		},
	}
	if err := w.Result("install", res); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "would install stable via rustup") {
		t.Errorf("a dry run must not claim software was installed:\n%s", out)
	}
	if !strings.Contains(out, "would record ruff via uv") {
		t.Errorf("adoption must also be prospective:\n%s", out)
	}
}

func TestSyncDoesNotClaimConvergenceItDidNotReach(t *testing.T) {
	// A package whose provider cannot install it produces no action and is still
	// not converged. Saying "In sync" there would be a false claim.
	var buf bytes.Buffer
	w := New(&buf, false)
	plan := model.Plan{
		States: []model.State{
			{Package: model.Package{Name: "ruff"}, Res: model.Resolution{Provider: "uv"}, Status: model.StatusCorrect},
			{Package: model.Package{Name: "typescript"}, Res: model.Resolution{Provider: "yarn"}, Status: model.StatusMissing},
		},
	}
	if err := w.Sync(&engine.Result{States: plan.States}, plan, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "In sync") {
		t.Errorf("a missing package must not be reported as in sync:\n%s", out)
	}
	if !strings.Contains(out, "no way to change") {
		t.Errorf("the reason should be stated:\n%s", out)
	}
}

func TestSyncConvergenceRequiresHealthyStates(t *testing.T) {
	var buf bytes.Buffer
	w := New(&buf, true)
	plan := model.Plan{
		States: []model.State{
			{Package: model.Package{Name: "x"}, Status: model.StatusMissing},
		},
	}
	if err := w.Sync(&engine.Result{States: plan.States}, plan, nil); err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if sync, ok := payload["sync"].(map[string]any); ok {
		payload = sync
	}
	if payload["converged"] != false {
		t.Errorf("converged must be false while a package is unsatisfied: %s", buf.String())
	}
}

func TestDoctorSummarisesThePATHInventoryByDefault(t *testing.T) {
	var inv []locator.Found
	for i := 0; i < 40; i++ {
		inv = append(inv, locator.Found{Path: fmt.Sprintf("/usr/bin/tool%02d", i), Attributed: true,
			Installation: &model.Installation{Provider: "system"}})
	}
	inv = append(inv,
		locator.Found{Path: "/opt/x/bin/mystery", Reason: "ownership unknown"},
		locator.Found{Path: "/opt/y/bin/tool01", Duplicate: true, Attributed: true,
			Installation: &model.Installation{Provider: "brew"}},
	)
	rep := DoctorReport{PATHInventory: inv, PATHSummary: locator.Summarize(inv), Healthy: true}

	var buf bytes.Buffer
	if err := New(&buf, false).Doctor(rep); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "42 executables") || !strings.Contains(out, "1 shadowed") {
		t.Errorf("the summary should count the inventory:\n%s", out)
	}
	if !strings.Contains(out, "/opt/x/bin/mystery") || !strings.Contains(out, "/opt/y/bin/tool01") {
		t.Errorf("unknown and shadowed entries should be shown:\n%s", out)
	}
	if strings.Contains(out, "/usr/bin/tool05") {
		t.Errorf("ordinary attributed entries belong behind --all:\n%s", out)
	}
	if !strings.Contains(out, "doctor --all") {
		t.Errorf("the way to the full list should be named:\n%s", out)
	}

	buf.Reset()
	rep.ShowAllPATH = true
	if err := New(&buf, false).Doctor(rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "/usr/bin/tool05") {
		t.Error("--all must list every entry")
	}

	buf.Reset()
	rep.ShowAllPATH = false
	if err := New(&buf, true).Doctor(rep); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "/usr/bin/tool05") || !strings.Contains(buf.String(), `"path_summary"`) {
		t.Error("JSON keeps the full inventory and the summary")
	}
}

func TestEffectLinesReadAsSentences(t *testing.T) {
	cases := []struct {
		e       model.Effect
		planned bool
		want    string
	}{
		{model.Effect{Op: model.EffectDelete, Kind: model.EffectSymlink, Path: "/x/bin/ruff", From: "/x/uv/ruff"}, false, "deleted symlink /x/bin/ruff → /x/uv/ruff"},
		{model.Effect{Op: model.EffectDelete, Kind: model.EffectPackage, Name: "ruff", From: "0.5.0", Provider: "uv"}, true, "would uninstall ruff 0.5.0 (uv)"},
		{model.Effect{Op: model.EffectModify, Kind: model.EffectPackage, Name: "ruff", From: "0.5.0", To: "0.6.0", Provider: "uv"}, false, "changed ruff 0.5.0 → 0.6.0 (uv)"},
		{model.Effect{Op: model.EffectDelete, Kind: model.EffectCommand, Name: "ruff", From: "/x/bin/ruff"}, false, "ruff no longer resolves on PATH (was /x/bin/ruff)"},
		{model.Effect{Op: model.EffectReplace, Kind: model.EffectFile, Path: "/x/bin/cowsay"}, true, "would replace file /x/bin/cowsay"},
		{model.Effect{Op: model.EffectModify, Kind: model.EffectShellProfile, Path: "/x/.bashrc"}, false, "modified shell profile /x/.bashrc"},
		{model.Effect{Op: model.EffectCreate, Kind: model.EffectFile, Path: "/y/z", Evidence: model.EvidenceReported}, false, "created file /y/z (reported by the provider)"},
	}
	for _, tc := range cases {
		if got := EffectLine(tc.e, tc.planned); got != tc.want {
			t.Errorf("EffectLine(%+v) = %q, want %q", tc.e, got, tc.want)
		}
	}
}

func TestWriteEffectsAlwaysShowsDestructiveAndCountsTheRest(t *testing.T) {
	effects := []model.Effect{
		{Op: model.EffectCreate, Kind: model.EffectFile, Path: "/a"},
		{Op: model.EffectDelete, Kind: model.EffectFile, Path: "/b", Destructive: true},
	}
	var quiet, all strings.Builder
	off := color.Off()
	WriteEffects(&quiet, off, effects, false, false, "")
	WriteEffects(&all, off, effects, false, true, "")
	if !strings.Contains(quiet.String(), "! deleted file /b") || strings.Contains(quiet.String(), "/a") ||
		!strings.Contains(quiet.String(), "1 other change(s)") {
		t.Errorf("default output shows destructive effects and counts the rest:\n%s", quiet.String())
	}
	if !strings.Contains(all.String(), "created file /a") {
		t.Errorf("full output lists everything:\n%s", all.String())
	}
}

// Color is decoration, never information: stripping the escapes from a colored
// rendering gives the plain rendering byte for byte. That also guards the
// alignment, which is measured without the escapes.
func TestColorNeverChangesTheText(t *testing.T) {
	states := []model.State{
		{
			Package: model.Package{Name: "ripgrep", Provider: "brew", Version: "14.1.0", Channel: "stable"},
			Res:     model.Resolution{Provider: "brew", Reason: "declared in the configuration"},
			Desired: &model.Installation{Name: "ripgrep", Path: "/opt/homebrew/bin/rg", Provider: "brew", Scope: model.ScopeUser, Version: "14.1.0"},
			Status:  model.StatusCorrect,
		},
		{
			Package: model.Package{Name: "python3"},
			Res:     model.Resolution{Provider: "uv"},
			Current: &model.Installation{Name: "python3", Path: "/usr/bin/python3", Provider: "system", Scope: model.ScopeGlobal},
			Status:  model.StatusProviderDiffers,
		},
		{
			Package: model.Package{Name: "gone"},
			Res:     model.Resolution{Provider: "brew"},
			Status:  model.StatusMissing,
		},
	}

	render := func(mode color.Mode) string {
		t.Helper()
		var buf bytes.Buffer
		w := New(&buf, false)
		w.SetColor(mode)
		if err := w.List(states); err != nil {
			t.Fatalf("list: %v", err)
		}
		if err := w.Info(states[0], nil); err != nil {
			t.Fatalf("info: %v", err)
		}
		if err := w.Transitions([]model.Transition{{
			Package: model.Package{Name: "ruff"},
			From:    model.ProviderRef{Name: "pip"},
			To:      model.ProviderRef{Name: "uv"},
			Phase:   model.TransitionPlanned,
			Warnings: []model.Warning{{
				Severity: model.SeverityWarn,
				Code:     "stale",
				Message:  "recorded a while ago",
			}},
		}}, true); err != nil {
			t.Fatalf("transitions: %v", err)
		}
		return buf.String()
	}

	plain := render(color.Never)
	colored := render(color.Always)
	if strings.Contains(plain, "\033[") {
		t.Fatalf("the plain rendering should have no escapes:\n%q", plain)
	}
	if !strings.Contains(colored, "\033[") {
		t.Fatal("the colored rendering should have escapes")
	}
	if got := color.Strip(colored); got != plain {
		t.Errorf("stripping color changed the text\nplain:\n%s\nstripped:\n%s", plain, got)
	}
}
