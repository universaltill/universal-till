package pages

import (
	"os"
	"path/filepath"
	"testing"
)

// The desktop app's WebView can't download an HTTP attachment, so "Download
// backup" copies the file to a local folder instead. This guards that copy:
// the destination file exists with the same bytes, and the dir is created.
func TestCopyBackupTo(t *testing.T) {
	srcDir := t.TempDir()
	name := "unitill-pos-20260716-120000.db"
	want := []byte("SQLite backup bytes")
	if err := os.WriteFile(filepath.Join(srcDir, name), want, 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A not-yet-existing destination dir (mirrors a fresh ~/Downloads).
	dstDir := filepath.Join(t.TempDir(), "Downloads")
	dst, err := copyBackupTo(dstDir, filepath.Join(srcDir, name), name)
	if err != nil {
		t.Fatalf("copyBackupTo: %v", err)
	}
	if dst != filepath.Join(dstDir, name) {
		t.Fatalf("dst = %q, want %q", dst, filepath.Join(dstDir, name))
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read copy: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("copied bytes = %q, want %q", got, want)
	}
}
