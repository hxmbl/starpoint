package cli

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/engine"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

type compareDoc struct {
	SchemaVersion int               `json:"schema_version"`
	Comparison    engine.Comparison `json:"comparison"`
	Warnings      []model.Warning   `json:"warnings"`
}

// writeManifest puts a manifest from "another machine" beside the harness's
// configuration and returns its file name, relative to the work directory.
func (h *harness) writeManifest(body string) string {
	h.t.Helper()
	testsupport.WriteFile(h.t, filepath.Join(h.env.WorkDir, "laptop.toml"), body)
	return "laptop.toml"
}

func (h *harness) compare(manifest string) compareDoc {
	h.t.Helper()
	var doc compareDoc
	h.decode(h.mustRun("inventory", "--compare", manifest, "--json"), &doc)
	return doc
}

func compareByName(c engine.Comparison) map[string]engine.CompareEntry {
	out := map[string]engine.CompareEntry{}
	for _, en := range c.Entries {
		out[en.Name] = en
	}
	return out
}

// assertUntouched fails when the comparison installed, removed or recorded
// anything.
func (h *harness) assertUntouched(before string) {
	h.t.Helper()
	if h.configBody() != before {
		h.t.Error("comparing must not touch the configuration")
	}
	for _, p := range []*testsupport.FakeProvider{h.brew, h.uv, h.cargo, h.npm, h.rustup} {
		if len(p.InstallCalls)+len(p.RemoveCalls) != 0 {
			h.t.Errorf("%s: comparing must not install or remove", p.ProviderName)
		}
	}
}

// Everything the manifest asks for is already here, as asked.
func TestCompareExactMatch(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.With(model.Installation{Name: "jq", Version: "1.7.1"})
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})
	m := h.writeManifest("schema = 1\n\n[[package]]\nname = \"jq\"\nprovider = \"brew\"\n\n[[package]]\nname = \"ruff\"\nprovider = \"uv\"\nversion = \"0.6.0\"\n")
	before := h.configBody()

	doc := h.compare(m)
	c := doc.Comparison
	if doc.SchemaVersion != 11 {
		t.Errorf("schema_version = %d, want 11", doc.SchemaVersion)
	}
	if c.Counts.Total != 2 || c.Counts.Present != 2 || len(c.Plan) != 0 {
		t.Errorf("want both present and an empty plan: %+v plan=%v", c.Counts, c.Plan)
	}
	if !strings.HasSuffix(c.Manifest, "laptop.toml") || !strings.Contains(c.Note, "Nothing was installed") {
		t.Errorf("manifest path and note: %q / %q", c.Manifest, c.Note)
	}
	out := h.mustRun("inventory", "--compare", m)
	for _, want := range []string{"Manifest compared with this machine", "2 entries: 2 present", "Recovery plan: nothing to install.", "Nothing was changed."} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	h.assertUntouched(before)
}

// Missing packages become an ordered plan of install commands, and the
// plan is printed, never run.
func TestCompareMissingPlansInstalls(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	m := h.writeManifest("schema = 1\n\n" +
		"[[package]]\nname = \"ruff\"\nprovider = \"uv\"\nversion = \"0.6.0\"\n\n" +
		"[[package]]\nname = \"jq\"\nprovider = \"brew\"\n\n" +
		"[[package]]\nname = \"stable\"\nprovider = \"rustup\"\nchannel = \"nightly\"\n\n" +
		"[[package]]\nname = \"fd\"\nprovider = \"brew\"\n")
	before := h.configBody()

	c := h.compare(m).Comparison
	if c.Counts.Missing != 4 || len(c.Plan) != 4 {
		t.Fatalf("want 4 missing and 4 steps: %+v %v", c.Counts, c.Plan)
	}
	want := []string{
		"str install fd brew",
		"str install jq brew",
		"str install stable rustup --channel nightly",
		"str install ruff@0.6.0 uv",
	}
	for i, st := range c.Plan {
		if st.Command != want[i] {
			t.Errorf("step %d = %q, want %q (ordered by provider, then name)", i+1, st.Command, want[i])
		}
	}
	// Deterministic: the same inputs give the same plan.
	again := h.compare(m).Comparison
	for i := range again.Plan {
		if again.Plan[i] != c.Plan[i] {
			t.Errorf("plan changed between runs: %v vs %v", again.Plan, c.Plan)
		}
	}
	out := h.mustRun("inventory", "--compare", m)
	for _, want := range []string{"Recovery plan (4 steps, not run)", "1. str install fd brew", "duplicate-install", "Nothing was changed."} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	h.assertUntouched(before)
}

// A pin, channel or provider the target cannot honour is reported and never
// offered in another form.
func TestCompareUnsupportedPinsAndProviders(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	m := h.writeManifest("schema = 1\n\n" +
		"[[package]]\nname = \"jq\"\nprovider = \"brew\"\nversion = \"1.7.1\"\n\n" +
		"[[package]]\nname = \"ruff\"\nprovider = \"uv\"\nchannel = \"beta\"\n\n" +
		"[[package]]\nname = \"htop\"\nprovider = \"apt\"\n\n" +
		"[[package]]\nname = \"zsh\"\nprovider = \"system\"\n\n" +
		"[[package]]\nname = \"rg\"\nprovider = \"cargo\"\nscope = \"global\"\n")
	before := h.configBody()

	c := h.compare(m).Comparison
	got := compareByName(c)
	for name, code := range map[string]string{
		"jq":   engine.PortPinUnavailable,
		"ruff": engine.PortChannelUnsupported,
		"htop": engine.CmpProviderUnavailable,
		"zsh":  engine.PortUnsupportedProvider,
		"rg":   engine.CmpScopeUnplannable,
	} {
		if got[name].Status != engine.CompareUnsupported || got[name].Code != code {
			t.Errorf("%s: %s/%s, want unsupported/%s", name, got[name].Status, got[name].Code, code)
		}
	}
	if !strings.Contains(got["htop"].Reason, "apt-get not found") {
		t.Errorf("an unavailable provider says why: %q", got["htop"].Reason)
	}
	if len(c.Plan) != 0 {
		t.Errorf("nothing unsupported is planned: %v", c.Plan)
	}
	h.assertUntouched(before)
}

