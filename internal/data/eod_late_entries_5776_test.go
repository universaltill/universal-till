package data

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#5776: the late-entries note also stores a per-till cleared-basket
// total (nil = not recorded) and a per-till earliest origin.

func TestLateEntries_ClearedTotalAndPerTillEarliest_5776(t *testing.T) {
	d := b8OpenDB(t, "late-5776.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	lateTill(t, d, "till-r", "Replica")
	lateTill(t, d, "till-q", "Queue")
	closeN := iwAnchor()
	next := closeN.Add(8 * time.Hour)

	qOrigin := closeN.Add(-2 * time.Hour)
	rOrigin := closeN.Add(-5 * time.Hour)
	if _, err := repo.ApplyJournaledClearedBasket(ctx, ClearedBasket{ID: "c1", CreatedAt: b8At(qOrigin), TillID: "till-q",
		LineCount: 1, Total: money.FromMinor(250), ReceivedAt: b8At(closeN.Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at, till_id, local_date)
VALUES ('lr', 'R-lr', 'completed', 'return', 'GBP', 40, 0, 0, 40, ?, ?, 'till-q', '')`, b8At(closeN.Add(-90*time.Minute)), b8At(closeN.Add(4*time.Minute)))
	lateSale(t, d, "l1", "till-r", b8At(rOrigin), b8At(closeN.Add(time.Minute)), 500)
	if _, err := repo.ApplyJournaledNoSaleEvent(ctx, NoSaleEvent{ID: "n1", CreatedAt: b8At(closeN.Add(-3 * time.Hour)), TillID: "till-r", ReceivedAt: b8At(closeN.Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}

	le, err := repo.LateEntriesForInstantWindow(ctx, closeN, next)
	if err != nil || le == nil || len(le.Tills) != 2 {
		t.Fatalf("late = %+v err=%v", le, err)
	}
	q, r := le.Tills[0], le.Tills[1] // TillID order: till-q, till-r
	if q.TillID != "till-q" || r.TillID != "till-r" {
		t.Fatalf("order = %q, %q", q.TillID, r.TillID)
	}
	if q.ClearedBasketTotal == nil || *q.ClearedBasketTotal != 250 {
		t.Fatalf("till-q cleared total = %v, want 250", q.ClearedBasketTotal)
	}
	if r.ClearedBasketTotal != nil {
		t.Fatalf("till-r has no late cleared baskets, total = %v want nil", *r.ClearedBasketTotal)
	}
	// till-q: cleared basket at -2h, refund at -90m: the min is the basket.
	if want := qOrigin.Local().Format(time.RFC3339); q.EarliestOrigin != want {
		t.Fatalf("till-q earliest = %q, want %q", q.EarliestOrigin, want)
	}
	// till-r: sale at -5h, no-sale at -3h: min is the sale.
	if want := rOrigin.Local().Format(time.RFC3339); r.EarliestOrigin != want {
		t.Fatalf("till-r earliest = %q, want %q", r.EarliestOrigin, want)
	}
	if want := rOrigin.Local().Format(time.RFC3339); le.EarliestOrigin != want {
		t.Fatalf("overall earliest = %q, want %q (unchanged: min of all)", le.EarliestOrigin, want)
	}
}

func TestLateEntries_ClearedTotalZeroIsRecorded_5776(t *testing.T) {
	d := b8OpenDB(t, "late-5776-zero.db")
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	lateTill(t, d, "till-q", "Queue")
	closeN := iwAnchor()
	if _, err := repo.ApplyJournaledClearedBasket(ctx, ClearedBasket{ID: "c0", CreatedAt: b8At(closeN.Add(-time.Hour)), TillID: "till-q",
		LineCount: 1, Total: money.FromMinor(0), ReceivedAt: b8At(closeN.Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}
	le, err := repo.LateEntriesForInstantWindow(ctx, closeN, closeN.Add(8*time.Hour))
	if err != nil || le == nil || len(le.Tills) != 1 {
		t.Fatalf("late = %+v err=%v", le, err)
	}
	if p := le.Tills[0].ClearedBasketTotal; p == nil || *p != 0 {
		t.Fatalf("a real 0 total must be recorded as non-nil 0, got %v", p)
	}
}

func TestEODLateTill_JSONOmitsNewFieldsWhenUnset_5776(t *testing.T) {
	b, err := json.Marshal(EODLateTill{TillID: "x", ClearedBasketCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(b); strings.Contains(s, "cleared_basket_total") || strings.Contains(s, "earliest_origin") {
		t.Fatalf("unset fields must be omitted, got %s", s)
	}
	z := int64(0)
	b, _ = json.Marshal(EODLateTill{TillID: "x", ClearedBasketTotal: &z, EarliestOrigin: "2026-01-01T10:00:00Z"})
	if s := string(b); !strings.Contains(s, `"cleared_basket_total":0`) || !strings.Contains(s, `"earliest_origin"`) {
		t.Fatalf("set fields must be written, got %s", s)
	}
	var old EODLateTill
	if err := json.Unmarshal([]byte(`{"till_id":"x","name":"n","cleared_basket_count":2}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.ClearedBasketTotal != nil || old.EarliestOrigin != "" {
		t.Fatalf("slice-A JSON must unmarshal to nil/empty, got %+v", old)
	}
}
