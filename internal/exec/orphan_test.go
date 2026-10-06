package exec

import (
	"context"
	"errors"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A provider lookup that times out must not leave the package manager running.
// Package managers are rarely one process: brew is a Ruby script that shells out
// to git and curl, pip can fan out to a build backend. Killing only the direct
// child leaves those grandchildren writing to the filesystem while Starpoint has
// already moved on and may be asking the same provider again.
//
// This is the reason the runner puts commands in their own process group, and it
// is worth a real process rather than a mock.
func TestCancellingDoesNotLeaveTheProcessTreeBehind(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no process groups on Windows; cancellation stops at the direct child there")
	}
	// A distinctive duration identifies the grandchild in `ps` output without any
	// quoting gymnastics: the backgrounded child's argv is literally "sleep 731".
	const marker = "sleep 731"
	cleanupProbe(t, marker)
	t.Cleanup(func() { cleanupProbe(t, marker) })

	// A leftover from an earlier failed run must be gone before measuring, or the
	// test reports someone else's orphan as its own.
	if n := survivors(t, marker); n != 0 {
		t.Fatalf("%d probe process(es) survived cleanup; refusing to measure on top of them", n)
	}

	// A shell that backgrounds a child and then waits: cancelling kills the shell,
	// and the backgrounded child — which holds the output pipe — is the orphan.
	// The shell backgrounds a child and then waits, so cancelling kills the shell
	// while its child — which holds the output pipe — lives on. The marker also
	// appears in the shell's own argv, so a survivor count above zero means the
	// shell itself failed to die.
	script := "sleep 731 & wait"

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := RealRunner{}.Run(ctx, "sh", "-c", script)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a cancelled command must report an error")
	}
	if elapsed > 10*time.Second {
		t.Fatalf("cancellation took %v; a surviving grandchild is blocking the output pipe", elapsed)
	}

	// Give the kill a moment to land before looking.
	time.Sleep(500 * time.Millisecond)
	if n := survivors(t, marker); n > 0 {
		t.Errorf("%d process(es) from the cancelled command survived; the tree was not cleaned up", n)
	}
}

// A cancelled command reports a timeout, not a nonzero exit.
//
// A killed process yields "signal: killed", which is an ExitError and therefore
// indistinguishable from a command that chose to exit 3. Without checking the
// context first, a provider lookup that timed out looked like a provider that
// merely failed, and every provider then classified the silence as its own kind
// of problem.
func TestCancellationIsReportedAsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(200 * time.Millisecond)
		cancel()
	}()

	_, err := RealRunner{}.Run(ctx, "sleep", "30")
	if err == nil {
		t.Fatal("a cancelled command must report an error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("the error should identify itself as a cancellation, got %v", err)
	}
}

// A command that exits normally must be reported normally, with its output intact.
func TestASuccessfulCommandStillReportsNormally(t *testing.T) {
	res, err := RealRunner{}.Run(context.Background(), "sh", "-c", "echo out; echo err 1>&2")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Failed() {
		t.Errorf("a successful command must not be reported as failed: %+v", res)
	}
	if !strings.Contains(res.Stdout, "out") || !strings.Contains(res.Stderr, "err") {
		t.Errorf("output was lost: %+v", res)
	}
}

// A command that never started has no process state, and reading an exit code out
// of nothing is a nil dereference waiting to happen.
func TestAMissingBinaryIsAnErrorNotAPanic(t *testing.T) {
	res, err := RealRunner{}.Run(context.Background(), "starpoint-no-such-binary-xyz")
	if err == nil {
		t.Fatal("a missing binary must be an error")
	}
	if res.ExitCode != -1 {
		t.Errorf("exit code should be -1 when nothing ran, got %d", res.ExitCode)
	}
}

// A nonzero exit is an outcome, not an error, so providers can inspect it.
func TestANonzeroExitIsNotAnError(t *testing.T) {
	res, err := RealRunner{}.Run(context.Background(), "sh", "-c", "exit 3")
	if err != nil {
		t.Fatalf("a nonzero exit must not be an error: %v", err)
	}
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
	if !res.Failed() {
		t.Error("Failed() should be true")
	}
}

// survivors counts live processes whose command line carries the marker.
//
// The count is done here rather than with `ps | grep -c`, because that pipeline
// prints nothing at all when there are no matches and this test would skip itself
// at exactly the moment it matters most: when the cleanup worked.
func survivors(t *testing.T, marker string) int {
	t.Helper()
	out, err := exec.Command("ps", "-Ao", "args=").Output()
	if err != nil {
		t.Skipf("cannot list processes on this platform: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, marker) {
			n++
		}
	}
	return n
}

// cleanupProbe removes probe processes left by a previous run and waits for them
// to actually be gone. A killed process takes a moment to disappear, and counting
// in the gap would blame this run for the last one's leftovers.
func cleanupProbe(t *testing.T, marker string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		_ = exec.Command("pkill", "-f", marker).Run()
		if survivors(t, marker) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Logf("probe processes matching %q did not go away; continuing anyway", marker)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
