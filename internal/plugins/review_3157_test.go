package plugins

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db/plugindb"
)

// Reviewer tests for ut-docs#3157 slice B (docs/code-reviews/
// 2026-11-28-plugin-sqlite-slice-b-3157.md, findings R1, R2, R4, R6).

const reviewInitSQL = "CREATE TABLE notes (v TEXT);"

// R1: ADR-0121 §8 "a reinstall restores it". A fresh install of a plugin
// whose data sits in .removed must get that data back, not an empty database
// that then blocks the restore for good.
func TestReinstallRestoresKeptData_3157(t *testing.T) {
	ctx := context.Background()
	mgr := plugindb.NewManager(t.TempDir(), nil)
	defer mgr.CloseAll()
	tree := writeCodeTree(t, "db/migrations", map[string]string{"0001_init.sql": reviewInitSQL})
	man := dbManifest("1.0.0", "db/migrations")

	if _, err := migrateDataWith(ctx, mgr, man, tree, ""); err != nil {
		t.Fatal(err)
	}
	st, err := mgr.Open(ctx, dbTestPlugin, plugindb.Options{NoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.Exec(ctx, "INSERT INTO notes(v) VALUES ('kept-across-uninstall')"); err != nil {
		t.Fatal(err)
	}
	if dst, err := mgr.UninstallRetained(dbTestPlugin, 10, "plugin.x.reason"); err != nil || dst == "" {
		t.Fatalf("uninstall: %q, %v", dst, err)
	}

	// Reinstall: fromVersion is "" because the plugin row is gone.
	if _, err := migrateDataWith(ctx, mgr, man, tree, ""); err != nil {
		t.Fatal(err)
	}
	if got := queryRows(t, mgr, "SELECT v FROM notes"); !strings.Contains(got, "kept-across-uninstall") {
		t.Fatalf("reinstall did not restore the kept data: %s", got)
	}
	if _, err := mgr.RemovedInfo(dbTestPlugin); !errors.Is(err, plugindb.ErrNotRemoved) {
		t.Fatalf("the restored tree is still listed as removed: %v", err)
	}
}

// R1: an upgrade (fromVersion set) never pulls an older removed tree over
// live data, and a plugin with no db block still gets its blobs tree back.
func TestReinstallRestoreOnlyOnFreshInstall_3157(t *testing.T) {
	ctx := context.Background()
	mgr := plugindb.NewManager(t.TempDir(), nil)
	defer mgr.CloseAll()
	tree := writeCodeTree(t, "db/migrations", map[string]string{"0001_init.sql": reviewInitSQL})
	man := dbManifest("1.0.0", "db/migrations")
	if _, err := migrateDataWith(ctx, mgr, man, tree, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.UninstallRetained(dbTestPlugin, 0, ""); err != nil {
		t.Fatal(err)
	}
	// An upgrade path call must not restore.
	if err := restoreRemovedDataForInstall(mgr, dbTestPlugin, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.RemovedInfo(dbTestPlugin); err != nil {
		t.Fatalf("an upgrade restored removed data: %v", err)
	}
	// No db block: the tree (blobs) still comes back, and no DB is migrated.
	up, err := migrateDataWith(ctx, mgr, &Manifest{ID: dbTestPlugin, Version: "1.0.0"}, t.TempDir(), "")
	if up != nil || err != nil {
		t.Fatalf("got %v, %v", up, err)
	}
	if _, err := mgr.RemovedInfo(dbTestPlugin); !errors.Is(err, plugindb.ErrNotRemoved) {
		t.Fatalf("fresh install of a no-db plugin left its data removed: %v", err)
	}
}

// R2: a rollback to a version that can read the live database as it is keeps
// it. Restoring the pre-upgrade copy there would silently set aside every row
// written since the upgrade.
func TestRollbackKeepsLiveDataWhenTargetCanReadIt_3157(t *testing.T) {
	ctx := context.Background()
	m1 := plugindb.Migration{Version: 1, Name: "init", SQL: "CREATE TABLE notes (v TEXT)"}
	root := t.TempDir()
	mgr := plugindb.NewManager(root, nil)
	restore := SetDataManagerForTest(mgr)
	defer func() { mgr.CloseAll(); restore() }()
	id := "com.t.keep"
	ins := func(v string) {
		t.Helper()
		s, err := mgr.Open(ctx, id, plugindb.Options{NoMigrate: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.Exec(ctx, "INSERT INTO notes(v) VALUES (?)", v); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mgr.MigrateForInstall(ctx, id, "", "1.0.0", plugindb.Options{Migrations: []plugindb.Migration{m1}}); err != nil {
		t.Fatal(err)
	}
	ins("before-upgrade")
	// 1.1.0 ships the same schema: a pre-upgrade copy 1.0.0.db is still taken.
	if _, err := mgr.MigrateForInstall(ctx, id, "1.0.0", "1.1.0", plugindb.Options{Migrations: []plugindb.Migration{m1}}); err != nil {
		t.Fatal(err)
	}
	ins("sold-after-upgrade")

	dir, man := rbDataTarget(t, map[string]string{"0001_init.sql": m1.SQL})
	undo, err := (&RollbackManager{}).prepareDataForRollback(ctx, id, "1.0.0", dir, man)
	if err != nil || undo == nil {
		t.Fatalf("undo=%v err=%v", undo != nil, err)
	}
	got := rbCount(t, mgr, id)
	if !strings.Contains(got, "before-upgrade") || !strings.Contains(got, "sold-after-upgrade") {
		t.Fatalf("rollback dropped rows written since the upgrade: %s", got)
	}
	if m, _ := filepath.Glob(filepath.Join(root, id, ".pre-upgrade", "rolled-back-*.db")); len(m) != 0 {
		t.Fatalf("a compatible rollback set data aside: %v", m)
	}
}

// R4: a store closed by the idle sweep between Open and the statement is
// reopened once; the statement never ran, so nothing can be written twice.
func TestRetryClosedStoreReopensOnce_3157(t *testing.T) {
	ctx := context.Background()
	mgr := plugindb.NewManager(t.TempDir(), nil)
	defer mgr.CloseAll()
	id := "com.t.closed"
	m1 := plugindb.Migration{Version: 1, Name: "init", SQL: "CREATE TABLE notes (v TEXT)"}
	if _, err := mgr.MigrateForInstall(ctx, id, "", "1.0.0", plugindb.Options{Migrations: []plugindb.Migration{m1}}); err != nil {
		t.Fatal(err)
	}
	opens := 0
	open := func() (*plugindb.Store, error) {
		opens++
		st, err := mgr.Open(ctx, id, plugindb.Options{NoMigrate: true})
		if err == nil && opens == 1 {
			mgr.CloseIdle(-time.Hour) // the sweep wins the race: st is now closed
		}
		return st, err
	}
	runs := 0
	err := retryClosedStore(open, func(st *plugindb.Store) error {
		runs++
		_, e := st.Exec(ctx, "INSERT INTO notes(v) VALUES ('once')")
		return e
	})
	if err != nil || opens != 2 || runs != 2 {
		t.Fatalf("err=%v opens=%d runs=%d; want nil, 2, 2", err, opens, runs)
	}
	st, _ := mgr.Open(ctx, id, plugindb.Options{NoMigrate: true})
	if b, _ := st.Query(ctx, "SELECT count(*) AS n FROM notes"); string(b) != `[{"n":1}]` {
		t.Fatalf("rows = %s, want exactly one", b)
	}
	// A store that keeps reporting closed is not retried for ever.
	n := 0
	err = retryClosedStore(func() (*plugindb.Store, error) { n++; return st, nil },
		func(*plugindb.Store) error { return plugindb.ErrClosed })
	if !errors.Is(err, plugindb.ErrClosed) || n != 2 {
		t.Fatalf("err=%v opens=%d", err, n)
	}
}

// R6: five failed attempts abandon the handover for THAT plugin version only;
// an upgraded plugin (the fixed handler) is asked again from zero.
func TestHandoverIsAskedAgainAfterAnUpgrade_3157(t *testing.T) {
	d := handoverPlugin(t, "com.ho.fixed", HandoverEvent, "wasm")
	f := &fakeHandover{resp: `{"done":false}`}
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		now = now.Add(100 * time.Hour)
		if n := runHandoverPass(ctx, d.DB, f, now); n != 1 {
			t.Fatalf("attempt %d = %d", i+1, n)
		}
	}
	if n := runHandoverPass(ctx, d.DB, f, now.Add(1000*time.Hour)); n != 0 {
		t.Fatalf("a sixth attempt on the same version ran: %d", n)
	}
	up := providesManifest("com.ho.fixed", nil, nil)
	up.Version = "1.0.1"
	up.Hooks = []ManifestHook{{Event: HandoverEvent, Action: "run"}}
	if err := PersistManifest(ctx, d.DB, up, InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	mustExecSQL(t, d, `UPDATE plugins SET runtime='wasm' WHERE id='com.ho.fixed'`)
	// Asked at once (the old version's backoff does not apply) ...
	if n := runHandoverPass(ctx, d.DB, f, now.Add(time.Second)); n != 1 {
		t.Fatalf("upgraded plugin was not asked again: %d", n)
	}
	var attempts int
	var ver string
	if err := d.DB.QueryRow(`SELECT attempts, asked_version FROM plugin_handover WHERE plugin_id='com.ho.fixed'`).Scan(&attempts, &ver); err != nil || attempts != 1 || ver != "1.0.1" {
		t.Fatalf("attempts=%d version=%q err=%v; want 1, 1.0.1", attempts, ver, err)
	}
	// ... and the new version has its own backoff and can finish.
	if n := runHandoverPass(ctx, d.DB, f, now.Add(2*time.Second)); n != 0 {
		t.Fatalf("new version ignored its backoff: %d", n)
	}
	f.resp = `{"done":true}`
	if n := runHandoverPass(ctx, d.DB, f, now.Add(100*time.Hour)); n != 1 {
		t.Fatalf("retry = %d", n)
	}
	if done, _ := handoverDone(ctx, d.DB, "com.ho.fixed"); !done {
		t.Fatal("not marked done")
	}
}
