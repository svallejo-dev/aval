package actions

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
)

// MaxStepSummaryBytes is GitHub's budget for a step summary file: 1 MiB. The
// file belongs to one step, not to the job — each step gets its own
// GITHUB_STEP_SUMMARY — and a file over the budget is not truncated by GitHub,
// it fails to upload and the summary is lost. So the budget belongs to the
// file, and a write that would go over it is cut here instead.
const MaxStepSummaryBytes = 1 << 20

// noticeFormat is the line that closes a summary that did not fit. The nonce
// makes it unforgeable: content is written by aval but built from a pull
// request's own test names and spec text, and without a nonce it could print
// this line itself, either faking a cut or hiding one.
const noticeFormat = "\n_Cut here (%s): the step summary hit GitHub's 1 MiB budget._\n"

var (
	// ErrNoSummaryPath means no file was named: GITHUB_STEP_SUMMARY is unset,
	// so there is no summary to write (see GapStepSummary).
	ErrNoSummaryPath = errors.New("no step summary path")
	// ErrSummaryFull means the file has no room left for a whole line of new
	// content. Nothing was written, and nothing already there was touched.
	ErrSummaryFull = errors.New("step summary is full")
	// ErrSummaryTruncated means the content was written but did not fit: whole
	// lines were kept and the rest replaced by the notice. It is not fatal, and
	// it is never silent, because what got cut may be the verdict itself: a
	// caller that sees it can re-emit what matters in a shorter form.
	ErrSummaryTruncated = errors.New("step summary truncated")
	// ErrNotRegularFile means the path is not a regular file: a symlink, a
	// directory or a device. The pull request's code runs in this job and could
	// repoint GITHUB_STEP_SUMMARY at GITHUB_ENV, so the summary is never
	// written through a link.
	ErrNotRegularFile = errors.New("not a regular file")
)

// WriteStepSummary appends content to the step summary file at path, the one
// GITHUB_STEP_SUMMARY names (ADR-0005 §7).
//
// It only ever appends, because the gate may write more than once into the same
// step's file, and it never appends half a line: when content does not fit in
// what is left of MaxStepSummaryBytes, it writes the longest run of whole lines
// that fits, closes it with a notice and returns ErrSummaryTruncated. A missing
// trailing newline is added, so the next write starts on its own line.
//
// It refuses a path that is not a regular file and never follows a symlink:
// content the gate writes must not become an environment file.
//
// Match the outcomes with errors.Is: ErrNoSummaryPath (no file named),
// ErrNotRegularFile, ErrSummaryFull (nothing written) and ErrSummaryTruncated
// (written, cut). None of them says anything about the gate's verdict.
func WriteStepSummary(path, content string) (err error) {
	if path == "" {
		return fmt.Errorf("write step summary: %w", ErrNoSummaryPath)
	}
	if content == "" {
		return nil
	}
	// An earlier step may not have created the file yet, so only an existing
	// non-regular file is a refusal. Opening it would be worse than a refusal:
	// a FIFO would block until someone read from it.
	if fi, lerr := os.Lstat(path); lerr == nil && !fi.Mode().IsRegular() {
		return fmt.Errorf("write step summary %s: %w", path, ErrNotRegularFile)
	}
	// O_NOFOLLOW closes the gap between that check and this open.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY|noFollow, 0o600) //nolint:gosec // the runner names the summary file; symlinks are refused
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
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("write step summary %s: %w", path, ErrNotRegularFile)
	}
	out, cut, err := fit(content, MaxStepSummaryBytes-fi.Size())
	if err != nil {
		return fmt.Errorf("write step summary %s: %w", path, err)
	}
	if _, err := f.WriteString(out); err != nil {
		return fmt.Errorf("write step summary %s: %w", path, err)
	}
	if cut > 0 {
		return fmt.Errorf("write step summary %s: %w: %d of %d bytes did not fit",
			path, ErrSummaryTruncated, cut, len(content))
	}
	return nil
}

// WriteStepSummary appends content to the summary of the step this context was
// detected in. It is ErrNoSummaryPath when the environment named no file.
func (c Context) WriteStepSummary(content string) error {
	return WriteStepSummary(c.StepSummaryPath, content)
}

// fit returns what to append so the file stays inside the budget, ending on a
// line boundary, and how many bytes of content were left out. room is how many
// bytes the file has left.
func fit(content string, room int64) (out string, cut int, err error) {
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	if int64(len(content)) <= room {
		return content, 0, nil
	}
	notice, err := truncationNotice()
	if err != nil {
		return "", 0, err
	}
	budget := room - int64(len(notice))
	if budget <= 0 {
		return "", 0, fmt.Errorf("%w: %d of %d bytes left", ErrSummaryFull, max(room, 0), int64(MaxStepSummaryBytes))
	}
	// budget < len(content), because len(content) > room > budget.
	keep := strings.LastIndexByte(content[:budget], '\n')
	if keep < 0 {
		return "", 0, fmt.Errorf("%w: no whole line fits in the %d bytes left", ErrSummaryFull, room)
	}
	return content[:keep+1] + notice, len(content) - keep - 1, nil
}

// truncationNotice builds the notice with a fresh nonce, so no content can
// produce the same line.
func truncationNotice() (string, error) {
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("truncation notice: %w", err)
	}
	return fmt.Sprintf(noticeFormat, fmt.Sprintf("%x", nonce)), nil
}
