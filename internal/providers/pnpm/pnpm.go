// Package pnpm implements the pnpm provider for globally installed Node
// packages.
//
// pnpm is not npm with a different name. It has its own store, its own global
// directory, and — the detail that matters most here — its own bin directory,
// which on a default macOS install is not on PATH at all. A provider that
// assumed npm's layout would attribute pnpm's tools to npm or to nothing.
//
// Starpoint asks pnpm where things are rather than guessing: `pnpm ls -g --json`
// reports the global directory it is using, and that directory is what the rest of
// the provider is anchored to.
package pnpm

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "pnpm"

// Provider implements provider.Provider for pnpm globals.
type Provider struct {
	runner exec.Runner

	// binary is the executable to invoke, resolved once: pnpm installations that
	// shadow npm commonly provide `pnpm` but not `pnpx`.
	binary string
	// globalDir is the directory pnpm reports it installs globals into.
	globalDir string
	dirKnown  bool
}

var _ provider.Provider = (*Provider)(nil)

// New builds a pnpm provider.
func New(runner exec.Runner) *Provider { return &Provider{runner: runner} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "pnpm (global packages)" }
func (p *Provider) Kind() provider.Kind { return provider.KindNode }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
		PinVersion:   true, // name@version, as npm
		DefaultScope: model.ScopeUser,
	}
}

func (p *Provider) Health(ctx context.Context) provider.Health {
	h := provider.Health{
		Name:         p.Name(),
		Description:  p.Description(),
		Kind:         p.Kind(),
		Capabilities: p.Capabilities(),
	}
	path, err := p.runner.LookPath("pnpm")
	if err != nil {
		h.Reason = "pnpm not found on PATH"
		return h
	}
	p.binary = "pnpm"
	h.Available = true
	h.Binary = path
	res, err := p.runner.Run(ctx, p.exec(), "--version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	h.Version = strings.TrimSpace(shell.FirstLine(res.Stdout))

	// pnpm puts its global bin directory outside PATH on a default install. That is
	// worth saying plainly, because it is the usual reason a pnpm-installed command
	// appears to be missing even though pnpm is right here.
	if bin, err := p.runner.Run(ctx, p.exec(), "bin", "-g"); err != nil || bin.Failed() || notInPath(bin) {
		h.Reason = "installed, but pnpm's global bin directory is not on PATH, so its commands may not resolve"
	}
	return h
}

// exec returns the pnpm executable to invoke.
func (p *Provider) exec() string {
	if p.binary == "" {
		return "pnpm"
	}
	return p.binary
}

// notInPath reports whether pnpm's output signals a bin directory it could not
// put on PATH.
func notInPath(res exec.Result) bool {
	return strings.Contains(strings.ToLower(res.Field()), "not_in_path")
}

// lsResponse is the subset of `pnpm ls -g --json` Starpoint reads. pnpm mirrors
// npm's shape: a list holding the global project and its dependencies.
type lsResponse []struct {
	Path         string                     `json:"path"`
	Dependencies map[string]json.RawMessage `json:"dependencies"`
}

// depEntry is a single global dependency.
type depEntry struct {
	Version string `json:"version"`
}

// present reports whether pnpm is on PATH. A provider that is not installed owns
// nothing, and saying so without running anything is both cheaper and more
// accurate than letting a command fail.
func (p *Provider) present() bool {
	if p.binary != "" {
		return true
	}
	for _, candidate := range []string{"pnpm"} {
		if _, err := p.runner.LookPath(candidate); err == nil {
			p.binary = candidate
			return true
		}
	}
	return false
}

// Discover lists globally installed packages.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	if !p.present() {
		return nil, nil
	}
	res, err := p.runner.Run(ctx, p.exec(), "ls", "-g", "--depth", "0", "--json")
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not list global pnpm packages", err)
	}
	if res.Failed() {
		// "No global packages found" is an empty inventory, not a failure, and
		// pnpm prints it as an error rather than an empty list.
		if strings.Contains(strings.ToLower(res.Field()), "no global packages") {
			return nil, nil
		}
		return nil, shell.Failure(p.Name(), "list", shell.Diagnostic(res.Field()), nil)
	}
	var parsed lsResponse
	if err := json.Unmarshal([]byte(p.stripNoise(res.Stdout)), &parsed); err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not parse pnpm output: "+err.Error(), nil)
	}
	out := []model.Installation{}
	for _, project := range parsed {
		p.recordGlobalDir(project.Path)
		for name, raw := range project.Dependencies {
			var dep depEntry
			// A dependency with no version is a link or a workspace entry, which
			// pnpm installs globals with and Starpoint cannot version.
			_ = json.Unmarshal(raw, &dep)
			out = append(out, model.Installation{
				Name:     name,
				Version:  strings.TrimPrefix(dep.Version, "^"),
				Provider: p.Name(),
				Scope:    model.ScopeUser,
				Path:     p.binPath(name),
			})
		}
	}
	return out, nil
}

