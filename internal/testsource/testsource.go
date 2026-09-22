// Package testsource reads Go test source, without compiling or running it,
// to find where obligation IDs are declared and to fingerprint everything the
// bound tests depend on, so the gate can tell when a bound test was edited,
// removed, disabled or skipped, or its test data rewritten, outside the delta
// of its obligation (ADR-0004).
//
// # Declarations
//
// Scan finds two kinds of declaration in the top-level functions of _test.go
// files. A Subtest is a Run call with two arguments whose first one is a
// string literal starting with an obligation ID followed by a space, a tab or
// the end: t.Run("ORD-F01 title", …), or s.Run in a suite. A TableEntry is an
// entry of a table that a range loop runs with t.Run(tt.<field>, …), or
// t.Run(<key>, …) over a map; its name is the string literal keyed by <field>
// (name, desc, title or any other), the first string literal of an unkeyed
// entry, or the map key. The table is the composite literal in the range
// clause, else the last one assigned to that identifier before the loop in the
// same function, else a package-level var of the package's test files. Not
// found: names built at run time, tables returned by helpers, unkeyed entries
// whose name is not their first string, index loops (for i := range tests)
// and IDs bound with t.Attr.
//
// # Fingerprints
//
// A fingerprint is the hex SHA-256 of, in order:
//
//   - the declaration kind, the package clause, the file's //go:build
//     expression and its GOOS/GOARCH file name suffix (x_plan9_test.go);
//   - the enclosing chain: the top-level function and every function literal
//     around the declaration, with each statement that only calls Run left
//     out, so adding a sibling subtest changes nothing while an early return,
//     a shadowing assignment, a testing.Short guard or a wrapping if false
//     changes every declaration below it;
//   - the bound code: for a Subtest the whole Run call, for a TableEntry the
//     entry and the body of the loop that runs it;
//   - the helpers: every top-level declaration of the package's _test.go files
//     that the code above names, transitively (functions, vars, consts, types
//     with their methods; Test, Benchmark, Fuzz and Example functions are never
//     helpers), plus its TestMain and init functions;
//   - the imports those files use for the names the code above qualifies, and
//     their blank and dot imports.
//
// Code is hashed through a canonical encoding of its syntax tree (see encoder):
// reformatting, commenting or moving it keeps the fingerprint, changing any
// token changes it. A table's entries are left out of every encoding but
// their own, so adding an entry changes no other fingerprint. Helpers are
// matched by name without type information: a local variable that shadows a
// helper's name still pulls the helper in, and an unnamed import is matched
// by a name guessed from its path. Production code is not included: changing
// it is what a change is for. Fingerprints are only comparable between scans
// by the same aval build.
//
// # Test data
//
// Test data is not part of the fingerprint, since any file may be read by any
// test of the package and adding one must not flag the others. Instead,
// Declaration.Testdata holds the content hash of every file in the package's
// testdata tree, and Compare reports a file that existed at base and was
// modified or removed at head.
//
// # Gaps left to the runtime checks
//
// Three dependencies are knowingly left out; the gate's runtime checks (M2)
// cover them: helpers in other packages (only the import path is hashed),
// unnamed imports whose package name is not the one their path suggests, and
// data files outside testdata.
//
// # Skips
//
// Declaration.Skips reports a Skip, Skipf or SkipNow call on a *testing.T, B
// or F or testing.TB parameter, in the bound code (a named function passed to
// Run included) or directly in a function around it outside its other
// subtests. Parameters are looked up by name; skips inside helpers are not
// reported as such, but a new helper call changes the fingerprint. Which
// entries a skip in a table's loop affects is decided at run time, so it marks
// every entry of that table.
package testsource

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"io/fs"
	"os"
	"path"
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
	Name        string // the subtest name as written, e.g. "ORD-F01 refunds once"
	File        string // slash-separated, relative to the scanned directory
	Test        string // enclosing top-level function: TestRefund, or (*Suite).TestRefund
	Kind        Kind
	Fingerprint string // hex SHA-256 of the bound test and what it depends on
	Line        int    // line of the string literal that carries the ID
	Skips       bool   // the bound code or a function around it calls Skip

	// Testdata maps each file of the package's testdata tree, as a slash path
	// relative to the scanned directory, to the hex SHA-256 of its content.
	// It is shared by the package's declarations: do not modify it.
	Testdata map[string]string
}

