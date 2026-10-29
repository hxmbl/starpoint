package apt

import (
	"context"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("apt-get")
	p := New(r)
	r.Set("apt 2.6.1 (amd64)", "apt-get", "--version")
	// Successful mutations produce no output.
	r.Set("", "apt-get", "install", "-y", "git=1:2.43.0-1")
	r.Set("", "apt-get", "install", "-y", "-t", "main", "git")
	r.Set("", "apt-get", "remove", "-y", "git")
	r.Set("", "apt-get", "install", "-y", "--only-upgrade", "git")
	return p, r
}

func TestHealth(t *testing.T) {
	p, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("apt should be available: %s", h.Reason)
	}
	if h.Version != "2.6.1" {
		t.Errorf("version parsed as %q", h.Version)
	}
	if !h.Capabilities.PinVersion {
		t.Error("apt can install pkg=version and should advertise it")
	}
	if h.Capabilities.DefaultScope != model.ScopeGlobal {
		t.Errorf("apt installs system-wide, got scope %q", h.Capabilities.DefaultScope)
	}
}

func TestDiscoverParsesDpkgOutput(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git\t1:2.43.0-1\nripgrep\t13.0.0-3\n", "dpkg-query", "-W", "-f="+dpkgFormat)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 packages, got %d", len(insts))
	}
	if insts[0].Name != "git" || insts[0].Version != "1:2.43.0-1" {
		t.Errorf("epoch-bearing versions must survive, got %+v", insts[0])
	}
	if insts[0].Scope != model.ScopeGlobal {
		t.Errorf("apt packages are global, got %q", insts[0].Scope)
	}
}

