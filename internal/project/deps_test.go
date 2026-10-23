package project

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/testsupport"
)

func depsFor(t *testing.T, files map[string]string, lookPath func(string) (string, error)) (*DependencyInstall, string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		testsupport.WriteFile(t, filepath.Join(dir, name), body)
	}
	info := Detect(dir)
	return info.Dependencies(lookPath)
}

func TestDependencyCommandFollowsTheProjectsFiles(t *testing.T) {
	noPip3 := func(string) (string, error) { return "", errors.New("missing") }
	hasAll := func(n string) (string, error) { return "/usr/bin/" + n, nil }
	cases := []struct {
		name  string
		files map[string]string
		look  func(string) (string, error)
		want  string
	}{
		{"npm lockfile", map[string]string{"package.json": "{}", "package-lock.json": "{}"}, nil, "npm ci"},
		{"npm without lockfile", map[string]string{"package.json": "{}"}, nil, "npm install"},
		{"pnpm lockfile", map[string]string{"package.json": "{}", "pnpm-lock.yaml": ""}, nil, "pnpm install"},
		{"yarn lockfile", map[string]string{"package.json": "{}", "yarn.lock": ""}, nil, "yarn install"},
		{"packageManager wins", map[string]string{"package.json": `{"packageManager":"pnpm@9.1.0"}`, "package-lock.json": "{}"}, nil, "pnpm install"},
		{"uv lockfile", map[string]string{"pyproject.toml": "[project]\nname='x'\n", "uv.lock": ""}, nil, "uv sync"},
		{"uv in pyproject", map[string]string{"pyproject.toml": "[project]\nname='x'\n[tool.uv]\ndev-dependencies=[]\n"}, nil, "uv sync"},
		{"requirements with pip3", map[string]string{"requirements.txt": "requests\n"}, hasAll, "pip3 install -r requirements.txt"},
		{"requirements with pip", map[string]string{"requirements.txt": "requests\n"}, noPip3, "pip install -r requirements.txt"},
		{"rust", map[string]string{"Cargo.toml": "[package]\nname='x'\n"}, nil, "cargo fetch"},
		{"go", map[string]string{"go.mod": "module x\n\ngo 1.22\n"}, nil, "go mod download"},
		{"ruby", map[string]string{"Gemfile": "source 'https://rubygems.org'\n"}, nil, "bundle install"},
	}
	for _, c := range cases {
		got, why := depsFor(t, c.files, c.look)
		if got == nil {
			t.Errorf("%s: expected %q, got nothing (%s)", c.name, c.want, why)
			continue
		}
		if got.String() != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got.String(), c.want)
		}
		if got.Dir == "" || got.Reason == "" {
			t.Errorf("%s: the directory and the reason must be reported: %+v", c.name, got)
		}
	}
}

func TestDependencyCommandIsAbsentWhenNothingSaysHow(t *testing.T) {
	// A pyproject.toml that does not use uv, with no requirements file, does
	// not say how its dependencies are installed; guessing would create a
	// lockfile or virtualenv the project never asked for.
	got, why := depsFor(t, map[string]string{"pyproject.toml": "[project]\nname='x'\n"}, nil)
	if got != nil {
		t.Errorf("expected nothing, got %q", got.String())
	}
	if !strings.Contains(why, "nothing says how") {
		t.Errorf("the reason should explain, got %q", why)
	}
	if got, _ := (Info{Kind: KindUnknown}).Dependencies(nil); got != nil {
		t.Error("no project, no command")
	}
}

// npm ci and uv sync are exact: they delete or prune what is already there.
// Additive installers replace nothing, and neither does an exact one in a
// project that has nothing to replace yet.
func TestReplacesNamesWhatExactInstallersDelete(t *testing.T) {
	dir := t.TempDir()
	ci := DependencyInstall{Tool: "npm", Args: []string{"ci"}, Dir: dir}
	sync := DependencyInstall{Tool: "uv", Args: []string{"sync"}, Dir: dir}
	fetch := DependencyInstall{Tool: "cargo", Args: []string{"fetch"}, Dir: dir}
	for _, d := range []DependencyInstall{ci, sync, fetch} {
		if got := d.Replaces(); len(got) != 0 {
			t.Errorf("%s in an empty project replaces nothing, got %+v", d, got)
		}
	}
	for _, name := range []string{"node_modules", ".venv"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got := ci.Replaces(); len(got) != 1 || got[0].Path != filepath.Join(dir, "node_modules") {
		t.Errorf("npm ci replaces node_modules, got %+v", got)
	}
	if got := sync.Replaces(); len(got) != 1 || got[0].Path != filepath.Join(dir, ".venv") {
		t.Errorf("uv sync prunes .venv, got %+v", got)
	}
	if got := fetch.Replaces(); len(got) != 0 {
		t.Errorf("cargo fetch only adds, got %+v", got)
	}
	install := DependencyInstall{Tool: "npm", Args: []string{"install"}, Dir: dir}
	if got := install.Replaces(); len(got) != 0 {
		t.Errorf("npm install keeps node_modules, got %+v", got)
	}
}

// Where a requirements.txt install goes decides whether it is safe: inside a
// virtualenv it only touches that environment, outside one it changes the
// site-packages everything else shares.
func TestPipInstallKnowsItsVirtualenv(t *testing.T) {
	venv := t.TempDir()
	inVenv := func(n string) (string, error) { return filepath.Join(venv, "bin", n), nil }
	system := func(n string) (string, error) { return "/usr/bin/" + n, nil }
	req := map[string]string{"requirements.txt": "requests\n"}

	t.Setenv("VIRTUAL_ENV", "")
	got, _ := depsFor(t, req, system)
	if got.Virtualenv != "" || !got.OutsideVirtualenv() {
		t.Errorf("no virtualenv active: %+v", got)
	}

	t.Setenv("VIRTUAL_ENV", venv)
	got, _ = depsFor(t, req, inVenv)
	if got.Virtualenv != venv || got.OutsideVirtualenv() || got.String() != "pip3 install -r requirements.txt" {
		t.Errorf("the active virtualenv's pip installs into it: %+v", got)
	}

	// VIRTUAL_ENV set, but the pip on PATH is not that environment's (sudo
	// or a PATH reset): pip installs outside it.
	got, _ = depsFor(t, req, system)
	if got.Virtualenv != "" || !got.OutsideVirtualenv() {
		t.Errorf("a pip outside VIRTUAL_ENV is outside the virtualenv: %+v", got)
	}

	// A project .venv with its own pip is used when nothing else is active.
	t.Setenv("VIRTUAL_ENV", "")
	dir := t.TempDir()
	testsupport.WriteFile(t, filepath.Join(dir, "requirements.txt"), "requests\n")
	pip := filepath.Join(dir, ".venv", "bin", "pip")
	if runtime.GOOS == "windows" {
		pip = filepath.Join(dir, ".venv", "Scripts", "pip.exe")
	}
	testsupport.WriteFile(t, pip, "")
	got, _ = Detect(dir).Dependencies(system)
	if got.Tool != pip || got.Virtualenv != filepath.Join(dir, ".venv") || got.OutsideVirtualenv() {
		t.Errorf("the project's .venv pip is used: %+v", got)
	}

	// Other installers are never pip installs.
	got, _ = depsFor(t, map[string]string{"go.mod": "module x\n\ngo 1.22\n"}, nil)
	if got.Pip() || got.OutsideVirtualenv() {
		t.Errorf("go mod download is not a pip install: %+v", got)
	}
}
