// Package history records and retrieves transition history.
//
// It maintains a log of completed provider transitions, enabling reversal
// and audit of ownership changes.
package history

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/starpoint/starpoint/internal/model"
)

// Entry records one completed transition.
type Entry struct {
	// Transition is the completed transition record.
	Transition model.Transition `json:"transition"`
	// Timestamp is when the transition completed.
	Timestamp time.Time `json:"timestamp"`
	// ConfigPath is the configuration file that was updated.
	ConfigPath string `json:"config_path,omitempty"`
}

// History maintains a log of completed transitions.
type History struct {
	mu      sync.RWMutex
	entries []Entry
	path    string
}

// DefaultPath returns the default path to the history database: ~/.starpoint/history.json.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".starpoint", "history.json"), nil
}

// New creates a new History backed by the given file path.
// If the file does not exist, it starts with an empty log.
func New(path string) (*History, error) {
	if path == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	h := &History{
		path: path,
	}
	if err := h.load(); err != nil {
		return nil, err
	}
	return h, nil
}

// load reads the history from disk.
func (h *History) load() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	data, err := os.ReadFile(h.path)
	if err != nil {
		if os.IsNotExist(err) {
			// No existing file, start empty
			h.entries = nil
			return nil
		}
		return fmt.Errorf("reading history database: %w", err)
	}

	if err := json.Unmarshal(data, &h.entries); err != nil {
		return fmt.Errorf("parsing history database: %w", err)
	}

	return nil
}

// save writes the history to disk.
func (h *History) save() error {
	// Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(h.path), 0o755); err != nil {
		return fmt.Errorf("creating history directory: %w", err)
	}

	data, err := json.MarshalIndent(h.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding history database: %w", err)
	}

	// Use a unique temporary file so an interrupted or concurrent writer never
	// truncates another writer's pending snapshot, and fsync before renaming so
	// the replacement is durable rather than merely written.
	//
	// Note the scope of that guarantee: the read-modify-write of h.entries is
	// serialised by h.mu within this process only. Two Starpoint processes
	// touching different configs still share this one history file and the last
	// rename wins. Mutating commands hold an exclusive config lock, so the
	// common case is serialised; the residual race is two processes mutating
	// different configs at the same instant, which loses a history entry but
	// never corrupts the file.
	f, err := os.CreateTemp(filepath.Dir(h.path), "history-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temporary history database: %w", err)
	}
	tmp := f.Name()
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("writing history database: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("syncing history database: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("closing history database: %w", err)
	}
	if err := os.Rename(tmp, h.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replacing history database: %w", err)
	}

	return nil
}

// Record adds a completed transition to the history.
func (h *History) Record(t model.Transition, configPath string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	// Only record completed transitions
	if t.Phase != model.TransitionCompleted {
		return nil
	}

	entry := Entry{
		Transition: t,
		Timestamp:  time.Now(),
		ConfigPath: configPath,
	}

	h.entries = append(h.entries, entry)
	if err := h.save(); err != nil {
		// Roll the in-memory append back. Leaving it in place would make memory
		// disagree with disk, and the next successful save would then write out
		// an entry that was already reported as failed to persist.
		h.entries = h.entries[:len(h.entries)-1]
		return err
	}
	return nil
}

// ForPackage returns all transitions for a specific package, most recent first.
func (h *History) ForPackage(packageName string) []Entry {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var matches []Entry
	for i := len(h.entries) - 1; i >= 0; i-- {
		if h.entries[i].Transition.Package.Name == packageName {
			matches = append(matches, h.entries[i])
		}
	}
	return matches
}

// All returns all entries, most recent first.
func (h *History) All() []Entry {
	h.mu.RLock()
	defer h.mu.RUnlock()

	// Return a copy in reverse order (most recent first)
	out := make([]Entry, len(h.entries))
	for i, e := range h.entries {
		out[len(h.entries)-1-i] = e
	}
	return out
}

// LastTransition returns the most recent transition for a package, or nil if none.
func (h *History) LastTransition(packageName string) *Entry {
	entries := h.ForPackage(packageName)
	if len(entries) == 0 {
		return nil
	}
	return &entries[0]
}

// Clear removes all history entries.
func (h *History) Clear() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.entries = nil
	return h.save()
}

// Prune removes entries older than the given duration.
func (h *History) Prune(olderThan time.Duration) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	var kept []Entry
	for _, e := range h.entries {
		if e.Timestamp.After(cutoff) {
			kept = append(kept, e)
		}
	}
	h.entries = kept
	return h.save()
}
