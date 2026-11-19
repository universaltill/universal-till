package data

// ut-docs#5574: the promotions admin list resolves the bound customer's name
// (no UUID in the UI), and an edit can rebind or clear the customer.

import (
	"context"
	"errors"
	"testing"
)

func seedPromoCustomers(t *testing.T, repo *POSRepo) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`INSERT INTO customers (id, name) VALUES ('c-live', 'Live Person'), ('c-other', 'Other Person'), ('c-shell', '')`,
	} {
		if _, err := repo.db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed customers: %v", err)
		}
	}
}

// seedDanglingPromo inserts a promo whose customer_id matches no customer row,
// the legacy state from when the FK pragma was silently ignored. FKs are
// switched off on ONE connection and restored on that same connection before
// it returns to the pool.
func seedDanglingPromo(t *testing.T, repo *POSRepo, code, custID string) {
	t.Helper()
	ctx := context.Background()
	conn, err := repo.db.Conn(ctx)
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatalf("fk off: %v", err)
	}
	_, err = conn.ExecContext(ctx, `INSERT INTO promotions (code, type, value, customer_id, is_active) VALUES (?, 'amount', 100, ?, 1)`, code, custID)
	if _, e2 := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); e2 != nil {
		t.Fatalf("fk on: %v", e2)
	}
	if err != nil {
		t.Fatalf("seed dangling promo: %v", err)
	}
}

func promoByCode(t *testing.T, repo *POSRepo, code string) PromotionAdmin {
	t.Helper()
	list, err := repo.ListPromotionsForAdmin(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, p := range list {
		if p.Code == code {
			return p
		}
	}
	t.Fatalf("promo %s not listed", code)
	return PromotionAdmin{}
}

func TestListPromotionsForAdmin_CustomerName(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()
	seedPromoCustomers(t, repo)
	for code, cust := range map[string]string{"P-NONE": "", "P-LIVE": "c-live"} {
		if err := repo.CreatePromotion(ctx, code, PromotionInput{Type: "amount", Value: 100, CustomerID: cust}); err != nil {
			t.Fatalf("create %s: %v", code, err)
		}
	}
	// A shell is not a valid create target, so bind it directly.
	if _, err := repo.db.ExecContext(ctx, `INSERT INTO promotions (code, type, value, customer_id, is_active) VALUES ('P-SHELL','amount',100,'c-shell',1)`); err != nil {
		t.Fatalf("seed shell promo: %v", err)
	}
	seedDanglingPromo(t, repo, "P-GONE", "c-gone")

	cases := []struct {
		code, name string
		missing    bool
	}{
		{"P-NONE", "", false},
		{"P-LIVE", "Live Person", false},
		{"P-SHELL", "", true},
		{"P-GONE", "", true},
	}
	for _, c := range cases {
		p := promoByCode(t, repo, c.code)
		if p.CustomerName != c.name || p.CustomerMissing != c.missing {
			t.Errorf("%s: name=%q missing=%v, want %q %v", c.code, p.CustomerName, p.CustomerMissing, c.name, c.missing)
		}
	}
}

func TestUpdatePromotion_KeepsDanglingCustomerOnUnchangedEdit(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()
	seedDanglingPromo(t, repo, "P-GONE", "c-gone")

	// Same id re-sent: SQLite would FK-check customer_id if it were in SET.
	if _, err := repo.UpdatePromotion(ctx, "P-GONE", PromotionInput{Type: "amount", Value: 200, Description: "edited", CustomerID: "c-gone"}); err != nil {
		t.Fatalf("unchanged dangling edit: %v", err)
	}
	p := promoByCode(t, repo, "P-GONE")
	if p.Value != 200 || p.Description != "edited" || p.CustomerID != "c-gone" || !p.CustomerMissing {
		t.Fatalf("after edit: %+v", p)
	}
	// KeepCustomer variant ignores CustomerID entirely.
	if _, err := repo.UpdatePromotion(ctx, "P-GONE", PromotionInput{Type: "amount", Value: 300, KeepCustomer: true, CustomerID: "ignored"}); err != nil {
		t.Fatalf("keep edit: %v", err)
	}
	if p := promoByCode(t, repo, "P-GONE"); p.Value != 300 || p.CustomerID != "c-gone" {
		t.Fatalf("after keep: %+v", p)
	}
}

func TestUpdatePromotion_RebindsAndClears(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()
	seedPromoCustomers(t, repo)
	if err := repo.CreatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100, CustomerID: "c-live"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpdatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100, CustomerID: "c-other"}); err != nil {
		t.Fatalf("rebind: %v", err)
	}
	if p := promoByCode(t, repo, "P1"); p.CustomerID != "c-other" || p.CustomerName != "Other Person" {
		t.Fatalf("rebind: %+v", p)
	}
	if _, err := repo.UpdatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100, CustomerID: ""}); err != nil {
		t.Fatalf("clear: %v", err)
	}
	var isNull bool
	if err := repo.db.QueryRowContext(ctx, `SELECT customer_id IS NULL FROM promotions WHERE code='P1'`).Scan(&isNull); err != nil || !isNull {
		t.Fatalf("customer_id not NULL: null=%v err=%v", isNull, err)
	}
}

func TestUpdatePromotion_RejectsUnknownAndShellCustomer(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()
	seedPromoCustomers(t, repo)
	if err := repo.CreatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100, Description: "orig", CustomerID: "c-live"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"nope", "c-shell"} {
		_, err := repo.UpdatePromotion(ctx, "P1", PromotionInput{Type: "percent", Value: 500, Description: "changed", CustomerID: id})
		if !errors.Is(err, ErrPromotionCustomerUnknown) {
			t.Fatalf("%s: err=%v, want ErrPromotionCustomerUnknown", id, err)
		}
		p := promoByCode(t, repo, "P1")
		if p.Description != "orig" || p.Type != "amount" || p.CustomerID != "c-live" {
			t.Fatalf("%s: row changed: %+v", id, p)
		}
	}
	if _, err := repo.UpdatePromotion(ctx, "MISSING", PromotionInput{Type: "amount", Value: 1}); !errors.Is(err, ErrPromotionNotFound) {
		t.Fatalf("missing code: %v", err)
	}
}

func TestCreatePromotion_RejectsUnknownCustomer(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()
	seedPromoCustomers(t, repo)
	for _, id := range []string{"nope", "c-shell"} {
		if err := repo.CreatePromotion(ctx, "PX", PromotionInput{Type: "amount", Value: 100, CustomerID: id}); !errors.Is(err, ErrPromotionCustomerUnknown) {
			t.Fatalf("%s: err=%v", id, err)
		}
	}
	var n int
	if err := repo.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM promotions WHERE code='PX'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("row inserted: n=%d err=%v", n, err)
	}
}

func TestEraseCustomer_PromotionShowsAnyNotDeleted(t *testing.T) {
	_, repo := openPromoTestDB(t)
	ctx := context.Background()
	seedPromoCustomers(t, repo)
	if err := repo.CreatePromotion(ctx, "P1", PromotionInput{Type: "amount", Value: 100, CustomerID: "c-live"}); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.EraseCustomer(ctx, "c-live", "", ""); err != nil || !ok {
		t.Fatalf("erase: ok=%v err=%v", ok, err)
	}
	if p := promoByCode(t, repo, "P1"); p.CustomerID != "" || p.CustomerMissing {
		t.Fatalf("after erase: %+v", p)
	}
}
