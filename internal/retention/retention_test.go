package retention

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/history"
	"github.com/hxmbl/starpoint/internal/tracking"
)

const ancientTime = "2020-01-01T00:00:00Z"

// auditWithOldRecord returns a log holding one entry dated ancientTime.
func auditWithOldRecord(t *testing.T, pkg string) *auditlog.Log {
	t.Helper()
	l := auditlog.New(filepath.Join(t.TempDir(), "logs"))
	ts, err := time.Parse(time.RFC3339, ancientTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Record(auditlog.Entry{Time: ts, Event: auditlog.EventAction, Package: pkg}); err != nil {
		t.Fatal(err)
	}
	return l
}

// historyWithOldEntry writes a history file by hand, because Record always
// stamps the current time and a store that cannot hold an old record cannot be
// tested for ageing one out.
func historyWithOldEntry(t *testing.T, name string) *history.History {
	t.Helper()
	path := filepath.Join(t.TempDir(), "history.json")
	body := `[{"transition":{"package":{"name":"` + name + `"},"phase":"completed"},"timestamp":"` + ancientTime + `"}]`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := history.New(path)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

// runsWithOldRecord returns a tracker holding one record dated ancientTime.
func runsWithOldRecord(t *testing.T, pkg string) *tracking.Tracker {
	t.Helper()
	tr, err := tracking.New(filepath.Join(t.TempDir(), "runs.json"))
	if err != nil {
		t.Fatal(err)
	}
	ts, err := time.Parse(time.RFC3339, ancientTime)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.Record(pkg, "/project", ts); err != nil {
		t.Fatal(err)
	}
	return tr
}

// An unset or nonsensical retention means the default window. Zero must never
// mean "delete everything".
func TestWindowFallsBackToTheDefault(t *testing.T) {
	for _, days := range []int{0, -1, -365} {
		if got := Window(days); got != DefaultDays*24*time.Hour {
			t.Errorf("Window(%d) = %v, want the %d-day default", days, got, DefaultDays)
		}
	}
	if got := Window(7); got != 7*24*time.Hour {
		t.Errorf("Window(7) = %v", got)
	}
}

func TestApplyAgesOutEveryStore(t *testing.T) {
	log := auditWithOldRecord(t, "ancient")
	h := historyWithOldEntry(t, "old-pkg")
	tr := runsWithOldRecord(t, "ancient-pkg")

	report := Apply(Options{Days: 30, Audit: log, History: h, Runs: tr})
	if err := report.Error(); err != nil {
		t.Fatalf("a clean pass should report no problems: %v", err)
	}
	if report.Days != 30 {
		t.Errorf("Days = %d, want 30", report.Days)
	}
	if report.AuditLogsRemoved != 1 {
		t.Errorf("expected the old audit file gone: %+v", report)
	}
	if report.HistoryEntriesRemoved != 1 {
		t.Errorf("expected the old transition gone: %+v", report)
	}
	if report.RunRecordsRemoved != 1 {
		t.Errorf("expected the old run record gone: %+v", report)
	}
	if report.Total() != 3 || !report.Removed() {
		t.Errorf("Total = %d", report.Total())
	}
	if len(h.All()) != 0 {
		t.Error("the aged-out transition should be gone from the history")
	}
	if len(tr.AllRecords()) != 0 {
		t.Error("the aged-out run record should be gone")
	}
}

// Nothing old means nothing to do, and nothing to report.
func TestApplyLeavesRecentRecordsAlone(t *testing.T) {
	log := auditlog.New(t.TempDir())
	recent := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := log.Record(auditlog.Entry{Time: recent, Event: auditlog.EventAction, Package: "recent"}); err != nil {
		t.Fatal(err)
	}

	report := Apply(Options{Days: 30, Audit: log})
	if err := report.Error(); err != nil {
		t.Fatal(err)
	}
	if report.Removed() {
		t.Errorf("a record from an hour ago must survive: %+v", report)
	}
}

// A store that cannot be aged out must not stop the others, and must be
// reported rather than swallowed.
func TestApplyContinuesPastAFailingStore(t *testing.T) {
	// A regular file where a directory belongs makes every write and read fail.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := auditlog.New(filepath.Join(blocker, "logs"))

	h := historyWithOldEntry(t, "old-pkg")
	report := Apply(Options{Days: 30, Audit: broken, History: h})

	if report.HistoryEntriesRemoved != 1 {
		t.Errorf("the healthy store should still be aged out: %+v", report)
	}
	if report.Error() == nil {
		t.Fatal("the failing store should be reported")
	}
	if !strings.Contains(report.Error().Error(), "audit log") {
		t.Errorf("the report should name the store that failed: %v", report.Error())
	}
}

func TestApplyToleratesAbsentStores(t *testing.T) {
	report := Apply(Options{Days: 30})
	if err := report.Error(); err != nil {
		t.Fatalf("an empty pass is clean: %v", err)
	}
	if report.Removed() {
		t.Errorf("nothing was there to remove: %+v", report)
	}
	if report.Days != DefaultDays {
		t.Errorf("Days = %d, want %d", report.Days, DefaultDays)
	}
}
