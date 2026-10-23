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

// npm ci deletes node_modules before installing. When there is one, running
// it unattended is a destructive run like any other: refused without
// --allow-destructive, with the deletion in the refusal's plan.
func TestInstallDotRefusesAnUnattendedNpmCiOverAnExistingNodeModules(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	dir := nodeProject(t, h)
	modules := filepath.Join(dir, "node_modules")
	testsupport.WriteFile(t, filepath.Join(modules, "patched.js"), "// a local change\n")

	code, _, errOut := h.run("install", ".", "--yes", "--json")
	if code != ExitRefusedDestructive {
		t.Fatalf("expected exit %d, got %d\n%s", ExitRefusedDestructive, code, errOut)
	}
	if len(h.ran) != 0 {
		t.Errorf("nothing may run: %v", h.ran)
	}
	if !strings.Contains(errOut, modules) || !strings.Contains(errOut, "destructive-unattended") {
		t.Errorf("the refusal names node_modules:\n%s", errOut)
	}

	h.mustRun("install", ".", "--yes", "--allow-destructive")
	if len(h.ran) != 1 || h.ran[0] != dir+": npm ci" {
		t.Errorf("--allow-destructive runs it: %v", h.ran)
	}
}

// Without node_modules there is nothing for npm ci to delete, and an
// unattended run goes ahead.
func TestInstallDotNpmCiIntoAFreshProjectIsNotDestructive(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	nodeProject(t, h)
	h.mustRun("install", ".", "--yes")
	if len(h.ran) != 1 {
		t.Errorf("a fresh npm ci runs unattended: %v", h.ran)
	}
}

