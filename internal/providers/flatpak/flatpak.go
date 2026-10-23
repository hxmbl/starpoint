// Package flatpak implements the Flatpak provider for sandboxed desktop apps.
//
// Flatpak's model is unlike a package manager's in two ways that matter here.
// Its packages are application IDs (org.gimp.GIMP), not command names, and its
// installations are separate from the operating system's: a system installation
// under /var/lib/flatpak and a per-user one under ~/.local/share/flatpak.
//
// Starpoint manages the system installation, which is what `flatpak install`
// creates by default and the one that uses the system's configured remotes.
// Per-user installations are not enumerated or modified: every command here
// addresses the system installation, so an app recorded from the user
// installation could not be removed through the same path.
package flatpak

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
const Name = "flatpak"

// exportsDir is where the system installation exposes the commands apps
// provide, each named after the app that provides it.
const exportsDir = "/var/lib/flatpak/exports/bin"

// Provider implements provider.Provider for the system Flatpak installation.
type Provider struct {
	runner   exec.Runner
	platform func() bool
}

var (
	_ provider.Provider              = (*Provider)(nil)
	_ provider.ExecutableDirProvider = (*Provider)(nil)
	_ provider.Privileged            = (*Provider)(nil)
)

// New builds a Flatpak provider.
func New(runner exec.Runner) *Provider {
	return &Provider{runner: runner, platform: func() bool { return runtime.GOOS == "linux" }}
}

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "Flatpak (desktop apps)" }
func (p *Provider) Kind() provider.Kind { return provider.KindFlatpak }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover:     true,
		Install:      true,
		Remove:       true,
		Update:       true,
		Channels:     true, // a branch such as stable or beta: app//branch
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
		h.Reason = "Flatpak is only supported on Linux"
		return h
	}
	path, err := p.runner.LookPath("flatpak")
	if err != nil {
		h.Reason = "flatpak not found on PATH"
		return h
	}
	h.Binary = path
	res, err := p.runner.Run(ctx, "flatpak", "--version")
	if err != nil {
		h.Reason = err.Error()
		return h
	}
	if res.Failed() {
		h.Reason = shell.Diagnostic(res.Field())
		if h.Reason == "" {
			h.Reason = "flatpak exited with a non-zero status"
		}
		return h
	}
	h.Available = true
	h.Version = shell.VersionFromArgs(strings.Fields(res.Stdout)...)
	return h
}

// Discover lists applications in the system installation. Runtimes are not
// applications and are excluded: they exist to serve an app, not because the
// user asked for them.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	res, err := p.runner.Run(ctx, "flatpak", "list", "--system", "--app", "--columns=application,version")
	if err != nil {
		return nil, shell.Failure(p.Name(), "list", "could not list Flatpak applications", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "list", shell.Diagnostic(res.Field()), nil)
	}
	var out []model.Installation
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) == 0 || fields[0] == "" {
			continue
		}
		inst := model.Installation{
			Name:     fields[0],
			Provider: p.Name(),
			Scope:    model.ScopeGlobal,
		}
		if len(fields) > 1 {
			inst.Version = fields[1]
		}
		out = append(out, inst)
	}
	return out, nil
}

// Installed reports whether the system installation has this application.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	res, err := p.runner.Run(ctx, "flatpak", "list", "--system", "--app", "--columns=application,version")
	if err != nil {
		return nil, shell.Failure(p.Name(), "installed", "could not query Flatpak", err)
	}
	if res.Failed() {
		return nil, shell.Failure(p.Name(), "installed", shell.Diagnostic(res.Field()), nil)
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		fields := strings.Split(strings.TrimSpace(line), "\t")
		if len(fields) == 0 || fields[0] != pkg.Name {
			continue
		}
		inst := &model.Installation{
			Name:     fields[0],
			Provider: p.Name(),
			Scope:    model.ScopeGlobal,
		}
		if len(fields) > 1 {
			inst.Version = fields[1]
		}
		return inst, nil
	}
	return nil, nil
}

// Install installs a system application. A channel is Flatpak's branch, which
// selects a build line (stable, beta, ...) rather than a version.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	ref := req.Package.Name
	if req.Package.Channel != "" {
		ref += "//" + req.Package.Channel
	}
	if req.DryRun {
		return &model.Installation{Name: req.Package.Name, Provider: p.Name(), Scope: model.ScopeGlobal}, nil
	}
	res, err := p.runner.Run(ctx, "flatpak", "install", "--system", "--noninteractive", ref)
	if err != nil {
		return nil, shell.Failure(p.Name(), "install", "could not run flatpak install", err)
	}
	if res.Failed() {
		return nil, classify("install", res)
	}
	return p.Installed(ctx, req.Package)
}

// Remove uninstalls an application from the system installation.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	res, err := p.runner.Run(ctx, "flatpak", "uninstall", "--system", "--noninteractive", pkg.Name)
	if err != nil {
		return shell.Failure(p.Name(), "remove", "could not run flatpak uninstall", err)
	}
	if res.Failed() {
		return classify("uninstall", res)
	}
	return nil
}

// Update installs the newest build of an application's current branch.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	res, err := p.runner.Run(ctx, "flatpak", "update", "--system", "--noninteractive", pkg.Name)
	if err != nil {
		return nil, shell.Failure(p.Name(), "update", "could not run flatpak update", err)
	}
	if res.Failed() {
		return nil, classify("update", res)
	}
	return p.Installed(ctx, pkg)
}

// ExecutableDir reports where the system installation exports the commands its
// applications provide.
func (p *Provider) ExecutableDir(context.Context) string { return exportsDir }

func classify(op string, res exec.Result) error {
	text := strings.ToLower(res.Field())
	switch {
	case strings.Contains(text, "not installed"), strings.Contains(text, "no remote refs found"),
		strings.Contains(text, "nothing matches"), strings.Contains(text, "not found"):
		return shell.NotFound(Name, op, "Flatpak does not have this application in its configured remotes")
	case strings.Contains(text, "requires authentication"), strings.Contains(text, "permission denied"),
		strings.Contains(text, "not authorized"), strings.Contains(text, "you don't have permission"):
		return shell.Err(Name, op, provider.ErrPermission, "the system Flatpak installation needs root; re-run with elevated privileges", nil)
	case strings.Contains(text, "couldn't connect"), strings.Contains(text, "connection refused"),
		strings.Contains(text, "failed to fetch"), strings.Contains(text, "unable to download"),
		strings.Contains(text, "network is unreachable"):
		return shell.Err(Name, op, provider.ErrNetwork, "could not reach the Flatpak remotes", nil)
	case strings.Contains(text, "already installed"):
		return shell.Err(Name, op, provider.ErrConflict, "the application is already installed", nil)
	default:
		msg := shell.Diagnostic(res.Field())
		if msg == "" {
			msg = "flatpak exited nonzero"
		}
		return shell.Failure(Name, op, msg, nil)
	}
}

// Privilege classifies the provider for an elevated run: it manages the system
// installation, which is root's.
func (p *Provider) Privilege(context.Context, exec.Runner, provider.PrivilegeTarget) provider.PrivilegeDecision {
	return shell.RootPrivilege("flatpak manages the system installation under /var/lib/flatpak")
}

var _ provider.RequestedByConstruction = (*Provider)(nil)

// DiscoverListsOnlyRequested reports that Discover lists only what the user
// installed on purpose: no dependencies appear in it.
func (p *Provider) DiscoverListsOnlyRequested() bool { return true }
