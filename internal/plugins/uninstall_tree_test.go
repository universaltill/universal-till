package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func pluginRowCount(t *testing.T, rm *RollbackManager, pluginID string) int {
	t.Helper()
	var n int
	if err := rm.db.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = ?`, pluginID).Scan(&n); err != nil {
		t.Fatalf("count plugin rows: %v", err)
	}
	return n
}

// ut-docs#3082: an uninstall (settings page, cloud remove_plugin, sync
// prune, salon removal) that fires while a Rollback of the same plugin is
// between its target stat and its commit must wait for the rollback, not
// delete the tree under it. Without the per-plugin lock the uninstall
// removes versions/1.0.0 and the rollback fails reading its manifest.
func TestUninstallPluginTree_WaitsForConcurrentRollback(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.uninstallrace"

	writeVersionDir(t, base, pluginID, "1.0.0", true)
	seedInstalledPlugin(t, db, pluginID, "RACE", "2.0.0", "none", true)
	seedCatalogRow(t, db, pluginID, "RACE", "1.0.0", "")

	uninstallDone := make(chan error, 1)
	uninstallStarting := make(chan struct{})
	rollbackAfterTargetStat = func() {
		go func() {
			close(uninstallStarting)
			uninstallDone <- UninstallPluginTree(ctx, db, base, pluginID)
		}()
		// Give the uninstall every chance to run before Rollback goes on;
		// holding the per-plugin lock, it blocks until Rollback returns.
		<-uninstallStarting
		select {
		case err := <-uninstallDone:
			uninstallDone <- err
		case <-time.After(200 * time.Millisecond):
		}
	}
	t.Cleanup(func() { rollbackAfterTargetStat = nil })

	if err := rm.Rollback(ctx, pluginID, "1.0.0", "tester"); err != nil {
		t.Fatalf("Rollback raced with an uninstall of the same plugin: %v", err)
	}
	if err := <-uninstallDone; err != nil {
		t.Fatalf("concurrent UninstallPluginTree: %v", err)
	}
	// The uninstall ran last, so it wins: no tree, no row, no snapshot of
	// the version Rollback left behind.
	if _, err := os.Stat(filepath.Join(base, pluginID)); !os.IsNotExist(err) {
		t.Fatalf("plugin tree still on disk after uninstall (stat err %v)", err)
	}
	if n := pluginRowCount(t, rm, pluginID); n != 0 {
		t.Fatalf("plugin rows after uninstall = %d, want 0", n)
	}
}

func TestUninstallPluginTree_RemovesRowsAndFiles(t *testing.T) {
	db := managerTestDB(t)
	ctx := context.Background()
	base := t.TempDir()
	rm := NewRollbackManager(db, base)
	pluginID := "com.test.uninstall"

	writeVersionDir(t, base, pluginID, "1.0.0", true)
	if err := os.MkdirAll(filepath.Join(base, pluginID, "2.0.0"), 0o755); err != nil {
		t.Fatalf("mkdir live dir: %v", err)
	}
	other := filepath.Join(base, "com.test.other", "1.0.0")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatalf("mkdir other plugin: %v", err)
	}
	seedInstalledPlugin(t, db, pluginID, "U", "2.0.0", "none", true)

	if err := UninstallPluginTree(ctx, db, base, pluginID); err != nil {
		t.Fatalf("UninstallPluginTree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, pluginID)); !os.IsNotExist(err) {
		t.Fatalf("plugin tree still on disk (stat err %v)", err)
	}
	if n := pluginRowCount(t, rm, pluginID); n != 0 {
		t.Fatalf("plugin rows = %d, want 0", n)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("another plugin's files were touched: %v", err)
	}
}

// The id is joined under base and RemoveAll'd: "." would be every plugin's
// files (ut-docs#2891 M2), so the helper validates it itself.
func TestUninstallPluginTree_RejectsInvalidID(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	keep := filepath.Join(base, "com.test.keep")
	if err := os.MkdirAll(keep, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, id := range []string{"", ".", "..", "../x", "a/b"} {
		if err := UninstallPluginTree(context.Background(), db, base, id); err == nil {
			t.Fatalf("UninstallPluginTree(%q) = nil, want an invalid-id error", id)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("an invalid id removed files: %v", err)
	}
}

// ut-docs#3082 review: the orphaned mismatched-version dir the cloudsync
// auto-rollback deletes is removed under the same lock, and nothing else.
func TestRemoveVersionDir(t *testing.T) {
	base := t.TempDir()
	pluginID := "com.test.rmver"
	bad := filepath.Join(base, pluginID, "9.9.9")
	good := filepath.Join(base, pluginID, "1.0.0")
	for _, d := range []string{bad, good} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	if err := RemoveVersionDir(base, pluginID, "9.9.9"); err != nil {
		t.Fatalf("RemoveVersionDir: %v", err)
	}
	if _, err := os.Stat(bad); !os.IsNotExist(err) {
		t.Fatalf("version dir still present (stat err %v)", err)
	}
	if _, err := os.Stat(good); err != nil {
		t.Fatalf("another version was removed: %v", err)
	}
	for _, v := range []string{"", ".", "..", "../x"} {
		if err := RemoveVersionDir(base, pluginID, v); err == nil {
			t.Fatalf("RemoveVersionDir(version %q) = nil, want an error", v)
		}
	}
	if _, err := os.Stat(good); err != nil {
		t.Fatalf("an invalid version removed files: %v", err)
	}
}
