package data

import (
	"context"
	"testing"
)

// ut-docs#5590: after the 097 repair a former orphan promo is (NULL, inactive).
// It must not resolve for anyone, while global and targeted promos still do.
func TestRepairedDanglingPromoStaysInertAtCheckout(t *testing.T) {
	dbo := newBatch8DB(t, "promo5590.db")
	ctx := context.Background()
	repo := NewPOSRepo(dbo.DB)

	mustExec(t, dbo, `INSERT INTO customers (id, name) VALUES ('c-real', 'Real Customer')`)
	mustExec(t, dbo, `INSERT INTO promotions (code, type, value, customer_id, is_active) VALUES
('REPAIRED', 'amount', 700, NULL, 0),
('GLOBAL', 'amount', 250, NULL, 1),
('TARGETED', 'amount', 500, 'c-real', 1)`)

	for _, cust := range []string{"", "c-real", "c-other"} {
		if _, _, ok := repo.FindActivePromo(ctx, cust, "REPAIRED"); ok {
			t.Fatalf("repaired promo resolved for customer %q", cust)
		}
		if _, promo, _, err := repo.ScanCodeKinds(ctx, "REPAIRED", cust); err != nil || promo {
			t.Fatalf("ScanCodeKinds(REPAIRED, %q): promo=%v err=%v", cust, promo, err)
		}
		if _, _, ok := repo.FindActivePromo(ctx, cust, "GLOBAL"); !ok {
			t.Fatalf("global promo must resolve for %q", cust)
		}
	}
	if _, _, ok := repo.FindActivePromo(ctx, "c-real", "TARGETED"); !ok {
		t.Fatal("targeted promo must resolve for its customer")
	}
	if _, _, ok := repo.FindActivePromo(ctx, "", "TARGETED"); ok {
		t.Fatal("targeted promo must not resolve for no customer")
	}
}

// Why 097 deactivates instead of only unlinking: an ACTIVE promo with a NULL
// customer is "any customer", i.e. the one-customer discount would widen.
func TestUnlinkedButActivePromoWidensToEveryone(t *testing.T) {
	dbo := newBatch8DB(t, "promo5590w.db")
	mustExec(t, dbo, `INSERT INTO promotions (code, type, value, customer_id, is_active) VALUES ('WIDE', 'amount', 700, NULL, 1)`)
	if _, _, ok := NewPOSRepo(dbo.DB).FindActivePromo(context.Background(), "", "WIDE"); !ok {
		t.Fatal("premise broken: active NULL-customer promo should resolve for everyone")
	}
}
