package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#3423: a held/table/counter order resumed into the live basket has
// already left held_sales (resumeHeldSale deletes or claims its row), so a
// New Sale tap on it used to drop the order with no trace at all -- no held
// row, no sale, no audit entry. #3423 audited that drop; ut-docs#3582 then
// replaced the drop itself: with an explicit Cancel order available, New
// Sale on a resumed order with items puts it back on hold instead.

func resetDiscardPost(t *testing.T, mux *http.ServeMux) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/reset", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	return rec
}

func resetDiscardAuditRows(t *testing.T, db *sql.DB) []struct{ entityID, payload string } {
	t.Helper()
	rows, err := db.Query(`SELECT entity_id, data_json FROM audit_log WHERE entity_type = 'held_sale' AND action = 'discard'`)
	if err != nil {
		t.Fatalf("query audit_log: %v", err)
	}
	defer rows.Close()
	var out []struct{ entityID, payload string }
	for rows.Next() {
		var r struct{ entityID, payload string }
		if err := rows.Scan(&r.entityID, &r.payload); err != nil {
			t.Fatalf("scan audit_log: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate audit_log: %v", err)
	}
	return out
}

// ut-docs#3582 (issue bullet 6): once an explicit Cancel order exists, New
// Sale on a resumed order that still has items RE-PARKS it under its own
// identity (the same path a manual Hold and resumeHeldSale's #1919 auto-park
// take) instead of discarding it -- an accidental tap never loses an order.
func TestResetHandler_ResumedOrderWithItemsIsReParked(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
	// What resumeHeldSale leaves behind: the order's lines restored into the
	// live basket with its held-sale identity, its held_sales row gone.
	snap := pos.BasketSnapshot{Lines: []pos.SnapshotLine{{SKU: "PLAIN", Name: "Plain Item", Qty: 2, PriceCents: 200, ItemID: "itm-plain2", TaxRateBP: 2000}}}
	dp.Engine.RestoreHeld(snap, pos.HeldOrigin{ID: "hold-42", Label: "Table 4", CreatedAt: "2026-10-03 10:00:00"})
	if !dp.Engine.HasItems() {
		t.Fatalf("expected the restored order to have items before reset")
	}
	wantTotal := dp.Engine.Snapshot().Total.Minor()

	rec := resetDiscardPost(t, mux)

	if dp.Engine.HasItems() || !dp.Engine.HeldOrigin().IsZero() {
		t.Fatalf("New Sale must still leave an empty basket with no held origin")
	}
	held, found, err := data.NewHeldSalesRepo(dp.Db).Get(context.Background(), "hold-42")
	if err != nil || !found {
		t.Fatalf("the resumed order must be parked again under its own id, found=%v err=%v", found, err)
	}
	if held.Label != "Table 4" || held.TotalMinor != wantTotal || held.LineCount != 1 {
		t.Fatalf("re-parked row = %+v, want label Table 4, total %d, 1 line", held, wantTotal)
	}
	if got := resetDiscardAuditRows(t, dp.Db); len(got) != 0 {
		t.Fatalf("a re-parked order was not discarded: no discard audit row expected, got %d", len(got))
	}
	if !strings.Contains(rec.Header().Get("HX-Trigger"), "held-changed") {
		t.Fatalf("re-park must fire held-changed for the Open orders badge, got %q", rec.Header().Get("HX-Trigger"))
	}
	if !strings.Contains(rec.Body.String(), fmt.Sprintf(httpx.T("en", "hold.toast.reparked"), "Table 4")) {
		t.Fatalf("the cashier must be told the order went back on hold: %s", rec.Body.String())
	}
}

// A re-parked table order keeps its table: the claim is the parked order's
// occupancy signal (ut-docs#1704), so New Sale must not release it.
func TestResetHandler_ResumedTableOrderKeepsItsTableClaim(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO tables (id, label, created_at, updated_at) VALUES ('tbl-rp','Table 4','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed table: %v", err)
	}
	if ok, err := data.NewPOSRepo(dp.Db).ClaimTable(context.Background(), "tbl-rp"); err != nil || !ok {
		t.Fatalf("seed claim: %v %v", ok, err)
	}
	snap := pos.BasketSnapshot{Lines: []pos.SnapshotLine{{SKU: "PLAIN", Name: "Plain Item", Qty: 1, PriceCents: 200, ItemID: "itm-plain2", TaxRateBP: 2000}}, TableID: "tbl-rp", TableLabel: "Table 4"}
	dp.Engine.RestoreHeld(snap, pos.HeldOrigin{ID: "hold-43", Label: "Table 4", CreatedAt: "2026-10-03 10:00:00"})

	resetDiscardPost(t, mux)

	held, found, err := data.NewHeldSalesRepo(dp.Db).Get(context.Background(), "hold-43")
	if err != nil || !found || held.TableID != "tbl-rp" {
		t.Fatalf("re-parked table order must keep its table, got %+v found=%v err=%v", held, found, err)
	}
	if !holdTestTableClaimed(t, dp, "tbl-rp") {
		t.Fatal("the re-parked order's table claim must be kept, not released")
	}
}

// Regression (ut-docs#3423 behaviour kept for the empty case): a resumed
// order with nothing in it has nothing to lose -- New Sale still just
// clears it, parks nothing and audits nothing.
func TestResetHandler_ResumedEmptyOrderIsStillDiscarded(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	dp.Engine.RestoreHeld(pos.BasketSnapshot{}, pos.HeldOrigin{ID: "hold-44", Label: "Empty", CreatedAt: "2026-10-03 10:00:00"})

	resetDiscardPost(t, mux)

	if !dp.Engine.HeldOrigin().IsZero() {
		t.Fatal("New Sale must clear the held origin")
	}
	if _, found, err := data.NewHeldSalesRepo(dp.Db).Get(context.Background(), "hold-44"); err != nil || found {
		t.Fatalf("an empty resumed order must not be re-parked, found=%v err=%v", found, err)
	}
	if got := resetDiscardAuditRows(t, dp.Db); len(got) != 0 {
		t.Fatalf("an empty resumed order must log nothing, got %d rows", len(got))
	}
}

func TestResetHandler_FreshBasketLeavesNoDiscardAudit(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	if _, err := dp.Engine.Scan("PLAIN"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	resetDiscardPost(t, mux)
	if got := resetDiscardAuditRows(t, dp.Db); len(got) != 0 {
		t.Fatalf("a basket that never was a held order must not log a discard, got %d rows", len(got))
	}
}

func TestResetHandler_EmptyBasketLeavesNoDiscardAudit(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	resetDiscardPost(t, mux)
	if got := resetDiscardAuditRows(t, dp.Db); len(got) != 0 {
		t.Fatalf("an empty basket must not log a discard, got %d rows", len(got))
	}
}

// Review finding (ut-docs#3423), carried into ut-docs#3582: a kiosk
// pay-at-counter order whose lines all failed to match the catalog resumes
// with zero priced lines and only "add by hand" entries
// (open_orders_counter.go) -- HasItems() is false for it, yet it is still a
// real order, so New Sale re-parks it rather than dropping it.
func TestResetHandler_ResumedByHandOnlyOrderIsReParked(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	snap := pos.BasketSnapshot{AddByHand: []pos.ByHandLine{{Name: "Latte", Qty: 2}}, DisplayNo: "C-12"}
	dp.Engine.RestoreHeld(snap, pos.HeldOrigin{ID: "hold-77", Label: "C-12 · Takeaway", CreatedAt: "2026-10-03 10:00:00"})

	resetDiscardPost(t, mux)

	held, found, err := data.NewHeldSalesRepo(dp.Db).Get(context.Background(), "hold-77")
	if err != nil || !found {
		t.Fatalf("the by-hand-only order must be re-parked, found=%v err=%v", found, err)
	}
	var back pos.BasketSnapshot
	if err := json.Unmarshal([]byte(held.Payload), &back); err != nil || len(back.AddByHand) != 1 {
		t.Fatalf("the re-parked payload must keep its add-by-hand lines, got %q (%v)", held.Payload, err)
	}
	if got := resetDiscardAuditRows(t, dp.Db); len(got) != 0 {
		t.Fatalf("re-parked, not discarded: got %d discard rows", len(got))
	}
}
