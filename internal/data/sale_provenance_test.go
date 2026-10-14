package data

import (
	"context"
	"testing"
)

// ADR-0155 §3 (ut-docs#4150): the cloud link's sale_detail names the sale's
// till (sales.till_id, "" = this till), its register's name and the
// cashier's display name, resolved on the till — and each is "" when this
// till has nothing to resolve it to (a replica's sale keeps no register
// here), never an id in its place.
func TestSaleProvenance(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	seedLifecycleSale(t, dbx, "own", "R1", "sale", "completed", "2026-01-01T10:00:00Z", 100, 0)
	mustExec(t, dbx.d, `UPDATE sales SET register_id = 'reg1', cashier_id = 'user1' WHERE id = 'own'`)
	got, ok, err := dbx.repo.SaleProvenance(ctx, "own")
	if err != nil || !ok {
		t.Fatalf("own sale: ok=%v err=%v", ok, err)
	}
	if got != (SaleProvenance{TillID: "", RegisterName: "Front Till", CashierName: "User One"}) {
		t.Fatalf("own sale provenance = %+v", got)
	}

	// A replica's journaled sale: till_id set, no register, no cashier.
	seedLifecycleSale(t, dbx, "replica", "T2-R1", "sale", "completed", "2026-01-01T11:00:00Z", 100, 0)
	if err := dbx.repo.SetSaleProvenance(ctx, "replica", "till-remote-1", "2026-01-01T11:00:00Z"); err != nil {
		t.Fatal(err)
	}
	got, ok, err = dbx.repo.SaleProvenance(ctx, "replica")
	if err != nil || !ok {
		t.Fatalf("replica sale: ok=%v err=%v", ok, err)
	}
	if got != (SaleProvenance{TillID: "till-remote-1"}) {
		t.Fatalf("replica sale provenance = %+v, want till id only", got)
	}

	if _, ok, err := dbx.repo.SaleProvenance(ctx, "missing"); err != nil || ok {
		t.Fatalf("missing sale: ok=%v err=%v, want not found", ok, err)
	}
}
