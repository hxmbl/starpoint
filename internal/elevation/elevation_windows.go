//go:build windows

package elevation

import "golang.org/x/sys/windows"

func detect() (bool, string) {
	if windows.GetCurrentProcessToken().IsElevated() {
		return true, "elevated administrator token"
	}
	return false, ""
}
