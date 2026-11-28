package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db/plugindb"
)

func rbDataTarget(t *testing.T, migs map[string]string) (string, *Manifest) {
	t.Helper()
	dir := t.TempDir()
	md := filepath.Join(dir, "db", "migrations")
	if err := os.MkdirAll(md, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, sql := range migs {
		if err := os.WriteFile(filepath.Join(md, n), []byte(sql), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir, &Manifest{DB: &ManifestDB{Migrations: "db/migrations"}}
}

func rbCount(t *testing.T, m *plugindb.Manager, id string) string {
	t.Helper()
	ctx := context.Background()
	s, err := m.Open(ctx, id, plugindb.Options{NoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Query(ctx, "SELECT group_concat(v) AS v FROM notes")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPrepareDataForRollback_3157(t *testing.T) {
	ctx := context.Background()
	m1 := plugindb.Migration{Version: 1, Name: "init", SQL: "CREATE TABLE notes (v TEXT)"}
	m2 := plugindb.Migration{Version: 2, Name: "more", SQL: "ALTER TABLE notes ADD COLUMN extra TEXT"}
	rm := &RollbackManager{}
	setup := func(t *testing.T) (*plugindb.Manager, func()) {
		mgr := plugindb.NewManager(t.TempDir(), nil)
		restore := SetDataManagerForTest(mgr)
		return mgr, func() { mgr.CloseAll(); restore() }
	}

	t.Run("no db block touches nothing", func(t *testing.T) {
		_, done := setup(t)
		defer done()
		undo, err := rm.prepareDataForRollback(ctx, "com.t.a", "1.0.0", t.TempDir(), &Manifest{})
		if err != nil || undo == nil {
			t.Fatalf("undo=%v err=%v", undo != nil, err)
		}
	})

	t.Run("restores pre-upgrade copy and undo reinstates newer data", func(t *testing.T) {
		mgr, done := setup(t)
		defer done()
		id := "com.t.b"
		if _, err := mgr.MigrateForInstall(ctx, id, "", "1.0.0", plugindb.Options{Migrations: []plugindb.Migration{m1}}); err != nil {
			t.Fatal(err)
		}
		s, _ := mgr.Open(ctx, id, plugindb.Options{NoMigrate: true})
		if _, err := s.Exec(ctx, "INSERT INTO notes(v) VALUES ('old')"); err != nil {
			t.Fatal(err)
		}
		if _, err := mgr.MigrateForInstall(ctx, id, "1.0.0", "1.1.0", plugindb.Options{Migrations: []plugindb.Migration{m1, m2}}); err != nil {
			t.Fatal(err)
		}
		s, _ = mgr.Open(ctx, id, plugindb.Options{NoMigrate: true})
		if _, err := s.Exec(ctx, "INSERT INTO notes(v) VALUES ('new')"); err != nil {
			t.Fatal(err)
		}
		dir, man := rbDataTarget(t, map[string]string{"0001_init.sql": m1.SQL})
		undo, err := rm.prepareDataForRollback(ctx, id, "1.0.0", dir, man)
		if err != nil {
			t.Fatal(err)
		}
		if got := rbCount(t, mgr, id); !strings.Contains(got, "old") || strings.Contains(got, "new") {
			t.Fatalf("after rollback: %s", got)
		}
		if err := undo(); err != nil {
			t.Fatal(err)
		}
		if got := rbCount(t, mgr, id); !strings.Contains(got, "new") {
			t.Fatalf("after undo: %s", got)
		}
	})

	t.Run("no pre-upgrade copy and db ahead of target is refused", func(t *testing.T) {
		mgr, done := setup(t)
		defer done()
		id := "com.t.c"
		if _, err := mgr.MigrateForInstall(ctx, id, "", "2.0.0", plugindb.Options{Migrations: []plugindb.Migration{m1, m2}}); err != nil {
			t.Fatal(err)
		}
		dir, man := rbDataTarget(t, map[string]string{"0001_init.sql": m1.SQL})
		undo, err := rm.prepareDataForRollback(ctx, id, "1.0.0", dir, man)
		if !errors.Is(err, plugindb.ErrMigrationBehind) || undo != nil {
			t.Fatalf("undo=%v err=%v", undo != nil, err)
		}
	})

	t.Run("no pre-upgrade copy but compatible db is allowed", func(t *testing.T) {
		mgr, done := setup(t)
		defer done()
		id := "com.t.d"
		if _, err := mgr.MigrateForInstall(ctx, id, "", "1.0.0", plugindb.Options{Migrations: []plugindb.Migration{m1}}); err != nil {
			t.Fatal(err)
		}
		dir, man := rbDataTarget(t, map[string]string{"0001_init.sql": m1.SQL, "0002_more.sql": m2.SQL})
		if undo, err := rm.prepareDataForRollback(ctx, id, "2.0.0", dir, man); err != nil || undo == nil {
			t.Fatalf("undo=%v err=%v", undo != nil, err)
		}
	})
}
