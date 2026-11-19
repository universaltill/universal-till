package db

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// ut-docs#5049: a data folder deleted from outside a live server must make
// the next NEW pooled connection fail, not silently create an empty database
// ("no such table: settings" flood).
func TestOpen_DeletedFileDoesNotRecreatePhantomDB(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("an open file cannot be removed on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "unitill-pos.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	// Force every later query onto a brand-new connection.
	d.SetMaxIdleConns(0)
	d.SetConnMaxLifetime(1)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatalf("remove %s: %v", path+suffix, err)
		}
	}

	var n int
	err = d.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n)
	if err == nil {
		t.Fatal("query against a deleted database succeeded")
	}
	if strings.Contains(err.Error(), "no such table") {
		t.Fatalf("a phantom empty database was created: %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("database file was recreated at %s (stat err: %v)", path, statErr)
	}
}

func TestOpen_CreatesAndMigratesMissingFileInExistingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fresh.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM settings`).Scan(&n); err != nil {
		t.Fatalf("settings table missing after Open: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("db file not created: %v", err)
	}
}
