package plugins

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// ADR-0121 §4 / #3157: plugin data lives in the plugin-data sibling root, so
// uninstalling the code tree and pruning old versions must never touch it.
func TestPluginDataSiblingRootSurvivesUninstallAndPrune_3157(t *testing.T) {
	db := managerTestDB(t)
	data := t.TempDir()
	base := filepath.Join(data, "plugins")
	pluginID := "com.test.keepdata"
	writeVersionDir(t, base, pluginID, "1.0.0", true)
	if err := os.MkdirAll(filepath.Join(base, pluginID, "2.0.0"), 0o755); err != nil {
		t.Fatal(err)
	}
	seedInstalledPlugin(t, db, pluginID, "K", "2.0.0", "none", true)
	dbFile := filepath.Join(data, "plugin-data", pluginID, "plugin.db")
	if err := os.MkdirAll(filepath.Dir(dbFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbFile, []byte("shop data"), 0o600); err != nil {
		t.Fatal(err)
	}

	rm := NewRollbackManager(db, base)
	if err := rm.cleanupOldVersions(pluginID); err != nil {
		t.Logf("prune: %v", err) // a non-error either way; data must survive
	}
	if err := UninstallPluginTree(context.Background(), db, base, pluginID); err != nil {
		t.Fatalf("UninstallPluginTree: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, pluginID)); !os.IsNotExist(err) {
		t.Fatalf("code tree not removed: %v", err)
	}
	b, err := os.ReadFile(dbFile)
	if err != nil || string(b) != "shop data" {
		t.Fatalf("plugin-data changed: %q, %v", b, err)
	}
}
