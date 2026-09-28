package issuereport

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ut-docs#3092: an unsent bundle (audio, video, screenshots) had no age cap.
// PruneOlderThan drops bundles captured before the cutoff; a newer bundle,
// and anything that isn't a bundle directory, stays.
func TestPruneOlderThan(t *testing.T) {
	withTempPendingDir(t)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-7 * 24 * time.Hour)

	oldID, err := Save("old report", "en", nil, []byte("video-bytes"), nil)
	if err != nil {
		t.Fatal(err)
	}
	newID, err := Save("new report", "en", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	setCreatedAt(t, oldID, now.Add(-8*24*time.Hour))
	setCreatedAt(t, newID, now.Add(-6*24*time.Hour))

	// A directory whose meta.json never got written: aged by its mtime.
	orphanOld := filepath.Join(PendingDir, "orphan-old")
	orphanNew := filepath.Join(PendingDir, "orphan-new")
	for _, d := range []string{orphanOld, orphanNew} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	oldTime := now.Add(-30 * 24 * time.Hour)
	if err := os.Chtimes(orphanOld, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(PendingDir, "stray.txt")
	if err := os.WriteFile(stray, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(stray, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}

	n, freed, err := PruneOlderThan(cutoff)
	if err != nil {
		t.Fatalf("PruneOlderThan: %v", err)
	}
	if n != 2 {
		t.Fatalf("removed %d bundles, want 2 (old report + old orphan)", n)
	}
	if freed < int64(len("video-bytes")) {
		t.Errorf("freed %d bytes, want at least the video's size", freed)
	}
	for _, gone := range []string{filepath.Join(PendingDir, oldID), orphanOld} {
		if _, err := os.Stat(gone); !os.IsNotExist(err) {
			t.Errorf("%s kept, want removed", gone)
		}
	}
	for _, kept := range []string{filepath.Join(PendingDir, newID), orphanNew, stray} {
		if _, err := os.Stat(kept); err != nil {
			t.Errorf("%s removed, want kept", kept)
		}
	}
}

func TestPruneOlderThan_MissingDirIsNotAnError(t *testing.T) {
	withTempPendingDir(t)
	PendingDir = filepath.Join(PendingDir, "never-created")
	if n, _, err := PruneOlderThan(time.Now()); err != nil || n != 0 {
		t.Fatalf("PruneOlderThan = %d, %v; want 0, nil", n, err)
	}
}

func setCreatedAt(t *testing.T, id string, at time.Time) {
	t.Helper()
	meta := readMeta(t, id)
	meta.CreatedAt = at
	if err := writeMetaAtomic(filepath.Join(PendingDir, id, "meta.json"), meta); err != nil {
		t.Fatal(err)
	}
}
