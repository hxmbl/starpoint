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
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/hxmbl/starpoint/internal/model"
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
	source []byte
	// LastUsedInDir tracks the last time this config was used in a particular directory.
	LastUsedInDir string `json:"last_used_in_dir,omitempty"`
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
	// RetentionDays is how many days of records Starpoint keeps: its audit log,
	// the transition history and the run history. Zero or negative means the
	// default, never "keep nothing". It is read from the global file only.
	RetentionDays int `toml:"retention_days,omitempty"`
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
		// RetentionDays is deliberately not merged from project files. Retention
		// is a property of the machine's records, not of a checkout, and a
		// repository-local file should not be able to shorten the window for
		// everything the machine keeps.
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

// EnsureProjectFile returns the project configuration rooted at root, adding a
// new, not-yet-written one to the document when there is none. It is written
// by the next Save.
func (d *Document) EnsureProjectFile(root string) File {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	path := filepath.Join(abs, ProjectFileName)
	for _, f := range d.Projects {
		if f.Path == path {
			return f
		}
	}
	f := File{Path: path, Project: true, Raw: Raw{Schema: SchemaVersion}}
	// Projects are ordered outermost first, so the most specific file wins.
	i := len(d.Projects)
	for i > 0 && len(filepath.Dir(d.Projects[i-1].Path)) > len(abs) {
		i--
	}
	d.Projects = append(d.Projects, File{})
	copy(d.Projects[i+1:], d.Projects[i:])
	d.Projects[i] = f
	return f
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
	if err := writeFileSync(tmp, []byte(sb.String())); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replacing config: %w", err)
	}
	syncDir(filepath.Dir(path))
	return nil
}

// writeFileSync writes data to path and flushes it to disk before returning.
//
// A plain WriteFile only promises the bytes reached the kernel. The config file
// is the source of truth for every later command, so a write that is still in
// the page cache when the machine loses power must not be treated as committed.
func writeFileSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("writing config: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("syncing config: %w", err)
	}
	return f.Close()
}

// syncDir flushes a directory entry so a completed rename survives a crash.
//
// Renaming a file is atomic with respect to readers, but the new directory entry
// itself is only durable once the containing directory has been synced.
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}

// Save writes the document back to disk.
func (d *Document) Save() error {
	files := d.Files()
	for _, f := range files {
		// Unchanged files of either scope need no write. This is important for global
		// config too: a read-only operation must never normalize source formatting.
		if f.Exists && unchangedFile(f) {
			continue
		}
		if err := writeFile(f); err != nil {
			return err
		}
	}
	return nil
}

// writeFile modifies only package tables in an existing file. If the source
// layout cannot be mapped unambiguously, it falls back to the standard encoder.
func writeFile(f File) error {
	if len(f.source) == 0 {
		return Write(f.Path, f.Raw)
	}
	lines := strings.SplitAfter(string(f.source), "\n")
	starts := make([]int, 0, len(f.Raw.Packages))
	for i, line := range lines {
		if strings.TrimSpace(beforeComment(line)) == "[[package]]" {
			starts = append(starts, i)
		}
	}
	wanted := make(map[string]model.Package, len(f.Raw.Packages))
	for _, pkg := range f.Raw.Packages {
		wanted[pkg.Name] = pkg
	}
	var out strings.Builder
	last := 0
	for _, start := range starts {
		end := len(lines)
		for j := start + 1; j < len(lines); j++ {
			trimmed := strings.TrimSpace(beforeComment(lines[j]))
			if strings.HasPrefix(trimmed, "[") {
				end = j
				break
			}
		}
		for _, line := range lines[last:start] {
			out.WriteString(line)
		}
		block := strings.Join(lines[start:end], "")
		var parsed struct {
			Packages []model.Package `toml:"package"`
		}
		if _, err := toml.Decode(block, &parsed); err != nil || len(parsed.Packages) != 1 {
			return fmt.Errorf("updating %s safely: could not map a package table to its config entry", f.Path)
		}
		old := parsed.Packages[0]
		pkg, keep := wanted[old.Name]
		if keep {
			delete(wanted, old.Name)
			if reflect.DeepEqual(old, pkg) {
				out.WriteString(block)
			} else {
				for _, line := range lines[start:end] {
					if comment := trailingComment(line); comment != "" {
						out.WriteString(strings.TrimSpace(comment))
						out.WriteByte('\n')
					}
				}
				row, err := encodePackage(pkg)
				if err != nil {
					return err
				}
				out.WriteString(row)
			}
		} else {
			// Preserve annotations even when their package is removed. They no
			// longer attach to a table, but remain available for the user to edit.
			for _, line := range lines[start:end] {
				if comment := trailingComment(line); comment != "" {
					out.WriteString(strings.TrimSpace(comment))
					out.WriteByte('\n')
				}
			}
		}
		last = end
	}
	for _, line := range lines[last:] {
		out.WriteString(line)
	}
	for _, pkg := range f.Raw.Packages {
		if _, exists := wanted[pkg.Name]; !exists {
			continue
		}
		row, err := encodePackage(pkg)
		if err != nil {
			return err
		}
		if out.Len() > 0 {
			if !strings.HasSuffix(out.String(), "\n") {
				out.WriteByte('\n')
			}
			out.WriteByte('\n')
		}
		out.WriteString(row)
		delete(wanted, pkg.Name)
	}
	if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
		return fmt.Errorf("creating config directory: %w", err)
	}
	tmp := f.Path + ".tmp"
	if err := writeFileSync(tmp, []byte(out.String())); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, f.Path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replacing config: %w", err)
	}
	syncDir(filepath.Dir(f.Path))
	return nil
}