// stripNoise removes pnpm's progress chatter, which surrounds the JSON document.
func (p *Provider) stripNoise(out string) string {
	start := strings.Index(out, "[")
	end := strings.LastIndex(out, "]")
	if start < 0 || end <= start {
		return out
	}
	return out[start : end+1]
}

// recordGlobalDir remembers the directory pnpm reported.
func (p *Provider) recordGlobalDir(dir string) {
	if dir != "" && !p.dirKnown {
		p.globalDir = dir
		p.dirKnown = true
	}
}

// Installed resolves one global package.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	all, err := p.Discover(ctx)
	if err != nil {
		return nil, err
	}
	for _, inst := range all {
		if inst.Name == pkg.Name {
			found := inst
			return &found, nil
		}
	}
	return nil, nil
}

// Install installs a global package, honouring a version pin or dist-tag.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	target := req.Package.Name
	switch {
	case req.Version() != "":
		target += "@" + strings.TrimPrefix(strings.TrimPrefix(req.Version(), "=="), "=")
	case req.Package.Channel != "":
		target += "@" + req.Package.Channel
	}
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeUser}, nil
	}
	p.invalidate()
	res, err := p.runner.Run(ctx, p.exec(), "add", "-g", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run pnpm add -g", err)
	}
	if res.Failed() {
		return nil, p.classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a global package.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	p.invalidate()
	res, err := p.runner.Run(ctx, p.exec(), "remove", "-g", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run pnpm remove -g", err)
	}
	if res.Failed() {
		return p.classify("remove", res)
	}
	p.invalidate()
	return nil
}

// Update upgrades a global package.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	target := pkg.Name
	if pkg.Version != "" {
		target += "@" + strings.TrimPrefix(strings.TrimPrefix(pkg.Version, "=="), "=")
	}
	p.invalidate()
	res, err := p.runner.Run(ctx, p.exec(), "update", "-g", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run pnpm update -g", err)
	}
	if res.Failed() {
		return nil, p.classify("upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

// invalidate drops the cached global directory so the next read re-asks.
func (p *Provider) invalidate() {
	p.dirKnown = false
	p.globalDir = ""
}

// GlobalDir returns the directory pnpm installs globals into.
func (p *Provider) GlobalDir() string { return p.globalDir }

// binPath returns where pnpm would expose a package's executables, which is the
// global directory's sibling bin rather than anything inside the global dir.
func (p *Provider) binPath(name string) string {
	if p.globalDir == "" || name == "" {
		return ""
	}
	bin := filepath.Join(filepath.Dir(p.globalDir), "bin", name)
	if _, err := p.runner.LookPath(bin); err == nil {
		return bin
	}
	return ""
}

// classify turns pnpm's output into typed errors.
func (p *Provider) classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "err_pnpm_package_not_found"),
		strings.Contains(text, "not found in the global directory"),
		strings.Contains(text, "no projects matched the filters"),
		strings.Contains(text, "cannot find package"):
		return shell.NotFound(Name, op, "pnpm does not have that global package")
	case strings.Contains(text, "already exists"), strings.Contains(text, "err_pnpm_cannot_overwrite"):
		return shell.Err(Name, op, provider.ErrConflict, "pnpm will not overwrite the existing install", nil)
	case strings.Contains(text, "eacces"), strings.Contains(text, "permission denied"):
		return shell.Err(Name, op, provider.ErrPermission, "pnpm's global directory is not writable", nil)
	case strings.Contains(text, "enotfound"), strings.Contains(text, "eai_again"),
		strings.Contains(text, "network"), strings.Contains(text, "registry.npmjs.org"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach the npm registry", nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "pnpm exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// Verify checks that an installed pnpm package is functional.
func (p *Provider) Verify(ctx context.Context, pkg model.Package) error {
	inst, err := p.Installed(ctx, pkg)
	if err != nil || inst == nil {
		return &provider.Error{
			Provider: p.Name(),
			Kind:     provider.ErrNotFound,
			Message:  "package not installed",
		}
	}
	binPath := p.binPath(pkg.Name)
	if binPath == "" {
		return &provider.Error{
			Provider: p.Name(),
			Kind:     provider.ErrFailed,
			Message:  "cannot locate package executable",
		}
	}
	_, err = p.runner.Run(ctx, binPath, "--version")
	if err != nil {
		return &provider.Error{
			Provider: p.Name(),
			Kind:     provider.ErrFailed,
			Message:  "package verification failed",
			Err:      err,
		}
	}
	return nil
}
