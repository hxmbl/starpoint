// Package providers assembles the built-in provider set.
//
// Registration lives here rather than in the core so that the engine depends on
// the provider contract only. Adding an ecosystem means adding a package and one
// line in DefaultRegistry.
package providers

import (
	"os"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/apt"
	"github.com/hxmbl/starpoint/internal/providers/brew"
	"github.com/hxmbl/starpoint/internal/providers/cargo"
	"github.com/hxmbl/starpoint/internal/providers/dnf"
	"github.com/hxmbl/starpoint/internal/providers/gem"
	"github.com/hxmbl/starpoint/internal/providers/golang"
	"github.com/hxmbl/starpoint/internal/providers/npm"
	"github.com/hxmbl/starpoint/internal/providers/pacman"
	"github.com/hxmbl/starpoint/internal/providers/pip"
	"github.com/hxmbl/starpoint/internal/providers/pnpm"
	"github.com/hxmbl/starpoint/internal/providers/rustup"
	"github.com/hxmbl/starpoint/internal/providers/system"
	"github.com/hxmbl/starpoint/internal/providers/uv"
	"github.com/hxmbl/starpoint/internal/providers/yarn"
)

// DefaultRegistry builds the registry of providers implemented in this release.
//
// The set covers providers implemented by this release. Platform-specific
// providers remain registered everywhere; their health probes report unavailable
// when their native package manager is not supported on the host.
//
// Registration order is not cosmetic. The engine asks providers for an existing
// installation before falling back to path attribution, so the more specific
// owner of a piece of software has to come first: rustup is registered ahead of
// Cargo because `rustc` and `cargo` are rustup's, while `cargo install` is
// Cargo's.
//
// Two tools are deliberately absent. Docker and Podman manage containers,
// not software on this machine, and Bun and Deno cannot enumerate what they have
// installed — a provider that cannot answer "what do you own?" would make
// Starpoint's central promise false. Both are recorded in docs/next-tasks.md.
func DefaultRegistry() *provider.Registry {
	r := exec.RealRunner{}
	home, _ := os.UserHomeDir()
	reg := provider.NewRegistry()
	reg.Register(system.New(r))
	reg.Register(brew.New(r))
	reg.Register(apt.New(r))
	reg.Register(dnf.New(r))
	reg.Register(pacman.New(r))
	reg.Register(uv.New(r, home))
	reg.Register(pip.New(r))
	reg.Register(rustup.New(r, home))
	reg.Register(cargo.New(r, home))
	reg.Register(npm.New(r))
	reg.Register(pnpm.New(r))
	reg.Register(yarn.New(r))
	reg.Register(gem.New(r))
	reg.Register(golang.New(r, home))
	return reg
}
