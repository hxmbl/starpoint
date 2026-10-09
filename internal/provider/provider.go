// Package provider defines the contract every package manager integration
// implements, plus the registry and health model used by the rest of Starpoint.
//
// The contract is intentionally not a lowest common denominator. Providers
// advertise what they can do through Capabilities and report their own errors
// through typed Error values, so the core never has to pretend that installing a
// Python tool, a Rust binary and a Homebrew formula are the same operation.
package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/starpoint/starpoint/internal/model"
)

// Kind groups providers by the world they operate in. It exists for reporting
// and for resolution heuristics, not to make them interchangeable.
type Kind string

const (
	KindSystem  Kind = "system"
	KindBrew    Kind = "system-brew"
	KindApt     Kind = "system-apt"
	KindDnf     Kind = "system-dnf"
	KindPacman  Kind = "system-pacman"
	KindNix     Kind = "system-nix"
	KindWinget  Kind = "system-winget"
	KindChoco   Kind = "system-choco"
	KindPython  Kind = "language-python"
	KindRust    Kind = "language-rust"
	KindNode    Kind = "language-node"
	KindGo      Kind = "language-go"
	KindRuby    Kind = "language-ruby"
	KindPHP     Kind = "language-php"
	KindDotnet  Kind = "language-dotnet"
	KindJVM     Kind = "language-jvm"
	KindSwift   Kind = "language-swift"
	KindCocoa   Kind = "language-cocoapods"
	KindVcpkg   Kind = "language-vcpkg"
	KindConda   Kind = "language-conda"
	KindFlatpak Kind = "app-flatpak"
	KindSnap    Kind = "app-snap"
)

// Capabilities describes what a provider can actually do. The core consults
// these instead of branching on provider names wherever possible.
type Capabilities struct {
	// Discover means the provider can enumerate what it has installed.
	Discover bool
	// Install means the provider can install software from a name.
	Install bool
	// Remove means the provider can uninstall.
	Remove bool
	// Update means the provider can upgrade already-installed software.
	Update bool
	// PinVersion means the provider can install a specific version on request.
	PinVersion bool
	// Channels means the provider understands a channel/track selector.
	Channels bool
	// Search means the provider can look up candidate package names.
	Search bool
	// ProjectInstall means the provider can install relative to a directory.
	ProjectInstall bool
	// CanInstallInContext means the provider supports context-aware installation,
	// i.e., it can install packages scoped to a specific project directory.
	CanInstallInContext bool
	// DefaultScope is where this provider places software when the user does
	// not care.
	DefaultScope model.Scope
}

// ErrorKind classifies provider failures so the engine can decide whether to
// retry, warn, or abort.
type ErrorKind string

const (
	// ErrNotFound means the package does not exist for this provider.
	ErrNotFound ErrorKind = "not-found"
	// ErrUnsupported means the provider cannot perform the requested operation.
	ErrUnsupported ErrorKind = "unsupported"
	// ErrPermission means the provider needs elevation Starpoint did not have.
	ErrPermission ErrorKind = "permission"
	// ErrNetwork means the provider could not reach its index.
	ErrNetwork ErrorKind = "network"
	// ErrConflict means the operation conflicted with existing state.
	ErrConflict ErrorKind = "conflict"
	// ErrFailed is any other failure.
	ErrFailed ErrorKind = "failed"
)

// Error is the single error type providers return. Keeping it typed means the
// core can present a useful diagnosis without string matching on subprocess
// output.
type Error struct {
	Provider string
	Op       string
	Kind     ErrorKind
	Message  string
	Err      error
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Provider)
	if e.Op != "" {
		fmt.Fprintf(&b, " %s", e.Op)
	}
	if e.Message != "" {
		fmt.Fprintf(&b, ": %s", e.Message)
	} else if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// IsNotFound reports whether err is a provider not-found error.
func IsNotFound(err error) bool {
	var pe *Error
	if as(err, &pe) {
		return pe.Kind == ErrNotFound
	}
	return false
}

// AsProviderError extracts a *Error from err, if present.
func AsProviderError(err error) (*Error, bool) {
	var pe *Error
	if as(err, &pe) {
		return pe, true
	}
	return nil, false
}