// Scan parses every _test.go file under dir and returns the obligation IDs
// they declare, sorted by file and line. Like the go tool, it skips testdata
// and vendor directories and names starting with "." or "_". A file that does
// not parse is an error that names its file and line.
func Scan(dir string) ([]Declaration, error) {
	s := &scanner{fsys: os.DirFS(dir), fset: token.NewFileSet(), pkgs: map[[2]string]*pkg{}}
	if err := fs.WalkDir(s.fsys, ".", s.visit); err != nil {
		return nil, fmt.Errorf("scan %s: %w", dir, err)
	}
	var decls []Declaration
	for _, p := range s.pkgs {
		found, err := p.declarations()
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", dir, err)
		}
		decls = append(decls, found...)
	}
	slices.SortStableFunc(decls, func(a, b Declaration) int {
		return cmp.Or(strings.Compare(a.File, b.File), cmp.Compare(a.Line, b.Line), strings.Compare(a.Name, b.Name))
	})
	return decls, nil
}

type scanner struct {
	fsys fs.FS
	fset *token.FileSet
	pkgs map[[2]string]*pkg // by directory and package name
}

func (s *scanner) visit(rel string, d fs.DirEntry, err error) error {
	if err != nil {
		return err
	}
	name := d.Name()
	hidden := strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")
	if d.IsDir() {
		if rel != "." && (hidden || name == "testdata" || name == "vendor") {
			return fs.SkipDir
		}
		return nil
	}
	if hidden || !strings.HasSuffix(name, "_test.go") {
		return nil
	}
	src, err := fs.ReadFile(s.fsys, rel)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}
	f, err := parser.ParseFile(s.fset, rel, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse: %w", err)
	}
	key := [2]string{path.Dir(rel), f.Name.Name}
	p := s.pkgs[key]
	if p == nil {
		p = &pkg{
			fsys: s.fsys, fset: s.fset, dir: key[0], decls: map[string][]top{},
			tables: map[*ast.CompositeLit]bool{}, infos: map[infoKey]*info{}, skips: map[ast.Node]bool{},
		}
		s.pkgs[key] = p
	}
	p.add(&file{path: rel, ast: f, constraint: buildConstraint(f, name)})
	return nil
}

// pkg is the test files of one package, which share their helpers.
type pkg struct {
	fsys   fs.FS
	fset   *token.FileSet
	dir    string
	files  []*file
	decls  map[string][]top // helpers by name; methods under their receiver type's
	always []top            // TestMain and init
	tables map[*ast.CompositeLit]bool
	infos  map[infoKey]*info
	skips  map[ast.Node]bool // per function: calls Skip outside its subtests
}

type file struct {
	path       string
	ast        *ast.File
	constraint string
}

// top is a top-level declaration and its file.
type top struct {
	node ast.Node
	file *file
}

func (p *pkg) add(f *file) {
	p.files = append(p.files, f)
	for _, d := range f.ast.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			name := d.Name.Name
			switch {
			case d.Recv == nil && (name == "TestMain" || name == "init"):
				p.always = append(p.always, top{d, f})
				continue
			case isEntryPoint(name):
				continue
			case d.Recv != nil && len(d.Recv.List) == 1:
				name = recvType(d.Recv.List[0].Type)
			}
			p.decls[name] = append(p.decls[name], top{d, f})
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, id := range s.Names {
						p.decls[id.Name] = append(p.decls[id.Name], top{d, f})
					}
				case *ast.TypeSpec:
					p.decls[s.Name.Name] = append(p.decls[s.Name.Name], top{d, f})
				}
			}
		}
	}
}

