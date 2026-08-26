package data

import (
	"context"
	"testing"
)

// ut-docs#1127 / ADR-0065: the durable, queryable record of a quarantined
// LAN-sync journal entry.

func TestSyncQuarantineRepo_InsertAndList(t *testing.T) {
	d := b8OpenDB(t, "sync-quarantine.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	if err := repo.InsertJournalQuarantine(ctx, JournalQuarantineEntry{
		TillID: "till-2", SaleID: "sale-q1", ReceiptNo: "T2-Q001",
		Reason: "unknown voucher on redemption replay", PayloadJSON: `{"sale":{"id":"sale-q1"}}`,
		QuarantinedAt: "2026-08-26T10:00:00Z",
	}); err != nil {
		t.Fatalf("InsertJournalQuarantine: %v", err)
	}
	if err := repo.InsertJournalQuarantine(ctx, JournalQuarantineEntry{
		TillID: "till-3", SaleID: "sale-q2", ReceiptNo: "T3-Q001",
		Reason: "voucher id collision on issue replay", PayloadJSON: `{"sale":{"id":"sale-q2"}}`,
		QuarantinedAt: "2026-08-26T11:00:00Z",
	}); err != nil {
		t.Fatalf("InsertJournalQuarantine: %v", err)
	}

	entries, err := repo.ListJournalQuarantine(ctx, 10)
	if err != nil {
		t.Fatalf("ListJournalQuarantine: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	// Newest first.
	if entries[0].SaleID != "sale-q2" || entries[1].SaleID != "sale-q1" {
		t.Fatalf("order = %+v, want sale-q2 then sale-q1", entries)
	}
	if entries[0].Reason != "voucher id collision on issue replay" || entries[0].TillID != "till-3" {
		t.Fatalf("entries[0] = %+v", entries[0])
	}

	// A second insert for the same sale_id is a silent no-op (idempotent
	// guard, see InsertJournalQuarantine's own comment) -- the cursor
	// mechanics should make this unreachable in practice, but a duplicate
	// insert must not error or create a second row.
	if err := repo.InsertJournalQuarantine(ctx, JournalQuarantineEntry{
		TillID: "till-2", SaleID: "sale-q1", ReceiptNo: "T2-Q001",
		Reason: "reinserted, should be ignored", PayloadJSON: `{}`,
		QuarantinedAt: "2026-08-26T12:00:00Z",
	}); err != nil {
		t.Fatalf("duplicate InsertJournalQuarantine: %v", err)
	}
	entries, err = repo.ListJournalQuarantine(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries after duplicate insert = %d, want still 2", len(entries))
	}
}
