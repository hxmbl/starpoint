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

	"github.com/hxmbl/starpoint/internal/model"
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
	// Verify means the provider can verify an installation by running the
	// installed executable (e.g., `ruff --version`) in addition to checking inventory.
	Verify bool
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

// Verifier is an optional provider capability for executable self-verification.
// Keeping it optional avoids requiring package-only providers to pretend their
// package identifiers are runnable commands.
type Verifier interface {
	Verify(ctx context.Context, pkg model.Package) error
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

// ExecutableMappingProvider is an optional interface that providers can implement
// to declare which executables their packages supply. This mapping is used for
// ownership attribution: when Starpoint finds an executable on PATH, it can
// determine which package and provider owns it.
type ExecutableMappingProvider interface {
	Provider
	// ExecutableToPackage returns a mapping from executable names to package names.
	// The key is the executable name (e.g., "git", "rustc") and the value is the
	// package name as known to this provider (e.g., "git", "rust"). Providers
	// that cannot provide this mapping may return nil or an empty map.
	ExecutableToPackage() map[string]string
}

// PackageExecutableProvider optionally maps an installed package back to the
// command it supplies. Providers use this to avoid treating package identifiers
// as executable names during self-verification.
type PackageExecutableProvider interface {
	ExecutableForPackage(packageName string) (string, bool)
}

// ExecutableDirProvider is implemented by providers that expose the commands
// they install in one known directory, such as uv's tool bin directory or pip's
// user-base bin directory.
//
// It exists because two providers can write the same file. uv and pip both put
// console scripts in ~/.local/bin on Linux, so moving a tool between them is not
// "install the new copy, then remove the old one": the install overwrites the
// old copy's executable and the removal then deletes the new one. Knowing where
// a provider will put an executable is what lets the core see that coming.
//
// An empty string means the provider cannot say.
type ExecutableDirProvider interface {
	ExecutableDir(ctx context.Context) string
}

// StateDirProvider is implemented by providers that keep per-user state in
// known directories away from the paths they report: Cargo's registry and
// install records under CARGO_HOME, Go's module and build caches, rustup's
// toolchains, uv's tool environments and cache.
//
// Starpoint uses these only when it runs elevated on behalf of the user who
// typed sudo or doas: whatever the provider wrote there as root is given back
// to that user, so their next ordinary run of the same tool is not locked
// out of its own state. They are never watched or scanned otherwise.
type StateDirProvider interface {
	StateDirs(ctx context.Context) []string
}

// LatestVersionProvider is implemented by providers that can say which version
// of a package they would install now. `update` uses it to tell a pin that is
// merely satisfied from a pin that is holding a package back.
//
// An empty version with a nil error means the provider does not know the
// package.
type LatestVersionProvider interface {
	LatestVersion(ctx context.Context, pkg model.Package) (string, error)
}

// RequestedReporter is implemented by providers that can tell software the
// user asked for from software installed only as a dependency: apt's manual
// marks, dnf's user-installed reason, pacman's explicit installs, Homebrew's
// installed-on-request.
//
// Requested is given what Discover reported and answers for those names:
// true for requested, false for a dependency. A name left out of the map is
// unknown. That distinction is the point: `adopt --all` takes only what is
// known to be requested, so a provider that cannot say must not guess.
type RequestedReporter interface {
	Requested(ctx context.Context, installed []model.Installation) (map[string]bool, error)
}

// RequestedByConstruction is implemented by providers whose Discover lists
// only what the user installed on purpose, because the tool keeps no
// dependencies in that list: uv's tools, `cargo install`, global npm and
// pnpm packages at depth 0, Go's installed binaries, rustup's toolchains,
// the Nix profile, Flatpak applications (runtimes are not listed).
type RequestedByConstruction interface {
	DiscoverListsOnlyRequested() bool
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
	// HasExecutableMapping is true when the provider implements ExecutableMappingProvider.
	HasExecutableMapping bool `json:"hasExecutableMapping,omitempty"`
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
		h := p.Health(ctx)
		_, isContextAware := p.(ContextAwareProvider)
		h.ContextAware = isContextAware
		_, hasExecutableMapping := p.(ExecutableMappingProvider)
		h.HasExecutableMapping = hasExecutableMapping
		out = append(out, h)
	}
	return out
}