func TestInstalledUsesVersionAsPresenceSignal(t *testing.T) {
	p, r := newProvider(t)
	// dpkg reports config-files-only packages with an empty version field.
	r.Set("git\t1:2.43.0-1\n", "dpkg-query", "-W", "-f="+dpkgFormat, "git")
	inst, err := p.Installed(context.Background(), model.Package{Name: "git"})
	if err != nil || inst == nil {
		t.Fatalf("git should be reported installed, got (%+v, %v)", inst, err)
	}

	r.Set("oldpkg\t\n", "dpkg-query", "-W", "-f="+dpkgFormat, "oldpkg")
	absent, err := p.Installed(context.Background(), model.Package{Name: "oldpkg"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if absent != nil {
		t.Errorf("a config-files-only package must not count as installed, got %+v", absent)
	}
}

func TestInstallWithVersionPin(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git\t1:2.43.0-1\n", "dpkg-query", "-W", "-f="+dpkgFormat, "git")
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "git", Version: "1:2.43.0-1"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("apt-get", "install", "-y", "git=1:2.43.0-1") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestInstallWithArchiveChannel(t *testing.T) {
	p, r := newProvider(t)
	r.Set("git\t1:2.43.0-1\n", "dpkg-query", "-W", "-f="+dpkgFormat, "git")
	pkg := model.Package{Name: "git", Channel: ChannelMain}
	if _, err := p.Install(context.Background(), provider.Request{Package: pkg}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("apt-get", "install", "-y", "-t", "main", "git") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestRemoveAndUpgradeCommands(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "apt-get", "remove", "-y", "git")
	r.Set("", "apt-get", "install", "-y", "--only-upgrade", "git")
	r.Set("git\t2.44.0-1\n", "dpkg-query", "-W", "-f="+dpkgFormat, "git")

	if err := p.Remove(context.Background(), model.Package{Name: "git"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("apt-get", "remove", "-y", "git") {
		t.Errorf("calls: %v", r.Calls)
	}
	if _, err := p.Update(context.Background(), model.Package{Name: "git"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("apt-get", "install", "-y", "--only-upgrade", "git") {
		t.Errorf("calls: %v", r.Calls)
	}
}

func TestDryRunDoesNotInstall(t *testing.T) {
	p, r := newProvider(t)
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "git"}, DryRun: true,
	}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if r.Ran("apt-get", "install", "-y", "git") {
		t.Error("a dry run must not run apt-get install")
	}
}

func TestClassifyErrors(t *testing.T) {
	tests := []struct {
		name   string
		stderr string
		want   provider.ErrorKind
	}{
		{"missing", "E: Unable to locate package zzz", provider.ErrNotFound},
		{"permission", "E: Could not open lock file /var/lib/dpkg/lock-frontend - open (13: Permission denied)", provider.ErrPermission},
		{"network", "Err:Could not resolve host:deb.debian.org", provider.ErrNetwork},
		{"conflict", "E: Depends: libfoo (= 1.0) but it is not going to be installed", provider.ErrConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := classify("install", exec.Result{Stderr: tc.stderr, ExitCode: 100})
			pe, ok := provider.AsProviderError(err)
			if !ok || pe.Kind != tc.want {
				t.Fatalf("classified as %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPermissionErrorNamesRoot(t *testing.T) {
	err := classify("install", exec.Result{Stderr: "E: Are you root?", ExitCode: 100})
	pe, _ := provider.AsProviderError(err)
	if pe == nil || pe.Kind != provider.ErrPermission {
		t.Fatalf("expected a permission error, got %v", err)
	}
	if pe.Message == "" {
		t.Error("the message must tell the user what to do")
	}
}

// dpkg qualifies Multi-Arch: same packages with the native architecture
// (libc6:arm64). Seen on a real Debian trixie arm64 host: the qualifier made
// a captured manifest name an arm64-only package, so an amd64 machine would
// have been planned a foreign-architecture install, and a configuration
// entry "zlib1g" never matched the installed "zlib1g:arm64". The native
// qualifier is dropped; a foreign one is a different package and is kept.
func TestDiscoverDropsTheNativeArchitectureQualifier(t *testing.T) {
	p, r := newProvider(t)
	r.Set("arm64\n", "dpkg", "--print-architecture")
	r.Set("libc6:arm64\t2.41-12\nlibc6:armhf\t2.41-12\ngit\t1:2.47.3-0\nzlib1g:arm64\t1:1.3.dfsg\n", "dpkg-query", "-W", "-f="+dpkgFormat)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	var names []string
	for _, in := range insts {
		names = append(names, in.Name)
	}
	if got := strings.Join(names, " "); got != "libc6 libc6:armhf git zlib1g" {
		t.Errorf("names = %q, want the native qualifier dropped and the foreign one kept", got)
	}
	r.Set("zlib1g:arm64\t1:1.3.dfsg\n", "dpkg-query", "-W", "-f="+dpkgFormat, "zlib1g")
	inst, err := p.Installed(context.Background(), model.Package{Name: "zlib1g"})
	if err != nil || inst == nil || inst.Name != "zlib1g" {
		t.Errorf("Installed should report the unqualified name: %+v, %v", inst, err)
	}
	marks, err := func() (map[string]bool, error) {
		r.Set("libc6\ngit\n", "apt-mark", "showmanual")
		return p.Requested(context.Background(), insts)
	}()
	if err != nil || !marks["libc6"] || marks["zlib1g"] || !marks["libc6:armhf"] {
		t.Errorf("manual marks still match: %v, %v", marks, err)
	}
}

// When dpkg cannot say its architecture, names are left as dpkg wrote them.
func TestDiscoverKeepsQualifiersWhenTheArchitectureIsUnknown(t *testing.T) {
	p, r := newProvider(t)
	r.Set("libc6:arm64\t2.41-12\n", "dpkg-query", "-W", "-f="+dpkgFormat)
	insts, err := p.Discover(context.Background())
	if err != nil || len(insts) != 1 || insts[0].Name != "libc6:arm64" {
		t.Errorf("want libc6:arm64 unchanged: %+v, %v", insts, err)
	}
}
