package plugins

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeVersionDir(t *testing.T, base, pluginID, version string, withManifest bool) string {
	t.Helper()
	dir := filepath.Join(base, pluginID, "versions", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if withManifest {
		manifest := `{
			"id": "` + pluginID + `",
			"name": "RB",
			"version": "` + version + `",
			"entrypoint": "./run",
			"runtime": "none",
			"canonical_type": "page",
			"device_arch": "any",
			"entries": [
				{"type": "page", "key": "rb-page-` + version + `", "label": "RB ` + version + `", "route": "/rb", "menu_group": "main"}
			]
		}`
		if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o644); err != nil {
			t.Fatalf("write manifest: %v", err)
		}
	}
	return dir
}

func TestRollbackFullArc(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.rb"

	// Installed at 2.0.0; 1.0.0 kept on disk for rollback.
	seedInstalledPlugin(t, db, pluginID, "RB", "2.0.0", "none", true)
	writeVersionDir(t, base, pluginID, "1.0.0", true)
	writeVersionDir(t, base, pluginID, "2.0.0", true)

	history, err := rm.GetVersionHistory(ctx, pluginID)
	if err != nil {
		t.Fatalf("GetVersionHistory: %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history len = %d", len(history))
	}
	activeSeen := false
	for _, v := range history {
		if v.Version == "2.0.0" && v.IsActive {
			activeSeen = true
		}
		if v.Version == "1.0.0" && v.IsActive {
			t.Fatalf("inactive version flagged active")
		}
	}
	if !activeSeen {
		t.Fatalf("active version not flagged: %+v", history)
	}

	// Error paths first.
	if err := rm.Rollback(ctx, pluginID, "9.9.9", "tester"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("missing target: %v", err)
	}
	if err := rm.Rollback(ctx, pluginID, "2.0.0", "tester"); err == nil || !strings.Contains(err.Error(), "already at version") {
		t.Fatalf("same version: %v", err)
	}
	writeVersionDir(t, base, pluginID, "0.5.0", false) // dir exists, no manifest
	if err := rm.Rollback(ctx, pluginID, "0.5.0", "tester"); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("missing manifest: %v", err)
	}

	// The real rollback. plugins.version has an FK to plugin_catalog(id,version).
	seedCatalogRow(t, db, pluginID, "RB", "1.0.0", "")
	if err := rm.Rollback(ctx, pluginID, "1.0.0", ""); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	var version string
	if err := db.QueryRow(`SELECT version FROM plugins WHERE id = ?`, pluginID).Scan(&version); err != nil {
		t.Fatalf("query: %v", err)
	}
	if version != "1.0.0" {
		t.Fatalf("version after rollback = %q", version)
	}

	// Entries replaced from the rolled-back manifest.
	var entryKey string
	if err := db.QueryRow(`SELECT key FROM plugin_entries WHERE plugin_id = ?`, pluginID).Scan(&entryKey); err != nil {
		t.Fatalf("query entries: %v", err)
	}
	if entryKey != "rb-page-1.0.0" {
		t.Fatalf("entry key = %q", entryKey)
	}

	// Audited.
	var auditCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'plugin_rollback' AND entity_id = ?`, pluginID).Scan(&auditCount); err != nil {
		t.Fatalf("query audit: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("audit rows = %d", auditCount)
	}
}

func TestRollbackWithoutActiveVersion(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	writeVersionDir(t, base, "com.test.ghost", "1.0.0", true)
	err := rm.Rollback(context.Background(), "com.test.ghost", "1.0.0", "tester")
	if err == nil || !strings.Contains(err.Error(), "no active version") {
		t.Fatalf("ghost plugin: %v", err)
	}
}

func TestGetVersionHistoryNoDirectory(t *testing.T) {
	db := managerTestDB(t)
	rm := NewRollbackManager(db, t.TempDir())
	history, err := rm.GetVersionHistory(context.Background(), "com.test.none")
	if err != nil {
		t.Fatalf("GetVersionHistory: %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("expected empty history, got %+v", history)
	}
}

func TestStoreVersionCleansUpOldVersions(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.cleanup"

	// Four existing versions with distinct mtimes, oldest first.
	now := time.Now()
	for i, v := range []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0"} {
		dir := writeVersionDir(t, base, pluginID, v, false)
		mtime := now.Add(time.Duration(i-10) * time.Hour)
		if err := os.Chtimes(dir, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	// Storing a fifth trips the cleanup (keep max 3, delete oldest).
	if err := rm.StoreVersion(pluginID, "1.4.0", ""); err != nil {
		t.Fatalf("StoreVersion: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(base, pluginID, "versions"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if len(entries) != 3 {
		t.Fatalf("kept %d versions, want 3: %v", len(entries), names)
	}
	if names["1.0.0"] || names["1.1.0"] {
		t.Fatalf("oldest versions not cleaned: %v", names)
	}
	if !names["1.4.0"] || !names["1.3.0"] {
		t.Fatalf("newest versions missing: %v", names)
	}
}

// ut-docs#495: StoreVersion's own comment used to say "we assume sourcePath
// is already in the correct location... In a full implementation, you'd
// copy files here" — it never actually snapshotted anything. A real
// sourcePath must now be copied (including a nested subdirectory, not just
// top-level files) so a later Rollback has real files to restore from.
func TestStoreVersion_CopiesFilesFromSourcePath(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.snapshot"

	// A live per-version install dir, the shape installer_marketplace.go's
	// installBundleFile leaves behind: pluginBaseDir/pluginID/version/...,
	// NOT pluginBaseDir/pluginID/versions/version/ (RollbackManager's own,
	// separate snapshot tree) — StoreVersion's job is to bridge the two.
	sourceDir := filepath.Join(base, pluginID, "1.0.0")
	if err := os.MkdirAll(filepath.Join(sourceDir, "assets"), 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "manifest.json"), []byte(`{"id":"com.test.snapshot","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "assets", "icon.png"), []byte("fake-png-bytes"), 0o644); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	if err := rm.StoreVersion(pluginID, "1.0.0", sourceDir); err != nil {
		t.Fatalf("StoreVersion: %v", err)
	}

	snapshotDir := filepath.Join(base, pluginID, "versions", "1.0.0")
	manifestBytes, err := os.ReadFile(filepath.Join(snapshotDir, "manifest.json"))
	if err != nil {
		t.Fatalf("snapshot manifest missing: %v", err)
	}
	if string(manifestBytes) != `{"id":"com.test.snapshot","version":"1.0.0"}` {
		t.Fatalf("snapshot manifest content = %q", manifestBytes)
	}
	assetBytes, err := os.ReadFile(filepath.Join(snapshotDir, "assets", "icon.png"))
	if err != nil {
		t.Fatalf("snapshot nested asset missing: %v", err)
	}
	if string(assetBytes) != "fake-png-bytes" {
		t.Fatalf("snapshot asset content = %q", assetBytes)
	}

	// The live source dir must be left untouched — StoreVersion snapshots,
	// it doesn't move.
	if _, err := os.Stat(filepath.Join(sourceDir, "manifest.json")); err != nil {
		t.Fatalf("source dir must survive the copy: %v", err)
	}
}
