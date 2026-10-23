package providers

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// wantPrivilege is the classification of every built-in provider whose
// answer does not depend on where it installs. It is the table the README
// documents under "Running as root"; a provider added without a line here fails
// TestEveryBuiltInProviderIsClassified.
var wantPrivilege = map[string]provider.Privilege{
	"uv":      provider.PrivilegeUser,
	"pip":     provider.PrivilegeUser,
	"cargo":   provider.PrivilegeUser,
	"go":      provider.PrivilegeUser,
	"rustup":  provider.PrivilegeUser,
	"nix":     provider.PrivilegeUser,
	"apt":     provider.PrivilegeRoot,
	"dnf":     provider.PrivilegeRoot,
	"pacman":  provider.PrivilegeRoot,
	"system":  provider.PrivilegeRoot,
	"flatpak": provider.PrivilegeRoot,
	"snap":    provider.PrivilegeRoot,
}

// byLocation are the providers classified by where they install, and the
// command each asks (as the invoking user) to find out.
var byLocation = map[string][]string{
	"npm":  {"npm", "config", "get", "prefix"},
	"pnpm": {"pnpm", "root", "-g"},
	"yarn": {"yarn", "global", "dir"},
	"gem":  {"gem", "environment", "gemdir"},
	"brew": {"brew", "--prefix"},
}

func target(home string) provider.PrivilegeTarget {
	return provider.PrivilegeTarget{User: "alice", UID: 4242, Home: home}
}

func TestEveryBuiltInProviderIsClassified(t *testing.T) {
	var names []string
	for _, p := range DefaultRegistry().All() {
		names = append(names, p.Name())
		if _, ok := p.(provider.Privileged); !ok {
			t.Errorf("%s does not say which account it runs as under sudo", p.Name())
		}
		_, static := wantPrivilege[p.Name()]
		_, dynamic := byLocation[p.Name()]
		if static == dynamic {
			t.Errorf("%s must be in exactly one of the classification tables", p.Name())
		}
	}
	sort.Strings(names)
	if len(names) != len(wantPrivilege)+len(byLocation) {
		t.Errorf("the tables and the registry disagree: %v", names)
	}
}

func TestStaticClassification(t *testing.T) {
	q := testsupport.NewFakeRunner()
	for _, p := range DefaultRegistry().All() {
		want, ok := wantPrivilege[p.Name()]
		if !ok {
			continue
		}
		d := p.(provider.Privileged).Privilege(context.Background(), q, target("/home/alice"))
		if d.Privilege != want || d.Reason == "" {
			t.Errorf("%s: got %+v, want %s with a reason", p.Name(), d, want)
		}
	}
	if len(q.Calls) != 0 {
		t.Errorf("a static classification runs nothing: %v", q.Calls)
	}
}

func TestClassificationByLocation(t *testing.T) {
	home := t.TempDir()
	mine := t.TempDir() // owned by whoever runs the suite
	reg := DefaultRegistry()
	for name, query := range byLocation {
		p, _ := reg.Get(name)
		classify := func(answer string, fail bool, tg provider.PrivilegeTarget) provider.PrivilegeDecision {
			q := testsupport.NewFakeRunner()
			if fail {
				q.SetFail(1, "not installed", query[0], query[1:]...)
			} else {
				q.Set(answer+"\n", query[0], query[1:]...)
			}
			d := p.(provider.Privileged).Privilege(context.Background(), q, tg)
			if !q.Ran(query[0], query[1:]...) {
				t.Errorf("%s: asks `%s` as the user", name, strings.Join(query, " "))
			}
			return d
		}
		if d := classify(filepath.Join(home, ".npm-global"), false, target(home)); d.Privilege != provider.PrivilegeUser ||
			!strings.Contains(d.Reason, "alice's home") {
			t.Errorf("%s: a location in the user's home runs as the user: %+v", name, d)
		}
		if d := classify("/usr", false, target(home)); d.Privilege != provider.PrivilegeRoot ||
			!strings.Contains(d.Reason, "system location") {
			t.Errorf("%s: a system location runs as root: %+v", name, d)
		}
		if d := classify("", true, target(home)); d.Privilege != provider.PrivilegeRoot ||
			!strings.Contains(d.Reason, "could not be determined") {
			t.Errorf("%s: an unknown location runs as root, as before: %+v", name, d)
		}
		owner := provider.PrivilegeTarget{User: "alice", UID: os.Getuid(), Home: home}
		if os.Getuid() > 0 {
			if d := classify(mine, false, owner); d.Privilege != provider.PrivilegeUser || !strings.Contains(d.Reason, "belongs to alice") {
				t.Errorf("%s: a prefix the user owns outside their home runs as the user: %+v", name, d)
			}
		}
	}
	brew, _ := reg.Get("brew")
	q := testsupport.NewFakeRunner().Set("/usr/local\n", "brew", "--prefix")
	if d := brew.(provider.Privileged).Privilege(context.Background(), q, target(home)); !strings.Contains(d.Reason, "refuses to run as root") {
		t.Errorf("Homebrew's own rule is named: %+v", d)
	}
}

