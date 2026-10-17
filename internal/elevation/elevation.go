// Package elevation reports whether Starpoint is running with elevated
// privileges, and what that means for where its effects land.
//
// Running as root changes more than permissions. The home directory becomes
// root's, so ~/.starpoint.toml, ~/.local/bin, ~/.cargo/bin and the shell
// profiles Starpoint watches are root's rather than those of the person who
// typed the command, and an install or removal lands outside that person's
// normal environment.
//
// When the elevation came through sudo or doas, the environment says who
// asked (SUDO_USER, SUDO_UID, SUDO_GID; DOAS_USER). Starpoint then acts on
// that person's environment rather than root's: their home, their
// configuration, their ~/.local/bin. See Target.
package elevation

import (
	"fmt"
	"os"
	"os/user"
	"strconv"
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
	// Via is the tool that elevated: "sudo" or "doas", when known.
	Via string
	// InvokingUID and InvokingGID are the invoking user's ids, from
	// SUDO_UID/SUDO_GID or the user database. -1 when unknown. Zero values
	// are treated as unknown too, so a Status built by hand without them
	// never targets root by accident.
	InvokingUID int
	InvokingGID int
	// InvokingHome is the invoking user's home directory from the user
	// database, or empty when it could not be found.
	InvokingHome string
}

// Detect inspects the running process.
func Detect() Status {
	elevated, mechanism := detect()
	s := Status{Elevated: elevated, Mechanism: mechanism, InvokingUID: -1, InvokingGID: -1}
	s.Home, _ = os.UserHomeDir()
	for _, v := range []struct{ env, via string }{{"SUDO_USER", "sudo"}, {"DOAS_USER", "doas"}} {
		if u := os.Getenv(v.env); u != "" {
			s.InvokingUser = u
			s.Via = v.via
			break
		}
	}
	if !elevated || s.InvokingUser == "" {
		return s
	}
	if s.Via == "sudo" {
		s.InvokingUID = atoiOr(os.Getenv("SUDO_UID"), -1)
		s.InvokingGID = atoiOr(os.Getenv("SUDO_GID"), -1)
	}
	if u, err := user.Lookup(s.InvokingUser); err == nil {
		s.InvokingHome = u.HomeDir
		if s.InvokingUID < 0 {
			s.InvokingUID = atoiOr(u.Uid, -1)
		}
		if s.InvokingGID < 0 {
			s.InvokingGID = atoiOr(u.Gid, -1)
		}
	}
	return s
}

func atoiOr(v string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}

// Target is whose environment an elevated run acts on.
type Target struct {
	// User, UID, GID and Home are the invoking user's.
	User string
	UID  int
	GID  int
	Home string
}

// Target reports the invoking user's environment, when an elevated run came
// through sudo or doas from an ordinary user whose ids and home are known.
//
// That environment is what such a run acts on: `sudo starpoint install ruff`
// is a person asking for ruff, and putting it in /root/.local/bin would give
// them nothing they can run. Running elevated from root itself (SUDO_USER is
// root), or with ids or a home that cannot be resolved, has no target: the
// run acts on root's own environment, as a plain root shell does.
func (s Status) Target() (Target, bool) {
	if !s.Elevated || s.InvokingUser == "" || s.InvokingUser == "root" {
		return Target{}, false
	}
	if s.InvokingUID <= 0 || s.InvokingGID < 0 || s.InvokingHome == "" {
		return Target{}, false
	}
	return Target{User: s.InvokingUser, UID: s.InvokingUID, GID: s.InvokingGID, Home: s.InvokingHome}, true
}

// Warning is the structured warning for an elevated run, or nil.
func (s Status) Warning() *model.Warning {
	if !s.Elevated {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Starpoint is running elevated (%s)", s.Mechanism)
	if t, ok := s.Target(); ok {
		via := s.Via
		if via == "" {
			via = "sudo or doas"
		}
		fmt.Fprintf(&b, " through %s for %s, so it acts on %s's environment (%s): their ~/.starpoint.toml, ~/.local/bin, ~/.cargo/bin and shell profiles. "+
			"What it creates or changes there is given to %s (uid %d, gid %d); what it installs outside that home is made readable and executable by everyone. "+
			"Provider commands run with full privileges.", via, t.User, t.User, t.Home, t.User, t.UID, t.GID)
		return &model.Warning{Severity: model.SeverityWarn, Code: model.WarnElevated, Message: b.String()}
	}
	if s.Home != "" {
		fmt.Fprintf(&b, "; the home directory resolves to %s", s.Home)
	}
	b.WriteString(", so ~/.starpoint.toml, ~/.local/bin, ~/.cargo/bin, shell profiles and every other per-user location are the elevated account's")
	if s.InvokingUser != "" {
		fmt.Fprintf(&b, ", not %s's", s.InvokingUser)
	}
	b.WriteString(". Changes may land outside the user's normal environment, and provider commands run with full privileges. What is installed outside that home is made readable and executable by everyone.")
	return &model.Warning{
		Severity: model.SeverityWarn,
		Code:     model.WarnElevated,
		Message:  b.String(),
	}
}
