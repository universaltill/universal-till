package pos

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

// ut-docs#3368: a completed sale may only be voided — never sent back to
// open/parked or marked refunded (both drop it out of every report and Z
// total without counting as a cancellation); "completed" is reached only by
// tender; voided/refunded are terminal.

// seedStatusSale completes a real sale, then forces its status to status
// directly — the starting state each transition case needs.
func seedStatusSale(t *testing.T, db *sql.DB, status string) string {
	t.Helper()
	ctx := context.Background()
	_, _ = db.Exec(`INSERT INTO stock_locations(id,name) VALUES('loc1','Main')`)
	_, _ = db.Exec(`INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm1','SKU1','Apple', 120, 1)`)
	_, _ = db.Exec(`INSERT INTO payment_methods(id,name,type,is_active) VALUES('cash','Cash','cash',1)`)
	_, _ = db.Exec(`INSERT INTO inventory(id, item_id, variant_id, location_id, quantity, updated_at) VALUES('inv1','itm1',NULL,'loc1',5,datetime('now'))`)
	saleID, err := CompleteSale(ctx, db, SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: true,
		Lines:    []SaleLineInput{{ItemID: "itm1", SKU: "SKU1", Name: "Apple", Qty: 1, UnitPrice: 120, TaxRateBasisPoints: 2000, LocationID: "loc1"}},
		Payments: []PaymentInput{{MethodID: "cash", Amount: 120}},
	})
	if err != nil {
		t.Fatalf("complete sale: %v", err)
	}
	if _, err := db.Exec(`UPDATE sales SET status = ? WHERE id = ?`, status, saleID); err != nil {
		t.Fatalf("set status %s: %v", status, err)
	}
	if _, err := db.Exec(`DELETE FROM audit_log WHERE entity_id = ?`, saleID); err != nil {
		t.Fatalf("clear seed audit: %v", err)
	}
	return saleID
}

func saleStatusOf(t *testing.T, db *sql.DB, id string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM sales WHERE id = ?`, id).Scan(&s); err != nil {
		t.Fatalf("read status %s: %v", id, err)
	}
	return s
}

func auditCountFor(t *testing.T, db *sql.DB, id string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_id = ?`, id).Scan(&n); err != nil {
		t.Fatalf("count audit %s: %v", id, err)
	}
	return n
}

func TestUpdateSaleStatus_TransitionRules(t *testing.T) {
	cases := []struct {
		from, to string
		allowed  bool
	}{
		{"completed", "open", false},
		{"completed", "parked", false},
		{"completed", "completed", false},
		{"completed", "voided", true},
		{"completed", "refunded", false},
		{"voided", "open", false},
		{"voided", "completed", false},
		{"voided", "refunded", false},
		{"refunded", "completed", false},
		{"refunded", "open", false},
		{"refunded", "voided", false},
		{"open", "parked", true},
		{"open", "voided", true},
		{"open", "completed", false},
		{"open", "refunded", false},
		{"parked", "completed", false},
		{"parked", "open", true},
		{"parked", "voided", true},
		{"some_future_status", "open", false},
	}
	for _, tc := range cases {
		t.Run(tc.from+"->"+tc.to, func(t *testing.T) {
			ctx := context.Background()
			db := setupSaleDB(t)
			defer db.Close()
			id := seedStatusSale(t, db, tc.from)

			err := UpdateSaleStatus(ctx, db, id, tc.to, "actor1", "", "test")
			got := saleStatusOf(t, db, id)
			if tc.allowed {
				if err != nil {
					t.Fatalf("%s -> %s refused: %v", tc.from, tc.to, err)
				}
				if got != tc.to {
					t.Fatalf("status = %q, want %q", got, tc.to)
				}
				if auditCountFor(t, db, id) != 1 {
					t.Fatalf("allowed transition must write exactly one audit row")
				}
				return
			}
			if !errors.Is(err, ErrSaleStatusTransition) {
				t.Fatalf("%s -> %s: err = %v, want ErrSaleStatusTransition", tc.from, tc.to, err)
			}
			if got != tc.from {
				t.Fatalf("refused transition still changed status to %q", got)
			}
			if n := auditCountFor(t, db, id); n != 0 {
				t.Fatalf("refused transition left %d audit row(s), want 0", n)
			}
		})
	}
}

func TestUpdateSaleStatus_ElevatedRecordsBlockedActor(t *testing.T) {
	ctx := context.Background()
	db := setupSaleDB(t)
	defer db.Close()
	id := seedStatusSale(t, db, "completed")

	if err := UpdateSaleStatus(ctx, db, id, "voided", "mgr1", "cashier1", "wrong item"); err != nil {
		t.Fatalf("elevated void: %v", err)
	}
	var actor, blocked string
	if err := db.QueryRow(`SELECT COALESCE(actor_id,''), COALESCE(blocked_actor_id,'') FROM audit_log WHERE entity_id = ?`, id).
		Scan(&actor, &blocked); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if actor != "mgr1" || blocked != "cashier1" {
		t.Fatalf("audit actor/blocked = %q/%q, want mgr1/cashier1", actor, blocked)
	}
}
