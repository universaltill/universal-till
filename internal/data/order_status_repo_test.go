package data

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// Harness matches pos_repo_sale_detail_test.go: real SQLite in a TempDir,
// repo methods exercised against real migrations. Deliberately NO os.Chdir
// here (unlike that older harness): migrations are embedded so db.Open never
// reads the working directory, and a permanent chdir from this file — which
// sorts before plugin_repo_payment_guard_test.go — broke that file's
// cwd-relative migration glob (repairMigrationPath).
func openOrderStatusDB(t *testing.T, name string) *db.DB {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func seedOrderStatusSale(t *testing.T, d *db.DB, saleID, receiptNo string) {
	t.Helper()
	if _, err := d.DB.Exec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES (?,?,'completed','sale','GBP',370,0,0,370,datetime('now'))`, saleID, receiptNo); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
}

// An allowed write must update the sale's current status AND append exactly
// one journal event carrying actor + timestamp — the history is the point
// (ut-docs#526 item 3), not just a last-write-wins column.
func TestApplyOrderStatus_AppliedWriteUpdatesCurrentAndJournals(t *testing.T) {
	d := openOrderStatusDB(t, "order_status_apply.db")
	seedOrderStatusSale(t, d, "sale-1", "R-0001")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	applied, found, err := repo.ApplyOrderStatus(ctx, "R-0001", "preparing", "u-alice", "2026-08-09T10:00:00Z",
		func(current string) bool {
			if current != "" {
				t.Errorf("allowed() saw current=%q, want \"\" (untracked)", current)
			}
			return true
		})
	if err != nil {
		t.Fatalf("ApplyOrderStatus: %v", err)
	}
	if !found || !applied {
		t.Fatalf("applied=%v found=%v, want both true", applied, found)
	}

	var status, updatedAt string
	if err := d.DB.QueryRow(`SELECT order_status, COALESCE(order_status_updated_at,'') FROM sales WHERE receipt_no='R-0001'`).Scan(&status, &updatedAt); err != nil {
		t.Fatal(err)
	}
	if status != "preparing" || updatedAt != "2026-08-09T10:00:00Z" {
		t.Fatalf("sales row = (%q, %q), want (preparing, 2026-08-09T10:00:00Z)", status, updatedAt)
	}

	events, err := repo.ListOrderStatusEvents(ctx, "R-0001")
	if err != nil {
		t.Fatalf("ListOrderStatusEvents: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 journal event, got %d", len(events))
	}
	ev := events[0]
	if ev.Status != "preparing" || ev.ActorID != "u-alice" || ev.CreatedAt != "2026-08-09T10:00:00Z" {
		t.Fatalf("journal event = %+v, want preparing/u-alice/2026-08-09T10:00:00Z", ev)
	}
}

// A disallowed write is a silent no-op: found but not applied, current
// status untouched, and — critically — NO journal event appended (ut-docs#526
// item 4: only an APPLIED write journals/broadcasts).
func TestApplyOrderStatus_DisallowedWriteIsSilentNoop(t *testing.T) {
	d := openOrderStatusDB(t, "order_status_noop.db")
	seedOrderStatusSale(t, d, "sale-1", "R-0002")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	if applied, found, err := repo.ApplyOrderStatus(ctx, "R-0002", "ready", "u-alice", "2026-08-09T10:00:00Z",
		func(string) bool { return true }); err != nil || !applied || !found {
		t.Fatalf("setup write: applied=%v found=%v err=%v", applied, found, err)
	}

	// The stale write: allowed() decides against it based on the CURRENT
	// stored value, which it must be shown.
	sawCurrent := ""
	applied, found, err := repo.ApplyOrderStatus(ctx, "R-0002", "preparing", "u-bob", "2026-08-09T10:05:00Z",
		func(current string) bool {
			sawCurrent = current
			return false
		})
	if err != nil {
		t.Fatalf("ApplyOrderStatus (stale): %v", err)
	}
	if sawCurrent != "ready" {
		t.Fatalf("allowed() saw current=%q, want ready", sawCurrent)
	}
	if applied || !found {
		t.Fatalf("applied=%v found=%v, want applied=false found=true", applied, found)
	}

	var status string
	if err := d.DB.QueryRow(`SELECT order_status FROM sales WHERE receipt_no='R-0002'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "ready" {
		t.Fatalf("status regressed to %q, must stay ready", status)
	}
	events, err := repo.ListOrderStatusEvents(ctx, "R-0002")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("dropped write must not journal: want 1 event, got %d", len(events))
	}
}

