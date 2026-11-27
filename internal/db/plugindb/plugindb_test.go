package plugindb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

var ctx = context.Background()

func migs() []Migration {
	return []Migration{
		{1, "init", `CREATE TABLE log(id INTEGER PRIMARY KEY, note TEXT NOT NULL);`},
		{2, "add_by", `ALTER TABLE log ADD COLUMN by_who TEXT NOT NULL DEFAULT '';`},
	}
}

func newMgr(t *testing.T) (*Manager, *time.Time) {
	t.Helper()
	clock := time.Now().UTC()
	return NewManager(filepath.Join(t.TempDir(), "plugin-data"), func() time.Time { return clock }), &clock
}

func appliedVersions(t *testing.T, m *Manager, id string) []int {
	t.Helper()
	s := m.open[id]
	rows, err := s.conn.QueryContext(ctx, `SELECT version FROM _ut_migrations ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		_ = rows.Scan(&v)
		out = append(out, v)
	}
	return out
}

func TestMigrationsApplyInOrderAndAreIdempotent(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	s, err := m.Open(ctx, "com.example.logbook", Options{Migrations: migs()})
	if err != nil {
		t.Fatal(err)
	}
	if got := appliedVersions(t, m, "com.example.logbook"); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("applied = %v, want [1 2]", got)
	}
	if _, err := s.Exec(ctx, `INSERT INTO log(note, by_who) VALUES(?,?)`, "oil change", "sam"); err != nil {
		t.Fatalf("column from migration 2 missing: %v", err)
	}
	// Re-open (process restart): no change, data intact.
	m.CloseAll()
	s2, err := m.Open(ctx, "com.example.logbook", Options{Migrations: migs()})
	if err != nil {
		t.Fatalf("re-run: %v", err)
	}
	b, err := s2.Query(ctx, `SELECT note, by_who FROM log`)
	if err != nil || string(b) != `[{"by_who":"sam","note":"oil change"}]` {
		t.Fatalf("rows = %s, %v", b, err)
	}
	if got := appliedVersions(t, m, "com.example.logbook"); len(got) != 2 {
		t.Fatalf("applied after re-run = %v, want 2 rows", got)
	}
}

func TestUpgradeAppliesOnlyNewMigration(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	if _, err := m.Open(ctx, "p", Options{Migrations: migs()[:1]}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Open(ctx, "p", Options{Migrations: migs()}); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	if got := appliedVersions(t, m, "p"); len(got) != 2 {
		t.Fatalf("applied = %v, want [1 2]", got)
	}
}

func TestTamperedEarlierMigrationFailsClosed(t *testing.T) {
	m, _ := newMgr(t)
	if _, err := m.Open(ctx, "p", Options{Migrations: migs()}); err != nil {
		t.Fatal(err)
	}
	m.CloseAll()
	bad := migs()
	bad[0].SQL = `CREATE TABLE log(id INTEGER PRIMARY KEY, note TEXT, evil TEXT);`
	_, err := m.Open(ctx, "p", Options{Migrations: bad})
	if !errors.Is(err, ErrMigrationTampered) {
		t.Fatalf("err = %v, want ErrMigrationTampered", err)
	}
	// The store must not have been handed out as usable.
	if _, ok := m.open["p"]; ok {
		t.Fatal("tampered store left registered")
	}
}

func TestDowngradeRefused(t *testing.T) {
	m, _ := newMgr(t)
	if _, err := m.Open(ctx, "p", Options{Migrations: migs()}); err != nil {
		t.Fatal(err)
	}
	m.CloseAll()
	if _, err := m.Open(ctx, "p", Options{Migrations: migs()[:1]}); !errors.Is(err, ErrMigrationBehind) {
		t.Fatalf("err = %v, want ErrMigrationBehind", err)
	}
}

func TestFailedMigrationRollsBackAndLeavesVersionUnchanged(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	if _, err := m.Open(ctx, "p", Options{Migrations: migs()[:1]}); err != nil {
		t.Fatal(err)
	}
	m.CloseAll()
	broken := append(migs(), Migration{3, "boom",
		`CREATE TABLE half(x INTEGER); INSERT INTO half VALUES(1); INSERT INTO no_such_table VALUES(1);`})
	if _, err := m.Open(ctx, "p", Options{Migrations: broken}); err == nil || !strings.Contains(err.Error(), "migration 3") {
		t.Fatalf("err = %v, want migration 3 failure", err)
	}
	// Reopen with the good set: version 2 applies cleanly, `half` never existed.
	s, err := m.Open(ctx, "p", Options{Migrations: migs()})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := appliedVersions(t, m, "p"); len(got) != 2 {
		t.Fatalf("applied = %v, want [1 2]", got)
	}
	if _, err := s.Query(ctx, `SELECT x FROM half`); err == nil {
		t.Fatal("table from the failed migration survived the rollback")
	}
}

func TestFailedMigrationAlsoRollsBackEarlierPendingOnes(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	bad := []Migration{migs()[0], {2, "boom", `INSERT INTO nope VALUES(1);`}}
	if _, err := m.Open(ctx, "p", Options{Migrations: bad}); err == nil {
		t.Fatal("expected failure")
	}
	m.CloseAll()
	s, err := m.Open(ctx, "p", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Query(ctx, `SELECT 1 FROM log`); err == nil {
		t.Fatal("migration 1 persisted although the batch failed")
	}
}

func TestMigrationSetMustBeContiguous(t *testing.T) {
	m, _ := newMgr(t)
	for name, set := range map[string][]Migration{
		"gap":       {{1, "a", "CREATE TABLE a(x);"}, {3, "c", "CREATE TABLE c(x);"}},
		"duplicate": {{1, "a", "CREATE TABLE a(x);"}, {1, "b", "CREATE TABLE b(x);"}},
		"empty sql": {{1, "a", "  "}},
		"from zero": {{0, "a", "CREATE TABLE a(x);"}},
	} {
		if _, err := m.Open(ctx, "p-"+strings.ReplaceAll(name, " ", "-"), Options{Migrations: set}); !errors.Is(err, ErrMigrationSet) {
			t.Errorf("%s: err = %v, want ErrMigrationSet", name, err)
		}
	}
}

func TestLoadMigrationsFromArtifactFS(t *testing.T) {
	fsys := fstest.MapFS{
		"db/migrations/0002_second.sql": {Data: []byte("ALTER TABLE a ADD COLUMN y;")},
		"db/migrations/0001_first.sql":  {Data: []byte("CREATE TABLE a(x);")},
	}
	got, err := LoadMigrations(fsys, "db/migrations")
	if err != nil || len(got) != 2 || got[0].Name != "first" || got[1].Version != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got[0].Checksum() != (Migration{1, "x", "CREATE TABLE a(x);"}).Checksum() {
		t.Fatal("checksum must depend on the SQL text only")
	}
	for name, files := range map[string]fstest.MapFS{
		"stray file": {"db/migrations/0001_a.sql": {Data: []byte("x")}, "db/migrations/README.md": {Data: []byte("x")}},
		"bad name":   {"db/migrations/1_a.sql": {Data: []byte("x")}},
		"gap":        {"db/migrations/0002_a.sql": {Data: []byte("x")}},
	} {
		if _, err := LoadMigrations(files, "db/migrations"); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestPathTraversalIDsRejected(t *testing.T) {
	m, _ := newMgr(t)
	for _, id := range []string{"", ".", "..", "../x", "a/b", `a\b`, ".removed", "a..b", "A", "x/../../y", strings.Repeat("a", 129)} {
		if _, err := m.Open(ctx, id, Options{}); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Open(%q) err = %v, want ErrInvalidID", id, err)
		}
		if _, err := m.Uninstall(id, 0); !errors.Is(err, ErrInvalidID) {
			t.Errorf("Uninstall(%q) err = %v, want ErrInvalidID", id, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(m.root), "x")); err == nil {
		t.Fatal("traversal created a path outside the root")
	}
}

func TestStatementsThatEscapeTheFileAreRefused(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	s, err := m.Open(ctx, "p", Options{Migrations: migs()})
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other.db")
	for name, q := range map[string]string{
		"attach":            `ATTACH DATABASE '` + other + `' AS o`,
		"attach lowercase":  `attach '` + other + `' as o`,
		"attach in comment": "/* hi */ -- x\n ATTACH '" + other + "' AS o",
		"detach":            `DETACH DATABASE main`,
		"vacuum into":       `VACUUM INTO '` + other + `'`,
		"pragma":            `PRAGMA max_page_count=999999999`,
		"pragma writable":   `PRAGMA writable_schema=1`,
		"two statements":    `SELECT 1; ATTACH '` + other + `' AS o`,
		"two after comment": "SELECT 1; /* c */ SELECT 2",
		"ledger write":      `DELETE FROM _ut_migrations`,
		"empty":             `   `,
		"only comment":      `-- nothing`,
	} {
		if _, err := s.Exec(ctx, q); !errors.Is(err, ErrStatementRefused) {
			t.Errorf("%s: Exec err = %v, want ErrStatementRefused", name, err)
		}
		if _, err := s.Query(ctx, q); !errors.Is(err, ErrStatementRefused) {
			t.Errorf("%s: Query err = %v, want ErrStatementRefused", name, err)
		}
	}
	if _, err := os.Stat(other); err == nil {
		t.Fatal("a file outside the plugin directory was created")
	}
	// Semicolons inside literals and a trailing one are fine.
	if _, err := s.Exec(ctx, `INSERT INTO log(note) VALUES('a; b -- c');`); err != nil {
		t.Fatalf("legit statement refused: %v", err)
	}
}

func TestAttachIsDisabledEvenWhenKeywordCheckIsBypassed(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	s, _ := m.Open(ctx, "p", Options{})
	other := filepath.Join(t.TempDir(), "other.db")
	// Straight on the connection, no keyword check: the connection limit is
	// the real guard (ADR-0121 §4).
	if _, err := s.conn.ExecContext(ctx, `ATTACH DATABASE '`+other+`' AS o`); err == nil {
		t.Fatal("ATTACH succeeded despite SQLITE_LIMIT_ATTACHED=0")
	}
	if _, err := os.Stat(other); err == nil {
		t.Fatal("ATTACH created a file")
	}
}

func TestAttachInMigrationRejected(t *testing.T) {
	m, _ := newMgr(t)
	other := filepath.Join(t.TempDir(), "other.db")
	_, err := m.Open(ctx, "p", Options{Migrations: []Migration{{1, "evil", `ATTACH '` + other + `' AS o;`}}})
	if err == nil {
		t.Fatal("migration ATTACH succeeded")
	}
	if _, serr := os.Stat(other); serr == nil {
		t.Fatal("file created")
	}
}

func TestExtensionLoadingUnavailable(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	s, _ := m.Open(ctx, "p", Options{})
	if _, err := s.Query(ctx, `SELECT load_extension('/tmp/evil')`); err == nil {
		t.Fatal("load_extension must fail")
	}
	if _, err := s.Query(ctx, `SELECT readfile('/etc/passwd')`); err == nil {
		t.Fatal("readfile must not exist")
	}
}

func TestTwoPluginsCannotSeeEachOthersData(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	a, _ := m.Open(ctx, "plugin-a", Options{Migrations: migs()})
	b, _ := m.Open(ctx, "plugin-b", Options{})
	if _, err := a.Exec(ctx, `INSERT INTO log(note) VALUES('secret-a')`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Query(ctx, `SELECT * FROM log`); err == nil {
		t.Fatal("plugin-b can read plugin-a's table")
	}
	if _, err := b.Query(ctx, `SELECT * FROM plugin_a.log`); err == nil {
		t.Fatal("schema-qualified access worked")
	}
	if a.Path() == b.Path() {
		t.Fatal("two plugins share one file")
	}
	if filepath.Dir(a.Path()) != filepath.Join(m.root, "plugin-a") {
		t.Fatalf("db path = %s", a.Path())
	}
}

func TestQuotaStopsGrowth(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	s, err := m.Open(ctx, "p", Options{QuotaBytes: 64 << 10, Migrations: migs()})
	if err != nil {
		t.Fatal(err)
	}
	blob := strings.Repeat("x", 4000)
	var hit error
	for i := 0; i < 200 && hit == nil; i++ {
		_, hit = s.Exec(ctx, `INSERT INTO log(note) VALUES(?)`, blob)
	}
	if !errors.Is(hit, ErrQuota) {
		t.Fatalf("err = %v, want ErrQuota", hit)
	}
	fi, _ := os.Stat(s.Path())
	if fi.Size() > 64<<10 {
		t.Fatalf("db grew to %d bytes over the 65536 quota", fi.Size())
	}
}

func TestQueryResultCappedAtOneMiB(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	s, _ := m.Open(ctx, "p", Options{Migrations: migs()})
	big := strings.Repeat("y", 100<<10)
	for i := 0; i < 12; i++ {
		if _, err := s.Exec(ctx, `INSERT INTO log(note) VALUES(?)`, big); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Query(ctx, `SELECT note FROM log`); !errors.Is(err, ErrResultTooLarge) {
		t.Fatalf("err = %v, want ErrResultTooLarge", err)
	}
	if _, err := s.Query(ctx, `SELECT note FROM log LIMIT 2`); err != nil {
		t.Fatalf("paged query: %v", err)
	}
}

func TestConnectionHardening(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	s, _ := m.Open(ctx, "p", Options{})
	want := map[string]string{"journal_mode": "wal", "foreign_keys": "1", "busy_timeout": "5000", "trusted_schema": "0", "secure_delete": "1", "journal_size_limit": "4194304"}
	for pragma, w := range want {
		var got string
		if err := s.conn.QueryRowContext(ctx, `PRAGMA `+pragma).Scan(&got); err != nil || got != w {
			t.Errorf("%s = %q (%v), want %q", pragma, got, err, w)
		}
	}
	if st := s.db.Stats(); st.MaxOpenConnections != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", st.MaxOpenConnections)
	}
	fi, _ := os.Stat(s.Path())
	if fi.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Errorf("db file mode %v is group/world accessible", fi.Mode())
	}
}

func TestUninstallSoftDeletesAndNeverDeletesEarly(t *testing.T) {
	m, clock := newMgr(t)
	s, _ := m.Open(ctx, "p", Options{Migrations: migs()})
	_, _ = s.Exec(ctx, `INSERT INTO log(note) VALUES('keep me')`)
	dst, err := m.Uninstall("p", 0)
	if err != nil || dst == "" {
		t.Fatalf("Uninstall = %q, %v", dst, err)
	}
	if _, err := os.Stat(m.dir("p")); err == nil {
		t.Fatal("live data dir still present after uninstall")
	}
	if _, err := os.Stat(filepath.Join(dst, DBFileName)); err != nil {
		t.Fatalf("data not kept: %v", err)
	}
	// Day 29: prune keeps it. Day 31: prune purges it.
	*clock = clock.AddDate(0, 0, 29)
	if p, _ := m.Prune(); len(p) != 0 {
		t.Fatalf("pruned too early: %v", p)
	}
	*clock = clock.AddDate(0, 0, 2)
	p, err := m.Prune()
	if err != nil || len(p) != 1 {
		t.Fatalf("Prune = %v, %v; want 1 purged", p, err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("expired data not purged")
	}
}

func TestReinstallWithinThirtyDaysRestoresData(t *testing.T) {
	m, _ := newMgr(t)
	s, _ := m.Open(ctx, "p", Options{Migrations: migs()})
	_, _ = s.Exec(ctx, `INSERT INTO log(note) VALUES('kept')`)
	if _, err := m.Uninstall("p", 0); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore("p"); err != nil {
		t.Fatal(err)
	}
	s2, err := m.Open(ctx, "p", Options{Migrations: migs()})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s2.Query(ctx, `SELECT note FROM log`)
	if string(b) != `[{"note":"kept"}]` {
		t.Fatalf("rows = %s", b)
	}
	m.CloseAll()
	if err := m.Restore("p"); !errors.Is(err, ErrExists) {
		t.Fatalf("restore over live data: %v, want ErrExists", err)
	}
}

func TestRetentionPeriodBlocksDeleteNowAndPrune(t *testing.T) {
	m, clock := newMgr(t)
	if _, err := m.Open(ctx, "tax", Options{Migrations: migs()}); err != nil {
		t.Fatal(err)
	}
	dst, err := m.Uninstall("tax", 10)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteNow("tax"); !errors.Is(err, ErrRetained) {
		t.Fatalf("DeleteNow = %v, want ErrRetained", err)
	}
	*clock = clock.AddDate(9, 11, 0)
	if p, _ := m.Prune(); len(p) != 0 {
		t.Fatalf("pruned inside retention: %v", p)
	}
	if _, err := os.Stat(dst); err != nil {
		t.Fatal("retained data disappeared")
	}
	*clock = clock.AddDate(0, 2, 0)
	if err := m.DeleteNow("tax"); err != nil {
		t.Fatalf("DeleteNow after retention: %v", err)
	}
}

func TestDeleteNowWorksForOrdinaryPlugin(t *testing.T) {
	m, _ := newMgr(t)
	_, _ = m.Open(ctx, "p", Options{})
	dst, _ := m.Uninstall("p", 0)
	if err := m.DeleteNow("p"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dst); err == nil {
		t.Fatal("not deleted")
	}
	if err := m.DeleteNow("p"); !errors.Is(err, ErrNotRemoved) {
		t.Fatalf("second DeleteNow = %v, want ErrNotRemoved", err)
	}
}

func TestPruneLeavesForeignDirectoriesAndOtherPluginsAlone(t *testing.T) {
	m, clock := newMgr(t)
	_, _ = m.Open(ctx, "live", Options{})
	rd := filepath.Join(m.root, removedDir)
	for _, d := range []string{"unmarked-20200101", "not-a-pattern", "../escape"} {
		_ = os.MkdirAll(filepath.Join(rd, d), 0o700)
	}
	_ = os.WriteFile(filepath.Join(rd, "stray-file"), nil, 0o600)
	_, _ = m.Open(ctx, "old", Options{})
	if _, err := m.Uninstall("old", 0); err != nil {
		t.Fatal(err)
	}
	*clock = clock.AddDate(0, 0, 60)
	p, err := m.Prune()
	if err != nil || len(p) != 1 || !strings.HasPrefix(p[0], "old-") {
		t.Fatalf("Prune = %v, %v; want only old-*", p, err)
	}
	for _, keep := range []string{filepath.Join(rd, "unmarked-20200101"), filepath.Join(rd, "not-a-pattern"), filepath.Join(rd, "stray-file"), m.dir("live")} {
		if _, err := os.Stat(keep); err != nil {
			t.Errorf("%s was removed: %v", keep, err)
		}
	}
}

func TestSameDayUninstallTwiceKeepsBoth(t *testing.T) {
	m, _ := newMgr(t)
	for i := 0; i < 2; i++ {
		if _, err := m.Open(ctx, "p", Options{}); err != nil {
			t.Fatal(err)
		}
		if _, err := m.Uninstall("p", 0); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(filepath.Join(m.root, removedDir))
	if len(ents) != 2 {
		t.Fatalf("removed trees = %d, want 2", len(ents))
	}
}

func TestUninstallOfPluginWithoutDataIsNoop(t *testing.T) {
	m, _ := newMgr(t)
	if dst, err := m.Uninstall("never-used", 0); err != nil || dst != "" {
		t.Fatalf("= %q, %v", dst, err)
	}
}

func TestClosedStoreRefusesCalls(t *testing.T) {
	m, _ := newMgr(t)
	s, _ := m.Open(ctx, "p", Options{})
	_ = m.Close("p")
	if _, err := s.Exec(ctx, `SELECT 1`); !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
}

func TestContextDeadlineInterruptsStatement(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	s, _ := m.Open(ctx, "p", Options{})
	c, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := s.Query(c, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n) SELECT count(*) FROM n`)
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("runaway statement not interrupted: %v after %v", err, time.Since(start))
	}
}

