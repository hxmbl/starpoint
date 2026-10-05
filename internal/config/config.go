// Package config reads and writes Starpoint's TOML configuration, which is the
// source of truth for desired state.
//
// The configuration is not a disposable generated artifact. Starpoint writes to
// it as part of normal operations: installing adds an entry, removing deletes
// one, and provider resolution persists its decision. A document may span several
// files — one global file plus any project-local files that apply to the current
// directory — and every edit lands in the file that owns the entry.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/starpoint/starpoint/internal/model"
)

// SchemaVersion is the version written into every file Starpoint creates.
const SchemaVersion = 1

// GlobalFileName is the base name of the global configuration file.
const GlobalFileName = ".starpoint.toml"

// ProjectFileName is the base name of a project-local configuration file.
const ProjectFileName = ".starpoint.toml"

// File is one parsed configuration file.
type File struct {
	// Path is the absolute path the file was read from or will be written to.
	Path string `json:"path"`
	// Project marks a project-local file.
	Project bool `json:"project"`
	// Raw is the parsed representation, mutated in place and written back.
	Raw Raw `json:"config"`
	// Exists reports whether the file was present on disk at load time.
	Exists bool `json:"exists"`
}

// Raw is the on-disk TOML shape. It is intentionally close to what users type.
type Raw struct {
	Schema   int             `toml:"schema"`
	Settings Settings        `toml:"settings,omitempty"`
	Packages []model.Package `toml:"package,omitempty"`
}

// Settings holds user preferences. These affect behaviour but never desired
// state.
type Settings struct {
	// Providers restricts automatic provider resolution to these names. Empty
	// means all available providers may be considered.
	Providers []string `toml:"providers,omitempty"`
	// NoConfirm makes destructive operations non-interactive by default.
	NoConfirm bool `toml:"no_confirm,omitempty"`
}

// Default returns the settings Starpoint assumes when nothing is configured.
func Default() Settings { return Settings{} }

// Document is a merged view of the global file and any project-local files that
// apply to the current directory.
type Document struct {
	// Global is the global configuration file.
	Global File
	// Projects are project-local files, ordered outermost first.
	Projects []File
	// Warnings holds non-fatal problems found while loading.
	Warnings []string `json:"warnings,omitempty"`
}

// Packages returns the merged desired packages, global first then project
// entries, with later entries overriding earlier ones of the same name. The
// override direction is deliberate: a project-local file is the more specific
// statement of intent.
func (d *Document) Packages() []model.Package {
	var out []model.Package
	index := map[string]int{}
	add := func(pkgs []model.Package, origin model.Origin) {
		for _, p := range pkgs {
			p.Origin = origin
			if p.Name == "" {
				continue
			}
			if i, ok := index[p.Name]; ok {
				out[i] = p
				continue
			}
			index[p.Name] = len(out)
			out = append(out, p)
		}
	}
	if d.Global.Raw.Schema == 0 && len(d.Global.Raw.Packages) > 0 {
		d.Global.Raw.Schema = SchemaVersion
	}
	add(d.Global.Raw.Packages, model.Origin{Path: d.Global.Path})
	for _, f := range d.Projects {
		add(f.Raw.Packages, model.Origin{Path: f.Path, Project: true})
	}
	return out
}

// Package looks up a merged entry by name.
func (d *Document) Package(name string) (model.Package, bool) {
	for _, p := range d.Packages() {
		if p.Name == name {
			return p, true
		}
	}
	return model.Package{}, false
}

// Settings returns the merged settings, with project files layered over global.
func (d *Document) Settings() Settings {
	s := d.Global.Raw.Settings
	for _, f := range d.Projects {
		if len(f.Raw.Settings.Providers) > 0 {
			s.Providers = f.Raw.Settings.Providers
		}
		if f.Raw.Settings.NoConfirm {
			s.NoConfirm = true
		}
	}
	return s
}

