package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// ut-docs#6566: the per-till card reader registry.

func newCardReaderTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	mig, err := os.ReadFile("../db/migrations/109_card_readers.sql")
	if err != nil {
		t.Fatalf("read migration 108: %v", err)
	}
	stmts := []string{
		`PRAGMA foreign_keys = ON;`,
		`CREATE TABLE plugins (id TEXT PRIMARY KEY, name TEXT, version TEXT, author TEXT, is_active INTEGER NOT NULL DEFAULT 1, install_state TEXT DEFAULT 'installed', runtime TEXT DEFAULT 'go', entrypoint TEXT DEFAULT '', installed_sha256 TEXT);`,
		`CREATE TABLE plugin_entries (id TEXT PRIMARY KEY, plugin_id TEXT NOT NULL, type TEXT, key TEXT, route TEXT, label TEXT, menu_group TEXT, icon_path TEXT, parent_page_key TEXT, target_action TEXT, trigger_event TEXT, config_json TEXT, sort_order INTEGER DEFAULT 0, is_active INTEGER NOT NULL DEFAULT 1);`,
		`CREATE TABLE plugin_settings (id TEXT PRIMARY KEY, plugin_id TEXT NOT NULL, key TEXT NOT NULL, value_json TEXT NOT NULL, scope TEXT NOT NULL DEFAULT 'global', scope_id TEXT, updated_at TEXT NOT NULL DEFAULT (datetime('now')), UNIQUE (plugin_id, key, scope, scope_id));`,
		string(mig),
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup schema: %v", err)
		}
	}
	return db
}

// seedReaderPlugin installs a payment plugin with one payment entry (key =
// method) and, when settingKey != "", its register-scoped legacy setting.
func seedReaderPlugin(t *testing.T, db *sql.DB, pluginID, method, label, settingKey, settingJSON, scope string) {
	t.Helper()
	mustExecCR(t, db, `INSERT INTO plugins(id,name,version,is_active) VALUES(?,?,'1.0',1)`, pluginID, label)
	mustExecCR(t, db, `INSERT INTO plugin_entries(id,plugin_id,type,key,label,trigger_event,is_active,sort_order) VALUES(?,?,'payment',?,?,?,1,0)`,
		"e-"+pluginID, pluginID, method, label, "payment."+method+".requested")
	if settingKey != "" {
		mustExecCR(t, db, `INSERT INTO plugin_settings(id,plugin_id,key,value_json,scope) VALUES(?,?,?,?,?)`,
			"s-"+pluginID+settingKey, pluginID, settingKey, settingJSON, scope)
	}
}

func mustExecCR(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}

func legacySetting(t *testing.T, db *sql.DB, pluginID, key string) string {
	t.Helper()
	var v string
	err := db.QueryRow(`SELECT value_json FROM plugin_settings WHERE plugin_id=? AND key=? AND scope='register'`, pluginID, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "<none>"
	}
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestMigrateLegacyReaderSettings_OneSettingOneRowIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newCardReaderTestDB(t)
	seedReaderPlugin(t, db, "p.stripe", "stripe", "Stripe", "stripe_reader_id", `"tmr_ABC123"`, "register")
	repo := NewCardReaderRepo(db)

	n, err := repo.MigrateLegacyReaderSettings(ctx)
	if err != nil || n != 1 {
		t.Fatalf("first migrate: n=%d err=%v, want 1 row", n, err)
	}
	rows, _ := repo.ListActive(ctx)
	if len(rows) != 1 {
		t.Fatalf("want 1 active reader, got %d", len(rows))
	}
	r := rows[0]
	if r.MethodKey != "stripe" || r.PluginID != "p.stripe" || r.ReaderRef != "tmr_ABC123" || r.Source != CardReaderSourceMigrated || r.Kind != "cloud" || r.Name != "Stripe" {
		t.Fatalf("unexpected migrated row: %+v", r)
	}
	if got := legacySetting(t, db, "p.stripe", "stripe_reader_id"); got != `"tmr_ABC123"` {
		t.Fatalf("migration must not rewrite the legacy setting, got %s", got)
	}
	n, err = repo.MigrateLegacyReaderSettings(ctx)
	if err != nil || n != 0 {
		t.Fatalf("second migrate: n=%d err=%v, want 0 (idempotent)", n, err)
	}
	if rows, _ = repo.ListActive(ctx); len(rows) != 1 {
		t.Fatalf("duplicate row after second migrate: %d", len(rows))
	}
}

