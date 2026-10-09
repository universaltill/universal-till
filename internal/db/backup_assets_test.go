package db

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeAsset writes rel (forward slashes) under root with body.
func writeAsset(t *testing.T, root, rel string, body []byte) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func readAsset(t *testing.T, root, rel string) ([]byte, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if os.IsNotExist(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	return b, true
}

func hasAssetsTable(t *testing.T, dbFile string) bool {
	t.Helper()
	sd, err := sql.Open("sqlite", dbFile)
	if err != nil {
		t.Fatal(err)
	}
	defer sd.Close()
	var n int
	if err := sd.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = ?`, backupAssetsTable).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// ut-docs#2724: a backup carries the uploaded item/category photos and the
// receipt logo inside the one .db file, and a restore puts them back — on a
// fresh device whose assets tree is empty, too.
func TestSnapshotWithAssets_RestoresPhotosOntoFreshDevice(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	assets := filepath.Join(t.TempDir(), "assets")
	writeAsset(t, assets, "items/it-1/thumb.png", []byte("item-photo"))
	writeAsset(t, assets, "items/it-1/variants/v-1/thumb.png", []byte("variant-photo"))
	writeAsset(t, assets, "categories/cat-1/thumb.png", []byte("category-photo"))
	writeAsset(t, assets, "logo/receipt-logo.png", []byte("logo"))
	// Not uploaded photos: a replica's in-flight sync temp file and an
	// unrelated tree are left out.
	writeAsset(t, assets, "items/it-2/thumb.png.sync-tmp", []byte("partial"))
	writeAsset(t, assets, "icons/builtin.svg", []byte("<svg/>"))

	snap, err := SnapshotWithAssets(d.DB, path, assets)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !hasAssetsTable(t, snap) {
		t.Fatal("snapshot must carry the photos table")
	}
	if hasAssetsTable(t, path) {
		t.Fatal("the live database must never gain the photos table")
	}

	// A fresh device: new data dir, empty assets tree, the downloaded
	// backup dropped into its backups folder.
	freshDB := testDBPath(t)
	freshAssets := filepath.Join(t.TempDir(), "assets")
	dir, err := BackupDir(freshDB)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, filepath.Base(snap)), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := StageRestore(freshDB, filepath.Base(snap)); err != nil {
		t.Fatalf("stage: %v", err)
	}
	if applied, err := ApplyPendingRestore(freshDB); err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	n, err := RestoreBackupAssets(freshDB, freshAssets)
	if err != nil {
		t.Fatalf("restore assets: %v", err)
	}
	if n != 4 {
		t.Errorf("restored %d files, want 4", n)
	}
	for rel, want := range map[string]string{
		"items/it-1/thumb.png":              "item-photo",
		"items/it-1/variants/v-1/thumb.png": "variant-photo",
		"categories/cat-1/thumb.png":        "category-photo",
		"logo/receipt-logo.png":             "logo",
	} {
		got, ok := readAsset(t, freshAssets, rel)
		if !ok || string(got) != want {
			t.Errorf("%s = %q (present %v), want %q", rel, got, ok, want)
		}
	}
	for _, rel := range []string{"items/it-2/thumb.png.sync-tmp", "icons/builtin.svg"} {
		if _, ok := readAsset(t, freshAssets, rel); ok {
			t.Errorf("%s must not be in the backup", rel)
		}
	}
	if hasAssetsTable(t, freshDB) {
		t.Error("the restored live database must not keep the photos table")
	}
	// The DB the till then opens is a normal, migrated database.
	openTest(t, freshDB)

	// Idempotent: nothing left to do on the next boot.
	if n, err := RestoreBackupAssets(freshDB, freshAssets); err != nil || n != 0 {
		t.Errorf("second run: %d %v", n, err)
	}
}

// Restore overwrites same-path photos with the backup's and leaves every
// other file alone (ut-docs#2724 AC 3).
func TestRestoreBackupAssets_OverwritesButNeverDeletes(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	assets := filepath.Join(t.TempDir(), "assets")
	writeAsset(t, assets, "items/it-1/thumb.png", []byte("old"))
	snap, err := SnapshotWithAssets(d.DB, path, assets)
	if err != nil {
		t.Fatal(err)
	}
	writeAsset(t, assets, "items/it-1/thumb.png", []byte("new"))
	writeAsset(t, assets, "items/it-9/thumb.png", []byte("later"))

	if err := StageRestore(path, filepath.Base(snap)); err != nil {
		t.Fatal(err)
	}
	d.Close()
	if _, err := ApplyPendingRestore(path); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreBackupAssets(path, assets); err != nil {
		t.Fatal(err)
	}
	if got, _ := readAsset(t, assets, "items/it-1/thumb.png"); string(got) != "old" {
		t.Errorf("it-1 = %q, want the backup's", got)
	}
	if got, ok := readAsset(t, assets, "items/it-9/thumb.png"); !ok || string(got) != "later" {
		t.Errorf("a photo not in the backup must be left alone, got %q %v", got, ok)
	}
}

// A backup made before ut-docs#2724 (no photos table) restores exactly as
// before, and an empty assets tree makes a valid backup.
func TestRestoreBackupAssets_OldBackupAndNoPhotos(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	snap, err := Snapshot(d.DB, path)
	if err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(t.TempDir(), "missing")
	if n, err := RestoreBackupAssets(snap, assets); err != nil || n != 0 {
		t.Fatalf("old backup: %d %v", n, err)
	}
	if _, err := os.Stat(assets); !os.IsNotExist(err) {
		t.Error("an old backup must not create the assets tree")
	}
	// Move the first file aside so a same-second Snapshot can't hand back
	// the same file to the second call.
	if err := os.Rename(snap, snap+".old"); err != nil {
		t.Fatal(err)
	}
	snap2, err := SnapshotWithAssets(d.DB, path, assets)
	if err != nil {
		t.Fatalf("snapshot with no photos: %v", err)
	}
	if n, err := RestoreBackupAssets(snap2, assets); err != nil || n != 0 {
		t.Fatalf("no photos: %d %v", n, err)
	}
	if n, err := RestoreBackupAssets(filepath.Join(t.TempDir(), "absent.db"), assets); err != nil || n != 0 {
		t.Fatalf("no database yet: %d %v", n, err)
	}
}

// A backup file can be dropped into the backups folder by hand, so every
// stored path is checked before it is joined onto the assets root.
func TestRestoreBackupAssets_RefusesUnsafePaths(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	assets := filepath.Join(t.TempDir(), "data", "public", "assets")
	writeAsset(t, assets, "items/ok/thumb.png", []byte("ok"))
	snap, err := SnapshotWithAssets(d.DB, path, assets)
	if err != nil {
		t.Fatal(err)
	}
	sd, err := sql.Open("sqlite", snap)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"../escape.png", "items/../../escape.png", "/etc/escape.png",
		`items\..\..\escape.png`, "C:escape.png", "plugins/x/evil.wasm", "items", "",
		"items/x\x00y.png", "items/x\ny.png",
	} {
		if _, err := sd.Exec(`INSERT INTO `+backupAssetsTable+` (path, data) VALUES (?, ?)`, bad, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	sd.Close()

	n, err := RestoreBackupAssets(snap, assets)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("restored %d files, want only the safe one", n)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(assets), "escape.png")); err == nil {
		t.Error("a path escaped the assets root")
	}
	if _, err := os.Stat(filepath.Join(assets, "plugins")); err == nil {
		t.Error("a path outside the photo trees was written")
	}
	if hasAssetsTable(t, snap) {
		t.Error("the table must be dropped even when some rows were refused")
	}
}

// Files over the upload cap are left out rather than bloating every snapshot.
func TestSnapshotWithAssets_SkipsOversizeFiles(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	assets := filepath.Join(t.TempDir(), "assets")
	writeAsset(t, assets, "items/big/thumb.png", bytes.Repeat([]byte{1}, backupAssetMaxBytes+1))
	writeAsset(t, assets, "items/small/thumb.png", []byte("s"))
	snap, err := SnapshotWithAssets(d.DB, path, assets)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "assets")
	if n, err := RestoreBackupAssets(snap, out); err != nil || n != 1 {
		t.Fatalf("restored %d %v, want 1", n, err)
	}
	if _, ok := readAsset(t, out, "items/big/thumb.png"); ok {
		t.Error("an oversize file must not be in the backup")
	}
}

// One photo that can't be written must not keep the table: kept, every boot
// would re-write the backup's photos over newer uploads (review finding).
func TestRestoreBackupAssets_WriteFailureStillDropsTable(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	assets := filepath.Join(t.TempDir(), "assets")
	writeAsset(t, assets, "items/a/thumb.png", []byte("a-old"))
	writeAsset(t, assets, "items/b/thumb.png", []byte("b"))
	snap, err := SnapshotWithAssets(d.DB, path, assets)
	if err != nil {
		t.Fatal(err)
	}
	// A directory where the photo file should go: that write fails.
	if err := os.RemoveAll(filepath.Join(assets, "items", "b")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(assets, "items", "b", "thumb.png"), 0o755); err != nil {
		t.Fatal(err)
	}
	n, err := RestoreBackupAssets(snap, assets)
	if err == nil || n != 1 {
		t.Fatalf("want one photo written and the failure reported, got %d %v", n, err)
	}
	if hasAssetsTable(t, snap) {
		t.Fatal("the table must be dropped after one attempt")
	}
	// A photo uploaded after the restore survives the next boot.
	writeAsset(t, assets, "items/a/thumb.png", []byte("a-new"))
	if n, err := RestoreBackupAssets(snap, assets); err != nil || n != 0 {
		t.Fatalf("next boot: %d %v", n, err)
	}
	if got, _ := readAsset(t, assets, "items/a/thumb.png"); string(got) != "a-new" {
		t.Errorf("newer upload clobbered: %q", got)
	}
}

// A symlinked directory planted in the photo tree must not carry a restored
// file outside it (review finding).
func TestRestoreBackupAssets_RefusesSymlinkEscape(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	assets := filepath.Join(t.TempDir(), "assets")
	writeAsset(t, assets, "items/it-1/thumb.png", []byte("p"))
	snap, err := SnapshotWithAssets(d.DB, path, assets)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(assets, "items", "it-1")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(assets, "items", "it-1")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if n, err := RestoreBackupAssets(snap, assets); err != nil || n != 0 {
		t.Fatalf("restored %d %v, want the escaping file refused", n, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "thumb.png")); err == nil {
		t.Fatal("a restored file escaped through a symlinked directory")
	}
}

// A shop whose photo tree is a symlink (another drive) still gets its photos
// backed up (review finding).
func TestSnapshotWithAssets_FollowsSymlinkedScopeRoot(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	assets := filepath.Join(t.TempDir(), "assets")
	external := t.TempDir()
	writeAsset(t, external, "it-1/thumb.png", []byte("ext"))
	if err := os.MkdirAll(assets, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(assets, "items")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	snap, err := SnapshotWithAssets(d.DB, path, assets)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "assets")
	if n, err := RestoreBackupAssets(snap, out); err != nil || n != 1 {
		t.Fatalf("restored %d %v, want 1", n, err)
	}
	if got, _ := readAsset(t, out, "items/it-1/thumb.png"); string(got) != "ext" {
		t.Errorf("got %q", got)
	}
}

// A same-second snapshot reuses the file; a photo deleted in between must
// not survive in it (review finding).
func TestSnapshotWithAssets_RebuildsTableOnReuse(t *testing.T) {
	path := testDBPath(t)
	d := openTest(t, path)
	assets := filepath.Join(t.TempDir(), "assets")
	writeAsset(t, assets, "items/gone/thumb.png", []byte("x"))
	if _, err := SnapshotWithAssets(d.DB, path, assets); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(assets, "items", "gone")); err != nil {
		t.Fatal(err)
	}
	snap, err := SnapshotWithAssets(d.DB, path, assets)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "assets")
	if n, err := RestoreBackupAssets(snap, out); err != nil || n != 0 {
		t.Fatalf("restored %d %v, want the deleted photo gone", n, err)
	}
}

// BackupLacksPhotos (ut-docs#3993): a snapshot with the photo table never
// lacks photos; one without it lacks them only if the shop has uploads.
func TestBackupLacksPhotos(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "till.db")
	d, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	assets := filepath.Join(t.TempDir(), "assets")
	writeAsset(t, assets, "items/x.jpg", []byte("jpg"))

	withTable, err := SnapshotWithAssets(d.DB, dbPath, assets)
	if err != nil {
		t.Fatal(err)
	}
	got, err := BackupLacksPhotos(withTable, assets)
	if err != nil || got {
		t.Fatalf("snapshot with table: lacks=%v err=%v, want false", got, err)
	}

	// A plain snapshot (no table), e.g. made before photos were embedded.
	time.Sleep(1100 * time.Millisecond) // Snapshot names are per-second
	plain, err := Snapshot(d.DB, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(plain)
	stBefore, _ := os.Stat(plain)
	got, err = BackupLacksPhotos(plain, assets)
	if err != nil || !got {
		t.Fatalf("no table + photos: lacks=%v err=%v, want true", got, err)
	}
	after, _ := os.ReadFile(plain)
	stAfter, _ := os.Stat(plain)
	if !bytes.Equal(before, after) || !stBefore.ModTime().Equal(stAfter.ModTime()) {
		t.Fatal("BackupLacksPhotos modified the snapshot file")
	}

	// Nothing embeddable: missing scope, tmp files, oversize.
	empty := filepath.Join(t.TempDir(), "assets")
	if got, err = BackupLacksPhotos(plain, empty); err != nil || got {
		t.Fatalf("no photos dir: lacks=%v err=%v, want false", got, err)
	}
	writeAsset(t, empty, "items/a.jpg.sync-tmp", []byte("x"))
	writeAsset(t, empty, "logo/b.png.restore-tmp", []byte("x"))
	writeAsset(t, empty, "categories/big.jpg", make([]byte, backupAssetMaxBytes+1))
	writeAsset(t, empty, "other/c.jpg", []byte("x")) // not an uploaded scope
	if got, err = BackupLacksPhotos(plain, empty); err != nil || got {
		t.Fatalf("only tmp/oversize/other: lacks=%v err=%v, want false", got, err)
	}

	// Symlinked scope root is resolved like embedBackupAssets does.
	link := filepath.Join(t.TempDir(), "assets")
	real := t.TempDir()
	writeAsset(t, real, "p.jpg", []byte("x"))
	if err := os.MkdirAll(link, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(link, "items")); err != nil {
		t.Fatal(err)
	}
	if got, err = BackupLacksPhotos(plain, link); err != nil || !got {
		t.Fatalf("symlinked scope: lacks=%v err=%v, want true", got, err)
	}

	if _, err := BackupLacksPhotos(filepath.Join(t.TempDir(), "missing.db"), assets); err == nil {
		t.Fatal("missing snapshot must return an error")
	}
}

// An unreadable photo tree is the likeliest reason a backup has no photos,
// so with the table absent it must still report "lacks", with the cause
// (ut-docs#3993 review finding 1).
func TestBackupLacksPhotos_UnreadableTreeCountsAsLacking(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0000 directory; CI runs as a normal user")
	}
	dbPath := filepath.Join(t.TempDir(), "till.db")
	d, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	plain, err := Snapshot(d.DB, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	assets := filepath.Join(t.TempDir(), "assets")
	writeAsset(t, assets, "items/sub/x.jpg", []byte("jpg"))
	locked := filepath.Join(assets, "items", "sub")
	if err := os.Chmod(locked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	got, err := BackupLacksPhotos(plain, assets)
	if !got || err == nil {
		t.Fatalf("unreadable photo tree: lacks=%v err=%v, want true with an error", got, err)
	}
}
