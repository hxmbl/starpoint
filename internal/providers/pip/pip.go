// Package pip implements the pip provider for Python packages installed with
// pip itself.
//
// pip installs into whichever interpreter owns it, which is why this provider is
// careful about two things. First, which interpreter: `pip` and `pip3` on the
// same machine can belong to different Pythons, so both are probed and the
// interpreter is named in diagnostics. Second, where: pip needs elevation to
// write a system site-packages, so every install is `--user` by default and the
// placement reported is the user base, which is the only one pip can write
// without root.
//
// uv and pip are deliberately separate providers. uv replaces the *workflow*;
// pip remains the tool that owns a site-packages tree, and a machine can have
// software installed by each.
package pip

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "pip"

// Provider implements provider.Provider for pip.
type Provider struct {
	runner exec.Runner

	// binary is the pip executable to invoke: "pip3" when present, else "pip".
	binary string
	// python is the interpreter pip belongs to, for diagnostics.
	python string
	// binDir caches the user-base bin directory once it is known.
	binDir string
}

var (
	_ provider.Provider              = (*Provider)(nil)
	_ provider.ExecutableDirProvider = (*Provider)(nil)
)

// New builds a pip provider.
func New(runner exec.Runner) *Provider { return &Provider{runner: runner} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "pip (Python packages)" }
func (p *Provider) Kind() provider.Kind { return provider.KindPython }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
		PinVersion:   true,            // name==version
		DefaultScope: model.ScopeUser, // installs with --user
	}
}

func (p *Provider) Health(ctx context.Context) provider.Health {
	h := provider.Health{
		Name:         p.Name(),
		Description:  p.Description(),
		Kind:         p.Kind(),
		Capabilities: p.Capabilities(),
	}
	path, binary, err := p.locate()
	if err != nil {
		h.Reason = "pip not found on PATH"
		return h
	}
	p.binary = binary
	h.Available = true
	h.Binary = path
	res, runErr := p.runner.Run(ctx, p.binary, "--version")
	if runErr != nil {
		h.Reason = runErr.Error()
		return h
	}
	// "pip 26.2.1 from /usr/local/lib/python3.14/site-packages/pip (python 3.14)"
	h.Version = shell.VersionFromArgs(shell.Fields(shell.FirstLine(res.Stdout))...)
	if interpreter := strings.TrimSpace(p.interpreterFrom(res.Stdout)); interpreter != "" {
		h.Reason = "installs into " + interpreter + "'s user site-packages"
	}
	return h
}

// locate finds a usable pip executable, preferring the version-specific name.
func (p *Provider) locate() (string, string, error) {
	if p.binary != "" {
		return "", p.binary, nil
	}
	for _, candidate := range []string{"pip3", "pip"} {
		if path, err := p.runner.LookPath(candidate); err == nil {
			// Remember it: every later command must use the same interpreter's
			// pip, not whichever name happens to appear in a config entry.
			p.binary = candidate
			p.python = candidate
			return path, candidate, nil
		}
	}
	return "", "", shell.NotFound(Name, "locate", "neither pip3 nor pip is on PATH")
}

// interpreterFrom extracts the interpreter from `pip --version`.
func (p *Provider) interpreterFrom(out string) string {
	open := strings.LastIndex(out, "(python ")
	if open < 0 {
		return ""
	}
	rest := out[open+len("(python "):]
	if end := strings.Index(rest, ")"); end > 0 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}

// Discover parses `pip list --format=freeze`, whose lines are `name==version`.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	if _, _, err := p.locate(); err != nil {
		return nil, nil
	}
	res, err := p.runner.Run(ctx, p.binary, "list", "--format=freeze")
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not list installed Python packages", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "list", shell.Diagnostic(res.Field()), nil)
	}
	return p.parseFreeze(res.Stdout), nil
}

