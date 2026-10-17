//go:build !linux && !darwin

package align

// Ownership and permission alignment is implemented where Starpoint is
// built and tested to run elevated: Linux and macOS. On Windows an elevated
// token does not change file ownership the way root does, and other Unix
// systems are not supported targets; the policy reports itself disabled and
// makes no changes.
const supported = false

func lstat(string) (stat, error) { return stat{}, nil }
