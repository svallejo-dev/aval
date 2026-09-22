// Package hook answers the hooks that coding agents run, fast and without
// Charm (ADR-0003). The CI gate is the real check; hooks give early feedback:
//
//   - post-tool-use reminds the agent, without blocking, that the test file
//     it edited binds tests to obligations outside the delta of every active
//     change, which the gate reports as tamper (ADR-0005 §4);
//   - stop keeps the agent working until the last `aval verify` passed on the
//     current working tree (ADR-0005 §7, see Status).
//
// The protocol is Claude Code's: the event arrives as JSON on stdin and the
// answer leaves as JSON on stdout, with exit status 0. Cursor's and GitHub
// Copilot's event shapes are read too, but answered in the same format.
//
// Hooks fail open: when the event cannot be read, the directory is not in a
// git repository or anything else fails, they write nothing and exit with 0,
// so a hook never breaks the agent.
package hook

import (
	"cmp"
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
	if r := respond(ctx, e, stdin); r != nil {
		_ = json.NewEncoder(stdout).Encode(r) // the agent stopped listening: nothing left to do
	}
}

// respond returns the answer to e, or nil. A panic yields nil too: a bug in
// a hook must not break the agent either.
func respond(ctx context.Context, e Event, stdin io.Reader) (r *response) {
	defer func() {
		if recover() != nil {
			r = nil
		}
	}()
	in, err := readInput(stdin)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
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

// input is what aval reads from an event, whichever agent sent it.
type input struct {
	Cwd            string // "" is the process's working directory
	Tool           string
	FilePath       string // relative paths are relative to Cwd
	StopHookActive bool
	LoopCount      int
}

// wireInput has the fields of Claude Code, Cursor and Copilot events.
// encoding/json matches keys case-insensitively, but tool_name and toolName
// still differ.
type wireInput struct {
	Cwd            string          `json:"cwd"`
	WorkspaceRoots []string        `json:"workspace_roots"` // Cursor
	ToolName       string          `json:"tool_name"`
	ToolNameCamel  string          `json:"toolName"` // Copilot
	ToolInput      json.RawMessage `json:"tool_input"`
	ToolArgs       json.RawMessage `json:"toolArgs"`  // Copilot: an object, or one encoded as a string
	FilePath       string          `json:"file_path"` // Cursor's afterFileEdit
	StopHookActive bool            `json:"stop_hook_active"`
	LoopCount      int             `json:"loop_count"` // Cursor
}

func readInput(r io.Reader) (input, error) {
	var w wireInput
	if err := json.NewDecoder(io.LimitReader(r, maxInput)).Decode(&w); err != nil {
		return input{}, fmt.Errorf("decode hook event: %w", err)
	}
	in := input{
		Cwd:            w.Cwd,
		Tool:           cmp.Or(w.ToolName, w.ToolNameCamel),
		FilePath:       cmp.Or(argsFile(w.ToolInput), argsFile(w.ToolArgs), w.FilePath),
		StopHookActive: w.StopHookActive,
		LoopCount:      w.LoopCount,
	}
	if in.Cwd == "" && len(w.WorkspaceRoots) > 0 {
		in.Cwd = w.WorkspaceRoots[0]
	}
	return in, nil
}

// argsFile returns the file_path, filePath or path of a tool's arguments.
func argsFile(raw json.RawMessage) string {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = json.RawMessage(encoded)
	}
	var args struct {
		FilePath      string `json:"file_path"`
		FilePathCamel string `json:"filePath"`
		Path          string `json:"path"`
	}
	if json.Unmarshal(raw, &args) != nil {
		return ""
	}
	return cmp.Or(args.FilePath, args.FilePathCamel, args.Path)
}
