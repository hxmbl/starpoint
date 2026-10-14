package gem

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

// gemList is the shape `gem list --local` prints, captured from RubyGems 3.0.3.1
// on macOS: bundled gems are marked "(default: x.y.z)".
const gemList = `*** LOCAL GEMS ***

bigdecimal (default: 1.4.1)
bundler (default: 1.17.2)
CFPropertyList (2.3.6)
did_you_mean (1.3.0)
libxml-ruby (3.2.1)
minitest (default: 5.11.3)
rake (13.0.6)
`

const gemEnvironment = `RubyGems environment:
  - RUBY VERSION: ruby 2.6.10
  - INSTALLATION DIRECTORY: /Library/Ruby/Gems/2.6.0
  - USER INSTALLATION DIRECTORY: /Users/u/.gem/ruby/2.6.0
  - RUBY EXECUTABLE: /usr/bin/ruby
`

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("gem")
	p := New(r)
	r.Set("3.0.3.1\n", "gem", "--version")
	r.Set(gemList, "gem", "list", "--local")
	r.Set(gemEnvironment, "gem", "environment")
	return p, r
}

func TestHealth(t *testing.T) {
	p, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("gem should be available: %s", h.Reason)
	}
	if h.Version != "3.0.3.1" {
		t.Errorf("version parsed as %q", h.Version)
	}
	if !h.Capabilities.PinVersion {
		t.Error("gem can pin a version with -v")
	}
	if h.Capabilities.DefaultScope != model.ScopeGlobal {
		t.Errorf("gems default to the system home, got %q", h.Capabilities.DefaultScope)
	}
}

func TestHealthWhenAbsent(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	h := p.Health(context.Background())
	if h.Available {
		t.Fatal("gem is not on the fake PATH")
	}
	if h.Reason == "" {
		t.Error("an unavailable provider must say why")
	}
}

func TestParseGemListKeepsBundledGemsDistinguishable(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	insts := p.parseGemList(gemList, model.ScopeGlobal)
	if len(insts) != 7 {
		t.Fatalf("expected 7 gems, got %d: %+v", len(insts), insts)
	}
	byName := map[string]model.Installation{}
	for _, i := range insts {
		byName[i.Name] = i
	}
	if byName["rake"].Version != "13.0.6" {
		t.Errorf("unexpected rake entry: %+v", byName["rake"])
	}
	if byName["bigdecimal"].Version != "default: 1.4.1" {
		t.Errorf("a bundled gem must keep its marker so it stays distinguishable: %+v", byName["bigdecimal"])
	}
	if !IsBundled(byName["bigdecimal"].Version) {
		t.Error("IsBundled should recognise the default marker")
	}
	if IsBundled(byName["rake"].Version) {
		t.Error("a normally installed gem is not bundled")
	}
}

func TestDiscoverReportsBothHomes(t *testing.T) {
	p, _ := newProvider(t)
	if _, err := p.Discover(context.Background()); err != nil {
		t.Fatalf("discover: %v", err)
	}
	if p.installDir != "/Library/Ruby/Gems/2.6.0" {
		t.Errorf("system home not read: %q", p.installDir)
	}
	if p.userDir != "/Users/u/.gem/ruby/2.6.0" {
		t.Errorf("user home not read: %q", p.userDir)
	}
	// With no per-user home on disk the gems present are the system set.
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range insts {
		if i.Scope != model.ScopeGlobal {
			t.Errorf("before a user home exists, placement is the system set: %+v", i)
		}
	}

	// Once the per-user home exists, `gem list` reports both and Starpoint can
	// only report the per-user placement; `gem list -d` is the way to tell them
	// apart and that is not something a listing can do.
	userHome := filepath.Join(t.TempDir(), "gems")
	p.userDir, p.dirsKnown = userHome, true
	if err := os.MkdirAll(userHome, 0o755); err != nil {
		t.Fatal(err)
	}
	insts, err = p.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range insts {
		if i.Scope != model.ScopeUser {
			t.Errorf("once a user home exists the placement is per-user: %+v", i)
		}
	}
}

func TestInstalled(t *testing.T) {
	p, _ := newProvider(t)
	inst, err := p.Installed(context.Background(), model.Package{Name: "rake"})
	if err != nil || inst == nil {
		t.Fatalf("rake should be found, got (%+v, %v)", inst, err)
	}
	if inst.Provider != Name {
		t.Errorf("attribution wrong: %+v", inst)
	}
	if absent, err := p.Installed(context.Background(), model.Package{Name: "nope"}); err != nil || absent != nil {
		t.Errorf("absence must be (nil, nil), got (%+v, %v)", absent, err)
	}
}

func TestInstallHonoursAPin(t *testing.T) {
	p, r := newProvider(t)
	r.Responses["gem install rake -v 13.0.6"] = exec.Result{}
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "rake", Version: "13.0.6"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("gem", "install", "rake", "-v", "13.0.6") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestRemoveSkipsDependenciesAndIgnoresInstalledDocs(t *testing.T) {
	p, r := newProvider(t)
	r.Responses["gem uninstall -x -I rake"] = exec.Result{}
	if err := p.Remove(context.Background(), model.Package{Name: "rake"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("gem", "uninstall", "-x", "-I", "rake") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestUpdateCommands(t *testing.T) {
	p, r := newProvider(t)
	r.Responses["gem update rake"] = exec.Result{}
	r.Responses["gem update rake -v 13.0.6"] = exec.Result{}

	if _, err := p.Update(context.Background(), model.Package{Name: "rake"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("gem", "update", "rake") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Update(context.Background(), model.Package{Name: "rake", Version: "13.0.6"}); err != nil {
		t.Fatalf("update with pin: %v", err)
	}
	if !r.Ran("gem", "update", "rake", "-v", "13.0.6") {
		t.Errorf("a pin must be passed through, calls: %v", r.Calls)
	}
}

func TestDryRunDoesNotInstall(t *testing.T) {
	p, r := newProvider(t)
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "rake"}, DryRun: true,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.Ran("gem", "install", "rake") {
		t.Error("a dry run must not invoke gem")
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		stderr string
		want   provider.ErrorKind
	}{
		{"ERROR:  Could not find a valid gem 'nope'", provider.ErrNotFound},
		{"ERROR:  While executing gem ... Gem::UninstallError: can't uninstall rake (default gem)", provider.ErrUnsupported},
		{"ERROR:  You don't have write permissions for the /Library/Ruby/Gems directory", provider.ErrPermission},
		{"ERROR:  Could not reach host https://rubygems.org", provider.ErrNetwork},
		{"ERROR:  While resolving dependencies: rake conflicts with x", provider.ErrConflict},
		{"ERROR:  something entirely new", provider.ErrFailed},
	}
	p, _ := newProvider(t)
	for _, tc := range tests {
		err := p.classify("install", exec.Result{Stderr: tc.stderr, ExitCode: 1})
		pe, ok := provider.AsProviderError(err)
		if !ok || pe.Kind != tc.want {
			t.Errorf("%q classified as %v, want %q", tc.stderr, err, tc.want)
		}
	}
}

func TestUserDirExists(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	if p.userDirExists() {
		t.Error("an empty user dir does not exist")
	}
	dir := filepath.Join(t.TempDir(), "gems")
	p.userDir = dir
	if p.userDirExists() {
		t.Error("a directory that has not been created does not exist")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if !p.userDirExists() {
		t.Error("the user home should be detected once created")
	}
}
