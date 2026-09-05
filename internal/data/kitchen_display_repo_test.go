package data

// Kitchen Display, HDMI-local slice (universaltill/ut-docs#544):
// destination_type gains 'both' (a station that prints AND shows on a
// screen); CreateKitchenStation/UpdateKitchenStation take and validate the
// type; ListRecentOrdersForStation is the station-scoped orders query and
// must implement exactly ResolveKitchenStations' precedence.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

func TestCreateKitchenStation_DestinationTypes(t *testing.T) {
	_, repo := openStationTestDB(t)
	ctx := context.Background()

	for _, dt := range []string{KitchenDestinationPrinter, KitchenDestinationDisplay, KitchenDestinationBoth} {
		id, err := repo.CreateKitchenStation(ctx, "S-"+dt, dt, "addr:9100")
		if err != nil {
			t.Fatalf("CreateKitchenStation(%s): %v", dt, err)
		}
		got, ok, err := repo.GetKitchenStation(ctx, id)
		if err != nil || !ok {
			t.Fatalf("GetKitchenStation(%s): ok=%v err=%v", dt, ok, err)
		}
		if got.DestinationType != dt {
			t.Fatalf("DestinationType = %q, want %q", got.DestinationType, dt)
		}
	}

	// Anything outside the fixed vocabulary is rejected with a clear error,
	// BEFORE the INSERT (the schema CHECK is the backstop, not the message).
	for _, bad := range []string{"", "screen", "PRINTER", "printer "} {
		if _, err := repo.CreateKitchenStation(ctx, "Bad", bad, "addr:9100"); err == nil {
			t.Fatalf("CreateKitchenStation(%q) must error", bad)
		} else if !strings.Contains(err.Error(), "destination type") {
			t.Fatalf("CreateKitchenStation(%q) error must name the destination type, got %v", bad, err)
		}
	}
	list, _ := repo.ListKitchenStations(ctx)
	if len(list) != 3 {
		t.Fatalf("invalid types must not be inserted: want 3 stations, got %d", len(list))
	}
}

func TestUpdateKitchenStation_DestinationTypes(t *testing.T) {
	_, repo := openStationTestDB(t)
	ctx := context.Background()

	id, err := repo.CreateKitchenStation(ctx, "Grill", KitchenDestinationPrinter, "g:9100")
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.UpdateKitchenStation(ctx, id, "Grill", KitchenDestinationBoth, "g:9100"); err != nil {
		t.Fatalf("UpdateKitchenStation(both): %v", err)
	}
	got, _, _ := repo.GetKitchenStation(ctx, id)
	if got.DestinationType != KitchenDestinationBoth {
		t.Fatalf("DestinationType = %q, want both", got.DestinationType)
	}
	// A display-only station legitimately has no printer address.
	if err := repo.UpdateKitchenStation(ctx, id, "Pass", KitchenDestinationDisplay, ""); err != nil {
		t.Fatalf("UpdateKitchenStation(display, no address): %v", err)
	}
	got, _, _ = repo.GetKitchenStation(ctx, id)
	if got.DestinationType != KitchenDestinationDisplay || got.PrinterAddress != "" || got.Name != "Pass" {
		t.Fatalf("update not persisted: %+v", got)
	}
	if err := repo.UpdateKitchenStation(ctx, id, "Grill", "nope", "g:9100"); err == nil {
		t.Fatal("UpdateKitchenStation with an invalid destination type must error")
	}
	got, _, _ = repo.GetKitchenStation(ctx, id)
	if got.DestinationType != KitchenDestinationDisplay {
		t.Fatalf("rejected update must not change the row, got %+v", got)
	}
}

func TestGetKitchenStation_NotFound(t *testing.T) {
	_, repo := openStationTestDB(t)
	_, ok, err := repo.GetKitchenStation(context.Background(), "nope")
	if err != nil || ok {
		t.Fatalf("missing station: ok=%v err=%v, want ok=false err=nil", ok, err)
	}
}

