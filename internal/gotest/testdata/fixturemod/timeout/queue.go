// Package timeout is a test2json fixture: a subtest never returns, so the
// go test -timeout alarm panics the test binary.
package timeout

// Drain is meant to return once the queue is empty. It has a deliberate bug:
// it blocks forever.
func Drain() {
	select {}
}
