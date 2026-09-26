package data

import (
	"context"
	"reflect"
	"testing"
)

// ut-docs#2880: the generic `receipt` object a fiscal.sign.ask signer may
// return alongside "approved" (contract fiscal-sign-ask.md 1.10.0) is
// persisted per sale in fiscal_receipt_evidence (migration 048) against the
// REAL migrated schema — the payload verbatim, the lines in order.
func TestFiscalReceiptEvidence_RoundTrip(t *testing.T) {
	d := openMigratedDB(t, "fiscal-receipt-evidence")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()

	if got, ok, err := repo.GetFiscalReceiptEvidence(ctx, "sale-1"); err != nil || ok || got != nil {
		t.Fatalf("no row must be (nil,false,nil), got %+v %v %v", got, ok, err)
	}

	in := FiscalReceiptEvidence{
		SaleID:    "sale-1",
		QRPayload: "V0;ut-till-1;Kassenbeleg-V1;Beleg^0.00_1.00_0.00_0.00_0.00^1.00:Bar;7;12;2026-09-26T10:00:00.000Z;2026-09-26T10:00:01.000Z;ecdsa-plain-SHA384;unixTime;SIG==;PUB==",
		Lines:     []string{"first line", "second line"},
	}
	if err := repo.RecordFiscalReceiptEvidence(ctx, in); err != nil {
		t.Fatalf("RecordFiscalReceiptEvidence: %v", err)
	}
	got, ok, err := repo.GetFiscalReceiptEvidence(ctx, "sale-1")
	if err != nil || !ok {
		t.Fatalf("expected a row, ok=%v err=%v", ok, err)
	}
	if got.SaleID != in.SaleID || got.QRPayload != in.QRPayload || !reflect.DeepEqual(got.Lines, in.Lines) {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, in)
	}
	if got.CreatedAt == "" {
		t.Fatal("created_at must be stamped by the DB")
	}

	// First write wins: a duplicated bookkeeping call never overwrites what
	// the signer returned at tender time (reprints read this, never re-derive).
	dup := in
	dup.QRPayload = "something else"
	dup.Lines = nil
	if err := repo.RecordFiscalReceiptEvidence(ctx, dup); err != nil {
		t.Fatalf("duplicate record must not error: %v", err)
	}
	again, _, _ := repo.GetFiscalReceiptEvidence(ctx, "sale-1")
	if again.QRPayload != in.QRPayload || !reflect.DeepEqual(again.Lines, in.Lines) {
		t.Fatalf("first recorded evidence must win, got %+v", again)
	}
}

// A payload-only answer (no lines) round-trips as an empty, non-nil-safe
// slice — the render paths range over it without special-casing.
func TestFiscalReceiptEvidence_NoLines(t *testing.T) {
	d := openMigratedDB(t, "fiscal-receipt-evidence-nolines")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	if err := repo.RecordFiscalReceiptEvidence(ctx, FiscalReceiptEvidence{SaleID: "s", QRPayload: "QR"}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := repo.GetFiscalReceiptEvidence(ctx, "s")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.QRPayload != "QR" || len(got.Lines) != 0 {
		t.Fatalf("got %+v", got)
	}
}
