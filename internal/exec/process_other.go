//go:build !unix

package exec

import "os/exec"

// isolate is a no-op where process groups are not available. Windows has no
// equivalent, so cancellation there stops at killing the direct child — which is
// the same behaviour this project had everywhere before, and no worse.
func isolate(cmd *exec.Cmd) {}

// killTree kills the command itself. There is no portable process-group handle
// on this platform.
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
