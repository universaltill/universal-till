package entitlement

import (
	"go/build"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const modulePath = "github.com/universaltill/universal-till"

// salePathPackages are the sale/receipt path: basket, tender, fiscal
// signing, receipt printing, EOD. ADR-0060 §5 ("never a sale gate,
// structurally") and §7 (core POS is never behind EffectivePlan) forbid any
// of them from reading the entitlement cache, so none may import this
// package, directly or transitively.
var salePathPackages = []string{
	modulePath + "/internal/pos",
	modulePath + "/internal/fiscal",
	modulePath + "/internal/print",
}

const entitlementPkg = modulePath + "/internal/entitlement"

// saleHandlerFiles are the HTTP entry points of the sale path, which live in
// internal/pages alongside surfaces that legitimately read EffectivePlan
// (the ADR-0060 §6 status chip, settings) — so the package as a whole may
// import internal/entitlement, but these files may not. Review finding on
// ut-docs#2547: the package-level walk above cannot see this layer.
var saleHandlerFiles = []string{
	"internal/pages/pos_api.go",     // basket, tender, CompleteSale
	"internal/pages/refund_page.go", // refunds
	"internal/pages/sync_sales.go",  // LAN sale sync
	"internal/pages/eod_api.go",     // end of day
	"internal/pages/eod_fiscal_device.go",
}

// fileImports reports whether the Go file at path imports pkg.
func fileImports(t *testing.T, path, pkg string) bool {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	for _, imp := range f.Imports {
		if strings.Trim(imp.Path.Value, `"`) == pkg {
			return true
		}
	}
	return false
}

// repoRoot walks up from this package's directory to the go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above test directory")
		}
		dir = parent
	}
}

// importPath returns the chain of in-module (non-test) imports from `from`
// to `target`, or nil when target is unreachable. Only this module's own
// packages are walked: stdlib and third-party code cannot import an
// internal package of ours. Parsing source with go/build (no `go list`
// subprocess) keeps the test hermetic.
func importPath(t *testing.T, root, from, target string) []string {
	t.Helper()
	parent := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur == target {
			var chain []string
			for p := cur; p != ""; p = parent[p] {
				chain = append([]string{p}, chain...)
			}
			return chain
		}
		dir := filepath.Join(root, strings.TrimPrefix(cur, modulePath))
		pkg, err := build.ImportDir(dir, 0)
		if err != nil {
			if _, ok := err.(*build.NoGoError); ok {
				continue
			}
			t.Fatalf("parse %s: %v", cur, err)
		}
		for _, imp := range pkg.Imports {
			if !strings.HasPrefix(imp, modulePath+"/") {
				continue
			}
			if _, seen := parent[imp]; seen {
				continue
			}
			parent[imp] = cur
			queue = append(queue, imp)
		}
	}
	return nil
}

func TestSalePathNeverImportsEntitlement(t *testing.T) {
	root := repoRoot(t)
	for _, pkg := range salePathPackages {
		if _, err := os.Stat(filepath.Join(root, strings.TrimPrefix(pkg, modulePath))); err != nil {
			t.Fatalf("sale-path package %s not found (%v) — update salePathPackages if it moved", pkg, err)
		}
		if chain := importPath(t, root, pkg, entitlementPkg); chain != nil {
			t.Errorf("ADR-0060 §5/§7: sale-path package %s imports internal/entitlement:\n  %s",
				pkg, strings.Join(chain, "\n  -> "))
		}
	}
}

// Positive control: the walker must actually find a real edge, or the test
// above would pass vacuously. internal/cloudsync is the cache's writer.
func TestImportWalkerFindsKnownEdge(t *testing.T) {
	root := repoRoot(t)
	if importPath(t, root, modulePath+"/internal/cloudsync", entitlementPkg) == nil {
		t.Fatal("import walker did not find internal/cloudsync -> internal/entitlement; the sale-path guard would be vacuous")
	}
}

func TestSaleHandlerFilesNeverImportEntitlement(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range saleHandlerFiles {
		path := filepath.Join(root, rel)
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("sale handler file %s not found (%v) — update saleHandlerFiles if it moved", rel, err)
		}
		if fileImports(t, path, entitlementPkg) {
			t.Errorf("ADR-0060 §5/§7: sale handler %s imports internal/entitlement — the sale path must never read EffectivePlan", rel)
		}
	}
}

// Positive control for the file-level check.
func TestFileImportsDetectsEntitlement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.go")
	src := "package pages\n\nimport _ \"" + entitlementPkg + "\"\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	if !fileImports(t, path, entitlementPkg) {
		t.Fatal("fileImports missed a direct import; the sale-handler guard would be vacuous")
	}
}
