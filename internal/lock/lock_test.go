package lock

import (
	"path/filepath"
	"testing"
	"time"
)

func TestNew(t *testing.T) {
	tmpDir := t.TempDir()
	m, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if m.lockDir != tmpDir {
		t.Errorf("expected lockDir %s, got %s", tmpDir, m.lockDir)
	}
}

func TestAcquire(t *testing.T) {
	tmpDir := t.TempDir()
	m, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	lock, err := m.Acquire("test-resource", "test-holder")
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	if lock.Holder() != "test-holder" {
		t.Errorf("expected holder test-holder, got %s", lock.Holder())
	}

	// Verify lock is held
	locked, err := m.IsLocked("test-resource")
	if err != nil {
		t.Fatalf("IsLocked failed: %v", err)
	}
	if !locked {
		t.Error("expected resource to be locked")
	}

	// Release the lock
	if err := lock.Release(); err != nil {
		t.Fatalf("Release failed: %v", err)
	}

	// Verify lock is released
	locked, err = m.IsLocked("test-resource")
	if err != nil {
		t.Fatalf("IsLocked failed: %v", err)
	}
	if locked {
		t.Error("expected resource to be unlocked")
	}
}

func TestAcquireConflict(t *testing.T) {
	tmpDir := t.TempDir()
	m, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Acquire lock
	lock1, err := m.Acquire("test-resource", "holder1")
	if err != nil {
		t.Fatalf("First Acquire failed: %v", err)
	}
	defer lock1.Release()

	// Try to acquire same lock - should fail
	_, err = m.Acquire("test-resource", "holder2")
	if err == nil {
		t.Error("expected error when acquiring already-locked resource")
	}
}

func TestTryAcquire(t *testing.T) {
	tmpDir := t.TempDir()
	m, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Acquire lock
	lock1, err := m.Acquire("test-resource", "holder1")
	if err != nil {
		t.Fatalf("First Acquire failed: %v", err)
	}

	// Try to acquire with timeout - should fail
	done := make(chan error)
	go func() {
		_, err := m.TryAcquire("test-resource", "holder2", 100*time.Millisecond)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected timeout error")
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("timeout did not occur")
	}

	// Release first lock
	lock1.Release()

	// Now try acquire should succeed
	lock2, err := m.TryAcquire("test-resource", "holder2", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("TryAcquire after release failed: %v", err)
	}
	lock2.Release()
}

func TestIsLocked(t *testing.T) {
	tmpDir := t.TempDir()
	m, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Resource not locked initially
	locked, err := m.IsLocked("nonexistent")
	if err != nil {
		t.Fatalf("IsLocked failed: %v", err)
	}
	if locked {
		t.Error("expected nonexistent resource to be unlocked")
	}

	// Acquire lock
	lock, err := m.Acquire("test-resource", "holder")
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	defer lock.Release()

	// Now should be locked
	locked, err = m.IsLocked("test-resource")
	if err != nil {
		t.Fatalf("IsLocked failed: %v", err)
	}
	if !locked {
		t.Error("expected resource to be locked")
	}
}

func TestUnlockAll(t *testing.T) {
	tmpDir := t.TempDir()
	m, err := New(tmpDir)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Acquire multiple locks
	lock1, err := m.Acquire("resource1", "holder1")
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}
	lock2, err := m.Acquire("resource2", "holder2")
	if err != nil {
		t.Fatalf("Acquire failed: %v", err)
	}

	// Verify locks are held
	if locked, _ := m.IsLocked("resource1"); !locked {
		t.Error("expected resource1 to be locked")
	}
	if locked, _ := m.IsLocked("resource2"); !locked {
		t.Error("expected resource2 to be locked")
	}

	// UnlockAll must not invalidate locks held by a live owner.
	if err := m.UnlockAll(); err != nil {
		t.Fatalf("UnlockAll failed: %v", err)
	}

	// Verify live locks remain held.
	if locked, _ := m.IsLocked("resource1"); !locked {
		t.Error("expected resource1 to remain locked after UnlockAll")
	}
	if locked, _ := m.IsLocked("resource2"); !locked {
		t.Error("expected resource2 to remain locked after UnlockAll")
	}

	// Clean up the held locks
	lock1.Release()
	lock2.Release()
	if locked, _ := m.IsLocked("resource1"); locked {
		t.Error("resource1 should unlock when its owner releases it")
	}
	if locked, _ := m.IsLocked("resource2"); locked {
		t.Error("resource2 should unlock when its owner releases it")
	}
}

func TestDefaultLockDir(t *testing.T) {
	dir, err := DefaultLockDir()
	if err != nil {
		t.Fatalf("DefaultLockDir failed: %v", err)
	}

	if dir == "" {
		t.Error("expected non-empty lock dir")
	}

	// Should contain .starpoint/locks
	if filepath.Base(filepath.Dir(dir)) != ".starpoint" {
		t.Errorf("expected .starpoint in path, got %s", dir)
	}
	if filepath.Base(dir) != "locks" {
		t.Errorf("expected locks in path, got %s", dir)
	}
}
