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

// TestNoStdoutPrintCalls is a regression guard for ut-docs#2728: a
// fmt.Print/Printf/Println call anywhere under internal/ goes to stdout
// only, so on a Windows GUI launch (invalid stdout handle) it never reaches
// till.log — use logging.L() (Infof/Warnf/Errorf) instead, which is routed
// through the redacted, rotated log file (internal/logging/file.go).
//
// Walks the internal/ tree (relative ".." from this package's directory),
// skipping testdata/ dirs and _test.go files, and parses each remaining .go
// file with go/parser + go/ast — not a regex — so a string literal or
// comment that merely mentions "fmt.Printf" never false-triggers.
func TestNoStdoutPrintCalls(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve internal/ root: %v", err)
	}

	var violations []string
	fset := token.NewFileSet()

	err = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
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
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkgIdent, ok := sel.X.(*ast.Ident)
			if !ok || pkgIdent.Name != "fmt" {
				return true
			}
			switch sel.Sel.Name {
			case "Print", "Printf", "Println":
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				pos := fset.Position(call.Pos())
				violations = append(violations, rel+":"+strconv.Itoa(pos.Line))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/ tree: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("fmt.Print/Printf/Println writes to stdout only and is lost on a Windows GUI launch (ut-docs#2728) — use logging.L().Infof/Warnf/Errorf instead:\n  %s",
			strings.Join(violations, "\n  "))
	}
}
