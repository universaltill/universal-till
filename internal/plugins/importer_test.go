package plugins

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- archive builders -------------------------------------------------------

func writeZipArchive(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write zip: %v", err)
	}
}

func writeTarGzArchive(t *testing.T, path string, files map[string][]byte) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data))}); err != nil {
			t.Fatalf("tar header %s: %v", name, err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatalf("tar write %s: %v", name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write tar.gz: %v", err)
	}
}

func validImportManifest(id string) []byte {
	return []byte(`{
		"id": "` + id + `",
		"name": "Import Test",
		"version": "1.0.0",
		"entrypoint": "./run",
		"runtime": "none",
		"canonical_type": "page",
		"device_arch": "any"
	}`)
}

func newTestImporter(t *testing.T) (*Importer, string) {
	t.Helper()
	db := openMarketplaceInstallerDB(t)
	t.Cleanup(func() { db.Close() })
	base := t.TempDir()
	return NewImporter(db, base, nil), base
}

// --- Import behavior --------------------------------------------------------

func TestImportZipHappyPath(t *testing.T) {
	imp, base := newTestImporter(t)
	archive := filepath.Join(t.TempDir(), "plugin.zip")
	writeZipArchive(t, archive, map[string][]byte{
		"manifest.json": validImportManifest("com.test.importzip"),
		"assets/a.txt":  []byte("hello"),
	})

	res, err := imp.Import(context.Background(), &ImportRequest{
		FilePath:      archive,
		TrustLevel:    "untrusted",
		Uploader:      "tester",
		SkipSignature: true,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.PluginID != "com.test.importzip" || res.Version != "1.0.0" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if len(res.Warnings) == 0 || !strings.Contains(res.Warnings[0], "skipped") {
		t.Fatalf("expected dev-mode warning, got %v", res.Warnings)
	}
	// Extracted into the permanent per-version dir.
	if _, err := os.Stat(filepath.Join(base, "com.test.importzip", "1.0.0", "assets", "a.txt")); err != nil {
		t.Fatalf("extracted asset missing: %v", err)
	}
	// Persisted in the DB.
	var count int
	if err := imp.db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.test.importzip'`).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("plugin row count = %d, want 1", count)
	}
}

func TestImportTarGzHappyPathWithFormatDetection(t *testing.T) {
	imp, _ := newTestImporter(t)
	archive := filepath.Join(t.TempDir(), "plugin.tar.gz")
	writeTarGzArchive(t, archive, map[string][]byte{
		"manifest.json": validImportManifest("com.test.importtgz"),
	})

	res, err := imp.Import(context.Background(), &ImportRequest{
		FilePath:      archive, // no Format: detectFormat must pick tar.gz
		SkipSignature: true,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.PluginID != "com.test.importtgz" {
		t.Fatalf("unexpected plugin id %q", res.PluginID)
	}
}

func TestImportValidationErrors(t *testing.T) {
	imp, _ := newTestImporter(t)
	ctx := context.Background()

	if _, err := imp.Import(ctx, &ImportRequest{}); err == nil || !strings.Contains(err.Error(), "file path is required") {
		t.Fatalf("empty path: %v", err)
	}
	if _, err := imp.Import(ctx, &ImportRequest{FilePath: filepath.Join(t.TempDir(), "missing.zip")}); err == nil || !strings.Contains(err.Error(), "failed to stat") {
		t.Fatalf("missing file: %v", err)
	}

	// Oversized archive file rejected before extraction.
	big := filepath.Join(t.TempDir(), "big.zip")
	writeZipArchive(t, big, map[string][]byte{"manifest.json": validImportManifest("com.test.big")})
	if _, err := imp.Import(ctx, &ImportRequest{FilePath: big, MaxSizeBytes: 10, SkipSignature: true}); err == nil || !strings.Contains(err.Error(), "exceeds maximum") {
		t.Fatalf("oversize: %v", err)
	}

	// Archive without a manifest.
	noManifest := filepath.Join(t.TempDir(), "nomanifest.zip")
	writeZipArchive(t, noManifest, map[string][]byte{"readme.txt": []byte("x")})
	if _, err := imp.Import(ctx, &ImportRequest{FilePath: noManifest, SkipSignature: true}); err == nil || !strings.Contains(err.Error(), "manifest.json not found") {
		t.Fatalf("no manifest: %v", err)
	}

	// Unsupported format string.
	bad := filepath.Join(t.TempDir(), "plugin.zip")
	writeZipArchive(t, bad, map[string][]byte{"manifest.json": validImportManifest("com.test.badfmt")})
	if _, err := imp.Import(ctx, &ImportRequest{FilePath: bad, Format: "rar", SkipSignature: true}); err == nil || !strings.Contains(err.Error(), "unsupported archive format") {
		t.Fatalf("bad format: %v", err)
	}
}

func TestImportRejectsZipPathTraversal(t *testing.T) {
	imp, _ := newTestImporter(t)
	archive := filepath.Join(t.TempDir(), "evil.zip")
	writeZipArchive(t, archive, map[string][]byte{
		"../../evil.txt": []byte("escape"),
	})
	_, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, SkipSignature: true})
	if err == nil || !strings.Contains(err.Error(), "illegal file path") {
		t.Fatalf("zip traversal not rejected: %v", err)
	}
}

func TestImportRejectsTarPathTraversal(t *testing.T) {
	imp, _ := newTestImporter(t)
	archive := filepath.Join(t.TempDir(), "evil.tar.gz")
	writeTarGzArchive(t, archive, map[string][]byte{
		"../../evil.txt": []byte("escape"),
	})
	_, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, SkipSignature: true})
	if err == nil || !strings.Contains(err.Error(), "illegal file path") {
		t.Fatalf("tar traversal not rejected: %v", err)
	}
}

