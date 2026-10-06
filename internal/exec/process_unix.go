//go:build unix

package exec

import (
	"os/exec"
	"syscall"
)

// isolate runs the command in its own process group.
//
// This is what makes cancelling a provider lookup actually clean up. Package
// managers are not single processes: `brew` is a Ruby script that shells out to
// git and curl, `pip` fans out to a build backend. Killing the direct child
// leaves those grandchildren running, still writing to the filesystem, while
// Starpoint has already moved on and may be asking the same provider again.
//
// A process group is the only portable-ish handle for that tree, so each command
// gets its own and the whole group is signalled together.
func isolate(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// killTree terminates the command and everything it started.
//
// It signals the group rather than the process, and falls back to the process
// itself if the group is already gone, because a command that finished normally
// must not report a kill failure.
//
// isolate and killTree are a pair, and must always be used together. Without
// isolate the child's process group is the caller's own, so signalling "the group"
// would kill Starpoint itself along with the command. That is not hypothetical:
// removing isolate and running the test suite kills the test binary.
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		// The process is already gone, or not ours to signal. Killing it directly
		// is still correct.
		return cmd.Process.Kill()
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
