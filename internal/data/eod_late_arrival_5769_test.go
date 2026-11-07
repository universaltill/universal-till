package data

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#5769 (ADR-0066 amendment): a sale journaled from another till is
// counted in the close whose window contains the time THIS till recorded it
// (sales.completed_at), not the origin created_at. Every instant is anchored
// on the host's local noon (iwAnchor), never a fixed date.

// lateSale seeds a completed sale with an explicit origin created_at, an
// arrival completed_at ("" = NULL, a legacy/test-seed row) and a till.
func lateSale(t *testing.T, d *db.DB, id, till, createdAt, completedAt string, total int64) {
	t.Helper()
	var cat any
	tid := till // sales.till_id is NOT NULL: '' = this till's own sale
	if completedAt != "" {
		cat = completedAt
	}
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at, till_id, local_date)
VALUES (?, ?, 'completed', 'sale', 'GBP', ?, 0, 0, ?, ?, ?, ?, date(?, 'localtime'))`,
		id, "R-"+id, total, total, createdAt, cat, tid, createdAt)
	mustExec(t, d, `INSERT INTO payments(id, sale_id, method_id, amount, currency, change_given, paid_at) VALUES (?, ?, 'cash', ?, 'GBP', 0, ?)`,
		"p-"+id, id, total, createdAt)
}

func lateTill(t *testing.T, d *db.DB, id, name string) {
	t.Helper()
	mustExec(t, d, `INSERT INTO tills (id, name, bearer_hash) VALUES (?, ?, ?)`, id, name, "h-"+id)
}

func TestEODInstant_LateJournaledSaleCountsInArrivalWindow(t *testing.T) {
	d := b8OpenDB(t, "late-sale.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	lateTill(t, d, "till-r", "Replica")

	closeN := iwAnchor()
	prevClose := closeN.Add(-8 * time.Hour)
	nextClose := closeN.Add(8 * time.Hour)

	// late: made before close N, received after it.
	lateSale(t, d, "late", "till-r", b8At(closeN.Add(-10*time.Minute)), b8At(closeN.Add(5*time.Minute)), 700)
	// on time: made and received inside Z N.
	lateSale(t, d, "ontime", "till-r", b8At(closeN.Add(-30*time.Minute)), b8At(closeN.Add(-20*time.Minute)), 300)
	// own sale: completed_at == created_at.
	lateSale(t, d, "own", "", b8At(closeN.Add(-15*time.Minute)), b8At(closeN.Add(-15*time.Minute)), 100)
	// pre-migration / seed row: NULL completed_at counts by created_at.
	lateSale(t, d, "legacy", "", b8At(closeN.Add(-12*time.Minute)), "", 50)

	zN, err := repo.EndOfDayInstant(ctx, prevClose, closeN)
	if err != nil {
		t.Fatal(err)
	}
	if zN.SalesCount != 3 || zN.Gross != 300+100+50 {
		t.Fatalf("Z N = %d/%d, want 3/450 (late row must not be in it)", zN.SalesCount, zN.Gross)
	}
	zN1, err := repo.EndOfDayInstant(ctx, closeN, nextClose)
	if err != nil {
		t.Fatal(err)
	}
	if zN1.SalesCount != 1 || zN1.Gross != 700 {
		t.Fatalf("Z N+1 = %d/%d, want 1/700 (the late row)", zN1.SalesCount, zN1.Gross)
	}
	if len(zN1.Methods) != 1 || zN1.Methods[0].In != 700 {
		t.Fatalf("Z N+1 methods = %+v, want cash 700", zN1.Methods)
	}
	bands, err := repo.SalesForTaxBandsInstant(ctx, closeN, nextClose)
	if err != nil || len(bands) != 1 || bands[0].ID != "late" {
		t.Fatalf("tax band sales in Z N+1 = %+v err=%v, want [late]", bands, err)
	}
	bandsN, err := repo.SalesForTaxBandsInstant(ctx, prevClose, closeN)
	if err != nil || len(bandsN) != 3 {
		t.Fatalf("tax band sales in Z N = %d err=%v, want 3", len(bandsN), err)
	}
}

// The arrival column is compared through datetime(...): a completed_at in the
// space-separated schema-default form must window like its RFC3339 twin.
func TestEODInstant_ArrivalTextFormsNormalised(t *testing.T) {
	d := b8OpenDB(t, "late-forms.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	lateTill(t, d, "till-r", "Replica")
	closeN := iwAnchor()
	lateSale(t, d, "sp", "till-r", b8At(closeN.Add(-time.Hour)),
		closeN.Add(10*time.Minute).UTC().Format("2006-01-02 15:04:05"), 400)
	z, err := repo.EndOfDayInstant(ctx, closeN, closeN.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if z.SalesCount != 1 || z.Gross != 400 {
		t.Fatalf("Z = %d/%d, want 1/400", z.SalesCount, z.Gross)
	}
}

// Day reports and the date-range paths stay on the origin local_date (AC7).
func TestEndOfDay_LateSaleKeepsOriginLocalDate(t *testing.T) {
	d := b8OpenDB(t, "late-localdate.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	lateTill(t, d, "till-r", "Replica")
	origin := iwAnchor().Add(-48 * time.Hour)
	lateSale(t, d, "old", "till-r", b8At(origin), b8At(iwAnchor()), 900)
	day := b8ExpectedDay(t, d, origin, 0, 0)
	rep, err := repo.EndOfDay(ctx, day)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SalesCount != 1 || rep.Gross != 900 {
		t.Fatalf("origin-day report = %d/%d, want 1/900", rep.SalesCount, rep.Gross)
	}
	arrDay := b8ExpectedDay(t, d, iwAnchor(), 0, 0)
	rep, err = repo.EndOfDay(ctx, arrDay)
	if err != nil {
		t.Fatal(err)
	}
	if rep.SalesCount != 0 {
		t.Fatalf("arrival-day report counted the late row (%d); day reports use origin local_date", rep.SalesCount)
	}
}
