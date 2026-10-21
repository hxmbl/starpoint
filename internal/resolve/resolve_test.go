package resolve

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/brew"
	"github.com/hxmbl/starpoint/internal/providers/cargo"
	"github.com/hxmbl/starpoint/internal/providers/flatpak"
	"github.com/hxmbl/starpoint/internal/providers/npm"
	"github.com/hxmbl/starpoint/internal/providers/rustup"
	"github.com/hxmbl/starpoint/internal/providers/system"
	"github.com/hxmbl/starpoint/internal/providers/uv"
	"github.com/hxmbl/starpoint/internal/testsupport"
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

func TestToolchainBinaryBelongsToRustupNotCargo(t *testing.T) {
	// "Rust" is two jobs. rustc and cargo are rustup's; `cargo install` is
	// Cargo's. Conflating them is how a machine ends up with two compilers.
	reg, health := buildRegistry(t,
		spec{name: rustup.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: cargo.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	for _, name := range []string{"rustc", "cargo", "rustfmt", "rust-analyzer", "clippy-driver", "rustup"} {
		got := r.Resolve(Input{
			Package:   model.Package{Name: name},
			Available: health,
			Locator:   nil,
		})
		if got.Provider != rustup.Name {
			t.Errorf("%s belongs to rustup, got %q (%s)", name, got.Provider, got.Reason)
		}
		if got.Ambiguous {
			t.Errorf("%s has one known owner and must not be ambiguous", name)
		}
	}
}

func TestOwnerHintSurvivesAnUnavailableProvider(t *testing.T) {
	// Falling through to Cargo for rustc would mean running `cargo install rustc`.
	reg, health := buildRegistry(t,
		spec{name: rustup.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: false},
		spec{name: cargo.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{Package: model.Package{Name: "rustc"}, Available: health, Locator: nil})
	if got.Provider != rustup.Name {
		t.Fatalf("the owner must be reported even when missing, got %q (%s)", got.Provider, got.Reason)
	}
	if !strings.Contains(got.Reason, "not available") {
		t.Errorf("the reason must say rustup is missing, got %q", got.Reason)
	}
}

func TestExplicitRestrictionOverridesAnOwnerHint(t *testing.T) {
	// --provider is a decision, so it outranks a built-in hint.
	reg, health := buildRegistry(t,
		spec{name: rustup.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "rustc"},
		Allowed:   []string{brew.Name},
		Available: health,
		Locator:   nil,
	})
	if got.Provider != brew.Name {
		t.Fatalf("--provider must win over a built-in owner hint, got %q", got.Provider)
	}
}

func TestExplicitProviderInConfigOverridesAnOwnerHint(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: rustup.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "rustc", Provider: brew.Name},
		Available: health,
		Locator:   nil,
	})
	if got.Provider != brew.Name || !got.Forced {
		t.Fatalf("an explicit configuration must win, got %+v", got)
	}
}

func TestRustToolchainOwnerDoesNotStealCratesFromCargo(t *testing.T) {
	// The owner hints must not swallow Cargo's actual job.
	reg, health := buildRegistry(t,
		spec{name: rustup.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: cargo.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "rg"},
		Available: health,
		Locator:   nil,
	})
	if got.Provider != cargo.Name {
		t.Errorf("rg is a Rust binary and belongs to cargo, got %q (%s)", got.Provider, got.Reason)
	}
	if got.Ambiguous {
		t.Errorf("rg has a known ecosystem and must not be ambiguous: %+v", got)
	}
}

func TestToolchainOwnerDoesNotOverrideHomebrewForCommonRustBinaries(t *testing.T) {
	// `brew install ripgrep` is what almost everyone means. Adding a rustup owner
	// hint for it would quietly change that into `cargo install`, which compiles
	// from source. The single available package manager should still win.
	reg, health := buildRegistry(t,
		spec{name: rustup.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "ripgrep"},
		Available: health,
		Locator:   nil,
	})
	if got.Provider != brew.Name {
		t.Errorf("ripgrep should stay with the only package manager available, got %q (%s)",
			got.Provider, got.Reason)
	}
}

