package data

import (
	"context"
	"slices"
	"testing"
)

// ut-docs#3435: a replica learns of a primary's GDPR erasure only through
// the admin pull, so ApplyAdminWithResult reports which customers it pruned
// — the caller (internal/pages) publishes customer.erased for each. Both
// prune branches count (hard-delete, and the retire-in-place shell for a
// customer local sales still reference); a re-apply of the same bundle, and
// a customer the primary still has, report nothing.
func TestApplyAdminWithResult_ReportsErasedCustomers(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openMigratedDB(t, "replica.db")
	mustExec(t, primary, `INSERT INTO customers (id, name) VALUES ('c-pinned', 'Pinned Person'), ('c-free', 'Free Person'), ('c-kept', 'Kept Person')`)

	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ErasedCustomerIDs) != 0 {
		t.Fatalf("first pull erased nothing, got %v", res.ErasedCustomerIDs)
	}
	// The replica's own sale pins c-pinned (retire-in-place branch).
	mustExec(t, replica, `INSERT INTO sales (id, receipt_no, subtotal, total, customer_id) VALUES ('s1', 'R-1', 100, 100, 'c-pinned')`)

	pos := NewPOSRepo(primary.DB)
	for _, id := range []string{"c-pinned", "c-free"} {
		if ok, err := pos.EraseCustomer(ctx, id, "", ""); err != nil || !ok {
			t.Fatalf("erase %s: ok=%v err=%v", id, ok, err)
		}
	}
	bundle2, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	res, err = NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle2))
	if err != nil {
		t.Fatal(err)
	}
	got := slices.Clone(res.ErasedCustomerIDs)
	slices.Sort(got)
	if want := []string{"c-free", "c-pinned"}; !slices.Equal(got, want) {
		t.Fatalf("erased customers = %v, want %v", got, want)
	}

	res, err = NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle2))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ErasedCustomerIDs) != 0 {
		t.Fatalf("re-applying the same bundle must not report the erasure again, got %v", res.ErasedCustomerIDs)
	}
}
