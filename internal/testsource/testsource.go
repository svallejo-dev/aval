// Package testsource reads Go test source, without compiling or running it,
// to find where obligation IDs are declared and to fingerprint the test code
// bound to each one, so the gate can tell when a bound test was edited,
// removed or skipped outside the delta of its obligation (ADR-0004).
//
// Scan finds two kinds of declaration in the top-level functions of _test.go
// files. A Subtest is a Run call with two arguments whose first one is a
// string literal starting with an obligation ID followed by a space, a tab or
// the end: t.Run("ORD-F01 title", func(t *testing.T) {…}), or s.Run in a
// suite. A TableEntry is an entry of a table that a range loop runs with
// t.Run(tt.<field>, …), or t.Run(<key>, …) over a map; its name is the string
// literal keyed by <field> (name, desc, title or any other), the first string
// literal of an unkeyed entry, or the map key. The table is the composite
// literal in the range clause, else the last one assigned to that identifier
// before the loop in the same function, else a package-level var of the same
// file. Not found: names built at run time, tables from helpers or other
// files, unkeyed entries whose name is not their first string, index loops
// (for i := range tests) and IDs bound with t.Attr.
//
// A fingerprint is the hex SHA-256 of the kind, the file's //go:build
// expression and the canonical encoding (see canon) of the bound code: for a
// Subtest the whole Run call, name and function literal included (a named
// function is not followed); for a TableEntry the entry and the body of the
// loop that runs it, so editing the shared runner changes every entry.
// Reformatting, commenting or moving that code keeps the fingerprint; changing
// any of its tokens, or excluding the file from the build, changes it. Helpers
// and setup outside the bound code are not included. Fingerprints are only
// comparable between scans by the same aval build.
//
// Declaration.Skips reports a Skip, Skipf or SkipNow call in the bound code,
// nested subtests included, or directly in a function around it outside its
// other subtests: a t.Skip at the top of a Test skips all its subtests. Which
// entries a skip in a table's loop affects is decided at run time, so it marks
// every entry of that table.
package testsource

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/svallejo-dev/aval/internal/obligation"
)

// Kind is how a test declares its obligation ID.
type Kind string

// Declaration kinds.
const (
	Subtest    Kind = "subtest"     // t.Run("ORD-F01 …", func(t *testing.T) {…})
	TableEntry Kind = "table_entry" // {name: "ORD-F01 …", …} run by t.Run(tt.name, …)
)

// Declaration is one place where a test binds itself to an obligation.
type Declaration struct {
	ID          obligation.ID
	File        string // slash-separated, relative to the scanned directory
	Test        string // enclosing top-level function: TestRefund, or (*Suite).TestRefund
	Kind        Kind
	Fingerprint string // hex SHA-256 of the bound code, see the package doc
	Line        int    // line of the string literal that carries the ID
	Skips       bool   // the bound code or a function around it calls Skip
}

// Scan parses every _test.go file under dir and returns the obligation IDs
// they declare, sorted by file and line. Like the go tool, it skips testdata
// and vendor directories and names starting with "." or "_". A file that does
// not parse is an error that names its file and line.
func Scan(dir string) ([]Declaration, error) {
	fsys := os.DirFS(dir)
	var decls []Declaration
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		hidden := strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
		if d.IsDir() {
			if path != "." && (hidden || name == "testdata" || name == "vendor") {
				return fs.SkipDir
			}
			return nil
		}
		if hidden || !strings.HasSuffix(name, "_test.go") {
			return nil
		}
		src, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		found, err := scanFile(path, src)
		decls = append(decls, found...)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	slices.SortStableFunc(decls, func(a, b Declaration) int {
		return cmp.Or(strings.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line))
	})
	return decls, nil
}

// fileScan collects the declarations of one file.
type fileScan struct {
	fset  *token.FileSet
	file  *ast.File
	build string // the //go:build expression, or ""
	decls []Declaration
}

func scanFile(path string, src []byte) ([]Declaration, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	s := &fileScan{fset: fset, file: f}
	for _, g := range f.Comments {
		for _, c := range g.List {
			if x, err := constraint.Parse(c.Text); err == nil && g.Pos() < f.Package {
				s.build = x.String()
			}
		}
	}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		test := fn.Name.Name
		if fn.Recv != nil && len(fn.Recv.List) == 1 {
			test = "(" + types.ExprString(fn.Recv.List[0].Type) + ")." + test
		}
		ast.PreorderStack(fn, nil, func(n ast.Node, stack []ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok && isRun(call) {
				s.run(call, stack, Declaration{File: path, Test: test})
			}
			return true
		})
	}
	return s.decls, nil
}

