// Package uv implements the uv provider for Python CLI tools and interpreters.
//
// uv's model is "tools", each installed into its own isolated environment, with
// executables exposed through a bin directory. Starpoint exposes that shape
// honestly: Discover lists tools, Installed resolves the environment path, and
// version pinning is supported through the PEP 440 requirement syntax uv
// already understands.
package uv

import (
	"context"
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
const Name = "uv"

// Provider implements provider.Provider for uv.
type Provider struct {
	runner exec.Runner
	home   string
}

var (
	_ provider.Provider              = (*Provider)(nil)
	_ provider.ExecutableDirProvider = (*Provider)(nil)
	_ provider.StateDirProvider      = (*Provider)(nil)
)

// New builds a uv provider. home is the user home directory used to locate uv's
// managed directories; an empty home makes the provider report unavailable.
func New(runner exec.Runner, home string) *Provider { return &Provider{runner: runner, home: home} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "uv (Python tools)" }
func (p *Provider) Kind() provider.Kind { return provider.KindPython }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
		PinVersion:   true, // uv tool install pkg==x.y
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
	path, err := p.runner.LookPath("uv")
	if err != nil {
		h.Reason = "uv not found on PATH"
		return h
	}
	h.Available = true
	h.Binary = path
	res, err := p.runner.Run(ctx, "uv", "--version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	h.Version = shell.VersionFromArgs(strings.Fields(res.Stdout)...)
	return h
}

// Discover lists uv-managed tools.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	res, err := p.runner.Run(ctx, "uv", "tool", "list")
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not list uv tools", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "list", shell.Diagnostic(res.Field()), nil)
	}
	return p.parseToolList(res.Stdout), nil
}

// parseToolList reads the `uv tool list` output. Each tool occupies a header line
// of the form "name v1.2.3" followed by indented "- executable" lines.
func (p *Provider) parseToolList(out string) []model.Installation {
	var res []model.Installation
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Executable lines are indented and start with a dash. Older and newer
		// uv versions differ in the indentation, so the dash is the reliable
		// marker.
		if strings.HasPrefix(trimmed, "- ") {
			if len(res) == 0 {
				continue
			}
			res[len(res)-1].Path = strings.TrimSpace(strings.TrimPrefix(trimmed, "-"))
			continue
		}
		fields := shell.Fields(trimmed)
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

// Installed resolves a single uv tool.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	_ = ctx
	res, err := p.runner.Run(ctx, "uv", "tool", "list")
	if err != nil {
		return nil, shell.Failure(p.Name(), "installed", "could not list uv tools", err)
	}
	if res.Failed() {
		return nil, nil
	}
	for _, inst := range p.parseToolList(res.Stdout) {
		if inst.Name == pkg.Name {
			found := inst
			found.Path = p.toolBinPath(pkg.Name)
			return &found, nil
		}
	}
	return nil, nil
}

// Install installs a uv tool, honouring a version pin.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	target := req.Package.Name
	if req.Version() != "" {
		target += "==" + strings.TrimPrefix(strings.TrimPrefix(req.Version(), "=="), "=")
	}
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeUser}, nil
	}
	res, err := p.runner.Run(ctx, "uv", "tool", "install", target)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run uv tool install", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a uv tool.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	res, err := p.runner.Run(ctx, "uv", "tool", "uninstall", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "uninstall", "could not run uv tool uninstall", err)
	}
	if res.Failed() {
		return classify("uninstall", res)
	}
	return nil
}

// Update upgrades a uv tool to its newest release.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	res, err := p.runner.Run(ctx, "uv", "tool", "upgrade", pkg.Name)
	if err != nil {
		return nil, shell.Failure(p.Name(), "upgrade", "could not run uv tool upgrade", err)
	}
	if res.Failed() {
		return nil, classify("upgrade", res)
	}
	return p.Installed(ctx, pkg)
}

// Verify checks that an installed uv tool is functional by running it with --version.
func (p *Provider) Verify(ctx context.Context, pkg model.Package) error {
	inst, err := p.Installed(ctx, pkg)
	if err != nil || inst == nil {
		return &provider.Error{
			Provider: p.Name(),
			Kind:     provider.ErrNotFound,
			Message:  "package not installed",
		}
	}
	// Try to run the tool with --version to verify it works
	binPath := p.toolBinPath(pkg.Name)
	if binPath == "" {
		return &provider.Error{
			Provider: p.Name(),
			Kind:     provider.ErrFailed,
			Message:  "cannot locate tool executable",
		}
	}
	_, err = p.runner.Run(ctx, binPath, "--version")
	if err != nil {
		return &provider.Error{
			Provider: p.Name(),
			Kind:     provider.ErrFailed,
			Message:  "tool verification failed",
			Err:      err,
		}
	}
	return nil
}

