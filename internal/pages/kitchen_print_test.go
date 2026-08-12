package pages

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
	"github.com/universaltill/universal-till/internal/settings"
)

// buildKitchenTicket must carry each item's chosen modifiers through to the
// cook — the whole point of ADR-0020's item modifiers (Farshid: "customer
// should be able to add extra shot to the coffee or customize the
// sandwich") is that the kitchen actually gets told. The print.KitchenTicket
// rendering already supported per-item Modifiers (internal/print/kitchen.go)
// before this change; the gap was that buildKitchenTicket never populated
// them from the persisted sale.
func TestBuildKitchenTicket_IncludesLineModifiers(t *testing.T) {
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "kitchen.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbase.Close()

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dbase.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-coffee','COFFEE','Flat White',370,1)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-1','R-0099','completed','sale','GBP',370,0,0,370,datetime('now'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-1','sale-1',1,'itm-coffee','Flat White',1,370,0,0,0,370,370)`)
	mustExec(`INSERT INTO sale_line_modifiers (id, sale_line_id, group_name_snapshot, option_name_snapshot, price_delta_minor) VALUES ('slm-1','line-1','Extras','Extra shot',50)`)

	dp := &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB)}
	ticket, err := buildKitchenTicket(context.Background(), dp, "R-0099")
	if err != nil {
		t.Fatalf("buildKitchenTicket: %v", err)
	}
	if len(ticket.Items) != 1 {
		t.Fatalf("want 1 item, got %d", len(ticket.Items))
	}
	if got := ticket.Items[0].Modifiers; len(got) != 1 || got[0] != "Extra shot" {
		t.Fatalf("want kitchen ticket item to carry [Extra shot], got %+v", got)
	}
}

// TestBuildKitchenTicket_IncludesOrderType covers ut-docs#181: order_type
// is now persisted on the sales row (was tracked in-memory only and
// discarded at completion), so a kitchen ticket built from a real
// takeaway sale must show TAKEAWAY -- buildKitchenTicket's own comment
// used to say "not yet — optional fields" because SaleDetail had nothing
// to read it from.
func TestBuildKitchenTicket_IncludesOrderType(t *testing.T) {
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "kitchen-order-type.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer dbase.Close()

	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dbase.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-bagel','BAGEL','Bagel',250,1)`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, order_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-2','R-0100','completed','sale','takeaway','GBP',250,0,0,250,datetime('now'))`)
	mustExec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax) VALUES ('line-2','sale-2',1,'itm-bagel','Bagel',1,250,0,0,0,250,250)`)

	dp := &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB)}
	ticket, err := buildKitchenTicket(context.Background(), dp, "R-0100")
	if err != nil {
		t.Fatalf("buildKitchenTicket: %v", err)
	}
	if ticket.OrderType != "takeaway" {
		t.Fatalf("ticket.OrderType = %q, want %q", ticket.OrderType, "takeaway")
	}
}

// --- Kitchen station routing (universaltill/ut-docs#516) ---------------------
//
// printKitchen now resolves each sale line to zero or more kitchen stations
// (item override beats category, unrouted lines fall back to the legacy
// default kitchen printer) and sends one ticket per target, independently.

// kitchenRoutingDeps opens a migrated DB with a settings store and seeds a
// small catalog: Food (burger, bread) and Drinks (cola), plus a combo item.
func kitchenRoutingDeps(t *testing.T) (*common.Deps, *db.DB) {
	t.Helper()
	chdirRoot(t)
	dbase, err := db.Open(filepath.Join(t.TempDir(), "kitchen-routing.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbase.Close() })
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := dbase.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec %s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO categories (id, name) VALUES ('cat-food','Food'), ('cat-drinks','Drinks')`)
	mustExec(`INSERT INTO items (id, sku, name, base_price, category_id, is_active) VALUES
		('itm-steak','STEAK','Steak',1500,'cat-food',1),
		('itm-bread','BREAD','Bread',200,'cat-food',1),
		('itm-cola','COLA','Cola',250,'cat-drinks',1),
		('itm-combo','COMBO','Combo',1800,'cat-food',1)`)
	return &common.Deps{Db: dbase.DB, Settings: settings.NewStore(dbase.DB)}, dbase
}

// seedKitchenSale inserts a completed sale whose lines reference the given
// item ids (name_snapshot = the item's catalog name).
func seedKitchenSale(t *testing.T, dbase *db.DB, receiptNo string, itemIDs ...string) {
	t.Helper()
	saleID := "sale-" + receiptNo
	if _, err := dbase.DB.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at)
		VALUES (?,?,'completed','sale','GBP',0,0,0,0,datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatal(err)
	}
	for i, itemID := range itemIDs {
		var name string
		if err := dbase.DB.QueryRow(`SELECT name FROM items WHERE id = ?`, itemID).Scan(&name); err != nil {
			t.Fatalf("look up %s: %v", itemID, err)
		}
		if _, err := dbase.DB.Exec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
			VALUES (?,?,?,?,?,1,0,0,0,0,0,0)`, saleID+"-l"+name, saleID, i+1, itemID, name); err != nil {
			t.Fatal(err)
		}
	}
}

