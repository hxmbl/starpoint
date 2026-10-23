package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// DependencyInstall is the ecosystem's own command for installing a project's
// dependencies, run in the project root.
//
// Starpoint does not resolve project dependencies itself. Each ecosystem already
// has a tool that reads its manifest and lockfile and does exactly that; the
// only decisions here are which tool, chosen from the files the project
// actually has, and where to run it.
type DependencyInstall struct {
	// Tool is the executable to run.
	Tool string `json:"tool"`
	// Args are its arguments.
	Args []string `json:"args"`
	// Dir is the project root the command runs in.
	Dir string `json:"dir"`
	// Reason names the file that chose the command.
	Reason string `json:"reason"`
}

// String renders the command line.
func (d DependencyInstall) String() string {
	return strings.TrimSpace(d.Tool + " " + strings.Join(d.Args, " "))
}

// Dependencies returns the command that installs this project's dependencies.
// It returns nil and an explanation when the project declares nothing a tool
// could install.
//
// lookPath is consulted only where the choice depends on which tools exist
// (pip3 versus pip); a missing tool is otherwise reported when the command runs.
func (i Info) Dependencies(lookPath func(string) (string, error)) (*DependencyInstall, string) {
	if i.Root == "" || i.Kind == KindUnknown {
		return nil, "no project detected"
	}
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(i.Root, name))
		return err == nil
	}
	cmd := func(reason, tool string, args ...string) (*DependencyInstall, string) {
		return &DependencyInstall{Tool: tool, Args: args, Dir: i.Root, Reason: reason}, ""
	}

	switch i.Kind {
	case KindNode:
		if !has("package.json") {
			return nil, "no package.json"
		}
		switch manager := packageManagerField(filepath.Join(i.Root, "package.json")); {
		case strings.HasPrefix(manager, "pnpm"):
			return cmd("package.json packageManager is "+manager, "pnpm", "install")
		case strings.HasPrefix(manager, "yarn"):
			return cmd("package.json packageManager is "+manager, "yarn", "install")
		case strings.HasPrefix(manager, "npm"):
			if has("package-lock.json") || has("npm-shrinkwrap.json") {
				return cmd("package.json packageManager is "+manager+" and a lockfile exists", "npm", "ci")
			}
			return cmd("package.json packageManager is "+manager, "npm", "install")
		}
		switch {
		case has("pnpm-lock.yaml"):
			return cmd("pnpm-lock.yaml", "pnpm", "install")
		case has("yarn.lock"):
			return cmd("yarn.lock", "yarn", "install")
		case has("package-lock.json"):
			return cmd("package-lock.json", "npm", "ci")
		case has("npm-shrinkwrap.json"):
			return cmd("npm-shrinkwrap.json", "npm", "ci")
		default:
			return cmd("package.json without a lockfile", "npm", "install")
		}
	case KindPython:
		switch {
		case has("uv.lock"):
			return cmd("uv.lock", "uv", "sync")
		case has("pyproject.toml") && fileContains(filepath.Join(i.Root, "pyproject.toml"), "[tool.uv"):
			return cmd("pyproject.toml configures uv", "uv", "sync")
		case has("requirements.txt"):
			pip := "pip"
			if lookPath != nil {
				if _, err := lookPath("pip3"); err == nil {
					pip = "pip3"
				}
			}
			return cmd("requirements.txt", pip, "install", "-r", "requirements.txt")
		default:
			return nil, "no uv.lock, uv-managed pyproject.toml or requirements.txt; nothing says how to install dependencies"
		}
	case KindRust:
		if !has("Cargo.toml") {
			return nil, "no Cargo.toml"
		}
		return cmd("Cargo.toml", "cargo", "fetch")
	case KindGo:
		return cmd("go.mod", "go", "mod", "download")
	case KindRuby:
		return cmd("Gemfile", "bundle", "install")
	}
	return nil, "no dependency installer for " + string(i.Kind) + " projects"
}

// packageManagerField reads package.json's "packageManager" (Corepack) field.
func packageManagerField(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var manifest struct {
		PackageManager string `json:"packageManager"`
	}
	if json.Unmarshal(data, &manifest) != nil {
		return ""
	}
	return strings.TrimSpace(manifest.PackageManager)
}

func fileContains(path, needle string) bool {
	data, err := os.ReadFile(path)
	return err == nil && strings.Contains(string(data), needle)
}

// Replaced is something in the project that a dependency install deletes or
// rewrites wholesale, which makes running the install a destructive change.
type Replaced struct {
	// Path is what is replaced.
	Path string `json:"path"`
	// Detail says what the installer does to it.
	Detail string `json:"detail"`
}

// Replaces reports what running d would delete or rewrite wholesale, among
// the things that are there now. Most installers only add (cargo fetch, go
// mod download, bundle install); two are exact by design:
//
//   - `npm ci` deletes node_modules and installs it again from the lockfile.
//   - `uv sync` removes every package the lockfile does not list from the
//     project's .venv.
func (d DependencyInstall) Replaces() []Replaced {
	var out []Replaced
	present := func(name string) (string, bool) {
		p := filepath.Join(d.Dir, name)
		_, err := os.Lstat(p)
		return p, err == nil
	}
	sub := ""
	if len(d.Args) > 0 {
		sub = d.Args[0]
	}
	switch {
	case d.Tool == "npm" && sub == "ci":
		if p, ok := present("node_modules"); ok {
			out = append(out, Replaced{Path: p,
				Detail: "npm ci deletes node_modules and installs it again from the lockfile; anything changed or added there by hand is lost"})
		}
	case d.Tool == "uv" && sub == "sync":
		if p, ok := present(".venv"); ok {
			out = append(out, Replaced{Path: p,
				Detail: "uv sync removes every package the lockfile does not list from .venv"})
		}
	}
	return out
}
