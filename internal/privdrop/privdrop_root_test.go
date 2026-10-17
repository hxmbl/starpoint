//go:build linux || darwin

package privdrop

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/hxmbl/starpoint/internal/elevation"
	"github.com/hxmbl/starpoint/internal/exec"
)

// These tests start processes as another user for real, which needs root.
// CI runs them with sudo and STARPOINT_ROOT_TESTS=1; elsewhere they skip.

const nobody = 65534

// nobodyHome is a home directory nobody owns, reachable from /.
func nobodyHome(t *testing.T) string {
	t.Helper()
	top := t.TempDir()
	// t.TempDir's parent is created 0700 by root; nobody must reach the home.
	for dir := top; dir != os.TempDir() && dir != "/" && dir != "."; dir = filepath.Dir(dir) {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home := filepath.Join(top, "home")
	if err := os.Mkdir(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(home, nobody, nobody); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestAsRootProbeAndRunAsTheInvokingUser(t *testing.T) {
	requireRoot(t)
	home := nobodyHome(t)
	tg := elevation.Target{User: "nobody", UID: nobody, GID: nobody, Home: home, Groups: []int{nobody}}
	id := Identity(tg, "/root", append(os.Environ(), "HOME=/root", "CARGO_HOME=/root/.cargo"))
	if err := Probe(context.Background(), id); err != nil {
		t.Fatalf("root can start a process as nobody: %v", err)
	}
	r := exec.RealRunner{As: id}
	res, err := r.Run(context.Background(), "sh", "-c", `id -u; id -g; id -G; echo "$HOME"; echo "${CARGO_HOME:-unset}"; touch "$HOME/made-by-provider"`)
	if err != nil || res.Failed() {
		t.Fatalf("%v %+v", err, res)
	}
	lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
	want := []string{strconv.Itoa(nobody), strconv.Itoa(nobody), strconv.Itoa(nobody), home, "unset"}
	if len(lines) != len(want) {
		t.Fatalf("output: %q", res.Stdout)
	}
	for i := range want {
		if strings.TrimSpace(lines[i]) != want[i] {
			t.Errorf("line %d: got %q, want %q (uid, gid, groups, HOME, CARGO_HOME)", i, lines[i], want[i])
		}
	}
	info, err := os.Lstat(filepath.Join(home, "made-by-provider"))
	if err != nil {
		t.Fatal(err)
	}
	if st := info.Sys().(*syscall.Stat_t); st.Uid != nobody || st.Gid != nobody {
		t.Errorf("a file the provider wrote is owned by %d:%d, want nobody's", st.Uid, st.Gid)
	}
}

func TestAsRootProbeRefusesAHomeTheUserCannotWrite(t *testing.T) {
	requireRoot(t)
	home := nobodyHome(t)
	if err := os.Chown(home, 0, 0); err != nil {
		t.Fatal(err)
	}
	id := Identity(elevation.Target{User: "nobody", UID: nobody, GID: nobody, Home: home}, "/root", os.Environ())
	err := Probe(context.Background(), id)
	if err == nil || !strings.Contains(err.Error(), "cannot write to their home directory") {
		t.Fatalf("a home the user cannot write is refused: %v", err)
	}
}

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
