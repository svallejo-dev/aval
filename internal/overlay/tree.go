package overlay

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The file modes git writes in a tree, other than 100644 for a plain file.
const (
	execMode    = "100755"
	symlinkMode = "120000"
	gitlinkMode = "160000" // a submodule's commit
)

const (
	// blobBudget bounds how much blob content one git cat-file --batch holds
	// in memory. A blob larger than it is streamed straight into its file.
	blobBudget = 8 << 20
	// blobBatch bounds how many entries one git cat-file --batch asks for, so
	// that a tree of many tiny blobs does not grow its input and output
	// without bound either.
	blobBatch = 4096
	// hexSHA256 is the length of an object ID in a SHA-256 repository.
	hexSHA256 = 64
)

// noConversion tells the git commands initRepo runs to convert no line
// ending. None of them writes a file of the tree, so today it changes
// nothing: what keeps every file byte for byte is that materialize writes
// the blobs itself. It keeps a command added later from converting one.
var noConversion = []string{"-c", "core.autocrlf=false", "-c", "core.eol=lf"}

// treeEntry is one entry of a git tree, as ls-tree -l reports it.
type treeEntry struct {
	size int64  // the blob's size; -1 when the entry is not a blob
	mode string // "100644", or execMode, symlinkMode or gitlinkMode
	oid  string // the object the entry names
	path string // repository-relative, with "/" separators
}

// entries lists every blob, symlink and gitlink of commit's tree, from the
// repository's root whatever directory r reads it from.
func (r repo) entries(ctx context.Context, commit string) ([]treeEntry, error) {
	out, err := r.Run(ctx, nil, "ls-tree", "-r", "-z", "-l", "--full-tree", "--end-of-options", commit)
	if err != nil {
		return nil, fmt.Errorf("overlay: %w", err)
	}
	return parseEntries(out)
}

