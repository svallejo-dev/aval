package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rogpeppe/go-internal/testscript"

	"github.com/svallejo-dev/aval/internal/hook"
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
	goroot, gocache := hostGo()
	testscript.Run(t, testscript.Params{
		Dir:                 "testdata/script",
		RequireExplicitExec: true,
		RequireUniqueNames:  true,
		Cmds:                map[string]func(*testscript.TestScript, bool, []string){"verify-status": verifyStatus},
		// Scripts that run go test (aval trace --run-tests) use the host
		// toolchain and build cache: testscript's HOME does not exist, and
		// version-manager shims need it.
		Setup: func(env *testscript.Env) error {
			if goroot != "" {
				env.Setenv("PATH", filepath.Join(goroot, "bin")+string(os.PathListSeparator)+env.Getenv("PATH"))
				env.Setenv("GOCACHE", gocache)
			}
			env.Setenv("GOPATH", filepath.Join(env.WorkDir, ".gopath"))
			env.Setenv("GOTOOLCHAIN", "local")
			env.Setenv("GOPROXY", "off")
			return nil
		},
	})
}

// verifyStatus writes the status that `aval verify` leaves for the git
// repository in the current directory: verify-status pass|fail.
func verifyStatus(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) != 1 || (args[0] != "pass" && args[0] != "fail") {
		ts.Fatalf("usage: verify-status pass|fail")
	}
	root, key, err := hook.CurrentKey(context.Background(), ts.MkAbs("."))
	ts.Check(err)
	ts.Check(hook.WriteStatus(root, hook.Status{Key: key, Passed: args[0] == "pass", VerifiedAt: time.Now()}))
}

// hostGo returns the GOROOT and GOCACHE of the go command on PATH, or empty
// strings when there is none.
func hostGo() (goroot, gocache string) {
	out, err := exec.Command("go", "env", "GOROOT", "GOCACHE").Output()
	if err != nil {
		return "", ""
	}
	goroot, gocache, _ = strings.Cut(strings.TrimSpace(string(out)), "\n")
	return goroot, gocache
}
