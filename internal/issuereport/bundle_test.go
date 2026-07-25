package issuereport

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func withTempPendingDir(t *testing.T) {
	t.Helper()
	orig := PendingDir
	PendingDir = t.TempDir()
	t.Cleanup(func() { PendingDir = orig })
}

func TestSaveRequiresAudio(t *testing.T) {
	withTempPendingDir(t)
	if _, err := Save("note", nil, nil); err == nil {
		t.Fatal("expected error when audio is empty")
	}
}

func TestSaveThenPendingRoundTrip(t *testing.T) {
	withTempPendingDir(t)
	id, err := Save("printer jammed", []byte("fake-audio"), []byte("fake-video"))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if id == "" {
		t.Fatal("expected a non-empty id")
	}

	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 pending bundle, got %d", len(bundles))
	}
	b := bundles[0]
	if b.Meta.ID != id {
		t.Errorf("Meta.ID = %q, want %q", b.Meta.ID, id)
	}
	if b.Meta.Note != "printer jammed" {
		t.Errorf("Meta.Note = %q, want %q", b.Meta.Note, "printer jammed")
	}
	if b.AudioPath == "" {
		t.Error("expected AudioPath to be set")
	}
	if b.VideoPath == "" {
		t.Error("expected VideoPath to be set when a video was captured")
	}
}

func TestSaveWithoutVideoLeavesVideoPathEmpty(t *testing.T) {
	withTempPendingDir(t)
	if _, err := Save("", []byte("fake-audio"), nil); err != nil {
		t.Fatalf("Save: %v", err)
	}
	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 1 {
		t.Fatalf("expected 1 pending bundle, got %d", len(bundles))
	}
	if bundles[0].VideoPath != "" {
		t.Errorf("VideoPath = %q, want empty (no screen recording captured)", bundles[0].VideoPath)
	}
}

func TestPendingOrdersOldestFirst(t *testing.T) {
	withTempPendingDir(t)
	first, err := Save("first", []byte("a"), nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Force a distinguishable timestamp — two Saves in the same test can
	// land in the same nanosecond-resolution instant on a fast machine.
	time.Sleep(2 * time.Millisecond)
	second, err := Save("second", []byte("a"), nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 2 {
		t.Fatalf("expected 2 pending bundles, got %d", len(bundles))
	}
	if bundles[0].Meta.ID != first || bundles[1].Meta.ID != second {
		t.Errorf("expected oldest-first order [%s, %s], got [%s, %s]",
			first, second, bundles[0].Meta.ID, bundles[1].Meta.ID)
	}
}

func TestDiscardRemovesBundle(t *testing.T) {
	withTempPendingDir(t)
	id, err := Save("note", []byte("a"), nil)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := Discard(id); err != nil {
		t.Fatalf("Discard: %v", err)
	}
	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending: %v", err)
	}
	if len(bundles) != 0 {
		t.Fatalf("expected 0 pending bundles after Discard, got %d", len(bundles))
	}
}

// A write failure partway through Save (e.g. disk full on the video file,
// after audio already succeeded) must not leave an orphaned bundle
// directory behind — one with no meta.json, invisible to Pending() and so
// never reachable by Discard() either.
func TestSaveCleansUpDirectoryOnWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission semantics differ on Windows")
	}
	withTempPendingDir(t)

	origID := newBundleID
	newBundleID = func() string { return "fixed-id-for-test" }
	t.Cleanup(func() { newBundleID = origID })

	// Pre-create the bundle directory read-only, so Save's os.MkdirAll is a
	// no-op (already exists) and the first file write inside it fails.
	dir := filepath.Join(PendingDir, "fixed-id-for-test")
	if err := os.MkdirAll(dir, 0o500); err != nil {
		t.Fatalf("pre-create read-only dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	if _, err := Save("note", []byte("audio"), nil); err == nil {
		t.Fatal("expected Save to fail on a read-only bundle directory")
	}

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected the failed bundle directory to be cleaned up, stat err = %v", err)
	}
}

func TestPendingOnMissingDirReturnsEmpty(t *testing.T) {
	withTempPendingDir(t)
	PendingDir = PendingDir + "/does-not-exist"
	bundles, err := Pending()
	if err != nil {
		t.Fatalf("Pending on missing dir: %v", err)
	}
	if len(bundles) != 0 {
		t.Fatalf("expected 0 bundles, got %d", len(bundles))
	}
}