func as(err error, target **Error) bool {
	for err != nil {
		if pe, ok := err.(*Error); ok {
			*target = pe
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// Request is what the core asks a provider to do.
type Request struct {
	// Package is the desired entry, including explicit version/channel pins.
	Package model.Package
	// Scope is the placement the core wants, if constrained.
	Scope model.Scope
	// Dir is the project directory for project-scoped installs.
	Dir string
	// DryRun asks the provider to report what it would do without doing it.
	DryRun bool
}

// Version returns the explicit version pin, or "" when unconstrained.
func (r Request) Version() string { return r.Package.Version }

// Provider is the integration contract. Implementations encapsulate every
// provider-specific assumption: how to ask what is installed, how to install,
// how to report versions, and what its limitations are.
type Provider interface {
	// Name is the stable identifier persisted in configuration.
	Name() string
	// Description is a human label for doctor output.
	Description() string
	// Kind categorizes the ecosystem.
	Kind() Kind
	// Capabilities reports what this provider supports.
	Capabilities() Capabilities
	// Health reports whether the provider is usable right now.
	Health(ctx context.Context) Health
	// Discover lists what the provider currently has installed. Providers that
	// cannot enumerate return an unsupported error.
	Discover(ctx context.Context) ([]model.Installation, error)
	// Installed reports a single installation, or nil if the provider does not
	// have it. It must not fail on absence.
	Installed(ctx context.Context, pkg model.Package) (*model.Installation, error)
	// Install installs the package, honouring version and channel when the
	// provider's capabilities allow it.
	Install(ctx context.Context, req Request) (*model.Installation, error)
	// Remove uninstalls the package.
	Remove(ctx context.Context, pkg model.Package) error
	// Update upgrades the package. Providers without update support return an
	// unsupported error.
	Update(ctx context.Context, pkg model.Package) (*model.Installation, error)
}

// ContextAwareProvider is an optional interface that providers can implement
// to support context-aware installation. When a provider implements this
// interface, the core can install packages scoped to a specific project
// directory (e.g., npm for node_modules, cargo for target, go for go.mod).
type ContextAwareProvider interface {
	Provider
	// InstallWithContext installs the package in the context of the given
	// directory. The contextDir is typically a project root where the provider
	// should scope its installation (e.g., npm install in a node project).
	// Providers that do not support context-aware installation should return
	// an unsupported error.
	InstallWithContext(ctx context.Context, pkg model.Package, contextDir string) (*model.Installation, error)
}

// Health is the result of probing a provider.
type Health struct {
	// Name is the provider name.
	Name string `json:"name"`
	// Description is the human label.
	Description string `json:"description"`
	// Kind is the ecosystem category.
	Kind Kind `json:"kind"`
	// Available is true when the provider's own tooling was found.
	Available bool `json:"available"`
	// Reason explains unavailability.
	Reason string `json:"reason,omitempty"`
	// Binary is the resolved path to the provider's own executable.
	Binary string `json:"binary,omitempty"`
	// Version is the provider tool's own version, when reported.
	Version string `json:"version,omitempty"`
	// Capabilities is included so doctor and JSON consumers see the same
	// information the resolver used.
	Capabilities Capabilities `json:"capabilities"`
	// ContextAware is true when the provider implements ContextAwareProvider.
	ContextAware bool `json:"contextAware,omitempty"`
}

// Registry holds the known providers.
type Registry struct {
	order     []string
	providers map[string]Provider
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

// Register adds a provider. Later registrations of the same name replace
// earlier ones, which keeps tests able to substitute fakes.
func (r *Registry) Register(p Provider) {
	if _, seen := r.providers[p.Name()]; !seen {
		r.order = append(r.order, p.Name())
	}
	r.providers[p.Name()] = p
}

// Get returns a provider by name.
func (r *Registry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// All returns every provider in registration order.
func (r *Registry) All() []Provider {
	out := make([]Provider, 0, len(r.order))
	for _, n := range r.order {
		out = append(out, r.providers[n])
	}
	return out
}

// Names returns registered provider names in registration order.
func (r *Registry) Names() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// Available returns the providers whose tooling is present. Health failures for
// individual providers are tolerated: a broken provider must not stop the rest
// of the system from working.
func (r *Registry) Available(ctx context.Context) []Health {
	var out []Health
	for _, p := range r.All() {
		out = append(out, p.Health(ctx))
	}
	return out
}
