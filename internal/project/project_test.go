package project

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/starpoint/starpoint/internal/model"
)

func TestDetectFindsMarkersByWalkingUp(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "Cargo.toml"))
	nested := filepath.Join(root, "src", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	info := Detect(nested)
	if info.Kind != KindRust {
		t.Fatalf("expected a Rust project, got %q", info.Kind)
	}
	if info.Root != root {
		t.Errorf("root should be %s, got %s", root, info.Root)
	}
	if info.Marker != "Cargo.toml" {
		t.Errorf("marker should be reported, got %q", info.Marker)
	}
}

func TestDetectRecognisesEachEcosystem(t *testing.T) {
	cases := map[string]Kind{
		"go.mod":           KindGo,
		"Cargo.toml":       KindRust,
		"package.json":     KindNode,
		"pyproject.toml":   KindPython,
		"requirements.txt": KindPython,
		"Gemfile":          KindRuby,
	}
	for marker, want := range cases {
		dir := t.TempDir()
		touch(t, filepath.Join(dir, marker))
		got := Detect(dir)
		if got.Kind != want {
			t.Errorf("%s detected as %q, want %q", marker, got.Kind, want)
		}
	}
}

func TestDetectOutsideAProject(t *testing.T) {
	// A temp directory has no marker, and walking up from it must not find one in
	// an unrelated project.
	dir := t.TempDir()
	info := Detect(dir)
	if info.Kind != KindUnknown {
		t.Fatalf("expected no project, got %q at %s", info.Kind, info.Root)
	}
	if info.String() == "" {
		t.Error("even an unknown project should render something useful")
	}
}

func TestToolchainsAreNeverInferredAsRequirements(t *testing.T) {
	// Every tool the markers name is a language runtime, and none of them may
	// become an installable requirement: `starpoint install .` must never try to
	// install rustc because it saw a Cargo.toml.
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "Cargo.toml"))
	info := Detect(dir)
	if len(info.Requirements()) != 0 {
		t.Errorf("a Rust project must not imply installable requirements, got %+v", info.Requirements())
	}
	tools := info.ExpectedTools()
	if len(tools) == 0 {
		t.Fatal("the expected toolchain should still be reported")
	}
	for _, tool := range tools {
		if !tool.Bootstrap {
			t.Errorf("%s should be marked as a bootstrap tool", tool.Name)
		}
	}
}

func TestRequirementsAreProjectScopedAndUndecided(t *testing.T) {
	// A hypothetical ecosystem entry with a non-bootstrap tool is what produces
	// requirements; the ones Starpoint ships today are all bootstrap tools, so
	// this asserts the contract of Requirements with a synthetic Info.
	got := requirementsFor("/proj", Tool{Name: "ruff"}, Tool{Name: "go", Bootstrap: true})
	if len(got) != 1 {
		t.Fatalf("only the non-bootstrap tool should be proposed, got %+v", got)
	}
	r := got[0]
	if r.Name != "ruff" {
		t.Errorf("unexpected requirement: %+v", r)
	}
	if r.Scope != model.ScopeProject {
		t.Errorf("inferred requirements must be project-scoped, got %q", r.Scope)
	}
	if !r.Origin.Project {
		t.Errorf("inferred requirements must be marked project-local: %+v", r.Origin)
	}
	if r.Provider != "" {
		t.Errorf("an inferred requirement must not carry a provider decision: %+v", r)
	}
}

func TestUnknownProjectHasNoRequirements(t *testing.T) {
	info := Detect(t.TempDir())
	if reqs := info.Requirements(); len(reqs) != 0 {
		t.Errorf("unknown projects must infer nothing, got %+v", reqs)
	}
}

func TestDetectsProjectConfigPresence(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "go.mod"))
	if Detect(dir).HasProjectConfig {
		t.Error("no project config yet")
	}
	touch(t, filepath.Join(dir, ".starpoint.toml"))
	if !Detect(dir).HasProjectConfig {
		t.Error("the project config should be detected")
	}
}

func TestStringDescribesTheProject(t *testing.T) {
	dir := t.TempDir()
	touch(t, filepath.Join(dir, "Cargo.toml"))
	s := Detect(dir).String()
	if s == "" {
		t.Fatal("expected a description")
	}
	for _, want := range []string{"rust", "Cargo.toml", dir} {
		if !contains(s, want) {
			t.Errorf("description %q should mention %q", s, want)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(needle) == 0 || (len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(""), 0o644); err != nil {
		t.Fatalf("creating %s: %v", path, err)
	}
}
