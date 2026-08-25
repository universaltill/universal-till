package data

import (
	"context"
	"testing"
)

// InsertSaleCharges writes rows in slice order (seq = index) and
// GetSaleDetail reads them back in that same order — the mechanism ADR-0062
// (ut-docs#963/#984) step 2/3 will build a multi-charge sale on top of.
func TestInsertSaleCharges_RoundTripsThroughGetSaleDetail(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	seedLifecycleSale(t, dbx, "sale1", "R-CHG-1", "sale", "completed", "2026-01-01T10:00:00Z", 1000, 0)

	charges := []SaleCharge{
		{Key: "service_charge", Amount: 50, TaxBasisBP: 0, Base: "net_lines"},
		{Key: "municipality_tax", Label: "Municipality Tax", Amount: 45, TaxBasisBP: 500},
		{Key: "tourism_tax", Label: "Tourism Tax", Amount: 36, TaxBasisBP: 400},
	}
	if err := dbx.repo.InsertSaleCharges(ctx, nil, "sale1", charges); err != nil {
		t.Fatalf("InsertSaleCharges: %v", err)
	}

	detail, ok, err := dbx.repo.GetSaleDetail(ctx, "R-CHG-1")
	if err != nil {
		t.Fatalf("GetSaleDetail: %v", err)
	}
	if !ok {
		t.Fatal("expected sale to be found")
	}
	if len(detail.Charges) != 3 {
		t.Fatalf("want 3 charges, got %d: %+v", len(detail.Charges), detail.Charges)
	}
	// Order must survive the round trip — it's the apportionment/display
	// order, not just a set.
	wantKeys := []string{"service_charge", "municipality_tax", "tourism_tax"}
	for i, want := range wantKeys {
		if detail.Charges[i].Key != want {
			t.Fatalf("charge %d: want key %q, got %q (order not preserved)", i, want, detail.Charges[i].Key)
		}
	}
	if detail.Charges[1].Label != "Municipality Tax" || detail.Charges[1].Amount != 45 || detail.Charges[1].TaxBasisBP != 500 {
		t.Fatalf("municipality charge fields not round-tripped: %+v", detail.Charges[1])
	}
	// Base defaults to "net_lines" when the caller leaves it empty (the
	// column's own SQL DEFAULT only fires on NULL, not Go's zero string).
	// Charges[0] (service_charge) explicitly sets Base above, so it can't
	// exercise the default -- Charges[1] (municipality_tax) is the one that
	// leaves it unset.
	if detail.Charges[1].Base != "net_lines" {
		t.Fatalf("want base to default to net_lines, got %q", detail.Charges[1].Base)
	}
}

// A sale with no sale_charges rows (every sale today, and every sale until
// ADR-0062 step 2/3 starts writing them) must read back an empty/nil
// Charges list, never an error — this is the zero-behavior-change case the
// ADR promises.
func TestGetSaleDetail_NoChargesRowsIsEmptyNotError(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	seedLifecycleSale(t, dbx, "sale1", "R-CHG-2", "sale", "completed", "2026-01-01T10:00:00Z", 1000, 0)

	detail, ok, err := dbx.repo.GetSaleDetail(ctx, "R-CHG-2")
	if err != nil {
		t.Fatalf("GetSaleDetail: %v", err)
	}
	if !ok {
		t.Fatal("expected sale to be found")
	}
	if len(detail.Charges) != 0 {
		t.Fatalf("want no charges, got %+v", detail.Charges)
	}
}

// InsertSaleCharges must be a clean no-op for an empty/nil list — the
// no-adopter case (nothing has started building a Charges list yet).
func TestInsertSaleCharges_EmptyListIsNoOp(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()

	seedLifecycleSale(t, dbx, "sale1", "R-CHG-3", "sale", "completed", "2026-01-01T10:00:00Z", 1000, 0)

	if err := dbx.repo.InsertSaleCharges(ctx, nil, "sale1", nil); err != nil {
		t.Fatalf("InsertSaleCharges(nil): %v", err)
	}
	var count int
	if err := dbx.d.DB.QueryRowContext(ctx, `SELECT count(*) FROM sale_charges WHERE sale_id = ?`, "sale1").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("want 0 sale_charges rows, got %d", count)
	}
}
