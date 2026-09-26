package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/svallejo-dev/aval/internal/platform/git"
)

// rangeFlags are the flags `aval verify` and `aval gate` share: which
// repository to work in and which commits to judge (ADR-0005 §1).
//
// There are two bases and they are not interchangeable, so there are two flags.
// Neither is allowed inside GitHub Actions on a pull request event, where both
// come from the event.
type rangeFlags struct {
	trustBase  string
	changeBase string
	head       string
	dir        string
}

// bind declares the flags on cmd.
func (f *rangeFlags) bind(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.trustBase, "trust-base", "",
		"tip of the default branch, where the policy, CODEOWNERS, the baseline and the lint configuration come from (default: origin/HEAD's target, origin/main or main)")
	fl.StringVar(&f.changeBase, "change-base", "",
		"where the range starts: what the pull request did is measured from here (default: the merge base of the head with the trust base)")
	fl.StringVar(&f.head, "head", "", "head of the range, the commit being judged (default: HEAD)")
	fl.StringVar(&f.dir, "dir", "", "any directory of the repository (default: the working directory); aval works from its root")
}

// rangeSet reports whether the invocation named any part of the range itself.
func (f *rangeFlags) rangeSet() []string {
	var set []string
	for _, p := range []struct{ name, value string }{
		{"--trust-base", f.trustBase}, {"--change-base", f.changeBase}, {"--head", f.head},
	} {
		if p.value != "" {
			set = append(set, p.name)
		}
	}
	return set
}

// commitRange is the two bases and the head, as full SHAs.
type commitRange struct {
	// trust is the tip of the repository's default branch. Everything that is
	// policy or trust comes from it, and nobody who opens a pull request gets
	// to choose it.
	trust string
	// change is the merge base of head with the branch the pull request
	// targets. Everything about what the pull request did is measured from it,
	// and whoever cut the branch chose it, so nothing read from it is trusted.
	change string
	// ref names where change came from: the branch the pull request targets, or
	// the revision that resolved to it. It is a label for the bundle, and
	// nothing is decided on it.
	ref  string
	head string
}

// fallbackTrustRefs close the list of revisions the trust base is taken from,
// for a repository whose remote says nothing and for one with no remote at all.
//
// The branch under judgement is deliberately not among them, and neither is
// @{upstream}, which can be that very branch: a range measured against itself
// is empty, and an empty range satisfies every rule (ADR-0005 §1).
var fallbackTrustRefs = []string{"origin/main", "main"}

// errNoRevision marks a revision this repository does not have, or that shares
// no history with the head. It is internal: the resolvers either try the next
// candidate or turn it into a usage error that names every one they tried.
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

// resolveRange resolves the range aval judges.
//
// The head is --head, or HEAD. The trust base is --trust-base, or the first of
// trustRefs that resolves. The change base is --change-base, or the merge base
// of the head with the first of prBaseRefs that yields one — the branch the
// pull request targets, which outside Actions is the trust base itself.
//
// Nothing here gives up quietly. A range aval cannot measure is not a pass: a
// change base that does not resolve, or that is the head itself, is an invalid
// invocation, and the error names every candidate it tried.
func resolveRange(ctx context.Context, g *git.Runner, f rangeFlags, trustRefs, prBaseRefs []string) (commitRange, error) {
	head, err := revision(ctx, g, cmp.Or(f.head, "HEAD"))
	switch {
	case errors.Is(err, errNoRevision) && f.head == "":
		return commitRange{}, usageError(errors.New("HEAD names no commit: this repository has no commits yet, so there is nothing to judge"))
	case errors.Is(err, errNoRevision):
		return commitRange{}, usageError(fmt.Errorf("--head %q names no commit of this repository", f.head))
	case err != nil:
		return commitRange{}, err
	}

	trust, trustRef, err := resolveTrust(ctx, g, f.trustBase, trustRefs)
	if err != nil {
		return commitRange{}, err
	}
	// Outside Actions nothing says which branch the pull request targets, so
	// the trust base stands in for it: locally the two are the same branch
	// almost always, and where they are not, --change-base says so.
	if len(prBaseRefs) == 0 {
		prBaseRefs = []string{trustRef}
	}
	change, ref, err := resolveChange(ctx, g, f.changeBase, prBaseRefs, head)
	if err != nil {
		return commitRange{}, err
	}
	return commitRange{trust: trust, change: change, ref: ref, head: head}, nil
}

