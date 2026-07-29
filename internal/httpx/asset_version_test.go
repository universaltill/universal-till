package httpx

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/paths"
)

// TestAssetVersionReflectsStableDirUpdates guards a real bug: uploaded item/
// variant photos and the receipt logo are written to the stable per-user data
// dir (internal/paths), but assetVersion only ever stat'd the old cwd-relative
// web/ tree. That stat always missed for an uploaded file, so imgv fell back
// to a constant boot-time version for the life of the process — a re-uploaded
// image kept the exact same ?v= query string and browsers kept serving the
// stale cached bytes. Confirmed live 2026-07-29: a second upload of the same
// item's photo did not change on screen until the process restarted.
func TestAssetVersionReflectsStableDirUpdates(t *testing.T) {
	chdirTemp(t)
	stable := t.TempDir()
	paths.Init(stable)
	t.Cleanup(func() { paths.Init("") })

	rel := filepath.Join("public", "assets", "items", "itm1", "thumb.png")
	full := paths.Data(rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}

	v1 := assetVersion(rel)

	// Simulate a re-upload replacing the file with a later mtime.
	later := time.Now().Add(2 * time.Second)
	if err := os.WriteFile(full, []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(full, later, later); err != nil {
		t.Fatal(err)
	}

	v2 := assetVersion(rel)
	if v1 == v2 {
		t.Fatalf("assetVersion did not change after the file was replaced: both returned %q — a re-uploaded image would be served stale from browser cache", v1)
	}
}

// TestAssetVersionFallsBackToReleaseTreeForBuiltinAssets guards the other
// half of the fix: built-in assets (app.css, vendor JS) are never written
// into the stable data dir, so assetVersion must still find them via the old
// cwd-relative web/ path.
func TestAssetVersionFallsBackToReleaseTreeForBuiltinAssets(t *testing.T) {
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(orig); err != nil {
			t.Fatal(err)
		}
	})
	paths.Init(filepath.Join(dir, "stable")) // stable dir has no such file
	t.Cleanup(func() { paths.Init("") })

	rel := filepath.Join("public", "app.css")
	full := filepath.Join(dir, "web", rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("body{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	v := assetVersion(rel)
	if v == "" {
		t.Fatal("expected a non-empty version for an asset present in the release tree")
	}
}
