package pages

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#1122: New sale on a live, non-resumed basket with lines records
// one cleared_baskets row (line count + basket total at the moment of
// clearing); an empty basket and a resumed order's re-park record nothing.

type clearedBasketRow struct {
	actor, localDate string
	lines            int
	total            int64
	tillNull         bool
}

func clearedBasketRows(t *testing.T, db *sql.DB) []clearedBasketRow {
	t.Helper()
	rows, err := db.Query(`SELECT COALESCE(actor_id,''), local_date, line_count, total_minor, till_id IS NULL FROM cleared_baskets`)
	if err != nil {
		t.Fatalf("query cleared_baskets: %v", err)
	}
	defer rows.Close()
	var out []clearedBasketRow
	for rows.Next() {
		var r clearedBasketRow
		if err := rows.Scan(&r.actor, &r.localDate, &r.lines, &r.total, &r.tillNull); err != nil {
			t.Fatalf("scan: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate: %v", err)
	}
	return out
}

func TestResetHandler_ClearedBasketIsRecorded(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	for _, code := range []string{"PLAIN", "PLAIN", "ABC"} {
		if _, err := dp.Engine.Scan(code); err != nil {
			t.Fatalf("seed scan %s: %v", code, err)
		}
	}
	snap := dp.Engine.Snapshot()
	wantLines, wantTotal := len(snap.Lines), snap.Total.Minor()
	if wantLines == 0 || wantTotal == 0 {
		t.Fatalf("fixture basket is empty: %+v", snap)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/pos/reset", nil), auth.User{ID: "cashier-1", Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: %d %s", rec.Code, rec.Body.String())
	}
	if dp.Engine.HasItemsOrByHand() {
		t.Fatal("New sale must still clear the basket")
	}
	got := clearedBasketRows(t, dp.Db)
	if len(got) != 1 {
		t.Fatalf("want exactly one cleared_baskets row, got %d", len(got))
	}
	r := got[0]
	if r.actor != "cashier-1" || r.lines != wantLines || r.total != wantTotal || !r.tillNull || r.localDate == "" {
		t.Fatalf("row = %+v, want actor cashier-1, %d lines, total %d, till NULL, a local_date", r, wantLines, wantTotal)
	}
}

// A fresh basket holding only "add by hand" lines is still a real basket.
func TestResetHandler_ByHandOnlyClearedBasketIsRecorded(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	dp.Engine.RestoreHeld(pos.BasketSnapshot{AddByHand: []pos.ByHandLine{{Name: "Latte", Qty: 2}}}, pos.HeldOrigin{})
	if !dp.Engine.HasItemsOrByHand() || !dp.Engine.HeldOrigin().IsZero() {
		t.Fatal("fixture: want a non-resumed by-hand-only basket")
	}
	resetDiscardPost(t, mux)
	got := clearedBasketRows(t, dp.Db)
	if len(got) != 1 || got[0].lines != 1 || got[0].total != 0 {
		t.Fatalf("by-hand-only basket rows = %+v, want one row with 1 line, total 0", got)
	}
}

func TestResetHandler_EmptyBasketRecordsNoClearedBasket(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	resetDiscardPost(t, mux)
	if got := clearedBasketRows(t, dp.Db); len(got) != 0 {
		t.Fatalf("an empty basket must record nothing, got %d rows", len(got))
	}
}

func TestResetHandler_ResumedOrderReParkRecordsNoClearedBasket(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	snap := pos.BasketSnapshot{Lines: []pos.SnapshotLine{{SKU: "PLAIN", Name: "Plain Item", Qty: 2, PriceCents: 200, ItemID: "itm-plain2", TaxRateBP: 2000}}}
	dp.Engine.RestoreHeld(snap, pos.HeldOrigin{ID: "hold-1122", Label: "Table 9", CreatedAt: "2026-10-03 10:00:00"})
	resetDiscardPost(t, mux)
	if got := clearedBasketRows(t, dp.Db); len(got) != 0 {
		t.Fatalf("a re-parked order was not cleared: want no row, got %d", len(got))
	}
}

// Offline-first: a failed insert never blocks New sale.
func TestResetHandler_ClearedBasketInsertFailureStillResets(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("PLAIN"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	if _, err := dp.Db.Exec(`DROP TABLE cleared_baskets`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	resetDiscardPost(t, mux)
	if dp.Engine.HasItemsOrByHand() {
		t.Fatal("a failed cleared-basket insert must not stop the reset")
	}
}
