package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hxmbl/starpoint/internal/align"
	"github.com/hxmbl/starpoint/internal/auditlog"
	"github.com/hxmbl/starpoint/internal/config"
	"github.com/hxmbl/starpoint/internal/exec"
	"github.com/hxmbl/starpoint/internal/model"
	"github.com/hxmbl/starpoint/internal/privdrop"
	"github.com/hxmbl/starpoint/internal/provider"
	"github.com/hxmbl/starpoint/internal/providers"
)

// This file is what Starpoint does differently when it runs as root or
// elevated.
//
// Four things change:
//
//  1. Whose environment the run acts on. Under sudo or doas from an ordinary
//     user, it is that user's: HOME is pointed at their home before anything
//     reads it, so the configuration, the state directory, ~/.local/bin,
//     ~/.cargo/bin and the shell profiles are theirs. A plain root shell (or
//     sudo from root) acts on root's own environment, as it always did.
//  2. Who each provider runs as. Under sudo or doas from an ordinary user,
//     providers that install into that user's home (uv, pip --user, Cargo,
//     Go, rustup; npm, pnpm, yarn, gem and Homebrew when their prefix is the
//     user's) run as the user: their uid, gid and groups and an environment
//     built for them (internal/privdrop). System providers (apt, dnf,
//     pacman; npm, pnpm, yarn and gem into a system prefix) run as root,
//     where root is the point. If the user cannot be resolved, or a process
//     cannot actually be started as them, changing commands are refused
//     rather than run as root.
//  3. Whether a changing command runs at all. It needs --allow-root, or,
//     under sudo or doas at a terminal, an explicit yes to a question that
//     says whose environment will change. Read-only commands and doctor are
//     always allowed.
//  4. What happens to the files it touches. What a root provider creates in
//     shared locations is made readable and executable by everyone. What is
//     found owned by root under the target user's home is given to that
//     user; after a provider that ran as the user nothing should be, so such
//     a fix is marked unexpected and warned about. Each fix is an effect
//     (chown, chmod) on the action that caused it and so is in the audit
//     log.
//
// Starpoint's own records under that home (configuration, state directory,
// audit log, locks) are written by Starpoint itself, as root, and given back
// to the user at the end of every command. Writing them as the user instead
// would mean switching the effective uid of the Starpoint process, which in
// Go applies to every thread at once and races with the provider commands it
// starts (a process whose effective uid is the user's can no longer start one
// as root). The set of files is small, known and Starpoint's own, so the
// chown-back is exact and cheap, and it is recorded in the audit log.

// Error codes for an elevated run that cannot act for its invoking user.
const (
	CodeInvokingUserUnresolved = "invoking-user-unresolved"
	CodePrivilegeDropFailed    = "privilege-drop-failed"
)

// retarget points the run at the invoking user's environment when it runs
// elevated through sudo or doas, and decides who providers run as. It runs
// before anything reads HOME.
func (e *Env) retarget(ctx context.Context) {
	e.target = nil
	e.identity = nil
	e.blockCode, e.blockMessage = "", ""
	status := e.elevationStatus()
	e.privileges = &providers.Privileges{Elevated: status.Elevated}
	if msg := status.Unresolved(); msg != "" {
		e.block(CodeInvokingUserUnresolved, msg)
		return
	}
	t, ok := status.Target()
	if !ok {
		return
	}
	set := e.SetHome
	if set == nil {
		set = func(home string) error { return os.Setenv("HOME", home) }
	}
	if err := set(t.Home); err != nil {
		e.block(CodeInvokingUserUnresolved, fmt.Sprintf("could not act on %s's environment: %v", t.User, err))
		return
	}
	e.target = &t
	environ := os.Environ
	if e.Environ != nil {
		environ = e.Environ
	}
	id := privdrop.Identity(t, status.Home, environ())
	probe := e.DropProbe
	if probe == nil {
		probe = privdrop.Probe
	}
	if err := probe(ctx, id); err != nil {
		e.block(CodePrivilegeDropFailed, err.Error())
		return
	}
	e.identity = id
	e.privileges.Identity = id
}

// block records that this elevated run cannot act for its invoking user.
func (e *Env) block(code, msg string) {
	e.blockCode, e.blockMessage = code, msg
	e.privileges.Unavailable = msg + "; providers run as root"
}

// dropWarning is the warning a read-only command carries when the run
// cannot act for its invoking user, or nil.
func (e *Env) dropWarning() *model.Warning {
	if e.blockCode == "" {
		return nil
	}
	code := model.WarnPrivilegeDropFailed
	return &model.Warning{Severity: model.SeverityWarn, Code: code,
		Message: e.blockMessage + ". Provider commands run as root in this run, so changing commands are refused (" + e.blockCode + ")"}
}

// providerRunAs is the account p's commands run as in this Run.
func (e *Env) providerRunAs(ctx context.Context, p provider.Provider) model.RunAs {
	return e.privileges.RunAs(ctx, p)
}

// actionRunAs is the account each provider of an action runs as: the
// action's provider, then for a relocation the one it moves away from.
func (e *Env) actionRunAs(a model.PlannedAction) []model.RunAs {
	ctx := e.runCtx
	if ctx == nil {
		ctx = context.Background()
	}
	names := []string{a.State.Res.Provider}
	if a.Transition != nil {
		names = append(names, a.Transition.To.Name, a.Transition.From.Name)
	}
	var out []model.RunAs
	seen := map[string]bool{}
	reg := e.registry()
	for _, name := range names {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		if p, ok := reg.Get(name); ok {
			out = append(out, e.providerRunAs(ctx, p))
		}
	}
	return out
}

// projectIdentity is who a project's own dependency installer (`install .`)
// runs as: the invoking user when privileges are dropped and the project
// directory is theirs, Starpoint's own account otherwise.
func (e *Env) projectIdentity(dir string) *exec.Identity {
	if e.identity == nil || !privdrop.OwnedBy(dir, e.identity.UID) {
		return nil
	}
	return e.identity
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
	command := canonicalCommand(cmd)
	if e.blockCode != "" {
		// --allow-root does not cover this: it allows acting as root on
		// behalf of the invoking user, which is exactly what cannot be done.
		user := status.InvokingUser
		msg := fmt.Sprintf("refusing to %s: %s. Starpoint does not run %s's providers as root instead. Nothing was changed. "+
			"Run it as %s without sudo, or, to act on root's own environment on purpose, from a root shell without SUDO_USER and DOAS_USER set",
			command, e.blockMessage, user, user)
		e.audit(auditlog.Entry{Event: auditlog.EventGuard, Command: command, Result: "refused", Elevated: true,
			Detail: e.blockCode + ": " + msg})
		return &RefusalError{Code: e.blockCode, Exit: ExitRefusedElevated, Message: msg, Command: command}
	}
	if e.allowRoot {
		e.rootApproved = true
		return nil
	}
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
