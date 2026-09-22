package testsource

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/token"
	"reflect"
	"strconv"
)

// encoding names the canonical encoding and changes with it, so fingerprints
// of different encodings never compare equal by accident.
const encoding = "aval-testsource-1"

// fingerprint hashes the encoding name, the declaration kind, the file's build
// constraint and parts, each prefixed with its length so that no two
// sequences of parts produce the same input.
func fingerprint(k Kind, build string, parts ...[]byte) string {
	var b bytes.Buffer
	for _, p := range append([][]byte{[]byte(encoding), []byte(k), []byte(build)}, parts...) {
		b.WriteString(strconv.Itoa(len(p)))
		b.WriteByte(':')
		b.Write(p)
	}
	sum := sha256.Sum256(b.Bytes())
	return hex.EncodeToString(sum[:])
}

var (
	posType     = reflect.TypeFor[token.Pos]()
	commentType = reflect.TypeFor[*ast.CommentGroup]()
)

// canon returns the canonical encoding of n: in tree order, each node's type
// name and every field, with identifiers and literals as written, operator
// tokens and flags. Of a position it keeps only whether it is set, which is
// how go/ast records optional tokens such as the "..." of f(xs...). Without
// positions or comments, the encoding does not depend on whitespace, line
// breaks, comments or where the code sits in the file.
//
// n must come from a parse with parser.SkipObjectResolution, which leaves
// every Ident.Obj nil: resolver objects would link nodes into cycles.
func canon(n ast.Node) []byte {
	var b bytes.Buffer
	encode(&b, reflect.ValueOf(n))
	return b.Bytes()
}

func encode(b *bytes.Buffer, v reflect.Value) {
	t := v.Type()
	switch {
	case t == commentType:
		return
	case t == posType:
		b.WriteString(strconv.FormatBool(token.Pos(v.Int()).IsValid()))
	case v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface:
		if v.IsNil() {
			b.WriteString("nil")
		} else {
			encode(b, v.Elem())
		}
	case v.Kind() == reflect.Struct:
		b.WriteString(t.Name() + "{")
		for i := range v.NumField() {
			encode(b, v.Field(i))
		}
		b.WriteByte('}')
	case v.Kind() == reflect.Slice:
		b.WriteByte('[')
		for i := range v.Len() {
			encode(b, v.Index(i))
		}
		b.WriteByte(']')
	case v.Kind() == reflect.String:
		b.WriteString(strconv.Quote(v.String()))
	case v.Kind() == reflect.Bool:
		b.WriteString(strconv.FormatBool(v.Bool()))
	case v.CanInt():
		b.WriteString(strconv.FormatInt(v.Int(), 10))
	default:
		// go/ast has no other kinds below a declaration. Dropping one would
		// hide a change, so a Go release that adds one must fail the tests.
		panic(fmt.Sprintf("testsource: cannot encode %s (%s)", t, v.Kind()))
	}
	b.WriteByte(' ')
}
