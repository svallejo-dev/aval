package hook

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestLatency runs the built aval 50 times per event in this repository and
// in a small one, and holds the p95 to ADR-0003's 50 ms. Shared CI runners
// are too noisy for that bound, so under CI it only catches gross regressions
// (250 ms): run `go test -run TestLatency -v ./internal/hook` locally.
func TestLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("builds aval and runs it 200 times")
	}
	budget := 50 * time.Millisecond
	if os.Getenv("CI") != "" {
		budget = 250 * time.Millisecond
	}
	// .exe runs everywhere, and Windows needs it.
	bin := filepath.Join(t.TempDir(), "aval.exe")
	if out, err := exec.Command("go", "build", "-o", bin, "../../cmd/aval").CombinedOutput(); err != nil { //nolint:gosec // bin is a temporary path
		t.Fatalf("go build: %v\n%s", err, out)
	}
	this, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	small := newRepo(t, shopFiles)
	edit := func(file string) string {
		return `{"hook_event_name":"PostToolUse","tool_name":"Edit","tool_input":{"file_path":` + quote(file) + `}}`
	}
	const stop = `{"hook_event_name":"Stop","stop_hook_active":false}`
	tests := []struct {
		repo, dir, stdin string
		event            Event
		want             string // in the answer, to prove the whole path ran
	}{
		{"this repo", this, edit(filepath.Join(this, "internal", "obligation", "obligation_test.go")), PostToolUse, ""},
		{"this repo", this, stop, Stop, ""},
		{"small repo", small, edit(filepath.Join(small, "refund", "refund_test.go")), PostToolUse, "ORD-F01, ORD-F03"},
		{"small repo", small, stop, Stop, `"decision":"block"`},
	}
	for _, tt := range tests {
		var out bytes.Buffer
		var times []time.Duration
		for i := range 53 { // 3 runs to warm up caches
			cmd := exec.Command(bin, "hook", string(tt.event)) //nolint:gosec // the aval just built
			cmd.Dir, cmd.Stdin, cmd.Stdout = tt.dir, strings.NewReader(tt.stdin), &out
			out.Reset()
			start := time.Now()
			if err := cmd.Run(); err != nil {
				t.Fatalf("%s: aval hook %s: %v", tt.repo, tt.event, err)
			}
			if i >= 3 {
				times = append(times, time.Since(start))
			}
		}
		if !strings.Contains(out.String(), tt.want) {
			t.Errorf("%s: aval hook %s wrote %q, want %q in it", tt.repo, tt.event, out.String(), tt.want)
		}
		slices.Sort(times)
		p95 := times[len(times)*95/100-1]
		t.Logf("%s: aval hook %s: p50 %v, p95 %v", tt.repo, tt.event, times[len(times)/2], p95)
		if p95 > budget {
			t.Errorf("%s: aval hook %s: p95 %v, over the %v budget", tt.repo, tt.event, p95, budget)
		}
	}
}

// TestNoCharm checks ADR-0003's rule as the go tool sees it: neither
// internal/hook nor its tests depend on Charm, internal/ui or internal/cli.
func TestNoCharm(t *testing.T) {
	t.Parallel()
	out, err := exec.Command("go", "list", "-deps", "-test", ".").Output()
	if err != nil {
		t.Fatalf("go list: %v", err)
	}
	for line := range strings.Lines(string(out)) {
		pkg, _, _ := strings.Cut(strings.TrimSpace(line), " ") // test variants: "p [p.test]"
		if strings.HasPrefix(pkg, "charm.land/") || strings.HasPrefix(pkg, "github.com/charmbracelet/") ||
			strings.HasSuffix(pkg, "aval/internal/ui") || strings.HasSuffix(pkg, "aval/internal/cli") {
			t.Errorf("internal/hook depends on %s", pkg)
		}
	}
}
