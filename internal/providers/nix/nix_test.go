package nix

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

const profileJSON = `{
  "version": 3,
  "elements": {
    "ripgrep": {
      "active": true,
      "attrPath": "legacyPackages.aarch64-darwin.ripgrep",
      "originalUrl": "flake:nixpkgs",
      "storePaths": ["/nix/store/3n9f0x4wq2jv7k1m8pz6c5b0a9d8e7f6-ripgrep-14.1.0"],
      "url": "github:NixOS/nixpkgs/abc123"
    },
    "jq": {
      "active": true,
      "attrPath": "legacyPackages.aarch64-darwin.jq",
      "originalUrl": "flake:nixpkgs",
      "storePaths": ["/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-jq-1.7.1"],
      "url": "github:NixOS/nixpkgs/abc123"
    }
  }
}`

func profileKey(args ...string) []string {
	return append([]string{"--extra-experimental-features", "nix-command flakes"}, args...)
}

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("nix")
	p := New(r, "/home/alice")
	r.Set("nix (Nix) 2.28.3\n", "nix", "--version")
	r.Set("", "nix", profileKey("profile", "--help")...)
	r.Set(profileJSON, "nix", profileKey("profile", "list", "--json")...)
	r.Set("", "nix", profileKey("profile", "install", "nixpkgs#ripgrep")...)
	r.Set("", "nix", profileKey("profile", "remove", "ripgrep")...)
	r.Set("", "nix", profileKey("profile", "upgrade", "ripgrep")...)
	return p, r
}

func TestHealth(t *testing.T) {
	p, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("nix should be available: %s", h.Reason)
	}
	if h.Version != "2.28.3" {
		t.Errorf("version parsed as %q", h.Version)
	}
	if h.Capabilities.PinVersion {
		t.Error("a flake reference cannot name an arbitrary version; it must not advertise a pin")
	}
	if h.Capabilities.DefaultScope != model.ScopeUser {
		t.Errorf("nix profile installs into the user's profile, got scope %q", h.Capabilities.DefaultScope)
	}
}

func TestHealthReportsNixWithoutProfile(t *testing.T) {
	p, r := newProvider(t)
	r.SetFail(1, "error: unrecognised command 'profile'", "nix", profileKey("profile", "--help")...)
	h := p.Health(context.Background())
	if h.Available {
		t.Fatal("a nix-env-only Nix has no profile support and must not be advertised")
	}
	if !strings.Contains(h.Reason, "2.4") {
		t.Errorf("the reason should say what is required: %q", h.Reason)
	}
}

func TestHealthReportsMissingHome(t *testing.T) {
	p := New(testsupport.NewFakeRunner().WithBin("nix"), "")
	h := p.Health(context.Background())
	if h.Available {
		t.Fatal("without a home there is no profile to manage")
	}
}

func TestDiscoverReadsProfilesAndVersions(t *testing.T) {
	p, _ := newProvider(t)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 profile elements, got %d: %+v", len(insts), insts)
	}
	byName := map[string]model.Installation{}
	for _, inst := range insts {
		byName[inst.Name] = inst
		if inst.Provider != Name || inst.Scope != model.ScopeUser {
			t.Errorf("ownership should be this provider's, got %+v", inst)
		}
	}
	if got := byName["ripgrep"].Version; got != "14.1.0" {
		t.Errorf("the version should come from the store path, got %q", got)
	}
	if got := byName["jq"].Version; got != "1.7.1" {
		t.Errorf("the version should come from the store path, got %q", got)
	}
}

func TestDiscoverRejectsUnparsableOutput(t *testing.T) {
	p, r := newProvider(t)
	r.Set("not json", "nix", profileKey("profile", "list", "--json")...)
	_, err := p.Discover(context.Background())
	pe, ok := provider.AsProviderError(err)
	if !ok {
		t.Fatalf("expected a provider error, got %v", err)
	}
	if !strings.Contains(pe.Message, "nix profile list") {
		t.Errorf("the message should name the command that failed: %q", pe.Message)
	}
}

func TestInstalled(t *testing.T) {
	p, _ := newProvider(t)
	inst, err := p.Installed(context.Background(), model.Package{Name: "ripgrep"})
	if err != nil || inst == nil || inst.Version != "14.1.0" {
		t.Fatalf("ripgrep should be installed, got (%+v, %v)", inst, err)
	}
	absent, err := p.Installed(context.Background(), model.Package{Name: "fd"})
	if err != nil || absent != nil {
		t.Errorf("an absent package must not count as installed, got (%+v, %v)", absent, err)
	}
}

