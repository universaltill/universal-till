package db

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
)

// pdFixture is a data dir with plugin-data/<id>/plugin.db (WAL) and a snapshot DB.
func pdOpenPlugin(t *testing.T, root, id string) *sql.DB {
	t.Helper()
	dir := filepath.Join(root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	d, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "plugin.db")+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`CREATE TABLE IF NOT EXISTS t (n INTEGER PRIMARY KEY, v TEXT)`); err != nil {
		t.Fatal(err)
	}
	return d
}

func pdRows(t *testing.T, path string) string {
	t.Helper()
	d, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	r, err := d.Query(`SELECT n, v FROM t ORDER BY n`)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var out string
	for r.Next() {
		var n int
		var v string
		if err := r.Scan(&n, &v); err != nil {
			t.Fatal(err)
		}
		out += fmt.Sprintf("%d=%s;", n, v)
	}
	return out
}

func pdSnapshotFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "snap.db")
	d, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`CREATE TABLE x (a)`); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBackupPluginDataWhileWriterActiveRestoresIdenticalRows(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugin-data")
	w := pdOpenPlugin(t, root, "shop-a")
	for i := 0; i < 200; i++ {
		if _, err := w.Exec(`INSERT INTO t (v) VALUES (?)`, fmt.Sprint("row", i)); err != nil {
			t.Fatal(err)
		}
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { // writer stays active during the backup
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				_, _ = w.Exec(`INSERT INTO t (v) VALUES (?)`, "live")
			}
		}
	}()
	snap := pdSnapshotFile(t)
	err := EmbedBackupPluginData(context.Background(), snap, root)
	close(stop)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	// Restore onto a fresh data dir.
	fresh := filepath.Join(t.TempDir(), "plugin-data")
	n, err := RestoreBackupPluginData(snap, fresh)
	if err != nil || n != 1 {
		t.Fatalf("restore n=%d err=%v", n, err)
	}
	got := pdRows(t, filepath.Join(fresh, "shop-a", "plugin.db"))
	var want string
	for i := 1; i <= 200; i++ {
		want += fmt.Sprintf("%d=row%d;", i, i-1)
	}
	if len(got) < len(want) || got[:len(want)] != want {
		t.Fatalf("the 200 committed rows must be identical after restore; got %.80s...", got)
	}
	// Integrity of the restored file.
	d, _ := sql.Open("sqlite", "file:"+filepath.Join(fresh, "shop-a", "plugin.db")+"?mode=ro")
	defer d.Close()
	var ic string
	if err := d.QueryRow(`PRAGMA integrity_check`).Scan(&ic); err != nil || ic != "ok" {
		t.Fatalf("integrity %q %v", ic, err)
	}
	// Table dropped: a second boot is a no-op.
	if n, err := RestoreBackupPluginData(snap, fresh); n != 0 || err != nil {
		t.Fatalf("second restore n=%d err=%v", n, err)
	}
}

func TestBackupPluginDataCarriesBigBlobsWithNoSizeSkip(t *testing.T) {
	root := filepath.Join(t.TempDir(), "plugin-data")
	blobs := filepath.Join(root, "p1", "blobs")
	if err := os.MkdirAll(blobs, 0o700); err != nil {
		t.Fatal(err)
	}
	big := bytes.Repeat([]byte("abcdefgh"), (backupAssetMaxBytes+3<<20)/8) // > 13 MiB, spans chunks
	if err := os.WriteFile(filepath.Join(blobs, "big.bin"), big, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(blobs, "empty.bin"), nil, 0o600)
	_ = os.WriteFile(filepath.Join(blobs, "Bad Name"), []byte("x"), 0o600)
	snap := pdSnapshotFile(t)
	if err := EmbedBackupPluginData(context.Background(), snap, root); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(t.TempDir(), "plugin-data")
	if n, err := RestoreBackupPluginData(snap, fresh); err != nil || n != 2 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	got, err := os.ReadFile(filepath.Join(fresh, "p1", "blobs", "big.bin"))
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("big blob differs (err %v, len %d want %d)", err, len(got), len(big))
	}
	if fi, err := os.Stat(filepath.Join(fresh, "p1", "blobs", "empty.bin")); err != nil || fi.Size() != 0 {
		t.Fatalf("empty blob lost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(fresh, "p1", "blobs", "Bad Name")); err == nil {
		t.Fatal("invalid blob name must not be backed up")
	}
}

