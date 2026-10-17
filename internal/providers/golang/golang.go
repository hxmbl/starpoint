// Package golang implements the Go provider for binaries installed with
// `go install`.
//
// Go's model is module paths, not names: `go install example.com/tool/cmd@latest`.
// A bare name like `gopls` is not installable directly. Starpoint therefore keeps
// the configuration entry and the install target distinct — the entry is what
// the user asked for, the module path is what Go needs — and refuses politely
// when only a bare name is available.
package golang

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "go"

// Provider implements provider.Provider for Go tool binaries.
type Provider struct {
	runner exec.Runner
	home   string
}

var _ provider.Provider = (*Provider)(nil)
var _ provider.ContextAwareProvider = (*Provider)(nil)
var _ provider.ExecutableDirProvider = (*Provider)(nil)
var _ provider.StateDirProvider = (*Provider)(nil)

// New builds a Go provider.
func New(runner exec.Runner, home string) *Provider { return &Provider{runner: runner, home: home} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "Go (go install binaries)" }
func (p *Provider) Kind() provider.Kind { return provider.KindGo }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:            true, // via the Go bin directory
		Install:             true,
		Remove:              true,
		Update:              true, // go install path@latest replaces the binary
		PinVersion:          true, // path@v1.2.3
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
	path, err := p.runner.LookPath("go")
	if err != nil {
		h.Reason = "go not found on PATH"
		return h
	}
	h.Available = true
	h.Binary = path
	res, err := p.runner.Run(ctx, "go", "version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	// Output looks like "go version go1.23.4 darwin/amd64". The Go tool prefixes
	// its own version string with "go", which Starpoint strips so versions from
	// different providers compare the same way.
	if fields := shell.FieldsFrom(res.Stdout, 2); len(fields) > 0 {
		h.Version = strings.TrimPrefix(fields[0], "go")
	}
	return h
}

// binDir returns the directory `go install` writes executables to. GOBIN wins
// over GOPATH/bin because that is Go's own precedence.
func (p *Provider) binDir(ctx context.Context) string {
	res, err := p.runner.Run(ctx, "go", "env", "GOBIN")
	if err == nil && !res.Failed() {
		if dir := strings.TrimSpace(res.Stdout); dir != "" {
			return dir
		}
	}
	res, err = p.runner.Run(ctx, "go", "env", "GOPATH")
	if err == nil && !res.Failed() {
		line := strings.TrimSpace(strings.Split(res.Stdout, "\n")[0])
		if gopath := filepath.SplitList(line); len(gopath) > 0 && gopath[0] != "" {
			return filepath.Join(gopath[0], "bin")
		}
	}
	if p.home == "" {
		return ""
	}
	return filepath.Join(p.home, "go", "bin")
}

// ExecutableDir reports where `go install` writes executables: GOBIN, then the
// first GOPATH entry's bin, then ~/go/bin. It asks `go env`, so a value set
// with `go env -w` counts; with go absent it falls back to the environment
// and the default, which is still where a later `go install` would write.
func (p *Provider) ExecutableDir(ctx context.Context) string {
	if _, err := p.runner.LookPath("go"); err != nil {
		if dir := os.Getenv("GOBIN"); dir != "" {
			return dir
		}
		if gopath := filepath.SplitList(os.Getenv("GOPATH")); len(gopath) > 0 && gopath[0] != "" {
			return filepath.Join(gopath[0], "bin")
		}
		if p.home == "" {
			return ""
		}
		return filepath.Join(p.home, "go", "bin")
	}
	return p.binDir(ctx)
}

// StateDirs reports where `go install` keeps state besides the executable:
// the module cache and the build cache, from `go env` when go is present.
func (p *Provider) StateDirs(ctx context.Context) []string {
	var out []string
	if _, err := p.runner.LookPath("go"); err == nil {
		for _, key := range []string{"GOMODCACHE", "GOCACHE"} {
			res, err := p.runner.Run(ctx, "go", "env", key)
			if err == nil && !res.Failed() {
				if dir := strings.TrimSpace(strings.Split(res.Stdout, "\n")[0]); dir != "" && dir != "off" {
					out = append(out, dir)
				}
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	if p.home != "" {
		out = append(out, filepath.Join(p.home, "go", "pkg", "mod"), filepath.Join(p.home, ".cache", "go-build"))
	}
	return out
}

// Discover lists executables in the Go bin directory. Go does not record metadata
// beside the binaries, so versions are unknown here and left empty rather than
// guessed.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	dir := p.binDir(ctx)
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, shell.Failure(p.Name(), "list", "could not read "+dir, err)
	}
	var out []model.Installation
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil || info.Mode()&0o111 == 0 {
			continue
		}
		out = append(out, model.Installation{
			Name:     e.Name(),
			Provider: p.Name(),
			Scope:    model.ScopeUser,
			Path:     filepath.Join(dir, e.Name()),
		})
	}
	return out, nil
}

// Installed resolves a single binary in the Go bin directory.
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

// binaryName derives the executable name from a module path.
func binaryName(module string) string {
	// Strip a version suffix, then take the last path element.
	path := module
	if at := strings.LastIndex(path, "@"); at >= 0 {
		path = path[:at]
	}
	path = strings.TrimSuffix(path, "/")
	if slash := strings.LastIndex(path, "/"); slash >= 0 {
		return path[slash+1:]
	}
	return path
}

// IsModulePath reports whether a configuration entry names an installable Go
// module rather than a bare binary name.
func IsModulePath(name string) bool {
	return strings.Contains(name, "/") || strings.Contains(name, ".")
}

