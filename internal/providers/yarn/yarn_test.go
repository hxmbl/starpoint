package yarn

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

func newProvider(t *testing.T, version string, bins ...string) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("yarn")
	p := New(r)
	r.Set(version+"\n", "yarn", "--version")

	// A classic yarn reports its global directory; berry does not, which is part
	// of why it cannot be managed.
	global := filepath.Join(t.TempDir(), "yarn-global")
	for _, b := range bins {
		dir := filepath.Join(global, "node_modules", b)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "package.json"),
			[]byte(`{"name":"`+b+`","version":"`+versionFor(b)+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	r.Set(global+"\n", "yarn", "global", "dir")
	return p, r
}

func versionFor(name string) string {
	if name == "typescript" {
		return "5.6.3"
	}
	return "1.0.0"
}

func TestMajorVersion(t *testing.T) {
	cases := map[string]int{
		"1.22.19": 1,
		"4.18.1":  4,
		"5.0.0":   5,
		"v3.6.4":  3,
		"":        0,
		"berry":   0,
	}
	for in, want := range cases {
		if got := MajorVersion(in); got != want {
			t.Errorf("MajorVersion(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestBerryHasNoGlobalPackageManager(t *testing.T) {
	// Yarn 4 deliberately has no global commands. Saying so is the whole point of
	// implementing the provider: a machine with yarn installed should not have
	// Starpoint quietly plan `yarn global add`.
	p, _ := newProvider(t, "4.18.1")
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("yarn is installed and should be reported present: %s", h.Reason)
	}
	if h.Capabilities.Install || h.Capabilities.Discover {
		t.Errorf("berry yarn must not claim it can manage globals: %+v", h.Capabilities)
	}
	if h.Reason == "" {
		t.Error("the limitation should be explained")
	}
	if p.Classic() {
		t.Error("4.18.1 is not classic")
	}
}

func TestBerryRefusesMutations(t *testing.T) {
	p, r := newProvider(t, "4.18.1")
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "typescript"},
	}); err == nil {
		t.Fatal("berry yarn cannot install globals")
	} else if pe, _ := provider.AsProviderError(err); pe == nil || pe.Kind != provider.ErrUnsupported {
		t.Errorf("expected an unsupported error, got %v", err)
	}
	if err := p.Remove(context.Background(), model.Package{Name: "typescript"}); err == nil {
		t.Error("berry yarn cannot remove globals")
	}
	if _, err := p.Update(context.Background(), model.Package{Name: "typescript"}); err == nil {
		t.Error("berry yarn cannot update globals")
	}
	// Reading the version is expected; nothing beyond that may run.
	for _, call := range r.Calls {
		if call != "yarn --version" {
			t.Errorf("only the version probe should run, got %q", call)
		}
	}
}

func TestBerryDiscoversNothingWithoutRunningAnything(t *testing.T) {
	p, r := newProvider(t, "4.18.1")
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(insts) != 0 {
		t.Errorf("berry owns no global packages, got %+v", insts)
	}
	if r.Ran("yarn", "global", "dir") {
		t.Errorf("berry must not be asked for a global directory, calls: %v", r.Calls)
	}
}

func TestClassicReportsFullCapabilities(t *testing.T) {
	p, _ := newProvider(t, "1.22.19", "typescript")
	h := p.Health(context.Background())
	if !h.Capabilities.Install || !h.Capabilities.Discover || !h.Capabilities.PinVersion {
		t.Errorf("classic yarn manages globals: %+v", h.Capabilities)
	}
	if !p.Classic() {
		t.Error("1.22.19 is classic")
	}
}

func TestClassicDiscoversFromTheGlobalDirectory(t *testing.T) {
	p, _ := newProvider(t, "1.22.19", "typescript", "tsc")
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 global packages, got %+v", insts)
	}
	for _, i := range insts {
		if i.Provider != Name {
			t.Errorf("attribution wrong: %+v", i)
		}
		if i.Version == "" {
			t.Errorf("version should come from the package manifest: %+v", i)
		}
	}
}

func TestClassicInstallAndRemoveCommands(t *testing.T) {
	p, r := newProvider(t, "1.22.19", "typescript")
	r.Responses["yarn global add typescript@5.6.3"] = exec.Result{}
	r.Responses["yarn global remove typescript"] = exec.Result{}

	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "typescript", Version: "5.6.3"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("yarn", "global", "add", "typescript@5.6.3") {
		t.Errorf("calls: %v", r.Calls)
	}
	if err := p.Remove(context.Background(), model.Package{Name: "typescript"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("yarn", "global", "remove", "typescript") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestClassicDryRunDoesNotInstall(t *testing.T) {
	p, r := newProvider(t, "1.22.19")
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "typescript"}, DryRun: true,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.Ran("yarn", "global", "add", "typescript") {
		t.Error("a dry run must not invoke yarn")
	}
}

func TestAbsentYarnOwnsNothing(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	h := p.Health(context.Background())
	if h.Available {
		t.Fatal("yarn is not on the fake PATH")
	}
	if insts, err := p.Discover(context.Background()); err != nil || len(insts) != 0 {
		t.Errorf("an absent provider owns nothing, got (%+v, %v)", insts, err)
	}
}

func TestPackageJSONParsingRejectsUnusableManifests(t *testing.T) {
	dir := t.TempDir()
	if _, _, ok := readPackageJSON(filepath.Join(dir, "missing.json")); ok {
		t.Error("a missing manifest is not a package")
	}
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readPackageJSON(bad); ok {
		t.Error("unparseable JSON is not a package")
	}
	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{"version":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := readPackageJSON(empty); ok {
		t.Error("a manifest with no name is not usable here")
	}
}
