package parallel

import "testing"

func TestDouble(t *testing.T) {
	tests := []struct {
		name string
		n    int
		want int
	}{
		{name: "ORD-F30 doubles one", n: 1, want: 2},
		{name: "ORD-F31 doubles zero", n: 0, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := Double(tt.n); got != tt.want {
				t.Errorf("Double(%d) = %d, want %d", tt.n, got, tt.want)
			}
		})
	}
}
