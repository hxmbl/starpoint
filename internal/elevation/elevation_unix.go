//go:build !windows

package elevation

import "os"

func detect() (bool, string) {
	if os.Geteuid() == 0 {
		return true, "effective user id 0, root"
	}
	return false, ""
}
