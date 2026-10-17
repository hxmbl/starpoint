// Package auditlog is Starpoint's own record of what it did.
//
// The other stores in Starpoint — runs.json, the transition history, the
// recovery journal — each answer one question: where software was installed,
// who owns it now, what was left behind by a crash. None of them answer "what
// did this tool do to my machine, and when". This package does.
//
// The log is append-only JSON Lines, rotated by day, so that retention is a
// file deletion rather than a rewrite of a file that grows without bound. It is
// deliberately the opposite of the transition history: that one is curated and
// can be edited back out of by an explicit reversal, whereas this one is a
// record of what happened and is only ever aged out.
//
// Nothing here is load-bearing. A Starpoint command must succeed or fail on its
// own merits, so callers treat every write as best effort: a log that cannot be
// written is a problem to report, never a reason to refuse to remove software.
package auditlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event names. They are stable strings so a consumer can filter without parsing
// prose.
const (
	// EventCommandStart is written before a command does anything, so a command
	// that dies mid-flight still leaves evidence that it ran.
	EventCommandStart = "command.start"
	// EventCommandFinish is written when a command returns, carrying its exit.
	EventCommandFinish = "command.finish"
	// EventAction is one change Starpoint made to the machine.
	EventAction = "action"
	// EventGuard is a destructive operation Starpoint refused to perform.
	EventGuard = "guard"
	// EventRetention is the ageing-out of records older than the retention
	// window.
	EventRetention = "retention"
	// EventConfig is a logging or retention setting Starpoint wrote.
	EventConfig = "config"
)

// Entry is one line of the audit log.
type Entry struct {
	// Time is when the entry was recorded, in UTC.
	Time time.Time `json:"time"`
	// Event is one of the Event constants.
	Event string `json:"event"`
	// Command is the canonical subcommand name.
	Command string `json:"command,omitempty"`
	// Args are the command's arguments, with secrets-free values only.
	Args []string `json:"args,omitempty"`
	// Directory is the working directory the command ran in.
	Directory string `json:"directory,omitempty"`
	// DryRun records that the command was asked not to change anything.
	DryRun bool `json:"dry_run,omitempty"`
	// Action is the change for an EventAction entry.
	Action string `json:"action,omitempty"`
	// Package is the software an entry concerns.
	Package string `json:"package,omitempty"`
	// Provider is who owned it.
	Provider string `json:"provider,omitempty"`
	// Result is what became of the action: installed, removed, refused, failed.
	Result string `json:"result,omitempty"`
	// Exit is the process exit code for a finished command.
	Exit int `json:"exit,omitempty"`
	// Detail is a human-readable explanation, never machine-parsed.
	Detail string `json:"detail,omitempty"`
}

// fileLayout is the rotation scheme: one file per UTC day.
const fileLayout = "2006-01-02"

// filePrefix and fileExt name the rotated files.
const (
	filePrefix = "audit-"
	fileExt    = ".jsonl"
)

// DefaultRetention is how long records are kept when nothing says otherwise.
const DefaultRetention = 30 * 24 * time.Hour

// Log is an append-only audit log stored in a directory.
type Log struct {
	mu  sync.Mutex
	dir string
	// now is overridable so tests can place entries on either side of a cutoff.
	now func() time.Time
}

// DefaultDir returns the default log directory: ~/.starpoint/logs.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".starpoint", "logs"), nil
}

// PathIn returns the audit log directory inside a Starpoint state directory.
func PathIn(stateDir string) string {
	return filepath.Join(stateDir, "logs")
}

// New returns a Log writing into dir. The directory is created on first write,
// so constructing a Log never fails and never touches the filesystem.
func New(dir string) *Log {
	return &Log{dir: dir, now: time.Now}
}

// Dir is where the log's files live.
func (l *Log) Dir() string { return l.dir }

// fileFor returns the path holding entries for a moment in time.
func (l *Log) fileFor(t time.Time) string {
	return filepath.Join(l.dir, filePrefix+t.UTC().Format(fileLayout)+fileExt)
}

// Record appends one entry, stamping it with the current time.
//
// The write is a single O_APPEND write of one complete line, so two Starpoint
// processes logging at once cannot interleave inside a line and leave a record
// that parses as neither.
func (l *Log) Record(e Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	if e.Time.IsZero() {
		e.Time = l.now().UTC()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encoding audit entry: %w", err)
	}
	if err := os.MkdirAll(l.dir, 0o755); err != nil {
		return fmt.Errorf("creating audit log directory: %w", err)
	}
	f, err := os.OpenFile(l.fileFor(e.Time), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening audit log: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("writing audit entry: %w", err)
	}
	return nil
}

// File is one rotated day's file.
type File struct {
	Name  string    `json:"name"`
	Path  string    `json:"path"`
	Day   string    `json:"day"`
	Time  time.Time `json:"time"`
	Bytes int64     `json:"bytes"`
}

// Files lists the rotated files, oldest first.
func (l *Log) Files() ([]File, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.filesLocked()
}

func (l *Log) filesLocked() ([]File, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading audit log directory: %w", err)
	}
	var out []File
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), filePrefix) || !strings.HasSuffix(e.Name(), fileExt) {
			continue
		}
		day := strings.TrimSuffix(strings.TrimPrefix(e.Name(), filePrefix), fileExt)
		t, err := time.Parse(fileLayout, day)
		if err != nil {
			// Not one of ours, or a file from a future layout. Leaving it alone
			// is safer than deleting something Starpoint did not write.
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, File{
			Name:  e.Name(),
			Path:  filepath.Join(l.dir, e.Name()),
			Day:   day,
			Time:  t,
			Bytes: info.Size(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

// Prune deletes the rotated files whose day is older than the retention window,
// and returns how many it removed. A non-positive retention removes nothing:
// ageing records out is not something to do by accident.
func (l *Log) Prune(retention time.Duration) (int, error) {
	if retention <= 0 {
		return 0, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := l.now().UTC().Add(-retention)
	cutoffDay := cutoff.Truncate(24 * time.Hour)
	files, err := l.filesLocked()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, f := range files {
		// A file is removed by its day, not by its contents: the day's entries
		// are all within 24 hours of each other, and reading them would make
		// ageing out depend on how much happened that day.
		if f.Time.After(cutoffDay) {
			continue
		}
		if err := os.Remove(f.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, fmt.Errorf("removing expired audit log %s: %w", f.Path, err)
		}
		removed++
	}
	return removed, nil
}

// Entries returns the entries at or after since, oldest first. An unreadable or
// malformed line is skipped rather than failing the read: a truncated tail from
// a killed process must not make the whole history unreadable.
func (l *Log) Entries(since time.Time) ([]Entry, error) {
	files, err := l.Files()
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, f := range files {
		fh, err := os.Open(f.Path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("reading %s: %w", f.Path, err)
		}
		sc := bufio.NewScanner(fh)
		// Entries are small, but a long detail string should not silently
		// truncate the rest of the file.
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var e Entry
			if err := json.Unmarshal([]byte(line), &e); err != nil {
				continue
			}
			if e.Time.Before(since) {
				continue
			}
			out = append(out, e)
		}
		fh.Close()
		if err := sc.Err(); err != nil {
			return out, fmt.Errorf("reading %s: %w", f.Path, err)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

// Count returns how many entries the log holds at or after since.
func (l *Log) Count(since time.Time) (int, error) {
	entries, err := l.Entries(since)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}
