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
const encoding = "aval-testsource-2"

// fingerprint hashes what pd's test depends on, as the package doc lists.
func (p *pkg) fingerprint(pd pending, testdata string) string {
	var enclosing []*info
	for _, n := range pd.stack {
		switch n.(type) {
		case *ast.FuncDecl, *ast.FuncLit:
			enclosing = append(enclosing, p.info(n, pd.file, true))
		}
	}
	bound := []*info{p.info(pd.call, pd.file, false)}
	if pd.loop != nil {
		bound = []*info{p.info(pd.loop.Body, pd.file, false), encode(pd.entry, pd.file, false, p.tables)}
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
	section(encoding, string(pd.d.Kind), pd.file.ast.Name.Name, pd.file.constraint, testdata)
	section(digests(enclosing)...)
	section(digests(bound)...)
	section(digests(helpers)...)
	section(imports(slices.Concat(code, helpers))...)
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:])
}

// info is the encoding of one node: its digest, the names it references and
// the file it belongs to.
type info struct {
	digest string
	idents map[string]bool // identifiers, except the selected name of x.sel
	quals  map[string]bool // x of every x.sel
	file   *file
}

type infoKey struct {
	node     ast.Node
	dropRuns bool
}

func (p *pkg) info(n ast.Node, f *file, dropRuns bool) *info {
	k := infoKey{n, dropRuns}
	if inf, ok := p.infos[k]; ok {
		return inf
	}
	inf := encode(n, f, dropRuns, p.tables)
	p.infos[k] = inf
	return inf
}

// helpers returns the top-level declarations of the package that code names,
// transitively, and TestMain and init, sorted by digest. The enclosing
// top-level function is not a helper of its own subtests.
func (p *pkg) helpers(enclosing ast.Node, code []*info) []*info {
	seen := map[ast.Node]bool{enclosing: true}
	queue := slices.Clone(p.always)
	for _, c := range code {
		for name := range c.idents {
			queue = append(queue, p.decls[name]...)
		}
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
		for name := range inf.idents {
			queue = append(queue, p.decls[name]...)
		}
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

// testdata hashes the names and contents of the package's testdata tree, or
// returns "" when it has none.
func (p *pkg) testdata() (string, error) {
	root := path.Join(p.dir, "testdata")
	fi, err := fs.Stat(p.fsys, root)
	if errors.Is(err, fs.ErrNotExist) || err == nil && !fi.IsDir() {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", root, err)
	}
	h := sha256.New()
	err = fs.WalkDir(p.fsys, root, func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fmt.Fprintf(h, "%q %s\n", strings.TrimPrefix(name, root), d.Type())
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := fs.ReadFile(p.fsys, name)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		fmt.Fprintf(h, "%d\n", len(data))
		h.Write(data)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", root, err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Known GOOS and GOARCH values, as in go/build.
var (
	knownOS   = strings.Fields("aix android darwin dragonfly freebsd hurd illumos ios js linux nacl netbsd openbsd plan9 solaris wasip1 windows zos")
	knownArch = strings.Fields("386 amd64 amd64p32 arm armbe arm64 arm64be loong64 mips mipsle mips64 mips64le mips64p32 mips64p32le ppc ppc64 ppc64le riscv riscv64 s390 s390x sparc sparc64 wasm")
)

// buildConstraint returns what restricts a file to some builds: the GOOS and GOARCH
// of its name, read as go/build does, and its //go:build expression.
func buildConstraint(f *ast.File, name string) string {
	var tags []string
	if _, rest, ok := strings.Cut(strings.TrimSuffix(name, "_test.go"), "_"); ok {
		l := strings.Split(rest, "_")
		n := len(l)
		switch {
		case n >= 2 && slices.Contains(knownOS, l[n-2]) && slices.Contains(knownArch, l[n-1]):
			tags = l[n-2:]
		case slices.Contains(knownOS, l[n-1]) || slices.Contains(knownArch, l[n-1]):
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
//   - with dropRuns, statements that only call Run are left out: they are
//     other subtests.
//
// Nodes must come from a parse with parser.SkipObjectResolution, which leaves
// every Ident.Obj nil: resolver objects would link nodes into cycles.
type encoder struct {
	buf      bytes.Buffer
	dropRuns bool
	tables   map[*ast.CompositeLit]bool
	sels     map[*ast.Ident]bool
	inf      *info
}

func encode(n ast.Node, f *file, dropRuns bool, tables map[*ast.CompositeLit]bool) *info {
	e := &encoder{
		dropRuns: dropRuns, tables: tables, sels: map[*ast.Ident]bool{},
		inf: &info{idents: map[string]bool{}, quals: map[string]bool{}, file: f},
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
			if !e.sels[n] {
				e.inf.idents[n.Name] = true
			}
		case *ast.SelectorExpr:
			e.sels[n.Sel] = true
			if x, ok := n.X.(*ast.Ident); ok {
				e.inf.quals[x.Name] = true
			}
		case *ast.CompositeLit:
			v = reflect.ValueOf(e.compositeLit(n))
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
			if t != stmtsType || !e.dropRuns || !isRunStmt(v.Index(i).Interface()) {
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
// a table, else with the types its elements elide written out.
func (e *encoder) compositeLit(n *ast.CompositeLit) *ast.CompositeLit {
	c := *n
	if e.tables[n] {
		c.Elts = nil
		return &c
	}
	key, elt := eltTypes(n.Type)
	c.Elts = make([]ast.Expr, len(n.Elts))
	for i, x := range n.Elts {
		c.Elts[i] = fillElt(x, key, elt)
	}
	return &c
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

func isRunStmt(s any) bool {
	es, ok := s.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	return ok && isRun(call)
}
