//go:build unix

package privdrop

import (
	"os"
	"syscall"
)

// ownedDir reports whether dir is a directory owned by uid.
func ownedDir(dir string, uid int) bool {
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}

// OwnedBy reports whether path exists and is owned by uid.
func OwnedBy(path string, uid int) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == uid
}
