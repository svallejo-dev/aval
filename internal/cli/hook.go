package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/svallejo-dev/aval/internal/hook"
)

const hookLong = `hook answers the hooks a coding agent runs: it reads the event as JSON on
stdin and answers on stdout in Claude Code's hook format. It always exits with
0 and writes nothing when anything fails, so a hook never breaks the agent.

In .claude/settings.json:

  {
    "hooks": {
      "PostToolUse": [{"matcher": "Edit|Write",
        "hooks": [{"type": "command", "command": "aval hook post-tool-use"}]}],
      "Stop": [{"hooks": [{"type": "command", "command": "aval hook stop"}]}]
    }
  }`

func newHookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hook <event>",
		Short: "Answer a coding agent's hook event (Claude Code)",
		Long:  hookLong,
		// An event this aval does not know, perhaps from a newer
		// configuration, fails open like any other hook error.
		Args:               cobra.ArbitraryArgs,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help() //nolint:wrapcheck // printing help cannot fail meaningfully
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "aval: unknown hook event %q, ignored\n", args[0])
			return nil
		},
	}
	for _, e := range []struct {
		event hook.Event
		short string
	}{
		{hook.PostToolUse, "Remind the agent that the bound tests it edited are guarded"},
		{hook.Stop, "Keep the agent working until `aval verify` passes on the working tree"},
	} {
		cmd.AddCommand(&cobra.Command{
			Use:   string(e.event),
			Short: e.short,
			// A hook must not fail on how it is invoked either: exit status
			// 2, a usage error, is how an agent hook blocks.
			Args:               cobra.ArbitraryArgs,
			FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
			Run: func(cmd *cobra.Command, _ []string) {
				hook.Run(cmd.Context(), e.event, cmd.InOrStdin(), cmd.OutOrStdout())
			},
		})
	}
	return cmd
}