// parseFreeze reads `name==version` lines.
func (p *Provider) parseFreeze(out string) []model.Installation {
	res := []model.Installation{}
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		name, version, found := strings.Cut(trimmed, "==")
		if !found || name == "" {
			continue
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

// Installed resolves one package.
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

// Install installs into the user site, which is the only location pip can write
// without elevation. An explicit version becomes a PEP 440 pin.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	if _, _, err := p.locate(); err != nil {
		return nil, shell.Unsupported(p.Name(), "install", "pip is not installed")
	}
	target := req.Package.Name
	if req.Version() != "" {
		target += "==" + strings.TrimPrefix(strings.TrimPrefix(req.Version(), "=="), "=")
	}
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeUser}, nil
	}
	res, err := p.runner.Run(ctx, p.binary, "install", "--user", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run pip install", err)
	}
	if res.Failed() {
		return nil, p.classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a package.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	if _, _, err := p.locate(); err != nil {
		return shell.Unsupported(p.Name(), "remove", "pip is not installed")
	}
	res, err := p.runner.Run(ctx, p.binary, "uninstall", "-y", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run pip uninstall", err)
	}
	if res.Failed() {
		return p.classify("remove", res)
	}
	return nil
}

// Update upgrades a package within the user site.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	if _, _, err := p.locate(); err != nil {
		return nil, shell.Unsupported(p.Name(), "upgrade", "pip is not installed")
	}
	args := []string{"install", "--user", "--upgrade"}
	if pkg.Version != "" {
		args = append(args, pkg.Name+"=="+strings.TrimPrefix(strings.TrimPrefix(pkg.Version, "=="), "="))
	} else {
		args = append(args, pkg.Name)
	}
	res, err := p.runner.Run(ctx, p.binary, args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run pip install --upgrade", err)
	}
	if res.Failed() {
		return nil, p.classify("upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

// classify turns pip's output into typed errors.
func (p *Provider) classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "no matching distribution found"),
		strings.Contains(text, "could not find a version"),
		strings.Contains(text, "no such file or directory: /"):
		return shell.NotFound(Name, op, "pip could not find that package")
	case strings.Contains(text, "permission denied"), strings.Contains(text, "externally-managed"),
		strings.Contains(text, "defaulting to user installation"):
		return shell.Err(Name, op, provider.ErrPermission,
			"pip cannot write this interpreter's site-packages", nil)
	case strings.Contains(text, "connection"), strings.Contains(text, "network"),
		strings.Contains(text, "pypi.org"), strings.Contains(text, "temporary failure"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach PyPI", nil)
	case strings.Contains(text, "conflict"), strings.Contains(text, "dependency"),
		strings.Contains(text, "resolutionimpossible"):
		return shell.Err(Name, op, provider.ErrConflict, shell.Diagnostic(res.Field()), nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "pip exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// Verify checks that an installed Python package is functional.
// For pip, this attempts to run the package's executable with --version.
func (p *Provider) Verify(ctx context.Context, pkg model.Package) error {
	if _, _, err := p.locate(); err != nil {
		return shell.Unsupported(p.Name(), "verify", "pip is not installed")
	}
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

// ExecutableDir reports where `pip install --user` puts console scripts: the
// bin directory of the interpreter's user base. On Linux that is ~/.local/bin,
// the same directory uv links its tools into, which is why a move between the
// two has to know about it.
func (p *Provider) ExecutableDir(ctx context.Context) string {
	if p.binDir != "" {
		return p.binDir
	}
	if runtime.GOOS == "windows" {
		// The Windows user scheme nests scripts under a versioned directory;
		// rather than guess, report that the location is unknown.
		return ""
	}
	base := os.Getenv("PYTHONUSERBASE")
	if base == "" {
		base = p.userBase(ctx)
	}
	if base == "" {
		return ""
	}
	p.binDir = filepath.Join(base, "bin")
	return p.binDir
}

// userBase asks the interpreter pip belongs to for its user base.
func (p *Provider) userBase(ctx context.Context) string {
	if _, _, err := p.locate(); err != nil {
		return ""
	}
	var candidates []string
	if res, err := p.runner.Run(ctx, p.binary, "--version"); err == nil && !res.Failed() {
		if version := strings.TrimSpace(p.interpreterFrom(res.Stdout)); version != "" {
			candidates = append(candidates, "python"+version)
		}
	}
	candidates = append(candidates, "python3")
	for _, python := range candidates {
		if _, err := p.runner.LookPath(python); err != nil {
			continue
		}
		res, err := p.runner.Run(ctx, python, "-m", "site", "--user-base")
		if err != nil || res.Failed() {
			continue
		}
		if base := strings.TrimSpace(shell.FirstLine(res.Stdout)); base != "" {
			return base
		}
	}
	return ""
}