func TestBackupPluginDataSkipsSymlinksRemovedAndPreUpgrade(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	root := filepath.Join(t.TempDir(), "plugin-data")
	pdOpenPlugin(t, root, "p1")
	secret := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(secret, []byte("top"), 0o600)
	_ = os.MkdirAll(filepath.Join(root, "p1", "blobs"), 0o700)
	_ = os.Symlink(secret, filepath.Join(root, "p1", "blobs", "link"))
	_ = os.MkdirAll(filepath.Join(root, ".removed", "old"), 0o700)
	_ = os.WriteFile(filepath.Join(root, ".removed", "old", "plugin.db"), []byte("x"), 0o600)
	_ = os.Symlink(t.TempDir(), filepath.Join(root, "linkplugin"))
	snap := pdSnapshotFile(t)
	if err := EmbedBackupPluginData(context.Background(), snap, root); err != nil {
		t.Fatal(err)
	}
	d, _ := sql.Open("sqlite", snap)
	defer d.Close()
	rows, _ := d.Query(`SELECT DISTINCT path FROM ` + backupPluginDataTable)
	defer rows.Close()
	for rows.Next() {
		var p string
		_ = rows.Scan(&p)
		if p != "p1/plugin.db" {
			t.Fatalf("unexpected entry %q", p)
		}
	}
}

// hostile writes raw rows into a snapshot's plugin-data table.
func pdHostile(t *testing.T, rels ...string) string {
	t.Helper()
	snap := pdSnapshotFile(t)
	d, _ := sql.Open("sqlite", snap)
	defer d.Close()
	if _, err := d.Exec(`CREATE TABLE ` + backupPluginDataTable + ` (path TEXT NOT NULL, chunk INTEGER NOT NULL, mod INTEGER NOT NULL DEFAULT 0, data BLOB NOT NULL, PRIMARY KEY (path, chunk))`); err != nil {
		t.Fatal(err)
	}
	for _, r := range rels {
		if _, err := d.Exec(`INSERT INTO `+backupPluginDataTable+` (path, chunk, data) VALUES (?, 0, 'evil')`, r); err != nil {
			t.Fatal(err)
		}
	}
	return snap
}

func TestRestorePluginDataRefusesTraversalAndOddEntries(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "data", "plugin-data")
	snap := pdHostile(t,
		"../evil/plugin.db", "p1/../../x/plugin.db", "/abs/plugin.db", `p1\..\x`, "C:/x/plugin.db",
		"p1/blobs/../../escape", "p1/blobs/sub/a", "p1/other.txt", ".removed/x/plugin.db",
		"p1/.pre-upgrade/1.0.0.db", "CON/plugin.db", "p1/blobs/nul", "p1/blobs/.", "../p1/blobs/a")
	n, err := RestoreBackupPluginData(snap, root)
	if n != 0 {
		t.Fatalf("no hostile entry may be written, wrote %d", n)
	}
	if err == nil {
		t.Fatal("refusals must be reported")
	}
	var found []string
	_ = filepath.WalkDir(base, func(p string, d os.DirEntry, _ error) error {
		if !d.IsDir() {
			found = append(found, p)
		}
		return nil
	})
	for _, f := range found {
		if filepath.Base(f) != "snap.db" && !bytes.Contains([]byte(f), []byte("snap.db")) {
			t.Fatalf("file written outside the archive rules: %s", f)
		}
	}
}

