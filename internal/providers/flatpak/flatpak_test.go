package flatpak

import (
	"context"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("flatpak")
	p := New(r)
	p.platform = func() bool { return true }
	r.Set("Flatpak 1.15.8\n", "flatpak", "--version")
	r.Set("", "flatpak", "install", "--system", "--noninteractive", "org.gimp.GIMP")
	r.Set("", "flatpak", "uninstall", "--system", "--noninteractive", "org.gimp.GIMP")
	r.Set("", "flatpak", "update", "--system", "--noninteractive", "org.gimp.GIMP")
	return p, r
}

func TestHealth(t *testing.T) {
	p, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("flatpak should be available: %s", h.Reason)
	}
	if h.Version != "1.15.8" {
		t.Errorf("version parsed as %q", h.Version)
	}
	if h.Capabilities.PinVersion {
		t.Error("Flatpak installs branches, not versions; it must not advertise a pin")
	}
	if !h.Capabilities.Channels {
		t.Error("a Flatpak branch is a channel, so Channels must be advertised")
	}
	if h.Capabilities.DefaultScope != model.ScopeGlobal {
		t.Errorf("the system installation is global, got scope %q", h.Capabilities.DefaultScope)
	}
}

func TestHealthReportsNonLinux(t *testing.T) {
	p, r := newProvider(t)
	p.platform = func() bool { return false }
	h := p.Health(context.Background())
	if h.Available {
		t.Fatal("Flatpak is Linux-only")
	}
	if !strings.Contains(h.Reason, "Linux") {
		t.Errorf("the reason should name the platform: %q", h.Reason)
	}
	if r.Ran("flatpak", "--version") {
		t.Error("an unsupported platform must not run the tool")
	}
}

func TestDiscoverListsApplicationsOnly(t *testing.T) {
	p, r := newProvider(t)
	r.Set("org.gimp.GIMP\t2.10.38\norg.videolan.VLC\t3.0.20\n", "flatpak", "list", "--system", "--app", "--columns=application,version")
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 applications, got %d", len(insts))
	}
	if insts[0].Name != "org.gimp.GIMP" || insts[0].Version != "2.10.38" {
		t.Errorf("application ID and version should be read, got %+v", insts[0])
	}
	if insts[0].Provider != Name || insts[0].Scope != model.ScopeGlobal {
		t.Errorf("ownership should be this provider's, got %+v", insts[0])
	}
}

func TestInstalled(t *testing.T) {
	p, r := newProvider(t)
	r.Set("org.gimp.GIMP\t2.10.38\n", "flatpak", "list", "--system", "--app", "--columns=application,version")
	inst, err := p.Installed(context.Background(), model.Package{Name: "org.gimp.GIMP"})
	if err != nil || inst == nil {
		t.Fatalf("the application should be reported installed, got (%+v, %v)", inst, err)
	}
	absent, err := p.Installed(context.Background(), model.Package{Name: "org.example.Missing"})
	if err != nil || absent != nil {
		t.Errorf("an absent application must not count as installed, got (%+v, %v)", absent, err)
	}
	r.SetFail(1, "error: Permission denied", "flatpak", "list", "--system", "--app", "--columns=application,version")
	if _, err := p.Installed(context.Background(), model.Package{Name: "org.gimp.GIMP"}); err == nil {
		t.Error("a Flatpak inventory failure must be returned, not treated as an absent application")
	}
}

func TestInstallUsesBranchForChannel(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "flatpak", "install", "--system", "--noninteractive", "org.gimp.GIMP//beta")
	r.Set("org.gimp.GIMP\t2.10.38\n", "flatpak", "list", "--system", "--app", "--columns=application,version")
	if _, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "org.gimp.GIMP", Channel: "beta"},
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if !r.Ran("flatpak", "install", "--system", "--noninteractive", "org.gimp.GIMP//beta") {
		t.Errorf("a channel must be passed as a Flatpak branch, calls: %v", r.Calls)
	}
}

func TestInstallDryRunRunsNothing(t *testing.T) {
	p, r := newProvider(t)
	inst, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "org.gimp.GIMP"},
		DryRun:  true,
	})
	if err != nil || inst == nil {
		t.Fatalf("dry run: (%+v, %v)", inst, err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("a dry run must not run flatpak: %v", r.Calls)
	}
}

func TestRemoveAndUpdate(t *testing.T) {
	p, r := newProvider(t)
	ctx := context.Background()
	pkg := model.Package{Name: "org.gimp.GIMP"}
	if err := p.Remove(ctx, pkg); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("flatpak", "uninstall", "--system", "--noninteractive", "org.gimp.GIMP") {
		t.Errorf("remove calls are wrong: %v", r.Calls)
	}
	r.Set("org.gimp.GIMP\t2.10.40\n", "flatpak", "list", "--system", "--app", "--columns=application,version")
	inst, err := p.Update(ctx, pkg)
	if err != nil || inst == nil || inst.Version != "2.10.40" {
		t.Fatalf("update: (%+v, %v)", inst, err)
	}
	if !r.Ran("flatpak", "update", "--system", "--noninteractive", "org.gimp.GIMP") {
		t.Errorf("update calls are wrong: %v", r.Calls)
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		output string
		kind   provider.ErrorKind
		want   string
	}{
		{"error: No remote refs found similar to ‘org.example.Missing’", provider.ErrNotFound, "remotes"},
		{"error: Requires authentication", provider.ErrPermission, "root"},
		{"error: Couldn't connect to the network", provider.ErrNetwork, "remotes"},
	}
	for _, tc := range cases {
		p, r := newProvider(t)
		r.SetFail(1, tc.output, "flatpak", "install", "--system", "--noninteractive", "org.example.App")
		_, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "org.example.App"}})
		pe, ok := provider.AsProviderError(err)
		if !ok {
			t.Fatalf("%q: expected a provider error, got %v", tc.output, err)
		}
		if pe.Kind != tc.kind {
			t.Errorf("%q: kind is %q, want %q", tc.output, pe.Kind, tc.kind)
		}
		if !strings.Contains(pe.Message, tc.want) {
			t.Errorf("%q: message %q should mention %q", tc.output, pe.Message, tc.want)
		}
	}
}

func TestExecutableDirAndPrivilege(t *testing.T) {
	p, _ := newProvider(t)
	if got := p.ExecutableDir(context.Background()); got != "/var/lib/flatpak/exports/bin" {
		t.Errorf("the system installation exports commands elsewhere: %q", got)
	}
	d := p.Privilege(context.Background(), nil, provider.PrivilegeTarget{User: "alice", UID: 4242, Home: "/home/alice"})
	if d.Privilege != provider.PrivilegeRoot || d.Reason == "" {
		t.Errorf("the system installation belongs to root: %+v", d)
	}
}
