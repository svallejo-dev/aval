// Package leak is a test2json fixture: its test passes but leaks a goroutine,
// which goleak.VerifyTestMain reports after all tests have run.
package leak

// Start launches a worker. It has a deliberate bug: nothing can ever stop
// the worker, so its goroutine outlives the caller.
func Start() {
	block := make(chan struct{})
	go func() { <-block }()
}
