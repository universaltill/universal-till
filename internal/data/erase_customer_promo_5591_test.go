package data

import (
	"context"
	"database/sql"
	"testing"
)

// ut-docs#5591: erasing a customer must deactivate the promotions bound to
// them as it unlinks them. A NULL customer_id means "any customer", so a
// plain unlink would widen a one-customer discount to everyone.
func TestEraseCustomerDeactivatesBoundPromos(t *testing.T) {
	dbo := newBatch8DB(t, "erasepromo5591.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO customers (id, name) VALUES ('c-gone', 'Erased Customer'), ('c-keep', 'Other Customer')`)
	mustExec(t, dbo, `INSERT INTO promotions (code, type, value, customer_id, is_active) VALUES
('BOUNDACTIVE', 'amount', 700, 'c-gone', 1),
('BOUNDOFF', 'amount', 300, 'c-gone', 0),
('GLOBAL', 'amount', 250, NULL, 1),
('OTHERS', 'amount', 500, 'c-keep', 1)`)

	ok, err := repo.EraseCustomer(ctx, "c-gone", "", "")
	if err != nil || !ok {
		t.Fatalf("EraseCustomer: ok=%v err=%v", ok, err)
	}

	for _, code := range []string{"BOUNDACTIVE", "BOUNDOFF"} {
		for _, cust := range []string{"", "c-gone", "c-other"} {
			if _, _, found := repo.FindActivePromo(ctx, cust, code); found {
				t.Fatalf("%s resolved for customer %q after erasure", code, cust)
			}
			if _, promo, _, err := repo.ScanCodeKinds(ctx, code, cust); err != nil || promo {
				t.Fatalf("ScanCodeKinds(%s, %q): promo=%v err=%v", code, cust, promo, err)
			}
		}
		var cid sql.NullString
		var active int
		if err := dbo.DB.QueryRow(`SELECT customer_id, is_active FROM promotions WHERE code = ?`, code).Scan(&cid, &active); err != nil {
			t.Fatal(err)
		}
		if cid.Valid || active != 0 {
			t.Fatalf("%s: want (NULL, 0), got (%v, %d)", code, cid, active)
		}
	}

	for _, cust := range []string{"", "c-gone", "c-other"} {
		if _, _, found := repo.FindActivePromo(ctx, cust, "GLOBAL"); !found {
			t.Fatalf("global promo must still resolve for %q", cust)
		}
	}
	if _, _, found := repo.FindActivePromo(ctx, "c-keep", "OTHERS"); !found {
		t.Fatal("another customer's promo must still resolve for them")
	}
	if _, _, found := repo.FindActivePromo(ctx, "", "OTHERS"); found {
		t.Fatal("another customer's promo must stay targeted")
	}
}
