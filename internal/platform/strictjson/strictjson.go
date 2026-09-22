// Package strictjson decodes JSON contracts strictly: exactly one value, no
// unknown fields and no duplicate object keys. encoding/json and the schema
// validator both let the last duplicate win, so without this check a document
// could show one value to a human reader and another to aval. Keys count as
// duplicates when encoding/json would map them to the same field, which
// ignores case under Unicode simple folding ("a" and "A", "s" and "ſ").
package strictjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// Decode checks data for duplicate keys, then decodes its single JSON value
// into v, rejecting fields v does not declare and any trailing data.
func Decode(data []byte, v any) error {
	if err := CheckDuplicateKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("trailing data after the JSON value")
	}
	return nil
}

// frame is one open object or array while walking the tokens.
type frame struct {
	keys    map[string]bool // folded keys; nil for arrays
	wantKey bool            // objects only: the next token is a key or '}'
}

// CheckDuplicateKeys reports the first object that repeats a key, at any
// depth. Malformed JSON is reported as a syntax error.
func CheckDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var stack []*frame
	valueDone := func() {
		if n := len(stack); n > 0 && stack[n-1].keys != nil {
			stack[n-1].wantKey = true
		}
	}
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("syntax: %w", err)
		}
		if n := len(stack); n > 0 && stack[n-1].wantKey {
			top := stack[n-1]
			if tok == json.Delim('}') {
				stack = stack[:n-1]
				valueDone()
				continue
			}
			key, ok := tok.(string)
			if !ok {
				return fmt.Errorf("syntax: object key is %v, not a string", tok)
			}
			folded := fold(key)
			if top.keys[folded] {
				return fmt.Errorf("duplicate key %q", key)
			}
			top.keys[folded], top.wantKey = true, false
			continue
		}
		switch tok {
		case json.Delim('{'):
			stack = append(stack, &frame{keys: map[string]bool{}, wantKey: true})
		case json.Delim('['):
			stack = append(stack, &frame{})
		case json.Delim(']'):
			stack = stack[:len(stack)-1]
			valueDone()
		default:
			valueDone()
		}
	}
}

// fold maps each rune to the smallest rune of its simple case-folding orbit,
// so fold(x) == fold(y) exactly when strings.EqualFold(x, y).
func fold(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		lowest := r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			lowest = min(lowest, f)
		}
		b.WriteRune(lowest)
	}
	return b.String()
}