func TestRestorePluginDataRefusesSymlinkedTargets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks")
	}
	outside := t.TempDir()
	root := filepath.Join(t.TempDir(), "plugin-data")
	_ = os.MkdirAll(root, 0o700)
	// plugin dir is a symlink out of the root
	_ = os.Symlink(outside, filepath.Join(root, "linked"))
	// blobs dir is a symlink out
	_ = os.MkdirAll(filepath.Join(root, "p2"), 0o700)
	_ = os.Symlink(outside, filepath.Join(root, "p2", "blobs"))
	// the target file itself is a symlink
	_ = os.MkdirAll(filepath.Join(root, "p3"), 0o700)
	victim := filepath.Join(outside, "victim")
	_ = os.WriteFile(victim, []byte("orig"), 0o600)
	_ = os.Symlink(victim, filepath.Join(root, "p3", "plugin.db"))
	snap := pdHostile(t, "linked/plugin.db", "p2/blobs/a", "p3/plugin.db")
	n, err := RestoreBackupPluginData(snap, root)
	if n != 0 || err == nil {
		t.Fatalf("n=%d err=%v; every symlinked target must be refused", n, err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "orig" {
		t.Fatal("a symlink carried the restore outside plugin-data")
	}
	ents, _ := os.ReadDir(outside)
	if len(ents) != 1 {
		t.Fatalf("outside dir gained files: %v", ents)
	}
}

func TestRestorePluginDataNeverTouchesPluginsNotInTheArchive(t *testing.T) {
	src := filepath.Join(t.TempDir(), "plugin-data")
	a := pdOpenPlugin(t, src, "plug-a")
	_, _ = a.Exec(`INSERT INTO t (v) VALUES ('from-backup')`)
	snap := pdSnapshotFile(t)
	if err := EmbedBackupPluginData(context.Background(), snap, src); err != nil {
		t.Fatal(err)
	}
	// Live device: A has older/other data, B has its own data; archive has only A.
	live := filepath.Join(t.TempDir(), "plugin-data")
	bdb := pdOpenPlugin(t, live, "plug-b")
	_, _ = bdb.Exec(`INSERT INTO t (v) VALUES ('b-data')`)
	_ = bdb.Close()
	bPath := filepath.Join(live, "plug-b", "plugin.db")
	before, _ := os.ReadFile(bPath)
	// stale -wal beside A's old file must go away on replace
	_ = os.MkdirAll(filepath.Join(live, "plug-a"), 0o700)
	_ = os.WriteFile(filepath.Join(live, "plug-a", "plugin.db"), []byte("old"), 0o600)
	_ = os.WriteFile(filepath.Join(live, "plug-a", "plugin.db-wal"), []byte("stale"), 0o600)
	if n, err := RestoreBackupPluginData(snap, live); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	after, _ := os.ReadFile(bPath)
	if !bytes.Equal(before, after) {
		t.Fatal("plugin B's database changed though it is not in the archive")
	}
	if _, err := os.Stat(filepath.Join(live, "plug-a", "plugin.db-wal")); err == nil {
		t.Fatal("stale WAL of the replaced database must be removed")
	}
	if got := pdRows(t, filepath.Join(live, "plug-a", "plugin.db")); got != "1=from-backup;" {
		t.Fatalf("A rows = %q", got)
	}
	// permissions
	if runtime.GOOS != "windows" {
		fi, _ := os.Stat(filepath.Join(live, "plug-a", "plugin.db"))
		if fi.Mode().Perm() != 0o600 {
			t.Fatalf("mode %v", fi.Mode())
		}
	}
	_ = context.Background()
}

func TestSnapshotWithAssetsCarriesPluginData(t *testing.T) {
	data := t.TempDir()
	assets := filepath.Join(data, "public", "assets")
	_ = os.MkdirAll(assets, 0o755)
	w := pdOpenPlugin(t, filepath.Join(data, "plugin-data"), "p1")
	_, _ = w.Exec(`INSERT INTO t (v) VALUES ('kept')`)
	dbPath := filepath.Join(data, "till.db")
	database, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	snap, err := SnapshotWithAssets(database.DB, dbPath, assets)
	if err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(t.TempDir(), "plugin-data")
	if n, err := RestoreBackupPluginData(snap, fresh); err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if got := pdRows(t, filepath.Join(fresh, "p1", "plugin.db")); got != "1=kept;" {
		t.Fatalf("rows %q", got)
	}
}
