// Package npm implements the npm global-package provider.
//
// npm's global prefix is its own placement decision, not Starpoint's: binaries
// land in <prefix>/bin. Starpoint asks npm for the prefix rather than guessing,
// so a custom prefix install still resolves to the right path. Reading the
// installed set uses `npm ls -g --json`, whose "problems" array is surfaced as a
// warning-worthy condition instead of a hard failure.
package npm

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "npm"

// Provider implements provider.Provider for npm globals.
type Provider struct {
	runner exec.Runner
	prefix string
}

var _ provider.Provider = (*Provider)(nil)
var _ provider.ContextAwareProvider = (*Provider)(nil)

// New builds an npm provider.
func New(runner exec.Runner) *Provider { return &Provider{runner: runner} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "npm (global packages)" }
func (p *Provider) Kind() provider.Kind { return provider.KindNode }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:            true,
		Install:             true,
		Remove:              true,
		Update:              true,
		PinVersion:          true, // name@version
		DefaultScope:        model.ScopeUser,
		CanInstallInContext: true,
	}
}

func (p *Provider) Health(ctx context.Context) provider.Health {
	h := provider.Health{
		Name:         p.Name(),
		Description:  p.Description(),
		Kind:         p.Kind(),
		Capabilities: p.Capabilities(),
		ContextAware: true,
	}
	path, err := p.runner.LookPath("npm")
	if err != nil {
		h.Reason = "npm not found on PATH"
		return h
	}
	h.Available = true
	h.Binary = path
	res, err := p.runner.Run(ctx, "npm", "--version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	h.Version = strings.TrimSpace(res.Stdout)
	p.prefix = p.resolvePrefix(ctx)
	return h
}

// resolvePrefix asks npm where its global bin directory lives.
func (p *Provider) resolvePrefix(ctx context.Context) string {
	res, err := p.runner.Run(ctx, "npm", "config", "get", "prefix")
	if err != nil || res.Failed() {
		return ""
	}
	return strings.TrimSpace(res.Stdout)
}

// lsOutput is the subset of `npm ls -g --json --depth=0` Starpoint reads.
type lsOutput struct {
	Dependencies map[string]struct {
		Version string `json:"version"`
	} `json:"dependencies"`
	Problems []string `json:"problems"`
}

// Discover lists globally installed npm packages.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	res, err := p.runner.Run(ctx, "npm", "ls", "-g", "--json", "--depth=0")
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not list global npm packages", err)
	}
	if res.Failed() {
		// npm exits nonzero when the tree has problems, but still prints JSON.
		if !looksLikeJSON(res.Stdout) {
			return nil, shell.Failure(p.Name(), "list", shell.Diagnostic(res.Field()), nil)
		}
	}
	var out lsOutput
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not parse npm output: "+err.Error(), nil)
	}
	insts := make([]model.Installation, 0, len(out.Dependencies))
	for name, dep := range out.Dependencies {
		insts = append(insts, model.Installation{
			Name:     name,
			Version:  dep.Version,
			Provider: p.Name(),
			Scope:    model.ScopeUser,
			Path:     p.binPath(name),
		})
	}
	return insts, nil
}

func looksLikeJSON(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")
}

// Installed resolves a single global package.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	res, err := p.runner.Run(ctx, "npm", "ls", "-g", "--json", "--depth=0", pkg.Name)
	if err != nil {
		return nil, shell.Failure(p.Name(), "installed", "could not query npm", err)
	}
	if !looksLikeJSON(res.Stdout) {
		return nil, nil
	}
	var out lsOutput
	if err := json.Unmarshal([]byte(res.Stdout), &out); err != nil {
		return nil, nil
	}
	dep, ok := out.Dependencies[pkg.Name]
	if !ok {
		return nil, nil
	}
	return &model.Installation{
		Name:     pkg.Name,
		Version:  dep.Version,
		Provider: p.Name(),
		Scope:    model.ScopeUser,
		Path:     p.binPath(pkg.Name),
	}, nil
}

// Install installs a global package, honouring a version pin or dist-tag.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	target := req.Package.Name
	if req.Version() != "" {
		target += "@" + strings.TrimPrefix(strings.TrimPrefix(req.Version(), "=="), "=")
	} else if req.Package.Channel != "" {
		target += "@" + req.Package.Channel
	}
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeUser}, nil
	}
	res, err := p.runner.Run(ctx, "npm", "install", "-g", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run npm install", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a global package.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	res, err := p.runner.Run(ctx, "npm", "uninstall", "-g", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run npm uninstall", err)
	}
	if res.Failed() {
		return classify("remove", res)
	}
	return nil
}