// Review 2026-11-27: a migration is plugin-authored text and gets the same
// refusals as Exec/Query. Before the fix the PRAGMA below ran and lifted the
// quota for the connection's lifetime.
func TestMigrationCannotRunPragmaOrTransactionControl(t *testing.T) {
	other := filepath.Join(t.TempDir(), "other.db")
	for name, sql := range map[string]string{
		"pragma quota":      "CREATE TABLE a(x);\nPRAGMA max_page_count=1000000;",
		"pragma first":      "PRAGMA writable_schema=1;",
		"pragma in comment": "CREATE TABLE a(x); /* c */ -- d\n pragma trusted_schema=1",
		"commit":            "CREATE TABLE a(x); COMMIT; CREATE TABLE b(x);",
		"end":               "CREATE TABLE a(x); END; CREATE TABLE b(x);",
		"rollback":          "CREATE TABLE a(x); ROLLBACK;",
		"begin":             "BEGIN; CREATE TABLE a(x);",
		"savepoint":         "SAVEPOINT s; CREATE TABLE a(x);",
		"vacuum into":       "CREATE TABLE a(x); COMMIT; VACUUM INTO '" + other + "';",
		"attach":            "CREATE TABLE a(x); ATTACH '" + other + "' AS o;",
		"pragma in trigger": "CREATE TABLE a(x); CREATE TRIGGER tr AFTER INSERT ON a BEGIN SELECT 1; END; PRAGMA max_page_count=1000000;",
		"ledger":            "CREATE TABLE a(x); DELETE FROM _UT_Migrations;",
		"only comments":     "-- nothing here\n/* at all */",
	} {
		m, _ := newMgr(t)
		_, err := m.Open(ctx, "p", Options{QuotaBytes: 64 << 10, Migrations: []Migration{{1, "evil", sql}}})
		if !errors.Is(err, ErrStatementRefused) && !errors.Is(err, ErrMigrationSet) {
			t.Errorf("%s: Open err = %v, want a refusal", name, err)
		}
		// Nothing of the refused migration may have been applied.
		s, err := m.Open(ctx, "p", Options{QuotaBytes: 64 << 10})
		if err != nil {
			t.Fatalf("%s: reopen: %v", name, err)
		}
		if got, _ := s.Query(ctx, `SELECT name FROM sqlite_master WHERE name IN ('a','b')`); string(got) != "[]" {
			t.Errorf("%s: refused migration left tables behind: %s", name, got)
		}
		var pages int64
		_ = s.conn.QueryRowContext(ctx, `PRAGMA max_page_count`).Scan(&pages)
		if pages != 16 { // 64 KiB quota / 4096-byte pages
			t.Errorf("%s: max_page_count = %d, want 16", name, pages)
		}
		m.CloseAll()
	}
	if _, err := os.Stat(other); err == nil {
		t.Fatal("a migration wrote a file outside the plugin directory")
	}
}

