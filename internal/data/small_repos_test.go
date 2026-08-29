package data

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// This file covers the remaining zero-coverage functions across several
// small, single-purpose repos: HeldSalesRepo and TillsRepo (both fully
// untested), and one gap each in InstallStatusRepo, ModifierRepo,
// RelatedItemsRepo, and SettingsRepo.

func newHeldSalesTestDB(t *testing.T) *HeldSalesRepo {
	t.Helper()
	dbc, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbc.Close() })
	if _, err := dbc.Exec(`CREATE TABLE held_sales (id TEXT PRIMARY KEY, label TEXT NOT NULL DEFAULT '', total_minor INTEGER NOT NULL DEFAULT 0, line_count INTEGER NOT NULL DEFAULT 0, payload TEXT NOT NULL, table_id TEXT, created_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		t.Fatal(err)
	}
	return NewHeldSalesRepo(dbc)
}

func TestHeldSalesRepo_InsertGetListDelete(t *testing.T) {
	repo := newHeldSalesTestDB(t)
	ctx := context.Background()

	if _, ok, err := repo.Get(ctx, "missing"); err != nil || ok {
		t.Fatalf("expected no held sale yet, got ok=%v err=%v", ok, err)
	}
	if list, err := repo.List(ctx); err != nil || len(list) != 0 {
		t.Fatalf("expected an empty list, got %+v err=%v", list, err)
	}

	if err := repo.Insert(ctx, HeldSale{ID: "h1", Label: "Table 4", TotalMinor: 1200, LineCount: 3, Payload: `{"lines":[]}`}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(ctx, HeldSale{ID: "h2", Label: "Table 5", TotalMinor: 500, LineCount: 1, Payload: `{}`}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := repo.Get(ctx, "h1")
	if err != nil || !ok {
		t.Fatalf("expected h1, got ok=%v err=%v", ok, err)
	}
	if got.Label != "Table 4" || got.TotalMinor != 1200 || got.LineCount != 3 {
		t.Fatalf("unexpected held sale: %+v", got)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 held sales, got %+v", list)
	}

	if err := repo.Delete(ctx, "h1"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repo.Get(ctx, "h1"); err != nil || ok {
		t.Fatalf("expected h1 gone after delete, got ok=%v err=%v", ok, err)
	}
	list, err = repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != "h2" {
		t.Fatalf("expected only h2 remaining, got %+v", list)
	}

	// Deleting an id that was never held is a no-op, not an error.
	if err := repo.Delete(ctx, "never-existed"); err != nil {
		t.Fatalf("expected no error deleting an unknown held sale, got %v", err)
	}
}

// ut-docs#820: a held sale's assigned table survives Insert/Get/List, and
// SetTable is the "move a parked order to a different table" write --
// updating table_id alone, leaving everything else about the held sale
// (its payload, its label, its total) untouched.
func TestHeldSalesRepo_TableID(t *testing.T) {
	repo := newHeldSalesTestDB(t)
	ctx := context.Background()

	if err := repo.Insert(ctx, HeldSale{ID: "h1", Label: "Table 4", TotalMinor: 1200, LineCount: 3, Payload: `{"lines":[]}`, TableID: "tbl-1"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Insert(ctx, HeldSale{ID: "h2", Label: "Walk-in", TotalMinor: 500, LineCount: 1, Payload: `{}`}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := repo.Get(ctx, "h1")
	if err != nil || !ok {
		t.Fatalf("Get h1: ok=%v err=%v", ok, err)
	}
	if got.TableID != "tbl-1" {
		t.Fatalf("Get h1: TableID = %q, want tbl-1", got.TableID)
	}
	got2, ok, err := repo.Get(ctx, "h2")
	if err != nil || !ok {
		t.Fatalf("Get h2: ok=%v err=%v", ok, err)
	}
	if got2.TableID != "" {
		t.Fatalf("Get h2: TableID = %q, want empty", got2.TableID)
	}

	list, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]HeldSale{}
	for _, h := range list {
		byID[h.ID] = h
	}
	if byID["h1"].TableID != "tbl-1" {
		t.Fatalf("List: h1.TableID = %q, want tbl-1", byID["h1"].TableID)
	}
	if byID["h2"].TableID != "" {
		t.Fatalf("List: h2.TableID = %q, want empty", byID["h2"].TableID)
	}

	// SetTable moves h2 onto tbl-2, without touching its other fields.
	if err := repo.SetTable(ctx, "h2", "tbl-2"); err != nil {
		t.Fatal(err)
	}
	moved, ok, err := repo.Get(ctx, "h2")
	if err != nil || !ok {
		t.Fatalf("Get h2 after SetTable: ok=%v err=%v", ok, err)
	}
	if moved.TableID != "tbl-2" {
		t.Fatalf("h2.TableID after SetTable = %q, want tbl-2", moved.TableID)
	}
	if moved.Label != "Walk-in" || moved.TotalMinor != 500 {
		t.Fatalf("SetTable must not disturb other fields, got %+v", moved)
	}

	// SetTable("") clears the assignment (moving a held order off any table).
	if err := repo.SetTable(ctx, "h2", ""); err != nil {
		t.Fatal(err)
	}
	cleared, _, _ := repo.Get(ctx, "h2")
	if cleared.TableID != "" {
		t.Fatalf("h2.TableID after clearing = %q, want empty", cleared.TableID)
	}

	// SetTable on an unknown id is a no-op, not an error -- mirroring
	// Delete's existing convention for an unknown id.
	if err := repo.SetTable(ctx, "never-existed", "tbl-1"); err != nil {
		t.Fatalf("expected no error setting table on an unknown held sale, got %v", err)
	}
}

func newTillsTestDB(t *testing.T) *TillsRepo {
	t.Helper()
	dbc, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dbc.Close() })
	if _, err := dbc.Exec(`CREATE TABLE tills (id TEXT PRIMARY KEY, name TEXT NOT NULL, bearer_hash TEXT NOT NULL UNIQUE, enrolled_at TEXT NOT NULL DEFAULT (datetime('now')), last_seen_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	return NewTillsRepo(dbc)
}

func TestTillsRepo_EnrollListLookupDelete(t *testing.T) {
	repo := newTillsTestDB(t)
	ctx := context.Background()

	id, err := repo.InsertTill(ctx, "Front Till", "hash-abc")
	if err != nil {
		t.Fatal(err)
	}
	if id == "" {
		t.Fatal("expected a non-empty till id")
	}

	tills, err := repo.ListTills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tills) != 1 || tills[0].ID != id || tills[0].Name != "Front Till" {
		t.Fatalf("unexpected till list: %+v", tills)
	}
	if tills[0].LastSeenAt != "" {
		t.Fatalf("expected no last_seen_at before any sync call, got %q", tills[0].LastSeenAt)
	}

	// TillByBearerHash resolves the till AND touches last_seen_at as a
	// side effect (a replica's sync auth doubles as a heartbeat).
	till, ok, err := repo.TillByBearerHash(ctx, "hash-abc")
	if err != nil || !ok || till.ID != id {
		t.Fatalf("expected to resolve the till by its bearer hash, got till=%+v ok=%v err=%v", till, ok, err)
	}
	tills, err = repo.ListTills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tills[0].LastSeenAt == "" {
		t.Fatal("expected last_seen_at to be stamped after TillByBearerHash")
	}

	if _, ok, err := repo.TillByBearerHash(ctx, "wrong-hash"); err != nil || ok {
		t.Fatalf("expected no match for a wrong bearer hash, got ok=%v err=%v", ok, err)
	}

	if err := repo.DeleteTill(ctx, id); err != nil {
		t.Fatal(err)
	}
	tills, err = repo.ListTills(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tills) != 0 {
		t.Fatalf("expected the till revoked, got %+v", tills)
	}
}

