//go:build !windows

package elevation

import "os"

// supportsInvokingUser is whether an elevated run can name the user it was
// elevated from (SUDO_USER, DOAS_USER) and act for them.
const supportsInvokingUser = true

func detect() (bool, string) {
	if os.Geteuid() == 0 {
		return true, "effective user id 0, root"
	}
	return false, ""
}
