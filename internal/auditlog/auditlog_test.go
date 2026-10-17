package auditlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestLog(t *testing.T) (*Log, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "logs")
	l := New(dir)
	return l, dir
}

// at pins the log's clock so entries land on either side of a cutoff.
func at(t *testing.T, l *Log, when string) {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, when)
	if err != nil {
		t.Fatalf("bad test time %q: %v", when, err)
	}
	l.now = func() time.Time { return ts }
}

func TestRecordWritesOneJSONLinePerEntry(t *testing.T) {
	l, dir := newTestLog(t)
	at(t, l, "2026-10-17T09:00:00Z")

	for _, name := range []string{"bat", "rg"} {
		if err := l.Record(Entry{Event: EventAction, Action: "install", Package: name, Provider: "brew", Result: "installed"}); err != nil {
			t.Fatal(err)
		}
	}

	path := filepath.Join(dir, "audit-2026-10-17.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("expected a rotated file for the day: %v", err)
	}
	lines := 0
	for _, line := range splitLines(data) {
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("line is not a complete entry: %v\n%s", err, line)
		}
		lines++
	}
	if lines != 2 {
		t.Errorf("expected 2 entries, got %d\n%s", lines, data)
	}
}

func TestRecordRotatesByDay(t *testing.T) {
	l, dir := newTestLog(t)
	at(t, l, "2026-10-16T23:59:59Z")
	if err := l.Record(Entry{Event: EventAction, Package: "a"}); err != nil {
		t.Fatal(err)
	}
	at(t, l, "2026-10-17T00:00:01Z")
	if err := l.Record(Entry{Event: EventAction, Package: "b"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"audit-2026-10-16.jsonl", "audit-2026-10-17.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, want)); err != nil {
			t.Errorf("missing %s: %v", want, err)
		}
	}
}

// Retention is the feature that stops the log growing forever, so it has to
// remove exactly the days outside the window and nothing else. The window runs
// back from the clock, so a file inside it must survive even if it is old.
func TestPruneRemovesDaysOutsideTheWindow(t *testing.T) {
	l, dir := newTestLog(t)
	expired := []string{"2026-09-01", "2026-09-16"}
	kept := []string{"2026-10-01", "2026-10-17"}
	for _, day := range append(append([]string{}, expired...), kept...) {
		at(t, l, day+"T12:00:00Z")
		if err := l.Record(Entry{Event: EventAction, Package: day}); err != nil {
			t.Fatal(err)
		}
	}
	at(t, l, "2026-10-17T12:00:00Z")

	removed, err := l.Prune(30 * 24 * time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != len(expired) {
		t.Errorf("expected %d expired days removed, got %d", len(expired), removed)
	}
	for _, gone := range expired {
		if _, err := os.Stat(filepath.Join(dir, "audit-"+gone+".jsonl")); !os.IsNotExist(err) {
			t.Errorf("audit-%s.jsonl should have been removed", gone)
		}
	}
	for _, stay := range kept {
		if _, err := os.Stat(filepath.Join(dir, "audit-"+stay+".jsonl")); err != nil {
			t.Errorf("audit-%s.jsonl is inside the window and must survive: %v", stay, err)
		}
	}
}

// A zero or negative retention would delete the whole history on the first run.
// Ageing records out is not something to do by accident.
func TestPruneNeverRunsWithoutAPositiveWindow(t *testing.T) {
	l, dir := newTestLog(t)
	at(t, l, "2020-01-01T12:00:00Z")
	if err := l.Record(Entry{Event: EventAction, Package: "ancient"}); err != nil {
		t.Fatal(err)
	}
	at(t, l, "2026-10-17T12:00:00Z")

	for _, d := range []time.Duration{0, -time.Hour} {
		removed, err := l.Prune(d)
		if err != nil {
			t.Fatalf("retention %v: %v", d, err)
		}
		if removed != 0 {
			t.Errorf("retention %v removed %d files", d, removed)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "audit-2020-01-01.jsonl")); err != nil {
		t.Errorf("the old log should still be there: %v", err)
	}
}

// Retention must not delete something it does not understand.
func TestPruneLeavesForeignFilesAlone(t *testing.T) {
	l, dir := newTestLog(t)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(foreign, []byte("not an audit log"), 0o644); err != nil {
		t.Fatal(err)
	}
	strange := filepath.Join(dir, filePrefix+"not-a-date"+fileExt)
	if err := os.WriteFile(strange, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	at(t, l, "2026-10-17T12:00:00Z")

	removed, err := l.Prune(time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Errorf("nothing of ours is old enough, got %d removed", removed)
	}
	for _, keep := range []string{foreign, strange} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s should have survived: %v", keep, err)
		}
	}
}

func TestEntriesFiltersByTimeAndSkipsTruncatedLines(t *testing.T) {
	l, _ := newTestLog(t)
	at(t, l, "2026-10-15T12:00:00Z")
	if err := l.Record(Entry{Event: EventAction, Package: "old"}); err != nil {
		t.Fatal(err)
	}
	at(t, l, "2026-10-17T12:00:00Z")
	if err := l.Record(Entry{Event: EventAction, Package: "new"}); err != nil {
		t.Fatal(err)
	}
	// A process killed mid-write leaves a partial line. That must not make the
	// rest of the history unreadable.
	f, err := os.OpenFile(filepath.Join(l.Dir(), "audit-2026-10-17.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"event":"action","pack`); err != nil {
		t.Fatal(err)
	}
	f.Close()

	entries, err := l.Entries(time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Package != "new" {
		t.Fatalf("expected only the recent entry, got %+v", entries)
	}
}

func TestFilesAreListedOldestFirst(t *testing.T) {
	l, _ := newTestLog(t)
	at(t, l, "2026-10-17T12:00:00Z")
	if err := l.Record(Entry{Event: EventAction}); err != nil {
		t.Fatal(err)
	}
	at(t, l, "2026-10-15T12:00:00Z")
	if err := l.Record(Entry{Event: EventAction}); err != nil {
		t.Fatal(err)
	}
	files, err := l.Files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %+v", files)
	}
	if files[0].Day != "2026-10-15" || files[1].Day != "2026-10-17" {
		t.Errorf("files are not oldest first: %+v", files)
	}
}

func TestEntriesOnAMissingDirectoryIsEmptyNotAnError(t *testing.T) {
	l := New(filepath.Join(t.TempDir(), "never-created"))
	entries, err := l.Entries(time.Time{})
	if err != nil {
		t.Fatalf("an absent log is empty, not broken: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected no entries, got %+v", entries)
	}
}

// splitLines yields each non-empty line as raw JSON.
func splitLines(data []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			if i > start {
				out = append(out, data[start:i])
			}
			start = i + 1
		}
	}
	if start < len(data) {
		out = append(out, data[start:])
	}
	return out
}