func TestApplyOrderStatus_UnknownReceiptNotFound(t *testing.T) {
	d := openOrderStatusDB(t, "order_status_missing.db")
	repo := NewPOSRepo(d.DB)
	applied, found, err := repo.ApplyOrderStatus(context.Background(), "R-NOPE", "preparing", "u-alice", "2026-08-09T10:00:00Z",
		func(string) bool { return true })
	if err != nil {
		t.Fatalf("ApplyOrderStatus: %v", err)
	}
	if applied || found {
		t.Fatalf("applied=%v found=%v, want both false", applied, found)
	}
}

// LatestOrderStatus returns the newest journal event (the current state's
// who/when), resolving the actor's display name like the audit page does.
func TestLatestOrderStatus_ReturnsNewestEventWithActorName(t *testing.T) {
	d := openOrderStatusDB(t, "order_status_latest.db")
	seedOrderStatusSale(t, d, "sale-1", "R-0003")
	if _, err := d.DB.Exec(`INSERT INTO users (id, username, display_name, role, is_active) VALUES ('u-alice','alice','Alice','cashier',1)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	for i, step := range []struct{ status, at string }{
		{"new", "2026-08-09T10:00:00Z"},
		{"preparing", "2026-08-09T10:01:00Z"},
	} {
		if applied, _, err := repo.ApplyOrderStatus(ctx, "R-0003", step.status, "u-alice", step.at, func(string) bool { return true }); err != nil || !applied {
			t.Fatalf("step %d: applied=%v err=%v", i, applied, err)
		}
	}

	ev, ok, err := repo.LatestOrderStatus(ctx, "R-0003")
	if err != nil {
		t.Fatalf("LatestOrderStatus: %v", err)
	}
	if !ok {
		t.Fatal("want a latest event")
	}
	if ev.Status != "preparing" || ev.CreatedAt != "2026-08-09T10:01:00Z" || ev.ActorID != "u-alice" || ev.ActorName != "Alice" {
		t.Fatalf("latest = %+v, want preparing @10:01 by u-alice/Alice", ev)
	}

	// No events → ok=false, not an error.
	seedOrderStatusSale(t, d, "sale-2", "R-0004")
	if _, ok, err := repo.LatestOrderStatus(ctx, "R-0004"); err != nil || ok {
		t.Fatalf("untracked sale: ok=%v err=%v, want false/nil", ok, err)
	}
}

// Print-failure flags (ut-docs#517a): SetKitchenPrintFailed /
// SetReceiptPrintFailed round-trip through ListRecentOrders — set a
// timestamp and the list shows it; clear with at="" and it reads back
// empty (SQL NULL, so untouched shops stay unaffected).
func TestSetPrintFailedFlags_RoundTripAndClear(t *testing.T) {
	d := openOrderStatusDB(t, "order_status_print_failed.db")
	seedOrderStatusSale(t, d, "sale-1", "R-0020")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	find := func(receiptNo string) OrderListEntry {
		t.Helper()
		orders, err := repo.ListRecentOrders(ctx, 10)
		if err != nil {
			t.Fatalf("ListRecentOrders: %v", err)
		}
		for _, o := range orders {
			if o.ReceiptNo == receiptNo {
				return o
			}
		}
		t.Fatalf("receipt %s not in list: %+v", receiptNo, orders)
		return OrderListEntry{}
	}

	// A fresh sale carries no failure flags.
	if e := find("R-0020"); e.KitchenPrintFailedAt != "" || e.ReceiptPrintFailedAt != "" {
		t.Fatalf("fresh sale must have empty flags, got %+v", e)
	}

	if err := repo.SetKitchenPrintFailed(ctx, "R-0020", "2026-08-12T10:00:00Z"); err != nil {
		t.Fatalf("SetKitchenPrintFailed: %v", err)
	}
	if err := repo.SetReceiptPrintFailed(ctx, "R-0020", "2026-08-12T10:01:00Z"); err != nil {
		t.Fatalf("SetReceiptPrintFailed: %v", err)
	}
	if e := find("R-0020"); e.KitchenPrintFailedAt != "2026-08-12T10:00:00Z" || e.ReceiptPrintFailedAt != "2026-08-12T10:01:00Z" {
		t.Fatalf("flags not round-tripped, got %+v", e)
	}

	// at="" clears back to NULL — a later successful print must un-stick
	// the warning, not leave it forever.
	if err := repo.SetKitchenPrintFailed(ctx, "R-0020", ""); err != nil {
		t.Fatalf("clear SetKitchenPrintFailed: %v", err)
	}
	if err := repo.SetReceiptPrintFailed(ctx, "R-0020", ""); err != nil {
		t.Fatalf("clear SetReceiptPrintFailed: %v", err)
	}
	if e := find("R-0020"); e.KitchenPrintFailedAt != "" || e.ReceiptPrintFailedAt != "" {
		t.Fatalf("flags must clear to empty, got %+v", e)
	}
}

// ListRecentOrders feeds the minimal one-tap list: newest first, includes
// untracked sales (status ""), excludes returns (kitchen never sees those).
func TestListRecentOrders(t *testing.T) {
	d := openOrderStatusDB(t, "order_status_recent.db")
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := d.DB.Exec(q, args...); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-1','R-0010','completed','sale','GBP',370,0,0,370,'2026-08-09T09:00:00Z')`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, order_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-2','R-0011','completed','sale','takeaway','GBP',500,0,0,500,'2026-08-09T09:30:00Z')`)
	mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES ('sale-3','R-0012','completed','return','GBP',100,0,0,100,'2026-08-09T09:45:00Z')`)
	// Non-completed sales (voided/refunded/parked/still-open) must NOT reach
	// the kitchen list — the kitchen must never be handed a one-tap "preparing"
	// button for a transaction that isn't a real, finished order.
	for _, status := range []string{"voided", "refunded", "parked", "open"} {
		mustExec(`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at) VALUES (?,?,?,'sale','GBP',200,0,0,200,'2026-08-09T09:50:00Z')`,
			"sale-noncompleted-"+status, "R-NC-"+status, status)
	}

	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	if applied, _, err := repo.ApplyOrderStatus(ctx, "R-0011", "ready", "u-alice", "2026-08-09T09:40:00Z", func(string) bool { return true }); err != nil || !applied {
		t.Fatalf("seed status: applied=%v err=%v", applied, err)
	}

	orders, err := repo.ListRecentOrders(ctx, 10)
	if err != nil {
		t.Fatalf("ListRecentOrders: %v", err)
	}
	if len(orders) != 2 {
		t.Fatalf("want 2 orders (returns + non-completed sales excluded), got %d: %+v", len(orders), orders)
	}
	for _, o := range orders {
		if strings.HasPrefix(o.ReceiptNo, "R-NC-") {
			t.Fatalf("non-completed sale %s leaked into the kitchen order list: %+v", o.ReceiptNo, orders)
		}
	}
	if orders[0].ReceiptNo != "R-0011" || orders[1].ReceiptNo != "R-0010" {
		t.Fatalf("want newest first [R-0011 R-0010], got [%s %s]", orders[0].ReceiptNo, orders[1].ReceiptNo)
	}
	if orders[0].Status != "ready" || orders[0].StatusUpdatedAt != "2026-08-09T09:40:00Z" || orders[0].OrderType != "takeaway" {
		t.Fatalf("R-0011 = %+v, want ready @09:40 takeaway", orders[0])
	}
	if orders[1].Status != "" {
		t.Fatalf("untracked sale must list with empty status, got %q", orders[1].Status)
	}
}
