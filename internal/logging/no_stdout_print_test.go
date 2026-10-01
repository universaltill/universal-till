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
		t.Errorf("fmt.Print* writes to stdout only and is lost on a Windows GUI launch (ut-docs#2728); fmt.Fprint*(os.Stderr|os.Stdout, ...) bypasses log redaction (ut-docs#3248); and any other os.Stderr/os.Stdout use (os.Stderr.Write, io.WriteString(os.Stderr, ...), w := os.Stderr, cmd.Stderr = os.Stderr), a dot-import of fmt/os, or the print/println builtins does too (ut-docs#3304) — aliased fmt/os imports included. Use logging.L().Infof/Warnf/Errorf (or logging.Stderr()) instead:\n  %s",
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

	// ut-docs#3304 shapes: aliased imports, a dot-import, any other
	// os.Stderr/os.Stdout use, and the print/println builtins.
	write("bad/alias.go", `package bad

import (
	f "fmt"
	osx "os"
)

func a(secret string) {
	f.Println(secret)
	f.Fprintf(osx.Stderr, "%s", secret)
}
`)
	write("bad/dot.go", `package bad

import . "fmt"

func d(secret string) { _ = Sprint(secret) }
`)
	write("bad/raw.go", `package bad

import (
	"io"
	"os"
	"os/exec"
)

func r(secret string) {
	_, _ = os.Stderr.Write([]byte(secret))
	_, _ = os.Stdout.WriteString(secret)
	_, _ = io.WriteString(os.Stderr, secret)
	w := os.Stderr
	_ = w
	cmd := exec.Command("x")
	cmd.Stderr = os.Stdout
	print(secret)
	println(secret)
}
`)
	write("ok/shadow.go", `package ok

import _ "os"

func print(s string) string { return s }

func s() string {
	// os.Stderr.Write in a comment; println("in a comment")
	return print("os.Stderr")
}
`)
	write("logging/sink.go", `package logging

import "os"

var sink = os.Stderr

func use() { _, _ = os.Stdout.Write(nil) }
`)
	write("other/logging/nested.go", "package logging\n\nimport \"os\"\n\nvar nested = os.Stderr\n")

	got, err := findStdoutWrites(root)
	if err != nil {
		t.Fatalf("findStdoutWrites: %v", err)
	}
	at := func(file string, line int) string {
		return filepath.Join("bad", file) + ":" + strconv.Itoa(line)
	}
	want := []string{
		at("alias.go", 9),
		at("alias.go", 10),
		at("dot.go", 3),
		at("raw.go", 10),
		at("raw.go", 11),
		at("raw.go", 12),
		at("raw.go", 13),
		at("raw.go", 16),
		at("raw.go", 17),
		at("raw.go", 18),
		at("stderr.go", 9),
		at("stderr.go", 10),
		at("stderr.go", 11),
		at("stderr.go", 12),
		// Only the top-level logging/ dir is the exempt sink.
		filepath.Join("other", "logging", "nested.go") + ":5",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("findStdoutWrites reported\n  %v\nwant\n  %v", got, want)
	}
}

// findStdoutWrites walks root, skipping testdata/ dirs and _test.go files,
// and parses each remaining .go file with go/parser + go/ast — not a regex —
// so a string literal or comment that merely mentions "fmt.Printf" never
// false-triggers. Package selectors are resolved through the file's own
// imports, so an aliased import (f "fmt", osx "os") is caught too. It
// returns "rel/path.go:line" (one per line, in walk order) for:
//   - every fmt.Print/Printf/Println call;
//   - every fmt.Fprint/Fprintf/Fprintln whose writer is os.Stderr/os.Stdout;
//   - a dot-import of fmt or os (its calls could not be told apart);
//   - any reference to os.Stderr or os.Stdout (os.Stderr.Write,
//     io.WriteString(os.Stderr, …), w := os.Stderr, cmd.Stderr = os.Stderr)
//     outside the logging package dir — the redacting sink that wraps them
//     (ut-docs#3304);
//   - a call to the builtin print/println, which writes to stderr unredacted
//     (ut-docs#3304).
func findStdoutWrites(root string) ([]string, error) {
	var violations []string
	seen := map[string]bool{}
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

		// Default mode: object resolution stays on, so a local func named
		// print (Obj != nil) is told apart from the builtin.
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return perr
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		// The logging package itself is the redacting sink around
		// os.Stderr/os.Stdout, so it alone may reference them.
		inLoggingPkg := filepath.Dir(rel) == "logging"

		report := func(pos token.Pos) {
			key := rel + ":" + strconv.Itoa(fset.Position(pos).Line)
			if !seen[key] {
				seen[key] = true
				violations = append(violations, key)
			}
		}

		names := importNames(file)
		for _, imp := range file.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if imp.Name != nil && imp.Name.Name == "." && (p == "fmt" || p == "os") {
				report(imp.Pos())
			}
		}
		isOSStd := func(e ast.Expr) bool {
			return isImportSel(e, names, "os", "Stderr") || isImportSel(e, names, "os", "Stdout")
		}

		ast.Inspect(file, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.SelectorExpr:
				if !inLoggingPkg && isOSStd(n) {
					report(n.Pos())
				}
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok && id.Obj == nil && (id.Name == "print" || id.Name == "println") {
					report(n.Pos())
					return true
				}
				for _, name := range []string{"Print", "Printf", "Println"} {
					if isImportSel(n.Fun, names, "fmt", name) {
						report(n.Pos())
						return true
					}
				}
				for _, name := range []string{"Fprint", "Fprintf", "Fprintln"} {
					if isImportSel(n.Fun, names, "fmt", name) && len(n.Args) > 0 && isOSStd(n.Args[0]) {
						report(n.Pos())
						return true
					}
				}
			}
			return true
		})
		return nil
	})
	return violations, err
}

// importNames maps each file-level local package name to its import path
// (an unnamed import uses the path's last element). Dot and blank imports
// bind no name and are left out.
func importNames(file *ast.File) map[string]string {
	names := map[string]string{}
	for _, imp := range file.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := p[strings.LastIndex(p, "/")+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		if name == "." || name == "_" {
			continue
		}
		names[name] = p
	}
	return names
}

// isImportSel reports whether e is name selected from the package imported
// at importPath (via whatever local name the file gave it). The receiver
// identifier must be unresolved (Obj == nil) — a local variable shadowing
// the package name is not the package.
func isImportSel(e ast.Expr, names map[string]string, importPath, name string) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != name {
		return false
	}
	id, ok := sel.X.(*ast.Ident)
	return ok && id.Obj == nil && names[id.Name] == importPath
}
