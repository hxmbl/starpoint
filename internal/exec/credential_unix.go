//go:build unix

package exec

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// runAs makes cmd start as id: the kernel sets the supplementary groups, the
// group id and then the user id in the child between fork and exec (Go's
// SysProcAttr.Credential), so the provider never runs a single instruction as
// root.
func runAs(cmd *exec.Cmd, id *Identity) error {
	if id.UID <= 0 || id.GID < 0 {
		return fmt.Errorf("refusing to run as uid %d gid %d", id.UID, id.GID)
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	groups := make([]uint32, 0, len(id.Groups))
	for _, g := range id.Groups {
		if g >= 0 {
			groups = append(groups, uint32(g))
		}
	}
	if len(groups) == 0 {
		groups = []uint32{uint32(id.GID)}
	}
	cmd.SysProcAttr.Credential = &syscall.Credential{
		Uid:    uint32(id.UID),
		Gid:    uint32(id.GID),
		Groups: groups,
	}
	return nil
}

func executableMode(info os.FileInfo) bool { return info.Mode().Perm()&0o111 != 0 }

// DropSupported reports whether this platform can start commands as another
// user.
const DropSupported = true
