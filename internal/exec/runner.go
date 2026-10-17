// Package exec runs external commands. It exists so providers can be tested
// without actually modifying the machine: tests substitute a scripted runner.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Result is the outcome of running a command.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner executes commands. Implementations must not mutate the caller's
// environment beyond what the command itself needs.
type Runner interface {
	// Run executes a command and captures output. A nonzero exit is reported
	// through ExitCode, not necessarily as an error, so callers can inspect
	// both.
	Run(ctx context.Context, name string, args ...string) (Result, error)
	// LookPath resolves an executable on PATH.
	LookPath(name string) (string, error)
}

// RealRunner runs commands on the host.
//
// With As set, every command runs as that identity, with its environment and
// its PATH; see Identity. Without it, commands run as Starpoint does.
type RealRunner struct {
	As *Identity
}

var _ Runner = RealRunner{}

// killGrace is how long a cancelled command is given to exit on its own before its
// process group is killed outright, and how long Run then waits for the output to
// drain.
//
// A package manager part-way through an install should be asked to stop before it
// is shot. But one that ignores the request must not be able to outlive the
// command that started it: a provider lookup that timed out and moved on would
// otherwise leave a process still writing to the filesystem, possibly while the
// same provider is being asked again.
const killGrace = 2 * time.Second

// Run executes the command. An error is returned only when the process could not
// be started at all, or the context was cancelled.
func (r RealRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	cmd, err := Command(ctx, r.As, name, args...)
	if err != nil {
		if IsDropError(err) {
			return Result{ExitCode: -1}, err
		}
		return Result{ExitCode: -1}, fmt.Errorf("running %s: %w", name, err)
	}
	// Give the command its own process group and cancel the whole group, so that
	// package managers which shell out to git, curl or a build backend take their
	// children with them. See process_unix.go.
	isolate(cmd)
	cmd.Cancel = func() error { return killTree(cmd) }
	// A surviving grandchild holding the output pipe would otherwise keep Run
	// blocked long after the process itself is dead.
	cmd.WaitDelay = killGrace

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	res := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode(cmd),
	}
	if err != nil {
		// Cancellation is checked first, and must stay first.
		//
		// A command that was killed reports "signal: killed", which is an
		// ExitError — indistinguishable from a command that chose to exit
		// nonzero. Returning the result without an error made a cancelled
		// provider lookup look like a provider that merely failed, and providers
		// then classified the silence as their own kind of problem. A timeout has
		// to be visible as a timeout.
		if ctx.Err() != nil {
			return res, fmt.Errorf("running %s: %w", name, ctx.Err())
		}
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return res, nil
		}
		if r.As != nil && cmd.Process == nil {
			// The process never started. With a credential set, the usual
			// reason is that the kernel would not switch to the user (no
			// CAP_SETUID, a uid the system rejects); it is reported as that,
			// and never retried with full privileges.
			return res, &DropError{User: r.As.User, Command: name, Err: err}
		}
		return res, fmt.Errorf("running %s: %w", name, err)
	}
	return res, nil
}

// exitCode reads the process's exit status, tolerating a command that never
// started — a missing binary, for instance, which has no process state at all.
func exitCode(cmd *exec.Cmd) int {
	if cmd.ProcessState == nil {
		return -1
	}
	return cmd.ProcessState.ExitCode()
}

// LookPath resolves an executable on PATH: the identity's PATH when the
// runner drops privileges, Starpoint's own otherwise.
func (r RealRunner) LookPath(name string) (string, error) {
	if r.As != nil {
		return lookPathIn(r.As.Env, name)
	}
	return exec.LookPath(name)
}

// Getenv reads key from the environment this runner's commands get.
func (r RealRunner) Getenv(key string) string {
	if r.As != nil {
		return r.As.Getenv(key)
	}
	return os.Getenv(key)
}

// Identity reports who this runner's commands run as: nil for Starpoint's own
// account.
func (r RealRunner) Identity() *Identity { return r.As }

// Chooser is a runner that decides per command which runner carries it. It is
// for providers whose privilege depends on where they install, such as npm,
// whose global prefix may be in the user's home or in /usr: Choose is asked
// on every call (and is expected to remember its answer).
type Chooser struct {
	Choose func(ctx context.Context) Runner
}

var _ Runner = Chooser{}

// Run implements Runner.
func (c Chooser) Run(ctx context.Context, name string, args ...string) (Result, error) {
	return c.Choose(ctx).Run(ctx, name, args...)
}

// LookPath implements Runner.
func (c Chooser) LookPath(name string) (string, error) {
	return c.Choose(context.Background()).LookPath(name)
}

// Getenv implements Environ.
func (c Chooser) Getenv(key string) string { return Getenv(c.Choose(context.Background()), key) }

// Field renders a result as a single diagnostic string.
func (r Result) Field() string {
	parts := []string{}
	if s := strings.TrimSpace(r.Stderr); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(r.Stdout); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "\n")
}

// Failed reports whether the command exited nonzero.
func (r Result) Failed() bool { return r.ExitCode != 0 }
