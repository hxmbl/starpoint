// Package project gives Starpoint awareness of where it is being run.
//
// The rule kept deliberately narrow: Starpoint does not invent a universal
// dependency system. It detects what kind of project it is standing in, finds the
// tooling that project's ecosystem expects, and offers to make that tooling
// match the configuration. Project-local configuration remains the source of
// truth for what a project actually wants.
package project

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hxmbl/starpoint/internal/model"
)

// Kind classifies a project by the ecosystem it belongs to.
type Kind string

const (
	KindUnknown Kind = "unknown"
	KindGo      Kind = "go"
	KindRust    Kind = "rust"
	KindNode    Kind = "node"
	KindPython  Kind = "python"
	KindRuby    Kind = "ruby"
)

// Info describes the project at a directory.
type Info struct {
	// Root is the directory containing the marker file.
	Root string `json:"root"`
	// Dir is the directory Starpoint was run from.
	Dir string `json:"dir"`
	// Kind is the detected ecosystem.
	Kind Kind `json:"kind"`
	// Marker is the file that identified the project.
	Marker string `json:"marker,omitempty"`
	// Tools are executables this ecosystem expects to be available.
	Tools []string `json:"tools,omitempty"`
	// Toolchain is the subset of Tools that is the language runtime itself.
	Toolchain []string `json:"toolchain,omitempty"`
	// ToolchainVersion is the toolchain version declared in project files.
	// For Rust, this comes from rust-toolchain.toml. For Go, from go.mod.
	ToolchainVersion string `json:"toolchain_version,omitempty"`
	// HasProjectConfig reports whether a project-local Starpoint config exists.
	HasProjectConfig bool `json:"has_project_config"`
}

// Tool is something a project's ecosystem expects to be present.
type Tool struct {
	// Name is the executable.
	Name string
	// Bootstrap marks the language runtime or toolchain itself — rustc, go,
	// node. Starpoint records these when they exist and reports them when they
	// do not, but never installs one implicitly: swapping a toolchain is a
	// decision with consequences far beyond a project directory, and no amount of
	// inference from a marker file justifies making it for someone.
	Bootstrap bool
}

// markers maps an ecosystem to the files that identify it and the tools it
// normally needs. The lists are deliberately minimal: these are the executables a
// developer cannot work without, not every optional tool in the ecosystem.
var markers = []struct {
	kind  Kind
	files []string
	tools []Tool
}{
	{KindGo, []string{"go.mod"}, []Tool{{Name: "go", Bootstrap: true}}},
	{KindRust, []string{"Cargo.toml", "rust-toolchain.toml", "rust-toolchain"}, []Tool{
		{Name: "cargo", Bootstrap: true},
		{Name: "rustc", Bootstrap: true},
	}},
	{KindNode, []string{"package.json"}, []Tool{
		{Name: "node", Bootstrap: true},
		{Name: "npm", Bootstrap: true},
	}},
	{KindPython, []string{"pyproject.toml", "requirements.txt", "Pipfile"}, []Tool{
		{Name: "python3", Bootstrap: true},
	}},
	{KindRuby, []string{"Gemfile"}, []Tool{
		{Name: "ruby", Bootstrap: true},
		{Name: "gem", Bootstrap: true},
	}},
}

// Detect inspects dir and returns project information. It walks up to the
// filesystem root looking for a marker, because Starpoint is usually run from a
// subdirectory of the project it cares about.
func Detect(dir string) Info {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	info := Info{Dir: abs, Kind: KindUnknown}
	for current := abs; ; {
		for _, m := range markers {
			for _, f := range m.files {
				if _, err := os.Stat(filepath.Join(current, f)); err == nil {
					info.Root = current
					info.Kind = m.kind
					info.Marker = f
					info.Tools = toolNames(m.tools)
					break
				}
			}
			if info.Kind != KindUnknown {
				break
			}
		}
		if info.Kind != KindUnknown {
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	info.Toolchain = toolNames(allBootstrap(info))
	info.ToolchainVersion = declaredToolchain(info.Root, info.Kind)
	info.HasProjectConfig = configExists(info.Root)
	return info
}

// declaredToolchain reads explicit project intent only. It never invokes
// rustup/go and never changes or installs a toolchain.
func declaredToolchain(root string, kind Kind) string {
	if root == "" {
		return ""
	}
	filename := ""
	if kind == KindRust {
		filename = "rust-toolchain.toml"
	}
	if kind == KindGo {
		filename = "go.mod"
	}
	if filename == "" {
		return ""
	}
	if kind == KindRust {
		if _, err := os.Stat(filepath.Join(root, filename)); err != nil {
			legacy, legacyErr := os.ReadFile(filepath.Join(root, "rust-toolchain"))
			if legacyErr == nil {
				return strings.TrimSpace(strings.SplitN(string(legacy), "#", 2)[0])
			}
			return ""
		}
	}
	f, err := os.Open(filepath.Join(root, filename))
	if err != nil {
		return ""
	}
	defer f.Close()
	if kind == KindRust {
		var inToolchain bool
		scanner := bufio.NewScanner(f)
		var channel string
		for scanner.Scan() {
			line := strings.TrimSpace(strings.SplitN(scanner.Text(), "#", 2)[0])
			if strings.HasPrefix(line, "[") {
				inToolchain = line == "[toolchain]"
				continue
			}
			if !inToolchain {
				continue
			}
			key, value, ok := strings.Cut(line, "=")
			if ok && strings.TrimSpace(key) == "channel" {
				channel = unquote(strings.TrimSpace(value))
			}
		}
		if channel != "" {
			return channel
		}
		return ""
	}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(strings.SplitN(scanner.Text(), "//", 2)[0])
		if len(fields) == 2 && fields[0] == "go" {
			return fields[1]
		}
	}
	return ""
}

func unquote(value string) string {
	if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
		return value[1 : len(value)-1]
	}
	return value
}

func allBootstrap(info Info) []Tool {
	for _, m := range markers {
		if m.kind == info.Kind {
			return m.tools
		}
	}
	return nil
}

func toolNames(tools []Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

// ExpectedTools returns the tools for this ecosystem, including whether each is
// the toolchain itself.
func (i Info) ExpectedTools() []Tool {
	return allBootstrap(i)
}

func configExists(root string) bool {
	if root == "" {
		return false
	}
	_, err := os.Stat(filepath.Join(root, ".starpoint.toml"))
	return err == nil
}

// Requirements returns the desired packages implied by the project's ecosystem.
//
// Only non-bootstrap tools become requirements. Everything returned here is a
// proposal for the project-local configuration to confirm, never an action
// Starpoint takes on its own.
// Requirements returns the packages this project implies but has not declared.
func (i Info) Requirements() []model.Package {
	return requirementsFor(i.Root, i.ExpectedTools()...)
}

// requirementsFor turns non-bootstrap tools into project-scoped proposals
// pointing at the project's own configuration file.
func requirementsFor(root string, tools ...Tool) []model.Package {
	var out []model.Package
	for _, t := range tools {
		if t.Bootstrap {
			continue
		}
		out = append(out, model.Package{
			Name:   t.Name,
			Scope:  model.ScopeProject,
			Origin: model.Origin{Path: filepath.Join(root, ".starpoint.toml"), Project: true},
		})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// String renders the project for display.
func (i Info) String() string {
	if i.Kind == KindUnknown {
		return "no project detected (searched up from " + i.Dir + ")"
	}
	return string(i.Kind) + " project at " + i.Root + " (via " + i.Marker + ")"
}
