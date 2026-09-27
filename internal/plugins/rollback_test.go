package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
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

// ut-docs#2239 review: rolling back must not silently destroy the ability to
// roll forward again. Rollback only ever restores from a versions/ snapshot
// (never from the live per-version install dir), so a version reachable ONLY
// through its live dir — never itself rolled away from — would vanish the
// moment Rollback switches away from it, unless Rollback snapshots it first.
func TestRollback_PreservesRollForwardToTheVersionItLeaves(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.forward"

	// 1.0.0 has a versions/ snapshot (rollback TARGET must have one).
	writeVersionDir(t, base, pluginID, "1.0.0", true)

	// 2.0.0 is the live, active install — a real per-version dir installed
	// by the marketplace installer, but never itself snapshotted into
	// versions/ (it was a straight fresh install, never rolled away from).
	liveDir := filepath.Join(base, pluginID, "2.0.0")
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatalf("mkdir live dir: %v", err)
	}
	manifest := `{"id":"` + pluginID + `","name":"FWD","version":"2.0.0","entrypoint":"./run","runtime":"none","canonical_type":"page","device_arch":"any","entries":[]}`
	if err := os.WriteFile(filepath.Join(liveDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write live manifest: %v", err)
	}
	seedInstalledPlugin(t, db, pluginID, "FWD", "2.0.0", "none", true)
	seedCatalogRow(t, db, pluginID, "FWD", "1.0.0", "")

	ctx := context.Background()
	if err := rm.Rollback(ctx, pluginID, "1.0.0", "tester"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	history, err := rm.GetVersionHistory(ctx, pluginID)
	if err != nil {
		t.Fatalf("GetVersionHistory: %v", err)
	}
	var found2 bool
	for _, v := range history {
		if v.Version == "2.0.0" {
			found2 = true
			if v.IsActive {
				t.Fatalf("2.0.0 should no longer be active after rolling back to 1.0.0: %+v", v)
			}
		}
	}
	if !found2 {
		t.Fatalf("expected 2.0.0 (the version just left) to remain reachable for a future roll-forward, got history: %+v", history)
	}
}

// ut-docs#2891 security review M2: POST /api/plugins/{id}/rollback and GET
// /api/plugins/{id}/versions fed the path id and the body version straight
// into filepath.Join. A version of "../../com.other.plugin/1.0.0" resolved to
// ANOTHER plugin's install tree (and was then persisted as this plugin's
// active version). Both are now validated inside the manager itself.
func TestRollbackRefusesTraversalVersion(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	const pluginID = "com.test.rb"
	seedInstalledPlugin(t, db, pluginID, "RB", "2.0.0", "none", true)
	writeVersionDir(t, base, pluginID, "2.0.0", true)
	// The traversal target really exists — only validation can stop it.
	other := filepath.Join(base, "com.other.plugin", "1.0.0")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"id":"com.test.rb","name":"RB","version":"1.0.0","entrypoint":"./run","runtime":"none","canonical_type":"page","device_arch":"any"}`
	if err := os.WriteFile(filepath.Join(other, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, v := range []string{"../../com.other.plugin/1.0.0", "..", "1.0.0/../../../x", `..\..\x`, "/etc"} {
		if err := rm.Rollback(ctx, pluginID, v, "test"); err == nil || !strings.Contains(err.Error(), "invalid plugin version") {
			t.Errorf("Rollback(version %q) = %v, want an invalid-version error", v, err)
		}
	}
	if got, _, _ := data.NewPluginRepo(db).GetActivePluginVersion(ctx, pluginID); got != "2.0.0" {
		t.Fatalf("active version changed to %q after refused rollbacks", got)
	}
	for _, id := range []string{"..", "../com.other.plugin", "com.test.rb/../..", "COM.TEST.RB/x"} {
		if err := rm.Rollback(ctx, id, "1.0.0", "test"); err == nil || !strings.Contains(err.Error(), "invalid plugin id") {
			t.Errorf("Rollback(id %q) = %v, want an invalid-id error", id, err)
		}
		if _, err := rm.GetVersionHistory(ctx, id); err == nil || !strings.Contains(err.Error(), "invalid plugin id") {
			t.Errorf("GetVersionHistory(id %q) = %v, want an invalid-id error", id, err)
		}
	}
	if err := rm.StoreVersion(pluginID, "../escape", filepath.Join(base, pluginID, "versions", "2.0.0")); err == nil {
		t.Error("StoreVersion accepted a traversal version")
	}
}

// ut-docs#2799: the live per-version install dir a snapshot is taken FROM
// can go missing (disk cleanup, a partial delete — anything short of the
// happy path) without that meaning a good version was never installed. The
// old code went straight to os.RemoveAll(versionDir) before ever checking
// the source existed, so a missing source destroyed an already-good
// existing snapshot and left nothing behind. StoreVersion must now refuse
// before touching the existing snapshot, and report ErrVersionSourceMissing
// so callers can tell this apart from "there was never a snapshot at all".
func TestStoreVersion_MissingSourceKeepsExistingSnapshotIntact(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.missingsrc"

	// A real, good snapshot already on disk from an earlier StoreVersion.
	existingSourceDir := filepath.Join(base, pluginID, "1.0.0")
	if err := os.MkdirAll(existingSourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(existingSourceDir, "manifest.json"), []byte(`{"id":"com.test.missingsrc","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := rm.StoreVersion(pluginID, "1.0.0", existingSourceDir); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	snapshotManifest := filepath.Join(base, pluginID, "versions", "1.0.0", "manifest.json")
	before, err := os.ReadFile(snapshotManifest)
	if err != nil {
		t.Fatalf("seeded snapshot missing: %v", err)
	}

	// The live per-version dir is now gone — StoreVersion is asked to
	// refresh a snapshot it can no longer read from.
	if err := os.RemoveAll(existingSourceDir); err != nil {
		t.Fatalf("remove live source dir: %v", err)
	}

	err = rm.StoreVersion(pluginID, "1.0.0", existingSourceDir)
	if err == nil {
		t.Fatal("StoreVersion succeeded against a missing source, want an error")
	}
	if !errors.Is(err, ErrVersionSourceMissing) {
		t.Fatalf("StoreVersion error = %v, want it to wrap ErrVersionSourceMissing", err)
	}

	after, err := os.ReadFile(snapshotManifest)
	if err != nil {
		t.Fatalf("existing snapshot must survive a failed StoreVersion, but it's gone: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("existing snapshot content changed: before=%q after=%q", before, after)
	}
}

// Same sentinel, different bad source: a FILE where a directory was
// expected (e.g. a stray marker) must not be treated as valid either.
func TestStoreVersion_SourceIsFileNotDirReturnsSentinel(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.filesrc"

	notADir := filepath.Join(base, "stray-file")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o644); err != nil {
		t.Fatalf("write stray file: %v", err)
	}

	err := rm.StoreVersion(pluginID, "1.0.0", notADir)
	if !errors.Is(err, ErrVersionSourceMissing) {
		t.Fatalf("StoreVersion(file source) error = %v, want it to wrap ErrVersionSourceMissing", err)
	}
	if _, statErr := os.Stat(filepath.Join(base, pluginID, "versions", "1.0.0")); statErr == nil {
		t.Fatal("a versions/1.0.0 dir must not be created when the source is invalid")
	}
}

func TestHasVersion(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.hasversion"

	if rm.HasVersion(pluginID, "1.0.0") {
		t.Fatal("HasVersion true before any snapshot exists")
	}

	// A crash-partial or empty versions/<v> dir (StoreVersion interrupted
	// before ever writing a manifest, or just os.MkdirAll'd for a
	// sourcePath == "" marker call) is not a real rollback target
	// (ut-docs#2799 review M-minor 4).
	emptyDir := filepath.Join(base, pluginID, "versions", "0.9.0")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatalf("mkdir empty version dir: %v", err)
	}
	if rm.HasVersion(pluginID, "0.9.0") {
		t.Fatal("HasVersion true for an empty versions/<v> dir with no manifest.json")
	}

	writeVersionDir(t, base, pluginID, "1.0.0", true)
	if !rm.HasVersion(pluginID, "1.0.0") {
		t.Fatal("HasVersion false for an existing versions/<v> directory with a manifest.json")
	}
	if rm.HasVersion(pluginID, "9.9.9") {
		t.Fatal("HasVersion true for a version never stored")
	}
	if rm.HasVersion("../escape", "1.0.0") {
		t.Fatal("HasVersion true for an invalid plugin id")
	}
	if rm.HasVersion(pluginID, "../escape") {
		t.Fatal("HasVersion true for an invalid version")
	}
}