func TestMigrateLegacyReaderSettings_TwoPluginsTwoRows(t *testing.T) {
	ctx := context.Background()
	db := newCardReaderTestDB(t)
	seedReaderPlugin(t, db, "p.stripe", "stripe", "Stripe", "stripe_reader_id", `"tmr_A"`, "register")
	seedReaderPlugin(t, db, "p.sumup", "sumup", "SumUp", "sumup_reader_id", `"rdr_B"`, "register")
	n, err := NewCardReaderRepo(db).MigrateLegacyReaderSettings(ctx)
	if err != nil || n != 2 {
		t.Fatalf("n=%d err=%v, want 2", n, err)
	}
}

func TestMigrateLegacyReaderSettings_IgnoresGlobalEmptyAndOtherKeys(t *testing.T) {
	ctx := context.Background()
	db := newCardReaderTestDB(t)
	seedReaderPlugin(t, db, "p.a", "a", "A", "a_reader_id", `"tmr_G"`, "global")
	seedReaderPlugin(t, db, "p.b", "b", "B", "b_reader_id", `""`, "register")
	seedReaderPlugin(t, db, "p.c", "c", "C", "c_other", `"tmr_C"`, "register")
	seedReaderPlugin(t, db, "p.d", "d", "D", "d_reader_id", `"bad/ref"`, "register")
	n, err := NewCardReaderRepo(db).MigrateLegacyReaderSettings(ctx)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v, want 0 (global, empty, other key, malformed ref are ignored)", n, err)
	}
}

