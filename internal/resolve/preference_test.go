package resolve

import (
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers/brew"
	"github.com/hxmbl/starpoint/internal/providers/cargo"
	"github.com/hxmbl/starpoint/internal/providers/uv"
)

// twoManagers is the classic tie: brew and apt can both install ripgrep and
// neither rule decides between them.
func twoManagers(t *testing.T, aptAvailable bool, aptCaps provider.Capabilities) (*Resolver, map[string]provider.Health) {
	t.Helper()
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: "apt", kind: provider.KindApt, caps: aptCaps, available: aptAvailable},
		spec{name: cargo.Name, kind: provider.KindRust, caps: fullCaps(model.ScopeUser), available: true},
	)
	return New(reg), health
}

func stepFor(expl Explanation, rule string) (Step, bool) {
	for _, s := range expl.Steps {
		if s.Rule == rule {
			return s, true
		}
	}
	return Step{}, false
}

func TestTieStaysAmbiguousWithoutAPreference(t *testing.T) {
	r, health := twoManagers(t, true, fullCaps(model.ScopeGlobal))
	expl := r.Explain(Input{Package: model.Package{Name: "ripgrep"}, Available: health})
	if !expl.Final.Ambiguous {
		t.Fatalf("no preference must leave the tie for the user, got %+v", expl.Final)
	}
	if _, ok := stepFor(expl, "configured preference"); ok {
		t.Error("with no [settings] providers there is no preference step")
	}
}

func TestPreferenceSettlesATieInOrder(t *testing.T) {
	r, health := twoManagers(t, true, fullCaps(model.ScopeGlobal))
	// Two system managers tie with each other; cargo is not part of that tie.
	for _, want := range []string{"apt", brew.Name} {
		others := []string{"apt", brew.Name}
		pref := []string{want}
		for _, o := range others {
			if o != want {
				pref = append(pref, o)
			}
		}
		expl := r.Explain(Input{Package: model.Package{Name: "ripgrep"}, Available: health, Preferred: pref})
		got := expl.Final
		if got.Ambiguous || got.Provider != want {
			t.Fatalf("preference %v: want %s settled, got %+v", pref, want, got)
		}
		if got.Confidence != model.ConfidenceHigh {
			t.Errorf("a configured preference is high confidence, got %q", got.Confidence)
		}
		if !strings.Contains(got.Reason, "[settings] providers") {
			t.Errorf("the reason must name the setting, got %q", got.Reason)
		}
		for _, a := range got.Alternatives {
			if a == want {
				t.Errorf("the chosen provider is not its own alternative: %v", got.Alternatives)
			}
		}
		step, ok := stepFor(expl, "configured preference")
		if !ok || step.Outcome != "selected" || !step.Applied || step.Provider != want {
			t.Errorf("explain must show the preference deciding, got %+v", step)
		}
	}
}

func TestPreferenceNeverWidensTheCandidates(t *testing.T) {
	r, health := twoManagers(t, true, fullCaps(model.ScopeGlobal))
	// cargo is available but not one of the tied system managers, so the
	// next listed provider that is tied decides.
	expl := r.Explain(Input{Package: model.Package{Name: "ripgrep"}, Available: health, Preferred: []string{cargo.Name, "apt"}})
	if expl.Final.Provider != "apt" || expl.Final.Ambiguous {
		t.Fatalf("a provider outside the tie must be skipped, got %+v", expl.Final)
	}
	// uv is not registered at all, so it cannot be among the tied providers.
	expl = r.Explain(Input{Package: model.Package{Name: "ripgrep"}, Available: health, Preferred: []string{uv.Name}})
	if !expl.Final.Ambiguous {
		t.Fatalf("a provider outside the tie must not decide, got %+v", expl.Final)
	}
	step, ok := stepFor(expl, "configured preference")
	if !ok || step.Outcome != "skipped" || step.Applied {
		t.Errorf("explain must show the preference skipped, got %+v", step)
	}
}

func TestUnavailablePreferredProviderIsPassedOver(t *testing.T) {
	// apt is listed first but is not available here.
	r, health := twoManagers(t, false, fullCaps(model.ScopeGlobal))
	expl := r.Explain(Input{Package: model.Package{Name: "ripgrep"}, Available: health, Preferred: []string{"apt", cargo.Name}})
	got := expl.Final
	if got.Ambiguous || got.Provider != cargo.Name {
		t.Fatalf("the next usable preferred provider should decide, got %+v", got)
	}
}