func TestUnrecognisedCrateNameIsAmbiguousNotOrderedIntoRustup(t *testing.T) {
	// An arbitrary crate name says nothing about its ecosystem. With rustup and
	// cargo both available the answer must be asked for, not settled by the order
	// providers happen to be registered in.
	reg, health := buildRegistry(t,
		spec{name: rustup.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: cargo.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "cargo-edit"},
		Available: health,
		Locator:   nil,
	})
	if !got.Ambiguous {
		t.Fatalf("an unrecognised name with two Rust providers must be ambiguous, got %+v", got)
	}
	found := false
	for _, alt := range append([]string{got.Provider}, got.Alternatives...) {
		if alt == cargo.Name {
			found = true
		}
	}
	if !found {
		t.Errorf("cargo must be offered as an option, got %+v", got)
	}
}

func TestSingleAvailableProviderIsNotAmbiguous(t *testing.T) {
	// One provider can act; there is nothing to choose between.
	reg, health := buildRegistry(t,
		spec{name: cargo.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package:   model.Package{Name: "some-unknown-crate"},
		Available: health,
		Locator:   nil,
	})
	if got.Ambiguous {
		t.Errorf("a single candidate is not ambiguous: %+v", got)
	}
	if got.Provider != cargo.Name {
		t.Errorf("expected cargo, got %q", got.Provider)
	}
}

func TestExistingCargoOwnedCrateKeepsItsOwner(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: rustup.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: cargo.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package: model.Package{Name: "cargo-edit"},
		Existing: &model.Installation{
			Name: "cargo-edit", Provider: cargo.Name, Path: "/home/u/.cargo/bin/cargo-add",
		},
		Available: health,
		Locator:   nil,
	})
	if got.Provider != cargo.Name {
		t.Errorf("existing ownership must be kept, got %q", got.Provider)
	}
}

func TestKnownOwnerWinsOverAnIncumbentFromAnotherProvider(t *testing.T) {
	// Homebrew also ships a rustc. That is a real duplicate, but it is not a
	// reason to record Homebrew as the owner of `rustc`.
	reg, health := buildRegistry(t,
		spec{name: rustup.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package: model.Package{Name: "rustc"},
		Existing: &model.Installation{
			Name: "rustc", Provider: brew.Name, Path: "/usr/local/bin/rustc",
		},
		Available: health,
		Locator:   nil,
	})
	if got.Provider != rustup.Name {
		t.Errorf("the known owner must win over an incidental incumbent, got %q (%s)", got.Provider, got.Reason)
	}
}

func TestIncumbentStillWinsForNamesWithoutAKnownOwner(t *testing.T) {
	// The exception above must not become a general rule: for an unrecognised
	// name, whoever already has it keeps it.
	reg, health := buildRegistry(t,
		spec{name: rustup.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	got := r.Resolve(Input{
		Package: model.Package{Name: "ripgrep"},
		Existing: &model.Installation{
			Name: "ripgrep", Provider: brew.Name, Path: "/usr/local/bin/rg",
		},
		Available: health,
		Locator:   nil,
	})
	if got.Provider != brew.Name {
		t.Errorf("existing ownership must be kept for an unrecognised name, got %q", got.Provider)
	}
}

// The audit: `install cowsay` on a machine with apt, uv, pip, cargo and npm
// picked apt with high confidence because apt was the only *system* package
// manager, and said "only one package manager is available".
func TestOneSystemManagerBesideLanguageProvidersIsAmbiguous(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: "apt", kind: provider.KindApt, caps: fullCaps(model.ScopeGlobal), available: true},
		spec{name: uv.Name, kind: provider.KindPython, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: cargo.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: rustup.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
	)
	got := New(reg).Resolve(Input{Package: model.Package{Name: "cowsay"}, Available: health})
	if !got.Ambiguous {
		t.Fatalf("an unknown name with several viable providers must be asked about, got %+v", got)
	}
	if got.Provider != "apt" {
		t.Errorf("the system package manager should be the offered default, got %q", got.Provider)
	}
	if got.Confidence == model.ConfidenceHigh || got.Confidence == model.ConfidenceCertain {
		t.Errorf("a guess is not high confidence: %q", got.Confidence)
	}
	alts := strings.Join(got.Alternatives, ",")
	if alts != "cargo,uv" {
		t.Errorf("language providers are the alternatives (rustup installs toolchains only), got %q", alts)
	}
	if strings.Contains(got.Reason, "only one package manager") {
		t.Errorf("the reason must not claim there is one package manager: %q", got.Reason)
	}
	if !strings.Contains(got.Reason, "only system package manager") {
		t.Errorf("the reason should say what is true, got %q", got.Reason)
	}
}

func TestOneSystemManagerAloneSaysSo(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: rustup.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
	)
	got := New(reg).Resolve(Input{Package: model.Package{Name: "ripgrep"}, Available: health})
	if got.Ambiguous || got.Provider != brew.Name {
		t.Fatalf("brew is the only plausible home, got %+v", got)
	}
	if got.Reason != "only one system package manager is available" {
		t.Errorf("reason = %q", got.Reason)
	}
}

