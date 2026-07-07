package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// TestExportImportRoundTrip proves an exported bundle re-imports cleanly on a
// fresh base dir + DB — the offline provisioning path (export on one till,
// import on an offline till without the marketplace).
func TestExportImportRoundTrip(t *testing.T) {
	ctx := context.Background()

	// Lay down an "installed" plugin at {base}/{id}/{version}/.
	srcBase := t.TempDir()
	id, version := "com.test.export", "2.1.0"
	pluginDir := filepath.Join(srcBase, id, version)
	manifest := `{"id":"com.test.export","name":"Export Test","version":"2.1.0","entrypoint":"./bin/app","runtime":"go"}`
	writeTestFile(t, filepath.Join(pluginDir, "manifest.json"), manifest)
	writeTestFile(t, filepath.Join(pluginDir, "bin", "app"), "binary-bytes")
	writeTestFile(t, filepath.Join(pluginDir, "content", "en.json"), `{"hello":"world"}`)

	// Export -> bundle.
	bundlePath := filepath.Join(t.TempDir(), "export.tar.gz")
	res, err := NewExporter(srcBase).Export(ctx, &ExportRequest{PluginID: id, Version: version, DestPath: bundlePath})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if res.Size == 0 || len(res.SHA256) != 64 {
		t.Fatalf("unexpected export result: %+v", res)
	}
	if _, err := os.Stat(bundlePath); err != nil {
		t.Fatalf("bundle not written: %v", err)
	}

	// Import into a *fresh* base dir + DB, simulating a different offline till.
	db := setupTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS audit_log (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		action TEXT NOT NULL, entity_type TEXT, entity_id TEXT,
		data_json TEXT, created_at TEXT NOT NULL)`); err != nil {
		t.Fatalf("create audit_log: %v", err)
	}

	destBase := t.TempDir()
	imp := NewImporter(db, destBase, nil) // nil verifier + SkipSignature (offline dev)
	ir, err := imp.Import(ctx, &ImportRequest{
		FilePath:      bundlePath,
		Format:        ImportFormatTarGz,
		TrustLevel:    "untrusted",
		Uploader:      "offline-provision",
		SkipSignature: true,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if ir.PluginID != id || ir.Version != version {
		t.Fatalf("imported manifest mismatch: %+v", ir)
	}

	// Files round-tripped to the new base dir, contents intact.
	for _, rel := range []string{"manifest.json", "bin/app", "content/en.json"} {
		if _, err := os.Stat(filepath.Join(destBase, id, version, rel)); err != nil {
			t.Errorf("missing after import: %s (%v)", rel, err)
		}
	}
	if got := readTestFile(t, filepath.Join(destBase, id, version, "content", "en.json")); got != `{"hello":"world"}` {
		t.Errorf("content corrupted after round-trip: %q", got)
	}

	// DB row persisted on import.
	var gotID string
	if err := db.QueryRowContext(ctx, `SELECT id FROM plugins WHERE id = ?`, id).Scan(&gotID); err != nil {
		t.Fatalf("plugin not persisted: %v", err)
	}
	if gotID != id {
		t.Errorf("persisted id = %q, want %q", gotID, id)
	}

	// The recorded bundle checksum matches the file on disk.
	if onDisk, err := ComputeSHA256(bundlePath); err != nil {
		t.Fatalf("ComputeSHA256: %v", err)
	} else if onDisk != res.SHA256 {
		t.Errorf("export checksum %s != on-disk %s", res.SHA256, onDisk)
	}
}

func TestExport_Validation(t *testing.T) {
	e := NewExporter(t.TempDir())
	ctx := context.Background()
	out := filepath.Join(t.TempDir(), "o.tar.gz")

	if _, err := e.Export(ctx, &ExportRequest{Version: "1", DestPath: out}); err == nil {
		t.Error("expected error for missing plugin id")
	}
	if _, err := e.Export(ctx, &ExportRequest{PluginID: "a", Version: "1", DestPath: ""}); err == nil {
		t.Error("expected error for missing dest path")
	}
	if _, err := e.Export(ctx, &ExportRequest{PluginID: "nope", Version: "9", DestPath: out}); err == nil {
		t.Error("expected error for missing plugin dir")
	}
}

func TestExport_RejectsPathTraversal(t *testing.T) {
	base := t.TempDir()
	// Plant a manifest OUTSIDE the base to prove traversal can't reach it.
	outside := filepath.Dir(base)
	writeTestFile(t, filepath.Join(outside, "manifest.json"), `{"id":"x","version":"1"}`)

	e := NewExporter(filepath.Join(base, "plugins"))
	_, err := e.Export(context.Background(), &ExportRequest{
		PluginID: "..", Version: "..", DestPath: filepath.Join(t.TempDir(), "o.tar.gz"),
	})
	if err == nil {
		t.Fatal("expected path-traversal rejection")
	}
}

func TestExport_MissingManifest(t *testing.T) {
	base := t.TempDir()
	writeTestFile(t, filepath.Join(base, "com.x", "1.0.0", "bin", "app"), "x") // no manifest.json
	_, err := NewExporter(base).Export(context.Background(), &ExportRequest{
		PluginID: "com.x", Version: "1.0.0", DestPath: filepath.Join(t.TempDir(), "o.tar.gz"),
	})
	if err == nil {
		t.Error("expected error when manifest.json is missing")
	}
}
