package cli

import (
	"fmt"
	"io"
	"strings"
)

// aliases maps every accepted spelling of a command to its canonical name.
var aliases = map[string]string{
	"init":       "init",
	"sync":       "sync",
	"list":       "list",
	"ls":         "list",
	"inventory":  "inventory",
	"info":       "info",
	"show":       "info",
	"explain":    "explain",
	"doctor":     "doctor",
	"install":    "install",
	"add":        "install",
	"i":          "install",
	"remove":     "remove",
	"uninstall":  "remove",
	"rm":         "remove",
	"transition": "transition",
	"move":       "transition",
	"update":     "update",
	"upgrade":    "update",
	"runs":       "runs",
	"history":    "runs",
	"prune":      "prune",
	"recover":    "recover",
	"logging":    "logging",
	"logs":       "logging",
	"help":       "help",
}

// canonicalCommand returns the canonical name for a command spelling, or "" when
// the spelling is not a command.
func canonicalCommand(name string) string { return aliases[name] }

// commandFlags lists each command's own options. It is the reference the shell
// completions under completions/ are checked against, so adding a flag here
// without completing it fails a test.
var commandFlags = map[string][]string{
	"init":       {"--force"},
	"sync":       {"--dry-run", "--no-confirm"},
	"list":       {"--managed", "--provider"},
	"inventory":  {"--provider"},
	"info":       {},
	"explain":    {"--provider"},
	"doctor":     {"--all"},
	"install":    {"--version", "--channel", "--project", "--provider", "--dry-run", "--no-confirm"},
	"remove":     {"--provider", "--dry-run", "--no-confirm", "--allow-empty"},
	"transition": {"--all", "--reverse", "--dry-run", "--no-confirm"},
	"update":     {"--self", "--dry-run", "--no-confirm"},
	"runs":       {"--package", "--directory"},
	"prune":      {"--dry-run", "--no-confirm"},
	"recover":    {"--discard", "--no-confirm"},
	"logging":    {},
	"help":       {},
}

// globalFlags are accepted anywhere on the command line.
var globalFlags = []string{"--json", "--provider", "--force", "--context", "--verbose", "--color", "--allow-destructive", "--allow-root", "--version", "--help"}

// isHelpFlag reports whether an argument asks for help.
func isHelpFlag(a string) bool { return a == "--help" || a == "-h" }

// wantsHelp reports whether a subcommand's arguments ask for its help.
//
// Help is honoured anywhere after the subcommand. `starpoint add --help` used to
// plan an install of a package called "--help"; with --no-confirm it would have
// run one. A help flag is never a package name.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if isHelpFlag(a) {
			return true
		}
	}
	return false
}