func TestMigrateLegacyReaderSettings_SoftRemovedNotRecreated(t *testing.T) {
	ctx := context.Background()
	db := newCardReaderTestDB(t)
	seedReaderPlugin(t, db, "p.stripe", "stripe", "Stripe", "stripe_reader_id", `"tmr_ABC123"`, "register")
	repo := NewCardReaderRepo(db)
	if _, err := repo.MigrateLegacyReaderSettings(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := repo.ListActive(ctx)
	if _, err := repo.SoftRemove(ctx, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	// SoftRemove cleared the setting; put it back to prove the removed row blocks re-import.
	mustExecCR(t, db, `INSERT INTO plugin_settings(id,plugin_id,key,value_json,scope) VALUES('s2','p.stripe','stripe_reader_id','"tmr_ABC123"','register')`)
	n, err := repo.MigrateLegacyReaderSettings(ctx)
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v, a removed reader must not be re-imported", n, err)
	}
	if rows, _ = repo.ListActive(ctx); len(rows) != 0 {
		t.Fatalf("want no active rows, got %d", len(rows))
	}
}

func TestSoftRemove_ClearsMatchingLegacySettingOnly(t *testing.T) {
	ctx := context.Background()
	db := newCardReaderTestDB(t)
	seedReaderPlugin(t, db, "p.stripe", "stripe", "Stripe", "stripe_reader_id", `"tmr_ABC123"`, "register")
	repo := NewCardReaderRepo(db)
	if _, err := repo.MigrateLegacyReaderSettings(ctx); err != nil {
		t.Fatal(err)
	}
	other, err := repo.Create(ctx, CardReader{Name: "Back", MethodKey: "stripe", PluginID: "p.stripe", ReaderRef: "tmr_OTHER"})
	if err != nil {
		t.Fatal(err)
	}
	cleared, err := repo.SoftRemove(ctx, other.ID)
	if err != nil || cleared {
		t.Fatalf("removing a non-matching reader: cleared=%v err=%v, want false", cleared, err)
	}
	if got := legacySetting(t, db, "p.stripe", "stripe_reader_id"); got != `"tmr_ABC123"` {
		t.Fatalf("non-matching removal touched the setting: %s", got)
	}
	rows, _ := repo.ListActive(ctx)
	cleared, err = repo.SoftRemove(ctx, rows[0].ID)
	if err != nil || !cleared {
		t.Fatalf("removing the legacy-equal reader: cleared=%v err=%v, want true", cleared, err)
	}
	if got := legacySetting(t, db, "p.stripe", "stripe_reader_id"); got != "<none>" {
		t.Fatalf("legacy setting must be cleared, got %s", got)
	}
}

func TestCreate_Validation(t *testing.T) {
	ctx := context.Background()
	db := newCardReaderTestDB(t)
	seedReaderPlugin(t, db, "p.stripe", "stripe", "Stripe", "", "", "")
	repo := NewCardReaderRepo(db)
	base := CardReader{Name: "Front", MethodKey: "stripe", PluginID: "p.stripe", ReaderRef: "tmr_1"}
	if _, err := repo.Create(ctx, base); err != nil {
		t.Fatalf("valid create: %v", err)
	}
	cases := []struct {
		name string
		mut  func(c *CardReader)
		want error
	}{
		{"empty name", func(c *CardReader) { c.Name = "  " }, ErrCardReaderName},
		{"41 char name", func(c *CardReader) { c.Name = strings.Repeat("n", 41) }, ErrCardReaderName},
		{"duplicate name", func(c *CardReader) { c.ReaderRef = "tmr_2" }, ErrCardReaderName},
		{"slash in ref", func(c *CardReader) { c.Name = "X"; c.ReaderRef = "a/b" }, ErrCardReaderRef},
		{"space in ref", func(c *CardReader) { c.Name = "X"; c.ReaderRef = "a b" }, ErrCardReaderRef},
		{"129 char ref", func(c *CardReader) { c.Name = "X"; c.ReaderRef = strings.Repeat("a", 129) }, ErrCardReaderRef},
		{"empty ref", func(c *CardReader) { c.Name = "X"; c.ReaderRef = "" }, ErrCardReaderRef},
		// Reviewer (#6566): a plugin puts the ref into a provider URL path, so a
		// dot segment must never pass core's check either (`readers/../x`).
		{"dot-dot ref", func(c *CardReader) { c.Name = "X"; c.ReaderRef = ".." }, ErrCardReaderRef},
		{"single dot ref", func(c *CardReader) { c.Name = "X"; c.ReaderRef = "." }, ErrCardReaderRef},
		{"dot-dot inside ref", func(c *CardReader) { c.Name = "X"; c.ReaderRef = "tmr_1..x" }, ErrCardReaderRef},
		{"leading dot ref", func(c *CardReader) { c.Name = "X"; c.ReaderRef = ".tmr_1" }, ErrCardReaderRef},
		{"duplicate ref", func(c *CardReader) { c.Name = "X"; c.ReaderRef = "tmr_1" }, ErrCardReaderRef},
		{"bad kind", func(c *CardReader) { c.Name = "X"; c.ReaderRef = "tmr_3"; c.Kind = "wormhole" }, ErrCardReaderKind},
	}
	for _, tc := range cases {
		c := base
		tc.mut(&c)
		if _, err := repo.Create(ctx, c); !errors.Is(err, tc.want) {
			t.Errorf("%s: err=%v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestCreate_CapOf32ActiveRows(t *testing.T) {
	ctx := context.Background()
	db := newCardReaderTestDB(t)
	repo := NewCardReaderRepo(db)
	for i := 0; i < MaxActiveCardReaders; i++ {
		if _, err := repo.Create(ctx, CardReader{Name: fmt.Sprintf("R%d", i), MethodKey: "m", PluginID: "p", ReaderRef: fmt.Sprintf("ref%d", i)}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	if _, err := repo.Create(ctx, CardReader{Name: "one too many", MethodKey: "m", PluginID: "p", ReaderRef: "refX"}); !errors.Is(err, ErrCardReaderLimit) {
		t.Fatalf("err=%v, want ErrCardReaderLimit", err)
	}
	rows, _ := repo.ListActive(ctx)
	if _, err := repo.SoftRemove(ctx, rows[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Create(ctx, CardReader{Name: "one too many", MethodKey: "m", PluginID: "p", ReaderRef: "refX"}); err != nil {
		t.Fatalf("a removed row frees a slot: %v", err)
	}
}

func TestCreate_RevivesRemovedRowWithSameRef(t *testing.T) {
	ctx := context.Background()
	db := newCardReaderTestDB(t)
	repo := NewCardReaderRepo(db)
	a, _ := repo.Create(ctx, CardReader{Name: "Old", MethodKey: "m", PluginID: "p", ReaderRef: "ref1"})
	if _, err := repo.SoftRemove(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	b, err := repo.Create(ctx, CardReader{Name: "New", MethodKey: "m", PluginID: "p", ReaderRef: "ref1"})
	if err != nil {
		t.Fatalf("re-adding a removed reader: %v", err)
	}
	if b.ID != a.ID || b.Name != "New" || b.Source != CardReaderSourceManual {
		t.Fatalf("want the old row revived and renamed, got %+v", b)
	}
}

func TestDefaultAndLastUsed(t *testing.T) {
	ctx := context.Background()
	db := newCardReaderTestDB(t)
	repo := NewCardReaderRepo(db)
	a, _ := repo.Create(ctx, CardReader{Name: "A", MethodKey: "m", PluginID: "p", ReaderRef: "ra"})
	b, _ := repo.Create(ctx, CardReader{Name: "B", MethodKey: "m", PluginID: "p", ReaderRef: "rb"})
	rows, _ := repo.ListActive(ctx)
	if rows[0].ID != a.ID || rows[1].ID != b.ID {
		t.Fatalf("creation order is the sort order: %+v", rows)
	}
	if err := repo.TouchLastUsed(ctx, b.ID); err != nil {
		t.Fatal(err)
	}
	// survive a repo reopen
	got, err := NewCardReaderRepo(db).Get(ctx, b.ID)
	if err != nil || got.LastUsedAt == "" {
		t.Fatalf("last_used_at not persisted: %+v err=%v", got, err)
	}
	if got, _ := repo.Get(ctx, a.ID); got.LastUsedAt != "" {
		t.Fatalf("untouched row must have no last_used_at")
	}
}

func TestUnattendedDefaultIsOnePerTill(t *testing.T) {
	ctx := context.Background()
	db := newCardReaderTestDB(t)
	repo := NewCardReaderRepo(db)
	a, _ := repo.Create(ctx, CardReader{Name: "A", MethodKey: "m", PluginID: "p", ReaderRef: "ra"})
	b, _ := repo.Create(ctx, CardReader{Name: "B", MethodKey: "m", PluginID: "p", ReaderRef: "rb"})
	if err := repo.SetUnattendedDefault(ctx, a.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetUnattendedDefault(ctx, b.ID, true); err != nil {
		t.Fatal(err)
	}
	ga, _ := repo.Get(ctx, a.ID)
	gb, _ := repo.Get(ctx, b.ID)
	if ga.UnattendedDefault || !gb.UnattendedDefault {
		t.Fatalf("ticking B must move the default off A: a=%v b=%v", ga.UnattendedDefault, gb.UnattendedDefault)
	}
	tap, _ := repo.Create(ctx, CardReader{Name: "Tap", MethodKey: "m", PluginID: "p", ReaderRef: "rt", Kind: CardReaderKindTapToPay})
	if err := repo.SetUnattendedDefault(ctx, tap.ID, true); !errors.Is(err, ErrCardReaderKind) {
		t.Fatalf("a tap_to_pay reader is never the self-order reader: %v", err)
	}
}

func TestRenameAndMove(t *testing.T) {
	ctx := context.Background()
	db := newCardReaderTestDB(t)
	repo := NewCardReaderRepo(db)
	a, _ := repo.Create(ctx, CardReader{Name: "A", MethodKey: "m", PluginID: "p", ReaderRef: "ra"})
	b, _ := repo.Create(ctx, CardReader{Name: "B", MethodKey: "m", PluginID: "p", ReaderRef: "rb"})
	if err := repo.Rename(ctx, a.ID, "B"); !errors.Is(err, ErrCardReaderName) {
		t.Fatalf("rename to a taken name: %v", err)
	}
	if err := repo.Rename(ctx, a.ID, "Front"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Move(ctx, b.ID, -1); err != nil {
		t.Fatal(err)
	}
	rows, _ := repo.ListActive(ctx)
	if rows[0].ID != b.ID || rows[1].ID != a.ID || rows[1].Name != "Front" {
		t.Fatalf("move up failed: %+v", rows)
	}
	if err := repo.Rename(ctx, "nope", "Z"); !errors.Is(err, ErrCardReaderNotFound) {
		t.Fatalf("rename of unknown id: %v", err)
	}
}
