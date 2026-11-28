// Command gotestdur reads `go test -json` on stdin, passes the output through
// the way plain (non-verbose) `go test` prints it, and ends with a duration
// summary: every package's elapsed time and the slowest top-level tests
// (ut-docs#7091). It exits 1 when any package or build failed, or when the
// input held no package result at all, as a belt to the workflows' pipefail.
//
//	go test -json ./... | go run ./scripts/ci/gotestdur [-top 20]
//
// Pass-through: package-level output (ok/FAIL lines, panics, the shuffle seed,
// build output) prints at once; a test's own output is buffered and printed
// only when that test fails or skips (the plugins leg runs -v so its SKIP
// lines show). A package fail, or end of input, flushes every buffer still
// open, so a -timeout panic or a crashed binary stays visible. A line that is
// not JSON prints verbatim. With $GITHUB_STEP_SUMMARY set the same data is
// appended there as markdown tables.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const modulePrefix = "github.com/universaltill/universal-till/"

type event struct {
	Action  string
	Package string
	Test    string
	Output  string
	Elapsed float64
}

type timing struct {
	name string
	secs float64
}

func main() {
	top := flag.Int("top", 20, "number of slowest top-level tests to list")
	flag.Parse()
	os.Exit(run(os.Stdin, os.Stdout, os.Getenv("GITHUB_STEP_SUMMARY"), *top))
}

func short(pkg string) string {
	if s := strings.TrimPrefix(pkg, modulePrefix); s != "" {
		return s
	}
	return pkg
}

func run(in io.Reader, out io.Writer, summaryPath string, top int) int {
	var (
		bufs    = map[string]*strings.Builder{}
		order   []string // keys of bufs in first-seen order
		pkgs    []timing
		tests   []timing
		failed  bool
		results int
	)
	key := func(e event) string { return e.Package + "\x00" + e.Test }
	flush := func(k string) {
		if b, ok := bufs[k]; ok {
			_, _ = io.WriteString(out, b.String())
			delete(bufs, k)
		}
	}
	flushPkg := func(pkg string) {
		for _, k := range order {
			if strings.HasPrefix(k, pkg+"\x00") {
				flush(k)
			}
		}
	}
	dropPkg := func(pkg string) {
		for _, k := range order {
			if strings.HasPrefix(k, pkg+"\x00") {
				delete(bufs, k)
			}
		}
	}

	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := sc.Text()
		var e event
		if len(line) == 0 || line[0] != '{' || json.Unmarshal([]byte(line), &e) != nil {
			fmt.Fprintln(out, line)
			continue
		}
		switch e.Action {
		case "output":
			if e.Test == "" {
				_, _ = io.WriteString(out, e.Output)
				continue
			}
			k := key(e)
			b, ok := bufs[k]
			if !ok {
				b = &strings.Builder{}
				bufs[k] = b
				order = append(order, k)
			}
			b.WriteString(e.Output)
		case "fail", "skip":
			if e.Test != "" {
				flush(key(e))
				if e.Action == "fail" {
					failed = true
				}
				if !strings.Contains(e.Test, "/") {
					tests = append(tests, timing{short(e.Package) + " " + e.Test, e.Elapsed})
				}
				continue
			}
			results++
			pkgs = append(pkgs, timing{short(e.Package), e.Elapsed})
			if e.Action == "fail" {
				failed = true
				flushPkg(e.Package)
			}
			dropPkg(e.Package)
		case "pass":
			if e.Test != "" {
				delete(bufs, key(e))
				if !strings.Contains(e.Test, "/") {
					tests = append(tests, timing{short(e.Package) + " " + e.Test, e.Elapsed})
				}
				continue
			}
			results++
			pkgs = append(pkgs, timing{short(e.Package), e.Elapsed})
			dropPkg(e.Package)
		case "build-output":
			// Go 1.24+: compiler and vet errors arrive as build-output events
			// (ImportPath set, Package empty); print them as plain go test does.
			_, _ = io.WriteString(out, e.Output)
		case "build-fail":
			failed = true
		}
	}
	for _, k := range order {
		flush(k)
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(out, "gotestdur: reading input: %v\n", err)
		failed = true
	}

	byDesc := func(s []timing) {
		sort.SliceStable(s, func(i, j int) bool { return s[i].secs > s[j].secs })
	}
	byDesc(pkgs)
	byDesc(tests)
	if top >= 0 && len(tests) > top {
		tests = tests[:top]
	}

	fmt.Fprintln(out, "== go test durations (ut-docs#7091) ==")
	for _, p := range pkgs {
		fmt.Fprintf(out, "%s %.1fs\n", p.name, p.secs)
	}
	fmt.Fprintf(out, "-- slowest %d tests --\n", len(tests))
	for _, t := range tests {
		fmt.Fprintf(out, "%.1fs %s\n", t.secs, t.name)
	}

	if summaryPath != "" {
		var md strings.Builder
		md.WriteString("### go test durations\n\n| package | seconds |\n|---|---|\n")
		for _, p := range pkgs {
			fmt.Fprintf(&md, "| %s | %.1f |\n", p.name, p.secs)
		}
		md.WriteString("\n| test | seconds |\n|---|---|\n")
		for _, t := range tests {
			fmt.Fprintf(&md, "| %s | %.1f |\n", t.name, t.secs)
		}
		md.WriteString("\n")
		if f, err := os.OpenFile(summaryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = f.WriteString(md.String())
			_ = f.Close()
		} else {
			fmt.Fprintf(out, "gotestdur: step summary: %v\n", err)
		}
	}

	if results == 0 {
		fmt.Fprintln(out, "gotestdur: no package result in the input")
		return 1
	}
	if failed {
		return 1
	}
	return 0
}
