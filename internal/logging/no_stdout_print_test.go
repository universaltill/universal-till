package logging

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestNoStdoutPrintCalls is a regression guard for ut-docs#2728 and
// ut-docs#3248: a fmt.Print/Printf/Println call anywhere under internal/ goes
// to stdout only, so on a Windows GUI launch (invalid stdout handle) it never
// reaches till.log; and a fmt.Fprint/Fprintf/Fprintln straight to os.Stderr
// or os.Stdout bypasses logging.Redact, so a secret in its arguments would
// reach the console unredacted. Use logging.L() (Infof/Warnf/Errorf) instead,
// which is routed through the redacted, rotated log file
// (internal/logging/file.go).
func TestNoStdoutPrintCalls(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve internal/ root: %v", err)
	}

	violations, err := findStdoutWrites(root)
	if err != nil {
		t.Fatalf("walk internal/ tree: %v", err)
	}
	if len(violations) > 0 {
		t.Errorf("fmt.Print* writes to stdout only and is lost on a Windows GUI launch (ut-docs#2728), and fmt.Fprint*(os.Stderr|os.Stdout, ...) bypasses log redaction (ut-docs#3248) — use logging.L().Infof/Warnf/Errorf instead:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestFindStdoutWritesCatchesPlantedCalls proves the guard above actually
// fires: it plants each forbidden shape in a temp tree and checks it is
// reported, while a fmt.Fprintf to an ordinary writer, a test file and a
// testdata/ file are not.
func TestFindStdoutWritesCatchesPlantedCalls(t *testing.T) {
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
	write("bad/stderr.go", `package bad

import (
	"fmt"
	"os"
)

func f(secret string) {
	fmt.Fprintf(os.Stderr, "token %s\n", secret)
	fmt.Fprintln(os.Stdout, secret)
	fmt.Fprint(os.Stderr, secret)
	fmt.Println(secret)
}
`)
	write("ok/writer.go", `package ok

import (
	"bytes"
	"fmt"
	"io"
)

func g(w io.Writer) string {
	var b bytes.Buffer
	fmt.Fprintf(&b, "x %d", 1)
	fmt.Fprintln(w, "y")
	// fmt.Fprintf(os.Stderr, "only a comment")
	return b.String() + "fmt.Fprintf(os.Stderr, \"in a string\")"
}
`)
	write("ok/skip_test.go", "package ok\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc h() { fmt.Fprintf(os.Stderr, \"test\") }\n")
	write("ok/testdata/guest.go", "package main\n\nimport (\n\t\"fmt\"\n\t\"os\"\n)\n\nfunc main() { fmt.Fprintf(os.Stderr, \"guest\") }\n")

	got, err := findStdoutWrites(root)
	if err != nil {
		t.Fatalf("findStdoutWrites: %v", err)
	}
	want := []string{
		filepath.Join("bad", "stderr.go") + ":9",
		filepath.Join("bad", "stderr.go") + ":10",
		filepath.Join("bad", "stderr.go") + ":11",
		filepath.Join("bad", "stderr.go") + ":12",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("findStdoutWrites reported\n  %v\nwant\n  %v", got, want)
	}
}

// findStdoutWrites walks root, skipping testdata/ dirs and _test.go files,
// and parses each remaining .go file with go/parser + go/ast — not a regex —
// so a string literal or comment that merely mentions "fmt.Printf" never
// false-triggers. It returns "rel/path.go:line" for every fmt.Print/Printf/
// Println call and every fmt.Fprint/Fprintf/Fprintln whose writer is
// os.Stderr or os.Stdout.
func findStdoutWrites(root string) ([]string, error) {
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

		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if !isPkgSel(call.Fun, "fmt") {
				return true
			}
			switch call.Fun.(*ast.SelectorExpr).Sel.Name {
			case "Print", "Printf", "Println":
			case "Fprint", "Fprintf", "Fprintln":
				if len(call.Args) == 0 || !(isSel(call.Args[0], "os", "Stderr") || isSel(call.Args[0], "os", "Stdout")) {
					return true
				}
			default:
				return true
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				rel = path
			}
			pos := fset.Position(call.Pos())
			violations = append(violations, rel+":"+strconv.Itoa(pos.Line))
			return true
		})
		return nil
	})
	return violations, err
}

// isPkgSel reports whether e is a selector on the identifier pkg (pkg.X).
func isPkgSel(e ast.Expr, pkg string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Name == pkg
}

// isSel reports whether e is exactly pkg.name.
func isSel(e ast.Expr, pkg, name string) bool {
	return isPkgSel(e, pkg) && e.(*ast.SelectorExpr).Sel.Name == name
}
