// Package providers assembles the built-in provider set.
//
// Registration lives here rather than in the core so that the engine depends on
// the provider contract only. Adding an ecosystem means adding a package and one
// line in DefaultRegistry.
package providers

import (
	"os"

	"github.com/starpoint/starpoint/internal/exec"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/apt"
	"github.com/starpoint/starpoint/internal/providers/brew"
	"github.com/starpoint/starpoint/internal/providers/cargo"
	"github.com/starpoint/starpoint/internal/providers/golang"
	"github.com/starpoint/starpoint/internal/providers/npm"
	"github.com/starpoint/starpoint/internal/providers/system"
	"github.com/starpoint/starpoint/internal/providers/uv"
)

// DefaultRegistry builds the registry of providers implemented in this release.
// The set is chosen to be genuinely usable rather than exhaustive: the operating
// system, the two dominant system package managers, and the four language
// ecosystems with the clearest installation story on the host platforms.
func DefaultRegistry() *provider.Registry {
	r := exec.RealRunner{}
	home, _ := os.UserHomeDir()
	reg := provider.NewRegistry()
	reg.Register(system.New(r))
	reg.Register(brew.New(r))
	reg.Register(apt.New(r))
	reg.Register(uv.New(r, home))
	reg.Register(cargo.New(r, home))
	reg.Register(npm.New(r))
	reg.Register(golang.New(r, home))
	return reg
}