// ExecutableDir reports the directory uv links tool executables into, following
// uv's own precedence: UV_TOOL_BIN_DIR, then XDG_BIN_HOME, then ~/.local/bin.
func (p *Provider) ExecutableDir(context.Context) string {
	if dir := exec.Getenv(p.runner, "UV_TOOL_BIN_DIR"); dir != "" {
		return dir
	}
	if dir := exec.Getenv(p.runner, "XDG_BIN_HOME"); dir != "" {
		return dir
	}
	if p.home == "" {
		return ""
	}
	return filepath.Join(p.home, ".local", "bin")
}

// StateDirs reports where uv keeps tool environments and its cache:
// UV_TOOL_DIR (or $XDG_DATA_HOME/uv/tools, ~/.local/share/uv/tools) and
// UV_CACHE_DIR (or $XDG_CACHE_HOME/uv, ~/.cache/uv). On macOS uv uses the
// same XDG-style layout.
func (p *Provider) StateDirs(context.Context) []string {
	var out []string
	switch {
	case exec.Getenv(p.runner, "UV_TOOL_DIR") != "":
		out = append(out, exec.Getenv(p.runner, "UV_TOOL_DIR"))
	case exec.Getenv(p.runner, "XDG_DATA_HOME") != "":
		out = append(out, filepath.Join(exec.Getenv(p.runner, "XDG_DATA_HOME"), "uv", "tools"))
	case p.home != "":
		out = append(out, filepath.Join(p.home, ".local", "share", "uv", "tools"))
	}
	switch {
	case exec.Getenv(p.runner, "UV_CACHE_DIR") != "":
		out = append(out, exec.Getenv(p.runner, "UV_CACHE_DIR"))
	case exec.Getenv(p.runner, "XDG_CACHE_HOME") != "":
		out = append(out, filepath.Join(exec.Getenv(p.runner, "XDG_CACHE_HOME"), "uv"))
	case p.home != "":
		out = append(out, filepath.Join(p.home, ".cache", "uv"))
	}
	return out
}

// toolBinPath reports where uv exposes the tool's executable.
//
// The bin directory is shared with other tools (pip --user writes console
// scripts to the same place), so a file existing there is not evidence that uv
// put it there. On Unix uv links its executables into its own tool directory,
// and only a link that points there is reported as uv's.
func (p *Provider) toolBinPath(name string) string {
	dir := p.ExecutableDir(context.Background())
	if dir == "" {
		return ""
	}
	candidate := filepath.Join(dir, name)
	info, err := os.Lstat(candidate)
	if err != nil {
		return ""
	}
	if runtime.GOOS != "windows" && info.Mode()&os.ModeSymlink == 0 {
		return ""
	}
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(candidate)
		if err != nil || !strings.Contains(filepath.ToSlash(target), "/uv/") {
			return ""
		}
	}
	return candidate
}

func classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "was not found in the registry"), strings.Contains(text, "no solution found"),
		strings.Contains(text, "not found in a package registry"):
		return shell.NotFound(Name, op, "uv could not resolve this tool from the package registry")
	case strings.Contains(text, "already installed"), strings.Contains(text, "is already installed"):
		return shell.Err(Name, op, provider.ErrConflict, "the tool is already installed through uv", nil)
	case strings.Contains(text, "executable already exists"):
		// uv will not overwrite a command another tool put in its bin directory.
		// The message names the file, which is the thing the user has to act on.
		return shell.Err(Name, op, provider.ErrConflict, shell.Diagnostic(res.Field()), nil)
	case strings.Contains(text, "connection"), strings.Contains(text, "network"), strings.Contains(text, "dns"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach the package registry", nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "uv exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

var _ provider.Privileged = (*Provider)(nil)

// Privilege classifies the provider for an elevated run: it runs as the
// invoking user.
func (p *Provider) Privilege(context.Context, exec.Runner, provider.PrivilegeTarget) provider.PrivilegeDecision {
	return shell.UserPrivilege("uv installs tools into the user's home (~/.local/share/uv/tools, ~/.local/bin)")
}
