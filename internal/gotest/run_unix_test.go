//go:build unix

package gotest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// cancelOn cancels a run as soon as marker shows up in its output.
type cancelOn struct {
	w      io.Writer
	marker []byte
	cancel context.CancelFunc
}

func (c cancelOn) Write(p []byte) (int, error) {
	if bytes.Contains(p, c.marker) {
		c.cancel()
	}
	n, err := c.w.Write(p)
	return n, err //nolint:wrapcheck // a pass-through writer
}

// TestRunCancelStopsTestBinary cancels a real run while its test sleeps and
// checks that the test binary does not outlive go test, even when it ignores
// the interrupt: then it dies once go does, after waitDelay.
func TestRunCancelStopsTestBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go test")
	}
	t.Parallel()
	for _, ignore := range []string{"0", "1"} {
		t.Run("ignore interrupt "+ignore, func(t *testing.T) {
			t.Parallel()
			tmp := t.TempDir()
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute) // never wait for the hour-long sleep
			defer cancel()
			env := append(slices.Clone(fixtureEnv), "AVAL_HANG_PIDFILE="+filepath.Join(tmp, "pid"), "AVAL_HANG_IGNORE_INT="+ignore)
			opts := Options{Packages: []string{"./hang"}, Count: 1, Env: env}
			_, err := run(ctx, fixtureModule, opts, func(ctx context.Context, dir string, args, env []string, stdout, stderr io.Writer) (int, error) {
				return execGo(ctx, dir, args, env, cancelOn{stdout, []byte("hanging"), cancel}, stderr)
			})
			var te *ToolError
			if !errors.Is(err, context.Canceled) || errors.As(err, &te) {
				t.Fatalf("err = %v, want context.Canceled and no *ToolError", err)
			}
			data, err := fs.ReadFile(os.DirFS(tmp), "pid")
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(string(data))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			for deadline := time.Now().Add(5 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(20 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatalf("test binary %d outlived the cancelled run", pid)
				}
			}
		})
	}
}

// TestExecGoExitStatus puts a fake go first in PATH to check how execGo
// reads each way go can end.
func TestExecGoExitStatus(t *testing.T) {
	failing, err := filepath.Abs(filepath.Join("testdata", "fail.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name, script string
		code         int  // Report.ExitCode or ToolError.ExitCode
		tool         bool // a *ToolError
	}{
		{"tests fail", "cat '" + failing + "'; exit 1", 1, false},
		{"usage", "echo 'flag provided but not defined: -x' >&2; exit 2", 2, true},
		{"killed", "kill -KILL $$", -1, true},
	}
	for _, tt := range tests {
		bin := t.TempDir()
		//nolint:gosec // the fake go must be executable
		if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\n"+tt.script+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", bin+":/bin:/usr/bin") // the fake go first; cat after it
		r, err := Run(t.Context(), t.TempDir(), Options{})
		var te *ToolError
		switch {
		case tt.tool != errors.As(err, &te):
			t.Errorf("%s: err = %v, want a *ToolError: %v", tt.name, err, tt.tool)
		case tt.tool && te.ExitCode != tt.code:
			t.Errorf("%s: ToolError.ExitCode = %d, want %d", tt.name, te.ExitCode, tt.code)
		case tt.tool && tt.code == 2 && !strings.Contains(te.Error(), "flag provided but not defined"):
			t.Errorf("%s: ToolError = %q, want go's complaint", tt.name, te)
		case tt.tool && tt.code == -1 && !errors.As(err, new(*exec.ExitError)):
			t.Errorf("%s: err = %v, want the *exec.ExitError of the signal", tt.name, err)
		case !tt.tool && (err != nil || r.ExitCode != tt.code || len(r.Packages) != 1):
			t.Errorf("%s: Run = exit %d, %d packages, %v; want exit %d, 1 package", tt.name, r.ExitCode, len(r.Packages), err, tt.code)
		}
	}
}
