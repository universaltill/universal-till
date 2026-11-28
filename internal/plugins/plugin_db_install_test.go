package plugins

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db/plugindb"
)

const dbTestPlugin = "com.test.dbplugin"

func dbManifest(version, dir string) *Manifest {
	return &Manifest{ID: dbTestPlugin, Version: version, DB: &ManifestDB{Migrations: dir}}
}

// writeCodeTree lays out an extracted plugin tree with migrations under dir.
func writeCodeTree(t *testing.T, dir string, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func queryRows(t *testing.T, mgr *plugindb.Manager, q string) string {
	t.Helper()
	st, err := mgr.Open(context.Background(), dbTestPlugin, plugindb.Options{NoMigrate: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	b, err := st.Query(context.Background(), q)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	return string(b)
}

func TestMigrateDataNoDBBlockTouchesNothing(t *testing.T) {
	root := t.TempDir()
	mgr := plugindb.NewManager(root, nil)
	up, err := migrateDataWith(context.Background(), mgr, &Manifest{ID: dbTestPlugin, Version: "1.0.0"}, t.TempDir(), "")
	if up != nil || err != nil {
		t.Fatalf("got %v, %v; want nil, nil", up, err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Fatalf("data root not empty: %v", entries)
	}
}

func TestMigrateDataFailureKeepsOldDatabaseByteForByte(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mgr := plugindb.NewManager(root, nil)

	v1 := writeCodeTree(t, "migrations", map[string]string{
		"0001_init.sql": "CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT); INSERT INTO notes(body) VALUES ('kept');",
	})
	up1, err := migrateDataWith(ctx, mgr, dbManifest("1.0.0", "migrations"), v1, "")
	if err != nil || up1 == nil {
		t.Fatalf("v1 migrate: %v %v", up1, err)
	}
	if err := mgr.Close(dbTestPlugin); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(root, dbTestPlugin, plugindb.DBFileName)
	before, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	// v2: second migration fails halfway (valid statement, then a bad one).
	v2 := writeCodeTree(t, "migrations", map[string]string{
		"0001_init.sql":  "CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT); INSERT INTO notes(body) VALUES ('kept');",
		"0002_break.sql": "ALTER TABLE notes ADD COLUMN extra TEXT; INSERT INTO no_such_table VALUES (1);",
	})
	up2, err := migrateDataWith(ctx, mgr, dbManifest("2.0.0", "migrations"), v2, "1.0.0")
	if err == nil || up2 != nil {
		t.Fatalf("v2 migrate = %v, %v; want error and no Upgrade", up2, err)
	}
	if !strings.Contains(err.Error(), "previous version kept") {
		t.Fatalf("error should say the previous version is kept: %v", err)
	}
	after, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("database changed after a failed migration")
	}
	if got := queryRows(t, mgr, "SELECT body FROM notes"); !strings.Contains(got, "kept") {
		t.Fatalf("rows = %s", got)
	}
}

func TestMigrateDataUndoRestoresPreUpgradeCopy(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	mgr := plugindb.NewManager(root, nil)
	v1 := writeCodeTree(t, "migrations", map[string]string{
		"0001_init.sql": "CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT); INSERT INTO notes(body) VALUES ('one');",
	})
	if _, err := migrateDataWith(ctx, mgr, dbManifest("1.0.0", "migrations"), v1, ""); err != nil {
		t.Fatal(err)
	}
	v2 := writeCodeTree(t, "migrations", map[string]string{
		"0001_init.sql": "CREATE TABLE notes(id INTEGER PRIMARY KEY, body TEXT); INSERT INTO notes(body) VALUES ('one');",
		"0002_more.sql": "CREATE TABLE extra(x INTEGER);",
	})
	up, err := migrateDataWith(ctx, mgr, dbManifest("2.0.0", "migrations"), v2, "1.0.0")
	if err != nil || up == nil {
		t.Fatalf("v2: %v %v", up, err)
	}
	if got := queryRows(t, mgr, "SELECT name FROM sqlite_master WHERE name='extra'"); !strings.Contains(got, "extra") {
		t.Fatalf("v2 table missing: %s", got)
	}
	undoDataMigration(up, dbTestPlugin) // a later install step failed
	if got := queryRows(t, mgr, "SELECT name FROM sqlite_master WHERE name='extra'"); strings.Contains(got, "extra") {
		t.Fatalf("v2 table still present after undo: %s", got)
	}
	if got := queryRows(t, mgr, "SELECT body FROM notes"); !strings.Contains(got, "one") {
		t.Fatalf("rows lost: %s", got)
	}
}

func TestMigrateDataRefusesBadMigrationPaths(t *testing.T) {
	ctx := context.Background()
	dataRoot := t.TempDir()
	mgr := plugindb.NewManager(dataRoot, nil)
	good := map[string]string{"0001_init.sql": "CREATE TABLE t(x INTEGER);"}

	for _, dir := range []string{"../escape", "/abs/path", "a/../../b", ".", "a\\b", ""} {
		root := writeCodeTree(t, "migrations", good)
		if _, err := migrateDataWith(ctx, mgr, dbManifest("1.0.0", dir), root, ""); err == nil {
			t.Errorf("dir %q accepted", dir)
		}
	}

	// A migrations dir that is a symlink out of the tree.
	outside := writeCodeTree(t, "m", good)
	root := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, "m"), filepath.Join(root, "migrations")); err != nil {
		t.Skip("symlinks unavailable")
	}
	if _, err := migrateDataWith(ctx, mgr, dbManifest("1.0.0", "migrations"), root, ""); err == nil {
		t.Error("symlinked migrations dir accepted")
	}

	// Bad file names / non-regular entries inside the dir.
	for _, name := range []string{"../0001_x.sql", "0001_x.sql.bak", "x.sql", "0003_gap.sql"} {
		files := map[string]string{"0001_init.sql": "CREATE TABLE t(x INTEGER);"}
		if !strings.Contains(name, "..") {
			files[name] = "CREATE TABLE u(x INTEGER);"
		}
		root := writeCodeTree(t, "migrations", files)
		if strings.Contains(name, "..") {
			continue // cannot be created on disk; zip-slip is refused at extraction
		}
		if _, err := migrateDataWith(ctx, mgr, dbManifest("1.0.0", "migrations"), root, ""); err == nil {
			t.Errorf("file %q accepted", name)
		}
	}

	// A symlinked migration file is refused (not regular).
	root = writeCodeTree(t, "migrations", good)
	target := filepath.Join(t.TempDir(), "evil.sql")
	_ = os.WriteFile(target, []byte("CREATE TABLE evil(x INTEGER);"), 0o644)
	if err := os.Symlink(target, filepath.Join(root, "migrations", "0002_evil.sql")); err == nil {
		if _, err := migrateDataWith(ctx, mgr, dbManifest("1.0.0", "migrations"), root, ""); err == nil {
			t.Error("symlinked migration file accepted")
		}
	}

	// No file was created for a plugin whose migrations were refused.
	if _, err := os.Stat(filepath.Join(dataRoot, dbTestPlugin, plugindb.DBFileName)); err == nil {
		t.Error("a database file exists after refused migrations")
	}
}

func TestMigrateDataRefusesHostileSQLAndDowngrade(t *testing.T) {
	ctx := context.Background()
	mgr := plugindb.NewManager(t.TempDir(), nil)
	evil := writeCodeTree(t, "migrations", map[string]string{
		"0001_evil.sql": "ATTACH DATABASE '/tmp/x' AS x;",
	})
	if _, err := migrateDataWith(ctx, mgr, dbManifest("1.0.0", "migrations"), evil, ""); err == nil {
		t.Fatal("ATTACH migration accepted")
	}

	two := writeCodeTree(t, "migrations", map[string]string{
		"0001_a.sql": "CREATE TABLE a(x INTEGER);",
		"0002_b.sql": "CREATE TABLE b(x INTEGER);",
	})
	if _, err := migrateDataWith(ctx, mgr, dbManifest("2.0.0", "migrations"), two, ""); err != nil {
		t.Fatal(err)
	}
	one := writeCodeTree(t, "migrations", map[string]string{"0001_a.sql": "CREATE TABLE a(x INTEGER);"})
	_, err := migrateDataWith(ctx, mgr, dbManifest("1.0.0", "migrations"), one, "2.0.0")
	if !errors.Is(err, plugindb.ErrMigrationBehind) {
		t.Fatalf("downgrade install = %v, want ErrMigrationBehind", err)
	}
}

func TestMigrationsNeedDigestCoveredArtifact(t *testing.T) {
	m := dbManifest("1.0.0", "migrations")
	if err := requireDigestCoveredMigrations(m, false); !errors.Is(err, errMigrationsNotCovered) {
		t.Fatalf("uncovered = %v", err)
	}
	if err := requireDigestCoveredMigrations(m, true); err != nil {
		t.Fatal(err)
	}
	if err := requireDigestCoveredMigrations(&Manifest{}, false); err != nil {
		t.Fatalf("no db block must not need coverage: %v", err)
	}
}

func TestImportRefusesMigrationsWithoutArtifactHash(t *testing.T) {
	if UnsignedPluginsAllowed() {
		t.Skip("devplugins build skips the digest requirement")
	}
	imp, base := newTestImporter(t)
	for name, hash := range map[string]string{"none": "", "wrong": "sha256:" + strings.Repeat("0", 64)} {
		archive := filepath.Join(t.TempDir(), "p.zip")
		manifest := `{"id":"com.test.dbimport","name":"Import Test","version":"1.0.0","entrypoint":"./run","runtime":"none","canonical_type":"page","device_arch":"any","artifact_hash":"` + hash + `","db":{"migrations":"migrations"}}`
		writeZipArchive(t, archive, map[string][]byte{
			"manifest.json":            []byte(manifest),
			"migrations/0001_init.sql": []byte("CREATE TABLE t(x INTEGER);"),
		})
		_, err := imp.Import(context.Background(), &ImportRequest{FilePath: archive, TrustLevel: "untrusted", Uploader: "t"})
		if !errors.Is(err, errMigrationsNotCovered) {
			t.Fatalf("%s hash: err = %v, want errMigrationsNotCovered", name, err)
		}
		if _, statErr := os.Stat(filepath.Join(base, "com.test.dbimport")); statErr == nil {
			t.Fatalf("%s hash: plugin dir created", name)
		}
	}
}
