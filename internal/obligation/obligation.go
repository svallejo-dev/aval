// Package obligation defines the obligation: a requirement with a stable ID
// that must be backed by evidence before a change may advance.
//
// An ID has the form <CTX>-<K><NN>, for example ORD-F01, where CTX is the
// bounded context (2–10 characters, starting with a letter), K is the kind and
// NN is a 2–4 digit number. The same ID starts the OpenSpec requirement name
// ("ORD-F01 Refund is idempotent") and the Go subtest name
// (t.Run("ORD-F01 rejects duplicates", ...)), which is how aval traces a
// requirement to the tests that prove it.
package obligation

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
)

// Kind classifies what an obligation asks for.
type Kind byte

// The kinds of obligation, as the letter used in the ID.
const (
	Positive   Kind = 'F' // MUST: behavior that has to happen
	Negative   Kind = 'N' // MUST-N: behavior that must never happen
	Invariant  Kind = 'I' // a property that always holds
	SLO        Kind = 'S' // an operational objective
	Assumption Kind = 'A' // an assumption still to validate
	Open       Kind = 'O' // an open question the agent must not invent
)

// Policy is what the gate does with an obligation of a given kind.
type Policy int

// Gate policies per kind (ADR-0004).
const (
	RequireTest Policy = iota + 1 // needs at least one bound, passing test
	Warn                          // reported, never blocks in v0
	Block                         // blocks until a human resolves it
)

// String returns the kind's letter.
func (k Kind) String() string { return string(rune(k)) }

// Valid reports whether k is one of the defined kinds.
func (k Kind) Valid() bool {
	switch k {
	case Positive, Negative, Invariant, SLO, Assumption, Open:
		return true
	}
	return false
}

// Policy returns how the gate treats obligations of kind k.
func (k Kind) Policy() Policy {
	switch k {
	case Positive, Negative, Invariant:
		return RequireTest
	case Open:
		return Block
	default:
		return Warn
	}
}

// ID identifies an obligation. The zero value is not a valid ID.
type ID struct {
	Context string
	Kind    Kind
	Number  int
	raw     string
}

// ErrInvalidID is returned by Parse for strings that are not obligation IDs.
var ErrInvalidID = errors.New("invalid obligation ID")

const idPattern = `([A-Z][A-Z0-9]{1,9})-([FNISAO])([0-9]{2,4})`

var (
	fullID = regexp.MustCompile(`^` + idPattern + `$`)
	// A test2json name segment: spaces became underscores and duplicate
	// subtest names got a #NN suffix.
	testSegment = regexp.MustCompile(`^` + idPattern + `(?:_|$|#[0-9]+$)`)
	// An OpenSpec requirement name: the ID, whitespace, then a title.
	requirementName = regexp.MustCompile(`^` + idPattern + `\s+(\S.*)$`)
)

// Parse parses s as an obligation ID such as "ORD-F01".
func Parse(s string) (ID, error) {
	m := fullID.FindStringSubmatch(s)
	if m == nil {
		return ID{}, fmt.Errorf("%w: %q (want <CTX>-<K><NN>, e.g. ORD-F01)", ErrInvalidID, s)
	}
	return fromMatch(m[1], m[2], m[3]), nil
}

// FromTestSegment extracts the ID that starts one "/"-separated segment of a
// test name as reported by `go test -json`, e.g. "ORD-F01_rejects_duplicates".
func FromTestSegment(segment string) (ID, bool) {
	m := testSegment.FindStringSubmatch(segment)
	if m == nil {
		return ID{}, false
	}
	return fromMatch(m[1], m[2], m[3]), true
}

// FromRequirementName splits an OpenSpec requirement name such as
// "ORD-F01 Refund is idempotent" into its ID and title.
func FromRequirementName(name string) (ID, string, bool) {
	m := requirementName.FindStringSubmatch(name)
	if m == nil {
		return ID{}, "", false
	}
	return fromMatch(m[1], m[2], m[3]), m[4], true
}

func fromMatch(ctx, kind, num string) ID {
	n, err := strconv.Atoi(num)
	if err != nil {
		// Unreachable: the regular expression only matches digits.
		panic(fmt.Sprintf("obligation: non-numeric match %q", num))
	}
	return ID{Context: ctx, Kind: Kind(kind[0]), Number: n, raw: ctx + "-" + kind + num}
}

// String returns the ID exactly as written, preserving leading zeros.
func (id ID) String() string { return id.raw }

// IsZero reports whether id is the zero value.
func (id ID) IsZero() bool { return id.raw == "" }