// commandHelp is the help text for each canonical command.
var commandHelp = map[string]string{
	"init": `Usage: starpoint init [--force]

Create the global configuration at ~/.starpoint.toml.

If the file already exists, nothing is changed and the command exits 1;
use --force to replace it.

Options:
  --force, -f    Overwrite an existing configuration
  --allow-root   Let it run as root or elevated
  --json         Machine-readable output
`,
	"sync": `Usage: starpoint sync [--dry-run] [--no-confirm]

Make the installed state match the configuration: install what is missing,
move software between providers, and reconcile pinned versions.

Options:
  --dry-run, -n       Show what would change; change nothing
  --no-confirm, -y, --yes
                      Do not ask before changing anything
  --allow-destructive Let an unattended run delete or replace things
  --allow-root        Let it run as root or elevated
  --provider <name>   Restrict automatic provider choice (repeatable)
  --json              Machine-readable output
  --verbose, -v       List every effect, not only what is removed or overwritten
`,
	"inventory": `Usage: starpoint inventory [--provider <name>]...

List every package each available provider reports as installed: provider,
the provider's own name for it, version, scope, path, and whether the
configuration manages it. Managed means the configuration has an entry with
that name and that provider.

Inventory only observes. Providers are probed for health and asked for their
package lists; nothing is resolved, installed, recorded in the configuration
or written to Starpoint's own logs. It works with no configuration at all.
A provider whose listing fails is reported (discover-failed) and the others
are still shown.

Options:
  --provider <name>   Only this provider (repeatable)
  --json              Machine-readable output
`,
	"list": `Usage: starpoint list [--managed] [--provider <name>]

Show the software in the configuration, where it is installed and who owns it,
plus PATH executables whose owner is unknown.

Aliases: ls

Options:
  --managed, -m       Only entries with a recorded provider
  --provider <name>   Only entries owned by this provider
  --json              Machine-readable output
`,
	"info": `Usage: starpoint info <package>

Show one package in detail: where it is installed, which provider has it, which
provider the configuration wants, and its status.

Aliases: show

Options:
  --json    Machine-readable output
`,
	"explain": `Usage: starpoint explain <package>

Show every rule the provider resolver evaluated for a package and which one
decided the outcome.

Options:
  --provider <name>   Restrict the candidates, as install would
  --json              Machine-readable output
`,
	"doctor": `Usage: starpoint doctor [--all]

Check Starpoint, its configuration, providers, ownership, transitions and PATH.

The PATH inventory is summarised by default: counts, names shadowed by an
earlier PATH entry, and executables with an unknown owner. --all lists every
executable. --json always carries the full inventory.

Options:
  --all, -a    List the whole PATH inventory
  --json       Machine-readable output
`,
	"install": `Usage: starpoint install <package>[@version] [provider] [options]
       starpoint install .

Install software and record it, with the provider that owns it, in the
configuration. Starpoint picks the provider and asks when the choice is
ambiguous. A provider may be named before or after the package.

'starpoint install .' installs what the project in the current directory
needs: the packages declared in its .starpoint.toml, its toolchain (recorded in
the project's .starpoint.toml), and its dependencies through the ecosystem's
own tool (npm ci / pnpm install / yarn install, uv sync / pip install -r,
cargo fetch, go mod download, bundle install). The dependency command is shown
and confirmed first; --dry-run only prints it. pip install -r outside a
virtualenv (no active VIRTUAL_ENV, no project .venv) can upgrade or downgrade
packages other software uses, so like npm ci over node_modules it needs
--allow-destructive when unattended.

Aliases: add, i

Options:
  --version, -V <v>    Pin a version (or write it inline: ripgrep@14.1.0)
  --channel <name>     Release channel, for providers that have them
  --project            Record the package in the project's configuration
  --provider <name>    Use this provider
  --dry-run, -n        Show what would happen; change nothing
  --no-confirm, -y     Do not ask first
  --allow-destructive  Let an unattended run delete or replace things
  --allow-root         Let it run as root or elevated
  --json               Machine-readable output
  --verbose, -v        List every effect, not only what is removed or overwritten

Examples:
  starpoint install ruff
  starpoint install ripgrep brew
  starpoint install ripgrep@14.1.0 --provider brew
  starpoint install .
`,
	"remove": `Usage: starpoint remove <package> [provider] [options]

Uninstall software and delete it from the configuration.

Aliases: uninstall, rm

Removing every configured package in one command is refused unless
--allow-empty says you mean it: there is no undo for that shape.

Options:
  --provider <name>    Use this provider
  --dry-run, -n        Show what would happen; change nothing
  --no-confirm, -y     Do not ask first
  --allow-destructive  Let an unattended run delete or replace things
  --allow-root         Let it run as root or elevated
  --allow-empty        Allow removing every configured package at once
  --json               Machine-readable output
  --verbose, -v        List every effect, not only what is removed or overwritten
`,
	"transition": `Usage: starpoint transition [package...] [options]

With no packages, show pending moves of ownership between providers and how
far each has got. With packages, perform those moves: install the new copy,
verify it, then remove the old one.

Aliases: move

Options:
  --all, -a            Include completed transitions in the report
  --reverse, -r        Reverse the most recent transition of each package
  --dry-run, -n        Describe the moves; change nothing
  --no-confirm, -y     Do not ask first
  --allow-destructive  Let an unattended run delete or replace things
  --allow-root         Let it run as root or elevated
  --json               Machine-readable output
  --verbose, -v        List every effect, not only what is removed or overwritten
`,
	"update": `Usage: starpoint update [--self] [--dry-run] [--no-confirm]

Update managed software. A version pinned in the configuration is respected:
when a newer version exists, Starpoint explains the conflict and asks whether
to update anyway, rewriting the pin if you say yes. With --no-confirm (or
without a terminal) the pin is kept.

Aliases: upgrade

Options:
  --self               Also update Starpoint itself, through its provider
  --dry-run, -n        Show what would be updated; change nothing
  --no-confirm, -y     Do not ask first (pins are kept)
  --allow-destructive  Let an unattended run delete or replace things
  --allow-root         Let it run as root or elevated
  --json               Machine-readable output
  --verbose, -v        List every effect, not only what is removed or overwritten
`,
	"runs": `Usage: starpoint runs [--package <name>] [--directory <dir>]

Show the history of package operations Starpoint performed.

Aliases: history

Options:
  --package <name>     Only this package
  --directory <dir>    Only operations run in this directory
  --json               Machine-readable output
`,
	"prune": `Usage: starpoint prune [--dry-run] [--no-confirm]

Remove orphaned user-scoped software that Starpoint-managed providers can
enumerate and remove. Configured packages and system/global software are never
touched. Prune only removes software inside the home directory of the user it
acts for (under sudo, the invoking user's), and never a package manager: no
registered provider (npm, pip, uv, pnpm, ...), corepack or starpoint, wherever
it lives. A system npm prefix or the interpreter's own site-packages is left
alone. Every orphan it leaves alone is still listed, with the reason, as a
prune-skipped warning.

Options:
  --dry-run, -n        Show what would be removed; change nothing
  --no-confirm, -y     Do not ask first
  --allow-destructive  Let an unattended run delete or replace things
  --allow-root         Let it run as root or elevated
  --json               Machine-readable output
  --verbose, -v        List every effect, not only what is removed or overwritten
`,
	"recover": `Usage: starpoint recover [--discard] [--no-confirm]

Inspect files left behind by an interrupted run. Read-only unless --discard is
given; a file that is the only copy of a configuration is never discarded.

Options:
  --discard            Remove the leftover files
  --no-confirm, -y     Do not ask before discarding
  --allow-root         Let --discard run as root or elevated
  --json               Machine-readable output
`,
	"logging": `Usage: starpoint logging [prune | config]

Show what Starpoint logs, how long it keeps it, and whether the system's own log
rotation has been pointed at it. Every command records what it did, and records
older than the retention window are deleted automatically; this command reports
on that rather than being how it happens.

Subcommands:
  (none)              Show the log directory, files, entries and retention window
  prune               Apply the retention window now and report what went
  config              Write the system's log rotation configuration, if the
                      platform has one, into Starpoint's own state directory

Options:
  --json              Machine-readable output

Retention is set with retention_days in ~/.starpoint.toml and defaults to 30.
It applies to the audit log, the transition history and the run history.
`,
	"help": `Usage: starpoint help [command]

Show general help, or help for one command.
`,
}

// printCommandHelp writes a command's help, falling back to the general usage.
func printCommandHelp(w io.Writer, name string) {
	if text, ok := commandHelp[canonicalCommand(name)]; ok {
		fmt.Fprint(w, text)
		return
	}
	printUsage(w)
}

// rejectOption turns a dash-prefixed token that reached the package-name slot
// into a usage error. No package manager has a package whose name starts with a
// dash, and treating one as a name is how `--hepl` becomes an install.
func rejectOption(cmd, arg string) error {
	if strings.HasPrefix(arg, "-") && arg != "-" {
		return fmt.Errorf("%w: unknown option %q for %s", ErrUsage, arg, cmd)
	}
	return nil
}