// Files returns every file backing the document, global first.
func (d *Document) Files() []File {
	out := make([]File, 0, 1+len(d.Projects))
	out = append(out, d.Global)
	out = append(out, d.Projects...)
	return out
}

// Find returns the file that currently declares a package.
func (d *Document) Find(name string) (File, bool) {
	for i := len(d.Projects) - 1; i >= 0; i-- {
		for _, p := range d.Projects[i].Raw.Packages {
			if p.Name == name {
				return d.Projects[i], true
			}
		}
	}
	for _, p := range d.Global.Raw.Packages {
		if p.Name == name {
			return d.Global, true
		}
	}
	return File{}, false
}

// ProjectFileFor returns the project file that owns dir, if any. Ownership means
// dir is at or below the project's declared root.
func (d *Document) ProjectFileFor(dir string) (File, bool) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		abs = dir
	}
	best := -1
	var match File
	for i, f := range d.Projects {
		// The project root is the directory holding the config file.
		root := filepath.Dir(f.Path)
		if strings.HasPrefix(root, "~") {
			if home, herr := os.UserHomeDir(); herr == nil {
				root = filepath.Join(home, strings.TrimPrefix(root, "~"))
			}
		}
		root, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if abs == root || strings.HasPrefix(abs, root+string(filepath.Separator)) {
			// The most deeply nested matching project wins.
			if i > best {
				best = i
				match = f
			}
		}
	}
	return match, best >= 0
}

