package db

import (
	"path/filepath"
	"strings"
	"testing"
)

// ut-docs#1368: migration 022 is the first migration to ship CREATE TRIGGER
// ... BEGIN ... END blocks (a generation counter bumped by triggers on every
// admin-synced table). splitStatements used to split on EVERY semicolon
// outside a string literal, which cut a trigger body's inner statements into
// separate "statements" and failed with "incomplete input" — the exact
// reason migration 007 chose write-time columns over a trigger. These pin
// the construct the splitter now understands.
func TestSplitStatements_KeepsTriggerBodyWhole(t *testing.T) {
	sql := `
CREATE TABLE IF NOT EXISTS v (id INTEGER PRIMARY KEY, n INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO v (id, n) VALUES (1, 0);
CREATE TRIGGER IF NOT EXISTS trg_a AFTER UPDATE ON t
WHEN NOT (OLD.name IS NEW.name AND OLD.enrolled_at IS NEW.enrolled_at)
BEGIN
  UPDATE v SET n = n + 1 WHERE id = 1;
  UPDATE v SET n = CASE WHEN n > 10 THEN 0 ELSE n END WHERE id = 1;
END;
CREATE TRIGGER trg_b AFTER DELETE ON t BEGIN UPDATE v SET n = n + 1 WHERE id = 1; END;
UPDATE v SET n = 'begin; end;' WHERE id = 1;
CREATE INDEX IF NOT EXISTS ix ON t (a)`
	got := splitStatements(stripLineComments(sql))
	want := []string{
		"CREATE TABLE IF NOT EXISTS v",
		"INSERT OR IGNORE INTO v",
		"CREATE TRIGGER IF NOT EXISTS trg_a",
		"CREATE TRIGGER trg_b",
		"UPDATE v SET n = 'begin; end;'",
		"CREATE INDEX IF NOT EXISTS ix",
	}
	if len(got) != len(want) {
		var texts []string
		for _, st := range got {
			texts = append(texts, strings.TrimSpace(st.text))
		}
		t.Fatalf("got %d statements, want %d:\n%s", len(got), len(want), strings.Join(texts, "\n---\n"))
	}
	for i, st := range got {
		if !strings.HasPrefix(strings.TrimSpace(st.text), want[i]) {
			t.Errorf("statement %d = %q, want prefix %q", i, strings.TrimSpace(st.text), want[i])
		}
	}
	// The whole trigger, header WHEN clause and CASE...END body included, is
	// one statement ending at its own END.
	trgA := strings.TrimSpace(got[2].text)
	if !strings.HasSuffix(trgA, "END;") || !strings.Contains(trgA, "CASE WHEN n > 10 THEN 0 ELSE n END WHERE id = 1;") {
		t.Errorf("trigger A body was not kept whole:\n%s", trgA)
	}
	// Lossless: joining reproduces the input (add_column_replay_test's
	// invariant for every shipped migration, checked here for the synthetic
	// one too).
	var b strings.Builder
	for _, st := range got {
		b.WriteString(st.text)
	}
	if strings.TrimSpace(b.String()) != strings.TrimSpace(stripLineComments(sql)) {
		t.Fatal("split/join is not lossless")
	}
}

// And end to end: a synthetic migration carrying a trigger runs through the
// real per-statement runner, and the trigger actually fires afterwards.
func TestApplyMigration_TriggerBlockExecutes(t *testing.T) {
	d, err := Open(filepath.Join(t.TempDir(), "trigger.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	sql := `
-- a comment with a semicolon; inside it
CREATE TABLE IF NOT EXISTS trig_probe (id INTEGER PRIMARY KEY, n INTEGER NOT NULL DEFAULT 0);
INSERT OR IGNORE INTO trig_probe (id, n) VALUES (1, 0);
CREATE TABLE IF NOT EXISTS trig_src (id TEXT PRIMARY KEY, name TEXT);
CREATE TRIGGER IF NOT EXISTS trg_probe_ins AFTER INSERT ON trig_src
BEGIN
  UPDATE trig_probe SET n = n + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS trg_probe_upd AFTER UPDATE ON trig_src
WHEN NOT (OLD.name IS NEW.name)
BEGIN
  UPDATE trig_probe SET n = n + 1 WHERE id = 1;
END;
`
	if err := runMigrationSQL(t, d, 9002, sql); err != nil {
		t.Fatalf("apply trigger migration: %v", err)
	}
	// Re-applying the same SQL must be a no-op (every migration is
	// re-runnable — openAtPreMigrationSchema's contract).
	if err := runMigrationSQL(t, d, 9003, sql); err != nil {
		t.Fatalf("re-apply trigger migration: %v", err)
	}
	n := func() int {
		var n int
		if err := d.DB.QueryRow(`SELECT n FROM trig_probe WHERE id = 1`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if _, err := d.DB.Exec(`INSERT INTO trig_src (id, name) VALUES ('a', 'x')`); err != nil {
		t.Fatal(err)
	}
	if got := n(); got != 1 {
		t.Fatalf("after insert: n = %d, want 1", got)
	}
	if _, err := d.DB.Exec(`UPDATE trig_src SET name = 'x' WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	if got := n(); got != 1 {
		t.Fatalf("after no-op update: n = %d, want 1 (WHEN clause must gate)", got)
	}
	if _, err := d.DB.Exec(`UPDATE trig_src SET name = 'y' WHERE id = 'a'`); err != nil {
		t.Fatal(err)
	}
	if got := n(); got != 2 {
		t.Fatalf("after real update: n = %d, want 2", got)
	}
}
