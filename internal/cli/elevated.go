package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hxmbl/starpoint/internal/align"
	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/model"
)

// This file is what Starpoint does differently when it runs as root or
// elevated.
//
// Three things change:
//
//  1. Whose environment the run acts on. Under sudo or doas from an ordinary
//     user, it is that user's: HOME is pointed at their home before anything
//     reads it, so the configuration, the state directory, ~/.local/bin,
//     ~/.cargo/bin and the shell profiles are theirs, and so is the
//     environment every provider subprocess inherits. A plain root shell (or
//     sudo from root) acts on root's own environment, as it always did.
//  2. Whether a changing command runs at all. It needs --allow-root, or,
//     under sudo or doas at a terminal, an explicit yes to a question that
//     says whose environment will change. Read-only commands and doctor are
//     always allowed.
//  3. What happens to the files it touches. What a provider writes under the
//     target user's home is given to that user; what it creates in shared
//     locations is made readable and executable by everyone. Each fix is an
//     effect (chown, chmod) on the action that caused it and so is in the
//     audit log. Starpoint's own records under that home are given back at
//     the end of every command.

// retarget points the run at the invoking user's environment when it runs
// elevated through sudo or doas. It runs before anything reads HOME.
func (e *Env) retarget() {
	e.target = nil
	t, ok := e.elevationStatus().Target()
	if !ok {
		return
	}
	set := e.SetHome
	if set == nil {
		set = func(home string) error { return os.Setenv("HOME", home) }
	}
	if err := set(t.Home); err != nil {
		e.warn(fmt.Sprintf("could not act on %s's environment (%v); acting on the elevated account's", t.User, err))
		return
	}
	e.target = &t
}

// changingCommand reports whether a command line would change the machine or
// Starpoint's configuration, as opposed to reading it.
func changingCommand(cmd string, args []string) bool {
	dryRun := hasFlag(args, "--dry-run", "-n")
	switch canonicalCommand(cmd) {
	case "init":
		return true
	case "sync", "install", "remove", "update", "prune":
		return !dryRun
	case "transition":
		if dryRun {
			return false
		}
		for i := 0; i < len(args); i++ {
			if args[i] == "--provider" || args[i] == "-p" || args[i] == "--context" {
				i++
				continue
			}
			if len(args[i]) > 0 && args[i][0] != '-' {
				return true
			}
		}
		return false
	case "recover":
		return hasFlag(args, "--discard", "--clean")
	case "logging":
		return len(args) > 0 && (args[0] == "prune" || args[0] == "config")
	}
	return false
}

// guardElevated refuses a changing command run as root or elevated unless
// --allow-root was given or, under sudo or doas at a terminal, the user
// confirms.
func (e *Env) guardElevated(cmd string, args []string) error {
	e.rootApproved = false
	status := e.elevationStatus()
	if !status.Elevated || !changingCommand(cmd, args) {
		return nil
	}
	if e.allowRoot {
		e.rootApproved = true
		return nil
	}
	command := canonicalCommand(cmd)
	unattended := hasFlag(args, "--yes", "-y", "--no-confirm")
	if status.InvokingUser != "" && !unattended && e.interactive() && e.Confirm != nil {
		out := e.promptOut()
		if warn := status.Warning(); warn != nil {
			fmt.Fprintf(out, "%s [%s]: %s\n\n", warn.Severity, warn.Code, warn.Message)
		}
		question := fmt.Sprintf("Run '%s' as root", command)
		if e.target != nil {
			question += fmt.Sprintf(", changing %s's environment (%s) and giving %s what it creates there?",
				e.target.User, e.target.Home, e.target.User)
		} else {
			question += ", changing root's environment?"
		}
		ok, err := e.Confirm(question)
		if err != nil {
			return err
		}
		if ok {
			e.rootApproved = true
			e.audit(auditlog.Entry{Event: auditlog.EventGuard, Command: command, Result: "allowed",
				Elevated: true, Detail: "elevated run confirmed interactively by " + status.InvokingUser})
			return nil
		}
	}
	msg := fmt.Sprintf("refusing to %s as root (%s). Nothing was changed. "+
		"Run it as your own user, or pass --allow-root to mean it", command, status.Mechanism)
	if status.InvokingUser != "" && unattended {
		msg += " (--yes does not answer this question; an unattended elevated run needs --allow-root)"
	}
	e.audit(auditlog.Entry{Event: auditlog.EventGuard, Command: command, Result: "refused", Elevated: true,
		Detail: CodeElevatedRefused + ": " + msg})
	return &RefusalError{Code: CodeElevatedRefused, Exit: ExitRefusedElevated, Message: msg, Command: command}
}

// alignPolicy is the ownership and permission policy for this run, or nil
// when it does not run elevated.
func (e *Env) alignPolicy() *align.Policy {
	status := e.elevationStatus()
	if !status.Elevated {
		return nil
	}
	if e.alignment != nil {
		return e.alignment
	}
	p := &align.Policy{Since: e.started.Add(-time.Second)}
	if e.target != nil {
		p.Home = e.target.Home
		p.Owner = &align.Owner{UID: e.target.UID, GID: e.target.GID}
		p.User = e.target.User
	} else {
		p.Home = status.Home
		if home, err := os.UserHomeDir(); err == nil && home != "" {
			p.Home = home
		}
	}
	e.alignment = p
	return p
}

// alignOwnFiles gives Starpoint's own records (the global configuration and
// the state directory: audit log, history, locks) back to the user an
// elevated run acts for. It runs at the end of every command, read-only ones
// included, since even `sudo starpoint list` writes to the audit log.
func (e *Env) alignOwnFiles() {
	if e.target == nil {
		return
	}
	p := e.alignPolicy()
	if p == nil || p.Owner == nil {
		return
	}
	var effects []model.Effect
	if path, err := e.globalPath(); err == nil {
		effects = append(effects, p.Align([]align.Touched{{Path: path}})...)
		if dir, err := config.StateDir(path); err == nil {
			effects = append(effects, p.Tree(dir)...)
		}
		// The mutation locks live beside the configuration.
		effects = append(effects, p.Tree(filepath.Join(filepath.Dir(path), ".starpoint"))...)
	}
	effects = model.MergeEffects(nil, effects...)
	if len(effects) == 0 {
		return
	}
	for _, eff := range effects {
		if eff.Error != "" {
			e.warn(fmt.Sprintf("could not give %s to %s: %s", eff.Path, e.target.User, eff.Error))
		}
	}
	e.audit(auditlog.Entry{Event: auditlog.EventAction, Command: "align", Action: "chown",
		Result: "applied", Elevated: true, Effects: effects,
		Detail: "Starpoint's own files given to " + e.target.User})
}