func isEntryPoint(name string) bool {
	return slices.ContainsFunc([]string{"Test", "Benchmark", "Fuzz", "Example"}, func(p string) bool {
		return strings.HasPrefix(name, p)
	})
}

func recvType(x ast.Expr) string {
	for {
		switch t := x.(type) {
		case *ast.StarExpr:
			x = t.X
		case *ast.IndexExpr:
			x = t.X
		case *ast.IndexListExpr:
			x = t.X
		case *ast.Ident:
			return t.Name
		default:
			return ""
		}
	}
}

// pending is a declaration found in the first pass, fingerprinted once every
// table of the package is known.
type pending struct {
	d     Declaration
	file  *file
	stack []ast.Node // from the top-level function down to call
	call  *ast.CallExpr
	loop  *ast.RangeStmt // TableEntry only
	entry ast.Expr       // TableEntry only
}

func (p *pkg) declarations() ([]Declaration, error) {
	var found []pending
	for _, f := range p.files {
		for _, d := range f.ast.Decls {
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
					found = p.discover(found, pending{d: Declaration{File: f.path, Test: test}, file: f, stack: slices.Clone(stack), call: call})
				}
				return true
			})
		}
	}
	if len(found) == 0 {
		return nil, nil
	}
	testdata, err := p.testdata()
	if err != nil {
		return nil, err
	}
	out := make([]Declaration, 0, len(found))
	for _, pd := range found {
		pd.d.Fingerprint, pd.d.Testdata = p.fingerprint(pd), testdata
		pd.d.Skips = p.skipped(pd)
		out = append(out, pd.d)
	}
	return out, nil
}

// discover appends the declarations bound by pd.call, a Run with two
// arguments, and records the table it runs, if any.
func (p *pkg) discover(found []pending, pd pending) []pending {
	if lit, ok := pd.call.Args[0].(*ast.BasicLit); ok {
		pd.d.Kind = Subtest
		return p.named(found, pd, lit)
	}
	var loopVar, field string
	switch arg := pd.call.Args[0].(type) {
	case *ast.Ident:
		loopVar = arg.Name
	case *ast.SelectorExpr:
		x, ok := arg.X.(*ast.Ident)
		if !ok {
			return found
		}
		loopVar, field = x.Name, arg.Sel.Name
	default:
		return found
	}
	loop := rangeOver(pd.stack, loopVar)
	if loop == nil {
		return found
	}
	table := p.resolve(loop.X, pd.stack[0], loop.Pos())
	if table == nil {
		return found
	}
	p.tables[table] = true
	pd.d.Kind, pd.loop = TableEntry, loop
	key, elt := eltTypes(table.Type)
	byKey := field == "" && isIdent(loop.Key, loopVar)
	for _, x := range table.Elts {
		pd.entry = fillElt(x, key, elt)
		found = p.named(found, pd, entryName(x, field, byKey))
	}
	return found
}

