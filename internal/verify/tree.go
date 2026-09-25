package verify

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"

	"github.com/svallejo-dev/aval/internal/platform/git"
)

// maxTreeFile caps one file of the extracted tree. Larger is an error, never
// silently truncated.
const maxTreeFile = 64 << 20

// extractTree writes the whole tree of commit into dir, from git objects
// alone, so that every input aval takes from the base commit — the policy,
// the baseline, the specs, the test declarations and the lint configuration —
// is read before any of head's code runs (ADR-0005 §1). The hardened Runner's
// --attr-source keeps a .gitattributes in the repository from filtering,
// substituting or dropping what comes out.
//
// Only directories and regular files are written: aval reads no symlink,
// device or hard link from a tree, and a link is how an extraction escapes
// its directory.
func extractTree(ctx context.Context, g *git.Runner, commit, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("verify: %w", err)
	}
	pr, pw := io.Pipe()
	var streamErr error
	done := make(chan struct{})
	go func() {
		defer close(done)
		streamErr = g.Stream(ctx, pw, "archive", "--format=tar", "--end-of-options", commit)
		_ = pw.CloseWithError(streamErr) // a PipeWriter's Close never fails
	}()
	err := untar(pr, dir)
	if err == nil {
		// The tar reader stops at the end-of-archive blocks; git still has
		// the padding of the last block to write, and a closed pipe would
		// fail it.
		_, err = io.Copy(io.Discard, pr)
	}
	_ = pr.CloseWithError(err) // lets git finish, or stop, once untar is done
	<-done
	switch {
	case err != nil:
		return fmt.Errorf("verify: extract %s: %w", commit, err)
	case streamErr != nil:
		return fmt.Errorf("verify: extract %s: %w", commit, streamErr)
	}
	return nil
}

// untar writes the regular files and directories of a tar stream under dir,
// through an os.Root, so no entry can write outside it.
func untar(r io.Reader, dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open %s: %w", dir, err)
	}
	defer root.Close()
	tr := tar.NewReader(bufio.NewReader(r))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar: %w", err)
		}
		name := path.Clean("/" + h.Name)[1:]
		switch {
		case name == "" || name == ".":
		case h.Typeflag == tar.TypeDir:
			err = root.MkdirAll(filepath.FromSlash(name), 0o750)
		case h.Typeflag != tar.TypeReg:
		case h.Size > maxTreeFile:
			err = fmt.Errorf("%s is larger than %d bytes", h.Name, maxTreeFile)
		default:
			err = writeFile(root, name, tr)
		}
		if err != nil {
			return err
		}
	}
}

// writeFile writes at most maxTreeFile bytes of r to name under root.
func writeFile(root *os.Root, name string, r io.Reader) error {
	if dir := path.Dir(name); dir != "." {
		if err := root.MkdirAll(filepath.FromSlash(dir), 0o750); err != nil {
			return fmt.Errorf("extract %s: %w", name, err)
		}
	}
	f, err := root.Create(filepath.FromSlash(name))
	if err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}
	_, err = io.Copy(f, io.LimitReader(r, maxTreeFile))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}
	return nil
}