func TestAKnownEcosystemIsStillNotAmbiguous(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: "apt", kind: provider.KindApt, caps: fullCaps(model.ScopeGlobal), available: true},
		spec{name: uv.Name, kind: provider.KindPython, caps: fullCaps(model.ScopeUser), available: true},
	)
	got := New(reg).Resolve(Input{Package: model.Package{Name: "ruff"}, Available: health})
	if got.Ambiguous || got.Provider != uv.Name {
		t.Errorf("a name that identifies its ecosystem is decided, got %+v", got)
	}
}

// A Flatpak application ID is the one name shape that identifies its own
// ecosystem. Without this rule, an app ID on a machine with apt or brew is
// resolved to that manager, which cannot install it and never offers Flatpak.
func TestReverseDNSNameResolvesToFlatpak(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: "apt", kind: provider.KindApt, caps: fullCaps(model.ScopeGlobal), available: true},
		spec{name: flatpak.Name, kind: provider.KindFlatpak, caps: fullCaps(model.ScopeGlobal), available: true},
	)
	expl := New(reg).Explain(Input{Package: model.Package{Name: "org.gimp.GIMP"}, Available: health})
	if expl.Final.Provider != flatpak.Name {
		t.Fatalf("an application ID belongs to Flatpak, got %+v", expl.Final)
	}
	if expl.Final.Ambiguous {
		t.Error("the name identifies the ecosystem; there is nothing to ask about")
	}
	applied := false
	for _, step := range expl.Steps {
		if step.Rule == "application ID" && step.Applied {
			applied = true
		}
	}
	if !applied {
		t.Errorf("the decision must be traceable to the application-ID rule: %+v", expl.Steps)
	}
}

func TestReverseDNSNameFallsThroughWhenFlatpakIsMissing(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: flatpak.Name, kind: provider.KindFlatpak, caps: fullCaps(model.ScopeGlobal), available: false},
	)
	got := New(reg).Resolve(Input{Package: model.Package{Name: "org.gimp.GIMP"}, Available: health})
	if got.Provider != brew.Name {
		t.Errorf("an unavailable Flatpak must not be chosen, got %+v", got)
	}
}

func TestReverseDNSNameRespectsTheAllowedSet(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: flatpak.Name, kind: provider.KindFlatpak, caps: fullCaps(model.ScopeGlobal), available: true},
	)
	got := New(reg).Resolve(Input{
		Package:   model.Package{Name: "org.gimp.GIMP"},
		Available: health,
		Allowed:   []string{brew.Name},
	})
	if got.Provider != brew.Name {
		t.Errorf("an explicit restriction outranks the name-shape hint, got %+v", got)
	}
}

func TestOrdinaryNamesAreNotApplicationIDs(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: flatpak.Name, kind: provider.KindFlatpak, caps: fullCaps(model.ScopeGlobal), available: true},
	)
	got := New(reg).Resolve(Input{Package: model.Package{Name: "firefox"}, Available: health})
	if got.Provider != brew.Name {
		t.Errorf("an ordinary package name must not be routed to Flatpak, got %+v", got)
	}
}

func TestIsAppID(t *testing.T) {
	valid := []string{
		"org.gimp.GIMP",
		"com.spotify.Client",
		"io.github.foo_bar",
		"md.obsidian.Obsidian",
	}
	for _, name := range valid {
		if !isAppID(name) {
			t.Errorf("%q has the shape of an application ID", name)
		}
	}
	invalid := []string{
		"firefox",
		"lodash.merge",       // two elements is an npm name, not a D-Bus name
		"org..GIMP",          // an empty element is invalid
		"1org.gimp.GIMP",     // an element may not start with a digit
		"org.gimp.GIMP/x",    // a path, not an ID
		"org.gimp.GIMP beta", // whitespace is not allowed
	}
	for _, name := range invalid {
		if isAppID(name) {
			t.Errorf("%q is not an application ID", name)
		}
	}
}
