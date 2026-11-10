package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// CommandName is the executable's name. Starpoint is the project; `str` is
// what people type.
const CommandName = "str"

// Main is the whole program behind cmd/str. args is os.Args.
func Main(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	env := &Env{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Stdin:   os.Stdin,
		WorkDir: cwd,
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
