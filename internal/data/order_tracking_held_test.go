package data

import (
	"context"
	"testing"
	"time"
)

// ut-docs#4206: a kiosk pay-at-counter order is a HELD SALE until the
// cashier tenders it, so its tracking token lives in the held payload
// (pos.BasketSnapshot.tracking_token) first and moves to sales.tracking_token
// once the order is paid.

func TestNewOrderTrackingToken_IsRandomHex(t *testing.T) {
	a, err := NewOrderTrackingToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewOrderTrackingToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 32 || a == b {
		t.Fatalf("want two distinct 32-char tokens, got %q %q", a, b)
	}
}

// The paid sale adopts the token its held order carried; a sale that
// already has a token is never overwritten, and a token another sale holds
// is refused (UNIQUE) as an error the caller can log.
func TestAdoptOrderTrackingToken(t *testing.T) {
	d := openOrderStatusDB(t, "tracking_token_adopt.db")
	seedOrderStatusSale(t, d, "sale-1", "R-0001")
	seedOrderStatusSale(t, d, "sale-2", "R-0002")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	adopted, err := repo.AdoptOrderTrackingToken(ctx, "R-0001", "0123456789abcdef0123456789abcdef")
	if err != nil || !adopted {
		t.Fatalf("adopt: adopted=%v err=%v", adopted, err)
	}
	o, found, err := repo.LookupOrderByTrackingToken(ctx, "0123456789abcdef0123456789abcdef")
	if err != nil || !found || o.ReceiptNo != "R-0001" {
		t.Fatalf("lookup after adopt: %+v found=%v err=%v", o, found, err)
	}

	// Guarded: an already-tokened sale keeps its token.
	adopted, err = repo.AdoptOrderTrackingToken(ctx, "R-0001", "ffffffffffffffffffffffffffffffff")
	if err != nil || adopted {
		t.Fatalf("re-adopt must be a no-op: adopted=%v err=%v", adopted, err)
	}
	if tok, _ := repo.EnsureOrderTrackingToken(ctx, "R-0001"); tok != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("existing token overwritten: %q", tok)
	}

	// Collision with another sale's token: error, nothing written.
	if _, err := repo.AdoptOrderTrackingToken(ctx, "R-0002", "0123456789abcdef0123456789abcdef"); err == nil {
		t.Fatal("adopting a token another sale holds must fail")
	}
	var n int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales WHERE receipt_no='R-0002' AND tracking_token IS NOT NULL`).Scan(&n)
	if n != 0 {
		t.Fatal("a refused adopt must leave the sale untokened")
	}

	// Empty token / unknown receipt: no-op, no error.
	if adopted, err := repo.AdoptOrderTrackingToken(ctx, "R-0002", ""); err != nil || adopted {
		t.Fatalf("empty token: adopted=%v err=%v", adopted, err)
	}
	if adopted, err := repo.AdoptOrderTrackingToken(ctx, "R-9999", "abababababababababababababababab"); err != nil || adopted {
		t.Fatalf("unknown receipt: adopted=%v err=%v", adopted, err)
	}
}

// A held order whose payload carries the token resolves to its customer
// order number; unknown tokens, empty tokens and held rows with an
// unparseable payload never match (and never error).
func TestLookupHeldOrderByTrackingToken(t *testing.T) {
	d := openOrderStatusDB(t, "tracking_token_held.db")
	ctx := context.Background()
	held := NewHeldSalesRepo(d.DB)
	for _, h := range []HeldSale{
		{ID: "hold-1", Label: "C-7 · Takeaway", Payload: `{"lines":[],"total":0,"display_no":"C-7","tracking_token":"0123456789abcdef0123456789abcdef"}`},
		{ID: "hold-2", Label: "Table 4", Payload: `{"lines":[],"total":0}`},
		{ID: "hold-3", Label: "broken", Payload: `not json`},
	} {
		if err := held.Upsert(ctx, h); err != nil {
			t.Fatalf("seed %s: %v", h.ID, err)
		}
	}
	repo := NewPOSRepo(d.DB)

	o, found, err := repo.LookupHeldOrderByTrackingToken(ctx, "0123456789abcdef0123456789abcdef", time.Now().Add(-time.Hour))
	if err != nil || !found {
		t.Fatalf("lookup: found=%v err=%v", found, err)
	}
	if o.DisplayNo != "C-7" {
		t.Fatalf("display no = %q, want C-7", o.DisplayNo)
	}
	if o.ReceiptNo == "hold-1" {
		t.Fatal("the held row's internal id must not be exposed as the receipt number")
	}
	// A row last written before notBefore is no answer: a replica kiosk's
	// mirror of an order the primary already claimed must not say
	// "waiting for payment" forever (ut-docs#4206 review).
	if _, found, err := repo.LookupHeldOrderByTrackingToken(ctx, "0123456789abcdef0123456789abcdef", time.Now().Add(time.Minute)); err != nil || found {
		t.Fatalf("stale held row must not resolve: found=%v err=%v", found, err)
	}
	if _, err := d.DB.Exec(`UPDATE held_sales SET updated_at = '2026-01-01 00:00:00' WHERE id = 'hold-1'`); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repo.LookupHeldOrderByTrackingToken(ctx, "0123456789abcdef0123456789abcdef", time.Now().Add(-2*time.Hour)); err != nil || found {
		t.Fatalf("held row older than the bound must not resolve: found=%v err=%v", found, err)
	}
	for _, tok := range []string{"", "ffffffffffffffffffffffffffffffff", "C-7"} {
		if _, found, err := repo.LookupHeldOrderByTrackingToken(ctx, tok, time.Now().Add(-time.Hour)); err != nil || found {
			t.Fatalf("token %q: found=%v err=%v", tok, found, err)
		}
	}
}
