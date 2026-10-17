// Package retention ages Starpoint's records out of its own stores.
//
// Starpoint keeps four things that grow without bound: the audit log, the
// transition history, the run history and the recovery journal. Retention is
// what makes them safe to keep forever by default, and it has to be applied
// somewhere without the user remembering to run it.
//
// It runs on every command. The pass is cheap — each store either has nothing
// old in it or is asked to rewrite a small file — and it is deliberately
// best-effort: a retention failure is reported, never fatal. A tool that could
// not delete a stale log must still be able to install software.
//
// Retention never runs without a positive window. Zero, a negative number or a
// missing setting means the default, not "keep nothing": the failure mode of an
// unset retention is a full disk, and the failure mode of a zero one is a tool
// that deletes an audit trail it was supposed to keep.
package retention

import (
	"errors"
	"fmt"
	"time"

	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/history"
	"github.com/hxmbl/starpoint/internal/tracking"
)

// DefaultDays is how long records are kept when nothing says otherwise.
const DefaultDays = 30

// Window converts a configured number of days into a duration, substituting the
// default for anything that is not a positive number.
func Window(days int) time.Duration {
	if days <= 0 {
		days = DefaultDays
	}
	return time.Duration(days) * 24 * time.Hour
}

// Options are the stores to age out. A nil store is skipped, so a caller can
// retain the audit log without opening the run history.
type Options struct {
	// Days is the configured retention. Anything not positive means DefaultDays.
	Days int
	// Audit is the append-only log of what Starpoint did.
	Audit *auditlog.Log
	// History is the transition history.
	History *history.History
	// Runs is the run history.
	Runs *tracking.Tracker
}

// Report is what one retention pass did, so the CLI can show it and the audit
// log can record it.
type Report struct {
	// Days is the window that was applied.
	Days int `json:"days"`
	// AuditLogsRemoved is how many rotated audit files were deleted.
	AuditLogsRemoved int `json:"audit_logs_removed"`
	// HistoryEntriesRemoved is how many transition records were dropped.
	HistoryEntriesRemoved int `json:"history_entries_removed"`
	// RunRecordsRemoved is how many run records were dropped.
	RunRecordsRemoved int `json:"run_records_removed"`
	// Problems are the stores that could not be aged out. Retention continues
	// past a failure: one unwritable file must not keep the others growing.
	Problems []string `json:"problems,omitempty"`
}

// Total is everything this pass deleted.
func (r Report) Total() int {
	return r.AuditLogsRemoved + r.HistoryEntriesRemoved + r.RunRecordsRemoved
}

// Removed reports whether the pass deleted anything.
func (r Report) Removed() bool { return r.Total() > 0 }

// Apply runs one retention pass and reports what it removed.
//
// Every store is attempted even if an earlier one failed, and the failures are
// collected rather than returned early.
func Apply(opts Options) Report {
	window := Window(opts.Days)
	report := Report{Days: int(window / (24 * time.Hour))}

	if opts.Audit != nil {
		n, err := opts.Audit.Prune(window)
		report.AuditLogsRemoved = n
		report.note(err, "audit log")
	}
	if opts.History != nil {
		n, err := opts.History.Prune(window)
		report.HistoryEntriesRemoved = n
		report.note(err, "transition history")
	}
	if opts.Runs != nil {
		n, err := opts.Runs.Prune(window)
		report.RunRecordsRemoved = n
		report.note(err, "run history")
	}
	return report
}

func (r *Report) note(err error, store string) {
	if err == nil {
		return
	}
	r.Problems = append(r.Problems, fmt.Sprintf("%s: %v", store, err))
}

// Error renders the problems as one error, or nil when the pass was clean.
func (r Report) Error() error {
	if len(r.Problems) == 0 {
		return nil
	}
	return errors.New("retention incomplete: " + join(r.Problems, "; "))
}

func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}
