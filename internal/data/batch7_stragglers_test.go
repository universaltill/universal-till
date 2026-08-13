package data

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// Coverage batch 7's straggler gaps: the last zero-coverage functions in
// InvoiceRepo, InstallStatusRepo, SettingsRepo, ModifierRepo, and the
// POSRepo sale-line-modifier writer that Phase-5 kitchen tickets read back.

func TestInvoiceRepo_BySaleAndByDisplayNo(t *testing.T) {
	dbo, err := db.Open(filepath.Join(t.TempDir(), "invoices.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()

	mustExec(t, dbo, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES('sale1', 'R1', 'completed', 'sale', 'GBP', 100, 0, 20, 120, '2026-01-01T10:00:00Z', '2026-01-01T10:00:00Z')`)

	repo := NewInvoiceRepo(dbo.DB)
	inv, err := repo.Create(ctx, InvoiceInput{
		Kind: "invoice", SaleID: "sale1",
		CustomerName: "Walk-in", SellerJSON: "{}", VATBreakdownJSON: "[]",
		NetTotal: 100, TaxTotal: 20, GrossTotal: 120,
		IssuedAt: "2026-01-01T10:05:00Z", IssuedBy: "user1",
	})
	if err != nil {
		t.Fatal(err)
	}
	credit, err := repo.Create(ctx, InvoiceInput{
		Kind: "credit_note", SaleID: "sale1",
		CustomerName: "Walk-in", SellerJSON: "{}", VATBreakdownJSON: "[]",
		NetTotal: -100, TaxTotal: -20, GrossTotal: -120,
		IssuedAt: "2026-01-02T10:05:00Z", IssuedBy: "user1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// BySale is kind-filtered: the refund page asks for the credit note
	// specifically and must not get the invoice back (and vice versa).
	got, ok, err := repo.BySale(ctx, "sale1", "invoice")
	if err != nil || !ok {
		t.Fatalf("BySale(invoice): ok=%v err=%v", ok, err)
	}
	if got.ID != inv.ID || got.Kind != "invoice" {
		t.Fatalf("BySale(invoice) returned wrong row: %+v", got)
	}
	got, ok, err = repo.BySale(ctx, "sale1", "credit_note")
	if err != nil || !ok || got.ID != credit.ID {
		t.Fatalf("BySale(credit_note): got %+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, err := repo.BySale(ctx, "sale-none", "invoice"); err != nil || ok {
		t.Fatalf("BySale(unknown sale) must be not-found: ok=%v err=%v", ok, err)
	}

	// ByDisplayNo resolves the printed number a customer reads out.
	got, ok, err = repo.ByDisplayNo(ctx, inv.DisplayNo)
	if err != nil || !ok || got.ID != inv.ID {
		t.Fatalf("ByDisplayNo(%q): got %+v ok=%v err=%v", inv.DisplayNo, got, ok, err)
	}
	if _, ok, err := repo.ByDisplayNo(ctx, "INV-9999"); err != nil || ok {
		t.Fatalf("ByDisplayNo(unknown) must be not-found: ok=%v err=%v", ok, err)
	}
}

func TestInstallStatusRepo_Get(t *testing.T) {
	dbo, err := db.Open(filepath.Join(t.TempDir(), "install.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	repo := NewInstallStatusRepo(dbo.DB)

	row := InstallStatusRow{
		ListingID: "listing-1", PluginID: "com.example.a", PluginName: "Example",
		TargetVersion: "1.2.0", CurrentVersion: "1.1.0", State: "installing",
		MessageKey: "plugins.install.downloading", Retryable: true,
		UpdatedAt: "2026-01-01T00:00:00Z",
	}
	if err := repo.Upsert(ctx, row); err != nil {
		t.Fatal(err)
	}

	got, ok, err := repo.Get(ctx, "listing-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got != row {
		t.Fatalf("Get round-trip mismatch:\n got %+v\nwant %+v", got, row)
	}
	if _, ok, err := repo.Get(ctx, "listing-none"); err != nil || ok {
		t.Fatalf("Get(unknown) must be not-found: ok=%v err=%v", ok, err)
	}
}

// ut-docs#368 (second Opus review round, NEW BLOCKER): several install-flow
// call sites Save lifecycle-only records (Requested / a classified Failure)
// without knowing the plugin's ID or name — for a listing that ALREADY has a
// successfully-installed plugin on record, a full-column upsert let that
// blank plugin_id clobber the stored one, and convergePluginSet's prune loop
// (which requires a non-blank plugin_id) could then never uninstall the
// plugin from a replica again. Identity fields, once learned, must survive a
// partial update; a non-blank incoming value still overwrites normally.
func TestInstallStatusRepo_UpsertBlankIdentityDoesNotClobber(t *testing.T) {
	dbo, err := db.Open(filepath.Join(t.TempDir(), "install-identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	repo := NewInstallStatusRepo(dbo.DB)

	if err := repo.Upsert(ctx, InstallStatusRow{
		ListingID: "listing-1", PluginID: "com.example.tax", PluginName: "Tax Plugin",
		CurrentVersion: "1.0.0", State: "active", UpdatedAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	// A failed re-fetch attempt writes lifecycle fields only — blank identity.
	if err := repo.Upsert(ctx, InstallStatusRow{
		ListingID: "listing-1", State: "failed",
		MessageKey: "plugins.install.error.retryable", Retryable: true,
		UpdatedAt: "2026-01-02T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := repo.Get(ctx, "listing-1")
	if err != nil || !ok {
		t.Fatalf("Get: ok=%v err=%v", ok, err)
	}
	if got.PluginID != "com.example.tax" {
		t.Fatalf("blank plugin_id must not clobber the stored one: got %q, want %q", got.PluginID, "com.example.tax")
	}
	if got.PluginName != "Tax Plugin" {
		t.Fatalf("blank plugin_name must not clobber the stored one: got %q, want %q", got.PluginName, "Tax Plugin")
	}
	// Lifecycle fields still update normally.
	if got.State != "failed" || got.MessageKey != "plugins.install.error.retryable" || !got.Retryable {
		t.Fatalf("lifecycle fields must still overwrite: got %+v", got)
	}

	// A NON-blank incoming identity still overwrites (e.g. a listing re-keyed
	// to a successor plugin ID by a later successful install).
	if err := repo.Upsert(ctx, InstallStatusRow{
		ListingID: "listing-1", PluginID: "com.example.tax2", PluginName: "Tax Plugin v2",
		CurrentVersion: "2.0.0", State: "active", UpdatedAt: "2026-01-03T00:00:00Z",
	}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = repo.Get(ctx, "listing-1")
	if got.PluginID != "com.example.tax2" || got.PluginName != "Tax Plugin v2" {
		t.Fatalf("non-blank identity must still overwrite: got %+v", got)
	}
}

func TestSettingsRepo_All(t *testing.T) {
	dbo, err := db.Open(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	repo := NewSettingsRepo(dbo.DB)

	// The migrated schema may seed settings — clear so the empty case is real.
	mustExec(t, dbo, `DELETE FROM settings`)

	all, err := repo.All(ctx)
	if err != nil {
		t.Fatalf("All(empty): %v", err)
	}
	if all == nil || len(all) != 0 {
		t.Fatalf("All on empty table must be an empty, non-nil map, got %#v", all)
	}

	if err := repo.Set(ctx, "shop.name", "Task Runner"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, "display.mode", "self_order"); err != nil {
		t.Fatal(err)
	}
	all, err = repo.All(ctx)
	if err != nil {
		t.Fatalf("All: %v", err)
	}
	if len(all) != 2 || all["shop.name"] != "Task Runner" || all["display.mode"] != "self_order" {
		t.Fatalf("All returned wrong map: %#v", all)
	}

	// Error paths (also exercise repoObservability's wrap and wrapf non-nil
	// branches): a closed DB must surface an error, not a silent empty map.
	dbo.Close()
	if _, err := repo.All(ctx); err == nil {
		t.Fatal("All on a closed DB must error")
	}
	if _, _, err := repo.Get(ctx, "shop.name"); err == nil {
		t.Fatal("Get on a closed DB must error")
	}
}

func TestInvoiceRepo_Create_SeriesNumberingAndDuplicateGuard(t *testing.T) {
	dbo, err := db.Open(filepath.Join(t.TempDir(), "invoices2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	repo := NewInvoiceRepo(dbo.DB)

	for _, sale := range []string{"s1", "s2", "s3"} {
		mustExec(t, dbo, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, 'R-'||?, 'completed', 'sale', 'GBP', 100, 0, 0, 100, '2026-01-01T10:00:00Z', '2026-01-01T10:00:00Z')`, sale, sale)
	}
	mk := func(series, kind, sale string) (InvoiceRow, error) {
		return repo.Create(ctx, InvoiceInput{
			Series: series, Kind: kind, SaleID: sale,
			CustomerName: "Walk-in", SellerJSON: "{}", VATBreakdownJSON: "[]",
			NetTotal: 100, GrossTotal: 100,
			IssuedAt: "2026-01-01T10:05:00Z", IssuedBy: "u1",
		})
	}

	// Numbering is per-series and sequential; the printed number embeds the
	// series prefix (a replica till's receipt prefix keeps its invoices from
	// colliding with the primary's).
	inv1, err := mk("", "invoice", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if inv1.InvoiceNo != 1 || inv1.DisplayNo != "INV-000001" {
		t.Fatalf("first invoice numbering wrong: %+v", inv1)
	}
	inv2, err := mk("", "invoice", "s2")
	if err != nil {
		t.Fatal(err)
	}
	if inv2.InvoiceNo != 2 || inv2.DisplayNo != "INV-000002" {
		t.Fatalf("second invoice numbering wrong: %+v", inv2)
	}
	invT2, err := mk("T2-", "invoice", "s3")
	if err != nil {
		t.Fatal(err)
	}
	if invT2.InvoiceNo != 1 || invT2.DisplayNo != "T2-INV-000001" {
		t.Fatalf("replica-series invoice must number independently with its prefix: %+v", invT2)
	}

	// UNIQUE(sale_id, kind): a sale can't be invoiced twice — and the retry
	// loop must recognize this as permanent, not spin on it.
	if _, err := mk("", "invoice", "s1"); err == nil {
		t.Fatal("second invoice for the same sale must error")
	}
	var n int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM invoices WHERE sale_id = 's1' AND kind = 'invoice'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("duplicate attempt must not insert, got %d rows", n)
	}
}

func TestModifierRepo_ItemIDsWithModifiers(t *testing.T) {
	dbo, err := db.Open(filepath.Join(t.TempDir(), "mods.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	repo := NewModifierRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-mod','SKU-M','Coffee',300,1)`)
	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-retired','SKU-R','Old Coffee',300,1)`)
	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-plain','SKU-P','Water',100,1)`)
	if _, err := repo.CreateGroup(ctx, "g-active", "itm-mod", "Extras", false, 0, 2, 0); err != nil {
		t.Fatal(err)
	}
	gid, err := repo.CreateGroup(ctx, "g-retired", "itm-retired", "Extras", false, 0, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, dbo, `UPDATE item_modifier_groups SET is_active = 0 WHERE id = ?`, gid)

	// Empty input: nothing to look up, and no SQL "IN ()" syntax error.
	got, err := repo.ItemIDsWithModifiers(ctx, nil)
	if err != nil {
		t.Fatalf("ItemIDsWithModifiers(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty result for empty input, got %#v", got)
	}

	got, err = repo.ItemIDsWithModifiers(ctx, []string{"itm-mod", "itm-retired", "itm-plain", "itm-ghost"})
	if err != nil {
		t.Fatalf("ItemIDsWithModifiers: %v", err)
	}
	if !got["itm-mod"] {
		t.Fatal("item with an active group must be flagged")
	}
	// The button grid uses "absent == no customization step" — an item whose
	// only group was retired must NOT be flagged, or every tap would open a
	// modifier picker with nothing in it.
	for _, id := range []string{"itm-retired", "itm-plain", "itm-ghost"} {
		if got[id] {
			t.Fatalf("%s must not be flagged: %#v", id, got)
		}
	}
}

func TestPOSRepo_InsertSaleLineModifiers(t *testing.T) {
	dbo, err := db.Open(filepath.Join(t.TempDir(), "slm.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbo.Close()
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-1','SKU1','Flat White',370,1)`)
	mustExec(t, dbo, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-1','R-1','completed','sale','GBP',470,0,0,470,datetime('now'))`)
	mustExec(t, dbo, `INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-1','sale-1',1,'itm-1','Flat White',1,470,0,0,0,470,470)`)

	// Runs inside the same tx as InsertSaleLine in production — honor that.
	tx, err := dbo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	mods := []SelectedModifier{
		{GroupID: "g1", OptionID: "o1", GroupName: "Extras", OptionName: "Extra shot", PriceDeltaMinor: 50},
		// Empty group/option ids must persist as NULL, not "" — the columns
		// are nullable with no FK (migration 017), and NULL is what keeps
		// "no source group" queryable via IS NULL. The live sale write path
		// (pos_modifiers_api.go) always supplies real ids, so this exercises
		// the repo's defensive nullableString handling, not a live flow.
		{GroupID: "", OptionID: "", GroupName: "Extras", OptionName: "Oat milk", PriceDeltaMinor: 50},
	}
	if err := repo.InsertSaleLineModifiers(ctx, tx, "line-1", mods); err != nil {
		tx.Rollback()
		t.Fatalf("InsertSaleLineModifiers: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	type row struct {
		groupID, optionID     *string
		groupName, optionName string
		delta                 int64
	}
	var rows []row
	res, err := dbo.DB.Query(`SELECT group_id, option_id, group_name_snapshot, option_name_snapshot, price_delta_minor FROM sale_line_modifiers WHERE sale_line_id = 'line-1' ORDER BY option_name_snapshot`)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Close()
	for res.Next() {
		var r row
		if err := res.Scan(&r.groupID, &r.optionID, &r.groupName, &r.optionName, &r.delta); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 modifier rows, got %d", len(rows))
	}
	// "Extra shot" sorts first.
	if rows[0].groupID == nil || *rows[0].groupID != "g1" || rows[0].optionID == nil || *rows[0].optionID != "o1" {
		t.Fatalf("first row lost its ids: %+v", rows[0])
	}
	if rows[0].optionName != "Extra shot" || rows[0].delta != 50 {
		t.Fatalf("first row snapshot wrong: %+v", rows[0])
	}
	if rows[1].groupID != nil || rows[1].optionID != nil {
		t.Fatalf("empty ids must persist as NULL: %+v", rows[1])
	}

	// Empty mods: a no-op that must not touch the table or error.
	tx2, err := dbo.DB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertSaleLineModifiers(ctx, tx2, "line-1", nil); err != nil {
		tx2.Rollback()
		t.Fatalf("InsertSaleLineModifiers(nil): %v", err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := dbo.DB.QueryRow(`SELECT COUNT(*) FROM sale_line_modifiers WHERE sale_line_id = 'line-1'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("empty-mods call must not change rows, got %d", n)
	}
}
