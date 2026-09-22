// Package baseline defines .aval/baseline.json: the unbound tests that were
// already failing when a repository adopted aval. The gate reads it from the
// base commit, never from the head, and does not count those failures as
// regressions (ADR-0005 §7).
//
// Entries are leaf failures: failing tests with no failing subtest in the
// same run. go test marks every parent of a failing subtest as failed too, so
// writers and the gate judge leaves only, and matching is exact. A new failing
// subtest under a listed parent is therefore a regression, never hidden.
package baseline

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/svallejo-dev/aval/internal/platform/strictjson"
)

// Version is the baseline schema version this build writes and reads.
const Version = 1

// Path is where the baseline lives, relative to the repository root.
const Path = ".aval/baseline.json"

// Baseline lists tests that were failing at adoption. Use Empty when the base
// has no file.
type Baseline struct {
	Version int    `json:"version"`
	Failing []Test `json:"failing"`
}

// Test is one failing test, named as go test -json reports it.
type Test struct {
	Package string `json:"package"` // import path
	Test    string `json:"test"`    // full name, e.g. TestX or TestX/sub
}

// ErrInvalid is wrapped by every parse and validation error.
var ErrInvalid = errors.New("invalid baseline")

// Empty is the baseline of a repository with no known failures.
func Empty() Baseline { return Baseline{Version: Version} }

// Contains reports whether the baseline lists exactly this leaf failure.
func (b Baseline) Contains(pkg, test string) bool {
	return slices.Contains(b.Failing, Test{Package: pkg, Test: test})
}

// MarshalJSON writes the tests sorted by package and name, and never null, so
// rewriting an unchanged baseline produces no diff.
func (b Baseline) MarshalJSON() ([]byte, error) {
	type plain Baseline // drops the method set, so Marshal does not recurse
	n := plain(b)
	n.Failing = slices.Clone(b.Failing)
	if n.Failing == nil {
		n.Failing = []Test{}
	}
	slices.SortFunc(n.Failing, func(x, y Test) int {
		return cmp.Or(cmp.Compare(x.Package, y.Package), cmp.Compare(x.Test, y.Test))
	})
	data, err := json.Marshal(n)
	if err != nil {
		return nil, fmt.Errorf("marshal baseline: %w", err)
	}
	return data, nil
}

// Validate checks b against the embedded JSON Schema, exactly as it would be
// written.
func (b Baseline) Validate() error {
	raw, err := json.Marshal(b)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return validateJSON(raw)
}

// maxBytes caps the file size. Larger input is an error, never silently
// truncated.
const maxBytes = 1 << 20

// Parse decodes and validates exactly one baseline document. Unknown fields,
// duplicate keys, nulls, other versions and trailing data are errors.
func Parse(r io.Reader) (Baseline, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return Baseline{}, fmt.Errorf("%s: read: %w", Path, err)
	}
	if len(data) > maxBytes {
		return Baseline{}, fmt.Errorf("%s: %w: larger than %d bytes", Path, ErrInvalid, maxBytes)
	}
	if err := validateJSON(data); err != nil {
		return Baseline{}, fmt.Errorf("%s: %w", Path, err)
	}
	var b Baseline
	if err := strictjson.Decode(data, &b); err != nil {
		return Baseline{}, fmt.Errorf("%s: %w: %w", Path, ErrInvalid, err)
	}
	return b, nil
}

// Schema is the JSON Schema of baseline version 1 (schema/baseline.v1.json).
//
//go:embed schema/baseline.v1.json
var Schema []byte

const schemaURL = "https://github.com/svallejo-dev/aval/schema/baseline.v1.json"

// compiledSchema compiles Schema once. A broken embedded schema is a build
// defect, so the error surfaces on every validation instead of panicking.
var compiledSchema = sync.OnceValues(func() (*jsonschema.Schema, error) {
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(Schema))
	if err != nil {
		return nil, fmt.Errorf("parse embedded schema: %w", err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource(schemaURL, doc); err != nil {
		return nil, fmt.Errorf("load embedded schema: %w", err)
	}
	sch, err := c.Compile(schemaURL)
	if err != nil {
		return nil, fmt.Errorf("compile embedded schema: %w", err)
	}
	return sch, nil
})

// validateJSON validates raw baseline JSON against the schema. The decoder
// rejects trailing data, so a second document cannot hide after the first.
func validateJSON(raw []byte) error {
	sch, err := compiledSchema()
	if err != nil {
		return err
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("%w: schema: %w", ErrInvalid, err)
	}
	return nil
}