// named appends pd if lit starts with an obligation ID followed by a space, a
// tab or the end of the string: go test turns that blank into the "_" that
// obligation.FromTestSegment expects.
func (p *pkg) named(found []pending, pd pending, lit *ast.BasicLit) []pending {
	if lit == nil || lit.Kind != token.STRING {
		return found
	}
	name, err := strconv.Unquote(lit.Value)
	if err != nil {
		return found
	}
	id, _, _ := strings.Cut(strings.ReplaceAll(name, "\t", " "), " ")
	if pd.d.ID, err = obligation.Parse(id); err != nil {
		return found
	}
	pd.d.Name, pd.d.Line = name, p.fset.Position(lit.Pos()).Line
	return append(found, pd)
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
func (p *pkg) resolve(x ast.Expr, fn ast.Node, loop token.Pos) *ast.CompositeLit {
	switch x := x.(type) {
	case *ast.CompositeLit:
		return x
	case *ast.Ident:
		if lit := lastDef(fn, x.Name, loop); lit != nil {
			return lit
		}
		for _, t := range p.decls[x.Name] {
			if gd, ok := t.node.(*ast.GenDecl); ok && gd.Tok == token.VAR {
				return lastDef(gd, x.Name, gd.End())
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

// entryName returns the string literal that names one table entry, or nil.
func entryName(x ast.Expr, field string, byKey bool) *ast.BasicLit {
	if kv, ok := x.(*ast.KeyValueExpr); ok { // a map entry
		if byKey {
			lit, _ := kv.Key.(*ast.BasicLit)
			return lit
		}
		x = kv.Value
	}
	if e, ok := x.(*ast.CompositeLit); ok && field != "" {
		for _, x := range e.Elts {
			if kv, ok := x.(*ast.KeyValueExpr); ok && isIdent(kv.Key, field) {
				lit, _ := kv.Value.(*ast.BasicLit)
				return lit
			} else if lit, ok := x.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				return lit // unkeyed: the first string literal
			}
		}
	}
	return nil
}

// skipped reports whether pd's bound code, or a function around it outside
// its other subtests, skips. For a table entry the bound code is the runner's
// function; a skip elsewhere in the loop body skips the enclosing function.
func (p *pkg) skipped(pd pending) bool {
	for i, n := range pd.stack {
		switch n.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
		default:
			continue
		}
		skips, ok := p.skips[n]
		if !ok {
			skips = hasSkip(n, pd.stack[:i], true)
			p.skips[n] = skips
		}
		if skips {
			return true
		}
	}
	if fn, ok := pd.call.Args[1].(*ast.Ident); ok { // a named function
		return slices.ContainsFunc(p.decls[fn.Name], func(t top) bool {
			fd, ok := t.node.(*ast.FuncDecl)
			return ok && fd.Recv == nil && hasSkip(fd, nil, false)
		})
	}
	return hasSkip(pd.call.Args[1], pd.stack, false)
}

// hasSkip reports whether a Skip, Skipf or SkipNow call on a testing
// parameter occurs in root, whose ancestors are given. With ownOnly it does
// not look into subtests started with Run: their skips only skip themselves.
func hasSkip(root ast.Node, ancestors []ast.Node, ownOnly bool) bool {
	found := false
	ast.PreorderStack(root, slices.Clip(ancestors), func(n ast.Node, stack []ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		switch {
		case found || !ok:
			return !found
		case isSkip(call, stack):
			found = true
		}
		return !found && (!ownOnly || !isRun(call))
	})
	return found
}

func isSkip(call *ast.CallExpr, stack []ast.Node) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || !slices.Contains([]string{"Skip", "Skipf", "SkipNow"}, sel.Sel.Name) {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && isTestingParam(stack, x.Name)
}

// isTestingParam reports whether name, seen from the innermost function in
// stack, is a *testing.T, *testing.B, *testing.F or testing.TB parameter.
// Local variables are not tracked.
func isTestingParam(stack []ast.Node, name string) bool {
	for i := len(stack) - 1; i >= 0; i-- {
		var ft *ast.FuncType
		switch fn := stack[i].(type) {
		case *ast.FuncLit:
			ft = fn.Type
		case *ast.FuncDecl:
			ft = fn.Type
		default:
			continue
		}
		for _, field := range ft.Params.List {
			if slices.ContainsFunc(field.Names, func(id *ast.Ident) bool { return id.Name == name }) {
				typ, ptr := field.Type, false
				if star, ok := typ.(*ast.StarExpr); ok {
					typ, ptr = star.X, true
				}
				sel, ok := typ.(*ast.SelectorExpr)
				return ok && (ptr && slices.Contains([]string{"T", "B", "F"}, sel.Sel.Name) || !ptr && sel.Sel.Name == "TB")
			}
		}
	}
	return false
}

func isRun(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Run" && len(call.Args) == 2
}

func isIdent(x ast.Expr, name string) bool {
	id, ok := x.(*ast.Ident)
	return ok && id.Name == name
}
