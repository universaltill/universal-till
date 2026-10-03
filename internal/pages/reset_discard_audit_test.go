package pages

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#3423: a held/table/counter order resumed into the live basket has
// already left held_sales (resumeHeldSale deletes or claims its row), so a
// New Sale tap on it used to drop the order with no trace at all -- no held
// row, no sale, no audit entry. POST /api/pos/reset must now leave an
// audit_log row naming the order it discarded.

func resetDiscardPost(t *testing.T, mux *http.ServeMux) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/reset", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
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

func TestResetHandler_ResumedOrderWithItemsLeavesDiscardAudit(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	// What resumeHeldSale leaves behind: the order's lines restored into the
	// live basket with its held-sale identity, its held_sales row gone.
	snap := pos.BasketSnapshot{Lines: []pos.SnapshotLine{{SKU: "PLAIN", Name: "Plain Item", Qty: 2, PriceCents: 200, ItemID: "itm-plain2", TaxRateBP: 2000}}}
	dp.Engine.RestoreHeld(snap, pos.HeldOrigin{ID: "hold-42", Label: "Table 4", CreatedAt: "2026-10-03 10:00:00"})
	if !dp.Engine.HasItems() {
		t.Fatalf("expected the restored order to have items before reset")
	}
	wantTotal := dp.Engine.Snapshot().Total.Minor()

	resetDiscardPost(t, mux)

	if dp.Engine.HasItems() || !dp.Engine.HeldOrigin().IsZero() {
		t.Fatalf("reset must still clear the basket and its held origin")
	}
	got := resetDiscardAuditRows(t, dp.Db)
	if len(got) != 1 {
		t.Fatalf("expected exactly one held_sale/discard audit row, got %d", len(got))
	}
	if got[0].entityID != "hold-42" {
		t.Fatalf("audit entity_id = %q, want the discarded order's id hold-42", got[0].entityID)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(got[0].payload), &payload); err != nil {
		t.Fatalf("decode audit payload %q: %v", got[0].payload, err)
	}
	if payload["label"] != "Table 4" {
		t.Errorf("payload label = %v, want Table 4", payload["label"])
	}
	if payload["line_count"] != float64(1) {
		t.Errorf("payload line_count = %v, want 1", payload["line_count"])
	}
	if payload["total_minor"] != float64(wantTotal) || wantTotal == 0 {
		t.Errorf("payload total_minor = %v, want %d (non-zero)", payload["total_minor"], wantTotal)
	}
	if payload["first_parked_at"] != "2026-10-03T10:00:00Z" {
		t.Errorf("payload first_parked_at = %v, want the order's first-parked time as ISO-8601", payload["first_parked_at"])
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

// Review finding (ut-docs#3423): a kiosk pay-at-counter order whose lines
// all failed to match the catalog resumes with zero priced lines and only
// "add by hand" entries (open_orders_counter.go) -- HasItems() is false for
// it, yet it is still a real order that New Sale would otherwise drop.
func TestResetHandler_ResumedByHandOnlyOrderLeavesDiscardAudit(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	snap := pos.BasketSnapshot{AddByHand: []pos.ByHandLine{{Name: "Latte", Qty: 2}}, DisplayNo: "C-12"}
	dp.Engine.RestoreHeld(snap, pos.HeldOrigin{ID: "hold-77", Label: "C-12 · Takeaway", CreatedAt: "2026-10-03 10:00:00"})

	resetDiscardPost(t, mux)

	got := resetDiscardAuditRows(t, dp.Db)
	if len(got) != 1 || got[0].entityID != "hold-77" {
		t.Fatalf("expected one held_sale/discard row for hold-77, got %+v", got)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(got[0].payload), &payload); err != nil {
		t.Fatalf("decode audit payload: %v", err)
	}
	if payload["by_hand_count"] != float64(1) {
		t.Errorf("payload by_hand_count = %v, want 1", payload["by_hand_count"])
	}
}
