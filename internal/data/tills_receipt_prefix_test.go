package data

import (
	"context"
	"testing"
)

// ut-docs#5058 UT-5: the main till records the receipt prefix it grants and
// knows every prefix in use — granted, or carried by a sale it holds.
func TestTillsRepo_ReceiptPrefixesInUse(t *testing.T) {
	d := openMigratedDB(t, "tills-prefix.db")
	ctx := context.Background()
	repo := NewTillsRepo(d.DB)
	id, err := repo.InsertTill(ctx, "Till 2", "hash-2")
	if err != nil {
		t.Fatal(err)
	}
	base := syncAdminGeneration(t, d)
	if err := repo.SetReceiptPrefix(ctx, id, "T4-"); err != nil {
		t.Fatal(err)
	}
	if syncAdminGeneration(t, d) == base {
		t.Fatal("a granted prefix did not bump the admin generation: replicas would keep a stale roster")
	}
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at) VALUES ('s1', 'T7-000000001', 'completed', 'sale', 'GBP', 1, 1, '2026-10-26T09:00:00Z')`)
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at) VALUES ('s2', '000000005', 'completed', 'sale', 'GBP', 1, 1, '2026-10-26T09:00:00Z')`)
	got, err := repo.ReceiptPrefixesInUse(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got["T4-"] || !got["T7-"] || len(got) != 2 {
		t.Fatalf("prefixes in use = %v, want T4- (granted) and T7- (a sale), not the main till's own unprefixed numbers", got)
	}
}
