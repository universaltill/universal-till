package pages

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

// exemptMarker is the reviewed escape hatch for a pos.RecordCashAdjustment
// call site that genuinely isn't a live completion — same convention
// scripts/ci/guard-i18n.sh (`i18n:ignore`) and guard-compliance-claims.sh
// (`compliance-claim:allow`) already use, placed on the call's own line.
var exemptMarker = regexp.MustCompile(`//.*\bfiscal-gate:exempt\b`)

// isRecordCashAdjustmentCall reports whether n is a call to
// pos.RecordCashAdjustment. Matched on the parsed AST rather than by
// grepping source text, so a mention inside a doc comment or a string
// literal can't trip the guard (a false positive would push the next
// author toward the escape hatch for no reason, which is exactly how a
// guard stops being trusted).
func isRecordCashAdjustmentCall(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "RecordCashAdjustment" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "pos"
}

// isFiscalGateCall reports whether n is a call to either ADR-0048 gate
// helper — enforceFiscalGate (the shared one in pos_api.go) or
// enforceCashAdjustmentFiscalGate (shifts_api.go's cash-payout wrapper
// around it).
func isFiscalGateCall(n ast.Node) bool {
	call, ok := n.(*ast.CallExpr)
	if !ok {
		return false
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return false
	}
	return id.Name == "enforceFiscalGate" || id.Name == "enforceCashAdjustmentFiscalGate"
}

// TestFiscalGate_EveryRecordCashAdjustmentCallSiteIsGated is the guard
// ut-docs#998 asked for: the ADR-0048 hard gate had been discovered by
// "does this handler call pos.CompleteSale", and RecordCashAdjustment moves
// real money out of the drawer without ever calling CompleteSale — so it
// stayed ungated until this card, found only by a human re-reading the
// package rather than by anything that would fail loudly. Rather than
// trust the next reviewer to think of it again for a THIRD money-moving
// entry point, this walks every non-test source file in the package and
// fails on a call to pos.RecordCashAdjustment whose OWN ENCLOSING FUNCTION
// doesn't also call the fiscal gate.
//
// Function scope, not file scope, is the whole point: both gated handlers
// live in shifts_api.go, so a file-level check would keep passing if a
// third payout handler were added to that same file and forgot the gate —
// which is by far the likeliest way this regresses (ut-docs#998 review).
// A genuinely exempt call site (none exist today) can add
// `// fiscal-gate:exempt <reason>` on the call's own line.
func TestFiscalGate_EveryRecordCashAdjustmentCallSiteIsGated(t *testing.T) {
	// Resolve this test's own directory rather than relying on the process
	// working directory — chdirRoot (ui_smoke_test.go) is called by many
	// other tests in this package and, since Go tests in one package share
	// a process, permanently changes cwd to the repo root for the rest of
	// the run in whatever order tests happen to execute.
	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}

	const remedy = "pos.RecordCashAdjustment is called with no ADR-0048 fiscal gate in the same function " +
		"(no call to enforceFiscalGate/enforceCashAdjustmentFiscalGate) — either wire the gate in " +
		"(see shifts_api.go's RecordCashAdjustment/PfandRueckgabe for the pattern) or mark this exact " +
		"call line `// fiscal-gate:exempt <reason>` if it's genuinely not a live completion (e.g. a " +
		"replay of an already-completed entry)"

	checked := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, f, raw, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		srcLines := strings.Split(string(raw), "\n")
		exemptAt := func(pos token.Pos) bool {
			line := fset.Position(pos).Line
			if line < 1 || line > len(srcLines) {
				return false
			}
			return exemptMarker.MatchString(srcLines[line-1])
		}

		// Every call site in the file, so one sitting outside any function
		// body (a package-level var initializer) can't slip through the
		// per-function walk below unnoticed.
		allCalls := map[token.Pos]bool{}
		ast.Inspect(parsed, func(n ast.Node) bool {
			if isRecordCashAdjustmentCall(n) {
				allCalls[n.Pos()] = true
			}
			return true
		})
		if len(allCalls) == 0 {
			continue
		}
		checked += len(allCalls)

		seen := map[token.Pos]bool{}
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			var calls []token.Pos
			gated := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if isRecordCashAdjustmentCall(n) {
					calls = append(calls, n.Pos())
				}
				if isFiscalGateCall(n) {
					gated = true
				}
				return true
			})
			for _, pos := range calls {
				seen[pos] = true
				if gated || exemptAt(pos) {
					continue
				}
				t.Errorf("%s: func %s: %s", fset.Position(pos), fn.Name.Name, remedy)
			}
		}
		for pos := range allCalls {
			if seen[pos] || exemptAt(pos) {
				continue
			}
			t.Errorf("%s: %s (this call is outside any function body)", fset.Position(pos), remedy)
		}
	}
	if checked == 0 {
		t.Fatal("no file in internal/pages calls pos.RecordCashAdjustment — this guard has nothing to check; " +
			"if the function was renamed/moved, update this test rather than letting it go silently green")
	}
}
