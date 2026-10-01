package logging

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// loggingImportPath is this package's import path, the only package whose
// RecoverAndLog satisfies the goroutine guard.
const loggingImportPath = "github.com/universaltill/universal-till/internal/logging"

// goroutineAllowRe matches a reviewed exception: the marker plus a
// non-empty reason.
var goroutineAllowRe = regexp.MustCompile(`goroutine-recover:allow\s+\S`)

// TestNoUnrecoveredGoroutines is a regression guard for ut-docs#3304: an
// unrecovered panic in ANY goroutine takes down the whole till process —
// mid-sale, on a merchant's counter — and its stack goes to the raw stderr,
// bypassing till.log, redaction and the Problems ring. Every `go` statement
// under internal/ must therefore start a func literal whose first statement
// is `defer logging.RecoverAndLog("name")`, which logs the panic (redacted)
// and ends just that goroutine. Reviewed exception: a
// `// goroutine-recover:allow <reason>` comment on the go line or the line
// directly above it.
func TestNoUnrecoveredGoroutines(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve internal/ root: %v", err)
	}
	violations, err := findUnrecoveredGoroutines(root)
	if err != nil {
		t.Fatalf("walk internal/ tree: %v", err)
	}
	if len(violations) > 0 {
		t.Errorf("%d go statement(s) can panic the whole till unrecovered and unredacted (ut-docs#3304) — write them as `go func() { defer logging.RecoverAndLog(\"pkg.what\"); ... }()`, or add a reviewed `// goroutine-recover:allow <reason>`:\n  %s",
			len(violations), strings.Join(violations, "\n  "))
	}
}

// TestFindUnrecoveredGoroutinesCatchesPlanted proves the guard bites: each
// unwrapped shape is reported, while the wrapped forms, an aliased logging
// import, a reasoned allow comment, test files and testdata/ are not.
func TestFindUnrecoveredGoroutinesCatchesPlanted(t *testing.T) {
	root := t.TempDir()
	write := func(rel, body string) {
		t.Helper()
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("bad/bad.go", `package bad

import "github.com/universaltill/universal-till/internal/logging"

type s struct{}

func (s) loop() {}
func doWork()   {}

func f(x s) {
	go x.loop()
	go func() { doWork() }()
	go func() {
		doWork()
		defer logging.RecoverAndLog("second")
	}()
	// goroutine-recover:allow
	go doWork()
	go func() { defer other.RecoverAndLog("wrong pkg") }()
	go func() { defer logging.Other("wrong func") }()
}
`)
	write("ok/ok.go", `package ok

import (
	"github.com/universaltill/universal-till/internal/logging"
	lg "github.com/universaltill/universal-till/internal/logging"
)

func doWork() {}

func g() {
	go func() {
		defer logging.RecoverAndLog("ok.wrapped")
		doWork()
	}()
	go func(n int) { defer lg.RecoverAndLog("ok.alias"); _ = n }(1)
	// goroutine-recover:allow imported by logging itself, would cycle
	go doWork()
	go doWork() // goroutine-recover:allow same-line reason
	_ = "go doWork()" // a go statement in a string
}
`)
	write("logging/self.go", `package logging

func RecoverAndLog(string) {}

func h() {
	go func() { defer RecoverAndLog("logging.self"); _ = 1 }()
}
`)
	// A second package that happens to be named logging (with its own no-op
	// RecoverAndLog) gets no exemption: only the internal/logging directory
	// itself may use the bare call (review of ut-docs#3304).
	write("foo/logging/impostor.go", `package logging

func RecoverAndLog(string) {}

func h() {
	go func() { defer RecoverAndLog("impostor"); _ = 1 }()
}
`)
	write("ok/skip_test.go", "package ok\n\nfunc t() { go doWork() }\n")
	write("ok/testdata/guest.go", "package main\n\nfunc main() { go main() }\n")

	got, err := findUnrecoveredGoroutines(root)
	if err != nil {
		t.Fatalf("findUnrecoveredGoroutines: %v", err)
	}
	at := func(line int) string { return filepath.Join("bad", "bad.go") + ":" + strconv.Itoa(line) }
	want := []string{at(11), at(12), at(13), at(18), at(19), at(20),
		filepath.Join("foo", "logging", "impostor.go") + ":6"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("findUnrecoveredGoroutines reported\n  %v\nwant\n  %v", got, want)
	}
}

// findUnrecoveredGoroutines walks root (skipping testdata/ and _test.go
// files), parses each .go file with go/ast and returns "rel/path.go:line"
// for every go statement that is not a func literal whose first statement
// is `defer <logging>.RecoverAndLog(...)` — <logging> resolved through the
// file's imports, or a bare RecoverAndLog inside package logging — and has
// no reasoned goroutine-recover:allow comment on its line or the line above.
func findUnrecoveredGoroutines(root string) ([]string, error) {
	var violations []string
	fset := token.NewFileSet()
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}

		allowed := map[int]bool{}
		for _, cg := range file.Comments {
			for _, c := range cg.List {
				if goroutineAllowRe.MatchString(c.Text) {
					allowed[fset.Position(c.Slash).Line] = true
				}
			}
		}
		names := importNames(file)
		// Keyed on the directory, not the package name, so another package
		// called logging cannot exempt itself (same rule as the stdout guard).
		inLogging := filepath.Dir(rel) == "logging"

		ast.Inspect(file, func(n ast.Node) bool {
			gs, ok := n.(*ast.GoStmt)
			if !ok {
				return true
			}
			line := fset.Position(gs.Pos()).Line
			if allowed[line] || allowed[line-1] || isRecoveredGo(gs, names, inLogging) {
				return true
			}
			violations = append(violations, rel+":"+strconv.Itoa(line))
			return true
		})
		return nil
	})
	return violations, err
}

// isRecoveredGo reports whether gs is `go func(...) { defer
// logging.RecoverAndLog(...); ... }(...)`.
func isRecoveredGo(gs *ast.GoStmt, names map[string]string, inLogging bool) bool {
	lit, ok := gs.Call.Fun.(*ast.FuncLit)
	if !ok || lit.Body == nil || len(lit.Body.List) == 0 {
		return false
	}
	def, ok := lit.Body.List[0].(*ast.DeferStmt)
	if !ok {
		return false
	}
	if inLogging {
		if id, ok := def.Call.Fun.(*ast.Ident); ok && id.Name == "RecoverAndLog" {
			return true
		}
	}
	return isImportSel(def.Call.Fun, names, loggingImportPath, "RecoverAndLog")
}
