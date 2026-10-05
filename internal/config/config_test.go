package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/starpoint/starpoint/internal/model"
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

func TestSavePersistsAndProjectConfigIsRemovedWhenEmptied(t *testing.T) {
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
	if f.Exists {
		t.Fatal("an emptied project config should be deleted, not left as an empty file")
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

func TestTemplateDocumentsEverySupportedKey(t *testing.T) {
	text := RenderTemplate()
	for _, key := range []string{"name", "version", "provider", "scope", "channel", "reason", "providers", "no_confirm"} {
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
