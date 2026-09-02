package db

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestMigrations_AddColumnReplaySafe (ut-docs#1412): a till that ran a
// pre-merge build already had a column when the release arrived with that
// DDL renumbered — the ledger said one less, so the migration re-ran and
// died with "duplicate column name", and the Android shell showed a white
// screen. Every migration must be re-runnable against an already-migrated
// database; ADD COLUMN has no IF NOT EXISTS in SQLite, so the runner has
// to supply that idempotence itself. The 78-file ledger this was first
// pinned against is gone (ADR-0074), but the property still matters for
// any future renumbering, so it is pinned here with a synthetic ADD COLUMN
// migration: apply it, drop only its ledger row (columns stay in place —
// exactly the shape a renumbered migration leaves behind on a device), and
// apply the same file again.
func TestMigrations_AddColumnReplaySafe(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	const version = 9100
	sql := "ALTER TABLE settings ADD COLUMN replay_probe TEXT NOT NULL DEFAULT '';\n"
	if err := runMigrationSQL(t, d, version, sql); err != nil {
		t.Fatalf("first application: %v", err)
	}
	if _, err := d.DB.Exec(`DELETE FROM schema_migrations WHERE version = ?`, version); err != nil {
		t.Fatalf("rewind ledger row: %v", err)
	}
	if err := runMigrationSQL(t, d, version, sql); err != nil {
		t.Fatalf("replaying an ADD COLUMN against a database that already has its column: %v", err)
	}

	if n := columnCount(t, d, "settings", "replay_probe"); n != 1 {
		t.Fatalf("settings.replay_probe count = %d, want exactly 1", n)
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&n); err != nil || n != 1 {
		t.Fatalf("schema_migrations has version %d %d time(s) err=%v, want 1 (replay must be recorded)", version, n, err)
	}
}

// TestMigrations_FreshInstallSkipsNothing pins the common path: on a brand
// new database the runner must never skip a statement — a skip there would
// mean the splitter or the matcher misread a shipped migration.
func TestMigrations_FreshInstallSkipsNothing(t *testing.T) {
	var skipped []string
	onSkippedAddColumn = func(v int, table, column string) {
		skipped = append(skipped, fmt.Sprintf("%03d %s.%s", v, table, column))
	}
	defer func() { onSkippedAddColumn = nil }()
	d, err := Open(filepath.Join(t.TempDir(), "fresh.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if len(skipped) != 0 {
		t.Fatalf("fresh install skipped ADD COLUMN statements: %v", skipped)
	}
	// And every shipped migration survives the comment-strip + split
	// round trip: joining the statements back reproduces the stripped
	// text byte for byte (nothing lost between semicolons).
	migs, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range migs {
		stripped := stripLineComments(m.SQL)
		var b strings.Builder
		for _, st := range splitStatements(stripped) {
			b.WriteString(st.text)
		}
		if strings.TrimSpace(b.String()) != strings.TrimSpace(stripped) {
			t.Fatalf("migration %03d %s: split/join is not lossless", m.Version, m.Name)
		}
	}
}

// runMigrationSQL applies one synthetic migration through the real
// per-statement runner and reports whether it succeeded.
func runMigrationSQL(t *testing.T, d *DB, version int, sql string) error {
	t.Helper()
	return d.applyMigration(migration{Version: version, Name: fmt.Sprintf("%03d_test.sql", version), SQL: sql})
}

func columnCount(t *testing.T, d *DB, table, column string) int {
	t.Helper()
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, table, column).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestExecMigrationStatements_EdgeCases covers the independent review's
// findings on the first draft (a whole-text regex rewrite), each of which
// is now a statement-level property:
//  1. a semicolon inside a quoted DEFAULT does not end the statement;
//  2. DDL-shaped text inside a string literal is data, never a match;
//  3. rebuild-then-add: the existence check runs against the table as it
//     is at that moment, so a column re-added after a table rebuild is
//     really added;
//  4. a final ADD COLUMN with no trailing semicolon is still recognised;
//  5. a schema-qualified table name is recognised;
//  6. an ADD COLUMN for a table that does not exist still fails loudly.
func TestExecMigrationStatements_EdgeCases(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	if _, err := d.DB.ExecContext(ctx, `CREATE TABLE t (a TEXT); CREATE TABLE x (id TEXT, c TEXT); INSERT INTO x VALUES ('1','old')`); err != nil {
		t.Fatal(err)
	}

	// 1 + 2 in one migration, replayed with t.a already present.
	sql := "ALTER TABLE t ADD COLUMN a TEXT NOT NULL DEFAULT 'x;y';\n" +
		"ALTER TABLE t ADD COLUMN sep TEXT NOT NULL DEFAULT ';';\n" +
		"INSERT INTO t(a, sep) VALUES ('ALTER TABLE t ADD COLUMN a TEXT;', 'z');\n" +
		"CREATE TABLE z (i INT);\n"
	if err := runMigrationSQL(t, d, 9001, sql); err != nil {
		t.Fatalf("literal-heavy migration: %v", err)
	}
	if columnCount(t, d, "t", "sep") != 1 || columnCount(t, d, "t", "a") != 1 {
		t.Fatal("t.sep must be added exactly once and t.a kept")
	}
	var got string
	if err := d.DB.QueryRow(`SELECT a FROM t`).Scan(&got); err != nil || got != "ALTER TABLE t ADD COLUMN a TEXT;" {
		t.Fatalf("literal row = %q err=%v — DDL-shaped data must be stored verbatim", got, err)
	}
	if columnCount(t, d, "z", "i") != 1 {
		t.Fatal("statement after the literal-heavy ones was lost")
	}

	// 3: rebuild x without c, then re-add c. The old x has c; the rebuilt
	// one must end up with it too.
	sql = "CREATE TABLE x_new (id TEXT);\n" +
		"INSERT INTO x_new(id) SELECT id FROM x;\n" +
		"DROP TABLE x;\n" +
		"ALTER TABLE x_new RENAME TO x;\n" +
		"ALTER TABLE x ADD COLUMN c TEXT NOT NULL DEFAULT 'new';\n"
	if err := runMigrationSQL(t, d, 9002, sql); err != nil {
		t.Fatalf("rebuild-then-add: %v", err)
	}
	if columnCount(t, d, "x", "c") != 1 {
		t.Fatal("rebuild-then-add lost column x.c (existence was checked against the OLD table)")
	}
	if err := d.DB.QueryRow(`SELECT c FROM x WHERE id = '1'`).Scan(&got); err != nil || got != "new" {
		t.Fatalf("rebuilt row c = %q err=%v", got, err)
	}

	// 4 + 5: no trailing semicolon, schema-qualified, both already present.
	if err := runMigrationSQL(t, d, 9003, "ALTER TABLE main.t ADD COLUMN sep TEXT;\nALTER TABLE `t` ADD a TEXT"); err != nil {
		t.Fatalf("qualified / unterminated replay: %v", err)
	}
	if columnCount(t, d, "t", "sep") != 1 || columnCount(t, d, "t", "a") != 1 {
		t.Fatal("qualified / unterminated ADD COLUMN must be skipped, not duplicated")
	}

	// 6: a missing table is not papered over.
	if err := runMigrationSQL(t, d, 9004, "ALTER TABLE missing ADD COLUMN c TEXT;"); err == nil {
		t.Fatal("ADD COLUMN on a missing table must still fail")
	}
}
