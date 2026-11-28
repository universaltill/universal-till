package plugindb

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Tester additions (ut-docs#3157), each one kills a mutation that survived
// the Dev tests.

// A refused word hidden inside a CREATE TRIGGER body is not a statement start
// to the splitter, so only the "anywhere" check refuses it. Mutation:
// refusedAnywhere emptied.
func TestMigrationRefusesPrivilegedWordInsideTriggerBody(t *testing.T) {
	for name, body := range map[string]string{
		"attach": "ATTACH 'x.db' AS o;",
		"pragma": "PRAGMA max_page_count=1000000;",
		"vacuum": "VACUUM;",
		"detach": "DETACH o;",
	} {
		m, _ := newMgr(t)
		sql := "CREATE TABLE a(x); CREATE TRIGGER tr AFTER INSERT ON a BEGIN " + body + " END;"
		_, err := m.Open(ctx, "p", Options{Migrations: []Migration{{1, "evil", sql}}})
		if !errors.Is(err, ErrStatementRefused) {
			t.Errorf("%s: Open err = %v, want ErrStatementRefused", name, err)
		}
	}
}

// Prune only touches trees with the strict <id>-YYYYMMDD name; an expired
// marker in a differently named directory is left alone. Mutation: the name
// filter removed.
func TestPruneIgnoresNonStrictNamesEvenWithExpiredMarker(t *testing.T) {
	m, clock := newMgr(t)
	rr, _ := m.resolvedRoot()
	rd := filepath.Join(rr, removedDir)
	for _, name := range []string{"notes", "shop", "x-2020", "x-20200101-old", "UPPER-20200101"} {
		d := filepath.Join(rd, name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		mk, _ := json.Marshal(removedMarker{PluginID: "x", RemovedAt: *clock, PurgeAfter: clock.Add(-time.Hour)})
		if err := os.WriteFile(filepath.Join(d, markerName), mk, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	purged, err := m.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if len(purged) != 0 {
		t.Fatalf("purged %v, want nothing", purged)
	}
	for _, name := range []string{"notes", "shop", "x-2020", "x-20200101-old", "UPPER-20200101"} {
		if _, err := os.Stat(filepath.Join(rd, name)); err != nil {
			t.Errorf("%s was removed: %v", name, err)
		}
	}
}

// A symlink with a strict removed-tree name that points at a directory with
// an expired marker is never followed. Mutation: both the IsDir and the
// ModeSymlink filters removed.
func TestPruneNeverFollowsAStrictlyNamedSymlinkEntry(t *testing.T) {
	m, clock := newMgr(t)
	victim := t.TempDir()
	mk, _ := json.Marshal(removedMarker{PluginID: "x", RemovedAt: *clock, PurgeAfter: clock.Add(-time.Hour)})
	if err := os.WriteFile(filepath.Join(victim, markerName), mk, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(victim, "keep.txt"), []byte("k"), 0o600); err != nil {
		t.Fatal(err)
	}
	rr, _ := m.resolvedRoot()
	rd := filepath.Join(rr, removedDir)
	_ = os.MkdirAll(rd, 0o700)
	if err := os.Symlink(victim, filepath.Join(rd, "x-20200101")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	purged, err := m.Prune()
	if err != nil {
		t.Fatal(err)
	}
	if len(purged) != 0 {
		t.Fatalf("purged %v through a symlink entry", purged)
	}
	if _, err := os.Lstat(filepath.Join(rd, "x-20200101")); err != nil {
		t.Fatalf("symlink entry was touched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(victim, "keep.txt")); err != nil {
		t.Fatalf("data behind the symlink was deleted: %v", err)
	}
}
