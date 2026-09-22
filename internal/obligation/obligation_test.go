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
		ctx     string
		kind    Kind
		number  int
	}{
		{in: "ORD-F01", ctx: "ORD", kind: Positive, number: 1},
		{in: "PAY2-N0042", ctx: "PAY2", kind: Negative, number: 42},
		{in: "ORDERS-I10", ctx: "ORDERS", kind: Invariant, number: 10},
		{in: "AB-S99", ctx: "AB", kind: SLO, number: 99},
		{in: "ORD-A01", ctx: "ORD", kind: Assumption, number: 1},
		{in: "ORD-O01", ctx: "ORD", kind: Open, number: 1},
		{in: "ABCDEFGHIJ-F01", ctx: "ABCDEFGHIJ", kind: Positive, number: 1}, // 10-char context, the maximum
		{in: "ord-F01", wantErr: true},                                       // context must be upper case
		{in: "O-F01", wantErr: true},                                         // context too short
		{in: "ABCDEFGHIJK-F01", wantErr: true},                               // context too long
		{in: "1RD-F01", wantErr: true},                                       // context starts with a digit
		{in: "ORD-X01", wantErr: true},                                       // unknown kind
		{in: "ORD-F1", wantErr: true},                                        // too few digits
		{in: "ORD-F12345", wantErr: true},                                    // too many digits
		{in: "ORD-F01 title", wantErr: true},                                 // trailing text
		{in: " ORD-F01", wantErr: true},                                      // leading space
		{in: "ORD-F０１", wantErr: true},                                       // full-width digits
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
				if !got.IsZero() {
					t.Errorf("Parse(%q) returned a non-zero ID on error: %v", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tt.in, err)
			}
			if got.Context() != tt.ctx || got.Kind() != tt.kind || got.Number() != tt.number {
				t.Errorf("Parse(%q) = %s/%s/%d, want %s/%s/%d", tt.in,
					got.Context(), got.Kind(), got.Number(), tt.ctx, tt.kind, tt.number)
			}
			if got.String() != tt.in {
				t.Errorf("String() = %q, want %q (leading zeros preserved)", got.String(), tt.in)
			}
		})
	}
}

// TestIDIdentity: equal strings are the same obligation, different strings never are.
func TestIDIdentity(t *testing.T) {
	t.Parallel()

	a, _ := Parse("ORD-F01")
	b, _ := FromTestSegment("ORD-F01_rejects_duplicates")
	c, _ := Parse("ORD-F001")
	if a != b {
		t.Errorf("Parse and FromTestSegment disagree on ORD-F01: %#v vs %#v", a, b)
	}
	if a == c {
		t.Error("ORD-F01 and ORD-F001 compare equal, want different obligations")
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
		{segment: "ORD-F01#", want: ""},               // # without digits is not a duplicate suffix
		{segment: "ORD-F01#01x", want: ""},            // nothing may follow the duplicate suffix
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

	tests := []struct {
		name      string
		wantID    string // "" means no match
		wantTitle string
	}{
		{name: "ORD-F01 Refund is idempotent", wantID: "ORD-F01", wantTitle: "Refund is idempotent"},
		{name: "ORD-F01\tRefund is idempotent", wantID: "ORD-F01", wantTitle: "Refund is idempotent"},
		{name: "ORD-F01   Refund   ", wantID: "ORD-F01", wantTitle: "Refund"},
		{name: "ORD-F01 R", wantID: "ORD-F01", wantTitle: "R"},
		{name: "Refund is idempotent"},
		{name: "ORD-F01"},
		{name: "ORD-F01   "},
		{name: "[ORD-F01] Refund"},
		{name: "ORD-F01\nRefund"}, // a line break is never part of a header
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			id, title, ok := FromRequirementName(tt.name)
			if tt.wantID == "" {
				if ok {
					t.Fatalf("FromRequirementName(%q) = %v %q, want no match", tt.name, id, title)
				}
				return
			}
			if !ok || id.String() != tt.wantID || title != tt.wantTitle {
				t.Errorf("FromRequirementName(%q) = %v %q %v; want %s %q", tt.name, id, title, ok, tt.wantID, tt.wantTitle)
			}
		})
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
	// Unknown kinds fail closed.
	for _, k := range []Kind{'X', 0} {
		if k.Valid() {
			t.Errorf("Kind %q: Valid() = true, want false", k)
		}
		if got := k.Policy(); got != Block {
			t.Errorf("Kind %q: Policy() = %d, want Block", k, got)
		}
	}
}
