package recovery

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDetectTempFiles(t *testing.T) {
	tmpDir := t.TempDir()
	d := New(tmpDir)

	// Create a temporary file
	tmpPath := filepath.Join(tmpDir, "config.toml.tmp")
	if err := os.WriteFile(tmpPath, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	ops, err := d.Detect()
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(ops))
	}

	if ops[0].Type != "temp_config" {
		t.Errorf("expected type temp_config, got %s", ops[0].Type)
	}

	if ops[0].Path != tmpPath {
		t.Errorf("expected path %s, got %s", tmpPath, ops[0].Path)
	}

	if ops[0].Recoverable {
		t.Errorf("expected operation to be preserved when target doesn't exist")
	}
}

func TestDetectTempFilesWithTarget(t *testing.T) {
	tmpDir := t.TempDir()
	d := New(tmpDir)

	// Create target file older than temp file
	targetPath := filepath.Join(tmpDir, "config.toml")
	tmpPath := filepath.Join(tmpDir, "config.toml.tmp")

	if err := os.WriteFile(targetPath, []byte("old"), 0o644); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}
	time.Sleep(10 * time.Millisecond) // Ensure timestamp difference
	if err := os.WriteFile(tmpPath, []byte("new"), 0o644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	ops, err := d.Detect()
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(ops))
	}

	// When temp is newer than target, it's not recoverable (may contain uncommitted changes)
	if ops[0].Recoverable {
		t.Errorf("expected operation to be unrecoverable when temp file is newer than target")
	}
}

func TestDetectTempFilesWithNewerTarget(t *testing.T) {
	tmpDir := t.TempDir()
	d := New(tmpDir)

	// Create temp file older than target file
	tmpPath := filepath.Join(tmpDir, "config.toml.tmp")
	targetPath := filepath.Join(tmpDir, "config.toml")

	if err := os.WriteFile(tmpPath, []byte("old"), 0o644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	time.Sleep(10 * time.Millisecond) // Ensure timestamp difference
	if err := os.WriteFile(targetPath, []byte("new"), 0o644); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}

	ops, err := d.Detect()
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(ops))
	}

	// When target is newer, temp is stale and recoverable
	if !ops[0].Recoverable {
		t.Errorf("expected operation to be recoverable when target file is newer")
	}
}

func TestCleanup(t *testing.T) {
	tmpDir := t.TempDir()
	d := New(tmpDir)

	// A temp file without its target may be the only copy of desired state.
	tmpPath := filepath.Join(tmpDir, "config.toml.tmp")
	if err := os.WriteFile(tmpPath, []byte("test"), 0o644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	ops, err := d.Detect()
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	cleaned, failed, err := d.Cleanup(ops)
	if err == nil || len(cleaned) != 0 {
		t.Fatalf("expected unsafe temp file to be preserved, cleaned=%v err=%v", cleaned, err)
	}

	if _, err := os.Stat(tmpPath); err != nil {
		t.Errorf("expected temp file to be preserved, stat error: %v", err)
	}

	// The sole copy must also be reported as such, so a caller can offer to
	// restore it instead of silently treating it as an aborted write.
	if len(failed) != 1 || !failed[0].SoleCopy {
		t.Errorf("expected one operation flagged as the sole copy, got %+v", failed)
	}
	if len(Discardable(ops)) != 0 {
		t.Errorf("a sole copy must never be discardable")
	}
	if !ops[0].Restorable() {
		t.Errorf("a sole-copy .toml.tmp should be restorable")
	}
}

func TestCleanupUnrecoverable(t *testing.T) {
	tmpDir := t.TempDir()
	d := New(tmpDir)

	// Create target and temp files
	targetPath := filepath.Join(tmpDir, "config.toml")
	tmpPath := filepath.Join(tmpDir, "config.toml.tmp")

	if err := os.WriteFile(targetPath, []byte("old"), 0o644); err != nil {
		t.Fatalf("failed to create target file: %v", err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(tmpPath, []byte("new"), 0o644); err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}

	ops, err := d.Detect()
	if err != nil {
		t.Fatalf("Detect failed: %v", err)
	}

	// Mark all as unrecoverable for this test
	for i := range ops {
		ops[i].Recoverable = false
	}

	cleaned, failed, err := d.Cleanup(ops)
	if err == nil {
		t.Errorf("expected error when cleaning unrecoverable operations")
	}

	if len(cleaned) != 0 {
		t.Errorf("expected 0 cleaned operations, got %d", len(cleaned))
	}

	if len(failed) != 1 {
		t.Errorf("expected the refusal to be reported back, got %+v", failed)
	}

	// Verify temp file still exists
	if _, err := os.Stat(tmpPath); os.IsNotExist(err) {
		t.Errorf("expected temp file to still exist, but it was removed")
	}
}

func TestRecoverable(t *testing.T) {
	ops := []Operation{
		{Type: "temp_config", Recoverable: true},
		{Type: "temp_config", Recoverable: false},
		{Type: "temp_config", Recoverable: true},
	}

	rec := Recoverable(ops)
	if len(rec) != 2 {
		t.Errorf("expected 2 recoverable operations, got %d", len(rec))
	}

	unrec := Unrecoverable(ops)
	if len(unrec) != 1 {
		t.Errorf("expected 1 unrecoverable operation, got %d", len(unrec))
	}
}

func TestDefaultConfigDir(t *testing.T) {
	dir, err := DefaultConfigDir()
	if err != nil {
		t.Fatalf("DefaultConfigDir failed: %v", err)
	}

	if dir == "" {
		t.Error("expected non-empty config dir")
	}

	// Should contain .starpoint
	if filepath.Base(dir) != ".starpoint" {
		t.Errorf("expected .starpoint in path, got %s", dir)
	}
}
