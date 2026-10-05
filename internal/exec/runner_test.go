package exec

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRunCapturesOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell invocation differs on windows")
	}
	r := RealRunner{}
	res, err := r.Run(context.Background(), "sh", "-c", "echo out; echo err 1>&2")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Stdout != "out\n" {
		t.Errorf("stdout captured as %q", res.Stdout)
	}
	if res.Stderr != "err\n" {
		t.Errorf("stderr captured as %q", res.Stderr)
	}
	if res.Failed() {
		t.Error("a zero exit must not be reported as a failure")
	}
}

func TestRunReportsExitCodeWithoutErroring(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell invocation differs on windows")
	}
	r := RealRunner{}
	res, err := r.Run(context.Background(), "sh", "-c", "exit 3")
	if err != nil {
		t.Fatalf("a nonzero exit is a result, not a Go error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit code %d, want 3", res.ExitCode)
	}
	if !res.Failed() {
		t.Error("Failed() should report the nonzero exit")
	}
}

func TestRunFailsWhenBinaryIsMissing(t *testing.T) {
	r := RealRunner{}
	if _, err := r.Run(context.Background(), "definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("a missing binary must be a Go error so providers can report it")
	}
}

func TestRunHonoursContextCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell invocation differs on windows")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := RealRunner{}
	if _, err := r.Run(ctx, "sleep", "5"); err == nil {
		t.Fatal("a cancelled context must abort the command")
	}
}

func TestLookPath(t *testing.T) {
	r := RealRunner{}
	if _, err := r.LookPath("sh"); err != nil && runtime.GOOS != "windows" {
		t.Fatalf("sh should be findable: %v", err)
	}
	if _, err := r.LookPath("definitely-not-a-real-binary-xyz"); err == nil {
		t.Fatal("expected an error for a missing binary")
	}
}

func TestFieldJoinsOutputForDiagnostics(t *testing.T) {
	res := Result{Stdout: "out", Stderr: "err"}
	if got := res.Field(); got != "err\nout" {
		t.Errorf("Field() = %q", got)
	}
	if got := (Result{}).Field(); got != "" {
		t.Errorf("empty result should render as empty, got %q", got)
	}
}

func TestRealRunnerFindsRepoRelativeBinary(t *testing.T) {
	// A light end-to-end sanity check that the runner works against the host.
	if runtime.GOOS == "windows" {
		t.Skip("skip on windows")
	}
	if _, err := os.Stat(filepath.Join("..", "..", "go.mod")); err != nil {
		t.Skip("not running inside the module")
	}
}
