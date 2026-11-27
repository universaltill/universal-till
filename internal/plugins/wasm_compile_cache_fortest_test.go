package plugins

import "testing"

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