// The dry run and the confirmation list the deletion, and the real run
// reports that node_modules was replaced.
func TestInstallDotListsAndObservesTheNodeModulesReplacement(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	dir := nodeProject(t, h)
	modules := filepath.Join(dir, "node_modules")
	testsupport.WriteFile(t, filepath.Join(modules, "patched.js"), "// a local change\n")
	h.env.RunInDir = func(_ context.Context, d string, _, _ io.Writer, name string, args ...string) error {
		h.ran = append(h.ran, d+": "+name+" "+strings.Join(args, " "))
		if err := os.RemoveAll(filepath.Join(d, "node_modules")); err != nil {
			return err
		}
		return os.MkdirAll(filepath.Join(d, "node_modules", "is-number"), 0o755)
	}

	dry := h.mustRun("install", ".", "--dry-run")
	if !strings.Contains(dry, "! would replace directory "+modules) {
		t.Errorf("the dry run lists the deletion:\n%s", dry)
	}
	out := h.mustRun("install", ".")
	if !strings.Contains(out, "! would replace directory "+modules) {
		t.Errorf("the confirmation lists the deletion with !:\n%s", out)
	}

	op := h.mustRun("install", ".", "--yes", "--allow-destructive", "--json")
	var payload struct {
		Dependencies struct {
			Effects []model.Effect `json:"effects"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(op), &payload); err != nil {
		t.Fatalf("%v\n%s", err, op)
	}
	var observed bool
	for _, e := range payload.Dependencies.Effects {
		if e.Path == modules && e.Op == model.EffectReplace && e.Destructive && e.Evidence == model.EvidenceObserved {
			observed = true
		}
	}
	if !observed {
		t.Errorf("the replaced node_modules is reported as observed and destructive: %+v", payload.Dependencies.Effects)
	}
}

// npm ci may empty node_modules in place rather than replace the directory;
// what it removed is still reported.
func TestInstallDotReportsWhatAnInPlaceNpmCiRemoved(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	dir := nodeProject(t, h)
	modules := filepath.Join(dir, "node_modules")
	patched := filepath.Join(modules, "patched.js")
	testsupport.WriteFile(t, patched, "// a local change\n")
	h.env.RunInDir = func(_ context.Context, d string, _, _ io.Writer, name string, args ...string) error {
		return os.Remove(filepath.Join(d, "node_modules", "patched.js"))
	}

	out := h.mustRun("install", ".", "--yes", "--allow-destructive", "--json")
	var payload struct {
		Dependencies struct {
			Effects []model.Effect `json:"effects"`
		} `json:"dependencies"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	var reported bool
	for _, e := range payload.Dependencies.Effects {
		if e.Path == patched && e.Op == model.EffectDelete && e.Destructive && e.Evidence == model.EvidenceObserved {
			reported = true
		}
	}
	if !reported {
		t.Errorf("the removed file is reported: %+v", payload.Dependencies.Effects)
	}
}

// pythonProject makes a requirements.txt project and a PATH holding only a
// pip3 in its own directory (or in pathDir when given). VIRTUAL_ENV is
// cleared, so no virtualenv is active unless the test sets one.
func pythonProject(t *testing.T, h *harness, pathDir string) string {
	t.Helper()
	dir := t.TempDir()
	testsupport.WriteFile(t, filepath.Join(dir, "requirements.txt"), "requests\n")
	h.env.WorkDir = dir
	h.setLocator(map[string]string{"python3": "/opt/homebrew/bin/python3", "python": "/opt/homebrew/bin/python"})
	if pathDir == "" {
		pathDir = t.TempDir()
	}
	writeExecutable(t, filepath.Join(pathDir, "pip3"))
	t.Setenv("PATH", pathDir)
	t.Setenv("VIRTUAL_ENV", "")
	return dir
}

func writeExecutable(t *testing.T, path string) {
	t.Helper()
	testsupport.WriteFile(t, path, "#!/bin/sh\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

type pipPayload struct {
	Warnings     []model.Warning `json:"warnings"`
	Dependencies struct {
		Command struct {
			Tool       string `json:"tool"`
			Virtualenv string `json:"virtualenv"`
		} `json:"command"`
		Ran     bool           `json:"ran"`
		Effects []model.Effect `json:"effects"`
	} `json:"dependencies"`
}

// Outside a virtualenv, pip install -r writes to the site-packages the whole
// account (as root, the machine) shares and can upgrade or downgrade what
// other software uses. Unattended, that is a destructive run: refused
// without --allow-destructive, planned and warned about either way.
func TestInstallDotTreatsPipOutsideAVirtualenvAsDestructive(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	dir := pythonProject(t, h, "")

	code, _, errOut := h.run("install", ".", "--yes", "--json")
	if code != ExitRefusedDestructive {
		t.Fatalf("expected exit %d, got %d\n%s", ExitRefusedDestructive, code, errOut)
	}
	if len(h.ran) != 0 {
		t.Errorf("nothing may run: %v", h.ran)
	}
	if !strings.Contains(errOut, "destructive-unattended") || !strings.Contains(errOut, "requirements.txt") {
		t.Errorf("the refusal names the pip install:\n%s", errOut)
	}

	dry := h.mustRun("install", ".", "--dry-run")
	for _, want := range []string{"! would install, upgrade or downgrade the Python packages requirements.txt names", "outside a virtualenv", "python3 -m venv .venv"} {
		if !strings.Contains(dry, want) {
			t.Errorf("the dry run shows %q:\n%s", want, dry)
		}
	}

	var payload pipPayload
	h.decode(h.mustRun("install", ".", "--yes", "--allow-destructive", "--json"), &payload)
	if len(h.ran) != 1 || h.ran[0] != dir+": pip3 install -r requirements.txt" {
		t.Fatalf("--allow-destructive runs pip3: %v", h.ran)
	}
	if !hasCode(payload.Warnings, model.WarnPipOutsideVirtualenv) {
		t.Errorf("the run warns that pip runs outside a virtualenv: %+v", payload.Warnings)
	}
	if payload.Dependencies.Command.Virtualenv != "" {
		t.Errorf("no virtualenv is reported: %+v", payload.Dependencies.Command)
	}
	if e := findEffect(payload.Dependencies.Effects, model.EffectReplace, model.EffectPackage, ""); e == nil || !e.Destructive || e.Evidence != model.EvidencePlanned {
		t.Errorf("a real run still reports the planned destructive effect: %+v", payload.Dependencies.Effects)
	}
}

// Inside the active virtualenv, pip only changes that environment: an
// ordinary planned effect, no warning, and --yes alone is enough.
func TestInstallDotPipInsideTheActiveVirtualenvIsNotDestructive(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	venv := t.TempDir()
	dir := pythonProject(t, h, filepath.Join(venv, "bin"))
	t.Setenv("VIRTUAL_ENV", venv)

	var payload pipPayload
	h.decode(h.mustRun("install", ".", "--yes", "--json"), &payload)
	if len(h.ran) != 1 || h.ran[0] != dir+": pip3 install -r requirements.txt" {
		t.Fatalf("pip3 runs unattended without --allow-destructive: %v", h.ran)
	}
	if payload.Dependencies.Command.Virtualenv != venv {
		t.Errorf("the virtualenv is reported: %+v", payload.Dependencies.Command)
	}
	if hasCode(payload.Warnings, model.WarnPipOutsideVirtualenv) {
		t.Errorf("no warning inside a virtualenv: %+v", payload.Warnings)
	}
	e := findEffect(payload.Dependencies.Effects, model.EffectModify, model.EffectDirectory, venv)
	if e == nil || e.Destructive || e.Evidence != model.EvidencePlanned {
		t.Errorf("the virtualenv is a planned, non-destructive effect: %+v", payload.Dependencies.Effects)
	}
	for _, eff := range payload.Dependencies.Effects {
		if eff.Destructive {
			t.Errorf("nothing destructive inside a virtualenv: %+v", eff)
		}
	}
}

// A project with its own .venv gets its dependencies there, through that
// environment's pip, even when no virtualenv is activated.
func TestInstallDotUsesTheProjectsVenv(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	dir := pythonProject(t, h, "")
	pip := filepath.Join(dir, ".venv", "bin", "pip3")
	writeExecutable(t, pip)

	var payload pipPayload
	h.decode(h.mustRun("install", ".", "--yes", "--json"), &payload)
	if len(h.ran) != 1 || h.ran[0] != dir+": "+pip+" install -r requirements.txt" {
		t.Fatalf("the project's own pip runs: %v", h.ran)
	}
	if payload.Dependencies.Command.Virtualenv != filepath.Join(dir, ".venv") {
		t.Errorf("the project's .venv is reported: %+v", payload.Dependencies.Command)
	}
	if hasCode(payload.Warnings, model.WarnPipOutsideVirtualenv) {
		t.Errorf("no warning inside the project's .venv: %+v", payload.Warnings)
	}
}
