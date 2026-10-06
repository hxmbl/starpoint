// Package gem implements the RubyGems provider.
//
// RubyGems has two installation directories — one system-wide, one per user —
// and a configuration entry can end up in either. `gem list --local` does not say
// which, so the provider reads `gem environment` for both directories and reports
// the placement from the default that applies, rather than guessing per gem.
//
// Bundled gems shipped with the interpreter are marked `(default: x.y.z)` and
// cannot be uninstalled. They are reported, because pretending the operating
// system's Ruby has no gems would be as wrong as claiming ownership of them.
package gem

import (
	"context"
	"os"
	"strings"

	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "gem"

// Provider implements provider.Provider for RubyGems.
type Provider struct {
	runner exec.Runner

	// installDir and userDir are the system-wide and per-user gem homes.
	installDir string
	userDir    string
	dirsKnown  bool
}

var _ provider.Provider = (*Provider)(nil)

// New builds a RubyGems provider.
func New(runner exec.Runner) *Provider { return &Provider{runner: runner} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "RubyGems" }
func (p *Provider) Kind() provider.Kind { return provider.KindRuby }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
		PinVersion:   true, // name -v x.y.z
		DefaultScope: model.ScopeGlobal,
	}
}

func (p *Provider) Health(ctx context.Context) provider.Health {
	h := provider.Health{
		Name:         p.Name(),
		Description:  p.Description(),
		Kind:         p.Kind(),
		Capabilities: p.Capabilities(),
	}
	path, err := p.runner.LookPath("gem")
	if err != nil {
		h.Reason = "gem not found on PATH"
		return h
	}
	h.Available = true
	h.Binary = path
	res, err := p.runner.Run(ctx, "gem", "--version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	h.Version = strings.TrimSpace(shell.FirstLine(res.Stdout))
	return h
}

// environment is the subset of `gem environment` Starpoint reads.
type environment struct {
	SystemDir string
	UserDir   string
}

// readEnvironment parses `gem environment` for the two installation homes.
func (p *Provider) readEnvironment(ctx context.Context) environment {
	if p.dirsKnown {
		return environment{SystemDir: p.installDir, UserDir: p.userDir}
	}
	p.dirsKnown = true
	res, err := p.runner.Run(ctx, "gem", "environment")
	if err != nil || res.Failed() {
		return environment{}
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "- INSTALLATION DIRECTORY:"):
			p.installDir = strings.TrimSpace(strings.TrimPrefix(trimmed, "- INSTALLATION DIRECTORY:"))
		case strings.HasPrefix(trimmed, "- USER INSTALLATION DIRECTORY:"):
			p.userDir = strings.TrimSpace(strings.TrimPrefix(trimmed, "- USER INSTALLATION DIRECTORY:"))
		}
	}
	return environment{SystemDir: p.installDir, UserDir: p.userDir}
}

// Discover parses `gem list --local`.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	res, err := p.runner.Run(ctx, "gem", "list", "--local")
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not list installed gems", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "list", shell.FirstLine(res.Field()), nil)
	}
	// `gem list` does not say which home a gem is in, so the placement reported
	// is the one that applies by default: the system home when the per-user home
	// does not exist yet, and the per-user home once it does.
	p.readEnvironment(ctx)
	scope := model.ScopeGlobal
	if p.userDirExists() && p.userDir != "" {
		scope = model.ScopeUser
	}
	return p.parseGemList(res.Stdout, scope), nil
}

// userDirExists reports whether the per-user gem home is present.
func (p *Provider) userDirExists() bool {
	if p.userDir == "" {
		return false
	}
	_, err := os.Stat(p.userDir)
	return err == nil
}

// parseGemList reads the `name (version)` lines of `gem list --local`.
//
// Bundled gems appear as `name (default: 1.0.0)`. They are reported with a
// `default:` marker in the version so the difference survives, and Remove refuses
// them rather than letting RubyGems produce an opaque error.
func (p *Provider) parseGemList(out string, scope model.Scope) []model.Installation {
	var res []model.Installation
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasSuffix(trimmed, ":") {
			// Section headers such as "*** LOCAL GEMS ***".
			continue
		}
		open := strings.Index(trimmed, " (")
		if open < 0 || !strings.HasSuffix(trimmed, ")") {
			continue
		}
		name := trimmed[:open]
		version := strings.TrimSuffix(trimmed[open+2:], ")")
		if name == "" || version == "" {
			continue
		}
		res = append(res, model.Installation{
			Name:     name,
			Version:  version,
			Provider: p.Name(),
			Scope:    scope,
		})
	}
	return res
}

// IsBundled reports whether a version string marks a gem shipped with the
// interpreter.
func IsBundled(version string) bool {
	return strings.HasPrefix(version, "default:")
}

// Installed resolves one gem.
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

// Install installs a gem, honouring a version pin.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeGlobal}, nil
	}
	args := []string{"install", req.Package.Name}
	if req.Version() != "" {
		args = append(args, "-v", strings.TrimPrefix(strings.TrimPrefix(req.Version(), "=="), "="))
	}
	res, err := p.runner.Run(ctx, "gem", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run gem install", err)
	}
	if res.Failed() {
		return nil, p.classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a gem.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	res, err := p.runner.Run(ctx, "gem", "uninstall", "-x", "-I", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run gem uninstall", err)
	}
	if res.Failed() {
		return p.classify("remove", res)
	}
	return nil
}

// Update upgrades a gem. RubyGems refuses to downgrade, which is the right
// behaviour for "make it current" and the wrong one for a downgrade request, so
// the pin is passed through and RubyGems decides.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	args := []string{"update"}
	if pkg.Version != "" {
		args = append(args, pkg.Name, "-v", strings.TrimPrefix(strings.TrimPrefix(pkg.Version, "=="), "="))
	} else {
		args = append(args, pkg.Name)
	}
	res, err := p.runner.Run(ctx, "gem", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run gem update", err)
	}
	if res.Failed() {
		return nil, p.classify("upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

// classify turns RubyGems' output into typed errors.
func (p *Provider) classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "could not find gem"), strings.Contains(text, "find a valid gem"),
		strings.Contains(text, "no such file"), strings.Contains(text, "not found"):
		return shell.NotFound(Name, op, "RubyGems does not have that gem")
	case strings.Contains(text, "can't uninstall"), strings.Contains(text, "default gem"):
		return shell.Unsupported(Name, op,
			"this gem ships with the interpreter and cannot be uninstalled")
	case strings.Contains(text, "permission denied"), strings.Contains(text, "you don't have write permissions"):
		return shell.Err(Name, op, provider.ErrPermission,
			"installing into the system gem home needs write access; install with --user for a per-user location", nil)
	case strings.Contains(text, "could not reach"), strings.Contains(text, "network"),
		strings.Contains(text, "rubygems.org"), strings.Contains(text, "connection"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach rubygems.org", nil)
	case strings.Contains(text, "can't satisfy your requirement"),
		strings.Contains(text, "conflict"), strings.Contains(text, "depends on"):
		return shell.Err(Name, op, provider.ErrConflict, shell.FirstLine(res.Field()), nil)
	default:
		msg := shell.FirstLine(res.Field())
		if msg == "" {
			msg = "gem exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}
