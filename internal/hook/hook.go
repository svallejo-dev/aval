// Package hook answers Claude Code's hooks, fast and without Charm
// (ADR-0003). The CI gate is the real check; hooks give early feedback:
//
//   - post-tool-use reminds the agent, without blocking, that the test file
//     it edited binds tests to obligations outside the delta of every active
//     change, which the gate reports as tamper (ADR-0005 §4);
//   - stop keeps the agent working until the last `aval verify` passed on the
//     current working tree (ADR-0005 §7, see Status).
//
// The event arrives as JSON on stdin and the answer leaves as JSON on
// stdout, with exit status 0. Other agents' events decode as far as they
// match Claude Code's; their own shapes are for a later shim (M6).
//
// Hooks fail open: when the event cannot be read, the directory is not in a
// git repository, anything fails or the deadline passes, they write nothing
// and exit with 0, so a hook never breaks the agent.
package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Event is a hook event aval answers, named as its subcommand.
type Event string

// Supported events.
const (
	PostToolUse Event = "post-tool-use"
	Stop        Event = "stop"
)

const (
	maxInput = 64 << 20        // a PostToolUse event carries the tool's response: maybe a whole file
	timeout  = 5 * time.Second // past it, the hook fails open
)

// Run reads event e from stdin and writes the agent's answer, if any, to
// stdout. It has no error to return: a hook fails open.
func Run(ctx context.Context, e Event, stdin io.Reader, stdout io.Writer) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if r := respond(ctx, e, stdin); r != nil {
		_ = json.NewEncoder(stdout).Encode(r) // the agent stopped listening: nothing left to do
	}
}

// respond returns the answer to e, or nil when there is none, when anything
// fails or panics, or when ctx is done first. The work runs in a goroutine so
// that nothing outlives the deadline: ctx kills git, and a blocked read of
// stdin or of a file ends with the process.
func respond(ctx context.Context, e Event, stdin io.Reader) *response {
	done := make(chan *response, 1)
	go func() {
		defer func() {
			if recover() != nil {
				done <- nil
			}
		}()
		done <- answer(ctx, e, stdin)
	}()
	select {
	case r := <-done:
		return r
	case <-ctx.Done():
		return nil
	}
}

func answer(ctx context.Context, e Event, stdin io.Reader) *response {
	in, err := readInput(stdin)
	if err != nil {
		return nil
	}
	switch e {
	case PostToolUse:
		return checkBoundTests(ctx, in)
	case Stop:
		return checkVerified(ctx, in)
	}
	return nil
}

// response is Claude Code's answer to a hook: Decision and Reason for Stop,
// Specific for PostToolUse.
type response struct {
	Decision string    `json:"decision,omitempty"` // "block" keeps the agent working
	Reason   string    `json:"reason,omitempty"`
	Specific *specific `json:"hookSpecificOutput,omitempty"`
}

type specific struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

// input is the part of a Claude Code event that aval reads.
type input struct {
	Cwd            string    `json:"cwd"` // "" is the process's working directory
	ToolName       string    `json:"tool_name"`
	ToolInput      toolInput `json:"tool_input"`
	StopHookActive bool      `json:"stop_hook_active"`
}

type toolInput struct {
	FilePath string `json:"file_path"` // absolute for Edit and Write
}

func readInput(r io.Reader) (input, error) {
	var in input
	if err := json.NewDecoder(io.LimitReader(r, maxInput)).Decode(&in); err != nil {
		return input{}, fmt.Errorf("decode hook event: %w", err)
	}
	return in, nil
}
