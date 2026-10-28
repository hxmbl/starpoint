package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
)

func TestLoadMissingFileIsEmptyNotAnError(t *testing.T) {
	doc, err := Load(LoadOptions{GlobalPath: filepath.Join(t.TempDir(), "nope.toml")})
	if err != nil {
		t.Fatalf("loading a missing config should succeed, got: %v", err)
	}
	if doc.Global.Exists {
		t.Fatal("a missing file must be reported as not existing")
	}
	if got := doc.Packages(); len(got) != 0 {
		t.Fatalf("expected no packages, got %d", len(got))
	}
}

func TestLoadParsesPackages(t *testing.T) {
	path := writeConfig(t, `
schema = 1

[[package]]
name = "ripgrep"

[[package]]
name = "ruff"
version = "0.5.0"

[[package]]
name = "firefox"
channel = "cask"
provider = "brew"
scope = "user"
`)
	doc := MustLoad(t, path)
	pkgs := doc.Packages()
	if len(pkgs) != 3 {
		t.Fatalf("expected 3 packages, got %d", len(pkgs))
	}
	if pkgs[0].Name != "ripgrep" || pkgs[1].Name != "ruff" || pkgs[2].Name != "firefox" {
		t.Fatalf("unexpected package order: %+v", pkgs)
	}
	if pkgs[1].Version != "0.5.0" {
		t.Errorf("version pin lost: %+v", pkgs[1])
	}
	if pkgs[2].Channel != "cask" || pkgs[2].Provider != "brew" || pkgs[2].Scope != model.ScopeUser {
		t.Errorf("channel/provider/scope lost: %+v", pkgs[2])
	}
	if pkgs[0].Origin.Path != path {
		t.Errorf("origin not recorded: %+v", pkgs[0].Origin)
	}
}

func TestLoadMalformedTOMLIsAnError(t *testing.T) {
	tests := map[string]string{
		"unterminated string": "schema = 1\n[[package]]\nname = \"oops\n",
		"garbage":             "this is not toml at all {{{",
		"bad key type":        "schema = \"one\"\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeConfig(t, body)
			if _, err := Load(LoadOptions{GlobalPath: path}); err == nil {
				t.Fatal("expected a parse error")
			}
		})
	}
}

func TestUnknownKeyIsRejected(t *testing.T) {
	path := writeConfig(t, "schema = 1\n\n[[package]]\nname = \"git\"\nprovder = \"brew\"\n")
	_, err := Load(LoadOptions{GlobalPath: path})
	if err == nil {
		t.Fatal("a misspelled key must be rejected rather than silently ignored")
	}
	if !strings.Contains(err.Error(), "provder") {
		t.Fatalf("error should name the unknown key, got: %v", err)
	}
}

func TestWarningsForDuplicateAndUnnamedPackages(t *testing.T) {
	path := writeConfig(t, `
schema = 1

[[package]]
name = "git"

[[package]]
name = "git"

[[package]]
version = "1.0"
`)
	doc := MustLoad(t, path)
	joined := strings.Join(doc.Warnings, "\n")
	if !strings.Contains(joined, "declared more than once") {
		t.Errorf("expected a duplicate warning, got: %v", doc.Warnings)
	}
	if !strings.Contains(joined, "has no name") {
		t.Errorf("expected a missing-name warning, got: %v", doc.Warnings)
	}
}

func TestFutureSchemaProducesWarning(t *testing.T) {
	path := writeConfig(t, "schema = 999\n")
	doc := MustLoad(t, path)
	if len(doc.Warnings) == 0 {
		t.Fatal("a newer schema version must produce a warning")
	}
}

func TestWriteAndReloadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".starpoint.toml")
	raw := Raw{
		Schema: SchemaVersion,
		Packages: []model.Package{
			{Name: "ripgrep", Provider: "brew", Reason: "system package manager"},
			{Name: "ruff", Version: "0.5.0", Provider: "uv"},
		},
	}
	if err := Write(path, raw); err != nil {
		t.Fatalf("write: %v", err)
	}
	doc := MustLoad(t, path)
	pkgs := doc.Packages()
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 packages after reload, got %d", len(pkgs))
	}
	if pkgs[0].Provider != "brew" || pkgs[0].Reason == "" {
		t.Errorf("provider decision did not survive the round trip: %+v", pkgs[0])
	}
	if pkgs[1].Version != "0.5.0" || pkgs[1].Provider != "uv" {
		t.Errorf("pin or provider lost: %+v", pkgs[1])
	}
}

