package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// captureMachine is a machine with a bit of everything: requested software,
// a dependency, a package nobody can say was asked for, one name two
// providers report, the operating system's own software, and an entry the
// configuration already manages with a pin and a channel.
func captureMachine(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"node\"\nprovider = \"brew\"\nversion = \"22.1.0\"\nchannel = \"lts\"\nreason = \"mine\"\n")
	h.brew.With(model.Installation{Name: "jq", Version: "1.7.1"}).
		With(model.Installation{Name: "oniguruma", Version: "6.9"}).
		With(model.Installation{Name: "odd", Version: "1.0"}).
		With(model.Installation{Name: "ripgrep", Version: "14.1.0"}).
		With(model.Installation{Name: "node", Version: "22.1.0"})
	h.env.Registry.Register(&testsupport.RequestedFake{FakeProvider: h.brew,
		Marks: map[string]bool{"jq": true, "oniguruma": false, "ripgrep": true}})
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})
	h.env.Registry.Register(testsupport.ListingFake{FakeProvider: h.uv})
	h.cargo.With(model.Installation{Name: "ripgrep", Version: "14.1.1"})
	h.env.Registry.Register(testsupport.ListingFake{FakeProvider: h.cargo})
	h.sys.With(model.Installation{Name: "zsh", Path: "/usr/bin/zsh"})
	return h
}

// parseManifest proves the output is Starpoint configuration by loading it
// the way Starpoint loads its own file.
func parseManifest(t *testing.T, out string) []model.Package {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".starpoint.toml")
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	doc, err := config.Load(config.LoadOptions{GlobalPath: path})
	if err != nil {
		t.Fatalf("the manifest must parse as configuration: %v\n%s", err, out)
	}
	if doc.Global.Raw.Schema != config.SchemaVersion {
		t.Errorf("schema = %d, want %d", doc.Global.Raw.Schema, config.SchemaVersion)
	}
	return doc.Global.Raw.Packages
}

func byName(pkgs []model.Package) map[string]model.Package {
	out := map[string]model.Package{}
	for _, p := range pkgs {
		out[p.Name] = p
	}
	return out
}

// The default policy captures requested and managed software, and reports
// the rest with a reason rather than guessing.
func TestInventoryTOMLCapturesRequestedSoftwareAndReportsTheRest(t *testing.T) {
	h := captureMachine(t)
	before := h.configBody()
	out := h.mustRun("inventory", "--format", "toml")
	got := byName(parseManifest(t, out))

	if len(got) != 3 {
		t.Errorf("want jq, node and ruff, got %v", got)
	}
	if p := got["jq"]; p.Provider != "brew" || p.Version != "" || p.Scope != "" || !strings.Contains(p.Reason, "installed on request") {
		t.Errorf("jq is captured unpinned with its provider and why: %+v", p)
	}
	if p := got["ruff"]; p.Provider != "uv" {
		t.Errorf("ruff is uv's: %+v", p)
	}
	// The managed entry is the configuration's own, pin and channel kept.
	if p := got["node"]; p.Version != "22.1.0" || p.Channel != "lts" || p.Reason != "mine" {
		t.Errorf("a managed entry is copied as written: %+v", p)
	}
	for _, absent := range []string{"oniguruma", "odd", "ripgrep", "zsh"} {
		if _, ok := got[absent]; ok {
			t.Errorf("%s must not be captured", absent)
		}
	}
	for _, want := range []string{
		"brew/odd [requested-unknown]",
		"ripgrep [ambiguous-identity]: installed through brew and cargo",
		"1 package(s) their provider installed as dependencies",
		"1 package(s) of the operating system's own",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the header should say %q:\n%s", want, out)
		}
	}
	if h.configBody() != before {
		t.Error("capturing must not touch the configuration")
	}
	for _, p := range []*testsupport.FakeProvider{h.brew, h.uv, h.cargo} {
		if len(p.InstallCalls)+len(p.RemoveCalls) != 0 {
			t.Errorf("%s: capturing must not install or remove", p.ProviderName)
		}
	}
	if strings.Contains(out, "[settings]") {
		t.Errorf("a manifest carries no machine settings:\n%s", out)
	}
	if again := h.mustRun("inventory", "--format", "toml"); again != out {
		t.Errorf("capture must be deterministic:\n%s\n---\n%s", out, again)
	}
}

