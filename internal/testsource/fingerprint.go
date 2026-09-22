package testsource

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"go/ast"
	"go/build/constraint"
	"go/token"
	"io/fs"
	"maps"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// encoding names the canonical encoding and changes with it, so fingerprints
// of different encodings never compare equal by accident.
const encoding = "aval-testsource-3"

// fingerprint hashes what pd's test depends on, as the package doc lists, and
// returns the testdata files that code names.
func (p *pkg) fingerprint(pd pending, testdata map[string]string) (string, []string) {
	var enclosing, bound []*info
	for _, n := range pd.stack {
		switch n := n.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			enclosing = append(enclosing, p.info(n, pd.file, true))
		case *ast.RangeStmt:
			// Table loops are left out of the enclosing chain; keep the
			// header of those around this declaration.
			if isTableLoop(n) && n != pd.loop {
				header := *n
				header.Body = &ast.BlockStmt{}
				bound = append(bound, encode(&header, pd.file, false, p.tables))
			}
		}
	}
	if pd.loop != nil {
		bound = append(bound, p.info(pd.loop, pd.file, false), encode(pd.entry, pd.file, false, p.tables))
	} else {
		bound = append(bound, p.info(pd.call, pd.file, false))
	}
	code := slices.Concat(enclosing, bound)
	helpers := p.helpers(pd.stack[0], code)

	var b bytes.Buffer
	section := func(items ...string) {
		b.WriteString(strconv.Itoa(len(items)))
		for _, s := range items {
			b.WriteString("," + strconv.Itoa(len(s)) + ":" + s)
		}
	}
	section(encoding, string(pd.d.Kind), pd.file.ast.Name.Name, pd.file.constraint)
	section(digests(enclosing)...)
	section(digests(bound)...)
	section(digests(helpers)...)
	section(imports(slices.Concat(code, helpers))...)
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:]), testdataRefs(slices.Concat(code, helpers), testdata)
}

// info is the encoding of one node, with what it names.
type info struct {
	digest   string
	idents   map[string]bool // identifiers in value, call or type position
	selected map[string]bool // sel of every x.sel
	quals    map[string]bool // x of every x.sel
	declared map[string]bool // names the node declares, which hide helpers
	strs     map[string]bool // string literals
	file     *file
}

type infoKey struct {
	node      ast.Node
	enclosing bool
}

func (p *pkg) info(n ast.Node, f *file, enclosing bool) *info {
	k := infoKey{n, enclosing}
	if inf, ok := p.infos[k]; ok {
		return inf
	}
	inf := encode(n, f, enclosing, p.tables)
	p.infos[k] = inf
	return inf
}

// helpers returns the top-level declarations of the package's test files
// that code names, transitively, the methods it selects by name, and the
// declarations that always run, sorted by digest. A name the code declares
// itself is a local, not a helper. The enclosing top-level function is not a
// helper of its own subtests.
func (p *pkg) helpers(enclosing ast.Node, code []*info) []*info {
	seen := map[ast.Node]bool{enclosing: true}
	queue := slices.Clone(p.always)
	enqueue := func(inf *info, locals map[string]bool) {
		for name := range inf.idents {
			if !locals[name] {
				queue = append(queue, p.decls[name]...)
			}
		}
		for name := range inf.selected {
			queue = append(queue, p.methods[name]...)
		}
	}
	locals := map[string]bool{}
	for _, c := range code {
		maps.Copy(locals, c.declared)
	}
	for _, c := range code {
		enqueue(c, locals)
	}
	var out []*info
	for len(queue) > 0 {
		t := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if seen[t.node] {
			continue
		}
		seen[t.node] = true
		inf := p.info(t.node, t.file, false)
		out = append(out, inf)
		enqueue(inf, inf.declared)
	}
	slices.SortFunc(out, func(a, b *info) int { return strings.Compare(a.digest, b.digest) })
	return out
}

