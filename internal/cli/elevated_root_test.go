//go:build linux || darwin

package cli

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/elevation"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/testsupport"
)

// TestAsRootSudoInstallLeavesNothingRootOwnedInTheUsersHome is the whole
// feature end to end, and needs root: CI runs it with sudo. The invoking user
// is nobody (65534), whose home is the harness's directory, which holds the
// configuration and the provider's bin directory.
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
	var reported bool
	for _, a := range op.Actions {
		if findEffect(a.Effects, model.EffectChown, model.EffectExecutable, exe) != nil {
			reported = true
		}
	}
	if !reported {
		t.Errorf("the chown is reported on the install: %+v", op.Actions)
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