func encodePackage(pkg model.Package) (string, error) {
	var row strings.Builder
	enc := toml.NewEncoder(&row)
	enc.Indent = "  "
	if err := enc.Encode(struct {
		Packages []model.Package `toml:"package"`
	}{Packages: []model.Package{pkg}}); err != nil {
		return "", fmt.Errorf("encoding package: %w", err)
	}
	return row.String(), nil
}

func beforeComment(line string) string {
	var quote rune
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if quote != 0 && r == '\\' {
			escaped = true
			continue
		}
		if (r == '"' || r == '\'') && (quote == 0 || quote == r) {
			if quote == 0 {
				quote = r
			} else {
				quote = 0
			}
			continue
		}
		if r == '#' && quote == 0 {
			return line[:i]
		}
	}
	return line
}

func trailingComment(line string) string {
	content := beforeComment(line)
	if content == line {
		return ""
	}
	return strings.TrimRight(line[len(content):], "\r\n")
}

func unchangedFile(f File) bool {
	if len(f.source) == 0 {
		return false
	}
	original, err := parse(f.source)
	return err == nil && reflect.DeepEqual(original, f.Raw)
}

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
	if len(bytes.TrimSpace(data)) == 0 {
		f.Exists = true
		f.source = append([]byte(nil), data...)
		f.Raw.Schema = SchemaVersion
		return f, nil, nil
	}
	f.Exists = true
	f.source = append([]byte(nil), data...)
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
  retention_days
              how many days of records to keep: the audit log of what Starpoint
              did, the transition history and the run history. Records older
              than this are deleted on every command. Defaults to 30, and is
              read from this file only, so a project cannot change it.

A project may have its own .starpoint.toml. It applies when you run Starpoint
inside that directory and takes precedence over this file for what it declares.
`, filepath.Join("~", GlobalFileName))
}

// StateDirName is the directory, beside the global configuration, where
// Starpoint keeps everything it writes: the transition history, the run
// history, the mutation locks and the audit log.
const StateDirName = ".starpoint"

// StateDir returns the state directory for a global configuration path.
//
// Deriving it from the configuration path is what keeps Starpoint's records out
// of somebody's real home directory when Starpoint is pointed somewhere else.
// That matters for tests, which run against a temporary configuration, and it
// is the same reason the mutation locks already live beside the configuration
// rather than in a hard-coded home.
func StateDir(globalConfigPath string) (string, error) {
	path := globalConfigPath
	if path == "" {
		defaultPath, err := DefaultGlobalPath()
		if err != nil {
			return "", err
		}
		path = defaultPath
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return filepath.Join(filepath.Dir(path), StateDirName), nil
}

// DefaultGlobalPath returns the default global configuration path.
func DefaultGlobalPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, GlobalFileName), nil
}
