package plugindb

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func rowsOf(t *testing.T, s *Store, q string, args ...any) []map[string]any {
	t.Helper()
	b, err := s.Query(ctx, q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	var out []map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func idDir(t *testing.T, m *Manager, id string) string {
	t.Helper()
	rr, err := m.resolvedRoot()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(rr, id)
}

func TestDecodeArgs(t *testing.T) {
	got, err := DecodeArgs([]byte(`[null,true,"x",42,1.5,{"b64":"aGk="},9223372036854775807]`))
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != nil || got[1] != true || got[2] != "x" || got[3] != int64(42) || got[4] != 1.5 ||
		string(got[5].([]byte)) != "hi" || got[6] != int64(9223372036854775807) {
		t.Fatalf("decoded wrong: %#v", got)
	}
	if a, err := DecodeArgs(nil); err != nil || a != nil {
		t.Fatalf("empty args: %v %v", a, err)
	}
	bad := map[string]string{
		"nested array":   `[[1]]`,
		"other object":   `[{"x":1}]`,
		"b64 plus key":   `[{"b64":"aGk=","y":1}]`,
		"bad base64":     `[{"b64":"!!!"}]`,
		"not array":      `{"a":1}`,
		"trailing":       `[1] [2]`,
		"garbage":        `[1,`,
		"too many":       "[" + strings.Repeat("1,", 64) + "1]",
		"too large":      `["` + strings.Repeat("a", 256<<10) + `"]`,
		"bare":           `1`,
		"unquoted ident": `[abc]`,
	}
	for name, raw := range bad {
		if _, err := DecodeArgs([]byte(raw)); !errors.Is(err, ErrStatementRefused) {
			t.Errorf("%s: err = %v, want ErrStatementRefused", name, err)
		}
	}
	if _, err := DecodeArgs([]byte("[" + strings.Repeat("1,", 63) + "1]")); err != nil {
		t.Fatalf("64 args must pass: %v", err)
	}
}

func TestBlobRoundTripsAsBase64(t *testing.T) {
	m, _ := newMgr(t)
	s, err := m.Open(ctx, "blobby", Options{Migrations: []Migration{{1, "i", `CREATE TABLE f(id INTEGER PRIMARY KEY, d BLOB, t TEXT);`}}})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := DecodeArgs([]byte(`[{"b64":"AAEC/w=="},"plain"]`))
	if _, err := s.Exec(ctx, `INSERT INTO f(d,t) VALUES(?,?)`, args...); err != nil {
		t.Fatal(err)
	}
	r := rowsOf(t, s, `SELECT d, t FROM f`)
	d, _ := r[0]["d"].(map[string]any)
	if d["b64"] != "AAEC/w==" || r[0]["t"] != "plain" {
		t.Fatalf("row = %#v", r[0])
	}
}

func TestValidIDRefusesWindowsDeviceNames(t *testing.T) {
	m, _ := newMgr(t)
	for _, id := range []string{"con", "prn", "aux", "nul", "com1", "com9", "lpt1", "lpt9", "con.txt", "nul.db", "aux.x.y", "shop.", "..", "a/b", `a\b`, "", ".removed", "A", "a\x00b", strings.Repeat("a", 129)} {
		if _, err := m.Open(ctx, id, Options{}); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Open(%q) = %v, want ErrInvalidID", id, err)
		}
		if _, err := m.UninstallRetained(id, 0, ""); !errors.Is(err, ErrInvalidID) {
			t.Errorf("UninstallRetained(%q) = %v", id, err)
		}
		if err := m.Restore(id); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Restore(%q) = %v", id, err)
		}
		if _, err := m.MigrateForInstall(ctx, id, "", "1.0.0", Options{Migrations: migs()}); !errors.Is(err, ErrInvalidID) {
			t.Errorf("MigrateForInstall(%q) = %v", id, err)
		}
		if _, err := m.RestorePreUpgrade(ctx, id, "1.0.0"); !errors.Is(err, ErrInvalidID) {
			t.Errorf("RestorePreUpgrade(%q) = %v", id, err)
		}
		if err := m.CheckCompatible(ctx, id, migs()); !errors.Is(err, ErrInvalidID) {
			t.Errorf("CheckCompatible(%q) = %v", id, err)
		}
	}
	for _, id := range []string{"com10", "console", "com.foo", "lpt10", "a-con"} {
		if err := validID(id); err != nil {
			t.Errorf("validID(%q) = %v, should be allowed", id, err)
		}
	}
	for _, v := range []string{"../x", "a/b", "", "1..2", `a\b`, ".x"} {
		if _, err := m.MigrateForInstall(ctx, "ok.id", "", v, Options{Migrations: migs()}); err == nil {
			t.Errorf("version %q accepted", v)
		}
		if _, err := m.RestorePreUpgrade(ctx, "ok.id", v); err == nil {
			t.Errorf("rollback target %q accepted", v)
		}
	}
}