// run records the declarations bound by call, a Run with two arguments.
// stack holds the nodes from the top-level function down to call, and d the
// fields every declaration of the function shares.
func (s *fileScan) run(call *ast.CallExpr, stack []ast.Node, d Declaration) {
	if lit, ok := call.Args[0].(*ast.BasicLit); ok {
		d.Kind, d.Skips = Subtest, hasSkip(call.Args[1], false) || enclosingSkips(stack)
		s.add(d, lit, canon(call))
		return
	}
	var loopVar, field string
	switch arg := call.Args[0].(type) {
	case *ast.Ident:
		loopVar = arg.Name
	case *ast.SelectorExpr:
		x, ok := arg.X.(*ast.Ident)
		if !ok {
			return
		}
		loopVar, field = x.Name, arg.Sel.Name
	default:
		return
	}
	loop := rangeOver(stack, loopVar)
	if loop == nil {
		return
	}
	table := s.resolve(loop.X, stack[0], loop.Pos())
	if table == nil {
		return
	}
	byKey := field == "" && isIdent(loop.Key, loopVar)
	runner := canon(loop.Body)
	d.Kind, d.Skips = TableEntry, hasSkip(loop.Body, false) || enclosingSkips(stack)
	for _, elt := range table.Elts {
		entry, lit := entryName(elt, field, byKey)
		s.add(d, lit, runner, canon(entry))
	}
}

// add records d if lit starts with an obligation ID followed by a space, a
// tab or the end of the string: go test turns that blank into the "_" that
// obligation.FromTestSegment expects.
func (s *fileScan) add(d Declaration, lit *ast.BasicLit, parts ...[]byte) {
	if lit == nil || lit.Kind != token.STRING {
		return
	}
	name, err := strconv.Unquote(lit.Value)
	if err != nil {
		return
	}
	if i := strings.IndexAny(name, " \t"); i >= 0 {
		name = name[:i]
	}
	if d.ID, err = obligation.Parse(name); err != nil {
		return
	}
	d.Line = s.fset.Position(lit.Pos()).Line
	d.Fingerprint = fingerprint(d.Kind, s.build, parts...)
	s.decls = append(s.decls, d)
}

// rangeOver returns the innermost range loop in stack that declares name as
// its key or value.
func rangeOver(stack []ast.Node, name string) *ast.RangeStmt {
	for i := len(stack) - 1; i >= 0; i-- {
		if r, ok := stack[i].(*ast.RangeStmt); ok && (isIdent(r.Key, name) || isIdent(r.Value, name)) {
			return r
		}
	}
	return nil
}

// resolve returns the composite literal a range clause iterates over: x
// itself, the last one assigned to x before the loop in fn, or the one of a
// package-level var x.
func (s *fileScan) resolve(x ast.Expr, fn ast.Node, loop token.Pos) *ast.CompositeLit {
	switch x := x.(type) {
	case *ast.CompositeLit:
		return x
	case *ast.Ident:
		if lit := lastDef(fn, x.Name, loop); lit != nil {
			return lit
		}
		for _, d := range s.file.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				if lit := lastDef(gd, x.Name, gd.End()); lit != nil {
					return lit
				}
			}
		}
	}
	return nil
}

// lastDef returns the composite literal of the last definition of name in
// root before pos, or nil if that definition is not a composite literal.
func lastDef(root ast.Node, name string, pos token.Pos) *ast.CompositeLit {
	var lit *ast.CompositeLit
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil || n.Pos() >= pos {
			return false
		}
		switch n := n.(type) {
		case *ast.AssignStmt:
			for i, l := range n.Lhs {
				if isIdent(l, name) && len(n.Rhs) == len(n.Lhs) {
					lit, _ = n.Rhs[i].(*ast.CompositeLit)
				}
			}
		case *ast.ValueSpec:
			for i, id := range n.Names {
				if id.Name == name {
					lit = nil
					if i < len(n.Values) {
						lit, _ = n.Values[i].(*ast.CompositeLit)
					}
				}
			}
		}
		return true
	})
	return lit
}

// entryName returns the node that defines one table entry and the string
// literal that names it, which is nil when the entry has no literal name.
func entryName(elt ast.Expr, field string, byKey bool) (ast.Node, *ast.BasicLit) {
	kv, isMap := elt.(*ast.KeyValueExpr)
	switch {
	case isMap && byKey:
		lit, _ := kv.Key.(*ast.BasicLit)
		return kv, lit
	case isMap:
		elt = kv.Value
	}
	if e, ok := elt.(*ast.CompositeLit); ok && field != "" {
		for _, x := range e.Elts {
			if kv, ok := x.(*ast.KeyValueExpr); ok && isIdent(kv.Key, field) {
				lit, _ := kv.Value.(*ast.BasicLit)
				return elt, lit
			} else if lit, ok := x.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				return elt, lit // unkeyed: the first string literal
			}
		}
	}
	return elt, nil
}

// enclosingSkips reports whether a function around the current node calls
// Skip outside its subtests, which skips everything that function runs.
func enclosingSkips(stack []ast.Node) bool {
	return slices.ContainsFunc(stack, func(n ast.Node) bool {
		switch n.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			return hasSkip(n, true)
		}
		return false
	})
}

// hasSkip reports whether n calls Skip, Skipf or SkipNow. With ownOnly it does
// not look into subtests started with Run: their skips only skip themselves.
func hasSkip(n ast.Node, ownOnly bool) bool {
	found := false
	ast.Inspect(n, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		switch {
		case found || !ok:
			return !found
		case isMethod(call, "Skip", "Skipf", "SkipNow"):
			found = true
		}
		return !found && (!ownOnly || !isRun(call))
	})
	return found
}

func isRun(call *ast.CallExpr) bool { return len(call.Args) == 2 && isMethod(call, "Run") }

func isMethod(call *ast.CallExpr, names ...string) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && slices.Contains(names, sel.Sel.Name)
}

func isIdent(x ast.Expr, name string) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == name
}
