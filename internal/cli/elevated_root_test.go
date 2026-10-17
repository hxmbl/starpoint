//go:build linux || darwin

package cli

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/elevation"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers"
	"github.com/hxmbl/starpoint/internal/providers/uv"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// TestAsRootSudoInstallLeavesNothingRootOwnedInTheUsersHome is the safety
// net end to end, and needs root: CI runs it with sudo. The invoking user is
// nobody (65534), whose home is the harness's directory, which holds the
// configuration and the provider's bin directory. The fake uv is classified
// user-level but writes in-process, as root: exactly what a provider that
// escalated on its own would leave. The file is still given back, and the
// fix is marked unexpected and warned about.
func TestAsRootSudoInstallLeavesNothingRootOwnedInTheUsersHome(t *testing.T) {
	requireRoot(t)
	h := newHarness(t, "schema = 1\n")
	home := filepath.Dir(h.config)
	bin := filepath.Join(home, ".local", "bin") // does not exist yet: the install creates it
	h.uv.ExeDir = bin
	h.uv.InstallPath = bin
	h.uv.InstallHook = func(req provider.Request) {
		testsupport.WriteExecutable(t, filepath.Join(bin, req.Package.Name), "#!/bin/sh\n")
	}
	h.elevated(elevation.Status{Elevated: true, Mechanism: "effective user id 0, root", Home: "/root",
		InvokingUser: "nobody", Via: "sudo", InvokingUID: 65534, InvokingGID: 65534, InvokingHome: home})

	op := decodeOperation(t, h.mustRun("install", "cowsay", "--provider", "uv", "--yes", "--allow-root", "--json"))
	exe := filepath.Join(bin, "cowsay")
	stateDir, _ := config.StateDir(h.config)
	for _, path := range []string{filepath.Join(home, ".local"), bin, exe, h.config, stateDir} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if st := info.Sys().(*syscall.Stat_t); st.Uid != 65534 || st.Gid != 65534 {
			t.Errorf("%s is owned by %d:%d; nothing under the invoking user's home may be left to root", path, st.Uid, st.Gid)
		}
	}
	var reported, warned bool
	for _, a := range op.Actions {
		if eff := findEffect(a.Effects, model.EffectChown, model.EffectExecutable, exe); eff != nil && eff.Unexpected {
			reported = true
		}
		for _, w := range a.Warnings {
			if w.Code == model.WarnRootOwnedAfterDrop {
				warned = true
			}
		}
	}
	if !reported || !warned {
		t.Errorf("the chown is reported on the install, marked unexpected, with a %s warning: %+v", model.WarnRootOwnedAfterDrop, op.Actions)
	}
	var logged bool
	for _, e := range h.withEvent("action") {
		if e.Command == "align" && len(e.Effects) > 0 {
			logged = true
		}
	}
	if !logged {
		t.Error("giving Starpoint's own files back is in the audit log")
	}
}

// fakeUV is a stand-in for uv that does what uv does to the filesystem:
// tool environments under ~/.local/share/uv/tools and a command in
// ~/.local/bin (or UV_TOOL_BIN_DIR), and records who it ran as.
const fakeUV = `#!/bin/sh
tools="${UV_TOOL_DIR:-$HOME/.local/share/uv/tools}"
bin="${UV_TOOL_BIN_DIR:-$HOME/.local/bin}"
case "$1 $2" in
"--version ") echo "uv 0.5.0" ;;
"tool list")
	[ -d "$tools" ] || exit 0
	for t in $(ls "$tools"); do echo "$t v1.0.0"; echo "- $t"; done ;;
"tool install")
	mkdir -p "$bin" "$tools/$3" || exit 1
	id -u > "$tools/$3/installed-by"
	printf '#!/bin/sh\necho ok\n' > "$bin/$3" && chmod 755 "$bin/$3" ;;
*) echo "unexpected: $*" >&2; exit 2 ;;
esac
`

