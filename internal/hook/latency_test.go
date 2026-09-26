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

// EnvLatency asks for the latency measurement. Without it TestLatency skips.
const EnvLatency = "AVAL_LATENCY"

// TestLatency runs the built aval 50 times per event and holds the p95 to
// ADR-0003's 50 ms.
//
// It only runs with AVAL_LATENCY=1, because a timing budget cannot be measured
// while twenty other packages fight for the CPU: under `go test -race ./...` it
// is the machine that is being measured, and it failed about one cold run in
// five, on the fixture case, at 54 to 68 ms. `make latency` runs it alone,
// serially and without -race, and CI runs that as a step of its own, so the
// budget is still enforced on every pull request — just not against noise.
//
// The cases over this repository keep a gross-regression budget: what they cost
// is dominated by hashing whatever the working tree happens to hold
// (hook.CurrentKey: the diff against HEAD plus every untracked file), so their
// number is not comparable between checkouts. The real budget is the fixture's,
// whose contents are fixed.
//
// Even alone, a machine can push one round over, so the majority of up to three
// rounds decides: two rounds within the budget pass, two over it fail, and a
// split takes a third.
func TestLatency(t *testing.T) {
	if testing.Short() {
		t.Skip("builds aval and runs it 200 times or more")
	}
	if os.Getenv(EnvLatency) == "" {
		t.Skip("timing is not measurable beside the other packages' tests: run `make latency`, " +
			"or set " + EnvLatency + "=1 to run it here")
	}
	fixture, gross := 50*time.Millisecond, 500*time.Millisecond
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
		// budget is fixture for the fixture repository, whose size is fixed, and
		// gross for this one, whose working tree the measurement depends on.
		budget time.Duration
	}{
		{"this repo", this, edit(filepath.Join(this, "internal", "obligation", "obligation_test.go")), PostToolUse, "", gross},
		{"this repo", this, stop, Stop, "", gross},
		{"small repo", small, edit(filepath.Join(small, "refund", "refund_test.go")), PostToolUse, "ORD-F01, ORD-F03", fixture},
		{"small repo", small, stop, Stop, `"decision":"block"`, fixture},
	}
	for _, tt := range tests {
		var p95s []time.Duration
		within, over := 0, 0
		for within < 2 && over < 2 {
			p50, p95, out := measure(t, bin, tt.dir, tt.stdin, tt.event)
			t.Logf("%s: aval hook %s: p50 %v, p95 %v", tt.repo, tt.event, p50, p95)
			if !strings.Contains(out, tt.want) {
				t.Fatalf("%s: aval hook %s wrote %q, want %q in it", tt.repo, tt.event, out, tt.want)
			}
			p95s = append(p95s, p95)
			if p95 <= tt.budget {
				within++
			} else {
				over++
			}
		}
		if over == 2 {
			t.Errorf("%s: aval hook %s: p95 %v in %d rounds, over the %v budget in two", tt.repo, tt.event, p95s, len(p95s), tt.budget)
		}
	}
}

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
