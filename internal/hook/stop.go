package hook

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const verifyReason = "Run `aval verify` and fix failures before finishing."

// checkVerified answers a Stop event: it blocks unless the last verify passed
// on the current working tree. When a stop hook already made the agent
// continue (stop_hook_active, or Cursor's loop_count), it lets the agent
// stop, so that a failing verify cannot trap it in a loop.
func checkVerified(ctx context.Context, in input) *response {
	if in.StopHookActive || in.LoopCount > 0 {
		return nil
	}
	root, key, err := CurrentKey(ctx, cmp.Or(in.Cwd, "."))
	if err != nil {
		return nil
	}
	s, err := ReadStatus(root)
	if err != nil && !errors.Is(err, ErrNoStatus) {
		return nil // there may be a status, but it cannot be read
	}
	if err == nil && s.Key == key && s.Passed {
		return nil
	}
	return &response{Decision: "block", Reason: verifyReason}
}

// StatusFile is where `aval verify` leaves its Status, relative to the
// repository root (ADR-0005 §7).
const StatusFile = ".aval/cache/verify-status.json"

// StatusVersion is the schemaVersion that WriteStatus stamps.
const StatusVersion = 1

// Key identifies a working tree: the commit HEAD resolves to and the hex
// SHA-256 of `git diff HEAD`. Any change to a tracked file changes it;
// untracked files are not part of it (ADR-0005 §7).
type Key struct {
	Head string `json:"head"`
	Diff string `json:"diff"`
}

// Status is the outcome of the last `aval verify`, stored in StatusFile:
//
//	{
//	  "schemaVersion": 1,
//	  "head": "<commit SHA>",
//	  "diff": "<hex SHA-256 of git diff HEAD>",
//	  "passed": true,
//	  "verifiedAt": "2026-09-22T10:00:00Z"
//	}
//
// verify takes the key with CurrentKey before it checks anything, so that an
// edit made during the run leaves the status stale.
type Status struct {
	SchemaVersion int `json:"schemaVersion"`
	Key
	Passed     bool      `json:"passed"` // verify found nothing that blocks
	VerifiedAt time.Time `json:"verifiedAt"`
}

// ErrNoStatus is wrapped by ReadStatus's error when there is no usable
// status: the file is missing, is not JSON, or has another schemaVersion.
var ErrNoStatus = errors.New("no verify status")

// WriteStatus stores s in the StatusFile of the repository at root. It
// replaces the file atomically, so a hook never reads half a status, and
// makes git ignore the cache directory: a committed status would change the
// diff it is keyed on.
func WriteStatus(root string, s Status) error {
	s.SchemaVersion = StatusVersion
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode verify status: %w", err)
	}
	name := filepath.Join(root, filepath.FromSlash(StatusFile))
	dir := filepath.Dir(name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("write verify status: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("*\n"), 0o600); err != nil {
		return fmt.Errorf("write verify status: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".verify-status-*")
	if err != nil {
		return fmt.Errorf("write verify status: %w", err)
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	_, err = tmp.Write(append(data, '\n'))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), name)
	}
	if err != nil {
		return fmt.Errorf("write verify status: %w", err)
	}
	return nil
}

// ReadStatus reads the Status in the StatusFile of the repository at root.
func ReadStatus(root string) (Status, error) {
	name := filepath.Join(root, filepath.FromSlash(StatusFile))
	data, err := os.ReadFile(name) //nolint:gosec // a fixed path below the repository root
	if errors.Is(err, fs.ErrNotExist) {
		return Status{}, fmt.Errorf("%w: %w", ErrNoStatus, err)
	}
	if err != nil {
		return Status{}, fmt.Errorf("read verify status: %w", err)
	}
	var s Status
	if err := json.Unmarshal(data, &s); err != nil {
		return Status{}, fmt.Errorf("%w: %s: %w", ErrNoStatus, name, err)
	}
	if s.SchemaVersion != StatusVersion {
		return Status{}, fmt.Errorf("%w: %s has schemaVersion %d, want %d", ErrNoStatus, name, s.SchemaVersion, StatusVersion)
	}
	return s, nil
}

// CurrentKey returns the root of the git working tree that holds dir and its
// Key. It runs git rev-parse and git diff concurrently, for latency.
func CurrentKey(ctx context.Context, dir string) (root string, key Key, err error) {
	type result struct {
		out []byte
		err error
	}
	revParse := make(chan result, 1)
	go func() {
		out, err := git(ctx, dir, "rev-parse", "--show-toplevel", "HEAD").Output()
		revParse <- result{out, err}
	}()
	// The flags make the output independent of diff drivers, colors and
	// diff.relative; --binary makes binary edits count.
	h := sha256.New()
	diff := git(ctx, dir, "diff", "--binary", "--no-color", "--no-ext-diff", "--no-textconv", "--no-relative", "HEAD", "--")
	diff.Stdout = h
	diffErr := diff.Run()

	rp := <-revParse
	if rp.err != nil {
		return "", Key{}, fmt.Errorf("git rev-parse in %s: %w", dir, rp.err)
	}
	if diffErr != nil {
		return "", Key{}, fmt.Errorf("git diff HEAD in %s: %w", dir, diffErr)
	}
	top, head, ok := strings.Cut(strings.TrimSpace(string(rp.out)), "\n")
	if !ok || top == "" || head == "" {
		return "", Key{}, fmt.Errorf("git rev-parse in %s: unexpected output %q", dir, rp.out)
	}
	return filepath.FromSlash(top), Key{Head: head, Diff: hex.EncodeToString(h.Sum(nil))}, nil
}

// git returns a git command for dir. GIT_OPTIONAL_LOCKS=0 keeps git diff from
// taking the index lock the agent's own git commands need.
func git(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...) //nolint:gosec // fixed git subcommands; dir is only a -C argument
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	cmd.WaitDelay = time.Second
	return cmd
}