func TestInstallUsesNixpkgsFlake(t *testing.T) {
	p, r := newProvider(t)
	inst, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "ripgrep"}})
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if inst == nil || inst.Version != "14.1.0" {
		t.Errorf("install should return the installed element, got %+v", inst)
	}
	if !r.Ran("nix", profileKey("profile", "install", "nixpkgs#ripgrep")...) {
		t.Errorf("install must name the flake and package, calls: %v", r.Calls)
	}
}

func TestInstallDryRunRunsNothing(t *testing.T) {
	p, r := newProvider(t)
	inst, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "ripgrep"},
		DryRun:  true,
	})
	if err != nil || inst == nil {
		t.Fatalf("dry run: (%+v, %v)", inst, err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("a dry run must not run nix: %v", r.Calls)
	}
}

func TestRemoveAndUpdate(t *testing.T) {
	p, r := newProvider(t)
	ctx := context.Background()
	pkg := model.Package{Name: "ripgrep"}
	if err := p.Remove(ctx, pkg); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("nix", profileKey("profile", "remove", "ripgrep")...) {
		t.Errorf("remove calls are wrong: %v", r.Calls)
	}
	if _, err := p.Update(ctx, pkg); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("nix", profileKey("profile", "upgrade", "ripgrep")...) {
		t.Errorf("update must upgrade the profile element, calls: %v", r.Calls)
	}
}

func TestVersionFromStorePath(t *testing.T) {
	cases := []struct {
		path, pkg, want string
	}{
		{"/nix/store/abc-ripgrep-14.1.0", "ripgrep", "14.1.0"},
		{"/nix/store/abc-hello-1.0.0-rc1", "hello", "1.0.0-rc1"},
		{"/nix/store/abc-python3-3.12.1", "python3", "3.12.1"},
		// The store path names a derivation that is not the element: report
		// unknown rather than a version that belongs to something else.
		{"/nix/store/abc-ripgrep-14.1.0", "fd", ""},
		{"", "ripgrep", ""},
	}
	for _, tc := range cases {
		if got := versionFromStorePath(tc.path, tc.pkg); got != tc.want {
			t.Errorf("versionFromStorePath(%q, %q) = %q, want %q", tc.path, tc.pkg, got, tc.want)
		}
	}
}

func TestPackageName(t *testing.T) {
	if got := packageName("nixpkgs#ripgrep"); got != "ripgrep" {
		t.Errorf("a flake-prefixed element names the package after #, got %q", got)
	}
	if got := packageName("ripgrep"); got != "ripgrep" {
		t.Errorf("a plain element is its own package name, got %q", got)
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		output string
		kind   provider.ErrorKind
	}{
		{"error: flake 'nixpkgs' does not provide attribute 'packages.aarch64-darwin.nope'", provider.ErrNotFound},
		{"error: opening lock file '/nix/var/nix/profiles/per-user/alice/profile.lock': Permission denied", provider.ErrPermission},
		{"error: unable to download 'https://cache.nixos.org/...': Could not resolve host: cache.nixos.org", provider.ErrNetwork},
		{"error: An element with the name 'ripgrep' already exists", provider.ErrConflict},
	}
	for _, tc := range cases {
		p, r := newProvider(t)
		r.SetFail(1, tc.output, "nix", profileKey("profile", "install", "nixpkgs#ripgrep")...)
		_, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "ripgrep"}})
		pe, ok := provider.AsProviderError(err)
		if !ok {
			t.Fatalf("%q: expected a provider error, got %v", tc.output, err)
		}
		if pe.Kind != tc.kind {
			t.Errorf("%q: kind is %q, want %q", tc.output, pe.Kind, tc.kind)
		}
	}
}

func TestExecutableDirAndPrivilege(t *testing.T) {
	p, _ := newProvider(t)
	want := filepath.Join("/home/alice", ".nix-profile", "bin")
	if got := p.ExecutableDir(context.Background()); got != want {
		t.Errorf("the profile exposes commands in %q, got %q", want, got)
	}
	d := p.Privilege(context.Background(), nil, provider.PrivilegeTarget{User: "alice", UID: 4242, Home: "/home/alice"})
	if d.Privilege != provider.PrivilegeUser || d.Reason == "" {
		t.Errorf("nix installs into the user's profile: %+v", d)
	}
}