// A real-world migration still runs: several statements, a trigger whose body
// has its own semicolons and a CASE ... END, quoted keywords, comments.
func TestMigrationSplitterRunsTriggersAndQuotedText(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	sql := `-- bookings
CREATE TABLE booking(id INTEGER PRIMARY KEY, state TEXT NOT NULL DEFAULT 'a;b', "pragma" TEXT, [vacuum] TEXT);
CREATE TABLE audit(id INTEGER PRIMARY KEY, booking_id INTEGER, note TEXT);
CREATE TEMP TRIGGER tmp_tr AFTER INSERT ON booking BEGIN SELECT 1; END;
CREATE TRIGGER booking_ai AFTER INSERT ON booking
WHEN CASE WHEN new.state = 'x' THEN 0 ELSE 1 END
BEGIN
  INSERT INTO audit(booking_id, note) VALUES (new.id, CASE WHEN new.state = 'a;b' THEN 'default; kept' ELSE 'other' END);
  UPDATE audit SET note = note || ' /* not a comment */ -- nor this' WHERE booking_id = new.id;
END;
/* trailing */ CREATE INDEX audit_booking ON audit(booking_id)`
	s, err := m.Open(ctx, "p", Options{Migrations: []Migration{{1, "init", sql}}})
	if err != nil {
		t.Fatalf("legitimate migration refused: %v", err)
	}
	if _, err := s.Exec(ctx, `INSERT INTO booking DEFAULT VALUES`); err != nil {
		t.Fatal(err)
	}
	got, err := s.Query(ctx, `SELECT note FROM audit`)
	const want = `[{"note":"default; kept /* not a comment */ -- nor this"}]`
	if err != nil || string(got) != want {
		t.Fatalf("trigger result = %s, %v; want %s", got, err, want)
	}
	if got, _ := s.Query(ctx, `SELECT count(*) AS n FROM sqlite_master WHERE name = 'audit_booking'`); string(got) != `[{"n":1}]` {
		t.Fatalf("statement after the trigger did not run: %s", got)
	}
}

