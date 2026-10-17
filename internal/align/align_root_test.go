//go:build linux || darwin

package align

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// The tests below need root: they change owners for real. CI runs them in a
// separate step with sudo; elsewhere they skip.

func TestAsRootChownGivesFilesToTheInvokingUser(t *testing.T) {
	requireSupported(t)
	requireRoot(t)
	home := t.TempDir()
	since := time.Now().Add(-time.Second)
	exe := filepath.Join(home, ".local", "bin", "ruff")
	write(t, exe, 0o755)
	nobody := Owner{UID: 65534, GID: 65534}
	p := &Policy{Home: home, Owner: &nobody, User: "nobody", FromUID: 0, Since: since}
	effects := p.Align([]Touched{{Path: exe, Created: true}})
	for _, path := range []string{filepath.Join(home, ".local"), filepath.Join(home, ".local", "bin"), exe} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		if int(st.Uid) != nobody.UID || int(st.Gid) != nobody.GID {
			t.Errorf("%s is owned by %d:%d, want %d:%d", path, st.Uid, st.Gid, nobody.UID, nobody.GID)
		}
	}
	for _, eff := range effects {
		if eff.Error != "" {
			t.Errorf("a fix failed: %+v", eff)
		}
	}
	if info, _ := os.Lstat(home); info.Sys().(*syscall.Stat_t).Uid != 0 {
		t.Error("the home directory itself must not be reassigned")
	}
}

func TestAsRootChmodUndoesARestrictiveUmask(t *testing.T) {
	requireSupported(t)
	requireRoot(t)
	old := syscall.Umask(0o077)
	defer syscall.Umask(old)
	root := t.TempDir()
	if err := os.Chmod(root, 0o755); err != nil {
		t.Fatal(err)
	}
	tool := filepath.Join(root, "opt", "tool")
	if err := os.MkdirAll(filepath.Join(tool, "bin"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tool, "bin", "tool"), []byte("#!/bin/sh\n"), 0o777); err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, filepath.Join(tool, "bin", "tool")); got != 0o700 {
		t.Fatalf("umask 077 should leave 0700, got %04o", got)
	}
	p := &Policy{Home: filepath.Join(root, "root-home"), FromUID: 0, Since: time.Now().Add(-time.Minute)}
	p.Align([]Touched{{Path: tool, Created: true}})
	for _, path := range []string{tool, filepath.Join(tool, "bin"), filepath.Join(tool, "bin", "tool")} {
		if got := modeOf(t, path); got != 0o755 {
			t.Errorf("%s: %04o, want 0755", path, got)
		}
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
