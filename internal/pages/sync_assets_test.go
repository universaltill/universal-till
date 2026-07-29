package pages

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/paths"
)

// listItemAssets/safeAssetPath back the LAN primary→replica item-image
// sync surface (GET /api/sync/assets, /api/sync/assets/file). Item image
// uploads write to the stable per-user data dir (paths.Data — fixed
// 2026-07-29 after uploads were found to be lost on every app
// self-update, see catalog/handlers.go). This test guards that the sync
// manifest looks in the SAME place uploads actually land — if it doesn't,
// listItemAssets silently returns an empty manifest forever and replica
// tills never receive item photos, with no error anywhere to notice it by.

func TestListItemAssets_FindsFilesInTheStableDataDir(t *testing.T) {
	orig := paths.DataDir()
	tmp := t.TempDir()
	paths.Init(tmp)
	t.Cleanup(func() { paths.Init(orig) })

	itemDir := filepath.Join(tmp, "public", "assets", "items", "itm001")
	if err := os.MkdirAll(itemDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(itemDir, "thumb.png"), []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	entries, err := listItemAssets()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected the uploaded file to appear in the sync manifest, got %+v", entries)
	}
	if entries[0].Path != "itm001/thumb.png" {
		t.Fatalf("expected path itm001/thumb.png, got %q", entries[0].Path)
	}
	if entries[0].Size != int64(len("fake-png-bytes")) {
		t.Fatalf("expected the real file size, got %d", entries[0].Size)
	}
}

func TestListItemAssets_EmptyTreeIsNotAnError(t *testing.T) {
	orig := paths.DataDir()
	paths.Init(t.TempDir()) // fresh dir, no items/ subtree at all
	t.Cleanup(func() { paths.Init(orig) })

	entries, err := listItemAssets()
	if err != nil {
		t.Fatalf("expected a missing tree to mean 'no images yet', not an error, got %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected no entries, got %+v", entries)
	}
}

func TestSafeAssetPath(t *testing.T) {
	orig := paths.DataDir()
	tmp := t.TempDir()
	paths.Init(tmp)
	t.Cleanup(func() { paths.Init(orig) })

	want := filepath.Join(tmp, "public", "assets", "items", "itm001", "thumb.png")
	got, ok := safeAssetPath("itm001/thumb.png")
	if !ok || got != want {
		t.Fatalf("expected (%q, true), got (%q, %v)", want, got, ok)
	}

	// Path traversal and absolute paths must be rejected outright — this
	// path is reachable from any enrolled replica's bearer token, so a
	// compromised or buggy replica must not be able to read arbitrary
	// files off the primary's disk.
	for _, bad := range []string{
		"", "../../../etc/passwd", "/etc/passwd", "itm001/../../../etc/passwd",
		"itm001\\..\\..\\secret", "..",
	} {
		if _, ok := safeAssetPath(bad); ok {
			t.Fatalf("expected safeAssetPath(%q) to be rejected, got ok=true", bad)
		}
	}
}