func TestKitchenStation_DestinationHelpers(t *testing.T) {
	cases := []struct {
		dt            string
		prints, shows bool
	}{
		{KitchenDestinationPrinter, true, false},
		{KitchenDestinationDisplay, false, true},
		{KitchenDestinationBoth, true, true},
	}
	for _, c := range cases {
		s := KitchenStation{DestinationType: c.dt}
		if s.PrintsTickets() != c.prints || s.ShowsOnDisplay() != c.shows {
			t.Errorf("%s: PrintsTickets=%v ShowsOnDisplay=%v, want %v/%v", c.dt, s.PrintsTickets(), s.ShowsOnDisplay(), c.prints, c.shows)
		}
	}
}

// seedStationSale inserts a sale whose lines reference the given item ids.
// An empty item id seeds a variant-only line (item_id NULL, variant_id set
// — sale_lines' CHECK requires exactly one of the two): a "Large" variant
// of itm-burger, deliberately a ROUTED parent item, so the variant test
// proves the parent's routing is not consulted for variant lines.
func seedStationSale(t *testing.T, dbo *db.DB, receiptNo, saleType, status, orderStatus, createdAt string, itemIDs ...string) {
	t.Helper()
	saleID := "sale-" + receiptNo
	if _, err := dbo.DB.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, order_status, currency, subtotal, discount_total, tax_total, total, created_at)
		VALUES (?,?,?,?,?,'GBP',0,0,0,0,?)`, saleID, receiptNo, status, saleType, orderStatus, createdAt); err != nil {
		t.Fatal(err)
	}
	for i, itemID := range itemIDs {
		var item, variant any = itemID, nil
		if itemID == "" {
			if _, err := dbo.DB.Exec(`INSERT OR IGNORE INTO item_variants (id, item_id, name, price) VALUES ('var-burger-large','itm-burger','Large',1100)`); err != nil {
				t.Fatal(err)
			}
			item, variant = nil, "var-burger-large"
		}
		if _, err := dbo.DB.Exec(`INSERT INTO sale_lines (id, sale_id, line_no, item_id, variant_id, name_snapshot, quantity, unit_price, line_discount, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
			VALUES (?,?,?,?,?,'x',1,0,0,0,0,0,0)`, fmt.Sprintf("%s-l%d", saleID, i), saleID, i+1, item, variant); err != nil {
			t.Fatal(err)
		}
	}
}

func receiptNos(entries []OrderListEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.ReceiptNo)
	}
	return out
}

func TestListRecentOrdersForStation_ItemOverrideWinsOverCategory(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", KitchenDestinationDisplay, "")
	bar, _ := repo.CreateKitchenStation(ctx, "Bar", KitchenDestinationDisplay, "")
	// Category rule: Food → Bar. Item override: burger → Grill ONLY.
	if err := repo.SetCategoryStationRoutes(ctx, "cat-food", []string{bar}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetItemStationRoutes(ctx, "itm-burger", []string{grill}); err != nil {
		t.Fatal(err)
	}
	seedStationSale(t, dbo, "R-1", "sale", "completed", "new", "2026-09-05T10:00:00Z", "itm-burger")

	got, err := repo.ListRecentOrdersForStation(ctx, grill, 50)
	if err != nil {
		t.Fatalf("ListRecentOrdersForStation(grill): %v", err)
	}
	if rn := receiptNos(got); len(rn) != 1 || rn[0] != "R-1" {
		t.Fatalf("grill must list the overridden burger order, got %v", rn)
	}
	got, err = repo.ListRecentOrdersForStation(ctx, bar, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("bar must NOT list it — the item override replaces (does not union with) the category rule, got %v", receiptNos(got))
	}
}

func TestListRecentOrdersForStation_CategoryFallback(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	bar, _ := repo.CreateKitchenStation(ctx, "Bar", KitchenDestinationBoth, "b:9100")
	if err := repo.SetCategoryStationRoutes(ctx, "cat-drinks", []string{bar}); err != nil {
		t.Fatal(err)
	}
	seedStationSale(t, dbo, "R-cola", "sale", "completed", "new", "2026-09-05T10:00:00Z", "itm-cola")
	seedStationSale(t, dbo, "R-burger", "sale", "completed", "new", "2026-09-05T10:01:00Z", "itm-burger")
	seedStationSale(t, dbo, "R-mystery", "sale", "completed", "new", "2026-09-05T10:02:00Z", "itm-mystery")
	// Mixed sale: one routed line is enough for the whole ORDER to appear.
	seedStationSale(t, dbo, "R-mixed", "sale", "completed", "preparing", "2026-09-05T10:03:00Z", "itm-burger", "itm-cola")

	got, err := repo.ListRecentOrdersForStation(ctx, bar, 50)
	if err != nil {
		t.Fatal(err)
	}
	// Newest first, same ordering as ListRecentOrders.
	if rn := receiptNos(got); len(rn) != 2 || rn[0] != "R-mixed" || rn[1] != "R-cola" {
		t.Fatalf("want [R-mixed R-cola] (category fallback, newest first), got %v", rn)
	}
	if got[0].Status != "preparing" || got[0].OrderType != "" || got[0].CreatedAt == "" {
		t.Fatalf("row shape must match ListRecentOrders: %+v", got[0])
	}
}

// An item whose item-level rows all point at OTHER stations must not fall
// back to its category rule for THIS station (tier decided by row
// existence — mirrors TestResolveKitchenStations_DisabledItemOverrideDoesNotFallBackToCategory).
func TestListRecentOrdersForStation_ItemRowsClaimTierEvenWhenTheyMissThisStation(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", KitchenDestinationDisplay, "")
	bar, _ := repo.CreateKitchenStation(ctx, "Bar", KitchenDestinationDisplay, "")
	if err := repo.SetCategoryStationRoutes(ctx, "cat-food", []string{bar}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetItemStationRoutes(ctx, "itm-burger", []string{grill}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetKitchenStationEnabled(ctx, grill, false); err != nil {
		t.Fatal(err)
	}
	seedStationSale(t, dbo, "R-1", "sale", "completed", "new", "2026-09-05T10:00:00Z", "itm-burger")

	got, err := repo.ListRecentOrdersForStation(ctx, bar, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("bar must not resurrect the category rule the item override replaced, got %v", receiptNos(got))
	}
}

func TestListRecentOrdersForStation_DisabledStationListsNothing(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", KitchenDestinationDisplay, "")
	if err := repo.SetItemStationRoutes(ctx, "itm-burger", []string{grill}); err != nil {
		t.Fatal(err)
	}
	seedStationSale(t, dbo, "R-1", "sale", "completed", "new", "2026-09-05T10:00:00Z", "itm-burger")
	if err := repo.SetKitchenStationEnabled(ctx, grill, false); err != nil {
		t.Fatal(err)
	}
	got, err := repo.ListRecentOrdersForStation(ctx, grill, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("a disabled station routes nothing, got %v", receiptNos(got))
	}
	// Unknown station id: empty, not an error.
	got, err = repo.ListRecentOrdersForStation(ctx, "nope", 50)
	if err != nil || len(got) != 0 {
		t.Fatalf("unknown station: got %v err=%v", receiptNos(got), err)
	}
}

func TestListRecentOrdersForStation_ExcludesTerminalAndNonSales(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", KitchenDestinationDisplay, "")
	if err := repo.SetCategoryStationRoutes(ctx, "cat-food", []string{grill}); err != nil {
		t.Fatal(err)
	}
	seedStationSale(t, dbo, "R-open", "sale", "completed", "new", "2026-09-05T10:00:00Z", "itm-burger")
	seedStationSale(t, dbo, "R-ready", "sale", "completed", "ready", "2026-09-05T10:01:00Z", "itm-burger")
	seedStationSale(t, dbo, "R-collected", "sale", "completed", "collected", "2026-09-05T10:02:00Z", "itm-burger")
	seedStationSale(t, dbo, "R-cancelled", "sale", "completed", "cancelled", "2026-09-05T10:03:00Z", "itm-burger")
	seedStationSale(t, dbo, "R-refund", "return", "completed", "new", "2026-09-05T10:04:00Z", "itm-burger")
	seedStationSale(t, dbo, "R-voided", "sale", "voided", "new", "2026-09-05T10:05:00Z", "itm-burger")
	seedStationSale(t, dbo, "R-parked", "sale", "parked", "new", "2026-09-05T10:06:00Z", "itm-burger")

	got, err := repo.ListRecentOrdersForStation(ctx, grill, 50)
	if err != nil {
		t.Fatal(err)
	}
	if rn := receiptNos(got); len(rn) != 2 || rn[0] != "R-ready" || rn[1] != "R-open" {
		t.Fatalf("want only the two non-terminal completed sales, newest first, got %v", rn)
	}

	// limit is honoured (and <=0 means the same default as ListRecentOrders).
	got, err = repo.ListRecentOrdersForStation(ctx, grill, 1)
	if err != nil || len(got) != 1 || got[0].ReceiptNo != "R-ready" {
		t.Fatalf("limit 1: got %v err=%v", receiptNos(got), err)
	}
	got, err = repo.ListRecentOrdersForStation(ctx, grill, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("limit 0 must fall back to the default cap: got %v err=%v", receiptNos(got), err)
	}
}

// A variant-only line (item_id NULL) is not station-routable — same
// limitation buildKitchenTargets has, even though the variant's PARENT item
// (itm-burger, routed to Grill via its category here) is — so an order made
// only of such lines never appears on any station screen.
func TestListRecentOrdersForStation_VariantOnlyLinesNeverMatch(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", KitchenDestinationDisplay, "")
	if err := repo.SetCategoryStationRoutes(ctx, "cat-food", []string{grill}); err != nil {
		t.Fatal(err)
	}
	seedStationSale(t, dbo, "R-variant", "sale", "completed", "new", "2026-09-05T10:00:00Z", "")
	seedStationSale(t, dbo, "R-both", "sale", "completed", "new", "2026-09-05T10:01:00Z", "", "itm-burger")

	got, err := repo.ListRecentOrdersForStation(ctx, grill, 50)
	if err != nil {
		t.Fatal(err)
	}
	if rn := receiptNos(got); len(rn) != 1 || rn[0] != "R-both" {
		t.Fatalf("want only the order that also carries a routable line, got %v", rn)
	}
}

// The same order routed to two stations appears on BOTH, exactly once each
// — status is per order, not per line (ut-docs#544 v1 limitation,
// documented on the page).
func TestListRecentOrdersForStation_SplitOrderAppearsOnBothStations(t *testing.T) {
	dbo, repo := openStationTestDB(t)
	seedStationCatalog(t, dbo)
	ctx := context.Background()

	grill, _ := repo.CreateKitchenStation(ctx, "Grill", KitchenDestinationDisplay, "")
	bar, _ := repo.CreateKitchenStation(ctx, "Bar", KitchenDestinationDisplay, "")
	if err := repo.SetCategoryStationRoutes(ctx, "cat-food", []string{grill}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetCategoryStationRoutes(ctx, "cat-drinks", []string{bar}); err != nil {
		t.Fatal(err)
	}
	// Two routed lines for the same station must not duplicate the row.
	seedStationSale(t, dbo, "R-split", "sale", "completed", "new", "2026-09-05T10:00:00Z", "itm-burger", "itm-burger", "itm-cola")

	for _, sid := range []string{grill, bar} {
		got, err := repo.ListRecentOrdersForStation(ctx, sid, 50)
		if err != nil {
			t.Fatal(err)
		}
		if rn := receiptNos(got); len(rn) != 1 || rn[0] != "R-split" {
			t.Fatalf("station %s must list the split order exactly once, got %v", sid, rn)
		}
	}
}
