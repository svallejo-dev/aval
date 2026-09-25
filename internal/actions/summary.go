package actions

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// MaxStepSummaryBytes is what GitHub keeps of one step summary file: 1 MiB.
// The budget belongs to the file, not to a write, because every step of the
// job appends to the same file.
const MaxStepSummaryBytes = 1 << 20

// truncationNotice closes a summary that did not fit. It is counted against
// the budget before anything is cut, so it always fits.
const truncationNotice = "\n_Cut here: GitHub keeps only 1 MiB of a job's step summary._\n"

var (
	// ErrNoSummaryPath means no file was named: GITHUB_STEP_SUMMARY is unset,
	// so there is no summary to write (see GapStepSummary).
	ErrNoSummaryPath = errors.New("no step summary path")
	// ErrSummaryFull means the file has no room left for a whole line of new
	// content. Nothing was written, and nothing that was already there was
	// touched.
	ErrSummaryFull = errors.New("step summary is full")
)

// WriteStepSummary appends content to the step summary file at path, the one
// GITHUB_STEP_SUMMARY names (ADR-0005 §7).
//
// It only ever appends, because every step of the job writes to the same file,
// and it never appends half a line: when content does not fit in what is left
// of MaxStepSummaryBytes, it writes the longest run of whole lines that fits
// and closes it with a notice. A missing trailing newline is added, so the
// next step's Markdown starts on its own line.
//
// An empty path is ErrNoSummaryPath and a file with no room left is
// ErrSummaryFull; match them with errors.Is. Neither says anything about the
// gate's verdict: a summary that could not be written is not a failed gate.
func WriteStepSummary(path, content string) (err error) {
	if path == "" {
		return fmt.Errorf("write step summary: %w", ErrNoSummaryPath)
	}
	if content == "" {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // the runner names the summary file
	if err != nil {
		return fmt.Errorf("write step summary %s: %w", path, err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("write step summary %s: %w", path, cerr)
		}
	}()
	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("write step summary %s: %w", path, err)
	}
	out, err := fit(content, MaxStepSummaryBytes-fi.Size())
	if err != nil {
		return fmt.Errorf("write step summary %s: %w", path, err)
	}
	if _, err := f.WriteString(out); err != nil {
		return fmt.Errorf("write step summary %s: %w", path, err)
	}
	return nil
}

// WriteStepSummary appends content to the summary of the step this context was
// detected in. It is ErrNoSummaryPath when the environment named no file.
func (c Context) WriteStepSummary(content string) error {
	return WriteStepSummary(c.StepSummaryPath, content)
}

// fit returns what to append so the file stays inside the budget, ending on a
// line boundary. room is how many bytes are left.
func fit(content string, room int64) (string, error) {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if int64(len(content)) <= room {
		return content, nil
	}
	budget := room - int64(len(truncationNotice))
	if budget <= 0 {
		return "", fmt.Errorf("%w: %d of %d bytes left", ErrSummaryFull, max(room, 0), int64(MaxStepSummaryBytes))
	}
	// budget < len(content), because len(content) > room > budget.
	cut := strings.LastIndexByte(content[:budget], '\n')
	if cut < 0 {
		return "", fmt.Errorf("%w: no whole line fits in the %d bytes left", ErrSummaryFull, room)
	}
	return content[:cut+1] + truncationNotice, nil
}
