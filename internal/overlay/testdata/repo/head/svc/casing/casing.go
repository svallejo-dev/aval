// Package casing renames Casing_test.go to casing_test.go at head.
package casing

import "strings"

// Loud is quiet at the base.
func Loud(s string) string { return strings.ToUpper(s) }
