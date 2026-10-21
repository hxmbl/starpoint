// Package snap implements the Snap provider for Ubuntu-style snaps.
//
// snapd installs and runs applications system-wide, and `snap` talks to that
// daemon rather than managing files itself. The inventory is therefore the
// daemon's list, reported in columns padded with spaces, and a snap's name is
// not always the command it provides (`lxd` installs `lxc`): Starpoint records
// the snap, and PATH attribution decides which command came from it.
package snap

import (
	"context"
	"runtime"
	"strings"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/shell"
)

// Name is the provider identifier persisted in configuration.
const Name = "snap"

// binDir is where snapd links the commands snaps provide.
const binDir = "/snap/bin"

// Provider implements provider.Provider for snaps.
type Provider struct {
	runner   exec.Runner
	platform func() bool
}

var (
	_ provider.Provider              = (*Provider)(nil)
	_ provider.ExecutableDirProvider = (*Provider)(nil)
	_ provider.Privileged            = (*Provider)(nil)
)

// New builds a Snap provider.
func New(runner exec.Runner) *Provider {
	return &Provider{runner: runner, platform: func() bool { return runtime.GOOS == "linux" }}
}

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "Snap (Ubuntu snaps)" }
func (p *Provider) Kind() provider.Kind { return provider.KindSnap }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
		Channels:     true, // stable, candidate, beta, edge
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
	if p.platform != nil && !p.platform() {
		h.Reason = "Snap is only supported on Linux"
		return h
	}
	path, err := p.runner.LookPath("snap")
	if err != nil {
		h.Reason = "snap not found on PATH"
		return h
	}
	h.Binary = path
	res, err := p.runner.Run(ctx, "snap", "version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	if res.Failed() {
		// `snap` can be installed without a working snapd (containers, WSL).
		// Reporting it as available would make every later command a surprise.
		h.Reason = shell.Diagnostic(res.Field())
		if h.Reason == "" {
			h.Reason = "snap exited with a non-zero status"
		}
		return h
	}
	h.Available = true
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := shell.Fields(line)
		if len(fields) >= 2 && fields[0] == "snap" {
			h.Version = fields[1]
			break
		}
	}
	return h
}

// Discover lists installed snaps. `snap list` pads its columns with spaces and
// has no machine-readable mode, so the first two fields are the only ones that
// can be read without depending on column widths: the name and the version.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	res, err := p.runner.Run(ctx, "snap", "list")
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not list snaps", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "list", shell.Diagnostic(res.Field()), nil)
	}
	var out []model.Installation
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := shell.Fields(line)
		// The header names the first column "Name"; every other row starts
		// with the snap's own name.
		if len(fields) < 2 || fields[0] == "Name" {
			continue
		}
		out = append(out, model.Installation{
			Name:     fields[0],
			Version:  fields[1],
			Provider: p.Name(),
			Scope:    model.ScopeGlobal,
		})
	}
	return out, nil
}

// Installed reports whether a snap is installed.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	res, err := p.runner.Run(ctx, "snap", "list", pkg.Name)
	if err != nil {
		return nil, shell.Failure(p.Name(), "installed", "could not query snap", err)
	}
	if res.Failed() {
		if strings.Contains(strings.ToLower(res.Field()), "no matching snaps installed") {
			return nil, nil
		}
		return nil, shell.Failure(p.Name(), "installed", shell.Diagnostic(res.Field()), nil)
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := shell.Fields(line)
		if len(fields) < 2 || fields[0] != pkg.Name {
			continue
		}
		return &model.Installation{
			Name:     fields[0],
			Version:  fields[1],
			Provider: p.Name(),
			Scope:    model.ScopeGlobal,
		}, nil
	}
	return nil, nil
}

// Install installs a snap, honouring a channel when one is configured.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	args := []string{"install"}
	if req.Package.Channel != "" {
		args = append(args, "--channel="+req.Package.Channel)
	}
	args = append(args, req.Package.Name)
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeGlobal}, nil
	}
	res, err := p.runner.Run(ctx, "snap", args...)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run snap install", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls a snap.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	res, err := p.runner.Run(ctx, "snap", "remove", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run snap remove", err)
	}
	if res.Failed() {
		return classify("remove", res)
	}
	return nil
}

// Update refreshes a snap to the newest revision on its channel.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	res, err := p.runner.Run(ctx, "snap", "refresh", pkg.Name)
	if err != nil {
		return nil, shell.Failure(p.Name(), "refresh", "could not run snap refresh", err)
	}
	if res.Failed() {
		return nil, classify("refresh", res)
	}
	return p.Installed(ctx, pkg)
}

// ExecutableDir reports where snapd links the commands snaps provide.
func (p *Provider) ExecutableDir(context.Context) string { return binDir }

func classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "classic confinement"):
		// Granting classic confinement gives a snap unrestricted access to the
		// machine. That is the user's decision to make explicitly, not a flag
		// Starpoint adds because the install failed.
		return shell.Err(Name, op, provider.ErrConflict,
			"this snap needs classic confinement; Starpoint will not grant it implicitly", nil)
	case strings.Contains(text, "not found"), strings.Contains(text, "no snap found"),
		strings.Contains(text, "has no install candidate"):
		return shell.NotFound(Name, op, "the snap store does not have this snap")
	case strings.Contains(text, "access denied"), strings.Contains(text, "permission denied"),
		strings.Contains(text, "authentication required"), strings.Contains(text, "requires sudo"):
		return shell.Err(Name, op, provider.ErrPermission, "snapd needs root; re-run with elevated privileges", nil)
	case strings.Contains(text, "cannot communicate with server"), strings.Contains(text, "connection refused"),
		strings.Contains(text, "network is unreachable"), strings.Contains(text, "cannot resolve"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach snapd or the snap store", nil)
	case strings.Contains(text, "already installed"):
		return shell.Err(Name, op, provider.ErrConflict, "the snap is already installed", nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "snap exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// Privilege classifies the provider for an elevated run: snaps are installed
// system-wide by snapd, which needs root.
func (p *Provider) Privilege(context.Context, exec.Runner, provider.PrivilegeTarget) provider.PrivilegeDecision {
	return shell.RootPrivilege("snap installs system-wide through snapd")
}
