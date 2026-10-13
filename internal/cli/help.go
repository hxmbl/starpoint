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
	"help":       "help",
}

// canonicalCommand returns the canonical name for a command spelling, or "" when
// the spelling is not a command.
func canonicalCommand(name string) string { return aliases[name] }

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

If the file already exists, nothing is changed and the command exits non-zero.

Options:
  --force, -f    Overwrite an existing configuration
  --json         Machine-readable output
`,
	"sync": `Usage: starpoint sync [--dry-run] [--no-confirm]

Make the installed state match the configuration: install what is missing,
move software between providers, and reconcile pinned versions.

Options:
  --dry-run, -n       Show what would change; change nothing
  --no-confirm, -y    Do not ask before changing anything
  --provider <name>   Restrict automatic provider choice (repeatable)
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
	"doctor": `Usage: starpoint doctor

Check Starpoint, its configuration, providers, ownership, transitions and PATH.

Options:
  --json       Machine-readable output
`,
	"install": `Usage: starpoint install <package>[@version] [provider] [options]
       starpoint install .

Install software and record it, with the provider that owns it, in the
configuration. Starpoint picks the provider and asks when the choice is
ambiguous. A provider may be named before or after the package.

'starpoint install .' installs what the project in the current directory
needs: the packages declared in its .starpoint.toml, and the toolchain its
ecosystem expects.

Aliases: add, i

Options:
  --version, -V <v>    Pin a version (or write it inline: ripgrep@14.1.0)
  --channel <name>     Release channel, for providers that have them
  --project            Record the package in the project's configuration
  --provider <name>    Use this provider
  --dry-run, -n        Show what would happen; change nothing
  --no-confirm, -y     Do not ask first
  --json               Machine-readable output

Examples:
  starpoint install ruff
  starpoint install ripgrep brew
  starpoint install ripgrep@14.1.0 --provider brew
  starpoint install .
`,
	"remove": `Usage: starpoint remove <package> [provider] [options]

Uninstall software and delete it from the configuration.

Aliases: uninstall, rm

Options:
  --provider <name>    Use this provider
  --dry-run, -n        Show what would happen; change nothing
  --no-confirm, -y     Do not ask first
  --json               Machine-readable output
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
  --json               Machine-readable output
`,
	"update": `Usage: starpoint update [--self] [--dry-run] [--no-confirm]

Update managed software. A version pinned in the configuration is respected
and left unchanged.

Aliases: upgrade

Options:
  --self               Also update Starpoint itself, through its provider
  --dry-run, -n        Show what would be updated; change nothing
  --no-confirm, -y     Do not ask first
  --json               Machine-readable output
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
touched.

Options:
  --dry-run, -n        Show what would be removed; change nothing
  --no-confirm, -y     Do not ask first
  --json               Machine-readable output
`,
	"recover": `Usage: starpoint recover [--discard] [--no-confirm]

Inspect files left behind by an interrupted run. Read-only unless --discard is
given; a file that is the only copy of a configuration is never discarded.

Options:
  --discard            Remove the leftover files
  --no-confirm, -y     Do not ask before discarding
  --json               Machine-readable output
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
