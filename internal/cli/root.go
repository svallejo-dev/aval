// Package cli defines aval's command tree, its global flags and how command
// results map to process exit codes.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// globalFlags are inherited by every subcommand.
type globalFlags struct {
	json        bool
	plain       bool
	yes         bool
	noAnimation bool
}

func newRootCmd(stdout, stderr io.Writer) *cobra.Command {
	var g globalFlags

	root := &cobra.Command{
		Use:   "aval",
		Short: "Govern agentic development: specs define obligations, evidence decides",
		Long: "aval turns every obligation of a spec into a row " +
			"obligation → verifier → evidence → gate, and lets a change advance " +
			"only when executed, traceable evidence backs it.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError(err)
	})

	pf := root.PersistentFlags()
	pf.BoolVar(&g.json, "json", false, "emit a machine-readable JSON envelope (for agents and hooks)")
	pf.BoolVar(&g.plain, "plain", false, "emit plain text without colors or animations (for CI logs)")
	pf.BoolVar(&g.yes, "yes", false, "accept defaults instead of prompting")
	pf.BoolVar(&g.noAnimation, "no-animation", false, "disable animations")
	root.MarkFlagsMutuallyExclusive("json", "plain")

	root.AddCommand(newVersionCmd(&g))
	return root
}

// Execute runs aval with args and returns the process exit code.
//
// Commands report domain failures as *ExitError. Any other error comes from
// cobra itself (unknown command, invalid arguments, conflicting flags), so it
// is a usage error.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	root := newRootCmd(stdout, stderr)
	root.SetArgs(args)

	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}
	fmt.Fprintln(stderr, "aval:", err)

	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return ExitUsage
}
