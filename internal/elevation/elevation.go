// Package elevation reports whether Starpoint is running with elevated
// privileges, and what that means for where its effects land.
//
// Running as root changes more than permissions. The home directory becomes
// root's, so ~/.starpoint.toml, ~/.local/bin, ~/.cargo/bin and the shell
// profiles Starpoint watches are root's rather than those of the person who
// typed the command, and an install or removal lands outside that person's
// normal environment. Starpoint does not refuse to run elevated; it says so.
package elevation

import (
	"fmt"
	"os"
	"strings"

	"github.com/hxmbl/starpoint/internal/model"
)

// Status describes the privileges of the running process.
type Status struct {
	// Elevated is true when the process runs as root (Unix) or with an
	// elevated administrator token (Windows).
	Elevated bool
	// Mechanism names how elevation was detected, for the message.
	Mechanism string
	// Home is the home directory the process resolves.
	Home string
	// InvokingUser is the user who elevated, when the environment says
	// (SUDO_USER, DOAS_USER).
	InvokingUser string
}

// Detect inspects the running process.
func Detect() Status {
	elevated, mechanism := detect()
	s := Status{Elevated: elevated, Mechanism: mechanism}
	s.Home, _ = os.UserHomeDir()
	for _, v := range []string{"SUDO_USER", "DOAS_USER"} {
		if u := os.Getenv(v); u != "" {
			s.InvokingUser = u
			break
		}
	}
	return s
}

// Warning is the structured warning for an elevated run, or nil.
func (s Status) Warning() *model.Warning {
	if !s.Elevated {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Starpoint is running elevated (%s)", s.Mechanism)
	if s.Home != "" {
		fmt.Fprintf(&b, "; the home directory resolves to %s", s.Home)
	}
	b.WriteString(", so ~/.starpoint.toml, ~/.local/bin, ~/.cargo/bin, shell profiles and every other per-user location are the elevated account's")
	if s.InvokingUser != "" {
		fmt.Fprintf(&b, ", not %s's", s.InvokingUser)
	}
	b.WriteString(". Changes may land outside the user's normal environment, and provider commands run with full privileges.")
	return &model.Warning{
		Severity: model.SeverityWarn,
		Code:     model.WarnElevated,
		Message:  b.String(),
	}
}
