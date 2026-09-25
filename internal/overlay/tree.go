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

// blobBudget bounds how much blob content one git cat-file --batch holds in
// memory. A blob larger than it is streamed straight into its file.
const blobBudget = 8 << 20

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
// head's entries for the files that travel. Leaving the base's out before
// adding head's keeps a rename that only changes case, on a
// case-insensitive file system, from losing the file head added.
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
		if budget += e.size; budget > blobBudget {
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
	if err := root.MkdirAll(filepath.FromSlash(dir), 0o750); err != nil {
		return fmt.Errorf("overlay: %w", err)
	}
	return nil
}

// perm is the permission a tree mode asks for. Only aval and the tests it
// runs read the temporary tree, so nothing outside the user needs access.
func perm(mode string) fs.FileMode {
	if mode == execMode {
		return 0o700
	}
	return 0o600
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
