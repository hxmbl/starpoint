// Command starpoint is a declarative orchestration layer over the package
// managers and developer tooling already on the machine.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/starpoint/starpoint/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cwd, err := os.Getwd()
	if err != nil {
		cwd = "."
	}

	env := &cli.Env{
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Stdin:   os.Stdin,
		WorkDir: cwd,
		Confirm: cli.PromptConfirm(os.Stdin, os.Stdout),
		Choose:  cli.PromptChoose(os.Stdin, os.Stdout),
	}
	os.Exit(cli.Run(ctx, env, os.Args[1:]))
}