// TDD (batch 5): the archive FILE size was checked, but nothing bounded what
// it decompressed TO — a small gzip/zip bomb could fill the till's disk before
// checkDiskBudget (which walks the tree only AFTER extraction) ever ran.
// Proven red against the pre-fix extractors: with the cap lowered to 64KB, a
// ~5KB archive expanding to 1MB extracted successfully; now it must fail
// loudly, for both formats.
func TestImportRejectsDecompressionBomb(t *testing.T) {
	bomb := bytes.Repeat([]byte{0}, 1024*1024) // 1MB of zeros, compresses tiny

	t.Run("tar.gz", func(t *testing.T) {
		imp, _ := newTestImporter(t)
		imp.maxExtractedBytes = 64 * 1024
		archive := filepath.Join(t.TempDir(), "bomb.tar.gz")
		writeTarGzArchive(t, archive, map[string][]byte{
			"manifest.json": validImportManifest("com.test.bombtgz"),
			"bomb.bin":      bomb,
		})
		_, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, SkipSignature: true})
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("tar.gz bomb not rejected: %v", err)
		}
	})

	t.Run("zip", func(t *testing.T) {
		imp, _ := newTestImporter(t)
		imp.maxExtractedBytes = 64 * 1024
		archive := filepath.Join(t.TempDir(), "bomb.zip")
		writeZipArchive(t, archive, map[string][]byte{
			"manifest.json": validImportManifest("com.test.bombzip"),
			"bomb.bin":      bomb,
		})
		_, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, SkipSignature: true})
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("zip bomb not rejected: %v", err)
		}
	})
}

func TestDetectFormat(t *testing.T) {
	cases := map[string]ImportFormat{
		"plugin.zip":      ImportFormatZip,
		"PLUGIN.ZIP":      ImportFormatZip,
		"plugin.tar.gz":   ImportFormatTarGz,
		"plugin.tgz.gz":   ImportFormatTarGz,
		"plugin.utplugin": ImportFormatTarGz,
		"plugin":          ImportFormatTarGz,
	}
	for path, want := range cases {
		if got := detectFormat(path); got != want {
			t.Errorf("detectFormat(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestCheckDiskBudgetRejectsOversizedPluginTree(t *testing.T) {
	imp, base := newTestImporter(t)

	// Existing installed version with a sparse ~700MB file (fast to create;
	// Walk uses info.Size(), not blocks on disk).
	existing := filepath.Join(base, "com.test.budget", "1.0.0")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	f, err := os.Create(filepath.Join(existing, "big.bin"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := f.Truncate(700 * 1024 * 1024); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	f.Close()

	// New extraction of another sparse ~700MB: 1.4GB total > 1GB budget.
	incoming := t.TempDir()
	nf, err := os.Create(filepath.Join(incoming, "new.bin"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := nf.Truncate(700 * 1024 * 1024); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	nf.Close()

	if err := imp.checkDiskBudget("com.test.budget", "1.1.0", incoming); err == nil || !strings.Contains(err.Error(), "1 GB limit") {
		t.Fatalf("budget not enforced: %v", err)
	}

	// Small tree passes.
	small := t.TempDir()
	if err := os.WriteFile(filepath.Join(small, "ok.txt"), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := imp.checkDiskBudget("com.test.other", "1.0.0", small); err != nil {
		t.Fatalf("small tree rejected: %v", err)
	}
}
