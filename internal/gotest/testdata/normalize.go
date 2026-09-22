//go:build ignore

// Normalize rewrites the parts of a `go test -json` stream that change from
// run to run or machine to machine, so that captured fixtures are stable.
//
// It edits raw lines instead of decoding events: field order, JSON escaping
// and the shape of every event (test2json and build events alike) stay
// exactly as go test wrote them. Only these values change:
//
//   - "Time" becomes a fixed timestamp and "Elapsed" becomes 0.
//   - Durations in framing and summary output ("(0.01s)", "\t0.216s") and in
//     rapid's "passed N tests (2.4ms)" become zero.
//   - Absolute paths to the fixture module, GOROOT and GOMODCACHE become
//     $FIXTUREMOD, $GOROOT and $GOMODCACHE.
//   - Goroutine IDs and program-counter offsets in stack traces become fixed.
//
// Every line must be valid JSON before and after normalizing; anything else
// is an error, so a broken capture is never written as a fixture.
//
// Usage (regen.sh does this):
//
//	go test -json ... | normalize -fixturemod DIR -goroot DIR -gomodcache DIR
package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
)

// rules operate on the JSON-encoded line, where a newline in Output reads
// as the two characters `\n` and a tab as `\t`. Each repl is a
// regexp.Expand template.
var rules = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`"Time":"[^"]*"`), `"Time":"2006-01-02T15:04:05Z"`},
	{regexp.MustCompile(`"Elapsed":[-+.0-9eE]+`), `"Elapsed":0`},
	// "--- PASS: TestX (0.01s)"
	{regexp.MustCompile(`\([0-9]+\.[0-9]+s\)\\n`), `(0.00s)\n`},
	// "ok  \texample.com/x\t0.216s", "FAIL\texample.com/x\t0.190s" and
	// "ok  \texample.com/x\t0.207s [no tests to run]"
	{regexp.MustCompile(`("Output":"(?:ok  |FAIL)\\t[^"\\]+)\\t[0-9]+\.[0-9]+s`), `${1}\t0.000s`},
	// "[rapid] OK, passed 100 tests (2.462083ms)"
	{regexp.MustCompile(` tests \([0-9][0-9.hmnsuµ]*\)`), ` tests (0s)`},
	// goleak: "[Goroutine 37 in state ..." and "created by ... in goroutine 36"
	{regexp.MustCompile(`Goroutine [0-9]+ in state`), `Goroutine N in state`},
	{regexp.MustCompile(`in goroutine [0-9]+`), `in goroutine N`},
	// Stack frame offsets vary with GOARCH: "worker.go:9 +0x24"
	{regexp.MustCompile(` \+0x[0-9a-f]+\\n`), ` +0x0\n`},
}

func main() {
	fixturemod := flag.String("fixturemod", "", "absolute path of the fixture module")
	goroot := flag.String("goroot", "", "output of go env GOROOT")
	gomodcache := flag.String("gomodcache", "", "output of go env GOMODCACHE")
	flag.Parse()

	paths := placeholders(map[string]string{
		*fixturemod: "$FIXTUREMOD",
		*goroot:     "$GOROOT",
		*gomodcache: "$GOMODCACHE",
	})
	if err := normalize(os.Stdout, os.Stdin, paths); err != nil {
		fmt.Fprintln(os.Stderr, "normalize:", err)
		os.Exit(1)
	}
}

// placeholders returns a replacer that swaps each non-empty directory in
// dirs for its placeholder. Longer directories go first, so a directory
// nested in another one resolves to its own placeholder.
func placeholders(dirs map[string]string) *strings.Replacer {
	olds := slices.DeleteFunc(slices.Collect(maps.Keys(dirs)), func(dir string) bool { return dir == "" })
	slices.SortFunc(olds, func(a, b string) int { return cmp.Compare(len(b), len(a)) })

	oldnew := make([]string, 0, 2*len(olds))
	for _, old := range olds {
		oldnew = append(oldnew, old, dirs[old])
	}
	return strings.NewReplacer(oldnew...)
}

func normalize(w io.Writer, r io.Reader, paths *strings.Replacer) (err error) {
	bw := bufio.NewWriter(w)
	defer func() {
		if ferr := bw.Flush(); ferr != nil {
			err = errors.Join(err, fmt.Errorf("flush: %w", ferr))
		}
	}()

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for n := 1; sc.Scan(); n++ {
		raw := sc.Bytes()
		if !json.Valid(raw) {
			return fmt.Errorf("line %d is not JSON: %q", n, raw)
		}
		line := paths.Replace(string(raw))
		for _, rule := range rules {
			line = rule.re.ReplaceAllString(line, rule.repl)
		}
		if !json.Valid([]byte(line)) {
			return fmt.Errorf("line %d is not JSON after normalizing: %q", n, line)
		}
		if _, err := fmt.Fprintln(bw, line); err != nil {
			return fmt.Errorf("write line %d: %w", n, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("read: %w", err)
	}
	return nil
}
