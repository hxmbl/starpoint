//go:build windows

package lock

import (
	"os"
	"syscall"
	"unsafe"
)

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
var unlockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("UnlockFileEx")

type overlapped struct {
	Internal     uintptr
	InternalHigh uintptr
	Offset       uint32
	OffsetHigh   uint32
	HEvent       syscall.Handle
}

func flockLock(fd uintptr, nonblocking bool) error {
	flags := uintptr(0x2)
	if nonblocking {
		flags |= 0x1
	}
	var ov overlapped
	r1, _, e := lockFileEx.Call(fd, flags, 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
	if r1 == 0 {
		if e != syscall.Errno(0) {
			return e
		}
		return os.ErrInvalid
	}
	return nil
}

func flockUnlock(fd uintptr) error {
	var ov overlapped
	r1, _, e := unlockFileEx.Call(fd, 0, 1, 0, uintptr(unsafe.Pointer(&ov)))
	if r1 == 0 {
		if e != syscall.Errno(0) {
			return e
		}
		return os.ErrInvalid
	}
	return nil
}

func isLockConflictImpl(err error) bool {
	// Windows returns ERROR_LOCK_VIOLATION for lock conflicts
	return err == syscall.ERROR_LOCK_VIOLATION
}
