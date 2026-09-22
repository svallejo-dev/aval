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
// in a small one, and holds the p95 to ADR-0003's 50 ms. A loaded machine,
// such as one running the other packages' tests, can push one round over, so
// it takes up to latencyRounds rounds and passes when any stays within the
// budget: a real regression is over in all of them. Shared CI runners are
// too noisy even for that, so under CI it only catches gross regressions
// (250 ms): run `go test -run TestLatency -v ./internal/hook` locally.
func TestLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("builds aval and runs it 200 times or more")
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
	writeFiles(t, small, map[string]string{"refund/new_test.go": "package refund\n"}) // untracked: stop hashes it and blocks
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
		var p95s []time.Duration
		for range latencyRounds {
			p50, p95, out := measure(t, bin, tt.dir, tt.stdin, tt.event)
			t.Logf("%s: aval hook %s: p50 %v, p95 %v", tt.repo, tt.event, p50, p95)
			if !strings.Contains(out, tt.want) {
				t.Fatalf("%s: aval hook %s wrote %q, want %q in it", tt.repo, tt.event, out, tt.want)
			}
			if p95s = append(p95s, p95); p95 <= budget {
				break
			}
		}
		if p95 := p95s[len(p95s)-1]; p95 > budget {
			t.Errorf("%s: aval hook %s: p95 %v in each of %d rounds, over the %v budget", tt.repo, tt.event, p95s, len(p95s), budget)
		}
	}
}

// latencyRounds is how many rounds of 50 runs TestLatency takes at most.
const latencyRounds = 3

// measure runs bin's hook event in dir 50 times, after 3 runs to warm up
// caches, and returns the p50 and p95 of the 50 and what the last one wrote.
func measure(t *testing.T, bin, dir, stdin string, event Event) (p50, p95 time.Duration, out string) {
	t.Helper()
	var stdout bytes.Buffer
	var times []time.Duration
	for i := range 53 {
		cmd := exec.Command(bin, "hook", string(event)) //nolint:gosec // the aval just built
		cmd.Dir, cmd.Stdin, cmd.Stdout = dir, strings.NewReader(stdin), &stdout
		stdout.Reset()
		start := time.Now()
		if err := cmd.Run(); err != nil {
			t.Fatalf("aval hook %s in %s: %v", event, dir, err)
		}
		if i >= 3 {
			times = append(times, time.Since(start))
		}
	}
	slices.Sort(times)
	return times[len(times)/2], times[(len(times)*95+99)/100-1], stdout.String()
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
