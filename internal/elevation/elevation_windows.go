//go:build windows

package elevation

import "golang.org/x/sys/windows"

// supportsInvokingUser is false on Windows: an elevated administrator token
// (UAC, or Windows' own sudo) is the same account with more rights, so there
// is no other user to act for and nothing to drop back to. Starpoint runs
// providers as the elevated account there, exactly as before.
const supportsInvokingUser = false

func detect() (bool, string) {
	if windows.GetCurrentProcessToken().IsElevated() {
		return true, "elevated administrator token"
	}
	return false, ""
}
