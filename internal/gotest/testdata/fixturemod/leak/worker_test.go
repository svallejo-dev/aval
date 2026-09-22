package leak

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestWorker(t *testing.T) {
	t.Run("ORD-O01 worker stops on shutdown", func(*testing.T) {
		Start()
	})
}
