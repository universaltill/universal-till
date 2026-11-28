package db

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

// Free space under twice the database size refuses that plugin's snapshot
// (reported), the others still go in. Mutation: delete the guard -> red.
func TestBackupPluginDataRefusesWhenDiskCannotHoldTheCopy(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugin-data")
	for _, id := range []string{"shop-a", "shop-b"} {
		d := pdOpenPlugin(t, root, id)
		if _, err := d.Exec(`INSERT INTO t (v) VALUES ('x')`); err != nil {
			t.Fatal(err)
		}
	}
	old := pluginFreeBytes
	defer func() { pluginFreeBytes = old }()
	pluginFreeBytes = func(string) (uint64, bool) { return 100, true }
	snap := pdSnapshotFile(t)
	err := EmbedBackupPluginData(context.Background(), snap, root)
	if !errors.Is(err, ErrBackupNoSpace) {
		t.Fatalf("err = %v, want ErrBackupNoSpace", err)
	}
	if n := countBackupRows(t, snap); n != 0 {
		t.Fatalf("%d rows embedded despite no space", n)
	}
	// Unknown free space does not refuse; plenty of space does not refuse.
	for _, f := range []func(string) (uint64, bool){
		func(string) (uint64, bool) { return 0, false },
		func(string) (uint64, bool) { return 1 << 40, true },
	} {
		pluginFreeBytes = f
		if err := EmbedBackupPluginData(context.Background(), pdSnapshotFile(t), root); err != nil {
			t.Fatalf("unexpected refusal: %v", err)
		}
	}
}

// A cancelled context stops the plugin-data part of a backup and says so.
// Mutation: ignore ctx -> no error and rows are written.
func TestBackupPluginDataStopsOnCancelledContext(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugin-data")
	d := pdOpenPlugin(t, root, "shop-a")
	if _, err := d.Exec(`INSERT INTO t (v) VALUES ('x')`); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithCancel(context.Background())
	cancel()
	snap := pdSnapshotFile(t)
	err := EmbedBackupPluginData(c, snap, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if n := countBackupRows(t, snap); n != 0 {
		t.Fatalf("%d rows embedded after cancel", n)
	}
}

func TestPluginBackupDeadlinesAreBounded(t *testing.T) {
	if pluginSnapshotDeadline <= 0 || pluginSnapshotDeadline >= pluginDataBackupDeadline || pluginDataBackupDeadline > time.Hour {
		t.Fatalf("deadlines: %v / %v", pluginSnapshotDeadline, pluginDataBackupDeadline)
	}
}

func countBackupRows(t *testing.T, snap string) int {
	t.Helper()
	d, err := openBackupFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM ` + backupPluginDataTable).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