// printerFile creates an empty file usable as a device-transport printer
// address, returning its path.
func printerFile(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// CRITICAL regression pin (ut-docs#516): with ZERO kitchen stations
// configured — every existing shop on migration day — printKitchen must
// produce EXACTLY today's behavior: one ticket, Station "KITCHEN", sent to
// the legacy printer.kitchen_addr, byte-for-byte identical to the legacy
// single-ticket render.
func TestPrintKitchen_ZeroStations_ByteIdenticalLegacyTicket(t *testing.T) {
	dp, dbase := kitchenRoutingDeps(t)
	_ = dbase
	ctx := context.Background()
	seedKitchenSale(t, dbase, "R-1000", "itm-steak", "itm-cola")

	defaultPrn := printerFile(t, "kitchen.prn")
	if err := dp.Settings.Set(ctx, keyPrinterKitchen, defaultPrn); err != nil {
		t.Fatal(err)
	}

	total, failures, err := printKitchen(ctx, dp, "R-1000", "")
	if err != nil {
		t.Fatalf("printKitchen: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %+v", failures)
	}
	if total != 1 {
		t.Fatalf("zero stations must produce exactly one ticket, got %d", total)
	}

	got, err := os.ReadFile(defaultPrn)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := buildKitchenTicket(ctx, dp, "R-1000")
	if err != nil {
		t.Fatal(err)
	}
	if legacy.Station != kitchenStation {
		t.Fatalf("legacy ticket station = %q, want %q", legacy.Station, kitchenStation)
	}
	want := print.RenderKitchenTicket(legacy)
	if !bytes.Equal(got, want) {
		t.Fatalf("zero-stations output is not byte-identical to the legacy ticket\ngot:  %q\nwant: %q", got, want)
	}
}

// Routed lines land on their station's printer, unrouted lines fall into the
// default KITCHEN bucket, and a line routed to two stations prints on BOTH
// tickets (product requirement: "kitchen or kitchen and customer printer").
func TestPrintKitchen_MultiStationRoutingAndDuplication(t *testing.T) {
	dp, dbase := kitchenRoutingDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dbase.DB)

	grillPrn := printerFile(t, "grill.prn")
	barPrn := printerFile(t, "bar.prn")
	defaultPrn := printerFile(t, "kitchen.prn")
	if err := dp.Settings.Set(ctx, keyPrinterKitchen, defaultPrn); err != nil {
		t.Fatal(err)
	}

	grill, err := repo.CreateKitchenStation(ctx, "Grill", grillPrn)
	if err != nil {
		t.Fatal(err)
	}
	bar, err := repo.CreateKitchenStation(ctx, "Bar", barPrn)
	if err != nil {
		t.Fatal(err)
	}
	// Steak → Grill (item route); Cola → Bar (via Drinks category);
	// Combo → BOTH; Bread → unrouted (default bucket).
	if err := repo.SetItemStationRoutes(ctx, "itm-steak", []string{grill}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetCategoryStationRoutes(ctx, "cat-drinks", []string{bar}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetItemStationRoutes(ctx, "itm-combo", []string{grill, bar}); err != nil {
		t.Fatal(err)
	}

	seedKitchenSale(t, dbase, "R-1001", "itm-steak", "itm-cola", "itm-combo", "itm-bread")

	total, failures, err := printKitchen(ctx, dp, "R-1001", "")
	if err != nil {
		t.Fatalf("printKitchen: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %+v", failures)
	}
	if total != 3 {
		t.Fatalf("want 3 tickets (Grill, Bar, default), got %d", total)
	}

	read := func(p string) []byte {
		t.Helper()
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	grillOut, barOut, defOut := read(grillPrn), read(barPrn), read(defaultPrn)

	assertHas := func(out []byte, label, s string, want bool) {
		t.Helper()
		if got := bytes.Contains(out, []byte(s)); got != want {
			t.Errorf("%s ticket: contains(%q) = %v, want %v", label, s, got, want)
		}
	}
	// Grill: header + Steak + Combo, no Cola/Bread.
	assertHas(grillOut, "grill", "Grill", true)
	assertHas(grillOut, "grill", "Steak", true)
	assertHas(grillOut, "grill", "Combo", true)
	assertHas(grillOut, "grill", "Cola", false)
	assertHas(grillOut, "grill", "Bread", false)
	// Bar: header + Cola + Combo (duplicated line), no Steak/Bread.
	assertHas(barOut, "bar", "Bar", true)
	assertHas(barOut, "bar", "Cola", true)
	assertHas(barOut, "bar", "Combo", true)
	assertHas(barOut, "bar", "Steak", false)
	assertHas(barOut, "bar", "Bread", false)
	// Default bucket: legacy KITCHEN header + only the unrouted Bread.
	assertHas(defOut, "default", kitchenStation, true)
	assertHas(defOut, "default", "Bread", true)
	assertHas(defOut, "default", "Steak", false)
	assertHas(defOut, "default", "Cola", false)
	assertHas(defOut, "default", "Combo", false)
}

// One target's send failure must not stop the others (offline-first): the
// failing station is reported + audited, the healthy one still prints.
func TestPrintKitchen_OneTargetFailureDoesNotBlockOthers(t *testing.T) {
	dp, dbase := kitchenRoutingDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dbase.DB)

	barPrn := printerFile(t, "bar.prn")
	grill, err := repo.CreateKitchenStation(ctx, "Grill", filepath.Join(t.TempDir(), "missing-dir", "grill.prn"))
	if err != nil {
		t.Fatal(err)
	}
	bar, err := repo.CreateKitchenStation(ctx, "Bar", barPrn)
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetItemStationRoutes(ctx, "itm-steak", []string{grill}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetItemStationRoutes(ctx, "itm-cola", []string{bar}); err != nil {
		t.Fatal(err)
	}

	seedKitchenSale(t, dbase, "R-1002", "itm-steak", "itm-cola")

	total, failures, err := printKitchen(ctx, dp, "R-1002", "")
	if err != nil {
		t.Fatalf("printKitchen must not hard-fail on a single dead target: %v", err)
	}
	if total != 2 {
		t.Fatalf("want 2 tickets, got %d", total)
	}
	if len(failures) != 1 || failures[0].Station != "Grill" {
		t.Fatalf("want exactly the Grill target to fail, got %+v", failures)
	}

	barOut, err := os.ReadFile(barPrn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(barOut, []byte("Cola")) {
		t.Fatal("healthy Bar target must still receive its ticket")
	}

	// Each failure is audited separately with the station label.
	var dataJSON string
	if err := dbase.DB.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'kitchen_print_failed'`).Scan(&dataJSON); err != nil {
		t.Fatalf("expected a kitchen_print_failed audit row: %v", err)
	}
	if !bytes.Contains([]byte(dataJSON), []byte("Grill")) {
		t.Fatalf("failure audit must carry the station name, got %s", dataJSON)
	}
}

// Item-level routes OVERRIDE category routes (no union) at the full
// build-and-print integration level, not just in the repo.
func TestPrintKitchen_ItemOverrideBeatsCategory(t *testing.T) {
	dp, dbase := kitchenRoutingDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dbase.DB)

	grillPrn := printerFile(t, "grill.prn")
	barPrn := printerFile(t, "bar.prn")
	grill, err := repo.CreateKitchenStation(ctx, "Grill", grillPrn)
	if err != nil {
		t.Fatal(err)
	}
	bar, err := repo.CreateKitchenStation(ctx, "Bar", barPrn)
	if err != nil {
		t.Fatal(err)
	}
	// Category rule: all Food → Bar. Item override: Steak → Grill only.
	if err := repo.SetCategoryStationRoutes(ctx, "cat-food", []string{bar}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetItemStationRoutes(ctx, "itm-steak", []string{grill}); err != nil {
		t.Fatal(err)
	}

	seedKitchenSale(t, dbase, "R-1003", "itm-steak")

	total, failures, err := printKitchen(ctx, dp, "R-1003", "")
	if err != nil {
		t.Fatalf("printKitchen: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("unexpected failures: %+v", failures)
	}
	if total != 1 {
		t.Fatalf("want exactly 1 ticket (Grill), got %d", total)
	}

	grillOut, _ := os.ReadFile(grillPrn)
	barOut, _ := os.ReadFile(barPrn)
	if !bytes.Contains(grillOut, []byte("Steak")) {
		t.Fatal("Steak must print at the item-override station (Grill)")
	}
	if len(barOut) != 0 {
		t.Fatalf("Bar must receive NO ticket (override beats category), got %d bytes", len(barOut))
	}
}

// A station with no printer address configured must not silently swallow
// its lines (code review, ut-docs#516) — the settings page now rejects a
// blank address at create/update time, but this pins the defense-in-depth
// fallback for any station that ends up blank anyway (legacy data, a
// direct repo call): its lines land in the default bucket instead of
// vanishing.
func TestPrintKitchen_BlankAddressStationFallsBackToDefault(t *testing.T) {
	dp, dbase := kitchenRoutingDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dbase.DB)

	defaultPrn := printerFile(t, "kitchen.prn")
	if err := dp.Settings.Set(ctx, keyPrinterKitchen, defaultPrn); err != nil {
		t.Fatal(err)
	}

	blank, err := repo.CreateKitchenStation(ctx, "No Address", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetItemStationRoutes(ctx, "itm-steak", []string{blank}); err != nil {
		t.Fatal(err)
	}

	seedKitchenSale(t, dbase, "R-1004", "itm-steak")

	total, failures, err := printKitchen(ctx, dp, "R-1004", "")
	if err != nil {
		t.Fatalf("printKitchen: %v", err)
	}
	if len(failures) != 0 {
		t.Fatalf("a blank-address station must never even become a send target: %+v", failures)
	}
	if total != 1 {
		t.Fatalf("want exactly 1 ticket (the default bucket), got %d", total)
	}

	got, err := os.ReadFile(defaultPrn)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("Steak")) {
		t.Fatal("Steak must still reach the default kitchen printer, not vanish")
	}
}
