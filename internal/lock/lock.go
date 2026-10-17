// Package lock provides file-based locking to prevent concurrent operations.
//
// It uses flock-style advisory locks to ensure only one Starpoint process
// can modify a given resource at a time.
package lock

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Lock represents a held file lock.
type Lock struct {
	file   *os.File
	path   string
	holder string
	mu     sync.Mutex
	closed bool
}

// Manager creates and manages locks.
type Manager struct {
	lockDir string
}

// New creates a new Manager for the given lock directory.
func New(lockDir string) (*Manager, error) {
	if err := os.MkdirAll(lockDir, 0o755); err != nil {
		return nil, fmt.Errorf("creating lock directory: %w", err)
	}
	return &Manager{lockDir: lockDir}, nil
}

// DefaultLockDir returns the default lock directory path.
func DefaultLockDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".starpoint", "locks"), nil
}

// NewDefault creates a Manager for the default lock directory.
func NewDefault() (*Manager, error) {
	dir, err := DefaultLockDir()
	if err != nil {
		return nil, err
	}
	return New(dir)
}

// Acquire attempts to acquire a lock for the given resource name.
//
// If the lock is already held by another process, it returns an error.
// The holder string identifies which operation holds the lock (for debugging).
func (m *Manager) Acquire(resource, holder string) (*Lock, error) {
	return acquirePath(m.lockPathFor(resource), resource, holder)
}

// lockPathFor maps a resource name to the lock file that guards it. Path
// separators are flattened so a name can never escape the lock directory.
func (m *Manager) lockPathFor(resource string) string {
	resource = strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r == ':' {
			return '_'
		}
		return r
	}, resource)
	return filepath.Join(m.lockDir, resource+".lock")
}

// AcquirePath locks the given stable path. Use this for resources whose identity
// is a filesystem path, such as distinct Starpoint configuration files.
func (m *Manager) AcquirePath(resourcePath, holder string) (*Lock, error) {
	resourcePath, err := filepath.Abs(resourcePath)
	if err != nil {
		return nil, fmt.Errorf("resolving lock resource: %w", err)
	}
	name := fmt.Sprintf("%x", sha256.Sum256([]byte(resourcePath)))
	return acquirePath(filepath.Join(m.lockDir, name+".lock"), resourcePath, holder)
}

func acquirePath(lockPath, resource, holder string) (*Lock, error) {
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening lock file: %w", err)
	}

	// Try to acquire exclusive lock (non-blocking)
	err = flockLock(file.Fd(), true)
	if err != nil {
		file.Close()
		if isLockConflict(err) {
			// Lock is held by another process
			data, readErr := os.ReadFile(lockPath)
			var pid int
			if readErr == nil {
				_, readErr = fmt.Sscanf(string(data), "%d", &pid)
			}
			if readErr == nil && pid > 0 {
				return nil, fmt.Errorf("resource %q is locked by process %d", resource, pid)
			}
			return nil, fmt.Errorf("resource %q is locked by another process", resource)
		}
		return nil, fmt.Errorf("acquiring lock: %w", err)
	}

	// Write our PID to the lock file. Truncate stale holder text from an earlier,
	// longer operation name while we own the file lock.
	pid := os.Getpid()
	if err := file.Truncate(0); err != nil {
		_ = flockUnlock(file.Fd())
		file.Close()
		return nil, fmt.Errorf("truncating lock holder: %w", err)
	}
	if _, err := file.WriteAt([]byte(fmt.Sprintf("%d\n%s", pid, holder)), 0); err != nil {
		_ = flockUnlock(file.Fd())
		file.Close()
		return nil, fmt.Errorf("writing lock holder: %w", err)
	}
	// Flush the holder text before returning. The PID is the one piece of this
	// file that has to survive a crash, because it is what the next process
	// reads to explain who is holding the lock.
	if err := file.Sync(); err != nil {
		_ = flockUnlock(file.Fd())
		file.Close()
		return nil, fmt.Errorf("syncing lock holder: %w", err)
	}

	return &Lock{
		file:   file,
		path:   lockPath,
		holder: holder,
	}, nil
}

// TryAcquire attempts to acquire a lock with a timeout.
//
// It retries every 100ms until the timeout is reached.
func (m *Manager) TryAcquire(resource, holder string, timeout time.Duration) (*Lock, error) {
	start := time.Now()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		lock, err := m.Acquire(resource, holder)
		if err == nil {
			return lock, nil
		}

		if time.Since(start) >= timeout {
			return nil, fmt.Errorf("timeout waiting for lock on %q: %w", resource, err)
		}

		<-ticker.C
	}
}

// Release releases the lock.
func (l *Lock) Release() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.closed {
		return nil
	}

	l.closed = true

	// Unlock the file
	if err := flockUnlock(l.file.Fd()); err != nil {
		l.file.Close()
		return fmt.Errorf("unlocking: %w", err)
	}

	if err := l.file.Close(); err != nil {
		return fmt.Errorf("closing lock file: %w", err)
	}

	return nil
}

// Holder returns the holder string for this lock.
func (l *Lock) Holder() string {
	return l.holder
}

// Path returns the lock file path.
func (l *Lock) Path() string {
	return l.path
}

// IsLocked reports whether a resource is currently locked.
func (m *Manager) IsLocked(resource string) (bool, error) {
	lockPath := m.lockPathFor(resource)

	file, err := os.OpenFile(lockPath, os.O_RDONLY, 0o644)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("opening lock file: %w", err)
	}
	defer file.Close()

	// Try to acquire a non-blocking lock
	err = flockLock(file.Fd(), true)
	if err == nil {
		// Lock was available, so it wasn't locked
		_ = flockUnlock(file.Fd())
		return false, nil
	}

	if isLockConflict(err) {
		return true, nil
	}

	return false, fmt.Errorf("checking lock: %w", err)
}

// UnlockAll releases all locks in the lock directory.
//
// This is a safety measure for cleanup after crashes. It should be used
// with caution as it breaks the locking guarantee.
//
// Deliberately a no-op. flock locks are owned by the open file description of
// the holding process, not by the file: there is nothing to clear from outside
// that process. A crashed holder's lock is already released by the kernel when
// its descriptors close, and unlinking the file would be actively wrong, since
// another process could be holding a lock on that inode and a fresh file at the
// same path would not be mutually exclusive with it.
//
// Lock files are intentionally left in place after release. They hold no state
// beyond a PID written for diagnostics, and removing them would race with a
// concurrent Acquire that has already opened the same inode.
//
// Kept as an exported no-op so callers written against it keep compiling.
func (m *Manager) UnlockAll() error { return nil }

// isLockConflict checks if an error is a lock conflict.
func isLockConflict(err error) bool {
	return isLockConflictImpl(err)
}
