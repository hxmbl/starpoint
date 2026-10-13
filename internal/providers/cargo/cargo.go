// Package cargo implements the Cargo provider for Rust binaries installed with
// `cargo install`.
//
// Cargo's registry model is crates rather than packages, and its installed set is
// reported by `cargo install --list` in a colon-terminated block format. Starpoint
// preserves that distinction: an entry names a crate, and the executable it
// provides is recorded as the path.
package cargo

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "cargo"

// Provider implements provider.Provider for Cargo.
type Provider struct {
	runner exec.Runner
	home   string
}

var _ provider.Provider = (*Provider)(nil)
var _ provider.ContextAwareProvider = (*Provider)(nil)

// New builds a Cargo provider.
func New(runner exec.Runner, home string) *Provider { return &Provider{runner: runner, home: home} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "Cargo (Rust binaries)" }
func (p *Provider) Kind() provider.Kind { return provider.KindRust }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:            true,
		Install:             true,
		Remove:              true,
		Update:              true, // `cargo install` over an existing crate is an upgrade.
		PinVersion:          true, // --version x.y.z
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
	path, err := p.runner.LookPath("cargo")
	if err != nil {
		h.Reason = "cargo not found on PATH"
		return h
	}
	h.Available = true
	h.Binary = path
	res, err := p.runner.Run(ctx, "cargo", "--version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	h.Version = shell.VersionFromArgs(strings.Fields(res.Stdout)...)
	return h
}

// Discover parses `cargo install --list`.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	res, err := p.runner.Run(ctx, "cargo", "install", "--list")
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not list installed crates", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "list", shell.Diagnostic(res.Field()), nil)
	}
	return p.parseList(res.Stdout), nil
}

// parseList reads the crate blocks emitted by `cargo install --list`. A block
// header is an unindented "crate v1.2.3:" line; the indented lines below it are
// the executables the crate installed.
func (p *Provider) parseList(out string) []model.Installation {
	var res []model.Installation
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		if indented {
			if len(res) == 0 {
				continue
			}
			exe := strings.TrimSpace(line)
			res[len(res)-1].Path = p.binPath(exe)
			continue
		}
		header := strings.TrimSuffix(strings.TrimSpace(line), ":")
		fields := shell.Fields(header)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		version := ""
		if len(fields) > 1 {
			version = strings.TrimPrefix(fields[1], "v")
		}
		res = append(res, model.Installation{
			Name:     name,
			Version:  version,
			Provider: p.Name(),
			Scope:    model.ScopeUser,
		})
	}
	return res
}

// Installed resolves a single crate.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	all, err := p.Discover(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == pkg.Name {
			found := all[i]
			return &found, nil
		}
	}
	return nil, nil
}

// Install installs a crate, honouring a version pin.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	args := []string{"install", req.Package.Name}
	if req.Version() != "" {
		args = append(args, "--version", strings.TrimPrefix(strings.TrimPrefix(req.Version(), "=="), "="))
	}
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeUser}, nil
	}
	res, err := p.runner.Run(ctx, "cargo", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run cargo install", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a crate.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	res, err := p.runner.Run(ctx, "cargo", "uninstall", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "uninstall", "could not run cargo uninstall", err)
	}
	if res.Failed() {
		return classify("uninstall", res)
	}
	return nil
}

// Update reinstalls the crate, which Cargo treats as an upgrade to the newest
// matching version.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	args := []string{"install"}
	if pkg.Version != "" {
		args = append(args, "--version", strings.TrimPrefix(strings.TrimPrefix(pkg.Version, "=="), "="))
	}
	args = append(args, "--force", pkg.Name)
	res, err := p.runner.Run(ctx, "cargo", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run cargo install", err)
	}
	if res.Failed() {
		return nil, classify("upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

// binPath resolves an executable inside ~/.cargo/bin.
func (p *Provider) binPath(exe string) string {
	if p.home == "" || exe == "" {
		return exe
	}
	candidate := filepath.Join(p.home, ".cargo", "bin", exe)
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return exe
}

func classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "no matching package named"), strings.Contains(text, "did not match any packages"),
		strings.Contains(text, "no package named"), strings.Contains(text, "registry index was not found"):
		return shell.NotFound(Name, op, "Cargo has no crate with this name in the registry")
	case strings.Contains(text, "already exists"), strings.Contains(text, "binary `") && strings.Contains(text, "already exists"):
		return shell.Err(Name, op, provider.ErrConflict, "the crate is already installed; use starpoint update", nil)
	case strings.Contains(text, "network"), strings.Contains(text, "connection"), strings.Contains(text, "could not resolve host"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach crates.io", nil)
	case strings.Contains(text, "permission denied"):
		return shell.Err(Name, op, provider.ErrPermission, "cargo needs write access to its install directory", nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "cargo exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// InstallWithContext installs a package in the context of a project directory.
// For cargo, this means using --manifest-path to install to the project's context.
func (p *Provider) InstallWithContext(ctx context.Context, pkg model.Package, contextDir string) (*model.Installation, error) {
	args := []string{"install", pkg.Name}
	if pkg.Version != "" {
		args = append(args, "--version", strings.TrimPrefix(strings.TrimPrefix(pkg.Version, "=="), "="))
	}
	// Use --manifest-path if Cargo.toml exists in the context directory
	manifestPath := filepath.Join(contextDir, "Cargo.toml")
	if _, err := os.Stat(manifestPath); err == nil {
		args = append(args, "--manifest-path", manifestPath)
	}
	res, err := p.runner.Run(ctx, "cargo", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run cargo install", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	// For project-local installs, return a minimal installation record
	return &model.Installation{
		Name:     pkg.Name,
		Version:  pkg.Version,
		Provider: p.Name(),
		Scope:    model.ScopeProject,
		Path:     filepath.Join(contextDir, "target", "debug", pkg.Name),
	}, nil
}

// Verify checks that an installed crate's executable is functional.
// For Cargo, this attempts to run the crate's executable with --version.
func (p *Provider) Verify(ctx context.Context, pkg model.Package) error {
	// First, discover the executable name for this crate
	all, err := p.Discover(ctx)
	if err != nil {
		return shell.Failure(p.Name(), "verify", "could not list installed crates", err)
	}
	var exeName string
	for _, inst := range all {
		if inst.Name == pkg.Name && inst.Path != "" {
			exeName = filepath.Base(inst.Path)
			break
		}
	}
	if exeName == "" {
		// Fall back to the package name as the executable name
		exeName = pkg.Name
	}
	res, err := p.runner.Run(ctx, exeName, "--version")
	if err != nil {
		return shell.Failure(p.Name(), "verify", "could not run executable for verification", err)
	}
	if res.Failed() {
		return shell.Failure(p.Name(), "verify", "executable failed verification", nil)
	}
	return nil
}

var _ provider.LatestVersionProvider = (*Provider)(nil)

// LatestVersion asks crates.io through `cargo search`, whose lines read
// `name = "x.y.z"    # description`. Only an exact name match counts.
func (p *Provider) LatestVersion(ctx context.Context, pkg model.Package) (string, error) {
	res, err := p.runner.Run(ctx, "cargo", "search", pkg.Name, "--limit", "1")
	if err != nil {
		return "", shell.Failure(p.Name(), "latest", "could not run cargo search", err)
	}
	if res.Failed() {
		return "", shell.Failure(p.Name(), "latest", shell.Diagnostic(res.Field()), nil)
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		name, rest, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != pkg.Name {
			continue
		}
		rest = strings.SplitN(rest, "#", 2)[0]
		return strings.Trim(strings.TrimSpace(rest), `"`), nil
	}
	return "", nil
}