// ut-docs#2799 review M1 (MAJOR): the HasVersion branch in
// cloudInstallPluginVersion means Rollback can now be reached with a real
// rollback target (a versions/<target> snapshot) whose LIVE per-version
// install dir is missing. Rollback used to only touch the DB — never
// restoring pluginBaseDir/id/target/ — so the plugin ended up "installed"
// with zero files on disk (wasm_runtime.go, plugins.go's locale loader,
// plugin_page.go, themes.go and plugin_icons.go all read from that live
// dir, never from versions/). It must now restore the live dir from the
// snapshot before completing.
func TestRollback_RestoresMissingLiveDirFromSnapshot(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.restore"

	// Installed at 2.0.0. 1.0.0 has a versions/ snapshot but deliberately
	// NO live per-version install dir — the shape a snapshot-but-no-live-dir
	// rollback target has after ut-docs#2799's HasVersion fix.
	seedInstalledPlugin(t, db, pluginID, "RS", "2.0.0", "none", true)
	writeVersionDir(t, base, pluginID, "2.0.0", true)
	writeVersionDir(t, base, pluginID, "1.0.0", true)
	seedCatalogRow(t, db, pluginID, "RS", "1.0.0", "")

	liveTargetDir := filepath.Join(base, pluginID, "1.0.0")
	if _, err := os.Stat(liveTargetDir); err == nil {
		t.Fatal("precondition: live target dir must not already exist")
	}

	if err := rm.Rollback(ctx, pluginID, "1.0.0", "tester"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	restored, err := os.ReadFile(filepath.Join(liveTargetDir, "manifest.json"))
	if err != nil {
		t.Fatalf("live target dir must be restored from the snapshot after Rollback: %v", err)
	}
	snapshot, err := os.ReadFile(filepath.Join(base, pluginID, "versions", "1.0.0", "manifest.json"))
	if err != nil {
		t.Fatalf("read snapshot manifest: %v", err)
	}
	if string(restored) != string(snapshot) {
		t.Fatalf("restored manifest = %q, want snapshot content %q", restored, snapshot)
	}
}

// ut-docs#2799 review M-minor 2: StoreVersion used to os.RemoveAll(versionDir)
// unconditionally before copying, so ANY copy failure destroyed an
// already-good existing snapshot and left nothing in its place. It must now
// copy into a temp sibling under versions/ first and only swap it in on
// success.
//
// The failure mechanism here is a symlinked source ROOT: filepath.WalkDir
// lstats the root itself, so a symlink there is walked as a single
// non-directory entry, and copyVersionFiles then calls os.ReadFile on a
// path that resolves to a directory — a copy failure confirmed in review
// and independent of uid/permissions (chmod 000 is ignored when running as
// root, which this sandbox does — id -u == 0).
func TestStoreVersion_CopyFailureKeepsExistingSnapshotIntact(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.copyfail"

	goodSourceDir := filepath.Join(base, pluginID, "1.0.0")
	if err := os.MkdirAll(goodSourceDir, 0o755); err != nil {
		t.Fatalf("mkdir source: %v", err)
	}
	if err := os.WriteFile(filepath.Join(goodSourceDir, "manifest.json"), []byte(`{"id":"com.test.copyfail","version":"1.0.0"}`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	if err := rm.StoreVersion(pluginID, "1.0.0", goodSourceDir); err != nil {
		t.Fatalf("seed snapshot: %v", err)
	}
	snapshotManifest := filepath.Join(base, pluginID, "versions", "1.0.0", "manifest.json")
	before, err := os.ReadFile(snapshotManifest)
	if err != nil {
		t.Fatalf("seeded snapshot missing: %v", err)
	}

	realDir := filepath.Join(base, "real-target-dir")
	if err := os.MkdirAll(filepath.Join(realDir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir real dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(realDir, "sub", "file.txt"), []byte("data"), 0o644); err != nil {
		t.Fatalf("write real file: %v", err)
	}
	symlinkSource := filepath.Join(base, "symlinked-source")
	if err := os.Symlink(realDir, symlinkSource); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if err := rm.StoreVersion(pluginID, "1.0.0", symlinkSource); err == nil {
		t.Fatal("StoreVersion succeeded copying from a symlinked source root, want an error")
	}

	after, err := os.ReadFile(snapshotManifest)
	if err != nil {
		t.Fatalf("existing snapshot must survive a failed copy, but it's gone: %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("existing snapshot content changed: before=%q after=%q", before, after)
	}

	entries, err := os.ReadDir(filepath.Join(base, pluginID, "versions"))
	if err != nil {
		t.Fatalf("readdir versions: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "1.0.0" {
		t.Fatalf("versions/ dir has unexpected entries after failed copy (leftover temp dir?): %v", entries)
	}
}

// ut-docs#2799 review M-minor 3: StoreVersion used to map EVERY os.Stat
// error on sourcePath to ErrVersionSourceMissing. Only "doesn't exist" or
// "exists but isn't a directory" should map to the sentinel — any other
// stat error (permissions, a malformed path, ...) is a real problem a
// caller should still Warn about, not silently treat as "no source, as
// expected".
//
// A NUL byte in the path makes os.Stat fail with a plain "invalid argument"
// syscall error on every platform, which is NOT fs.ErrNotExist — and unlike
// a permissions probe, it doesn't depend on running as non-root.
func TestStoreVersion_OtherStatErrorIsNotSentinel(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.staterr"

	badSourcePath := filepath.Join(base, "bad\x00path")

	err := rm.StoreVersion(pluginID, "1.0.0", badSourcePath)
	if err == nil {
		t.Fatal("StoreVersion succeeded against an invalid source path, want an error")
	}
	if errors.Is(err, ErrVersionSourceMissing) {
		t.Fatalf("StoreVersion error = %v, want a plain error, not the ErrVersionSourceMissing sentinel", err)
	}
}

// ut-docs#3016: a crash between StoreVersion's os.MkdirTemp(".store-*") and
// its final os.Rename leaves a `.store-*` dir under versions/ behind. It's
// not a version snapshot and must never be listed as one.
func TestGetVersionHistory_SkipsTempDirs(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.temphistory"

	writeVersionDir(t, base, pluginID, "1.0.0", true)
	seedInstalledPlugin(t, db, pluginID, "TH", "1.0.0", "none", true)

	leftover := filepath.Join(base, pluginID, "versions", ".store-abc123")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatalf("mkdir leftover: %v", err)
	}

	history, err := rm.GetVersionHistory(ctx, pluginID)
	if err != nil {
		t.Fatalf("GetVersionHistory: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history len = %d, want 1 (temp leftover must be excluded): %+v", len(history), history)
	}
	for _, v := range history {
		if v.Version == ".store-abc123" {
			t.Fatalf("temp leftover dir listed as a version: %+v", v)
		}
	}
}

// ut-docs#3016: a `.store-*` crash leftover in versions/ must not itself
// occupy a "kept version" slot, nor push a real snapshot out of the
// maxVersions=3 window. Without the fix, a leftover newer than an older
// real snapshot sorts ahead of it in cleanupOldVersions' age-ordered
// eviction, so the real snapshot gets deleted instead of the leftover.
func TestStoreVersion_TempLeftoverNotCountedTowardMaxVersions(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.leftovercount"

	now := time.Now()
	for i, v := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		dir := writeVersionDir(t, base, pluginID, v, false)
		mtime := now.Add(time.Duration(i-10) * time.Hour) // far in the past, oldest first
		if err := os.Chtimes(dir, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	// A fresh (not stale) leftover — recent enough that the sweep must
	// leave it alone, so only the isTempVersionDirName filter (not the
	// sweep) can save the real snapshots from it.
	leftover := filepath.Join(base, pluginID, "versions", ".store-leftover")
	if err := os.MkdirAll(leftover, 0o755); err != nil {
		t.Fatalf("mkdir leftover: %v", err)
	}

	// Storing a 4th real version trips cleanupOldVersions.
	if err := rm.StoreVersion(pluginID, "1.3.0", ""); err != nil {
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

	// maxVersions=3 real snapshots must survive: the 3 newest of
	// {1.0.0,1.1.0,1.2.0,1.3.0} are 1.1.0, 1.2.0, 1.3.0 — 1.0.0 (the oldest)
	// is the one that should be evicted, not 1.1.0.
	if !names["1.1.0"] || !names["1.2.0"] || !names["1.3.0"] {
		t.Fatalf("a real snapshot was wrongly evicted because of the temp leftover: %v", names)
	}
	if names["1.0.0"] {
		t.Fatalf("oldest real snapshot should have been evicted: %v", names)
	}
}

// ut-docs#3016: a `.store-*`/`.restore-*` temp dir older than
// staleTempDirAge is a crash leftover and gets swept; one younger is left
// alone in case a concurrent StoreVersion/restore call is still using it.
func TestCleanupOldVersions_SweepsStaleTempDirsButKeepsFreshOnes(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.sweep"

	versionsDir := filepath.Join(base, pluginID, "versions")
	if err := os.MkdirAll(versionsDir, 0o755); err != nil {
		t.Fatalf("mkdir versions: %v", err)
	}
	staleStore := filepath.Join(versionsDir, ".store-stale")
	freshStore := filepath.Join(versionsDir, ".store-fresh")
	if err := os.MkdirAll(staleStore, 0o755); err != nil {
		t.Fatalf("mkdir stale store: %v", err)
	}
	if err := os.MkdirAll(freshStore, 0o755); err != nil {
		t.Fatalf("mkdir fresh store: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(staleStore, old, old); err != nil {
		t.Fatalf("chtimes stale store: %v", err)
	}

	pluginDir := filepath.Join(base, pluginID)
	staleRestore := filepath.Join(pluginDir, ".restore-stale")
	freshRestore := filepath.Join(pluginDir, ".restore-fresh")
	if err := os.MkdirAll(staleRestore, 0o755); err != nil {
		t.Fatalf("mkdir stale restore: %v", err)
	}
	if err := os.MkdirAll(freshRestore, 0o755); err != nil {
		t.Fatalf("mkdir fresh restore: %v", err)
	}
	if err := os.Chtimes(staleRestore, old, old); err != nil {
		t.Fatalf("chtimes stale restore: %v", err)
	}

	if err := rm.cleanupOldVersions(pluginID); err != nil {
		t.Fatalf("cleanupOldVersions: %v", err)
	}

	if _, err := os.Stat(staleStore); !os.IsNotExist(err) {
		t.Fatalf("stale .store- dir must be swept, stat err = %v", err)
	}
	if _, err := os.Stat(freshStore); err != nil {
		t.Fatalf("fresh .store- dir must survive: %v", err)
	}
	if _, err := os.Stat(staleRestore); !os.IsNotExist(err) {
		t.Fatalf("stale .restore- dir must be swept, stat err = %v", err)
	}
	if _, err := os.Stat(freshRestore); err != nil {
		t.Fatalf("fresh .restore- dir must survive: %v", err)
	}
}

// ut-docs#3016: Rollback used to restore the live target dir from its
// snapshot BEFORE parsing the target manifest or running any validate*
// check. A refused rollback (bad manifest, a colliding key, ...) left that
// freshly-restored dir behind as an orphan — disk state for a rollback that
// never took effect. The restore must happen only after every check and DB
// write has succeeded, immediately before tx.Commit().
func TestRollback_RefusedValidationDoesNotLeaveOrphanedLiveDir(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.orphan"

	seedInstalledPlugin(t, db, pluginID, "OR", "2.0.0", "none", true)
	writeVersionDir(t, base, pluginID, "2.0.0", true)

	// Target version has a versions/ snapshot but its manifest.json fails to
	// parse — the validation step Rollback must run BEFORE restoring.
	targetDir := writeVersionDir(t, base, pluginID, "1.0.0", false)
	if err := os.WriteFile(filepath.Join(targetDir, "manifest.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write broken manifest: %v", err)
	}

	liveTargetDir := filepath.Join(base, pluginID, "1.0.0")
	if _, err := os.Stat(liveTargetDir); !os.IsNotExist(err) {
		t.Fatalf("precondition: live target dir must not already exist, stat err = %v", err)
	}

	err := rm.Rollback(ctx, pluginID, "1.0.0", "tester")
	if err == nil || !strings.Contains(err.Error(), "parse manifest") {
		t.Fatalf("Rollback(broken manifest) = %v, want a parse-manifest error", err)
	}

	if _, err := os.Stat(liveTargetDir); !os.IsNotExist(err) {
		t.Fatalf("live target dir must not exist after a refused rollback, stat err = %v", err)
	}
}

// ut-docs#3032: Rollback used to snapshot the version it's leaving (via
// StoreVersion, which runs cleanupOldVersions) BEFORE ever opening the
// rollback target's manifest. cleanupOldVersions keeps only maxVersions=3
// snapshots, oldest by mtime evicted — so if the rollback TARGET happened to
// be the oldest snapshot on disk, that pre-target StoreVersion call evicted
// it out from under the rollback that was about to read it, and Rollback
// failed with "failed to open manifest: .../versions/1.0.0/manifest.json: no
// such file or directory" despite the target existing when Rollback was
// called. The snapshot-the-version-we're-leaving step must happen only after
// tx.Commit() succeeds, and cleanupOldVersions must never evict the rollback
// target regardless of ordering.
func TestRollback_ToOldestSnapshotKeepsTarget(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.oldest"

	// Three existing snapshots, 1.0.0 strictly the oldest by mtime — the
	// rollback target.
	now := time.Now()
	for i, v := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		dir := writeVersionDir(t, base, pluginID, v, true)
		mtime := now.Add(time.Duration(i-10) * time.Hour)
		if err := os.Chtimes(dir, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", v, err)
		}
	}

	// Live, active install at 2.0.0 — never itself snapshotted yet, so
	// rolling back triggers a "snapshot the version we're leaving" write.
	liveDir := filepath.Join(base, pluginID, "2.0.0")
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatalf("mkdir live dir: %v", err)
	}
	manifest := `{"id":"` + pluginID + `","name":"OLD","version":"2.0.0","entrypoint":"./run","runtime":"none","canonical_type":"page","device_arch":"any","entries":[]}`
	if err := os.WriteFile(filepath.Join(liveDir, "manifest.json"), []byte(manifest), 0o644); err != nil {
		t.Fatalf("write live manifest: %v", err)
	}
	seedInstalledPlugin(t, db, pluginID, "OLD", "2.0.0", "none", true)
	seedCatalogRow(t, db, pluginID, "OLD", "1.0.0", "")

	if err := rm.Rollback(ctx, pluginID, "1.0.0", "tester"); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if _, err := os.Stat(filepath.Join(base, pluginID, "versions", "1.0.0", "manifest.json")); err != nil {
		t.Fatalf("rollback target snapshot must survive its own rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, pluginID, "versions", "2.0.0")); err != nil {
		t.Fatalf("roll-forward snapshot of the version just left must exist: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(base, pluginID, "versions"))
	if err != nil {
		t.Fatalf("readdir versions: %v", err)
	}
	var real int
	for _, e := range entries {
		if e.IsDir() && !isTempVersionDirName(e.Name()) {
			real++
		}
	}
	if real > 3 {
		t.Fatalf("real snapshot count = %d, want <= 3 (maxVersions): %v", real, entries)
	}
}

// ut-docs#3032: a rollback refused by a validator must leave the versions/
// directory exactly as it found it — no snapshot of the version being left,
// nothing evicted. The pre-fix code ran StoreVersion (and its
// cleanupOldVersions side effect) before any validation, so a refused
// rollback still mutated versions/ on disk.
func TestRollback_RefusedDoesNotTouchVersionsDir(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.refusedversions"

	seedInstalledPlugin(t, db, pluginID, "RV", "2.0.0", "none", true)

	// Live install for the current, active version — present, but never
	// itself snapshotted yet (so a pre-validation StoreVersion call would
	// write a new versions/2.0.0 entry).
	liveDir := filepath.Join(base, pluginID, "2.0.0")
	if err := os.MkdirAll(liveDir, 0o755); err != nil {
		t.Fatalf("mkdir live dir: %v", err)
	}
	liveManifest := `{"id":"` + pluginID + `","name":"RV","version":"2.0.0","entrypoint":"./run","runtime":"none","canonical_type":"page","device_arch":"any","entries":[]}`
	if err := os.WriteFile(filepath.Join(liveDir, "manifest.json"), []byte(liveManifest), 0o644); err != nil {
		t.Fatalf("write live manifest: %v", err)
	}

	// Three existing snapshots already at maxVersions, distinct mtimes —
	// the rollback target (1.0.0, newest of the three) has a broken
	// manifest, so validation refuses before parsing succeeds.
	now := time.Now()
	for i, v := range []string{"0.8.0", "0.9.0", "1.0.0"} {
		dir := writeVersionDir(t, base, pluginID, v, false)
		mtime := now.Add(time.Duration(i-10) * time.Hour)
		if err := os.Chtimes(dir, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", v, err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, pluginID, "versions", "1.0.0", "manifest.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatalf("write broken manifest: %v", err)
	}

	before, err := os.ReadDir(filepath.Join(base, pluginID, "versions"))
	if err != nil {
		t.Fatalf("readdir versions before: %v", err)
	}
	beforeNames := map[string]bool{}
	for _, e := range before {
		beforeNames[e.Name()] = true
	}

	err = rm.Rollback(ctx, pluginID, "1.0.0", "tester")
	if err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Fatalf("Rollback(broken manifest) = %v, want a manifest error", err)
	}

	after, err := os.ReadDir(filepath.Join(base, pluginID, "versions"))
	if err != nil {
		t.Fatalf("readdir versions after: %v", err)
	}
	afterNames := map[string]bool{}
	for _, e := range after {
		afterNames[e.Name()] = true
	}
	if len(beforeNames) != len(afterNames) {
		t.Fatalf("versions/ entry count changed after a refused rollback: before=%v after=%v", beforeNames, afterNames)
	}
	for name := range beforeNames {
		if !afterNames[name] {
			t.Fatalf("versions/%s was removed by a refused rollback: before=%v after=%v", name, beforeNames, afterNames)
		}
	}
	for name := range afterNames {
		if !beforeNames[name] {
			t.Fatalf("versions/%s was added by a refused rollback: before=%v after=%v", name, beforeNames, afterNames)
		}
	}
}

// ut-docs#3032: cleanupOldVersions' keep set must protect a snapshot from
// eviction regardless of its age — Rollback relies on this to pass the
// rollback target as a must-keep name for its post-commit "snapshot the
// version we're leaving" cleanup pass.
func TestCleanupOldVersions_KeepProtectsFromEviction(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.keepcleanup"

	now := time.Now()
	versions := []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0"} // oldest first
	for i, v := range versions {
		dir := writeVersionDir(t, base, pluginID, v, false)
		mtime := now.Add(time.Duration(i-10) * time.Hour)
		if err := os.Chtimes(dir, mtime, mtime); err != nil {
			t.Fatalf("chtimes %s: %v", v, err)
		}
	}

	// Keep the oldest (1.0.0) — without a keep set it would normally be the
	// first evicted. With 4 real snapshots and maxVersions=3, one deletion
	// is needed; since 1.0.0 is protected, the next-oldest (1.1.0) is the
	// one removed instead.
	if err := rm.cleanupOldVersions(pluginID, "1.0.0"); err != nil {
		t.Fatalf("cleanupOldVersions: %v", err)
	}

	entries, err := os.ReadDir(filepath.Join(base, pluginID, "versions"))
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.Name()] = true
	}
	if !names["1.0.0"] {
		t.Fatalf("kept version 1.0.0 was evicted: %v", names)
	}
	if names["1.1.0"] {
		t.Fatalf("second-oldest version 1.1.0 should have been evicted in place of the kept oldest: %v", names)
	}
	if !names["1.2.0"] || !names["1.3.0"] {
		t.Fatalf("newer versions missing: %v", names)
	}
	if len(entries) != 3 {
		t.Fatalf("kept %d versions, want 3: %v", len(entries), names)
	}
}
