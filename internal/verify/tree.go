package verify

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/svallejo-dev/aval/internal/platform/git"
)

const (
	// maxTreeFile caps one file of the extracted tree. Larger is an error,
	// never silently truncated.
	maxTreeFile = 64 << 20
	// maxBatchBytes caps how much of the tree aval holds in memory at once:
	// the files come out of git in batches of about this size.
	maxBatchBytes = 32 << 20
)

// extractTree writes the tree of commit into dir, read with git ls-tree and
// git cat-file, which answer from the object database alone.
//
// Neither consults .gitattributes, and that is the point: git archive does,
// so one line in the base's .gitattributes ("aval.yaml export-ignore") would
// drop the policy out of the extraction and leave the gate observing for ever
// with an empty baseline and no specs, while export-subst would rewrite the
// contents of what is left. --attr-source does not stop either, because
// export-ignore is read from the tree being archived.
//
// Only regular blobs are written: aval reads no symlink, device or submodule
// from a tree, and a link is how an extraction escapes its directory.
func extractTree(ctx context.Context, g *git.Runner, commit, dir string) error {
	blobs, err := lsTree(ctx, g, commit)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	defer root.Close()
	for len(blobs) > 0 {
		var batch []blob
		batch, blobs = nextBatch(blobs)
		var stdin strings.Builder
		for _, b := range batch {
			stdin.WriteString(b.oid)
			stdin.WriteByte('\n')
		}
		out, err := g.Run(ctx, []byte(stdin.String()), "cat-file", "--batch")
		if err != nil {
			return fmt.Errorf("verify: %w", err)
		}
		if err := writeBatch(root, batch, out); err != nil {
			return fmt.Errorf("verify: extract %s: %w", commit, err)
		}
	}
	return nil
}

// blob is one regular file of a tree.
type blob struct {
	oid  string
	name string // slash-separated, relative to the repository root
	size int64
}

// lsTree lists the regular files of commit with their object IDs and sizes.
func lsTree(ctx context.Context, g *git.Runner, commit string) ([]blob, error) {
	out, err := g.Run(ctx, nil, "ls-tree", "-r", "-l", "-z", "--full-tree", "--end-of-options", commit)
	if err != nil {
		return nil, fmt.Errorf("verify: %w", err)
	}
	var blobs []blob
	for rec := range strings.SplitSeq(string(out), "\x00") {
		if rec == "" {
			continue
		}
		meta, name, ok := strings.Cut(rec, "\t")
		f := strings.Fields(meta)
		if !ok || len(f) != 4 {
			return nil, fmt.Errorf("verify: unexpected git ls-tree record %q", rec)
		}
		if f[1] != "blob" || f[0] == "120000" { // a submodule or a symlink
			continue
		}
		size, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil || size < 0 || size > maxTreeFile {
			return nil, fmt.Errorf("verify: %s at %s: size %q is not usable, or is more than %d bytes", name, commit, f[3], maxTreeFile)
		}
		blobs = append(blobs, blob{oid: f[2], name: name, size: size})
	}
	return blobs, nil
}

// nextBatch splits off the blobs of one git cat-file call: as many as fit in
// maxBatchBytes, and always at least one.
func nextBatch(blobs []blob) (batch, rest []blob) {
	n, total := 0, int64(0)
	for n < len(blobs) && (n == 0 || total+blobs[n].size <= maxBatchBytes) {
		total += blobs[n].size
		n++
	}
	return blobs[:n], blobs[n:]
}

// writeBatch writes one batch of blobs. git cat-file --batch answers every
// object it was given, in order, with a header line, the contents and a
// newline.
func writeBatch(root *os.Root, batch []blob, out []byte) error {
	for _, b := range batch {
		header, rest, ok := bytes.Cut(out, []byte("\n"))
		if !ok {
			return fmt.Errorf("git cat-file stopped before %s", b.name)
		}
		f := strings.Fields(string(header))
		if len(f) != 3 || f[0] != b.oid || f[1] != "blob" || f[2] != strconv.FormatInt(b.size, 10) {
			return fmt.Errorf("git cat-file answered %q for %s", header, b.name)
		}
		if int64(len(rest)) < b.size+1 {
			return fmt.Errorf("git cat-file cut %s short", b.name)
		}
		if err := writeFile(root, b.name, rest[:b.size]); err != nil {
			return err
		}
		out = rest[b.size+1:] // the newline git writes after the contents
	}
	return nil
}

// writeFile writes data to name under root, creating the directories above it.
func writeFile(root *os.Root, name string, data []byte) error {
	name = path.Clean("/" + name)[1:]
	if name == "" || name == "." {
		return fmt.Errorf("git named a file %q", name)
	}
	if dir := path.Dir(name); dir != "." {
		if err := root.MkdirAll(filepath.FromSlash(dir), 0o750); err != nil {
			return fmt.Errorf("extract %s: %w", name, err)
		}
	}
	f, err := root.Create(filepath.FromSlash(name))
	if err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}
	return nil
}
