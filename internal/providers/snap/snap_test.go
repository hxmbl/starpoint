package snap

import (
	"context"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

const snapVersion = `snap    2.61.3
snapd   2.61.3
series  16
ubuntu  24.04
kernel  6.8.0-41-generic
`

const snapList = `Name      Version    Rev    Tracking       Publisher   Notes
core22    20240408   1380   latest/stable  canonical✓  base
firefox   124.0.2-1  3836   latest/stable  mozilla✓    -
`

func newProvider(t *testing.T) (*Provider, *testsupport.FakeRunner) {
	t.Helper()
	r := testsupport.NewFakeRunner().WithBin("snap")
	p := New(r)
	p.platform = func() bool { return true }
	r.Set(snapVersion, "snap", "version")
	r.Set(snapList, "snap", "list")
	r.Set("firefox  124.0.2-1  3836  latest/stable  mozilla✓  -\n", "snap", "list", "firefox")
	r.Set("", "snap", "install", "firefox")
	r.Set("", "snap", "remove", "firefox")
	r.Set("", "snap", "refresh", "firefox")
	return p, r
}

func TestHealth(t *testing.T) {
	p, _ := newProvider(t)
	h := p.Health(context.Background())
	if !h.Available {
		t.Fatalf("snap should be available: %s", h.Reason)
	}
	if h.Version != "2.61.3" {
		t.Errorf("version parsed as %q", h.Version)
	}
	if h.Capabilities.PinVersion {
		t.Error("snap installs revisions and channels, not versions; it must not advertise a pin")
	}
	if !h.Capabilities.Channels {
		t.Error("snap channels must be advertised")
	}
	if h.Capabilities.DefaultScope != model.ScopeGlobal {
		t.Errorf("snaps are installed system-wide, got scope %q", h.Capabilities.DefaultScope)
	}
}

func TestHealthReportsSnapdFailure(t *testing.T) {
	p, r := newProvider(t)
	r.SetFail(1, "error: cannot communicate with server: Post \"http://localhost/v2/snaps\": dial unix /run/snapd.socket: connect: no such file or directory", "snap", "version")
	h := p.Health(context.Background())
	if h.Available {
		t.Fatal("snap without snapd is not usable")
	}
	if !strings.Contains(h.Reason, "snapd.socket") {
		t.Errorf("the reason should carry the diagnosis: %q", h.Reason)
	}
}

func TestDiscoverSkipsTheHeader(t *testing.T) {
	p, _ := newProvider(t)
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if len(insts) != 2 {
		t.Fatalf("expected 2 snaps, got %d: %+v", len(insts), insts)
	}
	if insts[0].Name != "core22" || insts[0].Version != "20240408" {
		t.Errorf("the first snap should be core22 20240408, got %+v", insts[0])
	}
	for _, inst := range insts {
		if inst.Name == "Name" {
			t.Error("the header row is not a snap")
		}
		if inst.Provider != Name || inst.Scope != model.ScopeGlobal {
			t.Errorf("ownership should be this provider's, got %+v", inst)
		}
	}
}

func TestInstalled(t *testing.T) {
	p, r := newProvider(t)
	inst, err := p.Installed(context.Background(), model.Package{Name: "firefox"})
	if err != nil || inst == nil || inst.Version != "124.0.2-1" {
		t.Fatalf("firefox should be installed, got (%+v, %v)", inst, err)
	}
	r.SetFail(1, "error: no matching snaps installed", "snap", "list", "missing")
	absent, err := p.Installed(context.Background(), model.Package{Name: "missing"})
	if err != nil || absent != nil {
		t.Errorf("an absent snap must not count as installed, got (%+v, %v)", absent, err)
	}
	r.SetFail(1, "error: cannot communicate with server: connection refused", "snap", "list", "firefox")
	if _, err := p.Installed(context.Background(), model.Package{Name: "firefox"}); err == nil {
		t.Error("a snapd query failure must be returned, not treated as an absent snap")
	}
}

func TestInstallPassesTheChannel(t *testing.T) {
	p, r := newProvider(t)
	r.Set("", "snap", "install", "--channel=edge", "code")
	r.Set("code  1.90.0  1500  edge  vscode✓  classic\n", "snap", "list", "code")
	inst, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "code", Channel: "edge"},
	})
	if err != nil || inst == nil {
		t.Fatalf("install: (%+v, %v)", inst, err)
	}
	if !r.Ran("snap", "install", "--channel=edge", "code") {
		t.Errorf("the channel must be forwarded, calls: %v", r.Calls)
	}
}

func TestInstallDryRunRunsNothing(t *testing.T) {
	p, r := newProvider(t)
	inst, err := p.Install(context.Background(), provider.Request{
		Package: model.Package{Name: "firefox"},
		DryRun:  true,
	})
	if err != nil || inst == nil {
		t.Fatalf("dry run: (%+v, %v)", inst, err)
	}
	if len(r.Calls) != 0 {
		t.Errorf("a dry run must not run snap: %v", r.Calls)
	}
}

func TestRemoveAndUpdate(t *testing.T) {
	p, r := newProvider(t)
	ctx := context.Background()
	pkg := model.Package{Name: "firefox"}
	if err := p.Remove(ctx, pkg); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !r.Ran("snap", "remove", "firefox") {
		t.Errorf("remove calls are wrong: %v", r.Calls)
	}
	if _, err := p.Update(ctx, pkg); err != nil {
		t.Fatalf("update: %v", err)
	}
	if !r.Ran("snap", "refresh", "firefox") {
		t.Errorf("update must refresh the snap: %v", r.Calls)
	}
}

// Classic confinement grants a snap unrestricted access to the machine. It is
// never something Starpoint decides on the user's behalf.
func TestClassicConfinementIsNotGrantedImplicitly(t *testing.T) {
	p, r := newProvider(t)
	r.SetFail(1, "error: This revision of snap \"code\" was published using classic confinement and thus may perform arbitrary system changes outside of the security sandbox", "snap", "install", "code")
	_, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "code"}})
	pe, ok := provider.AsProviderError(err)
	if !ok || pe.Kind != provider.ErrConflict {
		t.Fatalf("classic confinement should be a conflict, got %v", err)
	}
	if !strings.Contains(pe.Message, "classic confinement") || !strings.Contains(pe.Message, "will not") {
		t.Errorf("the message must explain the refusal: %q", pe.Message)
	}
	if r.Ran("snap", "install", "--classic", "code") {
		t.Error("Starpoint must never add --classic on its own")
	}
}

func TestErrorClassification(t *testing.T) {
	cases := []struct {
		output string
		kind   provider.ErrorKind
	}{
		{"error: snap \"nope\" not found", provider.ErrNotFound},
		{"error: access denied (try with sudo)", provider.ErrPermission},
		{"error: cannot communicate with server: connection refused", provider.ErrNetwork},
		{"error: snap \"firefox\" is already installed", provider.ErrConflict},
	}
	for _, tc := range cases {
		p, r := newProvider(t)
		r.SetFail(1, tc.output, "snap", "install", "nope")
		_, err := p.Install(context.Background(), provider.Request{Package: model.Package{Name: "nope"}})
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
	if got := p.ExecutableDir(context.Background()); got != "/snap/bin" {
		t.Errorf("snapd links commands into /snap/bin, got %q", got)
	}
	d := p.Privilege(context.Background(), nil, provider.PrivilegeTarget{User: "alice", UID: 4242, Home: "/home/alice"})
	if d.Privilege != provider.PrivilegeRoot || d.Reason == "" {
		t.Errorf("snaps are system-wide: %+v", d)
	}
}
