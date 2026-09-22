// Command aval governs agentic development of Go services: SDD is the intent
// layer and a harness decides, with evidence, whether a change may advance.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/svallejo-dev/aval/internal/cli"
)

func main() {
	os.Exit(run())
}

// run wires the process to the command tree and returns the exit code.
// It is separate from main so testscript can invoke aval in-process.
func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return cli.Execute(ctx, os.Args[1:], os.Stdout, os.Stderr)
}
