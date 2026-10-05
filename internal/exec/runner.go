// Package exec runs external commands. It exists so providers can be tested
// without actually modifying the machine: tests substitute a scripted runner.
package exec

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
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
type RealRunner struct{}

var _ Runner = RealRunner{}

// Run executes the command. An error is returned only when the process could not
// be started at all, or the context was cancelled.
func (RealRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: cmd.ProcessState.ExitCode(),
	}
	if err != nil {
		var ee *exec.ExitError
		if asExitError(err, &ee) {
			return res, nil
		}
		if ctx.Err() != nil {
			return res, fmt.Errorf("running %s: %w", name, ctx.Err())
		}
		return res, fmt.Errorf("running %s: %w", name, err)
	}
	return res, nil
}

func asExitError(err error, target **exec.ExitError) bool {
	if e, ok := err.(*exec.ExitError); ok {
		*target = e
		return true
	}
	return false
}

// LookPath resolves an executable on PATH.
func (RealRunner) LookPath(name string) (string, error) { return exec.LookPath(name) }

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