func TestPreferenceFallsBackToTheQuestionWhenNothingFits(t *testing.T) {
	// apt cannot pin a version; the entry pins one, and apt is the only
	// provider the user listed.
	caps := fullCaps(model.ScopeGlobal)
	caps.PinVersion = false
	r, health := twoManagers(t, true, caps)
	expl := r.Explain(Input{Package: model.Package{Name: "ripgrep", Version: "14.1.0"}, Available: health, Preferred: []string{"apt"}})
	if !expl.Final.Ambiguous {
		t.Fatalf("a preferred provider that cannot honour the pin must not decide, got %+v", expl.Final)
	}
	step, ok := stepFor(expl, "configured preference")
	if !ok || step.Outcome != "skipped" {
		t.Fatalf("explain must show the preference skipped, got %+v", step)
	}
	if !strings.Contains(step.Reason, "cannot pin version 14.1.0") {
		t.Errorf("explain must say why apt was passed over, got %q", step.Reason)
	}
}

func TestPreferenceRespectsChannelAndScope(t *testing.T) {
	r, health := twoManagers(t, true, fullCaps(model.ScopeGlobal))
	cases := []struct {
		pkg  model.Package
		want string
	}{
		{model.Package{Name: "ripgrep", Channel: "edge"}, "has no channels"},
		{model.Package{Name: "ripgrep", Scope: model.ScopeUser}, "installs at global scope, not user"},
		{model.Package{Name: "ripgrep", Scope: model.ScopeProject}, "cannot install into a project"},
	}
	for _, c := range cases {
		expl := r.Explain(Input{Package: c.pkg, Available: health, Preferred: []string{"apt"}})
		if expl.Final.Provider == "apt" && !expl.Final.Ambiguous {
			t.Errorf("%+v: apt must not be forced, got %+v", c.pkg, expl.Final)
		}
		step, _ := stepFor(expl, "configured preference")
		if !strings.Contains(step.Reason, c.want) {
			t.Errorf("%+v: explain reason %q should contain %q", c.pkg, step.Reason, c.want)
		}
	}
}

func TestPreferenceRespectsTheAllowedSet(t *testing.T) {
	r, health := twoManagers(t, true, fullCaps(model.ScopeGlobal))
	expl := r.Explain(Input{Package: model.Package{Name: "ripgrep"}, Available: health,
		Allowed: []string{brew.Name, "apt"}, Preferred: []string{cargo.Name, "apt"}})
	if expl.Final.Provider != "apt" || expl.Final.Ambiguous {
		t.Fatalf("a preferred provider outside the command's allowed set must be passed over, got %+v", expl.Final)
	}
}

func TestDecisiveRulesOutrankThePreference(t *testing.T) {
	reg, health := buildRegistry(t,
		spec{name: brew.Name, kind: provider.KindBrew, caps: fullCaps(model.ScopeUser), available: true},
		spec{name: uv.Name, kind: provider.KindPython, caps: fullCaps(model.ScopeUser), available: true},
	)
	r := New(reg)
	pref := []string{brew.Name}

	// A provider written in the entry (or named with --provider) wins.
	got := r.Resolve(Input{Package: model.Package{Name: "ruff", Provider: uv.Name}, Available: health, Preferred: pref})
	if got.Provider != uv.Name || !got.Forced {
		t.Errorf("an explicit provider must win over the preference, got %+v", got)
	}

	// An existing owner keeps the package.
	got = r.Resolve(Input{Package: model.Package{Name: "black"},
		Existing:  &model.Installation{Name: "black", Provider: uv.Name, Path: "/home/u/.local/bin/black"},
		Available: health, Preferred: pref})
	if got.Provider != uv.Name {
		t.Errorf("an existing owner must win over the preference, got %+v", got)
	}

	// An ecosystem hint is a decision, not a tie, so the preference is not
	// consulted at all.
	got = r.Resolve(Input{Package: model.Package{Name: "ruff"}, Available: health, Preferred: pref})
	if got.Provider != uv.Name {
		t.Errorf("an ecosystem hint is not a tie for the preference to settle, got %+v", got)
	}
}
