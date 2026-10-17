package tracking

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// RunRecord represents a single execution record.
type RunRecord struct {
	PackageName string    `json:"package_name"`
	Directory   string    `json:"directory"`
	Timestamp   time.Time `json:"timestamp"`
	WorkingDir  string    `json:"working_dir,omitempty"`
}

// Tracker maintains a record of where software has been executed.
type Tracker struct {
	mu      sync.RWMutex
	records []RunRecord
	path    string
}

// PathIn returns the runs database path inside a Starpoint state directory.
func PathIn(stateDir string) string {
	return filepath.Join(stateDir, "runs.json")
}

// DefaultPath returns the default path to the runs database: ~/.starpoint/runs.json.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".starpoint", "runs.json"), nil
}

// New creates a new Tracker backed by the given file path.
// If the file does not exist, it starts with an empty record set.
func New(path string) (*Tracker, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	t := &Tracker{
		path: path,
	}
	if err := t.load(); err != nil {
		return nil, err
	}
	return t, nil
}

// load reads the records from disk.
func (t *Tracker) load() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	data, err := os.ReadFile(t.path)
	if err != nil {
		if os.IsNotExist(err) {
			// No existing file, start empty
			t.records = nil
			return nil
		}
		return fmt.Errorf("reading runs database: %w", err)
	}

	if err := json.Unmarshal(data, &t.records); err != nil {
		return fmt.Errorf("parsing runs database: %w", err)
	}

	return nil
}

// save writes the records to disk.
func (t *Tracker) save() error {
	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return fmt.Errorf("creating runs directory: %w", err)
	}

	data, err := json.MarshalIndent(t.records, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding runs database: %w", err)
	}

	// Write to temporary file then rename for atomicity
	tmp := t.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("writing runs database: %w", err)
	}
	if err := os.Rename(tmp, t.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replacing runs database: %w", err)
	}

	return nil
}

// Record adds a new execution record.
func (t *Tracker) Record(packageName, directory string, timestamp time.Time) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Get working directory
	wd, err := os.Getwd()
	if err != nil {
		wd = ""
	}

	record := RunRecord{
		PackageName: packageName,
		Directory:   directory,
		Timestamp:   timestamp,
		WorkingDir:  wd,
	}

	t.records = append(t.records, record)
	return t.save()
}

// LastRun returns the most recent execution record for a package,
// or nil if the package has never been run.
func (t *Tracker) LastRun(packageName string) *RunRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var latest *RunRecord
	var latestTime time.Time

	for i := range t.records {
		if t.records[i].PackageName == packageName {
			if t.records[i].Timestamp.After(latestTime) {
				latest = &t.records[i]
				latestTime = t.records[i].Timestamp
			}
		}
	}

	return latest
}

// RunsInDirectory returns all execution records for packages run in the given directory.
func (t *Tracker) RunsInDirectory(directory string) []RunRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()

	var matches []RunRecord
	for _, r := range t.records {
		if r.Directory == directory {
			matches = append(matches, r)
		}
	}
	return matches
}

// Prune drops records older than the given window and returns how many it
// removed. A non-positive window removes nothing: ageing records out is not
// something to do by accident.
func (t *Tracker) Prune(olderThan time.Duration) (int, error) {
	if olderThan <= 0 {
		return 0, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	kept := make([]RunRecord, 0, len(t.records))
	for _, r := range t.records {
		if r.Timestamp.After(cutoff) {
			kept = append(kept, r)
		}
	}
	removed := len(t.records) - len(kept)
	if removed == 0 {
		return 0, nil
	}
	t.records = kept
	return removed, t.save()
}

// AllRecords returns a copy of all records. This is primarily useful for testing.
func (t *Tracker) AllRecords() []RunRecord {
	t.mu.RLock()
	defer t.mu.RUnlock()

	cpy := make([]RunRecord, len(t.records))
	copy(cpy, t.records)
	return cpy
}
