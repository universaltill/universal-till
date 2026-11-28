package plugins

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseStrictSemver_3157(t *testing.T) {
	good := []string{"1.2.3", "v1.2.3", "0.0.1", "10.20.30", "1.2.3-rc.1", "1.2.3+build.5", "1.2.3-beta+exp.sha"}
	bad := []string{"", "notes", "plugin-data", ".hidden", "1.2", "1", "v1.2", "1.2.3.4", "v1.2.3-x/..", "1.2.3-", "a.b.c", "1.2.3 ", "..", "1.2.3/x", "latest", "V1.2.3"}
	for _, s := range good {
		if !parseStrictSemver(s) {
			t.Errorf("%q should parse", s)
		}
	}
	for _, s := range bad {
		if parseStrictSemver(s) {
			t.Errorf("%q must not parse", s)
		}
	}
}

// Only semver-named dirs are prune candidates; everything else (odd names,
// symlinks, files) survives, and semver dirs beyond maxVersions go oldest-first.
func TestCleanupOldVersionsOnlyPrunesSemverDirs_3157(t *testing.T) {
	db := managerTestDB(t)
	base := t.TempDir()
	pluginID := "com.test.strictprune"
	seedInstalledPlugin(t, db, pluginID, "S", "9.9.9", "none", true)

	now := time.Now()
	semver := []string{"1.0.0", "1.1.0", "1.2.0", "1.3.0", "v1.4.0"}
	for i, v := range semver {
		dir := writeVersionDir(t, base, pluginID, v, false)
		mt := now.Add(time.Duration(i-10) * time.Hour)
		if err := os.Chtimes(dir, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	versionsDir := filepath.Join(base, pluginID, "versions")
	// Oldest of all, so a non-strict prune would take them first.
	odd := []string{"notes", "plugin-data", ".hidden", "1.2", "v1.2.3-", "1.2.3.4"}
	for _, n := range odd {
		dir := filepath.Join(versionsDir, n)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		old := now.Add(-100 * time.Hour)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "keep.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(versionsDir, "0.0.1")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if err := os.WriteFile(filepath.Join(versionsDir, "0.0.2"), []byte("f"), 0o600); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(base, pluginID, "plugin-data")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatal(err)
	}

	rm := NewRollbackManager(db, base)
	rm.maxVersions = 3
	if err := rm.cleanupOldVersions(pluginID); err != nil {
		t.Fatalf("cleanupOldVersions: %v", err)
	}

	for _, n := range append(odd, "0.0.1", "0.0.2") {
		if _, err := os.Lstat(filepath.Join(versionsDir, n)); err != nil {
			t.Errorf("%s must survive prune: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Errorf("symlink target content touched: %v", err)
	}
	if _, err := os.Stat(sibling); err != nil {
		t.Errorf("sibling dir removed: %v", err)
	}
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if _, err := os.Stat(filepath.Join(versionsDir, v)); !os.IsNotExist(err) {
			t.Errorf("%s should be pruned oldest-first: %v", v, err)
		}
	}
	for _, v := range []string{"1.2.0", "1.3.0", "v1.4.0"} {
		if _, err := os.Stat(filepath.Join(versionsDir, v)); err != nil {
			t.Errorf("%s should remain: %v", v, err)
		}
	}
}