// Names Starpoint cannot map are left alone: an unknown provider is never
// translated, and an entry with no provider is never given one.
func TestCompareUnknownMappings(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.sys.With(model.Installation{Name: "curl", Path: "/usr/bin/curl"})
	m := h.writeManifest("schema = 1\n\n" +
		"[[package]]\nname = \"wget\"\nprovider = \"macports\"\n\n" +
		"[[package]]\nname = \"bat\"\n\n" +
		"[[package]]\nname = \"curl\"\n")
	c := h.compare(m).Comparison
	got := compareByName(c)
	if got["wget"].Status != engine.CompareUnsupported || got["wget"].Code != engine.PortUnsupportedProvider ||
		!strings.Contains(got["wget"].Reason, "does not map it") {
		t.Errorf("unknown provider: %+v", got["wget"])
	}
	for _, name := range []string{"bat", "curl"} {
		if got[name].Status != engine.CompareUnknown || got[name].Code != engine.PortUnknownProvider {
			t.Errorf("%s: %s/%s, want unknown/unknown-provider", name, got[name].Status, got[name].Code)
		}
	}
	if !strings.Contains(got["curl"].Reason, "only the operating system") {
		t.Errorf("curl names the system copy: %q", got["curl"].Reason)
	}
	if len(c.Plan) != 0 {
		t.Errorf("nothing unmapped is planned: %v", c.Plan)
	}
}

// When the answer would be a guess the plan refuses that entry: an entry
// with no provider that two providers have, and a package that another
// provider already has (installing it would make a second copy).
func TestComparePlanRefusesRatherThanGuess(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.brew.With(model.Installation{Name: "ruff", Version: "0.5.0"}).
		With(model.Installation{Name: "node", Version: "22.1.0"})
	h.uv.With(model.Installation{Name: "ruff", Version: "0.6.0"})
	h.npm.With(model.Installation{Name: "typescript", Version: "5.4.0"})
	m := h.writeManifest("schema = 1\n\n" +
		"[[package]]\nname = \"ruff\"\n\n" +
		"[[package]]\nname = \"node\"\nprovider = \"npm\"\n\n" +
		"[[package]]\nname = \"typescript\"\nprovider = \"npm\"\nversion = \"5.6.0\"\n")
	before := h.configBody()

	c := h.compare(m).Comparison
	got := compareByName(c)
	if r := got["ruff"]; r.Status != engine.CompareUnknown || r.Code != engine.PortAmbiguous ||
		len(r.Candidates) != 2 || r.Candidates[0] != "brew" || r.Candidates[1] != "uv" {
		t.Errorf("ambiguous: %+v", r)
	}
	n := got["node"]
	if n.Status != engine.CompareDrift || n.Code != engine.CmpProviderDrift {
		t.Errorf("node under brew is provider drift: %+v", n)
	}
	if len(n.Commands) != 2 || n.Commands[0] != "str adopt node brew" || !strings.Contains(n.Commands[1], "--allow-duplicate") {
		t.Errorf("drift names the commands that settle it by hand: %v", n.Commands)
	}
	if ts := got["typescript"]; ts.Status != engine.CompareDrift || ts.Code != engine.CmpVersionDrift ||
		len(ts.Target) != 1 || ts.Target[0].Version != "5.4.0" {
		t.Errorf("version drift: %+v", ts)
	}
	if len(c.Plan) != 0 {
		t.Errorf("the plan refuses rather than guess: %v", c.Plan)
	}
	out := h.mustRun("inventory", "--compare", m)
	for _, want := range []string{"Not planned", "[ambiguous-identity]", "[provider-drift]", "node: str adopt node brew", "Recovery plan: nothing to install."} {
		if !strings.Contains(out, want) {
			t.Errorf("want %q in:\n%s", want, out)
		}
	}
	h.assertUntouched(before)
}

// An entry with no provider is present when exactly one provider has it.
func TestCompareUnnamedResolvedByTarget(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	h.cargo.With(model.Installation{Name: "ripgrep", Version: "14.1.0"})
	m := h.writeManifest("schema = 1\n\n[[package]]\nname = \"ripgrep\"\n")
	en := compareByName(h.compare(m).Comparison)["ripgrep"]
	if en.Status != engine.ComparePresent || en.Resolved != "cargo" {
		t.Errorf("want present through cargo: %+v", en)
	}
}

func TestCompareUsage(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	m := h.writeManifest("schema = 1\n")
	for _, args := range [][]string{
		{"inventory", "--compare"},
		{"inventory", "--compare="},
		{"inventory", "--compare", m, "--portability"},
		{"inventory", "--compare", m, "--format", "toml"},
		{"inventory", "--compare", "nowhere.toml"},
	} {
		if code, _, _ := h.run(args...); code != 2 {
			t.Errorf("%v: exit %d, want 2 (usage)", args, code)
		}
	}
	h.writeManifest("schema = 1\n[[package]]\nname = \"jq\"\nflavour = \"x\"\n")
	if code, _, _ := h.run("inventory", "--compare", m); code == 0 {
		t.Error("a manifest with unknown keys is an error, not an empty comparison")
	}
}