// TestAsRootUserLevelProviderRunsAsTheInvokingUser is privilege dropping end
// to end: the real uv provider, through the real runner, starts a fake uv
// as nobody. Everything it writes in nobody's home is nobody's from the
// start, so the install carries no chown effect at all, and run_as says the
// privileges were dropped.
func TestAsRootUserLevelProviderRunsAsTheInvokingUser(t *testing.T) {
	requireRoot(t)
	h := newHarness(t, "schema = 1\n")
	top := t.TempDir()
	for dir := top; dir != os.TempDir() && dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(top, "home")
	tools := filepath.Join(top, "fakebin")
	for _, dir := range []string{home, tools} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chown(home, 65534, 65534); err != nil {
		t.Fatal(err)
	}
	testsupport.WriteExecutable(t, filepath.Join(tools, "uv"), fakeUV)

	h.env.GlobalConfig = filepath.Join(home, ".starpoint.toml")
	testsupport.WriteFile(t, h.env.GlobalConfig, "schema = 1\n")
	h.env.Registry = nil
	h.env.NewRegistry = func(pv *providers.Privileges) *provider.Registry {
		reg := provider.NewRegistry()
		var uvP *uv.Provider
		uvP = uv.New(pv.Runner(func() provider.Provider { return uvP }), home)
		reg.Register(uvP)
		return reg
	}
	h.env.Environ = func() []string {
		// Root's own locations must not reach the user's uv.
		return append(os.Environ(), "PATH="+tools+":/usr/bin:/bin", "UV_TOOL_DIR=/root/.local/share/uv/tools")
	}
	h.env.Elevation = func() elevation.Status {
		return elevation.Status{Elevated: true, Mechanism: "effective user id 0, root", Home: "/root",
			InvokingUser: "nobody", Via: "sudo", InvokingUID: 65534, InvokingGID: 65534, InvokingHome: home,
			InvokingGroups: []int{65534}}
	}
	h.env.SetHome = func(string) error { return nil }
	h.env.DropProbe = nil // the real probe

	op := decodeOperation(t, h.mustRun("install", "cowsay", "--provider", "uv", "--yes", "--allow-root", "--json"))
	exe := filepath.Join(home, ".local", "bin", "cowsay")
	env := filepath.Join(home, ".local", "share", "uv", "tools", "cowsay")
	for _, path := range []string{filepath.Join(home, ".local"), filepath.Dir(exe), exe, env, filepath.Join(env, "installed-by")} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if st := info.Sys().(*syscall.Stat_t); st.Uid != 65534 || st.Gid != 65534 {
			t.Errorf("%s is owned by %d:%d; uv ran as nobody, so it is nobody's", path, st.Uid, st.Gid)
		}
	}
	if by, _ := os.ReadFile(filepath.Join(env, "installed-by")); strings.TrimSpace(string(by)) != "65534" {
		t.Errorf("uv ran as uid %q", strings.TrimSpace(string(by)))
	}
	if _, err := os.Stat("/root/.local/share/uv/tools/cowsay"); err == nil {
		t.Error("root's UV_TOOL_DIR leaked into the user's uv")
	}
	if len(op.Actions) != 1 {
		t.Fatalf("one action: %+v", op.Actions)
	}
	a := op.Actions[0]
	if a.Result != "applied" {
		t.Fatalf("the install applied: %+v", a)
	}
	for _, eff := range a.Effects {
		if eff.Op == model.EffectChown || eff.Op == model.EffectChmod {
			t.Errorf("nothing needed fixing after a dropped run: %+v", eff)
		}
	}
	for _, w := range a.Warnings {
		if w.Code == model.WarnRootOwnedAfterDrop || w.Code == model.WarnPermissionFixFailed {
			t.Errorf("no ownership warning: %+v", w)
		}
	}
	if findEffect(a.Effects, model.EffectCreate, model.EffectExecutable, exe) == nil {
		t.Errorf("the new command is observed: %+v", a.Effects)
	}
	if len(a.RunAs) != 1 || !a.RunAs[0].Dropped || a.RunAs[0].User != "nobody" || a.RunAs[0].UID != 65534 {
		t.Errorf("run_as says uv ran as nobody: %+v", a.RunAs)
	}
	// Starpoint's own records are its own writes, given back at the end.
	info, err := os.Lstat(h.env.GlobalConfig)
	if err != nil {
		t.Fatal(err)
	}
	if st := info.Sys().(*syscall.Stat_t); st.Uid != 65534 {
		t.Errorf("the configuration is given back to nobody: owned by %d", st.Uid)
	}
}

// requireRoot skips a test that needs root, unless STARPOINT_ROOT_TESTS=1
// says it must run (CI's sudo step), in which case not being root fails it.
func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() == 0 {
		return
	}
	if os.Getenv("STARPOINT_ROOT_TESTS") == "1" {
		t.Fatal("STARPOINT_ROOT_TESTS=1 but the test is not running as root")
	}
	t.Skip("needs root (CI runs these with sudo and STARPOINT_ROOT_TESTS=1)")
}
