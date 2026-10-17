package align

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hxmbl/starpoint/internal/model"
)

func requireSupported(t *testing.T) {
	t.Helper()
	if !supported {
		t.Skip("ownership and permission alignment is implemented on Linux and macOS only")
	}
}

func mkdir(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func modeOf(t *testing.T, path string) fs.FileMode {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().Perm()
}

// recorder stands in for chown, which an ordinary user cannot do to anyone
// else, and records what would have been reassigned.
type recorder struct {
	paths []string
	fail  map[string]error
}

func (r *recorder) chown(path string, uid, gid int) error {
	if err := r.fail[path]; err != nil {
		return err
	}
	r.paths = append(r.paths, path)
	return nil
}

func (r *recorder) sorted() []string {
	out := append([]string{}, r.paths...)
	sort.Strings(out)
	return out
}

func TestWantMode(t *testing.T) {
	cases := []struct {
		in, want fs.FileMode
	}{
		{fs.ModeDir | 0o700, 0o755},
		{fs.ModeDir | 0o750, 0o755},
		{0o700, 0o755}, // an executable
		{0o711, 0o755},
		{0o600, 0o644}, // data
		{0o640, 0o644},
		{0o644, 0o644},
		{0o775, 0o775}, // bits are added, never removed
		{0o666, 0o666},
	}
	for _, tc := range cases {
		if got := wantMode(tc.in); got != tc.want {
			t.Errorf("wantMode(%v) = %04o, want %04o", tc.in, got, tc.want)
		}
	}
}

func TestSecretLooking(t *testing.T) {
	for _, name := range []string{".netrc", "credentials.toml", "id_ed25519", "server.key", "cert.pem", "api-token", "client_secret.json", ".npmrc"} {
		if !secretLooking(filepath.Join("/opt/tool", name)) {
			t.Errorf("%s should be treated as a secret", name)
		}
	}
	for _, name := range []string{"ruff", "README.md", "lib.so", "keyboard"} {
		if secretLooking(filepath.Join("/opt/tool", name)) {
			t.Errorf("%s is not a secret", name)
		}
	}
}

func TestChmodWidensWhatWasCreatedInASharedLocation(t *testing.T) {
	requireSupported(t)
	root := t.TempDir()
	home := filepath.Join(root, "home", "alice")
	mkdir(t, home, 0o755)
	shared := filepath.Join(root, "opt")
	mkdir(t, shared, 0o755)

	// What a provider leaves behind under umask 077.
	tool := filepath.Join(shared, "tool")
	mkdir(t, filepath.Join(tool, "bin"), 0o700)
	if err := os.Chmod(tool, 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(tool, "bin", "tool"), 0o700)
	write(t, filepath.Join(tool, "share.dat"), 0o600)
	write(t, filepath.Join(tool, "api-token"), 0o600)
	if err := os.Symlink("bin/tool", filepath.Join(tool, "link")); err != nil {
		t.Fatal(err)
	}
	// Something that was there before the run.
	existing := filepath.Join(shared, "existing")
	write(t, existing, 0o600)

	p := &Policy{Home: home, FromUID: os.Getuid(), Since: time.Now().Add(-time.Hour)}
	effects := p.Align([]Touched{{Path: tool, Created: true}, {Path: existing, Created: false}})

	for path, want := range map[string]fs.FileMode{
		tool:                               0o755,
		filepath.Join(tool, "bin"):         0o755,
		filepath.Join(tool, "bin", "tool"): 0o755,
		filepath.Join(tool, "share.dat"):   0o644,
		filepath.Join(tool, "api-token"):   0o600,
		existing:                           0o600,
	} {
		if got := modeOf(t, path); got != want {
			t.Errorf("%s: mode %04o, want %04o", path, got, want)
		}
	}
	if len(effects) != 1 {
		t.Fatalf("one effect for the created tree, got %+v", effects)
	}
	eff := effects[0]
	if eff.Op != model.EffectChmod || eff.Path != tool || eff.From != "0700" || eff.To != "0755" ||
		eff.Evidence != model.EvidenceObserved || eff.Destructive || eff.Error != "" {
		t.Errorf("wrong effect: %+v", eff)
	}
	if !strings.Contains(eff.Detail, "3 entries below") {
		t.Errorf("the effect should count what changed below: %q", eff.Detail)
	}
}

func TestChmodNeverTouchesTheTargetHome(t *testing.T) {
	requireSupported(t)
	home := t.TempDir()
	created := filepath.Join(home, ".local", "bin", "tool")
	write(t, created, 0o700)

	p := &Policy{Home: home, FromUID: os.Getuid(), Since: time.Now().Add(-time.Hour)}
	if effects := p.Align([]Touched{{Path: created, Created: true}}); len(effects) != 0 {
		t.Errorf("modes under the user's own home are theirs: %+v", effects)
	}
	if got := modeOf(t, created); got != 0o700 {
		t.Errorf("mode changed to %04o", got)
	}
}

func TestChownGivesBackWhatTheRunChangedUnderTheHome(t *testing.T) {
	requireSupported(t)
	home := t.TempDir()
	old := filepath.Join(home, ".cargo", "old-root-file")
	write(t, old, 0o644)
	time.Sleep(20 * time.Millisecond)
	since := time.Now()
	time.Sleep(20 * time.Millisecond)

	exe := filepath.Join(home, ".local", "bin", "ruff")
	write(t, exe, 0o755)
	registry := filepath.Join(home, ".cargo", "registry", "index", "entry")
	write(t, registry, 0o644)

	rec := &recorder{}
	p := &Policy{Home: home, Owner: &Owner{UID: 4242, GID: 4243}, User: "alice", FromUID: os.Getuid(), Since: since, Chown: rec.chown}
	effects := p.Align([]Touched{{Path: exe, Created: true}})
	effects = append(effects, p.Tree(filepath.Join(home, ".cargo"))...)

	// ~/.cargo itself changed (an entry was added to it during the run), so it
	// is reassigned too; the file in it that predates the run is not.
	want := []string{
		filepath.Join(home, ".cargo"),
		filepath.Join(home, ".cargo", "registry"),
		filepath.Join(home, ".cargo", "registry", "index"),
		registry,
		filepath.Join(home, ".local"),
		filepath.Join(home, ".local", "bin"),
		exe,
	}
	sort.Strings(want)
	if got := rec.sorted(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("reassigned:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, path := range rec.paths {
		if path == home || path == old {
			t.Errorf("%s must not be reassigned", path)
		}
	}
	var sawExe bool
	for _, eff := range effects {
		if eff.Op != model.EffectChown || eff.Evidence != model.EvidenceObserved || eff.To != "4242:4243" {
			t.Errorf("wrong effect: %+v", eff)
		}
		if eff.Path == exe {
			sawExe = true
			if !strings.Contains(eff.Detail, "alice") {
				t.Errorf("the effect should name who it was given to: %+v", eff)
			}
		}
	}
	if !sawExe {
		t.Errorf("the executable's chown is an effect: %+v", effects)
	}
}

func TestChownLeavesWhatAlreadyBelongsToTheUser(t *testing.T) {
	requireSupported(t)
	home := t.TempDir()
	exe := filepath.Join(home, ".local", "bin", "ruff")
	write(t, exe, 0o755)
	rec := &recorder{}
	// The files are the test user's own, and the policy reassigns uid 0's.
	p := &Policy{Home: home, Owner: &Owner{UID: 4242, GID: 4242}, FromUID: 0, Since: time.Now().Add(-time.Hour), Chown: rec.chown}
	if os.Getuid() == 0 {
		p.FromUID = 4241 // nothing here belongs to it
	}
	if effects := p.Align([]Touched{{Path: exe}}); len(effects) != 0 || len(rec.paths) != 0 {
		t.Errorf("nothing belongs to the elevated account, so nothing changes: %+v %v", effects, rec.paths)
	}
}

func TestChownOutsideTheHomeIsNeverDone(t *testing.T) {
	requireSupported(t)
	root := t.TempDir()
	home := filepath.Join(root, "home")
	mkdir(t, home, 0o755)
	shared := filepath.Join(root, "usr", "local", "bin", "tool")
	write(t, shared, 0o755)
	rec := &recorder{}
	p := &Policy{Home: home, Owner: &Owner{UID: 4242, GID: 4242}, FromUID: os.Getuid(), Since: time.Now().Add(-time.Hour), Chown: rec.chown}
	p.Align([]Touched{{Path: shared, Created: true}})
	p.Tree(filepath.Dir(shared))
	if len(rec.paths) != 0 {
		t.Errorf("shared locations stay root's: %v", rec.paths)
	}
}

func TestAFailedFixIsReportedWithItsError(t *testing.T) {
	requireSupported(t)
	home := t.TempDir()
	exe := filepath.Join(home, "bin", "ruff")
	write(t, exe, 0o755)
	rec := &recorder{fail: map[string]error{exe: errors.New("operation not permitted")}}
	p := &Policy{Home: home, Owner: &Owner{UID: 4242, GID: 4242}, FromUID: os.Getuid(), Since: time.Now().Add(-time.Hour), Chown: rec.chown}
	var failed *model.Effect
	for _, eff := range p.Align([]Touched{{Path: exe}}) {
		eff := eff
		if eff.Path == exe {
			failed = &eff
		}
	}
	if failed == nil || !strings.Contains(failed.Error, "not permitted") {
		t.Fatalf("the failure must be carried on the effect: %+v", failed)
	}
}

func TestTreeIsBounded(t *testing.T) {
	requireSupported(t)
	home := t.TempDir()
	dir := filepath.Join(home, ".cache", "tool")
	for i := 0; i < 5; i++ {
		write(t, filepath.Join(dir, string(rune('a'+i))), 0o644)
	}
	rec := &recorder{}
	p := &Policy{Home: home, Owner: &Owner{UID: 4242, GID: 4242}, FromUID: os.Getuid(), Since: time.Now().Add(-time.Hour),
		MaxEntries: 2, Chown: rec.chown}
	effects := p.Tree(dir)
	var top *model.Effect
	for _, eff := range effects {
		eff := eff
		if eff.Path == dir {
			top = &eff
		}
	}
	if top == nil || top.Error == "" || !strings.Contains(top.Detail, "stopped after 2") {
		t.Fatalf("a walk that hits the bound says so: %+v", effects)
	}
}

func TestANilPolicyDoesNothing(t *testing.T) {
	var p *Policy
	if p.Enabled() || p.Align([]Touched{{Path: "/x", Created: true}}) != nil {
		t.Error("a nil policy is off")
	}
}