// One statement per call on one pinned connection: a plugin's own BEGIN would
// leave a transaction open and lose every later write at Close.
func TestTransactionControlRefusedInExec(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	s, _ := m.Open(ctx, "p", Options{Migrations: migs()})
	for _, q := range []string{`BEGIN`, `begin immediate`, `COMMIT`, `END`, `ROLLBACK`, `SAVEPOINT s`, `RELEASE s`} {
		if _, err := s.Exec(ctx, q); !errors.Is(err, ErrStatementRefused) {
			t.Errorf("%s: err = %v, want ErrStatementRefused", q, err)
		}
	}
	if _, err := s.Exec(ctx, `INSERT INTO log(note) VALUES('kept')`); err != nil {
		t.Fatal(err)
	}
	_ = m.Close("p")
	s, _ = m.Open(ctx, "p", Options{Migrations: migs()})
	if got, _ := s.Query(ctx, `SELECT note FROM log`); string(got) != `[{"note":"kept"}]` {
		t.Fatalf("write lost across close: %s", got)
	}
}

// The TEMP schema lives in memory and is not covered by main's page limit.
func TestTempTablesAreCapped(t *testing.T) {
	m, _ := newMgr(t)
	defer m.CloseAll()
	s, _ := m.Open(ctx, "p", Options{QuotaBytes: 64 << 10})
	_, err := s.Exec(ctx, `CREATE TEMP TABLE big AS WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i<4000) SELECT i, hex(randomblob(500)) AS b FROM c`)
	if !errors.Is(err, ErrQuota) {
		t.Fatalf("4 MB temp table under a 64 KiB quota: err = %v, want ErrQuota", err)
	}
}

func TestTrailingDotIDRefused(t *testing.T) {
	m, _ := newMgr(t)
	if _, err := m.Open(ctx, "shop.", Options{}); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("err = %v, want ErrInvalidID", err)
	}
}
