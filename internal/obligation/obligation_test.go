package obligation

import (
	"errors"
	"testing"
)

func TestParse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in      string
		wantErr bool
		want    ID
	}{
		{in: "ORD-F01", want: ID{Context: "ORD", Kind: Positive, Number: 1, raw: "ORD-F01"}},
		{in: "PAY2-N0042", want: ID{Context: "PAY2", Kind: Negative, Number: 42, raw: "PAY2-N0042"}},
		{in: "ORDERS-I10", want: ID{Context: "ORDERS", Kind: Invariant, Number: 10, raw: "ORDERS-I10"}},
		{in: "AB-S99", want: ID{Context: "AB", Kind: SLO, Number: 99, raw: "AB-S99"}},
		{in: "ORD-A01", want: ID{Context: "ORD", Kind: Assumption, Number: 1, raw: "ORD-A01"}},
		{in: "ORD-O01", want: ID{Context: "ORD", Kind: Open, Number: 1, raw: "ORD-O01"}},
		{in: "ord-F01", wantErr: true},         // context must be upper case
		{in: "O-F01", wantErr: true},           // context too short
		{in: "ABCDEFGHIJK-F01", wantErr: true}, // context too long
		{in: "1RD-F01", wantErr: true},         // context starts with a digit
		{in: "ORD-X01", wantErr: true},         // unknown kind
		{in: "ORD-F1", wantErr: true},          // too few digits
		{in: "ORD-F12345", wantErr: true},      // too many digits
		{in: "ORD-F01 title", wantErr: true},   // trailing text
		{in: " ORD-F01", wantErr: true},        // leading space
		{in: "", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			got, err := Parse(tt.in)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidID) {
					t.Fatalf("Parse(%q) error = %v, want ErrInvalidID", tt.in, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
			if got.String() != tt.in {
				t.Errorf("String() = %q, want %q (leading zeros preserved)", got.String(), tt.in)
			}
		})
	}
}

func TestFromTestSegment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		segment string
		want    string // "" means no ID
	}{
		{segment: "ORD-F01_rejects_duplicates", want: "ORD-F01"},
		{segment: "ORD-F01", want: "ORD-F01"},
		{segment: "ORD-F01#01", want: "ORD-F01"},      // duplicate subtest name
		{segment: "ORD-F010_other", want: "ORD-F010"}, // a longer number is a different ID
		{segment: "ORD-F01x", want: ""},               // no separator after the ID
		{segment: "rejects_ORD-F01", want: ""},        // ID must come first
		{segment: "TestOrders", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.segment, func(t *testing.T) {
			t.Parallel()
			got, ok := FromTestSegment(tt.segment)
			if tt.want == "" {
				if ok {
					t.Fatalf("FromTestSegment(%q) = %v, want no ID", tt.segment, got)
				}
				return
			}
			if !ok || got.String() != tt.want {
				t.Errorf("FromTestSegment(%q) = %v, %v; want %s", tt.segment, got, ok, tt.want)
			}
		})
	}
}

func TestFromRequirementName(t *testing.T) {
	t.Parallel()

	id, title, ok := FromRequirementName("ORD-F01 Refund is idempotent")
	if !ok || id.String() != "ORD-F01" || title != "Refund is idempotent" {
		t.Fatalf("got %v %q %v; want ORD-F01, %q, true", id, title, ok, "Refund is idempotent")
	}
	for _, name := range []string{"Refund is idempotent", "ORD-F01", "ORD-F01   ", "[ORD-F01] Refund"} {
		if _, _, ok := FromRequirementName(name); ok {
			t.Errorf("FromRequirementName(%q) matched, want no match", name)
		}
	}
}

func TestKindPolicy(t *testing.T) {
	t.Parallel()

	want := map[Kind]Policy{
		Positive: RequireTest, Negative: RequireTest, Invariant: RequireTest,
		SLO: Warn, Assumption: Warn, Open: Block,
	}
	for k, p := range want {
		if !k.Valid() {
			t.Errorf("Kind %s: Valid() = false", k)
		}
		if got := k.Policy(); got != p {
			t.Errorf("Kind %s: Policy() = %d, want %d", k, got, p)
		}
	}
	if Kind('X').Valid() {
		t.Error("Kind X: Valid() = true, want false")
	}
}