func digests(infos []*info) []string {
	out := make([]string, len(infos))
	for i, inf := range infos {
		out[i] = inf.digest
	}
	return out
}

// imports returns, as "name path", the imports of the files of infos that
// they qualify names with, and those files' blank and dot imports. An
// unnamed import goes by the name its path suggests: the last element, before
// a major version (/v2) or a gopkg.in suffix (.v3), without a go- prefix or a
// -go suffix.
func imports(infos []*info) []string {
	set := map[string]bool{}
	for _, inf := range infos {
		for _, spec := range inf.file.ast.Imports {
			p, _ := strconv.Unquote(spec.Path.Value)
			name := path.Base(p)
			if v, ok := strings.CutPrefix(name, "v"); ok && v != "" && strings.Trim(v, "0123456789") == "" && path.Dir(p) != "." {
				name = path.Base(path.Dir(p))
			}
			name, _, _ = strings.Cut(name, ".")
			name = strings.TrimSuffix(strings.TrimPrefix(name, "go-"), "-go")
			if spec.Name != nil {
				name = spec.Name.Name
			}
			if name == "_" || name == "." || inf.quals[name] {
				set[name+" "+p] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(set))
}

// testdataRefs returns the testdata files whose base name ends one of the
// string literals of infos, such as "testdata/refund.golden" or
// "refund.golden".
func testdataRefs(infos []*info, testdata map[string]string) []string {
	if len(testdata) == 0 {
		return nil
	}
	bases := map[string]bool{}
	for _, inf := range infos {
		for s := range inf.strs {
			bases[path.Base(s)] = true
		}
	}
	var refs []string
	for name := range testdata {
		if bases[path.Base(name)] {
			refs = append(refs, name)
		}
	}
	slices.Sort(refs)
	return refs
}

// testdata returns the hex SHA-256 of the content of each file in the
// package's testdata tree, or nil when it has none. Other than regular files
// are recorded by their type.
func (p *pkg) testdata() (map[string]string, error) {
	root := path.Join(p.dir, "testdata")
	fi, err := fs.Stat(p.fsys, root)
	if errors.Is(err, fs.ErrNotExist) || err == nil && !fi.IsDir() {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("hash %s: %w", root, err)
	}
	files := map[string]string{}
	err = fs.WalkDir(p.fsys, root, func(name string, d fs.DirEntry, err error) error {
		switch {
		case err != nil || d.IsDir():
			return err
		case !d.Type().IsRegular():
			files[name] = d.Type().String()
			return nil
		}
		data, err := fs.ReadFile(p.fsys, name)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		sum := sha256.Sum256(data)
		files[name] = hex.EncodeToString(sum[:])
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("hash %s: %w", root, err)
	}
	return files, nil
}

// Known GOOS and GOARCH values, as in go/build.
var (
	knownOS   = strings.Fields("aix android darwin dragonfly freebsd hurd illumos ios js linux nacl netbsd openbsd plan9 solaris wasip1 windows zos")
	knownArch = strings.Fields("386 amd64 amd64p32 arm armbe arm64 arm64be loong64 mips mipsle mips64 mips64le mips64p32 mips64p32le ppc ppc64 ppc64le riscv riscv64 s390 s390x sparc sparc64 wasm")
)

// buildConstraint returns what restricts a file to some builds: the GOOS and
// GOARCH of its name, read exactly as go/build does, and its //go:build
// expression.
func buildConstraint(f *ast.File, name string) string {
	var tags []string
	name, _, _ = strings.Cut(name, ".")
	if i := strings.Index(name, "_"); i >= 0 {
		l := strings.Split(name[i:], "_")
		if n := len(l); l[n-1] == "test" {
			l = l[:n-1]
		}
		n := len(l)
		switch {
		case n >= 2 && slices.Contains(knownOS, l[n-2]) && slices.Contains(knownArch, l[n-1]):
			tags = l[n-2:]
		case n >= 1 && (slices.Contains(knownOS, l[n-1]) || slices.Contains(knownArch, l[n-1])):
			tags = l[n-1:]
		}
	}
	for _, g := range f.Comments {
		for _, c := range g.List {
			if x, err := constraint.Parse(c.Text); err == nil && g.Pos() < f.Package {
				tags = append(tags, x.String())
			}
		}
	}
	return strings.Join(tags, "\n")
}

var (
	posType     = reflect.TypeFor[token.Pos]()
	commentType = reflect.TypeFor[*ast.CommentGroup]()
	stmtsType   = reflect.TypeFor[[]ast.Stmt]()
)

// encoder writes the canonical encoding of a syntax tree: in tree order, each
// node's type name and every field, with identifiers and literals as written,
// operator tokens and flags. Of a position it keeps only whether it is set,
// which is how go/ast records optional tokens such as the "..." of f(xs...).
// Without positions or comments, the encoding does not depend on whitespace,
// line breaks, comments or where the code sits in the file. Besides:
//
//   - the entries of a table are left out, since each is bound on its own;
//   - the type an element of a slice, array or map literal elides is written
//     out, so []T{T{…}} and []T{{…}} encode alike;
//   - for an enclosing function, the statements that only belong to other
//     subtests are left out: those that only call Run, table loops (see
//     isTableLoop) and the composite literal definitions of tables used by
//     nothing else.
//
// Nodes must come from a parse with parser.SkipObjectResolution, which leaves
// every Ident.Obj nil: resolver objects would link nodes into cycles.
type encoder struct {
	buf       bytes.Buffer
	enclosing bool
	tables    map[*ast.CompositeLit]bool
	tableOnly map[string]bool     // tables used only by table loops
	nonRefs   map[*ast.Ident]bool // selected names and struct literal keys
	inf       *info
}

func encode(n ast.Node, f *file, enclosing bool, tables map[*ast.CompositeLit]bool) *info {
	e := &encoder{
		enclosing: enclosing, tables: tables, nonRefs: map[*ast.Ident]bool{},
		inf: &info{
			idents: map[string]bool{}, selected: map[string]bool{}, quals: map[string]bool{},
			declared: declared(n), strs: map[string]bool{}, file: f,
		},
	}
	if enclosing {
		e.tableOnly = tableOnly(n)
	}
	e.encode(reflect.ValueOf(n))
	sum := sha256.Sum256(e.buf.Bytes())
	e.inf.digest = string(sum[:])
	return e.inf
}

func (e *encoder) encode(v reflect.Value) {
	t := v.Type()
	switch {
	case t == commentType:
		return
	case t == posType:
		e.buf.WriteString(strconv.FormatBool(token.Pos(v.Int()).IsValid()))
	case (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) && v.IsNil():
		e.buf.WriteString("nil")
	case v.Kind() == reflect.Interface:
		e.encode(v.Elem())
		return
	case v.Kind() == reflect.Pointer:
		switch n := v.Interface().(type) {
		case *ast.Ident:
			if !e.nonRefs[n] {
				e.inf.idents[n.Name] = true
			}
		case *ast.SelectorExpr:
			e.nonRefs[n.Sel] = true
			e.inf.selected[n.Sel.Name] = true
			if x, ok := n.X.(*ast.Ident); ok {
				e.inf.quals[x.Name] = true
			}
		case *ast.BasicLit:
			if s, err := strconv.Unquote(n.Value); err == nil && n.Kind == token.STRING {
				e.inf.strs[s] = true
			}
		case *ast.CompositeLit:
			v = reflect.ValueOf(e.compositeLit(n))
		case ast.Node:
			for _, id := range declaring(n) {
				e.nonRefs[id] = true
			}
		}
		e.encode(v.Elem())
		return
	case v.Kind() == reflect.Struct:
		e.buf.WriteString(t.Name() + "{")
		for i := range v.NumField() {
			e.encode(v.Field(i))
		}
		e.buf.WriteByte('}')
	case v.Kind() == reflect.Slice:
		e.buf.WriteByte('[')
		for i := range v.Len() {
			if s, ok := v.Index(i).Interface().(ast.Stmt); !ok || t != stmtsType || !e.enclosing || !e.dropped(s) {
				e.encode(v.Index(i))
			}
		}
		e.buf.WriteByte(']')
	case v.Kind() == reflect.String:
		e.buf.WriteString(strconv.Quote(v.String()))
	case v.Kind() == reflect.Bool:
		e.buf.WriteString(strconv.FormatBool(v.Bool()))
	case v.CanInt():
		e.buf.WriteString(strconv.FormatInt(v.Int(), 10))
	default:
		// go/ast has no other kinds below a declaration. Dropping one would
		// hide a change, so a Go release that adds one must fail the tests.
		panic(fmt.Sprintf("testsource: cannot encode %s (%s)", t, v.Kind()))
	}
	e.buf.WriteByte(' ')
}

// compositeLit returns the copy of n to encode: without its entries if n is
// a table, else with the types its elements elide written out. The keys of a
// literal other than a map or array are field names, not references.
func (e *encoder) compositeLit(n *ast.CompositeLit) *ast.CompositeLit {
	c := *n
	if e.tables[n] {
		c.Elts = nil
		return &c
	}
	key, elt := eltTypes(n.Type)
	c.Elts = make([]ast.Expr, len(n.Elts))
	for i, x := range n.Elts {
		if kv, ok := x.(*ast.KeyValueExpr); ok && elt == nil {
			if id, ok := kv.Key.(*ast.Ident); ok {
				e.nonRefs[id] = true
			}
		}
		c.Elts[i] = fillElt(x, key, elt)
	}
	return &c
}

// dropped reports whether an enclosing function's statement s only belongs
// to other subtests.
func (e *encoder) dropped(s ast.Stmt) bool {
	switch s := s.(type) {
	case *ast.ExprStmt:
		call, ok := s.X.(*ast.CallExpr)
		return ok && isRun(call)
	case *ast.RangeStmt:
		return isTableLoop(s)
	case *ast.AssignStmt:
		return len(s.Lhs) == 1 && len(s.Rhs) == 1 && isCompositeLit(s.Rhs[0]) && e.tableOnly[identName(s.Lhs[0])]
	case *ast.DeclStmt:
		gd, ok := s.Decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR || len(gd.Specs) != 1 {
			return false
		}
		vs, ok := gd.Specs[0].(*ast.ValueSpec)
		return ok && len(vs.Names) == 1 && len(vs.Values) == 1 && isCompositeLit(vs.Values[0]) && e.tableOnly[vs.Names[0].Name]
	}
	return false
}

func isCompositeLit(x ast.Expr) bool {
	_, ok := x.(*ast.CompositeLit)
	return ok
}

// isTableLoop reports whether s is a range loop whose body only runs
// subtests with computed names, and perhaps copies its variables (tt := tt):
// the loop of a table.
func isTableLoop(s ast.Stmt) bool {
	r, ok := s.(*ast.RangeStmt)
	if !ok || len(r.Body.List) == 0 {
		return false
	}
	runs := false
	for _, s := range r.Body.List {
		switch s := s.(type) {
		case *ast.ExprStmt:
			call, ok := s.X.(*ast.CallExpr)
			if !ok || !isRun(call) {
				return false
			}
			if _, named := call.Args[0].(*ast.BasicLit); named {
				return false
			}
			runs = true
		case *ast.AssignStmt:
			if len(s.Lhs) != 1 || len(s.Rhs) != 1 || identName(s.Lhs[0]) == "" || identName(s.Lhs[0]) != identName(s.Rhs[0]) {
				return false
			}
		default:
			return false
		}
	}
	return runs
}

// tableOnly returns the names that fn defines and uses only as the range
// expression of table loops.
func tableOnly(fn ast.Node) map[string]bool {
	ranged, uses := map[string]bool{}, map[string]int{}
	skip := map[*ast.Ident]bool{}
	ast.Inspect(fn, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.RangeStmt:
			if x, ok := n.X.(*ast.Ident); ok && isTableLoop(n) {
				skip[x], ranged[x.Name] = true, true
			}
		case *ast.AssignStmt:
			if len(n.Lhs) == 1 && len(n.Rhs) == 1 {
				if id, ok := n.Lhs[0].(*ast.Ident); ok {
					skip[id] = true
				}
			}
		case *ast.ValueSpec:
			if len(n.Names) == 1 {
				skip[n.Names[0]] = true
			}
		case *ast.Ident:
			if !skip[n] {
				uses[n.Name]++
			}
		}
		return true
	})
	out := map[string]bool{}
	for name := range ranged {
		if uses[name] == 0 {
			out[name] = true
		}
	}
	return out
}

// declaring returns the identifiers n declares, which are not references:
// field, variable, constant, type, function and label names.
func declaring(n ast.Node) []*ast.Ident {
	var ids []*ast.Ident
	add := func(xs ...ast.Expr) {
		for _, x := range xs {
			if id, ok := x.(*ast.Ident); ok {
				ids = append(ids, id)
			}
		}
	}
	switch n := n.(type) {
	case *ast.Field:
		ids = n.Names
	case *ast.ValueSpec:
		ids = n.Names
	case *ast.TypeSpec:
		add(n.Name)
	case *ast.FuncDecl:
		add(n.Name)
	case *ast.LabeledStmt:
		add(n.Label)
	case *ast.BranchStmt:
		if n.Label != nil {
			add(n.Label)
		}
	case *ast.AssignStmt:
		if n.Tok == token.DEFINE {
			add(n.Lhs...)
		}
	case *ast.RangeStmt:
		if n.Tok == token.DEFINE {
			add(n.Key, n.Value)
		}
	}
	return ids
}

// declared returns the names of the variables, constants, types, parameters
// and results n declares, which hide helpers of the same name. Struct fields
// and methods are not in scope and hide nothing.
func declared(n ast.Node) map[string]bool {
	out := map[string]bool{}
	fields := func(lists ...*ast.FieldList) {
		for _, l := range lists {
			if l == nil {
				continue
			}
			for _, f := range l.List {
				for _, id := range f.Names {
					out[id.Name] = true
				}
			}
		}
	}
	ast.Inspect(n, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.FuncType:
			fields(n.TypeParams, n.Params, n.Results)
		case *ast.FuncDecl:
			fields(n.Recv)
		case *ast.ValueSpec, *ast.TypeSpec, *ast.AssignStmt, *ast.RangeStmt:
			for _, id := range declaring(n) {
				out[id.Name] = true
			}
		}
		return true
	})
	return out
}

func identName(x ast.Expr) string {
	if id, ok := x.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

// eltTypes returns the key and element types of a slice, array or map type.
func eltTypes(t ast.Expr) (key, elt ast.Expr) {
	switch t := t.(type) {
	case *ast.ArrayType:
		return nil, t.Elt
	case *ast.MapType:
		return t.Key, t.Value
	}
	return nil, nil
}

// fillElt writes out the types an element of a literal with the given key and
// element types elides: {…} becomes T{…}, or &T{…} when elt is *T.
func fillElt(x, key, elt ast.Expr) ast.Expr {
	if kv, ok := x.(*ast.KeyValueExpr); ok {
		c := *kv
		c.Key, c.Value = fillElt(kv.Key, nil, key), fillElt(kv.Value, nil, elt)
		return &c
	}
	lit, ok := x.(*ast.CompositeLit)
	if !ok || lit.Type != nil || elt == nil {
		return x
	}
	c := *lit
	if star, ok := elt.(*ast.StarExpr); ok {
		c.Type = star.X
		return &ast.UnaryExpr{OpPos: lit.Lbrace, Op: token.AND, X: &c}
	}
	c.Type = elt
	return &c
}