// Write persists a single file, creating parent directories as needed. Writes go
// through a temporary file in the same directory so a crash cannot leave a
// truncated source of truth behind.
func Write(path string, raw Raw) error {
	if raw.Schema == 0 {
		raw.Schema = SchemaVersion
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	var sb strings.Builder
	enc := toml.NewEncoder(&sb)
	enc.Indent = "  "
	if err := enc.Encode(raw); err != nil {
		return fmt.Errorf("encoding config: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replacing config: %w", err)
	}
	return nil
}

// Save writes the document back to disk.
func (d *Document) Save() error {
	files := d.Files()
	for _, f := range files {
		// Untouched project files that already exist on disk need no write.
		if f.Project && f.Exists && unchangedFile(f) {
			continue
		}
		if len(f.Raw.Packages) == 0 && len(f.Raw.Settings.Providers) == 0 && f.Project {
			if err := os.Remove(f.Path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("removing empty project config: %w", err)
			}
			continue
		}
		if err := Write(f.Path, f.Raw); err != nil {
			return err
		}
	}
	return nil
}

// unchangedFile is a placeholder for future dirty tracking. Currently every
// known file is written, which is safe because encoding is deterministic.
func unchangedFile(File) bool { return false }

// LoadOptions controls discovery.
type LoadOptions struct {
	// GlobalPath overrides the default global config path.
	GlobalPath string
	// Dir is the directory project discovery starts from.
	Dir string
	// DiscoverProjects enables walking up from Dir looking for project configs.
	DiscoverProjects bool
}

// Load reads the global configuration and, when asked, any project-local
// configurations that apply to Dir.
func Load(opts LoadOptions) (*Document, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("locating home directory: %w", err)
	}
	globalPath := opts.GlobalPath
	if globalPath == "" {
		globalPath = filepath.Join(home, GlobalFileName)
	}
	doc := &Document{}
	gf, warns, err := LoadFile(globalPath, false)
	if err != nil {
		return nil, err
	}
	doc.Warnings = append(doc.Warnings, warns...)
	doc.Global = gf

	if !opts.DiscoverProjects || opts.Dir == "" {
		return doc, nil
	}
	seen := map[string]bool{globalPath: true}
	dir := opts.Dir
	for {
		candidate := filepath.Join(dir, ProjectFileName)
		if !seen[candidate] {
			seen[candidate] = true
			pf, warns, err := LoadFile(candidate, true)
			if err != nil {
				return nil, err
			}
			doc.Warnings = append(doc.Warnings, warns...)
			if pf.Exists || len(pf.Raw.Packages) > 0 {
				doc.Projects = append(doc.Projects, pf)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return doc, nil
}

// LoadFile reads and validates one configuration file. A missing file is not an
// error; it yields an empty document marked non-existent.
func LoadFile(path string, project bool) (File, []string, error) {
	f := File{Path: path, Project: project}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			f.Raw.Schema = SchemaVersion
			return f, nil, nil
		}
		return f, nil, fmt.Errorf("reading %s: %w", path, err)
	}
	f.Exists = true
	var warns []string
	raw, err := parse(data)
	if err != nil {
		return f, nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	f.Raw = raw
	warns = append(warns, validate(f)...)
	return f, warns, nil
}

// parse decodes TOML and reports unknown keys, which are almost always typos.
func parse(data []byte) (Raw, error) {
	var raw Raw
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return raw, err
	}
	var unknown []string
	for _, k := range md.Undecoded() {
		unknown = append(unknown, k.String())
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return raw, fmt.Errorf("unknown configuration key(s): %s", strings.Join(unknown, ", "))
	}
	return raw, nil
}

// validate returns human-readable problems that do not prevent loading.
func validate(f File) []string {
	var out []string
	seen := map[string]bool{}
	for i, p := range f.Raw.Packages {
		switch {
		case p.Name == "":
			out = append(out, fmt.Sprintf("package #%d in %s has no name", i+1, f.Path))
		case seen[p.Name]:
			out = append(out, fmt.Sprintf("package %q is declared more than once in %s", p.Name, f.Path))
		default:
			seen[p.Name] = true
		}
		if p.Scope != "" && !p.Scope.Valid() {
			out = append(out, fmt.Sprintf("package %q in %s has unknown scope %q", p.Name, f.Path, p.Scope))
		}
	}
	if f.Raw.Schema > SchemaVersion {
		out = append(out, fmt.Sprintf("%s declares schema %d but this build understands up to %d",
			f.Path, f.Raw.Schema, SchemaVersion))
	}
	return out
}

// RenderTemplate is the documentation `starpoint init` prints.
//
// It is printed rather than written into the file on purpose: Starpoint rewrites
// the configuration whenever it records a provider decision, and a TOML round
// trip cannot preserve comments. Documentation that the first operation deletes
// is worse than none.
func RenderTemplate() string {
	return fmt.Sprintf(`Starpoint configuration
========================

  %s

This file is the source of truth for the software Starpoint manages on this
machine. Starpoint writes to it when you install or remove software, and reads
from it when you sync. Edit it freely; Starpoint will converge the machine onto
whatever it says.

Each entry describes WHAT you want, not HOW to get it:

  [[package]]
  name = "ripgrep"        # required
  version = "14.1.0"      # optional hard pin; omitted means "whatever is current"
  channel = "nightly"     # optional: a release channel, a cask name, a dist-tag

Starpoint fills in `+"`provider`"+` and `+"`reason`"+` once it decides who owns the package,
and keeps them up to date. Set `+"`provider`"+` yourself to override that decision. Editing it
by hand is also how you deliberately move software to a different provider,
which Starpoint performs as a transition rather than installing a second copy.

Available keys: name, version, provider, scope, channel, reason
  scope   global | user | project
  channel an opaque provider hint: a Homebrew cask name, an npm dist-tag,
          a Rust release channel (stable, beta, nightly)

[settings]
  providers   restrict automatic provider choice to these providers
  no_confirm  skip confirmation prompts for destructive operations

A project may have its own .starpoint.toml. It applies when you run Starpoint
inside that directory and takes precedence over this file for what it declares.
`, filepath.Join("~", GlobalFileName))
}

// DefaultGlobalPath returns ~/.starpoint.toml.
func DefaultGlobalPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, GlobalFileName), nil
}
