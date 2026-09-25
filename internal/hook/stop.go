package hook

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/svallejo-dev/aval/internal/platform/git"
)

const verifyReason = "Run `aval verify` and fix failures before finishing."

// checkVerified answers a Stop event: it blocks unless the last verify passed
// on the current working tree. It does not block when a stop hook already
// made the agent continue, so that a failing verify cannot trap it, nor when
// there is nothing to verify: no status, a clean tree and a HEAD that some
// remote-tracking branch contains, as after a turn that only answered a
// question. An unpushed HEAD may hold commits nobody verified.
func checkVerified(ctx context.Context, in input) *response {
	if in.StopHookActive {
		return nil
	}
	dir := cmp.Or(in.Cwd, ".")
	pushed := pushedHead(ctx, dir)
	root, key, err := CurrentKey(ctx, dir)
	if err != nil {
		return nil
	}
	s, err := ReadStatus(root)
	switch {
	case errors.Is(err, ErrNoStatus):
		if key.Clean() && <-pushed {
			return nil
		}
	case err != nil:
		return nil // there may be a status, but it cannot be read
	case s.Key == key && s.Passed:
		return nil
	}
	return &response{Decision: "block", Reason: verifyReason}
}

// pushedHead reports, once git answers, whether a remote-tracking branch
// contains HEAD: no commit of HEAD is missing from every remote. Unlike
// for-each-ref --contains, rev-list walks the history once, whatever the
// number of remote refs. It reports true when git fails: the hook fails open.
func pushedHead(ctx context.Context, dir string) <-chan bool {
	pushed := make(chan bool, 1)
	go func() {
		out, err := newGit(dir).Run(ctx, nil, "rev-list", "-n1", "HEAD", "--not", "--remotes")
		pushed <- err != nil || len(out) == 0
	}()
	return pushed
}

// StatusFile is where `aval verify` leaves its Status, relative to the
// repository root (ADR-0005 §7).
const StatusFile = ".aval/cache/verify-status.json"

// StatusVersion is the schemaVersion that WriteStatus stamps.
const StatusVersion = 1

// maxStatus caps what ReadStatus reads: a status is a few hundred bytes.
const maxStatus = 1 << 16

// Key identifies a working tree (ADR-0005 §7): the commit HEAD resolves to,
// and in Diff the hex SHA-256 of the output of `git diff HEAD` followed, for
// each untracked, not ignored file in path order, by
// "<path>\x00<hex SHA-256 of its content>\n". A symlink's content is its
// target. Untracked files under .aval/cache/ and .aval/evidence/ are left
// out: they are aval's own output, which verify writes after it takes the key.
type Key struct {
	Head string `json:"head"`
	Diff string `json:"diff"`
}

// Clean reports whether k is a tree with no change to tracked files and no
// untracked file.
func (k Key) Clean() bool { return k.Diff == emptyDigest }

var emptyDigest = hex.EncodeToString(sha256.New().Sum(nil))

// Status is the outcome of the last `aval verify`, stored in StatusFile:
//
//	{
//	  "schemaVersion": 1,
//	  "head": "<commit SHA>",
//	  "diff": "<hex SHA-256, see Key>",
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
// status: the file is missing, is not a regular file, is not JSON, or has
// another schemaVersion.
var ErrNoStatus = errors.New("no verify status")

// WriteStatus stores s in the StatusFile of the repository at root. It
// replaces the file atomically, so a hook never reads half a status, and
// makes git ignore the cache directory: a committed status would change the
// key it is stored under.
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
// Only a regular file counts, so that a FIFO or a link to a device cannot
// hang the hook.
func ReadStatus(root string) (Status, error) {
	name := filepath.Join(root, filepath.FromSlash(StatusFile))
	fi, err := os.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return Status{}, fmt.Errorf("%w: %w", ErrNoStatus, err)
	}
	if err != nil {
		return Status{}, fmt.Errorf("read verify status: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return Status{}, fmt.Errorf("%w: %s is not a regular file", ErrNoStatus, name)
	}
	f, err := os.Open(name) //nolint:gosec // a fixed path below the repository root
	if err != nil {
		return Status{}, fmt.Errorf("read verify status: %w", err)
	}
	defer f.Close()
	var s Status
	if err := json.NewDecoder(io.LimitReader(f, maxStatus)).Decode(&s); err != nil {
		return Status{}, fmt.Errorf("%w: %s: %w", ErrNoStatus, name, err)
	}
	if s.SchemaVersion != StatusVersion {
		return Status{}, fmt.Errorf("%w: %s has schemaVersion %d, want %d", ErrNoStatus, name, s.SchemaVersion, StatusVersion)
	}
	return s, nil
}

