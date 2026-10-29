package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/history"
	"github.com/hxmbl/starpoint/internal/oslog"
	"github.com/hxmbl/starpoint/internal/report"
	"github.com/hxmbl/starpoint/internal/retention"
	"github.com/hxmbl/starpoint/internal/tracking"
)

// cmdLogging shows, prunes, or configures Starpoint's own logging.
//
// Starpoint keeps four kinds of record, and the question this command answers is
// "what does it have, how long does it keep it, and who else is rotating it".
//
// Retention is not something to remember to run: it is applied on every command,
// so `logging prune` exists for inspecting and forcing the effect, not for making
// it happen.
func cmdLogging(env *Env, w *report.Writer, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("%w: logging takes at most one of prune or config", ErrUsage)
	}
	sub := ""
	if len(args) == 1 {
		sub = args[0]
	}

	stateDir := env.stateDir()
	if stateDir == "" {
		return fmt.Errorf("cannot locate the Starpoint state directory")
	}
	logDir := auditlog.PathIn(stateDir)

	switch sub {
	case "":
		return loggingStatus(env, w, stateDir, logDir)
	case "prune":
		return loggingPrune(env, w, stateDir)
	case "config":
		return loggingConfig(env, w, stateDir, logDir)
	default:
		return fmt.Errorf("%w: unknown option %q for logging", ErrUsage, sub)
	}
}

// retentionWindow returns the configured window and whether it came from the
// configuration file or from the default.
//
// It reads the global file only. Retention is a property of the machine's
// records, not of a checkout, so a project-local file must not be able to change
// it — and an unreadable or malformed file falls back to the default rather than
// failing the command that happened to ask.
func (e *Env) retentionWindow() (int, string) {
	path, err := e.globalPath()
	if err != nil {
		return retention.DefaultDays, "default"
	}
	doc, err := config.Load(config.LoadOptions{GlobalPath: path})
	if err != nil {
		return retention.DefaultDays, "default"
	}
	if days := doc.Global.Raw.Settings.Retention(); days > 0 {
		return days, "config"
	}
	return retention.DefaultDays, "default"
}

func loggingStatus(env *Env, w *report.Writer, stateDir, logDir string) error {
	days, source := env.retentionWindow()
	log := auditlog.New(logDir)

	status := report.LoggingStatus{
		StateDir:        stateDir,
		AuditLogDir:     logDir,
		RetentionDays:   days,
		RetentionSource: source,
		OS:              oslog.GenerateForHost(logDir, days),
	}
	files, err := log.Files()
	if err != nil {
		files = nil
	}
	status.Files = files
	// Count what retention would keep, so the number shown is the number that
	// will still be there tomorrow.
	if n, err := log.Count(time.Now().Add(-retention.Window(days))); err == nil {
		status.EntryCount = n
	}
	if status.OS.Supported {
		status.OSConfigured = isGeneratedConfig(status.OS.Path)
	}
	return w.Logging(status)
}

// isGeneratedConfig reports whether path holds a configuration Starpoint wrote.
//
// It checks rather than assumes, because writing over a file in the user's
// logging directory that Starpoint did not create would destroy whatever was in
// it. A file that is not ours is left alone and reported as absent.
func isGeneratedConfig(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return oslog.LooksGenerated(string(data))
}

func loggingPrune(env *Env, w *report.Writer, stateDir string) error {
	days, _ := env.retentionWindow()
	hist, _ := history.New(history.PathIn(stateDir))
	tracker, _ := tracking.New(tracking.PathIn(stateDir))
	rep := retention.Apply(retention.Options{
		Days:    days,
		Audit:   auditlog.New(auditlog.PathIn(stateDir)),
		History: hist,
		Runs:    tracker,
	})
	env.audit(auditlog.Entry{
		Event:   auditlog.EventRetention,
		Command: "logging",
		Result:  fmt.Sprintf("removed %d", rep.Total()),
		Detail:  errorDetail(rep.Error()),
	})
	if err := w.LoggingPrune(rep); err != nil {
		return err
	}
	return rep.Error()
}

func loggingConfig(env *Env, w *report.Writer, stateDir, logDir string) error {
	days, _ := env.retentionWindow()
	cfg := oslog.GenerateForHost(logDir, days)
	if !cfg.Supported {
		return w.LoggingConfig(cfg, false)
	}

	// Writing the same configuration twice should be quiet, so an unchanged file
	// is not rewritten.
	if isGeneratedConfig(cfg.Path) {
		if existing, err := os.ReadFile(cfg.Path); err == nil && string(existing) == cfg.Content {
			env.audit(auditlog.Entry{
				Event: auditlog.EventConfig, Command: "logging",
				Result: "unchanged", Detail: cfg.Path,
			})
			return w.LoggingConfig(cfg, false)
		}
	} else if _, err := os.Lstat(cfg.Path); err == nil {
		// Something is already there and Starpoint did not write it. It belongs to
		// the user, so it is left alone and reported rather than replaced.
		cfg.Note = fmt.Sprintf("%s exists and was not written by Starpoint, so it was left unchanged. Move it aside and run 'str logging config' again to generate one.", cfg.Path)
		env.audit(auditlog.Entry{
			Event: auditlog.EventConfig, Command: "logging",
			Result: "refused", Detail: cfg.Path,
		})
		if w.JSON() {
			return w.LoggingConfig(cfg, false)
		}
		fmt.Fprintln(env.Stdout, cfg.Note)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.Path), 0o755); err != nil {
		return fmt.Errorf("creating the logging configuration directory: %w", err)
	}
	if err := os.WriteFile(cfg.Path, []byte(cfg.Content), 0o644); err != nil {
		return fmt.Errorf("writing the logging configuration: %w", err)
	}
	env.audit(auditlog.Entry{
		Event: auditlog.EventConfig, Command: "logging",
		Result: "written", Detail: cfg.Path,
	})
	return w.LoggingConfig(cfg, true)
}
