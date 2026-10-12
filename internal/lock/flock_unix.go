//go:build darwin || linux || freebsd || openbsd || netbsd

package lock

import (
	"syscall"
)

func flockLock(fd uintptr, nonblocking bool) error {
	flags := syscall.LOCK_EX
	if nonblocking {
		flags |= syscall.LOCK_NB
	}
	return syscall.Flock(int(fd), flags)
}

func flockUnlock(fd uintptr) error {
	return syscall.Flock(int(fd), syscall.LOCK_UN)
}

func isLockConflictImpl(err error) bool {
	return err == syscall.EWOULDBLOCK || err == syscall.EAGAIN
}
