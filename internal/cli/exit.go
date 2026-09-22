package cli

import "fmt"

// Exit codes are part of aval's public contract. Scripts, CI
// jobs and agent hooks branch on them, so they never change meaning.
const (
	ExitOK     = 0 // success
	ExitFailed = 1 // verification failed or the gate blocked the change
	ExitUsage  = 2 // invalid invocation: unknown command, bad flag, bad config
	ExitTool   = 3 // a required tool is missing or has the wrong version
)

// ExitError carries a specific exit code out of a command's RunE.
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("exit status %d", e.Code)
	}
	return e.Err.Error()
}

func (e *ExitError) Unwrap() error { return e.Err }

// usageError marks err as an invalid invocation.
func usageError(err error) error {
	return &ExitError{Code: ExitUsage, Err: err}
}

// errorCode names an exit code in the JSON envelope's errors.
func errorCode(exitCode int) string {
	switch exitCode {
	case ExitFailed:
		return "failed"
	case ExitUsage:
		return "usage"
	case ExitTool:
		return "tool"
	}
	return "unknown"
}
