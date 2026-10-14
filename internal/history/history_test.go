package history

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/hxmbl/starpoint/internal/model"
)

func TestNew(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.json")

	h, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if h.path != path {
		t.Errorf("expected path %s, got %s", path, h.path)
	}

	// Should start empty
	if len(h.All()) != 0 {
		t.Errorf("expected empty history, got %d entries", len(h.All()))
	}
}

func TestRecord(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.json")
	h, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	transition := model.Transition{
		Package: model.Package{Name: "test-pkg"},
		From:    model.ProviderRef{Name: "from-provider"},
		To:      model.ProviderRef{Name: "to-provider"},
		Phase:   model.TransitionCompleted,
	}

	err = h.Record(transition, "/path/to/config")
	if err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	entries := h.All()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	if entries[0].Transition.Package.Name != "test-pkg" {
		t.Errorf("expected package name test-pkg, got %s", entries[0].Transition.Package.Name)
	}

	if entries[0].ConfigPath != "/path/to/config" {
		t.Errorf("expected config path /path/to/config, got %s", entries[0].ConfigPath)
	}
}

func TestRecordIncomplete(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.json")
	h, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Incomplete transition should not be recorded
	transition := model.Transition{
		Package: model.Package{Name: "test-pkg"},
		Phase:   model.TransitionPlanned,
	}

	err = h.Record(transition, "/path/to/config")
	if err != nil {
		t.Fatalf("Record failed: %v", err)
	}

	if len(h.All()) != 0 {
		t.Errorf("expected no entries for incomplete transition, got %d", len(h.All()))
	}
}

func TestForPackage(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.json")
	h, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Record multiple transitions
	for i := 0; i < 3; i++ {
		transition := model.Transition{
			Package: model.Package{Name: "pkg1"},
			Phase:   model.TransitionCompleted,
		}
		h.Record(transition, "/path/to/config")
	}

	transition := model.Transition{
		Package: model.Package{Name: "pkg2"},
		Phase:   model.TransitionCompleted,
	}
	h.Record(transition, "/path/to/config")

	entries := h.ForPackage("pkg1")
	if len(entries) != 3 {
		t.Errorf("expected 3 entries for pkg1, got %d", len(entries))
	}

	entries = h.ForPackage("pkg2")
	if len(entries) != 1 {
		t.Errorf("expected 1 entry for pkg2, got %d", len(entries))
	}

	entries = h.ForPackage("nonexistent")
	if len(entries) != 0 {
		t.Errorf("expected 0 entries for nonexistent package, got %d", len(entries))
	}
}

func TestLastTransition(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.json")
	h, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// No transitions initially
	last := h.LastTransition("test-pkg")
	if last != nil {
		t.Error("expected nil for non-existent package")
	}

	// Add transitions
	for i := 0; i < 3; i++ {
		transition := model.Transition{
			Package: model.Package{Name: "test-pkg"},
			To:      model.ProviderRef{Name: "provider" + string(rune('A'+i))},
			Phase:   model.TransitionCompleted,
		}
		h.Record(transition, "/path/to/config")
	}

	last = h.LastTransition("test-pkg")
	if last == nil {
		t.Fatal("expected last transition, got nil")
	}

	// Should be the most recent one
	if last.Transition.To.Name != "providerC" {
		t.Errorf("expected last transition to providerC, got %s", last.Transition.To.Name)
	}
}

func TestClear(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.json")
	h, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Add a transition
	transition := model.Transition{
		Package: model.Package{Name: "test-pkg"},
		Phase:   model.TransitionCompleted,
	}
	h.Record(transition, "/path/to/config")

	if len(h.All()) != 1 {
		t.Errorf("expected 1 entry before clear, got %d", len(h.All()))
	}

	err = h.Clear()
	if err != nil {
		t.Fatalf("Clear failed: %v", err)
	}

	if len(h.All()) != 0 {
		t.Errorf("expected 0 entries after clear, got %d", len(h.All()))
	}
}

func TestPrune(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.json")
	h, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Add old transition
	oldTransition := model.Transition{
		Package: model.Package{Name: "old-pkg"},
		Phase:   model.TransitionCompleted,
	}
	h.Record(oldTransition, "/path/to/config")

	// Manually set timestamp to be old
	h.mu.Lock()
	h.entries[0].Timestamp = time.Now().Add(-2 * time.Hour)
	h.mu.Unlock()

	// Add recent transition
	recentTransition := model.Transition{
		Package: model.Package{Name: "recent-pkg"},
		Phase:   model.TransitionCompleted,
	}
	h.Record(recentTransition, "/path/to/config")

	// Prune entries older than 1 hour
	err = h.Prune(1 * time.Hour)
	if err != nil {
		t.Fatalf("Prune failed: %v", err)
	}

	entries := h.All()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after prune, got %d", len(entries))
	}

	if entries[0].Transition.Package.Name != "recent-pkg" {
		t.Errorf("expected recent-pkg to remain, got %s", entries[0].Transition.Package.Name)
	}
}

func TestPersistence(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "history.json")

	// Create history and add entry
	h1, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	transition := model.Transition{
		Package: model.Package{Name: "test-pkg"},
		Phase:   model.TransitionCompleted,
	}
	h1.Record(transition, "/path/to/config")

	// Load history again
	h2, err := New(path)
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	entries := h2.All()
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after reload, got %d", len(entries))
	}

	if entries[0].Transition.Package.Name != "test-pkg" {
		t.Errorf("expected package name test-pkg, got %s", entries[0].Transition.Package.Name)
	}
}

func TestDefaultPath(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath failed: %v", err)
	}

	if path == "" {
		t.Error("expected non-empty path")
	}

	// Should contain .starpoint/history.json
	if filepath.Base(path) != "history.json" {
		t.Errorf("expected history.json in path, got %s", path)
	}
}
