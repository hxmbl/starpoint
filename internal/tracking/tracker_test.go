package tracking

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestNewCreatesEmptyTrackerForMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	tracker, err := New(path)
	if err != nil {
		t.Fatalf("creating tracker with missing file should succeed, got: %v", err)
	}
	if tracker == nil {
		t.Fatal("tracker should not be nil")
	}
	if len(tracker.AllRecords()) != 0 {
		t.Fatalf("expected no records, got %d", len(tracker.AllRecords()))
	}
}

func TestNewLoadsExistingRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	ts := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	// Create a pre-existing runs.json
	preExisting := []RunRecord{
		{PackageName: "ripgrep", Directory: "/home/user/project", Timestamp: ts},
	}
	data, err := json.MarshalIndent(preExisting, "", "  ")
	if err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	tracker, err := New(path)
	if err != nil {
		t.Fatalf("loading tracker: %v", err)
	}
	records := tracker.AllRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].PackageName != "ripgrep" {
		t.Errorf("package name mismatch: got %q", records[0].PackageName)
	}
	if records[0].Directory != "/home/user/project" {
		t.Errorf("directory mismatch: got %q", records[0].Directory)
	}
}

func TestRecordAddsNewEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	tracker, err := New(path)
	if err != nil {
		t.Fatalf("creating tracker: %v", err)
	}

	ts := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	if err := tracker.Record("ripgrep", "/home/user/project", ts); err != nil {
		t.Fatalf("record: %v", err)
	}

	records := tracker.AllRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(records))
	}
	if records[0].PackageName != "ripgrep" {
		t.Errorf("package name mismatch: got %q", records[0].PackageName)
	}
	if records[0].Directory != "/home/user/project" {
		t.Errorf("directory mismatch: got %q", records[0].Directory)
	}
	if !records[0].Timestamp.Equal(ts) {
		t.Errorf("timestamp mismatch: got %v", records[0].Timestamp)
	}
}

func TestLastRunReturnsMostRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	tracker, err := New(path)
	if err != nil {
		t.Fatalf("creating tracker: %v", err)
	}

	ts1 := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	ts2 := time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC)
	ts3 := time.Date(2024, 1, 3, 12, 0, 0, 0, time.UTC)

	_ = tracker.Record("ripgrep", "/dir1", ts1)
	_ = tracker.Record("ripgrep", "/dir2", ts2)
	_ = tracker.Record("ripgrep", "/dir3", ts3)

	last := tracker.LastRun("ripgrep")
	if last == nil {
		t.Fatal("expected a record, got nil")
	}
	if last.Directory != "/dir3" {
		t.Errorf("expected most recent directory /dir3, got %q", last.Directory)
	}
	if !last.Timestamp.Equal(ts3) {
		t.Errorf("expected timestamp %v, got %v", ts3, last.Timestamp)
	}
}

func TestLastRunReturnsNilForUnknownPackage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	tracker, err := New(path)
	if err != nil {
		t.Fatalf("creating tracker: %v", err)
	}

	last := tracker.LastRun("nonexistent")
	if last != nil {
		t.Fatal("expected nil for unknown package, got a record")
	}
}

func TestRunsInDirectoryFiltersByDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	tracker, err := New(path)
	if err != nil {
		t.Fatalf("creating tracker: %v", err)
	}

	ts := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	_ = tracker.Record("ripgrep", "/home/user/project", ts)
	_ = tracker.Record("ruff", "/home/user/project", ts.Add(time.Hour))
	_ = tracker.Record("git", "/other/dir", ts.Add(2*time.Hour))

	runs := tracker.RunsInDirectory("/home/user/project")
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs in directory, got %d", len(runs))
	}
	// Order may vary, check by package name
	pkgNames := make(map[string]bool)
	for _, r := range runs {
		pkgNames[r.PackageName] = true
	}
	if !pkgNames["ripgrep"] || !pkgNames["ruff"] {
		t.Errorf("expected ripgrep and ruff, got %v", pkgNames)
	}
}

func TestRunsInDirectoryReturnsEmptyForUnknownDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	tracker, err := New(path)
	if err != nil {
		t.Fatalf("creating tracker: %v", err)
	}

	runs := tracker.RunsInDirectory("/nonexistent")
	if len(runs) != 0 {
		t.Fatalf("expected no runs, got %d", len(runs))
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	ts := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)

	// Create tracker and add record
	tracker1, err := New(path)
	if err != nil {
		t.Fatalf("creating first tracker: %v", err)
	}
	if err := tracker1.Record("ripgrep", "/home/user/project", ts); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Load in a new tracker instance
	tracker2, err := New(path)
	if err != nil {
		t.Fatalf("creating second tracker: %v", err)
	}
	records := tracker2.AllRecords()
	if len(records) != 1 {
		t.Fatalf("expected 1 record after reload, got %d", len(records))
	}
	if records[0].PackageName != "ripgrep" {
		t.Errorf("package name lost: got %q", records[0].PackageName)
	}
	if records[0].Directory != "/home/user/project" {
		t.Errorf("directory lost: got %q", records[0].Directory)
	}
	if !records[0].Timestamp.Equal(ts) {
		t.Errorf("timestamp lost: got %v", records[0].Timestamp)
	}
}

func TestConcurrentAccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.json")
	tracker, err := New(path)
	if err != nil {
		t.Fatalf("creating tracker: %v", err)
	}

	var wg sync.WaitGroup
	numGoroutines := 10
	recordsPerGoroutine := 100

	// Concurrent writes
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < recordsPerGoroutine; j++ {
				pkgName := fmt.Sprintf("package-%d-%d", id, j)
				dir := fmt.Sprintf("/dir-%d", id)
				ts := time.Now()
				if err := tracker.Record(pkgName, dir, ts); err != nil {
					t.Errorf("concurrent record failed: %v", err)
				}
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < recordsPerGoroutine; j++ {
				pkgName := fmt.Sprintf("package-%d-%d", id, j)
				_ = tracker.LastRun(pkgName)
				_ = tracker.RunsInDirectory(fmt.Sprintf("/dir-%d", id))
			}
		}(i)
	}

	wg.Wait()

	// Verify all records were written
	records := tracker.AllRecords()
	expectedCount := numGoroutines * recordsPerGoroutine
	if len(records) != expectedCount {
		t.Fatalf("expected %d records, got %d", expectedCount, len(records))
	}
}

func TestDefaultPath(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath failed: %v", err)
	}
	if path == "" {
		t.Fatal("path should not be empty")
	}
	// Should contain .starpoint/runs.json
	if filepath.Base(path) != "runs.json" {
		t.Errorf("expected runs.json, got %s", filepath.Base(path))
	}
}

func TestEmptyPathUsesDefault(t *testing.T) {
	tracker, err := New("")
	if err != nil {
		t.Fatalf("New with empty path should use default: %v", err)
	}
	if tracker == nil {
		t.Fatal("tracker should not be nil")
	}
}
