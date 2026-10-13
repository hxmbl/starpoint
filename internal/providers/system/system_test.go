package system

import (
	"context"
	"runtime"
	"testing"

	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

func TestCapabilitiesAreReadOnly(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	caps := p.Capabilities()
	if !caps.Discover {
		t.Error("the system provider can see what is installed")
	}
	if caps.Install || caps.Remove || caps.Update {
		t.Errorf("the system provider must not claim to install, remove or update: %+v", caps)
	}
	if caps.DefaultScope != model.ScopeGlobal {
		t.Errorf("system software is global, got %q", caps.DefaultScope)
	}
}

func TestMutationsAreRefusedWithExplanation(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	ctx := context.Background()

	if _, err := p.Install(ctx, provider.Request{Package: model.Package{Name: "git"}}); err == nil {
		t.Error("install must be refused")
	} else if pe, _ := provider.AsProviderError(err); pe == nil || pe.Kind != provider.ErrUnsupported {
		t.Errorf("expected an unsupported error, got %v", err)
	}
	if err := p.Remove(ctx, model.Package{Name: "git"}); err == nil {
		t.Error("remove must be refused")
	}
	if _, err := p.Update(ctx, model.Package{Name: "git"}); err == nil {
		t.Error("update must be refused")
	}
}

func TestHealthIsAlwaysAvailableOnUnix(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	h := p.Health(context.Background())
	switch runtime.GOOS {
	case "darwin", "linux", "freebsd":
		if !h.Available {
			t.Fatalf("the system provider should be available: %s", h.Reason)
		}
	default:
		if h.Available {
			t.Fatalf("unexpected platform %s should report unavailable", runtime.GOOS)
		}
	}
	if h.Name != Name {
		t.Errorf("health reports the wrong name: %q", h.Name)
	}
}

func TestDirsAreSystemPathsOnly(t *testing.T) {
	for _, d := range Dirs() {
		switch d {
		case "/bin", "/sbin", "/usr/bin", "/usr/sbin":
		default:
			t.Errorf("%q is not a system directory", d)
		}
	}
}

func TestOwnsPath(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("system paths differ on this platform")
	}
	cases := map[string]bool{
		"/usr/bin/git":            true,
		"/bin/ls":                 true,
		"/usr/sbin/sshd":          true,
		"/usr/bin":                true,
		"/opt/homebrew/bin/rg":    false,
		"/usr/local/bin/python3":  false,
		"/usr/binaries/git":       false,
		"":                        false,
		"/home/u/.local/bin/ruff": false,
	}
	for path, want := range cases {
		if got := OwnsPath(path); got != want {
			t.Errorf("OwnsPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestDiscoverOnlyReturnsExecutablesInSystemDirs(t *testing.T) {
	p := New(testsupport.NewFakeRunner())
	insts, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	// The host has system binaries; the assertion is that anything returned is
	// attributed correctly rather than about a specific count.
	for _, i := range insts {
		if i.Provider != Name {
			t.Errorf("attribution wrong: %+v", i)
		}
		if i.Scope != model.ScopeGlobal {
			t.Errorf("scope wrong: %+v", i)
		}
		if !OwnsPath(i.Path) {
			t.Errorf("discovered a path outside the system directories: %+v", i)
		}
	}
}
