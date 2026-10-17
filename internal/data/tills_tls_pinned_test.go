package data

import (
	"context"
	"testing"
)

// ut-docs#2737 (ADR-0114 §7, Enforcement): the main till records, per till,
// that the till has authenticated over pinned TLS, so it can refuse that
// till's bearer on plain HTTP from then on. The record survives a restart
// (a table, not memory), is idempotent, and goes with the till's row when
// the till is removed (a re-join is a new row and starts unpinned).
func TestTillsRepo_TLSPinnedIsRecordedPerTillAndGoesWithTheTill(t *testing.T) {
	d := openMigratedDB(t, "tills-tls-pinned.db")
	repo := NewTillsRepo(d.DB)
	ctx := context.Background()
	a, err := repo.InsertTill(ctx, "Till 2", "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InsertTill(ctx, "Till 3", "hash-b"); err != nil {
		t.Fatal(err)
	}
	pinned := func(hash string) bool {
		t.Helper()
		row, ok, err := repo.TillByBearerHash(ctx, hash)
		if err != nil || !ok {
			t.Fatalf("TillByBearerHash(%s) = %v, %v", hash, ok, err)
		}
		return row.TLSPinned
	}
	if pinned("hash-a") || pinned("hash-b") {
		t.Fatal("a newly enrolled till must start unpinned")
	}
	for range 2 { // idempotent: every TLS request of a pinned till may call it
		if err := repo.MarkTLSPinned(ctx, a); err != nil {
			t.Fatalf("MarkTLSPinned: %v", err)
		}
	}
	if !pinned("hash-a") {
		t.Fatal("till a: want pinned after MarkTLSPinned")
	}
	if pinned("hash-b") {
		t.Fatal("till b: pinning one till must not pin another")
	}

	if err := repo.DeleteTill(ctx, a); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM till_tls_pinned`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("till_tls_pinned rows after removing the till = %d, want 0", n)
	}
	if err := repo.MarkTLSPinned(ctx, "no-such-till"); err == nil {
		t.Fatal("MarkTLSPinned for an unknown till must fail (FK), not leave an orphan row")
	}
}

// ut-docs#4411 (ADR-0114 §7): plain-HTTP peer sync closes once every paired
// till has pinned. AllTillsTLSPinned is that fleet condition: true only with
// at least one enrolled till and none unpinned — zero tills is no evidence
// the fleet speaks TLS, so it stays false (open).
func TestTillsRepo_AllTillsTLSPinned(t *testing.T) {
	d := openMigratedDB(t, "tills-all-pinned.db")
	repo := NewTillsRepo(d.DB)
	ctx := context.Background()
	all := func() bool {
		t.Helper()
		ok, err := repo.AllTillsTLSPinned(ctx)
		if err != nil {
			t.Fatalf("AllTillsTLSPinned: %v", err)
		}
		return ok
	}
	if all() {
		t.Fatal("zero tills: want false (nothing proves the fleet speaks TLS)")
	}
	a, err := repo.InsertTill(ctx, "Till 2", "hash-a")
	if err != nil {
		t.Fatal(err)
	}
	if all() {
		t.Fatal("one unpinned till: want false")
	}
	if err := repo.MarkTLSPinned(ctx, a); err != nil {
		t.Fatal(err)
	}
	if !all() {
		t.Fatal("every till pinned: want true")
	}
	b, err := repo.InsertTill(ctx, "Till 3", "hash-b")
	if err != nil {
		t.Fatal(err)
	}
	if all() {
		t.Fatal("one pinned, one unpinned: want false")
	}
	if err := repo.MarkTLSPinned(ctx, b); err != nil {
		t.Fatal(err)
	}
	if !all() {
		t.Fatal("both pinned: want true")
	}
}
