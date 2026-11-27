package plugins

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tetratelabs/wazero"
)

func TestSharedCompileCacheForTest_SkipsDiskCache(t *testing.T) {
	dir, cacheDir := t.TempDir(), t.TempDir()
	if w := newWasmRuntimeWithCacheFor(dir, cacheDir, "linux"); w.cacheDir != cacheDir {
		t.Fatalf("without the seam cacheDir = %q, want %q", w.cacheDir, cacheDir)
	}
	SharedCompileCacheForTest = wazero.NewCompilationCache()
	t.Cleanup(func() { SharedCompileCacheForTest = nil })
	w := newWasmRuntimeWithCacheFor(dir, cacheDir, "linux")
	if w.cacheDir != "" {
		t.Fatalf("with the seam cacheDir = %q, want empty (disk cache skipped)", w.cacheDir)
	}
	rt := newPluginRuntime(nil)
	if rt == nil {
		t.Fatal("runtime not built")
	}
	_ = rt.Close(t.Context())
	SharedCompileCacheForTest = nil
	if w := newWasmRuntimeWithCacheFor(dir, cacheDir, "linux"); w.cacheDir != cacheDir {
		t.Fatalf("after clearing the seam cacheDir = %q, want %q", w.cacheDir, cacheDir)
	}
}

// SharedCompileCacheForTest is exported from a non-_test file only because a
// TestMain in another package sets it; production code must never do so
// (ut-docs#7091 review). Pin that: outside its own two files, only *_test.go
// files may name it.
func TestSharedCompileCacheForTest_OnlySetFromTests(t *testing.T) {
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
			filepath.Base(p) == "wasm_compile_cache_fortest.go" || filepath.Base(p) == "wasm_compile_cache.go" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "SharedCompileCacheForTest") {
			t.Errorf("%s names SharedCompileCacheForTest outside a _test.go file", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
