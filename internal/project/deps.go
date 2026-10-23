package project

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
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
	// Virtualenv is the Python virtual environment a pip install goes into:
	// the active one (VIRTUAL_ENV, when the pip that runs is its own) or the
	// project's .venv. Empty for a pip install outside any virtualenv, which
	// writes to the user's (or, as root, the system's) site-packages, and for
	// every other installer.
	Virtualenv string `json:"virtualenv,omitempty"`
}

// Pip reports whether the command is a pip install.
func (d DependencyInstall) Pip() bool {
	base := strings.TrimSuffix(filepath.Base(d.Tool), ".exe")
	return base == "pip" || base == "pip3"
}

// OutsideVirtualenv reports whether the command is a pip install that runs
// outside any virtualenv. Such an install changes the Python environment
// everything else on the account (or, as root, the machine) shares: it can
// upgrade or downgrade packages other software depends on.
func (d DependencyInstall) OutsideVirtualenv() bool {
	return d.Pip() && d.Virtualenv == ""
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
			return i.pipInstall(lookPath)
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

// pipInstall chooses the pip that installs requirements.txt, and the
// virtualenv it installs into:
//
//  1. An active virtualenv (VIRTUAL_ENV) whose own pip is the one on PATH:
//     the user has said where Python packages go.
//  2. Otherwise the project's own .venv, through that environment's pip, so
//     a project that has a virtualenv gets its dependencies there.
//  3. Otherwise pip3 (or pip) from PATH, outside any virtualenv.
//
// lookPath may be nil, in which case PATH is not consulted and pip3 is not
// preferred.
func (i Info) pipInstall(lookPath func(string) (string, error)) (*DependencyInstall, string) {
	args := []string{"install", "-r", "requirements.txt"}
	pip, resolved := "pip", ""
	if lookPath != nil {
		if p, err := lookPath("pip3"); err == nil {
			pip, resolved = "pip3", p
		} else if p, err := lookPath("pip"); err == nil {
			resolved = p
		}
	}
	if venv := os.Getenv("VIRTUAL_ENV"); venv != "" && resolved != "" && within(resolved, venv) {
		return &DependencyInstall{Tool: pip, Args: args, Dir: i.Root, Virtualenv: venv,
			Reason: "requirements.txt, in the active virtualenv " + venv}, ""
	}
	venv := filepath.Join(i.Root, ".venv")
	for _, rel := range venvPips() {
		if p := filepath.Join(venv, rel); isFile(p) {
			return &DependencyInstall{Tool: p, Args: args, Dir: i.Root, Virtualenv: venv,
				Reason: "requirements.txt, in the project's .venv"}, ""
		}
	}
	return &DependencyInstall{Tool: pip, Args: args, Dir: i.Root, Reason: "requirements.txt"}, ""
}

// venvPips are where a virtualenv keeps its pip, relative to its root.
func venvPips() []string {
	if runtime.GOOS == "windows" {
		return []string{filepath.Join("Scripts", "pip3.exe"), filepath.Join("Scripts", "pip.exe")}
	}
	return []string{filepath.Join("bin", "pip3"), filepath.Join("bin", "pip")}
}

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// within reports whether path is dir or below it, as written or with
// symlinks resolved.
func within(path, dir string) bool {
	in := func(p, d string) bool {
		rel, err := filepath.Rel(d, p)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
	}
	if in(filepath.Clean(path), filepath.Clean(dir)) {
		return true
	}
	rp, err1 := filepath.EvalSymlinks(path)
	rd, err2 := filepath.EvalSymlinks(dir)
	return err1 == nil && err2 == nil && in(rp, rd)
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