// counting is a provider whose classification is counted.
type counting struct {
	*testsupport.FakeProvider
	calls int
}

func (c *counting) Privilege(ctx context.Context, q exec.Runner, t provider.PrivilegeTarget) provider.PrivilegeDecision {
	c.calls++
	return c.FakeProvider.Privilege(ctx, q, t)
}

func identity(t *testing.T) *exec.Identity {
	home := t.TempDir()
	return &exec.Identity{User: "alice", UID: 4242, GID: 4243, Home: home, Env: []string{"HOME=" + home, "PATH=/usr/bin:/bin"}}
}

func TestPrivilegesDecideOncePerProvider(t *testing.T) {
	c := &counting{FakeProvider: testsupport.NewFakeProvider("uv", provider.KindPython)}
	c.RunsAs = provider.PrivilegeUser
	pv := &Privileges{Identity: identity(t), Elevated: true}
	for i := 0; i < 3; i++ {
		if d := pv.Decide(context.Background(), c); d.Privilege != provider.PrivilegeUser {
			t.Fatalf("got %+v", d)
		}
	}
	if c.calls != 1 {
		t.Errorf("classified %d times; a run classifies each provider once", c.calls)
	}
}

func TestRunnersFollowTheDecision(t *testing.T) {
	id := identity(t)
	pv := &Privileges{Identity: id, Elevated: true}
	user := testsupport.NewFakeProvider("uv", provider.KindPython)
	user.RunsAs = provider.PrivilegeUser
	root := testsupport.NewFakeProvider("apt", provider.KindApt)
	t.Setenv("HOME", "/root")
	if got := exec.Getenv(pv.Runner(func() provider.Provider { return user }), "HOME"); got != id.Home {
		t.Errorf("a user-level provider's commands get the user's environment: HOME=%q", got)
	}
	if got := exec.Getenv(pv.Runner(func() provider.Provider { return root }), "HOME"); got != "/root" {
		t.Errorf("a root provider's commands get Starpoint's: HOME=%q", got)
	}
	if _, ok := (&Privileges{}).Runner(func() provider.Provider { return user }).(exec.RealRunner); !ok {
		t.Error("without an identity every provider gets a plain runner")
	}
	var nilPV *Privileges
	if _, ok := nilPV.Runner(func() provider.Provider { return user }).(exec.RealRunner); !ok {
		t.Error("a nil Privileges is an ordinary run")
	}
}

func TestRunAsRecords(t *testing.T) {
	ctx := context.Background()
	id := identity(t)
	user := testsupport.NewFakeProvider("cargo", provider.KindRust)
	user.RunsAs = provider.PrivilegeUser
	root := testsupport.NewFakeProvider("apt", provider.KindApt)
	self := Self()

	pv := &Privileges{Identity: id, Elevated: true}
	if got := pv.RunAs(ctx, user); got != (model.RunAs{Provider: "cargo", User: "alice", UID: 4242, GID: 4243, Elevated: true,
		Dropped: true, Privilege: "user", Reason: "fake cargo installs into the user's home"}) {
		t.Errorf("dropped: %+v", got)
	}
	if got := pv.RunAs(ctx, root); got.Dropped || got.Privilege != "root" || got.UID != self.UID || !got.Elevated {
		t.Errorf("root provider: %+v", got)
	}
	if got := (&Privileges{Elevated: true}).RunAs(ctx, user); got.Dropped || !strings.Contains(got.Reason, "plain root run") {
		t.Errorf("plain root: %+v", got)
	}
	if got := (&Privileges{Elevated: true, Unavailable: "no such user"}).RunAs(ctx, user); got.Dropped || got.Reason != "no such user" {
		t.Errorf("unavailable: %+v", got)
	}
	if got := (*Privileges)(nil).RunAs(ctx, user); got.Elevated || got.Dropped || got.Provider != "cargo" || got.UID != self.UID {
		t.Errorf("ordinary: %+v", got)
	}
}

func TestUnclassifiedProvidersRunAsRoot(t *testing.T) {
	pv := &Privileges{Identity: identity(t), Elevated: true}
	d := pv.Decide(context.Background(), plain{testsupport.NewFakeProvider("mystery", provider.KindSystem)})
	if d.Privilege != provider.PrivilegeRoot || !strings.Contains(d.Reason, "does not say") {
		t.Errorf("got %+v", d)
	}
}

// plain hides the fake's classification.
type plain struct{ provider.Provider }

// Commands that run as root in an elevated run acting for a user get the
// root environment, with root's home, not Starpoint's retargeted one.
func TestRootRunnerUsesTheRootEnvironment(t *testing.T) {
	pv := &Privileges{Elevated: true, RootEnv: []string{"PATH=/usr/bin:/bin", "HOME=/root"}}
	r := pv.Runner(func() provider.Provider { return nil })
	if got := exec.Getenv(r, "HOME"); got != "/root" {
		t.Errorf("a root command's HOME is %q, want /root", got)
	}
}
