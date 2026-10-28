package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

// CommandName is the executable's name. Starpoint is the project; `str` is
// what people type.
const CommandName = "str"

// LegacyCommandName is the executable's name before STRM-445. It still runs
// the same program during the transition: `go install .../cmd/starpoint`
// builds it, and the Homebrew formula links it to str, so scripts and muscle
// memory written against it keep working.
const LegacyCommandName = "starpoint"

// Main is the whole program behind both executables: cmd/str and the
// transitional cmd/starpoint. args is os.Args, so the name the program was
// started under is args[0].
func Main(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	name := invokedName(args)
	if note := legacyNameNotice(name, IsTerminal(os.Stderr)); note != "" {
		fmt.Fprintln(os.Stderr, note)
	}

	env := &Env{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Stdin:   os.Stdin,
		WorkDir: cwd,
		Name:    name,
		Confirm: PromptConfirm(os.Stdin, os.Stdout),
		Choose:  PromptChoose(os.Stdin, os.Stdout),
		// A run is interactive only when a person can answer at a terminal.
		Interactive: func() bool { return IsTerminal(os.Stdin) },
	}
	var rest []string
	if len(args) > 1 {
		rest = args[1:]
	}
	return Run(ctx, env, rest)
}

// invokedName is the name the program was started under, reduced to the two
// it answers to. A copy renamed to anything else is treated as str.
func invokedName(args []string) string {
	if len(args) == 0 {
		return CommandName
	}
	base := strings.TrimSuffix(filepath.Base(args[0]), ".exe")
	if base == LegacyCommandName {
		return LegacyCommandName
	}
	return CommandName
}

// legacyNameNotice is the one line a person sees when they start the program
// under its old name. It goes to stderr and only when stderr is a terminal, so
// a script that runs `starpoint ... --json` or captures stderr sees exactly
// what it saw before the rename.
func legacyNameNotice(name string, stderrIsTerminal bool) string {
	if name != LegacyCommandName || !stderrIsTerminal {
		return ""
	}
	return "note: 'starpoint' is now 'str'. The old name keeps working for now; the same commands, configuration (~/.starpoint.toml) and --json output apply."
}

// commandName is the name to show in this run's own output, such as the
// version line: what the person typed, or str.
func (e *Env) commandName() string {
	if e.Name == LegacyCommandName {
		return LegacyCommandName
	}
	return CommandName
}
