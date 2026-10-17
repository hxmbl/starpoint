// Package system implements the "system" provider: software that the operating
// system or a base layer already provides.
//
// This provider exists because software ownership is not only about who can
// install something. Knowing that /usr/bin/git came with the OS, that it is
// therefore not Homebrew's to update, and that removing it would be destructive,
// is exactly the kind of fact Starpoint needs to make correct decisions. It
// therefore advertises no install, remove or update capability at all: the
// provider is read-only by design.
package system

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
const Name = "system"

// systemDirs are the directories the OS owns. Anything found here is attributed
// to the system provider rather than to whichever package manager happens to be
// installed on the machine.
var systemDirs = map[string][]string{
	"darwin":  {"/bin", "/sbin", "/usr/bin", "/usr/sbin"},
	"linux":   {"/bin", "/sbin", "/usr/bin", "/usr/sbin"},
	"freebsd": {"/bin", "/sbin", "/usr/bin", "/usr/sbin"},
}

// Provider implements the read-only system provider.
type Provider struct {
	runner exec.Runner
}

var _ provider.Provider = (*Provider)(nil)

// New builds a system provider.
func New(runner exec.Runner) *Provider { return &Provider{runner: runner} }

func (p *Provider) Name() string        { return Name }
func (p *Provider) Description() string { return "Operating system (preinstalled)" }
func (p *Provider) Kind() provider.Kind { return provider.KindSystem }

func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		Discover: true,
		// Install, Remove and Update are deliberately false. The system provider
		// is read-only: Starpoint must not pretend it can replace a component of
		// the operating system.
		DefaultScope: model.ScopeGlobal,
	}
}

func (p *Provider) Health(ctx context.Context) provider.Health {
	return provider.Health{
		Name:         p.Name(),
		Description:  p.Description(),
		Kind:         p.Kind(),
		Available:    len(Dirs()) > 0,
		Capabilities: p.Capabilities(),
		Reason:       reason(),
	}
}

func reason() string {
	if len(Dirs()) == 0 {
		return "no recognized system directories on this platform"
	}
	return "read-only provider for " + runtime.GOOS
}

// Dirs returns the system-owned binary directories for the current platform.
func Dirs() []string { return systemDirs[runtime.GOOS] }

// Discover lists executables in the system directories.
func (p *Provider) Discover(ctx context.Context) ([]model.Installation, error) {
	var out []model.Installation
	for _, dir := range Dirs() {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
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
				Scope:    model.ScopeGlobal,
				Path:     filepath.Join(dir, e.Name()),
			})
		}
	}
	return out, nil
}

// Installed resolves a name within the system directories, ignoring PATH. Looking
// only at the system directories is what makes this provider trustworthy: a
// Homebrew git in /usr/local/bin must not be reported as system-owned.
func (p *Provider) Installed(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	for _, dir := range Dirs() {
		candidate := filepath.Join(dir, pkg.Name)
		info, err := os.Stat(candidate)
		if err != nil || info.IsDir() || info.Mode()&0o111 == 0 {
			continue
		}
		return &model.Installation{
			Name:     pkg.Name,
			Provider: p.Name(),
			Scope:    model.ScopeGlobal,
			Path:     candidate,
		}, nil
	}
	return nil, nil
}

// Install is not supported by this provider.
func (p *Provider) Install(ctx context.Context, req provider.Request) (*model.Installation, error) {
	return nil, shell.Unsupported(p.Name(), "install",
		"the operating system provider is read-only; use a package manager such as brew or apt")
}

// Remove is not supported by this provider.
func (p *Provider) Remove(ctx context.Context, pkg model.Package) error {
	return shell.Unsupported(p.Name(), "remove",
		"removing software owned by the operating system would be destructive; use the system's own tools if this is really intended")
}

// Update is not supported by this provider.
func (p *Provider) Update(ctx context.Context, pkg model.Package) (*model.Installation, error) {
	return nil, shell.Unsupported(p.Name(), "upgrade",
		"the operating system provider is read-only; update the system through its own update mechanism")
}

// Verify is not supported by this provider.
func (p *Provider) Verify(ctx context.Context, pkg model.Package) error {
	return shell.Unsupported(p.Name(), "verify",
		"the operating system provider is read-only; verification is not applicable")
}

// OwnsPath reports whether a filesystem path lies inside a system directory. It
// is used to attribute an arbitrary executable to its true owner.
func OwnsPath(path string) bool {
	if path == "" {
		return false
	}
	for _, dir := range Dirs() {
		if path == dir || strings.HasPrefix(path, dir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

var _ provider.Privileged = (*Provider)(nil)

// Privilege classifies the provider for an elevated run: it runs as root.
func (p *Provider) Privilege(context.Context, exec.Runner, provider.PrivilegeTarget) provider.PrivilegeDecision {
	return shell.RootPrivilege("the operating system's own software is read-only to Starpoint; its inventory is read as root")
}