// TestTillsRepo_NameTaken covers the enrolment-time uniqueness check
// (ut-docs#1264): a joining till's name must not collide with an
// already-enrolled sibling, case-insensitively.
func TestTillsRepo_NameTaken(t *testing.T) {
	repo := newTillsTestDB(t)
	ctx := context.Background()

	// No tills at all: nothing is taken.
	taken, err := repo.NameTaken(ctx, "Till 2")
	if err != nil {
		t.Fatal(err)
	}
	if taken {
		t.Fatal("expected no name taken in an empty tills table")
	}

	if _, err := repo.InsertTill(ctx, "Till 2", "hash-till2"); err != nil {
		t.Fatal(err)
	}

	// Exact match.
	taken, err = repo.NameTaken(ctx, "Till 2")
	if err != nil {
		t.Fatal(err)
	}
	if !taken {
		t.Fatal("expected an exact-match name to be taken")
	}

	// Case-insensitive match.
	taken, err = repo.NameTaken(ctx, "till 2")
	if err != nil {
		t.Fatal(err)
	}
	if !taken {
		t.Fatal("expected a case-insensitive match to be taken")
	}

	// A different name is free.
	taken, err = repo.NameTaken(ctx, "Till 3")
	if err != nil {
		t.Fatal(err)
	}
	if taken {
		t.Fatal("expected an unused name to be free")
	}

	// Non-ASCII case-insensitivity (independent review finding). SQLite's
	// built-in lower() folds ASCII only, so a `lower(name) = lower(?)`
	// implementation passes every assertion above and still lets "ünite"
	// enrol alongside "Ünite" — on a product that ships tr/fa/ar. These
	// names are the realistic ones, so they are the ones under test.
	if _, err := repo.InsertTill(ctx, "Ünite", "hash-unite"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InsertTill(ctx, "Café", "hash-cafe"); err != nil {
		t.Fatal(err)
	}
	for _, probe := range []string{"ünite", "üNite", "café", "CAFÉ"} {
		taken, err = repo.NameTaken(ctx, probe)
		if err != nil {
			t.Fatal(err)
		}
		if !taken {
			t.Fatalf("expected %q to collide case-insensitively with an existing non-ASCII till name", probe)
		}
	}
}

func TestInstallStatusRepo_DeleteForPlugin(t *testing.T) {
	dbc, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer dbc.Close()
	if _, err := dbc.Exec(`CREATE TABLE plugin_install_status (listing_id TEXT PRIMARY KEY, plugin_id TEXT, plugin_name TEXT, target_version TEXT, current_version TEXT, state TEXT NOT NULL, message_key TEXT, retryable INTEGER NOT NULL DEFAULT 0, updated_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := NewInstallStatusRepo(dbc)

	if err := repo.Upsert(ctx, InstallStatusRow{ListingID: "l1", PluginID: "com.example.a", State: "installed", UpdatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Upsert(ctx, InstallStatusRow{ListingID: "l2", PluginID: "com.example.b", State: "installed", UpdatedAt: "2026-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteForPlugin(ctx, "com.example.a"); err != nil {
		t.Fatal(err)
	}
	rows, err := repo.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].PluginID != "com.example.b" {
		t.Fatalf("expected only com.example.b's record left, got %+v", rows)
	}

	// A plugin id with no records is a no-op, not an error.
	if err := repo.DeleteForPlugin(ctx, "never-installed"); err != nil {
		t.Fatalf("expected no error for an unknown plugin id, got %v", err)
	}
}

func TestModifierRepo_DeleteOption(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "mod.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	repo := NewModifierRepo(d.DB)

	if _, err := d.DB.ExecContext(ctx, `INSERT INTO items(id,sku,name,base_price,is_active,is_weighed,unit) VALUES('itm1','SKU1','Latte',300,1,0,'each')`); err != nil {
		t.Fatal(err)
	}
	groupID, err := repo.CreateGroup(ctx, "grp1", "itm1", "Size", false, 0, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	optID, err := repo.CreateOption(ctx, "opt1", groupID, "Large", 50, 0)
	if err != nil {
		t.Fatal(err)
	}

	groups, err := repo.ListAllGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Options) != 1 {
		t.Fatalf("expected 1 group with 1 option before delete, got %+v", groups)
	}

	if err := repo.DeleteOption(ctx, optID); err != nil {
		t.Fatal(err)
	}
	groups, err = repo.ListAllGroupsForItem(ctx, "itm1")
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Options) != 0 {
		t.Fatalf("expected the option removed, got %+v", groups)
	}

	if err := repo.DeleteOption(ctx, ""); err == nil {
		t.Fatal("expected an error for an empty option id")
	}
}

func TestRelatedItemsRepo_LastRebuilt(t *testing.T) {
	dbc, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer dbc.Close()
	if _, err := dbc.Exec(`CREATE TABLE related_items (item_id TEXT NOT NULL, related_item_id TEXT NOT NULL, support INTEGER NOT NULL, score REAL NOT NULL, updated_at TEXT NOT NULL DEFAULT (datetime('now')), PRIMARY KEY (item_id, related_item_id))`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := NewRelatedItemsRepo(dbc)

	// Empty table: zero time, not an error.
	ts, err := repo.LastRebuilt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ts.IsZero() {
		t.Fatalf("expected a zero time for an empty table, got %v", ts)
	}

	if _, err := dbc.Exec(`INSERT INTO related_items(item_id, related_item_id, support, score, updated_at) VALUES('a','b',5,0.8,'2026-01-01 10:00:00')`); err != nil {
		t.Fatal(err)
	}
	ts, err = repo.LastRebuilt(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ts.IsZero() || ts.Year() != 2026 {
		t.Fatalf("expected a non-zero timestamp from the seeded row, got %v", ts)
	}
}

func TestSettingsRepo_DeleteAndClearReplicaIdentity(t *testing.T) {
	dbc, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer dbc.Close()
	if _, err := dbc.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT, updated_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := NewSettingsRepo(dbc)

	if err := repo.Set(ctx, "shop.name", "Task Runner"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, "shop.name"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := repo.Get(ctx, "shop.name"); err != nil || ok {
		t.Fatalf("expected the setting gone after delete, got ok=%v err=%v", ok, err)
	}
	// Deleting a key that was never set is a no-op, not an error.
	if err := repo.Delete(ctx, "never-set"); err != nil {
		t.Fatalf("expected no error deleting an unknown key, got %v", err)
	}

	// ClearReplicaIdentity: promoting a replica to primary. Every sync.*
	// key goes EXCEPT sync.receipt_prefix (so the till's receipt numbering
	// keeps its prefix and never collides with the old primary's).
	for k, v := range map[string]string{
		"sync.primary_url":    "https://old-primary",
		"sync.last_pulled_at": "2026-01-01T00:00:00Z",
		"sync.receipt_prefix": "T2-",
		"shop.name":           "Task Runner",
	} {
		if err := repo.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.ClearReplicaIdentity(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := repo.Get(ctx, "sync.primary_url"); ok {
		t.Fatal("expected sync.primary_url cleared")
	}
	if _, ok, _ := repo.Get(ctx, "sync.last_pulled_at"); ok {
		t.Fatal("expected sync.last_pulled_at cleared")
	}
	if val, ok, err := repo.Get(ctx, "sync.receipt_prefix"); err != nil || !ok || val != "T2-" {
		t.Fatalf("expected sync.receipt_prefix PRESERVED (receipt numbering must not collide), got val=%q ok=%v err=%v", val, ok, err)
	}
	if val, ok, err := repo.Get(ctx, "shop.name"); err != nil || !ok || val != "Task Runner" {
		t.Fatalf("expected a non-sync.* key untouched, got val=%q ok=%v err=%v", val, ok, err)
	}
}

func TestInvoiceRepo_List(t *testing.T) {
	dbo, err := db.Open(filepath.Join(t.TempDir(), "invoices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	if _, err := dbo.DB.ExecContext(ctx, `DELETE FROM invoices`); err != nil {
		t.Fatal(err)
	}

	if _, err := dbo.DB.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES('sale1', 'R1', 'completed', 'sale', 'GBP', 100, 0, 20, 120, '2026-01-01T10:00:00Z', '2026-01-01T10:00:00Z')`); err != nil {
		t.Fatal(err)
	}

	repo := NewInvoiceRepo(dbo.DB)
	inv, err := repo.Create(ctx, InvoiceInput{
		Series: "", Kind: "invoice", SaleID: "sale1",
		CustomerName: "Jane Doe", SellerJSON: "{}", VATBreakdownJSON: "[]",
		NetTotal: 100, TaxTotal: 20, GrossTotal: 120, IssuedAt: "2026-01-01T10:05:00Z", IssuedBy: "user1",
	})
	if err != nil {
		t.Fatal(err)
	}

	items, err := repo.List(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].DisplayNo != inv.DisplayNo || items[0].ReceiptNo != "R1" {
		t.Fatalf("expected the invoice with its sale's receipt number, got %+v", items)
	}

	// Bounded by date range: a query that starts after the invoice's
	// issued_at must exclude it.
	items, err = repo.List(ctx, "2026-02-01T00:00:00Z", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("expected no invoices in a future-only date range, got %+v", items)
	}

	// A `to` bound on just the date part must include the whole day.
	items, err = repo.List(ctx, "", "2026-01-01")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("expected the invoice included when `to` is a bare date covering issue day, got %+v", items)
	}
}
