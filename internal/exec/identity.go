package exec

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Identity is the account a command runs as when Starpoint, running as root
// on behalf of the user who typed sudo or doas, drops privileges for it.
//
// A command run with an Identity gets that user's uid, primary gid and
// supplementary groups (see credential_unix.go) and exactly the environment
// in Env: nothing of root's environment reaches it. The executable is resolved
// on the PATH in Env, never on root's.
type Identity struct {
	// User is the account name, for messages.
	User string
	// UID and GID are the account's user id and primary group id.
	UID int
	GID int
	// Groups are the supplementary group ids. Empty means the primary group
	// only.
	Groups []int
	// Home is the account's home directory.
	Home string
	// Env is the complete environment the command runs with, as KEY=value.
	Env []string
}

// Getenv reads a variable from the identity's environment.
func (id *Identity) Getenv(key string) string {
	if id == nil {
		return ""
	}
	prefix := key + "="
	for i := len(id.Env) - 1; i >= 0; i-- {
		if strings.HasPrefix(id.Env[i], prefix) {
			return id.Env[i][len(prefix):]
		}
	}
	return ""
}

// ErrDropUnsupported is returned where an operating system has no notion of
// starting a process as another user that Starpoint can use. On Windows an
// elevated token has no invoking user to return to, so nothing is ever asked
// to drop there.
var ErrDropUnsupported = errors.New("running a command as another user is not supported on this platform")

// DropError is a command that could not be started as the identity it was
// meant to run as. It is never retried as root.
type DropError struct {
	User    string
	Command string
	Err     error
}

func (e *DropError) Error() string {
	return fmt.Sprintf("could not run %s as %s: %v (it was not run as root instead)", e.Command, e.User, e.Err)
}

func (e *DropError) Unwrap() error { return e.Err }

// IsDropError reports whether err is, or wraps, a DropError.
func IsDropError(err error) bool {
	var de *DropError
	return errors.As(err, &de)
}

// Environ is implemented by runners whose commands get an environment other
// than Starpoint's own. Providers read their location variables (CARGO_HOME,
// GOBIN, UV_TOOL_DIR, ...) through Getenv so they predict paths from the
// environment their commands will actually see.
type Environ interface {
	Getenv(key string) string
}

// Getenv reads key from the environment r's commands run with: the dropped
// identity's for a runner that drops privileges, Starpoint's own otherwise.
func Getenv(r Runner, key string) string {
	if e, ok := r.(Environ); ok {
		return e.Getenv(key)
	}
	return os.Getenv(key)
}

// lookPathIn resolves name on the PATH of an environment, as exec.LookPath
// does on the process's own PATH. A name with a separator is used as given.
func lookPathIn(env []string, name string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) || strings.ContainsRune(name, '/') {
		if isExecutable(name) {
			return name, nil
		}
		return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
	}
	path := ""
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], "PATH=") {
			path = env[i][len("PATH="):]
			break
		}
	}
	for _, dir := range filepath.SplitList(path) {
		if dir == "" || !filepath.IsAbs(dir) {
			// A relative PATH entry would resolve against whatever directory
			// Starpoint was started in, which is not a choice the user made
			// for this command.
			continue
		}
		candidate := filepath.Join(dir, name)
		if isExecutable(candidate) {
			return candidate, nil
		}
	}
	return "", &exec.Error{Name: name, Err: exec.ErrNotFound}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return executableMode(info)
}

// Command builds a command that runs as id, or as Starpoint itself when id
// is nil. The caller sets output and directory and runs it.
func Command(ctx context.Context, id *Identity, name string, args ...string) (*exec.Cmd, error) {
	if id == nil {
		return exec.CommandContext(ctx, name, args...), nil
	}
	resolved, err := lookPathIn(id.Env, name)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, resolved, args...)
	cmd.Env = append([]string(nil), id.Env...)
	if err := runAs(cmd, id); err != nil {
		return nil, &DropError{User: id.User, Command: name, Err: err}
	}
	return cmd, nil
}