func TestBusyReturnsErrBusyInsteadOfHanging(t *testing.T) {
	m, _ := newMgr(t)
	s, err := m.Open(ctx, "busy", Options{BusyWait: 100 * time.Millisecond, Migrations: migs()})
	if err != nil {
		t.Fatal(err)
	}
	long, cancel := context.WithCancel(ctx)
	defer cancel()
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		close(started)
		_, err := s.Query(long, `WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c) SELECT count(*) FROM c`)
		done <- err
	}()
	<-started
	time.Sleep(100 * time.Millisecond)
	t0 := time.Now()
	_, err = s.Query(ctx, `SELECT 1`)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if d := time.Since(t0); d > 2*time.Second {
		t.Fatalf("waited %v", d)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("runaway CTE not interrupted by ctx")
	}
	if _, err := s.Query(ctx, `SELECT 1`); err != nil {
		t.Fatalf("store unusable after interrupt: %v", err)
	}
}

func TestNoMigrateSkipsLedgerAndQuotaIsReapplied(t *testing.T) {
	m, _ := newMgr(t)
	s, err := m.Open(ctx, "nomig", Options{NoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.conn.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE name='_ut_migrations'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("NoMigrate touched the ledger: n=%d err=%v", n, err)
	}
	pages := func() int64 {
		var p int64
		_ = s.conn.QueryRowContext(ctx, `PRAGMA max_page_count`).Scan(&p)
		return p
	}
	before := pages()
	if _, err := m.Open(ctx, "nomig", Options{NoMigrate: true, QuotaBytes: 1 << 20}); err != nil {
		t.Fatal(err)
	}
	if after := pages(); after >= before {
		t.Fatalf("quota not re-applied: %d -> %d", before, after)
	}
}

func TestCloseIdleAndLRUCap(t *testing.T) {
	m, _ := newMgr(t)
	for i := 0; i < maxOpenStores+5; i++ {
		if _, err := m.Open(ctx, fmt.Sprintf("p%d", i), Options{Migrations: migs()}); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(m.open); n > maxOpenStores {
		t.Fatalf("%d stores open, cap %d", n, maxOpenStores)
	}
	s, _ := m.Open(ctx, "p0", Options{Migrations: migs()})
	if _, err := s.Exec(ctx, `INSERT INTO log(note) VALUES('kept')`); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	m.CloseIdle(time.Millisecond)
	if n := len(m.open); n != 0 {
		t.Fatalf("%d stores still open after CloseIdle", n)
	}
	if _, err := s.Query(ctx, `SELECT 1`); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed store err = %v", err)
	}
	s2, err := m.Open(ctx, "p0", Options{Migrations: migs()})
	if err != nil {
		t.Fatal(err)
	}
	if r := rowsOf(t, s2, `SELECT note FROM log`); len(r) != 1 || r[0]["note"] != "kept" {
		t.Fatalf("data lost across idle close: %v", r)
	}
	// A store in use is never closed by CloseIdle.
	long, cancel := context.WithCancel(ctx)
	go func() {
		_, _ = s2.Query(long, `WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c) SELECT count(*) FROM c`)
	}()
	time.Sleep(100 * time.Millisecond)
	m.CloseIdle(0)
	cancel()
	if len(m.open) != 1 {
		t.Fatal("busy store was closed")
	}
}

func TestSymlinkedPluginDirRefused(t *testing.T) {
	m, _ := newMgr(t)
	victim := t.TempDir()
	rr, _ := m.resolvedRoot()
	if err := os.Symlink(victim, filepath.Join(rr, "evil")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := m.Open(ctx, "evil", Options{Migrations: migs()}); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
	if ents, _ := os.ReadDir(victim); len(ents) != 0 {
		t.Fatalf("host wrote into the link target: %v", ents)
	}
	if _, err := m.UninstallRetained("evil", 0, ""); !errors.Is(err, ErrSymlink) {
		t.Fatalf("uninstall through link: %v", err)
	}
	if _, err := m.MigrateForInstall(ctx, "evil", "", "1.0.0", Options{Migrations: migs()}); !errors.Is(err, ErrSymlink) {
		t.Fatalf("migrate through link: %v", err)
	}
}

func TestSymlinkedDBFileRefused(t *testing.T) {
	m, _ := newMgr(t)
	if _, err := m.Open(ctx, "linkdb", Options{Migrations: migs()}); err != nil {
		t.Fatal(err)
	}
	_ = m.Close("linkdb")
	posDB := filepath.Join(t.TempDir(), "pos.db")
	if err := os.WriteFile(posDB, []byte("host data"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(idDir(t, m, "linkdb"), DBFileName)
	_ = os.Remove(p)
	if err := os.Symlink(posDB, p); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := m.Open(ctx, "linkdb", Options{Migrations: migs()}); !errors.Is(err, ErrSymlink) {
		t.Fatalf("err = %v, want ErrSymlink", err)
	}
	if b, _ := os.ReadFile(posDB); string(b) != "host data" {
		t.Fatalf("link target modified: %q", b)
	}
}

func TestSymlinkedDataRootIsAllowed(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	_ = os.MkdirAll(real, 0o700)
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	m := NewManager(link, nil)
	if _, err := m.Open(ctx, "okid", Options{Migrations: migs()}); err != nil {
		t.Fatalf("operator-symlinked data drive refused: %v", err)
	}
	if _, err := os.Stat(filepath.Join(real, "okid", DBFileName)); err != nil {
		t.Fatal(err)
	}
}

func TestSymlinkedRemovedDirNeverPurgedThrough(t *testing.T) {
	m, clock := newMgr(t)
	victim := t.TempDir()
	tree := filepath.Join(victim, "x-20200101")
	_ = os.MkdirAll(tree, 0o700)
	mk, _ := json.Marshal(removedMarker{PluginID: "x", RemovedAt: *clock, PurgeAfter: clock.Add(-time.Hour)})
	_ = os.WriteFile(filepath.Join(tree, markerName), mk, 0o600)
	rr, _ := m.resolvedRoot()
	if err := os.Symlink(victim, filepath.Join(rr, removedDir)); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if _, err := m.Prune(); !errors.Is(err, ErrSymlink) {
		t.Fatalf("Prune err = %v, want ErrSymlink", err)
	}
	if err := m.DeleteNow("x"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("DeleteNow err = %v", err)
	}
	if _, err := os.Stat(tree); err != nil {
		t.Fatalf("data behind the link was deleted: %v", err)
	}
}

func TestStatementLimits(t *testing.T) {
	m, _ := newMgr(t)
	s, err := m.Open(ctx, "limits", Options{Migrations: migs()})
	if err != nil {
		t.Fatal(err)
	}
	// Giant zeroblob / string: killed before it is materialised.
	if _, err := s.Query(ctx, `SELECT zeroblob(2000000000)`); err == nil {
		t.Fatal("2 GB zeroblob allowed")
	}
	if _, err := s.Exec(ctx, `INSERT INTO log(note) VALUES(zeroblob(2000000))`); err == nil {
		t.Fatal("2 MB value allowed")
	}
	if _, err := s.Query(ctx, `SELECT replace(hex(zeroblob(400000)),'0','00')`); err == nil {
		t.Fatal("string over 1 MiB allowed")
	}
	// SQL over 64 KiB.
	if _, err := s.Query(ctx, `SELECT '`+strings.Repeat("a", 70<<10)+`'`); err == nil {
		t.Fatal("65+ KiB statement allowed")
	}
	// More than 64 variables.
	q := "SELECT " + strings.TrimSuffix(strings.Repeat("?,", 65), ",")
	args := make([]any, 65)
	if _, err := s.Query(ctx, q, args...); err == nil {
		t.Fatal("65 variables allowed")
	}
	// More than 20 compound selects.
	cq := "SELECT 1" + strings.Repeat(" UNION ALL SELECT 1", 25)
	if _, err := s.Query(ctx, cq); err == nil {
		t.Fatal("25-term compound select allowed")
	}
	if _, err := s.Query(ctx, `SELECT 1 AS ok`); err != nil {
		t.Fatalf("normal statement broke: %v", err)
	}
}

func TestGuardRefusesFileAndPageReaders(t *testing.T) {
	m, _ := newMgr(t)
	s, _ := m.Open(ctx, "guard", Options{Migrations: migs()})
	for _, q := range []string{
		`SELECT * FROM sqlite_dbpage`,
		`select * from SQLITE_DBPAGE where pgno=1`,
		`SELECT load_extension('x')`,
		`SELECT /* c */ Load_Extension('x')`,
		`SELECT readfile('/etc/passwd')`,
		`SELECT writefile('/tmp/x','y')`,
		`SELECT fts3_tokenizer('a')`,
		`SELECT 1; SELECT 2`,
		`SELECT ';'; SELECT 2`,
		`/* x */ ATTACH 'f' AS o`,
		" PRAGMA query_only=0",
		`vacuum into '/tmp/x'`,
		`CREATE TRIGGER t AFTER INSERT ON log BEGIN ATTACH 'f' AS o; END`,
	} {
		if _, err := s.Query(ctx, q); err == nil {
			t.Errorf("Query accepted %q", q)
		}
		if _, err := s.Exec(ctx, q); err == nil {
			t.Errorf("Exec accepted %q", q)
		}
	}
	for _, mig := range []string{`SELECT readfile('x');`, `SELECT * FROM sqlite_dbpage;`, `SELECT load_extension('x');`} {
		if _, err := splitMigration(mig); !errors.Is(err, ErrStatementRefused) {
			t.Errorf("migration %q: %v", mig, err)
		}
	}
}

func TestSnapshotFileOfLiveWALDatabase(t *testing.T) {
	m, _ := newMgr(t)
	s, _ := m.Open(ctx, "snap", Options{Migrations: migs()})
	for i := 0; i < 50; i++ {
		if _, err := s.Exec(ctx, `INSERT INTO log(note) VALUES(?)`, fmt.Sprint("n", i)); err != nil {
			t.Fatal(err)
		}
	}
	dest := filepath.Join(t.TempDir(), "copy.db")
	// No checkpoint: rows may sit only in the -wal file.
	if err := SnapshotFile(ctx, s.Path(), dest); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
	if err := SnapshotFile(ctx, s.Path(), dest); err == nil {
		t.Fatal("overwrote an existing destination")
	}
	db, err := sql.Open("sqlite", "file:"+dest+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM log`).Scan(&n); err != nil || n != 50 {
		t.Fatalf("copy has %d rows, err %v", n, err)
	}
	// The live store is undisturbed.
	if _, err := s.Exec(ctx, `INSERT INTO log(note) VALUES('after')`); err != nil {
		t.Fatal(err)
	}
	if err := SnapshotFile(ctx, filepath.Join(t.TempDir(), "missing.db"), filepath.Join(t.TempDir(), "o.db")); err == nil {
		t.Fatal("missing source accepted")
	}
	if err := s.Checkpoint(ctx); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
}

var migsV2 = append(migs(), Migration{3, "idx", `CREATE INDEX log_note ON log(note);` + "\n" + `ALTER TABLE log ADD COLUMN extra TEXT NOT NULL DEFAULT 'v2';`})

func seed(t *testing.T, m *Manager, id string, note string) {
	t.Helper()
	s, err := m.Open(ctx, id, Options{NoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Exec(ctx, `INSERT INTO log(note) VALUES(?)`, note); err != nil {
		t.Fatal(err)
	}
	_ = m.Close(id)
}

func notes(t *testing.T, m *Manager, id string) []string {
	t.Helper()
	s, err := m.Open(ctx, id, Options{NoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(id)
	var out []string
	for _, r := range rowsOf(t, s, `SELECT note FROM log ORDER BY id`) {
		out = append(out, r["note"].(string))
	}
	return out
}

func TestMigrateForInstallFreshAndUpgradeMakesPreUpgradeCopy(t *testing.T) {
	m, _ := newMgr(t)
	up, err := m.MigrateForInstall(ctx, "inst", "", "1.0.0", Options{Migrations: migs()})
	if err != nil {
		t.Fatal(err)
	}
	if up.Undo == nil {
		t.Fatal("nil Undo")
	}
	seed(t, m, "inst", "one")
	if _, err := os.Stat(filepath.Join(idDir(t, m, "inst"), preUpgradeDir)); err == nil {
		t.Fatal("fresh install made a pre-upgrade copy")
	}
	if _, err := m.MigrateForInstall(ctx, "inst", "1.0.0", "1.1.0", Options{Migrations: migsV2}); err != nil {
		t.Fatal(err)
	}
	pd := filepath.Join(idDir(t, m, "inst"), preUpgradeDir)
	for _, f := range []string{"1.0.0.db", "1.0.0.json"} {
		if _, err := os.Stat(filepath.Join(pd, f)); err != nil {
			t.Fatalf("missing %s: %v", f, err)
		}
	}
	if got := notes(t, m, "inst"); len(got) != 1 || got[0] != "one" {
		t.Fatalf("data after upgrade: %v", got)
	}
	if err := m.CheckCompatible(ctx, "inst", migsV2); err != nil {
		t.Fatal(err)
	}
	if err := m.CheckCompatible(ctx, "inst", migs()); !errors.Is(err, ErrMigrationBehind) {
		t.Fatalf("downgrade check = %v, want ErrMigrationBehind", err)
	}
	tampered := append([]Migration(nil), migsV2...)
	tampered[0].SQL += " "
	if err := m.CheckCompatible(ctx, "inst", tampered); !errors.Is(err, ErrMigrationTampered) {
		t.Fatalf("tamper check = %v", err)
	}
	if err := m.CheckCompatible(ctx, "never-installed", migs()); err != nil {
		t.Fatalf("no db must be compatible: %v", err)
	}
}

func TestMigrateForInstallNoMigrationsTouchesNothing(t *testing.T) {
	m, _ := newMgr(t)
	up, err := m.MigrateForInstall(ctx, "plain", "", "1.0.0", Options{})
	if err != nil || up == nil || up.Undo() != nil {
		t.Fatalf("up=%v err=%v", up, err)
	}
	if _, err := os.Stat(idDir(t, m, "plain")); err == nil {
		t.Fatal("a plugin without migrations got a data dir")
	}
}

func TestFailedUpgradeLeavesDatabaseUnchangedAndFreshFailureLeavesNoFile(t *testing.T) {
	m, _ := newMgr(t)
	bad := append(migs(), Migration{3, "boom", `ALTER TABLE log ADD COLUMN z TEXT; SELECT * FROM no_such_table;`})
	if _, err := m.MigrateForInstall(ctx, "failnew", "", "1.0.0", Options{Migrations: bad}); err == nil {
		t.Fatal("bad migration accepted")
	}
	if _, err := os.Stat(filepath.Join(idDir(t, m, "failnew"), DBFileName)); err == nil {
		t.Fatal("failed fresh install left plugin.db")
	}
	if _, err := m.MigrateForInstall(ctx, "failup", "", "1.0.0", Options{Migrations: migs()}); err != nil {
		t.Fatal(err)
	}
	seed(t, m, "failup", "keep me")
	if _, err := m.MigrateForInstall(ctx, "failup", "1.0.0", "1.1.0", Options{Migrations: bad}); err == nil {
		t.Fatal("bad upgrade accepted")
	}
	if got := notes(t, m, "failup"); len(got) != 1 || got[0] != "keep me" {
		t.Fatalf("data after failed upgrade: %v", got)
	}
	if v := appliedVersions2(t, m, "failup"); len(v) != 2 {
		t.Fatalf("applied = %v, want [1 2]", v)
	}
	tamper := append([]Migration(nil), migs()...)
	tamper[1].SQL = `ALTER TABLE log ADD COLUMN other TEXT;`
	if _, err := m.MigrateForInstall(ctx, "failup", "1.0.0", "1.1.0", Options{Migrations: tamper}); !errors.Is(err, ErrMigrationTampered) {
		t.Fatalf("tampered upgrade = %v", err)
	}
	if _, err := m.MigrateForInstall(ctx, "failup", "1.0.0", "0.9.0", Options{Migrations: migs()[:1]}); !errors.Is(err, ErrMigrationBehind) {
		t.Fatalf("downgrade install = %v", err)
	}
}

func appliedVersions2(t *testing.T, m *Manager, id string) []int {
	t.Helper()
	s, err := m.Open(ctx, id, Options{NoMigrate: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(id)
	var out []int
	rows, err := s.conn.QueryContext(ctx, `SELECT version FROM _ut_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		_ = rows.Scan(&v)
		out = append(out, v)
	}
	return out
}

func TestUndoRestoresPreUpgradeOrDeletesFreshDatabase(t *testing.T) {
	m, _ := newMgr(t)
	up, err := m.MigrateForInstall(ctx, "undo", "", "1.0.0", Options{Migrations: migs()})
	if err != nil {
		t.Fatal(err)
	}
	if err := up.Undo(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(idDir(t, m, "undo"), DBFileName)); err == nil {
		t.Fatal("Undo of a fresh install left plugin.db")
	}
	if _, err := m.MigrateForInstall(ctx, "undo", "", "1.0.0", Options{Migrations: migs()}); err != nil {
		t.Fatal(err)
	}
	seed(t, m, "undo", "before")
	up, err = m.MigrateForInstall(ctx, "undo", "1.0.0", "1.1.0", Options{Migrations: migsV2})
	if err != nil {
		t.Fatal(err)
	}
	if v := appliedVersions2(t, m, "undo"); len(v) != 3 {
		t.Fatalf("applied %v", v)
	}
	if err := up.Undo(); err != nil {
		t.Fatal(err)
	}
	if err := up.Undo(); err != nil {
		t.Fatalf("second Undo: %v", err)
	}
	if v := appliedVersions2(t, m, "undo"); len(v) != 2 {
		t.Fatalf("applied after Undo %v", v)
	}
	if got := notes(t, m, "undo"); len(got) != 1 || got[0] != "before" {
		t.Fatalf("data after Undo: %v", got)
	}
}

func TestRestorePreUpgradeRollsBackAndKeepsNewerData(t *testing.T) {
	m, _ := newMgr(t)
	if _, err := m.MigrateForInstall(ctx, "rb", "", "1.0.0", Options{Migrations: migs()}); err != nil {
		t.Fatal(err)
	}
	seed(t, m, "rb", "old-version-row")
	if _, err := m.MigrateForInstall(ctx, "rb", "1.0.0", "1.1.0", Options{Migrations: migsV2}); err != nil {
		t.Fatal(err)
	}
	seed(t, m, "rb", "written-by-newer")
	if _, err := m.RestorePreUpgrade(ctx, "rb", "0.5.0"); !errors.Is(err, ErrNoPreUpgrade) {
		t.Fatalf("unknown target = %v", err)
	}
	up, err := m.RestorePreUpgrade(ctx, "rb", "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if got := notes(t, m, "rb"); len(got) != 1 || got[0] != "old-version-row" {
		t.Fatalf("rolled-back data: %v", got)
	}
	if err := m.CheckCompatible(ctx, "rb", migs()); err != nil {
		t.Fatalf("older code must accept the restored db: %v", err)
	}
	matches, _ := filepath.Glob(filepath.Join(idDir(t, m, "rb"), preUpgradeDir, "rolled-back-*.db"))
	if len(matches) != 1 {
		t.Fatalf("rolled-back copies: %v", matches)
	}
	if err := up.Undo(); err != nil {
		t.Fatal(err)
	}
	if got := notes(t, m, "rb"); len(got) != 2 || got[1] != "written-by-newer" {
		t.Fatalf("after Undo: %v", got)
	}
	if _, err := m.RestorePreUpgrade(ctx, "never-existed", "1.0.0"); !errors.Is(err, ErrNoPreUpgrade) {
		t.Fatalf("no dir = %v", err)
	}
}

func TestSweepPreUpgradeTmpAgeAndKeepNewestTwo(t *testing.T) {
	m, clock := newMgr(t)
	if _, err := m.MigrateForInstall(ctx, "sw", "", "1.0.0", Options{Migrations: migs()}); err != nil {
		t.Fatal(err)
	}
	pd, err := preDir(idDir(t, m, "sw"), true)
	if err != nil {
		t.Fatal(err)
	}
	w := func(name string, age time.Duration) {
		p := filepath.Join(pd, name)
		_ = os.WriteFile(p, []byte("x"), 0o600)
		at := clock.Add(-age)
		_ = os.Chtimes(p, at, at)
	}
	w("1.0.db", 5*24*time.Hour)
	w("1.1.db", 4*24*time.Hour)
	w("1.2.db", 3*24*time.Hour)
	w("1.2.json", 3*24*time.Hour)
	w("1.3.db", 2*24*time.Hour)
	w("0.1.db", 40*24*time.Hour)
	w("crash.db.tmp", time.Hour)
	w("rolled-back-1.db", 10*24*time.Hour)
	w("rolled-back-2.db", 40*24*time.Hour)
	if err := m.SweepPreUpgrade(30*24*time.Hour, 2); err != nil {
		t.Fatal(err)
	}
	left, _ := os.ReadDir(pd)
	var names []string
	for _, e := range left {
		names = append(names, e.Name())
	}
	want := "1.3.db,1.2.db,rolled-back-1.db,rolled-back-2.db" // rolled-back copies are never aged out
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	for _, n := range strings.Split(want, ",") {
		if !got[n] {
			t.Errorf("%s was swept; left %v", n, names)
		}
	}
	for _, n := range []string{"1.0.db", "1.1.db", "0.1.db", "crash.db.tmp", "1.2.json"} {
		if n == "1.2.json" {
			continue // kept together with its db
		}
		if got[n] {
			t.Errorf("%s survived; left %v", n, names)
		}
	}
}

func TestUninstallRetainedKeepsReasonKeyAndBlocksDeleteNow(t *testing.T) {
	m, clock := newMgr(t)
	if _, err := m.Open(ctx, "fiscal", Options{Migrations: migs()}); err != nil {
		t.Fatal(err)
	}
	dst, err := m.UninstallRetained("fiscal", 7, "plugin.fiscal.retained")
	if err != nil || dst == "" {
		t.Fatal(dst, err)
	}
	ri, err := m.RemovedInfo("fiscal")
	if err != nil {
		t.Fatal(err)
	}
	if ri.ReasonKey != "plugin.fiscal.retained" || !ri.Retained || ri.Dir != dst || ri.RetainUntil.Before(clock.AddDate(6, 11, 0)) {
		t.Fatalf("info = %+v", ri)
	}
	if err := m.DeleteNow("fiscal"); !errors.Is(err, ErrRetained) {
		t.Fatalf("DeleteNow = %v", err)
	}
	*clock = clock.AddDate(7, 0, 1)
	if ri, _ = m.RemovedInfo("fiscal"); ri.Retained {
		t.Fatal("still retained after the period")
	}
	if p, err := m.Prune(); err != nil || len(p) != 1 {
		t.Fatalf("prune = %v %v", p, err)
	}
	if _, err := m.RemovedInfo("fiscal"); !errors.Is(err, ErrNotRemoved) {
		t.Fatalf("after prune: %v", err)
	}
	// Uninstall still works with the original two-argument form.
	_, _ = m.Open(ctx, "plain", Options{Migrations: migs()})
	if _, err := m.UninstallRetained("plain", 0, ""); err != nil {
		t.Fatal(err)
	}
	if ri, _ := m.RemovedInfo("plain"); ri.Retained || ri.ReasonKey != "" {
		t.Fatalf("plain info = %+v", ri)
	}
}

func TestUninstallMovesPreUpgradeCopiesWithTheDirectory(t *testing.T) {
	m, _ := newMgr(t)
	_, _ = m.MigrateForInstall(ctx, "mv", "", "1.0.0", Options{Migrations: migs()})
	_, _ = m.MigrateForInstall(ctx, "mv", "1.0.0", "1.1.0", Options{Migrations: migsV2})
	dst, err := m.UninstallRetained("mv", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, preUpgradeDir, "1.0.0.db")); err != nil {
		t.Fatalf("pre-upgrade copy did not follow the dir: %v", err)
	}
}

func makeSQLite(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "import.db")
	db, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{`CREATE TABLE t(a INTEGER, b TEXT, c BLOB)`, `INSERT INTO t VALUES(1,'x',x'0102')`, `INSERT INTO t VALUES(2,'y',NULL)`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func TestOpenImportIsReadOnlyAndConfined(t *testing.T) {
	p := makeSQLite(t)
	before, _ := os.ReadFile(p)
	d, err := OpenImport(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	b, err := d.Query(ctx, `SELECT a, b, c FROM t ORDER BY a`)
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	_ = json.Unmarshal(b, &rows)
	if len(rows) != 2 || rows[0]["b"] != "x" || rows[0]["c"].(map[string]any)["b64"] != "AQI=" {
		t.Fatalf("rows = %s", b)
	}
	for _, q := range []string{
		`INSERT INTO t VALUES(3,'z',NULL)`, `UPDATE t SET a=9`, `DELETE FROM t`, `DROP TABLE t`, `CREATE TABLE u(x)`,
		`PRAGMA query_only=0`, `pragma writable_schema=1`, `ATTACH DATABASE '/tmp/x' AS o`, `VACUUM`, `VACUUM INTO '/tmp/zzz'`,
		`SELECT load_extension('x')`, `SELECT * FROM sqlite_dbpage`, `SELECT 1; SELECT 2`,
	} {
		if _, err := d.Query(ctx, q); err == nil {
			t.Errorf("import Query accepted %q", q)
		}
	}
	after, _ := os.ReadFile(p)
	if string(before) != string(after) {
		t.Fatal("import file was modified")
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Query(ctx, `SELECT 1`); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: %v", err)
	}
}

func TestOpenImportRejectsHostileFiles(t *testing.T) {
	dir := t.TempDir()
	cases := map[string][]byte{
		"empty":       {},
		"text":        []byte("hello world, not a database at all"),
		"header-only": append([]byte("SQLite format 3\x00"), make([]byte, 20)...),
		"bad-page":    append(append([]byte("SQLite format 3\x00"), 0xff, 0xff, 1, 1), make([]byte, 200)...),
	}
	for name, content := range cases {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, content, 0o600)
		d, err := OpenImport(ctx, p)
		if err == nil {
			_ = d.Close()
			t.Errorf("%s: accepted", name)
		}
	}
	p := filepath.Join(dir, "text")
	if _, err := OpenImport(ctx, p); !errors.Is(err, ErrNotSQLite) {
		t.Errorf("text file: %v, want ErrNotSQLite", err)
	}
	if _, err := OpenImport(ctx, filepath.Join(dir, "missing")); err == nil {
		t.Error("missing file accepted")
	}
	real := makeSQLite(t)
	link := filepath.Join(dir, "link.db")
	if err := os.Symlink(real, link); err == nil {
		if _, err := OpenImport(ctx, link); !errors.Is(err, ErrSymlink) {
			t.Errorf("symlinked import = %v", err)
		}
	}
}

func TestOpenImportHostileSchemaCannotAttachOrUseUnsafeViews(t *testing.T) {
	p := filepath.Join(t.TempDir(), "evil.db")
	db, _ := sql.Open("sqlite", "file:"+p)
	for _, q := range []string{
		`CREATE TABLE t(a)`,
		`CREATE VIEW v AS SELECT load_extension('x')`,
		`CREATE VIEW v2 AS SELECT randomblob(1) AS r`,
	} {
		_, _ = db.Exec(q)
	}
	db.Close()
	d, err := OpenImport(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Query(ctx, `SELECT * FROM v`); err == nil {
		t.Fatal("view calling load_extension ran")
	}
}

func TestStoreSurvivesConcurrentUseAcrossPlugins(t *testing.T) {
	m, _ := newMgr(t)
	errc := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func(i int) {
			id := fmt.Sprintf("conc%d", i%4)
			for j := 0; j < 20; j++ {
				s, err := m.Open(ctx, id, Options{Migrations: migs()})
				if err != nil {
					errc <- err
					return
				}
				if _, err := s.Exec(ctx, `INSERT INTO log(note) VALUES('x')`); err != nil {
					errc <- err
					return
				}
			}
			errc <- nil
		}(i)
	}
	for i := 0; i < 8; i++ {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
}

func BenchmarkOpenExisting(b *testing.B) {
	m := NewManager(filepath.Join(b.TempDir(), "pd"), nil)
	if _, err := m.Open(ctx, "bench", Options{Migrations: migs()}); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = m.Close("bench")
		if _, err := m.Open(ctx, "bench", Options{NoMigrate: true}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQuery1000Rows(b *testing.B) {
	m := NewManager(filepath.Join(b.TempDir(), "pd"), nil)
	s, _ := m.Open(ctx, "bench", Options{Migrations: migs()})
	for i := 0; i < 1000; i++ {
		_, _ = s.Exec(ctx, `INSERT INTO log(note) VALUES('row')`)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Query(ctx, `SELECT * FROM log`); err != nil {
			b.Fatal(err)
		}
	}
}

func TestPerformanceBudgets(t *testing.T) {
	m, _ := newMgr(t)
	var stmts strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&stmts, "CREATE TABLE t%d(id INTEGER PRIMARY KEY, v TEXT);\n", i)
	}
	t0 := time.Now()
	if _, err := m.MigrateForInstall(ctx, "perf", "", "1.0.0", Options{Migrations: []Migration{{1, "big", stmts.String()}}}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 10*time.Second { // budget 1 s, x10 CI slack
		t.Fatalf("50-statement migration took %v", d)
	}
	s, _ := m.Open(ctx, "perf", Options{NoMigrate: true})
	for i := 0; i < 1000; i++ {
		_, _ = s.Exec(ctx, `INSERT INTO t0(v) VALUES('row')`)
	}
	// Best of five: one sample is a scheduler lottery on a loaded machine or
	// under -race (seen 140-160 ms at load average 55), the minimum is not.
	best := time.Hour
	for i := 0; i < 5; i++ {
		t0 = time.Now()
		if _, err := s.Query(ctx, `SELECT * FROM t0`); err != nil {
			t.Fatal(err)
		}
		best = min(best, time.Since(t0))
	}
	if best > 100*time.Millisecond { // budget 10 ms
		t.Fatalf("1000-row query took %v (best of 5)", best)
	}
	_ = m.Close("perf")
	t0 = time.Now()
	if _, err := m.Open(ctx, "perf", Options{NoMigrate: true}); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(t0); d > 500*time.Millisecond { // budget 50 ms
		t.Fatalf("open took %v", d)
	}
}
