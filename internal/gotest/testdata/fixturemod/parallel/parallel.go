// Package parallel is a test2json fixture: ID subtests that call t.Parallel,
// which adds pause and cont events to the stream.
package parallel

// Double returns twice n.
func Double(n int) int { return 2 * n }
