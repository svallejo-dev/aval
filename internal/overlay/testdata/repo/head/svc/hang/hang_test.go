package hang

import (
	"os"
	"testing"
	"time"
)

func TestHang(t *testing.T) {
	t.Run("ORD-F07 never ends", func(t *testing.T) {
		if err := os.WriteFile(os.Getenv("AVAL_OVERLAY_STARTED"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Hour)
	})
}
