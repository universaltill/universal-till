package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pfx = "github.com/universaltill/universal-till/"

func runIn(t *testing.T, in string, top int, summary string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	code := run(strings.NewReader(in), &out, summary, top)
	return out.String(), code
}

func lines(s ...string) string { return strings.Join(s, "\n") + "\n" }

func TestPassingPackage(t *testing.T) {
	in := lines(
		`{"Action":"output","Package":"`+pfx+`internal/x","Test":"TestA","Output":"quiet noise\n"}`,
		`{"Action":"pass","Package":"`+pfx+`internal/x","Test":"TestA","Elapsed":0.5}`,
		`{"Action":"pass","Package":"`+pfx+`internal/x","Test":"TestB","Elapsed":2.5}`,
		`{"Action":"pass","Package":"`+pfx+`internal/x","Test":"TestB/sub","Elapsed":9}`,
		`{"Action":"output","Package":"`+pfx+`internal/x","Output":"ok  \tinternal/x\t1.5s\n"}`,
		`{"Action":"pass","Package":"`+pfx+`internal/x","Elapsed":1.5}`,
	)
	out, code := runIn(t, in, 1, "")
	if code != 0 {
		t.Fatalf("exit %d, out:\n%s", code, out)
	}
	if !strings.Contains(out, "ok  \tinternal/x\t1.5s") {
		t.Errorf("package ok line not passed through:\n%s", out)
	}
	if strings.Contains(out, "quiet noise") {
		t.Errorf("passing test output leaked:\n%s", out)
	}
	if !strings.Contains(out, "internal/x 1.5s") {
		t.Errorf("no package summary line:\n%s", out)
	}
	if !strings.Contains(out, "2.5s internal/x TestB") || strings.Contains(out, "0.5s internal/x TestA") {
		t.Errorf("top list should hold only the slowest (-top 1), no subtests:\n%s", out)
	}
	if strings.Contains(out, "TestB/sub") {
		t.Errorf("subtest listed as top-level:\n%s", out)
	}
	two, _ := runIn(t, in, 5, "")
	if strings.Index(two, "2.5s internal/x TestB") > strings.Index(two, "0.5s internal/x TestA") {
		t.Errorf("top list not descending:\n%s", two)
	}
}

func TestFailingTestPrintsOutputAndExits1(t *testing.T) {
	in := lines(
		`{"Action":"output","Package":"p","Test":"TestF","Output":"boom detail\n"}`,
		`{"Action":"fail","Package":"p","Test":"TestF","Elapsed":0.1}`,
		`{"Action":"fail","Package":"p","Elapsed":0.2}`,
	)
	out, code := runIn(t, in, 5, "")
	if code != 1 || !strings.Contains(out, "boom detail") {
		t.Fatalf("code %d, out:\n%s", code, out)
	}
}

func TestSkippedTestOutputPrinted(t *testing.T) {
	in := lines(
		`{"Action":"output","Package":"p","Test":"TestS","Output":"--- SKIP: TestS\n"}`,
		`{"Action":"skip","Package":"p","Test":"TestS","Elapsed":0}`,
		`{"Action":"pass","Package":"p","Elapsed":0.2}`,
	)
	out, code := runIn(t, in, 5, "")
	if code != 0 || !strings.Contains(out, "--- SKIP: TestS") {
		t.Fatalf("code %d, out:\n%s", code, out)
	}
}

func TestTimeoutFlushesUnterminatedTest(t *testing.T) {
	in := lines(
		`{"Action":"output","Package":"p","Test":"TestHang","Output":"goroutine dump\n"}`,
		`{"Action":"fail","Package":"p","Elapsed":1200}`,
	)
	out, code := runIn(t, in, 5, "")
	if code != 1 || !strings.Contains(out, "goroutine dump") {
		t.Fatalf("code %d, out:\n%s", code, out)
	}
}

func TestEmptyInputExits1(t *testing.T) {
	if out, code := runIn(t, "", 5, ""); code != 1 {
		t.Fatalf("code %d, out:\n%s", code, out)
	}
}

func TestBuildFailExits1AndPrintsOutput(t *testing.T) {
	in := lines(
		`not json: go: downloading x`,
		`{"Action":"build-output","ImportPath":"p","Output":"x.go:1: syntax error\n"}`,
		`{"Action":"output","Package":"p","Output":"FAIL\tp [build failed]\n"}`,
		`{"Action":"build-fail","ImportPath":"p"}`,
		`{"Action":"fail","Package":"p","Elapsed":0}`,
	)
	out, code := runIn(t, in, 5, "")
	if code != 1 || !strings.Contains(out, "build failed") || !strings.Contains(out, "go: downloading x") {
		t.Fatalf("code %d, out:\n%s", code, out)
	}
	// The compiler error itself (a build-output event) must reach the log.
	if !strings.Contains(out, "x.go:1: syntax error") {
		t.Fatalf("build-output not printed:\n%s", out)
	}
}

func TestStepSummaryAppendedNotTruncated(t *testing.T) {
	in := lines(`{"Action":"pass","Package":"` + pfx + `internal/x","Elapsed":1.5}`)
	dir := t.TempDir()
	sum := filepath.Join(dir, "sum.md")
	if err := os.WriteFile(sum, []byte("earlier\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_STEP_SUMMARY", sum)
	if _, code := runIn(t, in, 5, os.Getenv("GITHUB_STEP_SUMMARY")); code != 0 {
		t.Fatal("exit != 0")
	}
	b, _ := os.ReadFile(sum)
	if !strings.HasPrefix(string(b), "earlier\n") || !strings.Contains(string(b), "| internal/x | 1.5 |") {
		t.Fatalf("summary:\n%s", b)
	}
	// Unset: nothing is written.
	none := filepath.Join(dir, "none.md")
	runIn(t, in, 5, "")
	if _, err := os.Stat(none); err == nil {
		t.Fatal("file written without summary path")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("unexpected files: %v", entries)
	}
}
