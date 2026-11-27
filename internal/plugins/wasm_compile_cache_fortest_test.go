package plugins

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestShareCompileCacheForTest_RestoresAndSkipsDiskCache(t *testing.T) {
	dir, cacheDir := t.TempDir(), t.TempDir()
	if w := newWasmRuntimeWithCacheFor(dir, cacheDir, "linux"); w.cacheDir != cacheDir {
		t.Fatalf("without the seam cacheDir = %q, want %q", w.cacheDir, cacheDir)
	}
	restore := ShareCompileCacheForTest()
	t.Cleanup(restore)
	if sharedCompileCacheForTest == nil {
		t.Fatal("seam not set")
	}
	w := newWasmRuntimeWithCacheFor(dir, cacheDir, "linux")
	if w.cacheDir != "" {
		t.Fatalf("with the seam cacheDir = %q, want empty (disk cache skipped)", w.cacheDir)
	}
	rt := newPluginRuntime(nil)
	if rt == nil {
		t.Fatal("runtime not built")
	}
	_ = rt.Close(t.Context())
	restore()
	if sharedCompileCacheForTest != nil {
		t.Fatal("restore did not clear the seam")
	}
}

// ShareCompileCacheForTest is exported from a non-_test file only because a
// TestMain in another package calls it; production code must never do so
// (ut-docs#7091 review). Pin that: only *_test.go files may name it.
func TestShareCompileCacheForTest_OnlyCalledFromTests(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".claude", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") ||
			filepath.Base(p) == "wasm_compile_cache_fortest.go" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "ShareCompileCacheForTest(") {
			t.Errorf("%s calls ShareCompileCacheForTest outside a _test.go file", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
