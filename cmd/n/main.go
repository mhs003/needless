// Command n is Needless: it maps a natural-language prompt to one of the
// user's own nscript commands and runs it.
//
// It is a thin shell around internal/cli, which owns the behaviour and the
// exit codes. Nothing here but wiring, so that every decision the CLI makes is
// testable without a process.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/mhs003/needless/internal/cli"
)

func main() {
	// Ctrl-C cancels the context, which stops a running command's child
	// processes and shuts the model worker down.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	os.Exit(cli.Main(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
