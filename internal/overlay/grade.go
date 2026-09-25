package overlay

import (
	"path"
	"slices"
	"strings"

	"github.com/svallejo-dev/aval/internal/evidence"
	"github.com/svallejo-dev/aval/internal/gotest"
)

// grade turns what the base's runs of t's tests report into t's evidence
// (ADR-0005 §2). A failure is only weak when something in t's package
// directories could have caused it by itself: files head adds or modifies
// that did not travel, or paths the base tree could not hold. A failed build
// is weak only when it is t's own test build or a package head adds, and
// none otherwise: a package the base already has, or a missing module.
func (w *Tree) grade(t Target, rep gotest.Report) Obligation {
	o := Obligation{ID: t.ID, Before: rep.Status(t.ID, t.Packages...), After: t.After}
	o.Strength = Strength(o.Before, o.After, t.Characterization)
	switch o.Strength {
	case evidence.Strong:
		var notes []string
		if files := w.uncopied(t.Packages); len(files) > 0 {
			notes = append(notes, "head-only files: "+strings.Join(files, ", "))
		}
		if files := w.inPackages(w.Skipped, t.Packages); len(files) > 0 {
			notes = append(notes, "paths the base tree cannot hold: "+strings.Join(files, ", "))
		}
		if len(notes) > 0 {
			o.Strength = evidence.Weak
			o.Note = "base failure may come from " + strings.Join(notes, "; and from ")
		}
	case evidence.Weak:
		for _, p := range rep.Packages {
			if p.Status != evidence.BuildFail || !slices.Contains(t.Packages, p.Name) || ownBuild(p) {
				continue
			}
			failed, _, _ := strings.Cut(p.FailedBuild, " [")
			if dir, ok := w.dir(failed); !ok || !w.added[dir] {
				o.Strength = evidence.None
				o.Note = "base build failed in " + failed + ": land new dependencies or helper packages in an earlier dx PR"
				break
			}
			o.Note = "base build failed in " + failed + ", a package head adds"
		}
	}
	return o
}

// ownBuild reports whether p failed to build itself or its tests: the
// package, its external _test package, or its test variant.
func ownBuild(p gotest.Package) bool {
	name, variant, ok := strings.Cut(p.FailedBuild, " [")
	if ok && variant != p.Name+".test]" {
		return false
	}
	return name == "" || name == p.Name || name == p.Name+"_test"
}

// uncopied returns the files that head adds or modifies under the
// directories of pkgs and that did not travel.
func (w *Tree) uncopied(pkgs []string) []string {
	return w.inPackages(w.others, pkgs)
}

// inPackages returns the files, repository-relative paths, that live under
// the directory of one of pkgs, a list of import paths.
func (w *Tree) inPackages(files, pkgs []string) []string {
	var dirs []string
	for _, pkg := range pkgs {
		if dir, ok := w.dir(pkg); ok {
			dirs = append(dirs, dir)
		}
	}
	var found []string
	for _, f := range files {
		if slices.ContainsFunc(dirs, func(dir string) bool { return dir == "" || strings.HasPrefix(f, dir+"/") }) {
			found = append(found, f)
		}
	}
	return found
}

// dir returns the repository-relative directory of pkg, an import path, or
// false when pkg is outside the module.
func (w *Tree) dir(pkg string) (string, bool) {
	if pkg == w.module {
		return w.root, true
	}
	rel, ok := strings.CutPrefix(pkg, w.module+"/")
	if !ok {
		return "", false
	}
	return path.Join(w.root, rel), true
}
