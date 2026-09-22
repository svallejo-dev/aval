package main

import (
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// TestMain lets testscript run the aval binary in-process as "aval".
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"aval": main,
	})
}

// TestScript runs the end-to-end scripts in testdata/script. Script names
// start with an obligation ID once aval traces itself.
func TestScript(t *testing.T) {
	t.Parallel()
	testscript.Run(t, testscript.Params{
		Dir:                 "testdata/script",
		RequireExplicitExec: true,
		RequireUniqueNames:  true,
	})
}
