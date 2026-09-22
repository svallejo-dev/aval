// Package hang is not a captured fixture: aval's cancellation test runs it
// and cancels the run once the test is hanging.
package hang

import (
	"os"
	"os/signal"
	"strconv"
	"testing"
	"time"
)

// TestHang writes its PID to $AVAL_HANG_PIDFILE, says so and hangs, so the
// caller can check that cancelling go test also ends the test binary. With
// AVAL_HANG_IGNORE_INT=1 it ignores the interrupt a cancellation sends.
func TestHang(t *testing.T) {
	file := os.Getenv("AVAL_HANG_PIDFILE")
	if file == "" {
		t.Skip("AVAL_HANG_PIDFILE not set")
	}
	if os.Getenv("AVAL_HANG_IGNORE_INT") == "1" {
		signal.Ignore(os.Interrupt)
	}
	if err := os.WriteFile(file, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Log("hanging")
	time.Sleep(time.Hour)
}
