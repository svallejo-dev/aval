package banner

import (
	"os"
	"testing"
)

func TestBanner(t *testing.T) {
	t.Run("ORD-F06 matches the golden file", func(t *testing.T) {
		want, err := os.ReadFile("testdata/want.txt")
		if err != nil {
			t.Fatal(err)
		}
		if got := Text(); got != string(want) {
			t.Errorf("Text() = %q, want %q", got, want)
		}
	})
}
