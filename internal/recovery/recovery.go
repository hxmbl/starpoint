// Package recovery handles crash detection and cleanup of interrupted operations.
//
// It inspects machine state to detect incomplete operations, cleans up temporary
// resources when safe, and preserves evidence when recovery is unsafe.
package recovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Operation represents an interrupted operation that was detected.
type Operation struct {
	// Type is the kind of operation that was interrupted.
	Type string `json:"type"`
	// Path is the filesystem path where the operation was occurring.
	Path string `json:"path"`
	// Timestamp is when the operation was detected (file modification time).
	Timestamp time.Time `json:"timestamp"`
	// Recoverable reports whether automatic cleanup is safe.
	Recoverable bool `json:"recoverable"`
	// SoleCopy reports that the configuration file this operation was writing
	// does not exist, so the temporary file holds the only copy of the user's
	// declared state. It must never be deleted: discarding it would destroy the
	// configuration outright rather than reverting an aborted write.
	SoleCopy bool `json:"sole_copy,omitempty"`
	// Reason explains why recovery is or is not safe.
	Reason string `json:"reason,omitempty"`
}

// Restorable reports whether the temporary file can be promoted back into place
// as the configuration itself.
//
// This is the remedy for a SoleCopy: the write completed but the rename that
// publishes it never happened, so moving the file to its target finishes the
// operation instead of discarding it.
func (o Operation) Restorable() bool {
	return o.SoleCopy && strings.HasSuffix(o.Path, ".tmp")
}

// Target returns the configuration path this operation was writing.
func (o Operation) Target() string {
	return strings.TrimSuffix(o.Path, ".tmp")
}

// Detector finds interrupted operations by inspecting the filesystem.
type Detector struct {
	configDir  string
	configPath string
}

// New creates a new Detector for the given Starpoint config directory.
func New(configDir string) *Detector {
	return &Detector{configDir: configDir}
}

// ForConfig creates a detector scoped to one Starpoint TOML file. This avoids
// inspecting unrelated temporary files in the same directory.
func ForConfig(configPath string) *Detector {
	return &Detector{configDir: filepath.Dir(configPath), configPath: configPath}
}

// DefaultConfigDir returns the default Starpoint configuration directory.
func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".starpoint"), nil
}

// NewDefault creates a Detector for the default configuration directory.
func NewDefault() (*Detector, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return nil, err
	}
	return New(dir), nil
}

// Detect scans for interrupted operations.
func (d *Detector) Detect() ([]Operation, error) {
	var ops []Operation

	// Scan for temporary config files (.tmp suffix)
	tmpOps, err := d.detectTempFiles()
	if err != nil {
		return nil, err
	}
	ops = append(ops, tmpOps...)

	return ops, nil
}

// detectTempFiles finds temporary configuration files left by crashes.
func (d *Detector) detectTempFiles() ([]Operation, error) {
	var ops []Operation

	entries, err := os.ReadDir(d.configDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading config directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Starpoint's config writer uses a sibling <name>.toml.tmp file. Avoid
		// treating unrelated editor, download, or tracker temp files as Starpoint
		// operations merely because they share the user's home directory.
		if d.configPath != "" {
			if name != filepath.Base(d.configPath)+".tmp" {
				continue
			}
		} else if name != ".starpoint.toml.tmp" && !strings.HasSuffix(name, ".toml.tmp") {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}

		path := filepath.Join(d.configDir, name)
		op := Operation{
			Type:        "temp_config",
			Path:        path,
			Timestamp:   info.ModTime(),
			Recoverable: false,
		}

		// Check if the target file exists.
		targetPath := op.Target()
		targetInfo, err := os.Stat(targetPath)
		switch {
		case err == nil:
			if targetInfo.ModTime().After(info.ModTime()) {
				// Target is newer - the temp file is stale and safe to remove
				op.Recoverable = true
				op.Reason = "target file is newer; temporary file is stale"
			} else {
				// The target is older or equally new, which is exactly what a crash
				// between writing the temp file and renaming it looks like. The
				// target itself is never left half-written, because the rename is
				// the only step that publishes it, so committed state is intact.
				// The temp file holds a write that never completed.
				op.Recoverable = false
				op.Reason = "target config is intact; this is an uncommitted write from an interrupted run"
			}
		case os.IsNotExist(err):
			// No config at all. The write finished but was never published, so
			// this file is the only copy of the user's declared state.
			op.SoleCopy = true
			op.Reason = "the configuration is missing; this file is the only copy of it"
		default:
			op.Reason = "could not inspect target config; preserve temporary data and inspect manually"
		}

		ops = append(ops, op)
	}

	return ops, nil
}

// Cleanup removes temporary resources, returning the operations it cleared and
// the ones it refused or could not remove.
//
// The second return carries the reason each failure actually occurred. Without
// it a caller can only report how many failed, not why, and is left to guess.
func (d *Detector) Cleanup(ops []Operation) (cleaned, failed []Operation, err error) {
	for _, op := range ops {
		if !op.Recoverable {
			failed = append(failed, op)
			continue
		}

		if removeErr := os.Remove(op.Path); removeErr != nil {
			if os.IsNotExist(removeErr) {
				// Already gone, which is the state we wanted. Report it as such
				// rather than silently claiming a deletion that did not happen.
				op.Reason = "temporary file was already removed"
			} else {
				op.Recoverable = false
				op.Reason = fmt.Sprintf("cleanup failed: %v", removeErr)
				failed = append(failed, op)
				continue
			}
		}
		cleaned = append(cleaned, op)
	}

	if len(failed) > 0 {
		return cleaned, failed, fmt.Errorf("%d operations could not be cleaned up", len(failed))
	}
	return cleaned, failed, nil
}

// Discardable returns the operations that may be deleted outright: leftovers
// whose real configuration is present and intact.
//
// Operations flagged as the only copy of a configuration are excluded whatever
// their Recoverable verdict, because deleting those does not revert an aborted
// write, it destroys the user's declared state.
func Discardable(ops []Operation) []Operation {
	var out []Operation
	for _, op := range ops {
		if op.SoleCopy {
			continue
		}
		out = append(out, op)
	}
	return out
}

// SoleCopies returns the operations whose temporary file is the only copy of a
// configuration, and so must be restored rather than discarded.
func SoleCopies(ops []Operation) []Operation {
	var out []Operation
	for _, op := range ops {
		if op.SoleCopy {
			out = append(out, op)
		}
	}
	return out
}

// Recoverable returns the subset of operations that are safe to recover automatically.
func Recoverable(ops []Operation) []Operation {
	var out []Operation
	for _, op := range ops {
		if op.Recoverable {
			out = append(out, op)
		}
	}
	return out
}

// Unrecoverable returns the subset of operations that are unsafe to recover automatically.
func Unrecoverable(ops []Operation) []Operation {
	var out []Operation
	for _, op := range ops {
		if !op.Recoverable {
			out = append(out, op)
		}
	}
	return out
}
