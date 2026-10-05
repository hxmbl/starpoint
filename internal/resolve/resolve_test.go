package resolve

import (
	"testing"

	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
	"github.com/starpoint/starpoint/internal/providers/brew"
	"github.com/starpoint/starpoint/internal/providers/cargo"
	"github.com/starpoint/starpoint/internal/providers/npm"
	"github.com/starpoint/starpoint/internal/providers/system"
	"github.com/starpoint/starpoint/internal/providers/uv"
	"github.com/starpoint/starpoint/internal/testsupport"
)

// buildRegistry wires fake providers whose capabilities mirror the real ones.
func buildRegistry(t *testing.T, specs ...spec) (*provider.Registry, map[string]provider.Health) {
	t.Helper()
	reg := provider.NewRegistry()
	health := map[string]provider.Health{}
	for _, s := range specs {
		p := testsupport.NewFakeProvider(s.name, s.kind)
		p.Caps = s.caps
		p.IsAvailable = s.available
		reg.Register(p)
		health[s.name] = p.Health(t.Context())
	}
	return reg, health
}

type spec struct {
	name      string
	kind      provider.Kind
	caps      provider.Capabilities
	available bool
}

func fullCaps(scope model.Scope) provider.Capabilities {
	return provider.Capabilities{Discover: true, Install: true, Remove: true, Update: true, PinVersion: true, DefaultScope: scope}
}

func systemCaps() provider.Capabilities {
	return provider.Capabilities{Discover: true, DefaultScope: model.ScopeGlobal}
}

func TestExplicitProviderInConfigWins(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: uv.Name, kind: provider.KindPython, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "ruff", Provider: uv.Name},
		Available: health,
	})
	if got.Provider != uv.Name {
		t.Fatalf("explicit provider must win, got %q", got.Provider)
	}
	if !got.Forced {
		t.Error("an explicit choice must be marked as forced")
	}
	if got.Ambiguous {
		t.Error("an explicit choice is never ambiguous")
	}
}

func TestExplicitProviderIsKeptEvenWhenUnavailable(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: uv.Name, kind: provider.KindPython, caps: fullCaps(model.ScopeUser), available: false},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "ruff", Provider: uv.Name},
		Available: health,
	})
	if got.Provider != uv.Name {
		t.Fatalf("a forced provider must not be silently replaced, got %q", got.Provider)
	}
	if !got.Forced {
		t.Error("must remain marked as forced so the engine reports the conflict")
	}
}

func TestExistingOwnershipIsNotStolen(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: uv.Name, kind: provider.KindPython, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	// ruff is a Python tool so uv would win on preference alone, but it is
	// already installed through brew and re-installing under uv would leave two
	// copies on the machine.
	got := r.Resolve(Input{
		Package:   model.Package{Name: "ruff"},
		Existing:  &model.Installation{Name: "ruff", Provider: brew.Name, Path: "/opt/homebrew/bin/ruff"},
		Available: health,
	})
	if got.Provider != brew.Name {
		t.Fatalf("existing owner must keep the package, got %q", got.Provider)
	}
	if got.Confidence != model.ConfidenceCertain {
		t.Errorf("keeping the existing owner should be certain, got %q", got.Confidence)
	}
}

func TestEcosystemNativeProviderPreferredForNewSoftware(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: uv.Name, kind: provider.KindPython, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	// The configuration asks for uv and nothing is installed anywhere.
	got := r.Resolve(Input{
		Package: model.Package{Name: "python"},
		// No host PATH: resolution must be decided by the fake providers alone.
		Locator:   nil,
		Available: health,
	})
	if got.Provider != uv.Name {
		t.Fatalf("python should be provided by uv when uv is available, got %q (reason: %s)", got.Provider, got.Reason)
	}
}

func TestSingleSystemManagerIsNotAmbiguous(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "ripgrep"},
		Available: health,
		// No host PATH: resolution must be decided by the fake providers alone.
		Locator: nil,
	})
	if got.Provider != brew.Name {
		t.Fatalf("expected brew, got %q", got.Provider)
	}
	if got.Ambiguous {
		t.Error("a single available package manager is not ambiguous")
	}
}

func TestTwoSystemManagersAreAmbiguous(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: "apt", kind: provider.KindApt, caps: fullCaps(model.ScopeGlobal), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "ripgrep"},
		Available: health,
		// No host PATH: resolution must be decided by the fake providers alone.
		Locator: nil,
	})
	if !got.Ambiguous {
		t.Fatalf("two plausible package managers must be reported as ambiguous, got %+v", got)
	}
	if len(got.Alternatives) == 0 {
		t.Error("the user needs alternatives to choose from")
	}
}

func TestSystemOwnedCopyDoesNotBlockResolution(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package: model.Package{Name: "perl"},
		Existing: &model.Installation{
			Name:     "perl",
			Provider: system.Name,
			Path:     "/usr/bin/perl",
		},
		Available: health,
	})
	if got.Provider == system.Name {
		t.Fatal("Starpoint must not adopt operating-system software as something it manages")
	}
}

func TestAllowedListRestrictsCandidates(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: uv.Name, kind: provider.KindPython, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "ruff"},
		Allowed:   []string{brew.Name},
		Available: health,
		// No host PATH: resolution must be decided by the fake providers alone.
		Locator: nil,
	})
	if got.Provider != brew.Name {
		t.Fatalf("--provider must restrict resolution, got %q", got.Provider)
	}
}

func TestUnavailableProvidersAreNeverChosen(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: false},
		spec{name: cargo.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "ripgrep"},
		Available: health,
		// No host PATH: resolution must be decided by the fake providers alone.
		Locator: nil,
	})
	if got.Provider == brew.Name {
		t.Fatal("an unavailable provider must not be selected")
	}
}

func TestNodeEcosystemPrefersNPM(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: npm.Name, kind: provider.KindNode, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "pnpm"},
		Available: health,
		// No host PATH: resolution must be decided by the fake providers alone.
		Locator: nil,
	})
	if got.Provider != npm.Name {
		t.Fatalf("pnpm belongs to npm, got %q", got.Provider)
	}
}

func TestNoProviderAvailableIsReportedSpeculatively(t *testing.T) {
	reg := provider.NewRegistry()
	health := map[string]provider.Health{}
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "ripgrep"},
		Available: health,
		// No host PATH: resolution must be decided by the fake providers alone.
		Locator: nil,
	})
	if got.Confidence != model.ConfidenceSpeculat {
		t.Fatalf("with nothing available the resolver must not sound confident: %+v", got)
	}
	if got.Reason == "" {
		t.Error("a reason is required so the user can see why")
	}
}

func TestReadOnlyProviderIsNotPreferredForNewInstalls(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: system.Name, kind: provider.KindSystem, caps: systemCaps(), available: true},
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "ripgrep"},
		Available: health,
		// No host PATH: resolution must be decided by the fake providers alone.
		Locator: nil,
	})
	if got.Provider != brew.Name {
		t.Fatalf("a read-only provider cannot install; expected brew, got %q", got.Provider)
	}
}