// resolveTrust resolves the trust base: the flag, or the first candidate this
// repository has.
//
// No relation to the head is required or checked. A stacked pull request targets
// a branch of its own and its head may be far from the default branch, which is
// legitimate: merging into a branch that is not the default one cannot reach the
// default branch without another pull request, and this same gate judges that
// one.
func resolveTrust(ctx context.Context, g *git.Runner, flag string, refs []string) (sha, ref string, err error) {
	if flag != "" {
		sha, err := revision(ctx, g, flag)
		if errors.Is(err, errNoRevision) {
			return "", "", usageError(fmt.Errorf("--trust-base %q names no commit of this repository", flag))
		}
		return sha, flag, err
	}
	var tried []string
	for _, candidate := range refs {
		sha, err := revision(ctx, g, candidate)
		if err == nil {
			return sha, candidate, nil
		}
		if !errors.Is(err, errNoRevision) {
			return "", "", err
		}
		tried = append(tried, strconv.Quote(candidate))
	}
	return "", "", usageError(fmt.Errorf("cannot find the tip of the default branch, and without it there is no policy to judge against: "+
		"none of %s names a commit here; fetch the default branch or pass --trust-base", strings.Join(tried, ", ")))
}

// resolveChange resolves the change base: the flag, or the merge base of the
// head with the first candidate that yields one.
//
// A merge base equal to the head is refused. It means the head is already
// contained in that branch, so the range is empty and every rule would be
// satisfied by there being nothing to judge — the quietest way there is for a
// gate to pass something (ADR-0005 §1).
func resolveChange(ctx context.Context, g *git.Runner, flag string, refs []string, head string) (sha, ref string, err error) {
	if flag != "" {
		sha, err := revision(ctx, g, flag)
		if errors.Is(err, errNoRevision) {
			return "", "", usageError(fmt.Errorf("--change-base %q names no commit of this repository", flag))
		}
		if err != nil {
			return "", "", err
		}
		if sha == head {
			return "", "", usageError(fmt.Errorf("--change-base %q is the head commit: the range is empty, and an empty range is not a pass", flag))
		}
		return sha, flag, nil
	}
	var tried []string
	for _, candidate := range refs {
		sha, err := mergeBase(ctx, g, candidate, head)
		switch {
		case errors.Is(err, errNoRevision):
			tried = append(tried, strconv.Quote(candidate)+" (no merge base with the head)")
			continue
		case err != nil:
			return "", "", err
		case sha == head:
			tried = append(tried, strconv.Quote(candidate)+" (the head is already on it, so the range would be empty)")
			continue
		}
		return sha, candidate, nil
	}
	return "", "", usageError(fmt.Errorf("cannot measure what this range changed, and an empty range is not a pass: tried %s; "+
		"put the work on a branch of its own, or pass --change-base", strings.Join(tried, ", ")))
}

// trustRefs returns the revisions to take the trust base from, most
// authoritative first: the default branch GitHub named, then what origin/HEAD
// points at, which a clone sets from the remote's own default branch, then the
// fallbacks.
func trustRefs(ctx context.Context, g *git.Runner, defaultBranch string) []string {
	refs := make([]string, 0, 5)
	add := func(ref string) {
		if ref != "" && !slices.Contains(refs, ref) {
			refs = append(refs, ref)
		}
	}
	if defaultBranch != "" {
		add("refs/remotes/origin/" + defaultBranch)
		add("refs/heads/" + defaultBranch)
	}
	if target, err := originHead(ctx, g); err == nil {
		add(target)
	}
	for _, ref := range fallbackTrustRefs {
		add(ref)
	}
	return refs
}

// originHead returns the ref refs/remotes/origin/HEAD points at, empty when
// there is no such ref.
func originHead(ctx context.Context, g *git.Runner) (string, error) {
	out, err := g.Run(ctx, nil, "symbolic-ref", "--quiet", "refs/remotes/origin/HEAD")
	var gerr *git.Error
	if errors.As(err, &gerr) && gerr.ExitCode > 0 {
		return "", nil // --quiet: exit 1 and no message when the ref is not there
	}
	if err != nil {
		return "", fmt.Errorf("read refs/remotes/origin/HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// revision resolves rev to the full SHA of the commit it names, and
// errNoRevision when this repository does not have it.
func revision(ctx context.Context, g *git.Runner, rev string) (string, error) {
	if rev == "" || strings.HasPrefix(rev, "-") {
		return "", fmt.Errorf("%w: %q is not a revision", errNoRevision, rev)
	}
	out, err := g.Run(ctx, nil, "rev-parse", "--verify", "--quiet", "--end-of-options", rev+"^{commit}")
	// --quiet leaves exit 1 and no message for a revision that is not there;
	// a malformed one is 128.
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

// fetchCommit asks origin for one commit by SHA, for when the checkout did not
// bring it: a pull request from a fork has its base branch tip in the upstream
// repository, which fetch-depth: 0 on the fork's head does not fetch. It is the
// only network call aval makes with git, and it happens in phase 1, before any
// of head's code runs (ADR-0005 §1).
func fetchCommit(ctx context.Context, g *git.Runner, sha string) error {
	_, err := g.Run(ctx, nil, "fetch", "--no-tags", "--no-write-fetch-head",
		"--no-recurse-submodules", "--quiet", "--end-of-options", "origin", sha)
	if err != nil {
		return fmt.Errorf("fetch %s from origin: %w", sha, err)
	}
	return nil
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
