//go:build darwin

package align

import (
	"os"
	"syscall"
	"time"
)

const supported = true

func lstat(path string) (stat, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return stat{}, err
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return stat{}, nil
	}
	return stat{
		uid: int(sys.Uid), gid: int(sys.Gid),
		ctime: time.Unix(int64(sys.Ctimespec.Sec), int64(sys.Ctimespec.Nsec)), //nolint:unconvert // field widths vary by architecture
		known: true,
	}, nil
}
