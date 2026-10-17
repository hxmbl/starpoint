package report

import (
	"fmt"

	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/oslog"
	"github.com/hxmbl/starpoint/internal/retention"
)

// LoggingStatus is the machine-readable shape of `starpoint logging`.
type LoggingStatus struct {
	StateDir      string          `json:"state_dir"`
	AuditLogDir   string          `json:"audit_log_dir"`
	Files         []auditlog.File `json:"files"`
	EntryCount    int             `json:"entry_count"`
	RetentionDays int             `json:"retention_days"`
	// RetentionSource is "config" when the window came from the configuration
	// file and "default" when nothing said otherwise. It matters because a
	// retention window nobody set is still a promise about how long records live.
	RetentionSource string       `json:"retention_source"`
	OS              oslog.Config `json:"os"`
	// OSConfigured reports whether the generated system configuration is
	// present on disk.
	OSConfigured bool `json:"os_configured"`
}

// Logging renders `starpoint logging`.
func (w *Writer) Logging(status LoggingStatus) error {
	if w.asJSON {
		return w.respond(flatten(status))
	}
	fmt.Fprintf(w.out, "%s\n\n", w.section("Logging"))
	fmt.Fprintf(w.out, "  State directory:  %s\n", status.StateDir)
	fmt.Fprintf(w.out, "  Audit log:        %s\n", status.AuditLogDir)
	fmt.Fprintf(w.out, "  Log files:        %d", len(status.Files))
	if n := len(status.Files); n > 0 {
		fmt.Fprintf(w.out, " (%s to %s)", status.Files[0].Day, status.Files[n-1].Day)
	}
	fmt.Fprintln(w.out)
	fmt.Fprintf(w.out, "  Entries kept:     %d\n", status.EntryCount)
	source := status.RetentionSource
	if source == "" {
		source = "default"
	}
	fmt.Fprintf(w.out, "  Retention:        %d days (%s)\n", status.RetentionDays, source)

	fmt.Fprintf(w.out, "\n%s\n", w.section("System log retention"))
	if !status.OS.Supported {
		fmt.Fprintf(w.out, "  %s: %s\n", status.OS.Goos, status.OS.Note)
		return nil
	}
	fmt.Fprintf(w.out, "  %s: %s\n", status.OS.Goos, status.OS.Mechanism)
	if status.OSConfigured {
		fmt.Fprintf(w.out, "  Generated:        %s\n", status.OS.Path)
		if hint := oslog.InstallHint(status.OS); hint != "" {
			fmt.Fprintf(w.out, "  Install with:     %s\n", hint)
		}
	} else {
		fmt.Fprintf(w.out, "  Generated:        not written yet (%s)\n", status.OS.Path)
	}
	fmt.Fprintf(w.out, "  %s\n", status.OS.Note)
	return nil
}

// LoggingPrune renders `starpoint logging prune`.
func (w *Writer) LoggingPrune(rep retention.Report) error {
	if w.asJSON {
		return w.respond(map[string]any{
			"operation":               "logging.prune",
			"retention_days":          rep.Days,
			"audit_logs_removed":      rep.AuditLogsRemoved,
			"history_entries_removed": rep.HistoryEntriesRemoved,
			"run_records_removed":     rep.RunRecordsRemoved,
			"problems":                rep.Problems,
		})
	}
	if rep.Total() == 0 {
		fmt.Fprintln(w.out, w.p.Green(fmt.Sprintf("Nothing is older than %d days.", rep.Days)))
	} else {
		fmt.Fprintf(w.out, "%s\n", w.section(fmt.Sprintf("Removed %d record(s) older than %d days:", rep.Total(), rep.Days)))
		if rep.AuditLogsRemoved > 0 {
			fmt.Fprintf(w.out, "  %d audit log file(s)\n", rep.AuditLogsRemoved)
		}
		if rep.HistoryEntriesRemoved > 0 {
			fmt.Fprintf(w.out, "  %d transition record(s)\n", rep.HistoryEntriesRemoved)
		}
		if rep.RunRecordsRemoved > 0 {
			fmt.Fprintf(w.out, "  %d run record(s)\n", rep.RunRecordsRemoved)
		}
	}
	for _, p := range rep.Problems {
		fmt.Fprintf(w.out, "%s %s\n", w.p.Yellow("warning:"), p)
	}
	return nil
}

// LoggingConfig renders `starpoint logging config`.
func (w *Writer) LoggingConfig(cfg oslog.Config, written bool) error {
	if w.asJSON {
		return w.respond(map[string]any{
			"operation":   "logging.config",
			"goos":        cfg.Goos,
			"supported":   cfg.Supported,
			"mechanism":   cfg.Mechanism,
			"path":        cfg.Path,
			"written":     written,
			"system_path": cfg.SystemPath,
			"install":     oslog.InstallHint(cfg),
			"note":        cfg.Note,
		})
	}
	if !cfg.Supported {
		fmt.Fprintf(w.out, "%s\n", cfg.Note)
		return nil
	}
	if written {
		fmt.Fprintf(w.out, "Wrote %s configuration: %s\n", cfg.Mechanism, cfg.Path)
	} else {
		fmt.Fprintf(w.out, "%s configuration is already current: %s\n", cfg.Mechanism, cfg.Path)
	}
	fmt.Fprintf(w.out, "%s\n", cfg.Note)
	if hint := oslog.InstallHint(cfg); hint != "" && written {
		fmt.Fprintf(w.out, "\nTo hand these logs to the system logger:\n  %s\n", hint)
	}
	return nil
}
