package cli

import (
	"os"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

type portabilityDoc struct {
	SchemaVersion int                `json:"schema_version"`
	Portability   engine.Portability `json:"portability"`
	Warnings      []model.Warning    `json:"warnings"`
}

func (h *harness) portability() portabilityDoc {
	var doc portabilityDoc
	h.decode(h.mustRun("inventory", "--portability", "--json"), &doc)
	return doc
}

func entriesByName(p engine.Portability) map[string]engine.PortabilityEntry {
	out := map[string]engine.PortabilityEntry{}
	for _, en := range p.Entries {
		out[en.Name] = en
	}
	return out
}

// Everything selected is declared with a provider that can install it, or
// was installed on request through one: fully reproducible.
func TestPortabilityFullyCaptured(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\nversion = \"0.6.0\"\n")
	h.brew.With(model.Installation{Name: "jq", Version: "1.7.1"}).
		With(model.Installation{Name: "oniguruma", Version: "6.9"})
	h.env.Registry.Register(&testsupport.RequestedFake{FakeProvider: h.brew, Marks: map[string]bool{"jq": true, "oniguruma": false}})
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})
	h.sys.With(model.Installation{Name: "zsh", Path: "/usr/bin/zsh"})
	before := h.configBody()

	doc := h.portability()
	p := doc.Portability
	if doc.SchemaVersion != 10 {
		t.Errorf("schema_version = %d, want 10", doc.SchemaVersion)
	}
	if p.Declared.Total != 2 || p.Declared.Reproducible != 2 || p.Discovered.Total != 0 {
		t.Errorf("want both declared entries reproducible and nothing else selected: %+v / %+v", p.Declared, p.Discovered)
	}
	if p.Dependencies != 1 || p.System != 1 {
		t.Errorf("dependencies and system software are counted and left out: deps=%d system=%d", p.Dependencies, p.System)
	}
	if p.Note == "" || !strings.Contains(p.Note, "not a guarantee") {
		t.Errorf("the summary must say it is an estimate: %q", p.Note)
	}
	out := h.mustRun("inventory", "--portability")
	for _, want := range []string{"Portability estimate", "not a guarantee", "About 2 of 2 selected package(s) (100%)", "Nothing was changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	if h.configBody() != before {
		t.Error("the summary must not touch the configuration")
	}
	for _, p := range []*testsupport.FakeProvider{h.brew, h.uv, h.cargo} {
		if len(p.InstallCalls)+len(p.RemoveCalls) != 0 {
			t.Errorf("%s: the summary must not install or remove", p.ProviderName)
		}
	}
	if again := h.mustRun("inventory", "--portability"); again != out {
		t.Errorf("the summary must be deterministic:\n%s\n---\n%s", out, again)
	}
}

// Some of it is reproducible; the rest says why not.
func TestPortabilityPartiallyCaptured(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n")
	h.brew.With(model.Installation{Name: "jq", Version: "1.7.1"}).
		With(model.Installation{Name: "wget", Version: "1.24"}).
		With(model.Installation{Name: "odd", Version: "1.0"}).
		With(model.Installation{Name: "ripgrep", Version: "14.1.0"})
	h.env.Registry.Register(&testsupport.RequestedFake{FakeProvider: h.brew,
		Marks: map[string]bool{"jq": true, "wget": true, "ripgrep": true}})
	h.cargo.With(model.Installation{Name: "ripgrep", Version: "14.1.1"})
	h.env.Registry.Register(testsupport.ListingFake{FakeProvider: h.cargo})

	p := h.portability().Portability
	got := entriesByName(p)
	if en := got["jq"]; en.Source != engine.PortSourceDeclared || en.Status != engine.PortableYes {
		t.Errorf("jq: %+v", en)
	}
	if en := got["wget"]; en.Source != engine.PortSourceDiscovered || en.Status != engine.PortableYes || en.Provider != "brew" {
		t.Errorf("wget is undeclared requested software brew can install: %+v", en)
	}
	if en := got["odd"]; en.Status != engine.PortableUnknown || en.Code != engine.PortMissingMetadata {
		t.Errorf("odd cannot be said to be wanted: %+v", en)
	}
	if en := got["ripgrep"]; en.Status != engine.PortableUnknown || en.Code != engine.PortAmbiguous || len(en.Candidates) != 2 {
		t.Errorf("ripgrep has two owners and nothing recording which: %+v", en)
	}
	if p.Overall.Total != 4 || p.Overall.Reproducible != 2 || p.Overall.Unknown != 2 {
		t.Errorf("overall = %+v", p.Overall)
	}
	out := h.mustRun("inventory", "--portability")
	for _, want := range []string{"[missing-metadata]", "[ambiguous-identity]", "About 2 of 4", "inventory --format toml"} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "wget  ") && strings.Contains(out, "reproducible  ") {
		t.Errorf("reproducible entries are counted, not listed as problems:\n%s", out)
	}
}

