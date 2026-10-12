// Package shell contains the small amount of plumbing shared by concrete
// providers: capability declarations and output parsing helpers.
package shell

import (
	"sort"
	"strings"

	"github.com/starpoint/starpoint/internal/provider"
)

// ExecutableForPackage resolves the executable that verifies a package, given an
// executable-to-package mapping.
//
// The mapping is many-to-one: "python3" and "python" both name the python3
// package, so more than one key can claim a package. Go randomises map
// iteration, so scanning the map directly would return a different executable
// from one run to the next and make verification fail intermittently.
//
// The tie is broken in two deterministic steps. An executable named exactly
// after its package wins, because that pairing is the one the author of the
// mapping wrote down deliberately: Debian and Fedora ship /usr/bin/python3 for
// the python3 package, while /usr/bin/python may be a virtual provide with no
// executable behind it. Failing that, the shortest name wins, purely so the
// answer never depends on map order again.
func ExecutableForPackage(mapping map[string]string, packageName string) (string, bool) {
	keys := make([]string, 0, len(mapping))
	for executable := range mapping {
		keys = append(keys, executable)
	}
	sort.Strings(keys)
	best := ""
	for _, executable := range keys {
		if mapping[executable] != packageName {
			continue
		}
		if best == "" {
			best = executable
			continue
		}
		// An exact name match outranks any other claimant.
		if executable == packageName {
			return executable, true
		}
		if best == packageName {
			continue
		}
		if len(executable) < len(best) {
			best = executable
		}
	}
	return best, best != ""
}

// VersionFromArgs picks the most likely version token out of free-form CLI
// output such as `git version 2.43.0`.
func VersionFromArgs(args ...string) string {
	for _, a := range args {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if isVersionish(a) {
			return a
		}
	}
	return ""
}

func isVersionish(s string) bool {
	if len(s) == 0 {
		return false
	}
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case r == '.' || r == '-' || r == '+':
		default:
			return false
		}
	}
	return digits > 0
}

// FieldsFrom returns the fields of s starting at index n, or nil when there are
// not that many. Version banners vary between tools, so slicing must never
// panic on short output.
func FieldsFrom(s string, n int) []string {
	fields := strings.Fields(s)
	if len(fields) < n {
		return nil
	}
	return fields[n:]
}

// FirstLine returns the first non-empty line of s.
func FirstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// Fields splits a line into fields, dropping empties.
func Fields(line string) []string { return strings.Fields(line) }

// Err builds a typed provider error.
func Err(name, op string, kind provider.ErrorKind, msg string, err error) *provider.Error {
	return &provider.Error{Provider: name, Op: op, Kind: kind, Message: msg, Err: err}
}

// NotFound builds a not-found error.
func NotFound(name, op, msg string) *provider.Error {
	return Err(name, op, provider.ErrNotFound, msg, nil)
}

// Unsupported builds an unsupported-operation error.
func Unsupported(name, op, msg string) *provider.Error {
	return Err(name, op, provider.ErrUnsupported, msg, nil)
}

// Failure builds a generic failure.
func Failure(name, op, msg string, err error) *provider.Error {
	return Err(name, op, provider.ErrFailed, msg, err)
}

// UnsupportedVerify builds an unsupported error for Verify operations.
func UnsupportedVerify(name string) *provider.Error {
	return Unsupported(name, "verify", "this provider does not support executable verification")
}
