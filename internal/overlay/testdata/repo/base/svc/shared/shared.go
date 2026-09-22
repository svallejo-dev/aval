// Package shared keeps package-level state that one subtest can leak into
// the next.
package shared

var level int

// Raise raises the level by one while f runs.
func Raise(f func()) {
	level++
	f()
	// The base forgets to lower the level again.
}

// Level is the current level.
func Level() int { return level }