// Round trip: the manifest parses back to the same entries, and loading it
// and writing it with Starpoint's own writer loses nothing.
func TestInventoryTOMLRoundTripsThroughTheConfiguration(t *testing.T) {
	h := captureMachine(t)
	out := h.mustRun("inventory", "--format", "toml", "--pin-versions")
	first := parseManifest(t, out)

	path := filepath.Join(t.TempDir(), "again.toml")
	if err := config.Write(path, config.Raw{Packages: first}); err != nil {
		t.Fatal(err)
	}
	doc, err := config.Load(config.LoadOptions{GlobalPath: path})
	if err != nil {
		t.Fatal(err)
	}
	second := doc.Global.Raw.Packages
	if len(first) != len(second) {
		t.Fatalf("round trip changed the entry count: %d → %d", len(first), len(second))
	}
	for i := range first {
		a, b := first[i], second[i]
		a.Origin, b.Origin = model.Origin{}, model.Origin{}
		if a != b {
			t.Errorf("round trip changed an entry:\n%+v\n%+v", a, b)
		}
	}
	if p := byName(first)["ruff"]; p.Version != "0.6.0" {
		t.Errorf("--pin-versions pins a provider that can pin: %+v", p)
	}
	// Homebrew cannot install a chosen version, so jq stays unpinned.
	if p := byName(first)["jq"]; p.Version != "" || !strings.Contains(out, "brew/jq [pin-unsupported]") {
		t.Errorf("brew cannot pin: %+v\n%s", p, out)
	}
}

// --pin-versions never pins what the provider cannot install, nor a version
// it did not report; both are captured unpinned and said so.
func TestInventoryTOMLDoesNotPinWhatCannotBePinned(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.Caps.PinVersion = false
	h.brew.With(model.Installation{Name: "jq", Version: "1.7.1"})
	h.env.Registry.Register(&testsupport.RequestedFake{FakeProvider: h.brew, Marks: map[string]bool{"jq": true}})
	h.uv.With(model.Installation{Name: "ruff"})
	h.env.Registry.Register(testsupport.ListingFake{FakeProvider: h.uv})
	out := h.mustRun("inventory", "--format", "toml", "--pin-versions")
	got := byName(parseManifest(t, out))
	if got["jq"].Version != "" || got["ruff"].Version != "" {
		t.Errorf("nothing pinnable here: %+v", got)
	}
	for _, want := range []string{"brew/jq [pin-unsupported]", "uv/ruff [version-unknown]"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
}

// Naming packages is the deliberate selection: exactly those, a dependency
// included, while an ambiguous name, system software and an unknown name are
// still reported, not guessed.
func TestInventoryTOMLCapturesExactlyTheNamedPackages(t *testing.T) {
	h := captureMachine(t)
	out := h.mustRun("inventory", "--format", "toml", "oniguruma", "odd", "ripgrep", "zsh", "nosuch")
	got := byName(parseManifest(t, out))
	if len(got) != 2 || got["oniguruma"].Provider != "brew" || got["odd"].Provider != "brew" {
		t.Errorf("want oniguruma and odd: %+v", got)
	}
	for _, want := range []string{
		"brew/oniguruma [dependency]",
		"ripgrep [ambiguous-identity]",
		"system/zsh [system-software]",
		"nosuch [not-found]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
}

// A provider that could not list is named, so a short manifest is not
// mistaken for a complete one.
func TestInventoryTOMLSaysWhenAProviderCouldNotList(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.DiscoverError = os.ErrPermission
	out := h.mustRun("inventory", "--format", "toml")
	if !strings.Contains(out, "Incomplete:") || !strings.Contains(out, "brew") {
		t.Errorf("the failed provider is named:\n%s", out)
	}
	parseManifest(t, out)
}

func TestInventoryTOMLRejectsConflictingOptions(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	for _, args := range [][]string{
		{"inventory", "--format", "toml", "--json"},
		{"inventory", "--format", "yaml"},
		{"inventory", "jq"},
		{"inventory", "--pin-versions"},
		{"inventory", "--format"},
	} {
		if code, _, _ := h.run(args...); code != ExitUsage {
			t.Errorf("%v: exit %d, want %d", args, code, ExitUsage)
		}
	}
}

// Scope is written only when it differs from where the provider installs:
// a user-scope default is implied by the provider, a global copy is not.
func TestInventoryTOMLWritesScopeOnlyWhenItSaysSomething(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.uv.With(model.Installation{Name: "ruff", Scope: model.ScopeUser}).
		With(model.Installation{Name: "black", Scope: model.ScopeGlobal})
	h.env.Registry.Register(testsupport.ListingFake{FakeProvider: h.uv})
	got := byName(parseManifest(t, h.mustRun("inventory", "--format", "toml")))
	if got["ruff"].Scope != "" || got["black"].Scope != model.ScopeGlobal {
		t.Errorf("scope: ruff=%q black=%q", got["ruff"].Scope, got["black"].Scope)
	}
}