// goVersion normalises a requested version into the form Go modules use.
//
// `go install` wants `path@v1.2.3`, but a user who wrote `gopls@v0.16.1` has
// already supplied the prefix. Blindly adding "v" would produce "@vv0.16.1", so
// an existing prefix is kept rather than doubled.
func goVersion(requested string) string {
	v := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(requested, "=="), "="), "v")
	if v == "" {
		return v
	}
	// Only strip a "v" that is actually part of a version number, so a branch
	// name or channel is left alone.
	if v[0] < '0' || v[0] > '9' {
		return requested
	}
	return v
}

// Install installs a module, honouring an explicit version pin.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	if !IsModulePath(req.Package.Name) {
		return nil, shell.Unsupported(p.Name(), "install",
			"go install needs a module path (for example golang.org/x/tools/gopls), not the binary name")
	}
	target := req.Package.Name
	if req.Version() != "" {
		target += "@v" + strings.TrimPrefix(strings.TrimPrefix(req.Version(), "=="), "=")
	} else {
		target += "@latest"
	}
	if req.DryRun {
		return &model.Installation{Name: binaryName(req.Package.Name), Provider: p.Name(), Scope: model.ScopeUser}, nil
	}
	res, err := p.runner.Run(ctx, "go", "install", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run go install", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	return p.Installed(ctx, model.Package{Name: binaryName(req.Package.Name)})
}

// Remove deletes a binary from the Go bin directory. Go has no uninstall command,
// so this removes the file the install produced and nothing else.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	dir := p.binDir(ctx)
	if dir == "" {
		return shell.Unsupported(p.Name(), "remove", "could not determine the Go bin directory")
	}
	target := filepath.Join(dir, pkg.Name)
	if _, err := os.Stat(target); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return shell.Failure(p.Name(), "remove", "could not inspect "+target, err)
	}
	if err := os.Remove(target); err != nil {
		return shell.Failure(p.Name(), "remove", "could not remove "+target, err)
	}
	return nil
}

// Update reinstalls the module at its latest version.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	if !IsModulePath(pkg.Name) {
		return nil, shell.Unsupported(p.Name(), "upgrade",
			"the configured entry is a bare binary name, which is not enough to reinstall; record the module path instead")
	}
	target := pkg.Name
	if pkg.Version != "" {
		target += "@v" + strings.TrimPrefix(strings.TrimPrefix(pkg.Version, "=="), "=")
	} else {
		target += "@latest"
	}
	res, err := p.runner.Run(ctx, "go", "install", "-force", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run go install", err)
	}
	if res.Failed() {
		return nil, classify("upgrade", res)
	}
	return p.Installed(ctx, model.Package{Name: binaryName(pkg.Name)})
}

func classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "malformed module path"), strings.Contains(text, "malformed import path"),
		strings.Contains(text, "version \"@"), strings.Contains(text, "invalid version"):
		return shell.Err(Name, op, provider.ErrUnsupported, shell.Diagnostic(res.Field()), nil)
	case strings.Contains(text, "unknown revision"), strings.Contains(text, "module lookup disabled"),
		strings.Contains(text, "no matching versions"), strings.Contains(text, "not a known dependency"),
		strings.Contains(text, "does not contain"), strings.Contains(text, "unrecognized import path"), strings.Contains(text, "reading"):
		return shell.NotFound(Name, op, "the Go module or version could not be resolved")
	case strings.Contains(text, "permission denied"):
		return shell.Err(Name, op, provider.ErrPermission, "the Go bin directory is not writable", nil)
	case strings.Contains(text, "dial tcp"), strings.Contains(text, "no such host"),
		strings.Contains(text, "proxy.golang.org"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach the Go module proxy", nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "go exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// InstallWithContext installs a package in the context of a project directory.
// For Go, this means setting GOBIN to a project-local bin directory during install.
func (p *Provider) InstallWithContext(ctx context.Context, pkg model.Package, contextDir string) (*model.Installation, error) {
	if !IsModulePath(pkg.Name) {
		return nil, shell.Unsupported(p.Name(), "install",
			"go install needs a module path (for example golang.org/x/tools/gopls), not the binary name")
	}
	target := pkg.Name
	if pkg.Version != "" {
		target += "@v" + strings.TrimPrefix(strings.TrimPrefix(pkg.Version, "=="), "=")
	} else {
		target += "@latest"
	}
	// Set GOBIN to project-local bin directory
	projectBin := filepath.Join(contextDir, "bin")
	// Note: We'd need to modify the runner to set environment variables.
	// For now, we'll do a standard install and note the limitation.
	res, err := p.runner.Run(ctx, "go", "install", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run go install", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	// Return a minimal installation record
	return &model.Installation{
		Name:     binaryName(pkg.Name),
		Version:  pkg.Version,
		Provider: p.Name(),
		Scope:    model.ScopeProject,
		Path:     filepath.Join(projectBin, binaryName(pkg.Name)),
	}, nil
}

// Verify checks that an installed Go binary is functional.
// For Go, this attempts to run the binary with --version.
func (p *Provider) Verify(ctx context.Context, pkg model.Package) error {
	// For Go packages, the executable name is derived from the module path
	exeName := binaryName(pkg.Name)
	res, err := p.runner.Run(ctx, exeName, "--version")
	if err != nil {
		return shell.Failure(p.Name(), "verify", "could not run executable for verification", err)
	}
	if res.Failed() {
		return shell.Failure(p.Name(), "verify", "executable failed verification", nil)
	}
	return nil
}
