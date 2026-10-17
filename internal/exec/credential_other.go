//go:build !unix

package exec

import (
	"os"
	"os/exec"
)

// runAs cannot change the account of a child process here. On Windows an
// elevated token has no invoking user to return to (there is no SUDO_USER),
// so Starpoint never asks for it; this exists so a mistake fails loudly
// instead of running the command elevated.
func runAs(*exec.Cmd, *Identity) error { return ErrDropUnsupported }

func executableMode(os.FileInfo) bool { return true }

// DropSupported reports whether this platform can start commands as another
// user.
const DropSupported = false
