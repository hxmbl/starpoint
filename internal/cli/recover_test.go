package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/starpoint/starpoint/internal/locator"
	"github.com/starpoint/starpoint/internal/model"
	"github.com/starpoint/starpoint/internal/provider"
)

// A crash between writing the config's temp file and renaming it leaves a
// stray "<config>.tmp" next to a config that is perfectly intact, because the
// rename is the only step that publishes a config. Every mutating command
// refuses to start while that file is present, which is the correct default but
// is only safe if the user can get out of it. These tests pin down both halves:
// the refusal, and the way back.
func TestMutationBlockedByInterruptedConfigWrite(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	h.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0", Path: "/opt/homebrew/bin/rg"})

	stray := h.config + ".tmp"
	if err := os.WriteFile(stray, []byte("schema = 1\n\n[[package]]\nname = \"half-written\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	buf := &bytes.Buffer{}
	h.env.Stdout, h.env.Stderr = buf, buf
	code := Run(context.Background(), h.env, []string{"sync", "--no-confirm"})
	if code == 0 {
		t.Fatalf("sync should refuse while an interrupted write is unresolved:\n%s", buf.String())
	}
	got := buf.String()
	if !strings.Contains(got, "interrupted") {
		t.Fatalf("the refusal should explain what is unresolved:\n%s", got)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatalf("the refused run must not have deleted evidence: %v", err)
	}
}

func TestRecoverReportsAndClearsInterruptedWrite(t *testing.T) {
	h := newHarness(t, "schema = 1\n\n[[package]]\nname = \"ripgrep\"\nprovider = \"brew\"\n")
	h.brew.With(model.Installation{Name: "ripgrep", Version: "14.1.0", Path: "/opt/homebrew/bin/rg"})

	stray := h.config + ".tmp"
	if err := os.WriteFile(stray, []byte("schema = 1\n\n[[package]]\nname = \"half-written\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Bare 'recover' is read-only: it explains the situation and how to fix it.
	report := h.mustRun("recover")
	if !strings.Contains(report, stray) {
		t.Errorf("recover should name the leftover file:\n%s", report)
	}
	if !strings.Contains(report, "--discard") {
		t.Errorf("recover should point at the way out:\n%s", report)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Fatalf("recover without --discard must not delete anything: %v", err)
	}

	cleared := h.mustRun("recover", "--discard", "--no-confirm")
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Fatalf("recover --discard should remove the leftover file, got err=%v", err)
	}
	// The config itself must survive untouched.
	if _, err := os.Stat(h.config); err != nil {
		t.Fatalf("recover removed the configuration: %v", err)
	}

	// And the block is genuinely lifted.
	after := h.mustRun("sync", "--no-confirm")
	if strings.Contains(after, "cannot safely continue") {
		t.Errorf("sync should proceed after recovery:\n%s", after)
	}
	_ = cleared
}

// newHarnesslessEnv builds an Env whose global config does not exist, which is
// the state a crash during the very first init leaves behind.
func newHarnesslessEnv(t *testing.T) (*Env, string, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	cfg := filepath.Join(dir, ".starpoint.toml")
	buf := &bytes.Buffer{}
	return &Env{
		Stdout:       buf,
		Stderr:       buf,
		Stdin:        strings.NewReader(""),
		WorkDir:      dir,
		GlobalConfig: cfg,
		Registry:     provider.NewRegistry(),
		Locator:      &locator.Locator{LookPath: func(string) (string, error) { return "", errNotOnPath }},
	}, cfg, buf
}

// A crash during the very first init can leave the temp file holding the whole
// configuration with no config beside it. Discarding that would destroy the
// user's entire declared state and report success, so it must be refused and
// offered for restoration instead.
func TestRecoverRefusesToDiscardTheOnlyCopyOfAConfig(t *testing.T) {
	env, cfg, buf := newHarnesslessEnv(t)
	stray := cfg + ".tmp"
	desired := "schema = 1\n\n[[package]]\nname = \"ripgrep\"\n"
	if err := os.WriteFile(stray, []byte(desired), 0o644); err != nil {
		t.Fatal(err)
	}

	if code := Run(context.Background(), env, []string{"recover", "--discard", "--no-confirm"}); code != 0 {
		t.Fatalf("expected a clean refusal, got exit %d:\n%s", code, buf.String())
	}
	out := buf.String()

	// The file must still be there: it is the only copy.
	got, err := os.ReadFile(stray)
	if err != nil {
		t.Fatalf("--discard deleted the only copy of the configuration: %v", err)
	}
	if string(got) != desired {
		t.Errorf("the surviving file was modified: %q", got)
	}
	if strings.Contains(out, "Removed") {
		t.Errorf("--discard claimed to have removed something:\n%s", out)
	}
	// And it must tell the user how to finish the interrupted write.
	if !strings.Contains(out, "mv ") {
		t.Errorf("recover should explain how to restore the file:\n%s", out)
	}
}

// Restoring is the user's call, but it must be a supported path: the file the
// CLI refused to delete is exactly the file that becomes the config again.
func TestRecoverSoleCopyCanBeRestoredByHand(t *testing.T) {
	env, cfg, _ := newHarnesslessEnv(t)
	stray := cfg + ".tmp"
	desired := "schema = 1\n\n[[package]]\nname = \"ripgrep\"\n"
	if err := os.WriteFile(stray, []byte(desired), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := Run(context.Background(), env, []string{"recover", "--discard", "--no-confirm"}); code != 0 {
		t.Fatal("expected refusal")
	}

	if err := os.Rename(stray, cfg); err != nil {
		t.Fatal(err)
	}
	if code := Run(context.Background(), env, []string{"sync", "--no-confirm"}); code != 0 {
		t.Errorf("restoring the file should unblock Starpoint:\n%s", env.Stdout.(*bytes.Buffer).String())
	}
	if _, err := os.Stat(stray); !os.IsNotExist(err) {
		t.Errorf("the temp file should be gone after being restored, got %v", err)
	}
}

// Deleting without the ability to ask must fail rather than proceed, matching
// the rule every other mutating command follows.
func TestRecoverDiscardRefusesWithoutConfirmation(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	if err := os.WriteFile(h.config+".tmp", []byte("schema = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.env.Confirm = nil

	buf := &bytes.Buffer{}
	h.env.Stdout, h.env.Stderr = buf, buf
	if code := Run(context.Background(), h.env, []string{"recover", "--discard"}); code == 0 {
		t.Errorf("discard with no way to confirm should fail:\n%s", buf.String())
	}
	if _, err := os.Stat(h.config + ".tmp"); err != nil {
		t.Errorf("nothing should have been deleted: %v", err)
	}
}

func TestRecoverWithNothingToDo(t *testing.T) {
	h := newHarness(t, "schema = 1\n")
	out := h.mustRun("recover")
	if !strings.Contains(out, "No interrupted operations") {
		t.Errorf("recover should say there is nothing to do:\n%s", out)
	}
}

func TestRecoverRejectsUnknownOption(t *testing.T) {
	dir := t.TempDir()
	out := &bytes.Buffer{}
	env := &Env{Stdout: out, Stderr: out, WorkDir: dir,
		GlobalConfig: filepath.Join(dir, "c.toml"), Registry: provider.NewRegistry()}
	if code := Run(context.Background(), env, []string{"recover", "--nope"}); code == 0 {
		t.Errorf("unknown option should be a usage error, got success:\n%s", out.String())
	}
}
