// Package cli defines aval's command tree, its global flags and how command
// results map to process exit codes.
package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/svallejo-dev/aval/internal/envelope"
	"github.com/svallejo-dev/aval/internal/ui"
)

// globalFlags are inherited by every subcommand.
type globalFlags struct {
	json        bool
	plain       bool
	yes         bool
	noAnimation bool
}

// printer resolves the output settings once cobra has parsed the flags and
// returns a printer for cmd's streams. The mode follows stdout, where results go.
func (g *globalFlags) printer(cmd *cobra.Command) *ui.Printer {
	stdout := cmd.OutOrStdout()
	s := ui.Resolve(ui.Input{
		Getenv:         os.Getenv,
		JSON:           g.json,
		Plain:          g.plain,
		Yes:            g.yes,
		NoAnimation:    g.noAnimation,
		OutputTerminal: ui.IsTerminal(stdout),
		StdinTerminal:  ui.IsTerminal(cmd.InOrStdin()),
	})
	return ui.NewPrinter(s, stdout, cmd.ErrOrStderr())
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
		Args:          cobra.NoArgs,
		RunE: runE(func(cmd *cobra.Command, _ []string) error {
			if g.json {
				return usageError(errors.New("a command is required, e.g. `aval version --json`"))
			}
			return cmd.Help() //nolint:wrapcheck // printing help cannot fail meaningfully
		}),
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

// runE adapts a command body so that any error it returns without an explicit
// exit code counts as a failed command (exit 1), never as a usage error.
func runE(fn func(*cobra.Command, []string) error) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		err := fn(cmd, args)
		if err == nil {
			return nil
		}
		var ee *ExitError
		if errors.As(err, &ee) {
			return err
		}
		return &ExitError{Code: ExitFailed, Err: err}
	}
}

// Execute runs aval with args and returns the process exit code.
func Execute(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return execute(ctx, newRootCmd(stdout, stderr), args, stdout, stderr)
}

// execute runs root and maps its error to an exit code. Commands built with
// runE always return *ExitError; any other error comes from cobra itself
// (unknown command, invalid arguments, conflicting flags), so it is usage.
func execute(ctx context.Context, root *cobra.Command, args []string, stdout, stderr io.Writer) int {
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}

	code := ExitUsage
	var ee *ExitError
	if errors.As(err, &ee) {
		code = ee.Code
		if ee.Err == nil {
			return code // the command already reported its outcome
		}
	}

	s := errorSettings(args, ui.IsTerminal(stderr), os.Getenv)
	ui.NewPrinter(s, stdout, stderr).PrintError(commandName(root, args), envelope.Issue{
		Code:    errorCode(code),
		Message: err.Error(),
	})
	return code
}

// errorSettings resolves how execute reports an error. Flag parsing may have
// failed before cobra bound the flags, so --json and --plain are read from
// args. The message goes to stderr, so the mode follows stderr, except in json
// mode, where the envelope goes to stdout. Reporting an error never prompts.
func errorSettings(args []string, stderrTerminal bool, getenv func(string) string) ui.Settings {
	return ui.Resolve(ui.Input{
		Getenv:         getenv,
		JSON:           boolFlag(args, "json"),
		Plain:          boolFlag(args, "plain"),
		OutputTerminal: stderrTerminal,
	})
}

// boolFlag reads the bool flag --name from args without cobra. Like pflag, it
// accepts --name and --name=<v> for any v strconv.ParseBool accepts, the last
// occurrence wins, and "--" ends the flags.
func boolFlag(args []string, name string) bool {
	on := false
	for _, a := range args {
		if a == "--" {
			break
		}
		rest, ok := strings.CutPrefix(a, "--"+name)
		if !ok {
			continue
		}
		if rest == "" {
			on = true
		} else if v, ok := strings.CutPrefix(rest, "="); ok {
			if b, err := strconv.ParseBool(v); err == nil {
				on = b
			}
		}
	}
	return on
}

// commandName returns the subcommand args point to, or "aval".
func commandName(root *cobra.Command, args []string) string {
	if cmd, _, err := root.Find(args); err == nil && cmd != root {
		return cmd.Name()
	}
	return "aval"
}