// parseEntries reads ls-tree -r -z -l output: "<mode> <type> <oid> <size>",
// a tab, the path, and a NUL after every entry.
func parseEntries(out []byte) ([]treeEntry, error) {
	var entries []treeEntry
	for record := range strings.SplitSeq(strings.TrimSuffix(string(out), "\x00"), "\x00") {
		if record == "" {
			continue
		}
		meta, name, ok := strings.Cut(record, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 4 {
			return nil, fmt.Errorf("overlay: unexpected git ls-tree entry %q", record)
		}
		e := treeEntry{size: -1, mode: fields[0], oid: fields[2], path: name}
		if fields[1] == "blob" {
			size, err := strconv.ParseInt(fields[3], 10, 64)
			if err != nil {
				return nil, fmt.Errorf("overlay: unexpected size in git ls-tree entry %q", record)
			}
			e.size = size
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// merge returns the entries of the tree to materialize: base's, without the
// paths head deleted and without the ones head's own entries replace, then
// head's entries for the files that travel.
//
// What keeps a rename that only changes case from losing the file head added
// is that the base's entry is dropped, not the order of the two loops: on a
// case-insensitive file system the two names are one file, and only head's
// content is ever written to it.
func merge(base, head []treeEntry, copied, removed []string) ([]treeEntry, error) {
	drop := make(map[string]bool, len(removed)+len(copied))
	for _, name := range removed {
		drop[name] = true
	}
	want := make(map[string]bool, len(copied))
	for _, name := range copied {
		drop[name], want[name] = true, true
		// Head may have replaced a file with a directory that holds one.
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			drop[dir] = true
		}
	}
	files := make([]treeEntry, 0, len(base)+len(copied))
	for _, e := range base {
		if !drop[e.path] {
			files = append(files, e)
		}
	}
	for _, e := range head {
		if want[e.path] {
			files = append(files, e)
			delete(want, e.path)
		}
	}
	if len(want) > 0 {
		missing := slices.Sorted(maps.Keys(want))
		return nil, fmt.Errorf("overlay: %s is not in head's tree", missing[0])
	}
	return files, nil
}

// materialize writes files into dir from the git objects they name, never
// through git's checkout, which a change's own .git/config or
// .git/info/attributes could filter. Every blob lands byte for byte, with
// its executable bit, and every symlink whose target stays inside the tree
// becomes a symlink. It returns the paths it left out, sorted: submodule
// gitlinks, and symlinks that point out of the tree.
func (r repo) materialize(ctx context.Context, dir string, files []treeEntry) ([]string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("overlay: %w", err)
	}
	defer root.Close()
	var skipped []string
	batch, budget := make([]treeEntry, 0, 64), int64(0)
	for _, e := range files {
		if e.mode == gitlinkMode {
			skipped = append(skipped, e.path)
			continue
		}
		if e.size < 0 {
			return nil, fmt.Errorf("overlay: %s is not a file in the tree (mode %s)", e.path, e.mode)
		}
		if err := mkdirAll(root, e.path); err != nil {
			return nil, err
		}
		if e.size > blobBudget {
			if err := r.stream(ctx, root, e); err != nil {
				return nil, err
			}
			continue
		}
		if budget += e.size; budget > blobBudget || len(batch) == blobBatch {
			if err := r.write(ctx, root, batch, &skipped); err != nil {
				return nil, err
			}
			batch, budget = batch[:0], e.size
		}
		batch = append(batch, e)
	}
	if err := r.write(ctx, root, batch, &skipped); err != nil {
		return nil, err
	}
	slices.Sort(skipped)
	return skipped, nil
}

// write reads the contents of batch in one git cat-file --batch and writes
// each entry, appending to skipped the symlinks that point out of the tree.
func (r repo) write(ctx context.Context, root *os.Root, batch []treeEntry, skipped *[]string) error {
	if len(batch) == 0 {
		return nil
	}
	list := make([]byte, 0, len(batch)*(len(batch[0].oid)+1))
	for _, e := range batch {
		list = append(append(list, e.oid...), '\n')
	}
	out, err := r.Run(ctx, list, "cat-file", "--batch")
	if err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	for _, e := range batch {
		content, rest, err := nextBlob(out, e)
		if err != nil {
			return err
		}
		out = rest
		switch {
		case e.mode == symlinkMode && !insideTree(e.path, string(content)):
			*skipped = append(*skipped, e.path)
		case e.mode == symlinkMode:
			if err := root.Symlink(filepath.FromSlash(string(content)), filepath.FromSlash(e.path)); err != nil {
				return fmt.Errorf("overlay: %w", err)
			}
		default:
			if err := root.WriteFile(filepath.FromSlash(e.path), content, perm(e.mode)); err != nil {
				return fmt.Errorf("overlay: %w", err)
			}
		}
	}
	if len(out) > 0 {
		return fmt.Errorf("overlay: git cat-file --batch left %d bytes unread", len(out))
	}
	return nil
}

// nextBlob splits off the content git cat-file --batch wrote for e, which
// announces every object with "<oid> <type> <size>" and follows its content
// with a newline.
func nextBlob(out []byte, e treeEntry) (content, rest []byte, err error) {
	header, body, ok := bytes.Cut(out, []byte("\n"))
	want := e.oid + " blob " + strconv.FormatInt(e.size, 10)
	if !ok || string(header) != want || int64(len(body)) < e.size+1 || body[e.size] != '\n' {
		return nil, nil, fmt.Errorf("overlay: git cat-file answered %q for %s, want %q", header, e.path, want)
	}
	return body[:e.size], body[e.size+1:], nil
}

// stream writes one blob too large to hold in memory straight into its file.
func (r repo) stream(ctx context.Context, root *os.Root, e treeEntry) error {
	f, err := root.OpenFile(filepath.FromSlash(e.path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm(e.mode))
	if err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	defer f.Close()
	if err := r.Stream(ctx, f, "cat-file", "blob", "--end-of-options", e.oid); err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	return nil
}

// mkdirAll creates the directories name needs, relative to root.
func mkdirAll(root *os.Root, name string) error {
	dir := path.Dir(name)
	if dir == "." {
		return nil
	}
	if err := root.MkdirAll(filepath.FromSlash(dir), dirPerm); err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	return nil
}

// The modes a checkout writes, git's 0666 and 0777 less the umask. A test at
// the base may assert the mode it reads, or need a file a stricter mode of
// ours would hide from it, and would then fail there and pass at head.
const (
	dirPerm  fs.FileMode = 0o755
	filePerm fs.FileMode = 0o644
	execPerm fs.FileMode = 0o755
)

// perm is the permission a tree mode asks for.
func perm(mode string) fs.FileMode {
	if mode == execMode {
		return execPerm
	}
	return filePerm
}

// insideTree reports whether the symlink at link, a repository-relative
// path, points to a target inside the tree. A target that leaves it, or an
// absolute one, would let a test at the base read or write outside the
// temporary directory.
func insideTree(link, target string) bool {
	if target == "" || path.IsAbs(target) || filepath.IsAbs(target) || strings.ContainsAny(target, "\x00\\") {
		return false
	}
	resolved := path.Join(path.Dir(link), target)
	return resolved != ".." && !strings.HasPrefix(resolved, "../")
}

// objectStore returns the absolute path of the repository's object store,
// which the materialized tree borrows instead of copying objects.
func (r repo) objectStore(ctx context.Context) (string, error) {
	out, err := r.Run(ctx, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("overlay: %w", err)
	}
	common := strings.TrimSuffix(string(out), "\n")
	// objects/info/alternates holds one path per line, and a line that opens
	// with a quote is a C-quoted string.
	if !filepath.IsAbs(common) || strings.ContainsAny(common, "\n\"") {
		return "", fmt.Errorf("overlay: git reported the common directory as %q", common)
	}
	return filepath.Join(common, "objects"), nil
}

// initRepo makes the materialized tree at dir a repository of its own, so
// that a test at the base that shells out to git finds one: HEAD is the base
// commit, the index holds its tree, and every object comes from the caller's
// store, at objects, through objects/info/alternates. So a test that reads
// the base's history passes there as it does at head, instead of failing for
// want of a repository and forging fail-before evidence.
//
// Nothing here writes a file of the tree: git checks nothing out, and no
// filter can rewrite what materialize wrote. The tree is a plain repository,
// not a worktree of the caller's, so it shares no index, no refs and no
// config with it; what it borrows it only reads.
//
// git init runs in the caller's repository, r, where the hardened runner can
// resolve the empty tree it reads attributes from. template is an empty
// directory, so no init template of the developer's installs hooks in the
// tree.
func (r repo) initRepo(ctx context.Context, dir, template, base, objects string) error {
	format := "sha1"
	if len(base) == hexSHA256 {
		format = "sha256"
	}
	init := append(slices.Clone(noConversion), "init", "--quiet",
		"--template="+template, "--object-format="+format, "--end-of-options", dir)
	if _, err := r.Run(ctx, nil, init...); err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	alternates := filepath.Join(dir, ".git", "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte(objects+"\n"), 0o600); err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	tree := newRepo(dir)
	for _, args := range [][]string{
		{"update-ref", "--no-deref", "--end-of-options", "HEAD", base}, // detached at the base
		{"read-tree", "--end-of-options", base},                        // so git status sees only the overlay
	} {
		if _, err := tree.Run(ctx, nil, append(slices.Clone(noConversion), args...)...); err != nil {
			return fmt.Errorf("overlay: %w", err)
		}
	}
	return nil
}
