package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// nodeProject makes a Node project with a lockfile and puts node and npm on a
// scripted PATH, attributed to Homebrew.
func nodeProject(t *testing.T, h *harness) string {
	t.Helper()
	dir := t.TempDir()
	testsupport.WriteFile(t, filepath.Join(dir, "package.json"), `{"name":"demo"}`)
	testsupport.WriteFile(t, filepath.Join(dir, "package-lock.json"), `{}`)
	h.env.WorkDir = dir
	h.setLocator(map[string]string{
		"node": "/opt/homebrew/bin/node",
		"npm":  "/opt/homebrew/bin/npm",
	})
	return dir
}

func TestInstallDotInstallsDependenciesWithTheEcosystemTool(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	dir := nodeProject(t, h)
	globalBefore := h.configBody()

	out := h.mustRun("install", ".")
	if len(h.ran) != 1 || h.ran[0] != dir+": npm ci" {
		t.Fatalf("expected npm ci in the project, got %v", h.ran)
	}
	if !h.asked() || !strings.Contains(out, "Run npm ci") {
		t.Errorf("the dependency install should be described and confirmed:\n%s", out)
	}

	// The toolchain goes to the project's own configuration, which is created.
	projectConfig := testsupport.ReadFile(t, filepath.Join(dir, ".starpoint.toml"))
	for _, want := range []string{`name = "node"`, `name = "npm"`, `provider = "brew"`} {
		if !strings.Contains(projectConfig, want) {
			t.Errorf("project config should record %s:\n%s", want, projectConfig)
		}
	}
	if h.configBody() != globalBefore {
		t.Errorf("the global configuration must be left alone:\n%s", h.configBody())
	}
}

func TestInstallDotDryRunChangesNothing(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	dir := nodeProject(t, h)

	out := h.mustRun("install", ".", "--dry-run")
	if len(h.ran) != 0 {
		t.Errorf("a dry run must not run anything: %v", h.ran)
	}
	if !strings.Contains(out, "Would run: npm ci") {
		t.Errorf("the dry run should say what it would run:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".starpoint.toml")); err == nil {
		t.Error("a dry run must not create the project configuration")
	}
}

func TestInstallDotDeclinedChangesNothing(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	dir := nodeProject(t, h)
	h.ask(false)

	out := h.mustRun("install", ".")
	if len(h.ran) != 0 {
		t.Errorf("declining must not run anything: %v", h.ran)
	}
	if _, err := os.Stat(filepath.Join(dir, ".starpoint.toml")); err == nil {
		t.Error("declining must not create the project configuration")
	}
	if !strings.Contains(out, "Nothing was changed") {
		t.Errorf("the refusal should be acknowledged:\n%s", out)
	}
}

func TestInstallDotUnattendedNeedsNoConfirm(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	nodeProject(t, h)
	h.unattended()
	if code, _, _ := h.run("install", "."); code == 0 {
		t.Error("an unconfirmed dependency install must not run unattended")
	}
	if len(h.ran) != 0 {
		t.Errorf("nothing may run: %v", h.ran)
	}
	h.mustRun("install", ".", "--no-confirm")
	if len(h.ran) != 1 {
		t.Errorf("--no-confirm should run it: %v", h.ran)
	}
}

func TestInstallDotJSONReportsTheDependencyCommand(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	nodeProject(t, h)
	out := h.mustRun("install", ".", "--no-confirm", "--json")
	var payload struct {
		Dependencies struct {
			Command struct {
				Tool string   `json:"tool"`
				Args []string `json:"args"`
			} `json:"command"`
			Ran bool `json:"ran"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("stdout must be one JSON document: %v\n%s", err, out)
	}
	if deps := payload.Dependencies; deps.Command.Tool != "npm" || !deps.Ran {
		t.Errorf("unexpected dependency report: %+v", deps)
	}
}

func TestInstallDotReportsAFailedDependencyInstall(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	nodeProject(t, h)
	h.env.RunInDir = nil // the real runner; npm is not resolvable through the harness
	t.Setenv("PATH", t.TempDir())
	code, _, errOut := h.run("install", ".", "--no-confirm")
	if code == 0 || !strings.Contains(errOut, "npm") {
		t.Errorf("a dependency install that cannot run must fail and say why, got %d %q", code, errOut)
	}
}

func TestInstallDotReportsWhatTheDependencyInstallerChanged(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	dir := nodeProject(t, h)
	h.env.RunInDir = func(_ context.Context, d string, _, _ io.Writer, name string, args ...string) error {
		if err := os.MkdirAll(filepath.Join(d, "node_modules"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(d, "package-lock.json"), []byte(`{"lockfileVersion":3}`), 0o644)
	}
	out := h.mustRun("install", ".", "--no-confirm", "--json")
	var payload struct {
		Dependencies struct {
			Effects []model.Effect `json:"effects"`
		} `json:"dependencies"`
		Summary map[string]any `json:"summary"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var created, rewritten bool
	for _, e := range payload.Dependencies.Effects {
		if e.Path == filepath.Join(dir, "node_modules") && e.Op == model.EffectCreate && e.Kind == model.EffectDirectory {
			created = true
		}
		if e.Path == filepath.Join(dir, "package-lock.json") && e.Destructive {
			rewritten = true
		}
	}
	if !created || !rewritten {
		t.Errorf("the installer's effects on the project are reported: %+v", payload.Dependencies.Effects)
	}
	if payload.Summary == nil {
		t.Error("install . carries the operation summary")
	}

	dry := h.mustRun("install", ".", "--dry-run")
	if !strings.Contains(dry, "would modify directory "+dir) {
		t.Errorf("the dry run says where the installer will write:\n%s", dry)
	}
}
