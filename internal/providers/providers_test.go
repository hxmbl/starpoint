package providers

import (
	"testing"

	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/resolve"
)

// The registry is where Starpoint's decisions about the machine are actually
// encoded, and two of its properties are load-bearing rather than incidental.
// Both are asserted here because neither fails loudly: a provider in the wrong
// order still works, and a resolver table naming a provider that does not exist
// still produces a plan — just one Starpoint cannot carry out.

func TestDefaultRegistryCoversEveryPackageManagerOnTheHost(t *testing.T) {
	reg := DefaultRegistry()
	// The compatibility target is the machine this was developed on. Losing one
	// of these would mean the resolver can no longer see software it used to see.
	want := []string{
		"system", "brew", "apt",
		"uv", "pip",
		"rustup", "cargo",
		"npm", "pnpm", "yarn",
		"gem",
		"go",
	}
	for _, name := range want {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("provider %q is missing from the registry", name)
		}
	}
	if got := len(reg.All()); got != len(want) {
		t.Errorf("expected %d providers, found %d", len(want), got)
	}
}

// Registration order decides which provider gets asked first for an existing
// installation. rustup owns `rustc` and `cargo`; `cargo install` is Cargo's. If
// Cargo were asked first it would answer "no" for a toolchain it never installed,
// and ownership would fall through to path attribution.
func TestRustupIsRegisteredBeforeCargo(t *testing.T) {
	reg := DefaultRegistry()
	rustupAt, cargoAt := -1, -1
	for i, p := range reg.All() {
		switch p.Name() {
		case "rustup":
			rustupAt = i
		case "cargo":
			cargoAt = i
		}
	}
	if rustupAt < 0 || cargoAt < 0 {
		t.Fatalf("both providers must be registered: rustup=%d cargo=%d", rustupAt, cargoAt)
	}
	if rustupAt > cargoAt {
		t.Errorf("rustup is at %d and cargo at %d; rustup must come first", rustupAt, cargoAt)
	}
}

// The read-only system provider matches almost every command on a Unix machine,
// so it must come after the providers that actually manage software.
func TestSystemIsNotAskedBeforeManagingProviders(t *testing.T) {
	reg := DefaultRegistry()
	all := reg.All()
	systemAt := -1
	for i, p := range all {
		if p.Name() == "system" {
			systemAt = i
		}
	}
	if systemAt < 0 {
		t.Fatal("the system provider must be registered")
	}
	for _, p := range all[:systemAt] {
		if p.Capabilities().Install {
			t.Errorf("%q can install and is registered before system", p.Name())
		}
	}
}

// A resolver table naming a provider this build does not have would produce a
// resolution Starpoint cannot carry out — the failure mode this table once had,
// when it referred to dnf, nix, winget and choco.
func TestResolverNamesOnlyProvidersThatExist(t *testing.T) {
	reg := DefaultRegistry()
	resolver := resolve.New(reg)
	for _, name := range resolver.KnownProviders() {
		if _, ok := reg.Get(name); !ok {
			t.Errorf("the resolver names %q but it is not registered", name)
		}
	}
}

// Every registered provider must describe itself. A provider that answers an
// empty name or empty description cannot be reported by `doctor`, which is the
// only way a user finds out what Starpoint can see.
func TestEveryProviderDescribesItself(t *testing.T) {
	for _, p := range DefaultRegistry().All() {
		if p.Name() == "" {
			t.Error("a provider has no name")
		}
		if p.Description() == "" {
			t.Errorf("%s has no description", p.Name())
		}
		if p.Kind() == "" {
			t.Errorf("%s has no kind", p.Name())
		}
		var zero provider.Capabilities
		if p.Capabilities() == zero {
			t.Errorf("%s claims no capabilities at all", p.Name())
		}
	}
}

// Providers must be distinct: two registrations of the same name would make
// whichever came last silently win everywhere.
func TestProviderNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range DefaultRegistry().All() {
		if seen[p.Name()] {
			t.Errorf("%q is registered twice", p.Name())
		}
		seen[p.Name()] = true
	}
}
