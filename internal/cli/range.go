package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/svallejo-dev/aval/internal/platform/git"
)

// rangeFlags are the flags `aval verify` and `aval gate` share: which
// repository to work in and which commits to judge (ADR-0005 §1).
type rangeFlags struct {
	base string
	head string
	dir  string
}

// bind declares the flags on cmd.
func (f *rangeFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.base, "base", "", "base of the range (default: the merge base of the head with origin/main, the configured upstream or main)")
	fl.StringVar(&f.head, "head", "", "head of the range, the commit being judged (default: HEAD)")
	fl.StringVar(&f.dir, "dir", "", "any directory of the repository (default: the working directory); aval works from its root")
}

// defaultBaseRefs are the revisions the base comes from when --base is not
// given, in order: the main branch of the remote, which is what a pull request
// targets; then the branch's own upstream, for a fork or a repository whose
// remote is not called origin; then a local main, for a repository with no
// remote at all (ADR-0005 §1).
var defaultBaseRefs = []string{"origin/main", "@{upstream}", "main"}

// errNoRevision marks a revision this repository does not have, or that shares
// no history with the head. It is internal: resolveRange either tries the next
// candidate or turns it into a usage error that names every one it tried.
var errNoRevision = errors.New("no such revision")

// openRepo resolves the repository aval works in: a hardened git runner in dir
// and the absolute root of its working tree, which is where the evidence
// bundle and the hook status go.
//
// git's version is checked first, so that a git older than 2.40 is exit 3
// (ADR-0005 §6) and not a puzzling failure of the first command that needs
// --attr-source.
func openRepo(ctx context.Context, dir string) (string, *git.Runner, error) {
	where := "the working directory"
	if dir != "" {
		where = dir
		if _, err := os.Stat(dir); err != nil {
			return "", nil, usageError(fmt.Errorf("--dir: %w", err))
		}
	}
	g := git.New(cmp.Or(dir, "."))
	if err := g.CheckVersion(ctx); err != nil {
		if errors.Is(err, git.ErrToolMissing) {
			return "", nil, &ExitError{Code: ExitTool, Err: err}
		}
		return "", nil, fmt.Errorf("run git in %s: %w", where, err)
	}
	out, err := g.Run(ctx, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", nil, usageError(fmt.Errorf("find the repository root of %s: %w", where, err))
	}
	root := strings.TrimSpace(string(out))
	if root == "" { // a bare repository: git says nothing and exits 0
		return "", nil, usageError(fmt.Errorf("%s is not inside a git working tree", where))
	}
	return filepath.Clean(root), g, nil
}

// resolveRange resolves the range aval judges into full commit SHAs: the head
// is --head, or HEAD, and the base is --base, or the merge base of the head
// with the first of refs that both resolves and shares history with it.
//
// When nothing resolves, the error names every candidate it tried: "cannot
// resolve the base" on its own leaves whoever reads it nothing to act on.
func resolveRange(ctx context.Context, g *git.Runner, f rangeFlags, refs []string) (base, head string, err error) {
	head, err = revision(ctx, g, cmp.Or(f.head, "HEAD"))
	switch {
	case errors.Is(err, errNoRevision) && f.head == "":
		return "", "", usageError(errors.New("HEAD names no commit: this repository has no commits yet, so there is nothing to judge"))
	case errors.Is(err, errNoRevision):
		return "", "", usageError(fmt.Errorf("--head %q names no commit of this repository", f.head))
	case err != nil:
		return "", "", err
	}

	if f.base != "" {
		base, err = revision(ctx, g, f.base)
		if errors.Is(err, errNoRevision) {
			return "", "", usageError(fmt.Errorf("--base %q names no commit of this repository", f.base))
		}
		if err != nil {
			return "", "", err
		}
		return base, head, nil
	}
	for _, ref := range refs {
		base, err = mergeBase(ctx, g, ref, head)
		if err == nil {
			return base, head, nil
		}
		if !errors.Is(err, errNoRevision) {
			return "", "", err
		}
	}
	return "", "", usageError(fmt.Errorf("cannot resolve the base of %s: none of %s names a commit that shares history with it; "+
		"fetch the target branch (in CI, actions/checkout with fetch-depth: 0) or pass --base",
		head, strings.Join(quoted(refs), ", ")))
}

// revision resolves rev to the full SHA of the commit it names, and
// errNoRevision when this repository does not have it.
func revision(ctx context.Context, g *git.Runner, rev string) (string, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("%w: %q is not a revision", errNoRevision, rev)
	}
	out, err := g.Run(ctx, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	// --quiet leaves exit 1 and no message for a revision that is not there;
	// a malformed one, such as @{upstream} without an upstream, is 128.
	var gerr *git.Error
	if errors.As(err, &gerr) && gerr.ExitCode > 0 {
		return "", fmt.Errorf("%w: %q", errNoRevision, rev)
	}
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", rev, err)
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("%w: %q", errNoRevision, rev)
	}
	return sha, nil
}

// mergeBase returns the merge base of ref and head, and errNoRevision when ref
// does not resolve or shares no history with head: both mean "try the next
// candidate", not "this repository is broken".
func mergeBase(ctx context.Context, g *git.Runner, ref, head string) (string, error) {
	out, err := g.Run(ctx, nil, "merge-base", "--end-of-options", ref, head)
	var gerr *git.Error
	if errors.As(err, &gerr) && gerr.ExitCode > 0 {
		return "", fmt.Errorf("%w: no merge base of %q with %s", errNoRevision, ref, head)
	}
	if err != nil {
		return "", fmt.Errorf("merge base of %q with %s: %w", ref, head, err)
	}
	base, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if base == "" {
		return "", fmt.Errorf("%w: no merge base of %q with %s", errNoRevision, ref, head)
	}
	return base, nil
}

// originRepo names the repository the way GitHub does, owner/name, from the
// URL of its origin remote. It is what the bundle records when nothing else
// says, such as a local `aval verify`; naming the repository is not worth
// failing a verification over, so anything unexpected is simply "".
func originRepo(ctx context.Context, g *git.Runner) string {
	out, err := g.Run(ctx, nil, "config", "--get", "remote.origin.url")
	if err != nil {
		return ""
	}
	return repoFullName(string(out))
}

// repoFullName extracts owner/name from a git remote URL, either with a scheme
// (https://github.com/o/r.git, ssh://git@github.com/o/r) or in scp form
// (git@github.com:o/r.git). A local path names no GitHub repository, so it
// gives "".
func repoFullName(remote string) string {
	s := strings.TrimSpace(remote)
	switch {
	case strings.Contains(s, "://"):
		_, s, _ = strings.Cut(s, "://")
		if _, rest, ok := strings.Cut(s, "/"); ok {
			s = rest // drop the host
		} else {
			return ""
		}
	case strings.Contains(s, "@") && strings.Contains(s, ":"):
		_, s, _ = strings.Cut(s, ":")
	default:
		return ""
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(strings.Trim(s, "/"), ".git"), "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || name == "" {
		return ""
	}
	return owner + "/" + name
}

// quoted quotes each item, for an error that lists what it tried.
func quoted(items []string) []string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, strconv.Quote(s))
	}
	return out
}
