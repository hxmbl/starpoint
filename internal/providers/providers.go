// Package providers assembles the built-in provider set.
//
// Registration lives here rather than in the core so that the engine depends on
// the provider contract only. Adding an ecosystem means adding a package and one
// line in DefaultRegistry.
package providers

import (
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/apt"
	"github.com/hxmbl/starpoint/internal/providers/brew"
	"github.com/hxmbl/starpoint/internal/providers/cargo"
	"github.com/hxmbl/starpoint/internal/providers/dnf"
	"github.com/hxmbl/starpoint/internal/providers/flatpak"
	"github.com/hxmbl/starpoint/internal/providers/gem"
	"github.com/hxmbl/starpoint/internal/providers/golang"
	"github.com/hxmbl/starpoint/internal/providers/nix"
	"github.com/hxmbl/starpoint/internal/providers/npm"
	"github.com/hxmbl/starpoint/internal/providers/pacman"
	"github.com/hxmbl/starpoint/internal/providers/pip"
	"github.com/hxmbl/starpoint/internal/providers/pnpm"
	"github.com/hxmbl/starpoint/internal/providers/rustup"
	"github.com/hxmbl/starpoint/internal/providers/snap"
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
// Starpoint's central promise false.
func DefaultRegistry() *provider.Registry { return New(nil) }

// New builds the built-in registry for a run whose privileges pv describes.
// A nil pv, or one without an Identity, runs every provider as Starpoint
// itself. With an Identity (an elevated run for an invoking user), each
// provider's commands run as the account its classification names: see
// Privileges.
func New(pv *Privileges) *provider.Registry {
	home := pv.home()
	reg := provider.NewRegistry()
	var (
		sysP    *system.Provider
		brewP   *brew.Provider
		aptP    *apt.Provider
		dnfP    *dnf.Provider
		pacmanP *pacman.Provider
		nixP    *nix.Provider
		uvP     *uv.Provider
		pipP    *pip.Provider
		rustupP *rustup.Provider
		cargoP  *cargo.Provider
		npmP    *npm.Provider
		pnpmP   *pnpm.Provider
		yarnP   *yarn.Provider
		gemP    *gem.Provider
		goP     *golang.Provider
		fpP     *flatpak.Provider
		snapP   *snap.Provider
	)
	// Each runner asks for its provider lazily, because the provider is
	// built with the runner.
	sysP = system.New(pv.Runner(func() provider.Provider { return sysP }))
	brewP = brew.New(pv.Runner(func() provider.Provider { return brewP }))
	aptP = apt.New(pv.Runner(func() provider.Provider { return aptP }))
	dnfP = dnf.New(pv.Runner(func() provider.Provider { return dnfP }))
	pacmanP = pacman.New(pv.Runner(func() provider.Provider { return pacmanP }))
	nixP = nix.New(pv.Runner(func() provider.Provider { return nixP }), home)
	uvP = uv.New(pv.Runner(func() provider.Provider { return uvP }), home)
	pipP = pip.New(pv.Runner(func() provider.Provider { return pipP }))
	rustupP = rustup.New(pv.Runner(func() provider.Provider { return rustupP }), home)
	cargoP = cargo.New(pv.Runner(func() provider.Provider { return cargoP }), home)
	npmP = npm.New(pv.Runner(func() provider.Provider { return npmP }))
	pnpmP = pnpm.New(pv.Runner(func() provider.Provider { return pnpmP }))
	yarnP = yarn.New(pv.Runner(func() provider.Provider { return yarnP }))
	gemP = gem.New(pv.Runner(func() provider.Provider { return gemP }))
	goP = golang.New(pv.Runner(func() provider.Provider { return goP }), home)
	fpP = flatpak.New(pv.Runner(func() provider.Provider { return fpP }))
	snapP = snap.New(pv.Runner(func() provider.Provider { return snapP }))
	for _, p := range []provider.Provider{sysP, brewP, aptP, dnfP, pacmanP, nixP, uvP, pipP, rustupP, cargoP, npmP, pnpmP, yarnP, gemP, goP, fpP, snapP} {
		reg.Register(p)
	}
	return reg
}