// Update upgrades a global package to the latest matching version.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	spec := pkg.Name
	if pkg.Version != "" {
		spec += "@" + strings.TrimPrefix(strings.TrimPrefix(pkg.Version, "=="), "=")
	}
	res, err := p.runner.Run(ctx, "npm", "update", "-g", spec)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run npm update", err)
	}
	if res.Failed() {
		return nil, classify("upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

// binPath resolves an executable inside npm's global prefix.
func (p *Provider) binPath(name string) string {
	if p.prefix == "" {
		return ""
	}
	candidate := filepath.Join(p.prefix, "bin", name)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

func classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "404 not found"), strings.Contains(text, "e404"),
		strings.Contains(text, "no such package"):
		return shell.NotFound(Name, op, "npm could not find this package in the registry")
	case strings.Contains(text, "eexist"), strings.Contains(text, "already exists"):
		return shell.Err(Name, op, provider.ErrConflict, "the package is already installed; use starpoint update", nil)
	case strings.Contains(text, "eacces"), strings.Contains(text, "permission denied"):
		return shell.Err(Name, op, provider.ErrPermission, "npm needs write access to its global prefix", nil)
	case strings.Contains(text, "enetunreach"), strings.Contains(text, "eai_again"),
		strings.Contains(text, "network"), strings.Contains(text, "socket hang up"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach the npm registry", nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "npm exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// InstallWithContext installs a package in the context of a project directory.
// For npm, this means using --prefix to install to the project's node_modules.
func (p *Provider) InstallWithContext(ctx context.Context, pkg model.Package, contextDir string) (*model.Installation, error) {
	target := pkg.Name
	if pkg.Version != "" {
		target += "@" + strings.TrimPrefix(strings.TrimPrefix(pkg.Version, "=="), "=")
	} else if pkg.Channel != "" {
		target += "@" + pkg.Channel
	}
	args := []string{"install", "--prefix", contextDir, target}
	res, err := p.runner.Run(ctx, "npm", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run npm install", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	// For project-local installs, we don't have a simple way to query the installed state
	// like we do for global installs. Return a minimal installation record.
	return &model.Installation{
		Name:     pkg.Name,
		Version:  pkg.Version,
		Provider: p.Name(),
		Scope:    model.ScopeProject,
		Path:     filepath.Join(contextDir, "node_modules", ".bin", pkg.Name),
	}, nil
}

// Verify checks that an installed npm package's executable is functional.
// For npm, this attempts to run the package's executable with --version.
func (p *Provider) Verify(ctx context.Context, pkg model.Package) error {
	// Try to run the executable with --version to verify it's functional.
	res, err := p.runner.Run(ctx, pkg.Name, "--version")
	if err != nil {
		return shell.Failure(p.Name(), "verify", "could not run executable for verification", err)
	}
	if res.Failed() {
		return shell.Failure(p.Name(), "verify", "executable failed verification", nil)
	}
	return nil
}

var _ provider.LatestVersionProvider = (*Provider)(nil)

// LatestVersion asks the registry for the version npm would install.
func (p *Provider) LatestVersion(ctx context.Context, pkg model.Package) (string, error) {
	res, err := p.runner.Run(ctx, "npm", "view", pkg.Name, "version")
	if err != nil {
		return "", shell.Failure(p.Name(), "latest", "could not run npm view", err)
	}
	if res.Failed() {
		return "", shell.Failure(p.Name(), "latest", shell.Diagnostic(res.Field()), nil)
	}
	return strings.TrimSpace(shell.FirstLine(res.Stdout)), nil
}

var (
	_ provider.Privileged         = (*Provider)(nil)
	_ provider.InstallDirProvider = (*Provider)(nil)
	_ provider.StateDirProvider   = (*Provider)(nil)
)

// Privilege classifies npm by its global prefix, asked as the invoking user
// (so their ~/.npmrc decides): a prefix in their home (nvm, ~/.npm-global)
// or owned by them runs npm as them; a system prefix (/usr, a root-owned
// /usr/local) runs it as root, and what it creates there is made readable by
// everyone.
func (p *Provider) Privilege(ctx context.Context, q exec.Runner, t provider.PrivilegeTarget) provider.PrivilegeDecision {
	dir := ""
	if res, err := q.Run(ctx, "npm", "config", "get", "prefix"); err == nil && !res.Failed() {
		dir = strings.TrimSpace(shell.FirstLine(res.Stdout))
	}
	return shell.PrivilegeAt(dir, "npm's global prefix", t)
}

// globalModules is the directory global packages are installed in:
// <prefix>/lib/node_modules, or <prefix>/node_modules on Windows.
func (p *Provider) globalModules(ctx context.Context) string {
	if p.prefix == "" {
		p.prefix = p.resolvePrefix(ctx)
	}
	if p.prefix == "" {
		return ""
	}
	if runtime.GOOS == "windows" {
		return filepath.Join(p.prefix, "node_modules")
	}
	return filepath.Join(p.prefix, "lib", "node_modules")
}

// InstallDirs reports where a global package's files land, beside the
// command npm links into <prefix>/bin.
func (p *Provider) InstallDirs(ctx context.Context, pkg model.Package) []string {
	dir := p.globalModules(ctx)
	if dir == "" || pkg.Name == "" {
		return nil
	}
	return []string{filepath.Join(dir, filepath.FromSlash(pkg.Name))}
}

// StateDirs reports npm's cache (~/.npm by default), which every install
// writes to, and the global node_modules directory. They are only ever
// checked for root-owned entries under the invoking user's home.
func (p *Provider) StateDirs(ctx context.Context) []string {
	var out []string
	if res, err := p.runner.Run(ctx, "npm", "config", "get", "cache"); err == nil && !res.Failed() {
		if dir := strings.TrimSpace(shell.FirstLine(res.Stdout)); filepath.IsAbs(dir) {
			out = append(out, dir)
		}
	}
	if dir := p.globalModules(ctx); dir != "" {
		out = append(out, dir)
	}
	return out
}