// A setup that mostly cannot be reproduced: a pin the provider cannot honour,
// a channel it has no notion of, a provider Starpoint does not have, the
// operating system's own software, and an entry nobody can place.
func TestPortabilityPoorlyReproducible(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n"+
		"[[package]]\nname = \"jq\"\nprovider = \"brew\"\nversion = \"1.6\"\n\n"+
		"[[package]]\nname = \"node\"\nprovider = \"brew\"\nchannel = \"lts\"\n\n"+
		"[[package]]\nname = \"thing\"\nprovider = \"conda\"\n\n"+
		"[[package]]\nname = \"zsh\"\nprovider = \"system\"\n\n"+
		"[[package]]\nname = \"mystery\"\n\n"+
		"[[package]]\nname = \"fd\"\nprovider = \"cargo\"\n")
	h.brew.Caps.PinVersion = false
	h.brew.Caps.Channels = false
	h.sys.With(model.Installation{Name: "zsh", Path: "/usr/bin/zsh"})

	p := h.portability().Portability
	got := entriesByName(p)
	cases := map[string]struct{ status, code string }{
		"jq":      {engine.PortableUnsupported, engine.PortPinUnavailable},
		"node":    {engine.PortableUnsupported, engine.PortChannelUnsupported},
		"thing":   {engine.PortableUnsupported, engine.PortUnsupportedProvider},
		"zsh":     {engine.PortableUnsupported, engine.PortUnsupportedProvider},
		"mystery": {engine.PortableUnknown, engine.PortUnknownProvider},
		"fd":      {engine.PortableYes, ""},
	}
	for name, want := range cases {
		en := got[name]
		if en.Status != want.status || en.Code != want.code {
			t.Errorf("%s: got %s/%s, want %s/%s (%s)", name, en.Status, en.Code, want.status, want.code, en.Reason)
		}
		if want.status != engine.PortableYes && en.Reason == "" {
			t.Errorf("%s: a non-reproducible entry says why", name)
		}
	}
	if p.Declared.Total != 6 || p.Declared.Reproducible != 1 || p.Declared.Unsupported != 4 || p.Declared.Unknown != 1 {
		t.Errorf("declared = %+v", p.Declared)
	}
	out := h.mustRun("inventory", "--portability")
	if !strings.Contains(out, "About 1 of 6 selected package(s) (16%)") {
		t.Errorf("want the estimate line:\n%s", out)
	}
}

// A declared entry with no provider is placed by the one provider that
// reports it, and is ambiguous when several do.
func TestPortabilityPlacesAnUnrecordedDeclaration(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ruff\"\n\n[[package]]\nname = \"ripgrep\"\n")
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})
	h.brew.With(model.Installation{Name: "ripgrep"})
	h.cargo.With(model.Installation{Name: "ripgrep"})
	h.env.Registry.Register(testsupport.ListingFake{FakeProvider: h.cargo})
	got := entriesByName(h.portability().Portability)
	if en := got["ruff"]; en.Provider != "uv" || en.Status != engine.PortableYes {
		t.Errorf("ruff: %+v", en)
	}
	if en := got["ripgrep"]; en.Status != engine.PortableUnknown || en.Code != engine.PortAmbiguous {
		t.Errorf("ripgrep: %+v", en)
	}
}

// A provider that could not list makes the summary incomplete, and it says so.
func TestPortabilitySaysWhenAProviderCouldNotList(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.DiscoverError = os.ErrPermission
	out := h.mustRun("inventory", "--portability")
	if !strings.Contains(out, "Incomplete") || !strings.Contains(out, "Nothing selected") {
		t.Errorf("want the incomplete note:\n%s", out)
	}
	if doc := h.portability(); len(doc.Warnings) == 0 {
		t.Error("the JSON carries the discover-failed warning")
	}
}

func TestPortabilityRejectsTOML(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	if code, _, _ := h.run("inventory", "--portability", "--format", "toml"); code != ExitUsage {
		t.Errorf("exit %d, want %d", code, ExitUsage)
	}
}