func TestSavePreservesEmptyProjectConfigWhenEmptied(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.toml")
	project := filepath.Join(dir, "project.toml")

	if err := Write(global, Raw{Packages: []model.Package{{Name: "git"}}}); err != nil {
		t.Fatalf("write global: %v", err)
	}
	if err := Write(project, Raw{Packages: []model.Package{{Name: "cargo"}}}); err != nil {
		t.Fatalf("write project: %v", err)
	}

	doc := MustLoad(t, global)
	doc.Projects = []File{mustProjectFile(t, project)}
	// Empty the project file the way removing the last entry would.
	doc.Projects[0].Raw.Packages = nil
	if err := doc.Save(); err != nil {
		t.Fatalf("save: %v", err)
	}
	f, _, err := LoadFile(project, true)
	if err != nil {
		t.Fatalf("reload project: %v", err)
	}
	if !f.Exists {
		t.Fatal("an emptied project config should remain so its comments and intent are preserved")
	}
}

func TestPackagesMergeProjectOverGlobal(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.toml")
	project := filepath.Join(dir, "project.toml")
	if err := Write(global, Raw{Packages: []model.Package{
		{Name: "ruff", Provider: "brew"},
		{Name: "git", Provider: "system"},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := Write(project, Raw{Packages: []model.Package{
		{Name: "ruff", Provider: "uv"},
	}}); err != nil {
		t.Fatal(err)
	}

	doc := MustLoad(t, global)
	doc.Projects = []File{mustProjectFile(t, project)}

	pkgs := doc.Packages()
	if len(pkgs) != 2 {
		t.Fatalf("expected 2 merged packages, got %d", len(pkgs))
	}
	ruff, ok := doc.Package("ruff")
	if !ok {
		t.Fatal("ruff missing from merged view")
	}
	if ruff.Provider != "uv" {
		t.Errorf("the project-local decision must win, got provider %q", ruff.Provider)
	}
	if !ruff.Origin.Project {
		t.Error("origin should be marked as project-local")
	}
}

func TestFindLocatesOwningFile(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "global.toml")
	project := filepath.Join(dir, "project.toml")
	_ = Write(global, Raw{Packages: []model.Package{{Name: "git"}}})
	_ = Write(project, Raw{Packages: []model.Package{{Name: "cargo"}}})

	doc := MustLoad(t, global)
	doc.Projects = []File{mustProjectFile(t, project)}

	f, ok := doc.Find("cargo")
	if !ok || f.Path != project {
		t.Errorf("cargo should be found in the project file, got %+v", f)
	}
	f, ok = doc.Find("git")
	if !ok || f.Path != global {
		t.Errorf("git should be found in the global file, got %+v", f)
	}
}

func TestProjectFileForUsesDeepestMatch(t *testing.T) {
	dir := t.TempDir()
	outer := filepath.Join(dir, "outer.toml")
	inner := filepath.Join(dir, "inner.toml")

	doc := &Document{}
	doc.Projects = []File{
		{Path: outer, Project: true},
		{Path: inner, Project: true},
	}
	f, ok := doc.ProjectFileFor(filepath.Join(dir, "sub", "deeper"))
	if !ok {
		t.Fatal("expected a match")
	}
	if f.Path != inner {
		t.Errorf("the most deeply nested project must win, got %s", f.Path)
	}
	if _, ok := doc.ProjectFileFor(filepath.Join(t.TempDir(), "elsewhere")); ok {
		t.Error("a directory outside every project must not match")
	}
}

func TestSettingsMergeProjectOverGlobal(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "g.toml")
	project := filepath.Join(dir, "p.toml")
	_ = Write(global, Raw{Settings: Settings{Providers: []string{"brew"}}})
	_ = Write(project, Raw{Settings: Settings{Providers: []string{"uv"}, NoConfirm: true}})

	doc := MustLoad(t, global)
	doc.Projects = []File{mustProjectFile(t, project)}
	s := doc.Settings()
	if len(s.Providers) != 1 || s.Providers[0] != "uv" {
		t.Errorf("project settings should override global ones, got %v", s.Providers)
	}
	if !s.NoConfirm {
		t.Error("project no_confirm should be honored")
	}
}

func TestStateDirFollowsTheConfigurationPath(t *testing.T) {
	// This is what keeps a run against a temporary configuration from writing
	// records into a real home directory, which is what the tests rely on.
	got, err := StateDir(filepath.Join("/tmp", "somewhere", ".starpoint.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join("/tmp", "somewhere", StateDirName)
	if got != want {
		t.Errorf("StateDir = %q, want %q", got, want)
	}
}

// With no path at all, the state directory is the one beside the default global
// configuration.
func TestStateDirDefaultsBesideTheDefaultConfiguration(t *testing.T) {
	global, err := DefaultGlobalPath()
	if err != nil {
		t.Fatal(err)
	}
	got, err := StateDir("")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(filepath.Dir(global), StateDirName) {
		t.Errorf("StateDir(\"\") = %q, want it beside %q", got, global)
	}
}

func TestRetentionDaysIsReadFromTheSettingsTable(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "g.toml")
	_ = Write(global, Raw{Settings: Settings{RetentionDays: intPtr(45)}})
	if got := MustLoad(t, global).Settings().Retention(); got != 45 {
		t.Errorf("retention_days = %d, want 45", got)
	}

	unset := filepath.Join(dir, "u.toml")
	_ = Write(unset, Raw{})
	if got := MustLoad(t, unset).Settings().Retention(); got != 0 {
		t.Errorf("an unset retention must stay 0 so the default applies, got %d", got)
	}
}

// Retention governs the machine's records, so a checkout must not be able to
// change it for everything.
func TestRetentionDaysIgnoresProjectFiles(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "g.toml")
	project := filepath.Join(dir, "p.toml")
	_ = Write(global, Raw{Settings: Settings{RetentionDays: intPtr(45)}})
	_ = Write(project, Raw{Settings: Settings{RetentionDays: intPtr(1)}})

	doc := MustLoad(t, global)
	doc.Projects = []File{mustProjectFile(t, project)}
	if got := doc.Settings().Retention(); got != 45 {
		t.Errorf("a project file changed the machine-wide retention window to %d", got)
	}
}

func TestTemplateDocumentsEverySupportedKey(t *testing.T) {
	text := RenderTemplate()
	for _, key := range []string{"name", "version", "provider", "scope", "channel", "reason", "providers", "no_confirm", "retention_days"} {
		if !strings.Contains(text, key) {
			t.Errorf("the printed documentation should mention %q", key)
		}
	}
	if !strings.Contains(text, GlobalFileName) {
		t.Errorf("the documentation should name the file it describes")
	}
}

func TestTemplateIsPrintableNotAConfigFile(t *testing.T) {
	// init prints this; if it were ever written to disk as-is it would not parse.
	if strings.HasPrefix(strings.TrimSpace(RenderTemplate()), "schema") {
		t.Error("the printed documentation must not look like a config file")
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".starpoint.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	return path
}

func MustLoad(t *testing.T, path string) *Document {
	t.Helper()
	doc, err := Load(LoadOptions{GlobalPath: path})
	if err != nil {
		t.Fatalf("loading %s: %v", path, err)
	}
	return doc
}

// mustProjectFile loads a project-local file the way project discovery would.
func mustProjectFile(t *testing.T, path string) File {
	t.Helper()
	f, _, err := LoadFile(path, true)
	if err != nil {
		t.Fatalf("loading project file %s: %v", path, err)
	}
	return f
}

// Execution tracking is now handled by the tracking package, not config.
// See internal/tracking for execution record tests.

func intPtr(n int) *int { return &n }

// retention_days written as 0 is the user's own line and survives a rewrite;
// an unset value is not invented. The TOML encoder never omits a plain int,
// which used to put `retention_days = 0` into every file Starpoint saved.
func TestRetentionDaysRoundTripsExactlyAsWritten(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string // the line expected after a rewrite, "" for none
		value int
	}{
		{"unset", "schema = 1\n", "", 0},
		{"explicit zero", "schema = 1\n\n[settings]\nretention_days = 0\n", "retention_days = 0", 0},
		{"explicit window", "schema = 1\n\n[settings]\nretention_days = 45\n", "retention_days = 45", 45},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "g.toml")
			if err := os.WriteFile(path, []byte(tc.input), 0o644); err != nil {
				t.Fatal(err)
			}
			doc := MustLoad(t, path)
			if err := Write(path, doc.Global.Raw); err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			has := strings.Contains(string(body), "retention_days")
			switch {
			case tc.want == "" && has:
				t.Errorf("an unset retention_days was written:\n%s", body)
			case tc.want != "" && !strings.Contains(string(body), tc.want):
				t.Errorf("want %q after the rewrite:\n%s", tc.want, body)
			}
			if got := MustLoad(t, path).Settings().Retention(); got != tc.value {
				t.Errorf("Retention() = %d, want %d", got, tc.value)
			}
		})
	}
}
