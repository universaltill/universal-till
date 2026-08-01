package data

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// InsertSale/GetSaleDetail must round-trip service_charge_amount end to end
// (ut-docs#72, "Service charge field on sales, distinct from tip"). Unlike
// tip_amount (metadata on a payment, excluded from the sale total), a
// service charge is revenue the customer owes and already participates in
// `total` -- it is stored separately only so it can be broken out on the
// receipt/journal, not recomputed from a rate that may since have changed.
func TestPOSRepo_ServiceCharge_RoundTrips(t *testing.T) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "service_charge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	// subtotal 1000, 10% service charge -> total 1100.
	if err := repo.InsertSale(ctx, nil, "sale-sc", "R-SC-1", "sale", "", "", "", "GBP",
		1000, 0, 0, 1100, 100, "", "2026-08-01T10:00:00Z", "card", false, "synced", 0, "", ""); err != nil {
		t.Fatalf("insert sale: %v", err)
	}

	detail, ok, err := repo.GetSaleDetail(ctx, "R-SC-1")
	if err != nil {
		t.Fatalf("GetSaleDetail: %v", err)
	}
	if !ok {
		t.Fatal("expected sale to be found")
	}
	if detail.ServiceCharge != 100 {
		t.Fatalf("want ServiceCharge 100, got %d", detail.ServiceCharge)
	}
	if detail.Total != 1100 {
		t.Fatalf("want Total 1100 (service charge already included), got %d", detail.Total)
	}
}

// A sale with no service charge (the common case) must default
// service_charge_amount to 0, not NULL or an error.
func TestPOSRepo_ServiceCharge_DefaultsToZero(t *testing.T) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(filepath.Join(t.TempDir(), "service_charge_zero.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()

	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	if err := repo.InsertSale(ctx, nil, "sale-nosc", "R-NOSC-1", "sale", "", "", "", "GBP",
		250, 0, 0, 250, 0, "", "2026-08-01T10:00:00Z", "cash", false, "synced", 0, "", ""); err != nil {
		t.Fatalf("insert sale: %v", err)
	}

	detail, ok, err := repo.GetSaleDetail(ctx, "R-NOSC-1")
	if err != nil {
		t.Fatalf("GetSaleDetail: %v", err)
	}
	if !ok {
		t.Fatal("expected sale to be found")
	}
	if detail.ServiceCharge != 0 {
		t.Fatalf("want ServiceCharge 0, got %d", detail.ServiceCharge)
	}
}