// CurrentKey returns the root of the git working tree that holds dir and its
// Key. It runs its three git commands concurrently, for latency, with the
// hooks' git (newGit).
func CurrentKey(ctx context.Context, dir string) (root string, key Key, err error) {
	g := newGit(dir)
	type result struct {
		out []byte
		err error
	}
	output := func(args ...string) <-chan result {
		ch := make(chan result, 1)
		go func() {
			out, err := g.Run(ctx, nil, args...)
			ch <- result{out, err}
		}()
		return ch
	}
	revParse := output("rev-parse", "--show-toplevel", "HEAD")
	untracked := output("ls-files", "-z", "--others", "--exclude-standard", "--full-name", "--", ":/")
	// The flags make the output independent of diff drivers, colors,
	// diff.relative and the submodules' ignore settings; --binary makes
	// binary edits count.
	h := sha256.New()
	diffErr := g.Stream(ctx, h, "diff", "--binary", "--no-color", "--no-ext-diff", "--no-textconv", "--no-relative",
		"--ignore-submodules=none", "HEAD", "--")

	rp, ut := <-revParse, <-untracked
	if err := errors.Join(rp.err, ut.err, diffErr); err != nil {
		return "", Key{}, fmt.Errorf("git in %s: %w", dir, err)
	}
	top, head, ok := strings.Cut(strings.TrimSpace(string(rp.out)), "\n")
	if !ok || top == "" || head == "" {
		return "", Key{}, fmt.Errorf("git rev-parse in %s: unexpected output %q", dir, rp.out)
	}
	root = filepath.FromSlash(top)
	for p := range strings.SplitSeq(string(ut.out), "\x00") {
		if p == "" || strings.HasPrefix(p, ".aval/cache/") || strings.HasPrefix(p, ".aval/evidence/") {
			continue
		}
		sum, err := contentDigest(filepath.Join(root, filepath.FromSlash(p)))
		if err != nil {
			return "", Key{}, err
		}
		fmt.Fprintf(h, "%s\x00%x\n", p, sum)
	}
	return root, Key{Head: head, Diff: hex.EncodeToString(h.Sum(nil))}, nil
}

// contentDigest returns the SHA-256 of the regular file name, of its target
// if it is a symlink, and of nothing for anything else, or if it is gone.
func contentDigest(name string) ([]byte, error) {
	h := sha256.New()
	fi, err := os.Lstat(name)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("hash untracked file: %w", err)
	case fi.Mode()&fs.ModeSymlink != 0:
		target, err := os.Readlink(name)
		if err != nil {
			return nil, fmt.Errorf("hash untracked file: %w", err)
		}
		h.Write([]byte(target))
	case fi.Mode().IsRegular():
		f, err := os.Open(name) //nolint:gosec // a file git listed in the working tree
		if err != nil {
			return nil, fmt.Errorf("hash untracked file: %w", err)
		}
		defer f.Close()
		if _, err := io.Copy(h, f); err != nil {
			return nil, fmt.Errorf("hash untracked file: %w", err)
		}
	}
	return h.Sum(nil), nil
}

// newGit returns the Runner the hooks run git with: hardened, but reading
// attributes from the working tree (git.WithoutAttrSource). The hooks read
// the agent's own working tree for an advisory key, within ADR-0003's 50 ms,
// and --attr-source would cost a git hash-object before any other git. An
// agent that could plant a .gitattributes could as well write the status by
// hand; the gate in CI, which does not trust head, keeps --attr-source.
func newGit(dir string) *git.Runner {
	return git.New(dir, git.WithoutAttrSource())
}
