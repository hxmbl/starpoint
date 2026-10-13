package project

import (
	"errors"
	"path/filepath"
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
