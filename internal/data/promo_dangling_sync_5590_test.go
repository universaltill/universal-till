package data

import (
	"context"
	"testing"
)

// ut-docs#5590 (review H1): a satellite already on 097 has repaired an orphan
// promo to (NULL, inactive) and no longer holds the customer. An older main
// till still sends customer_id='c-gone'; applying that raw would fail the whole
// admin apply with FOREIGN KEY 787. The apply clamps it to (NULL, inactive).
func TestApplyAdmin_OrphanPromoCustomerFromOldMainIsClamped(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openReplicaDB(t, "replica.db")
	mustExec(t, primary, `INSERT INTO customers (id, name) VALUES ('c-gone', 'Gone Person')`)
	mustExec(t, primary, `INSERT INTO promotions (code, type, value, customer_id, is_active) VALUES ('ORPH', 'amount', 700, 'c-gone', 1)`)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The old main till's bundle: promo still names c-gone, customer is gone.
	custs := bundle.Tables["customers"][:0]
	bundle.Tables["customers"] = custs
	// Replica: repaired row already present.
	mustExec(t, replica, `INSERT INTO promotions (code, type, value, customer_id, is_active) VALUES ('ORPH', 'amount', 700, NULL, 0)`)

	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatalf("apply must not fail on an orphan promo customer: %v", err)
	}
	var cust *string
	var active int
	if err := replica.DB.QueryRow(`SELECT customer_id, is_active FROM promotions WHERE code='ORPH'`).Scan(&cust, &active); err != nil {
		t.Fatal(err)
	}
	if cust != nil || active != 0 {
		t.Fatalf("orphan promo = (%v, %d), want (NULL, 0): never widened", cust, active)
	}
}

// A customer that exists locally, or arrives in the same bundle, is not clamped.
func TestApplyAdmin_PromoWithValidCustomerNotClamped(t *testing.T) {
	ctx := context.Background()
	primary := openMigratedDB(t, "primary.db")
	replica := openReplicaDB(t, "replica.db")
	mustExec(t, primary, `INSERT INTO customers (id, name) VALUES ('c-new', 'New Person')`)
	mustExec(t, primary, `INSERT INTO promotions (code, type, value, customer_id, is_active) VALUES ('NEWP', 'amount', 500, 'c-new', 1)`)
	bundle, err := NewSyncAdminRepo(primary.DB).DumpAdmin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// c-new arrives in the same bundle; replica does not hold it yet.
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatal(err)
	}
	// Second apply: customer now present locally and not in a changed state.
	if _, err := NewSyncAdminRepo(replica.DB).ApplyAdminWithResult(ctx, wireTrip(t, bundle)); err != nil {
		t.Fatal(err)
	}
	var cust *string
	var active int
	if err := replica.DB.QueryRow(`SELECT customer_id, is_active FROM promotions WHERE code='NEWP'`).Scan(&cust, &active); err != nil {
		t.Fatal(err)
	}
	if cust == nil || *cust != "c-new" || active != 1 {
		t.Fatalf("valid promo = (%v, %d), want (c-new, 1)", cust, active)
	}
}
