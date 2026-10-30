package data

import (
	"bytes"
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#5172: the LAN bearer of a joined till lives in a file under
// secrets/ (excluded from backup), never in a settings row a DB backup copies.

type bearerFixture struct {
	dbPath string
	db     *sql.DB
	repo   *SettingsRepo
	file   string
}

func newBearerFixture(t *testing.T) *bearerFixture {
	t.Helper()
	dbPath := testsupport.MigratedDBFile(t, "test.db")
	dbh, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { dbh.Close() })
	return &bearerFixture{
		dbPath: dbPath,
		db:     dbh.DB,
		repo:   NewSettingsRepo(dbh.DB),
		file:   filepath.Join(t.TempDir(), "secrets", "sync-bearer"),
	}
}

func (f *bearerFixture) register(t *testing.T) {
	t.Helper()
	if err := KeepSyncBearerOutOfDB(context.Background(), f.db, f.file); err != nil {
		t.Fatalf("KeepSyncBearerOutOfDB: %v", err)
	}
	t.Cleanup(func() { syncBearerFiles.Delete(f.db) })
}

func (f *bearerFixture) rowCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = ?`, SyncBearerSettingsKey).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (f *bearerFixture) setRow(t *testing.T, v string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, SyncBearerSettingsKey, v); err != nil {
		t.Fatal(err)
	}
}

func (f *bearerFixture) readFile(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.file)
	if err != nil {
		t.Fatalf("read bearer file: %v", err)
	}
	return string(b)
}

func TestSyncBearer_RegisteredSetWritesFileNotRow(t *testing.T) {
	f := newBearerFixture(t)
	f.register(t)
	ctx := context.Background()

	if err := f.repo.Set(ctx, "sync.bearer", "b1"); err != nil {
		t.Fatal(err)
	}
	if got := f.readFile(t); got != "b1" {
		t.Fatalf("file = %q", got)
	}
	fi, _ := os.Stat(f.file)
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v", fi.Mode().Perm())
	}
	di, _ := os.Stat(filepath.Dir(f.file))
	if di.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode = %v", di.Mode().Perm())
	}
	if n := f.rowCount(t); n != 0 {
		t.Fatalf("settings row count = %d, want 0", n)
	}
	v, ok, err := f.repo.Get(ctx, "sync.bearer")
	if err != nil || !ok || v != "b1" {
		t.Fatalf("Get = %q %v %v", v, ok, err)
	}
}

func TestSyncBearer_SnapshotCarriesNoBearer(t *testing.T) {
	f := newBearerFixture(t)
	f.register(t)
	const secret = "very-secret-bearer-value-5172"
	if err := f.repo.Set(context.Background(), "sync.bearer", secret); err != nil {
		t.Fatal(err)
	}
	snap, err := db.Snapshot(f.db, f.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) {
		t.Fatal("snapshot contains the bearer")
	}
	if bytes.Contains(raw, []byte("sync.bearer")) {
		t.Fatal("snapshot contains a sync.bearer row")
	}
}

func TestSyncBearer_MigratesRowToFile(t *testing.T) {
	f := newBearerFixture(t)
	f.setRow(t, "old-row")
	f.register(t)
	if got := f.readFile(t); got != "old-row" {
		t.Fatalf("file = %q", got)
	}
	if n := f.rowCount(t); n != 0 {
		t.Fatalf("row not removed (%d)", n)
	}
	// Second registration: no-op, keeps the value.
	if err := KeepSyncBearerOutOfDB(context.Background(), f.db, f.file); err != nil {
		t.Fatal(err)
	}
	if got := f.readFile(t); got != "old-row" {
		t.Fatalf("file after 2nd call = %q", got)
	}
}

func TestSyncBearer_RowWinsOverExistingFile(t *testing.T) {
	f := newBearerFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.file, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.setRow(t, "fresh")
	f.register(t)
	if got := f.readFile(t); got != "fresh" {
		t.Fatalf("file = %q, want fresh", got)
	}
	if n := f.rowCount(t); n != 0 {
		t.Fatalf("row not removed")
	}
}

func TestSyncBearer_NoRowKeepsExistingFile(t *testing.T) {
	f := newBearerFixture(t)
	if err := os.MkdirAll(filepath.Dir(f.file), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.file, []byte("kept\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.register(t)
	v, ok, err := f.repo.Get(context.Background(), "sync.bearer")
	if err != nil || !ok || v != "kept" {
		t.Fatalf("Get = %q %v %v", v, ok, err)
	}
}

func TestSyncBearer_EmptyRowJustDeleted(t *testing.T) {
	f := newBearerFixture(t)
	f.setRow(t, "")
	f.register(t)
	if n := f.rowCount(t); n != 0 {
		t.Fatal("empty row kept")
	}
	if _, err := os.Stat(f.file); !os.IsNotExist(err) {
		t.Fatalf("file should not exist: %v", err)
	}
}

func TestSyncBearer_EmptySetAndDeleteRemoveFile(t *testing.T) {
	f := newBearerFixture(t)
	f.register(t)
	ctx := context.Background()
	for name, clear := range map[string]func() error{
		"set-empty": func() error { return f.repo.Set(ctx, "sync.bearer", "") },
		"delete":    func() error { return f.repo.Delete(ctx, "sync.bearer") },
	} {
		if err := f.repo.Set(ctx, "sync.bearer", "x"); err != nil {
			t.Fatal(err)
		}
		if err := clear(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := os.Stat(f.file); !os.IsNotExist(err) {
			t.Fatalf("%s: file still there (%v)", name, err)
		}
		v, ok, err := f.repo.Get(ctx, "sync.bearer")
		if err != nil || ok || v != "" {
			t.Fatalf("%s: Get = %q %v %v", name, v, ok, err)
		}
	}
}

func TestSyncBearer_ClearReplicaIdentityRemovesFile(t *testing.T) {
	f := newBearerFixture(t)
	f.register(t)
	ctx := context.Background()
	if err := f.repo.Set(ctx, "sync.bearer", "b"); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.Set(ctx, "sync.till_id", "t1"); err != nil {
		t.Fatal(err)
	}
	if err := f.repo.ClearReplicaIdentity(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.file); !os.IsNotExist(err) {
		t.Fatalf("file still there (%v)", err)
	}
}

func TestSyncBearer_PrefixAllAndSetMany(t *testing.T) {
	f := newBearerFixture(t)
	f.register(t)
	ctx := context.Background()
	if err := f.repo.SetMany(ctx, map[string]string{"sync.bearer": "bm", "sync.till_id": "t9", "other.k": "o"}); err != nil {
		t.Fatal(err)
	}
	if n := f.rowCount(t); n != 0 {
		t.Fatal("SetMany left a bearer row")
	}
	if got := f.readFile(t); got != "bm" {
		t.Fatalf("file = %q", got)
	}
	m, err := f.repo.GetByPrefix(ctx, "sync.")
	if err != nil || m["sync.bearer"] != "bm" || m["sync.till_id"] != "t9" {
		t.Fatalf("GetByPrefix = %v %v", m, err)
	}
	m, err = f.repo.GetByPrefix(ctx, "other.")
	if err != nil || len(m) != 1 || m["other.k"] != "o" {
		t.Fatalf("GetByPrefix other = %v %v", m, err)
	}
	all, err := f.repo.All(ctx)
	if err != nil || all["sync.bearer"] != "bm" || all["other.k"] != "o" {
		t.Fatalf("All = %v %v", all, err)
	}
}

func TestSyncBearer_SetTxAndGetOrCreate(t *testing.T) {
	f := newBearerFixture(t)
	f.register(t)
	ctx := context.Background()
	tx, err := f.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.repo.SetTx(ctx, tx, "sync.bearer", "tx1"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := f.readFile(t); got != "tx1" {
		t.Fatalf("file = %q", got)
	}
	v, err := f.repo.GetOrCreate(ctx, "sync.bearer", "seed")
	if err != nil || v != "tx1" {
		t.Fatalf("GetOrCreate existing = %q %v", v, err)
	}
	_ = f.repo.Delete(ctx, "sync.bearer")
	v, err = f.repo.GetOrCreate(ctx, "sync.bearer", "seed")
	if err != nil || v != "seed" || f.readFile(t) != "seed" || f.rowCount(t) != 0 {
		t.Fatalf("GetOrCreate new = %q %v", v, err)
	}
}

func TestSyncBearer_UnregisteredKeepsRowBehaviour(t *testing.T) {
	f := newBearerFixture(t)
	ctx := context.Background()
	if err := f.repo.Set(ctx, "sync.bearer", "row"); err != nil {
		t.Fatal(err)
	}
	if n := f.rowCount(t); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
	if _, err := os.Stat(f.file); !os.IsNotExist(err) {
		t.Fatal("file written for unregistered db")
	}
	v, ok, _ := f.repo.Get(ctx, "sync.bearer")
	if !ok || v != "row" {
		t.Fatalf("Get = %q %v", v, ok)
	}
}

func TestSyncBearer_WriteFailureKeepsRowAndDoesNotRegister(t *testing.T) {
	f := newBearerFixture(t)
	f.setRow(t, "keepme")
	blocker := filepath.Join(t.TempDir(), "afile")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := KeepSyncBearerOutOfDB(context.Background(), f.db, filepath.Join(blocker, "secrets", "sync-bearer"))
	if err == nil {
		t.Fatal("expected error")
	}
	if n := f.rowCount(t); n != 1 {
		t.Fatal("row deleted despite failed file write")
	}
	v, ok, _ := f.repo.Get(context.Background(), "sync.bearer")
	if !ok || v != "keepme" {
		t.Fatalf("Get after failed registration = %q %v", v, ok)
	}
}

// The migration's delete must not leave the old bearer readable in the live
// database file or its WAL, both of which an iCloud backup of the data folder
// (or a user's copy of the .db) carries (ut-docs#5172 review).
func TestSyncBearer_MigrationLeavesNoBytesInLiveFiles(t *testing.T) {
	f := newBearerFixture(t)
	const secret = "residual-bearer-bytes-5172-zz"
	f.setRow(t, secret)
	// Some unrelated churn on the same page, as a real till has.
	for i := 0; i < 20; i++ {
		if err := f.repo.Set(context.Background(), "other.k", string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	f.register(t)
	for _, p := range []string{f.dbPath, f.dbPath + "-wal"} {
		raw, err := os.ReadFile(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(secret)) {
			t.Errorf("%s still contains the migrated bearer's bytes", filepath.Base(p))
		}
	}
}
