package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// TestReportsTabs_Shrinkage_EmptyWindowShowsNone (ut-docs#1465): a shop
// with no shrinkage_events rows yet renders the tab without error, showing
// the same "no data" empty state every other tab uses for an empty window
// — never a raw error page.
func TestReportsTabs_Shrinkage_EmptyWindowShowsNone(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newReportsPageTestDeps(t)

	rec := getReportsTab(t, mux, "shrinkage", "?days=14")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No data for this period") {
		t.Fatalf("expected the empty-window state, got: %.2000s", body)
	}
}

// TestReportsTabs_Shrinkage_SeededWindowShowsTotals (ut-docs#1465): a
// seeded shrinkage_events row for the selected window shows up in both the
// by-reason summary (count/value) and the top-items table.
func TestReportsTabs_Shrinkage_SeededWindowShowsTotals(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)

	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itm-shrink','SKU-SHRINK','Shrinkage Apple',150,1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO shrinkage_events (id, reason_category, item_id, item_name, sku, quantity, unit_price_minor, extended_value_minor, actor_id, order_type, created_at)
VALUES ('evt1', 'waste', 'itm-shrink', 'Shrinkage Apple', 'SKU-SHRINK', 2, 150, 300, 'mgr1', '', datetime('now'))`); err != nil {
		t.Fatalf("seed shrinkage event: %v", err)
	}

	rec := getReportsTab(t, mux, "shrinkage", "?days=14")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Shrinkage Apple", "Waste"} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in the rendered tab, got: %.3000s", want, body)
		}
	}
	// The by-reason total (£3.00 = 300 minor units) and the top-items
	// revenue figure both render the same money value.
	if strings.Count(body, "£3.00") < 2 {
		t.Fatalf("expected the shrinkage value (£3.00) in both the summary and top-items tables, got: %.3000s", body)
	}
}

// TestReportsTabs_Shrinkage_GatedOnVoidCompWastePermission (ut-docs#1465):
// a session without void_comp_waste sees the tab render (200, no error)
// but with none of the seeded data — the button that reaches this tab is
// already hidden from such a session (CanViewShrinkage in reports.html),
// but the handler itself re-checks directly too, same defense-in-depth as
// every other canPerform-gated tab.
func TestReportsTabs_Shrinkage_GatedOnVoidCompWastePermission(t *testing.T) {
	mux, dp := newReportsPageTestDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,is_active) VALUES('itm-shrink','SKU-SHRINK','Shrinkage Apple',150,1)`); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO shrinkage_events (id, reason_category, item_id, item_name, sku, quantity, unit_price_minor, extended_value_minor, actor_id, order_type, created_at)
VALUES ('evt1', 'waste', 'itm-shrink', 'Shrinkage Apple', 'SKU-SHRINK', 2, 150, 300, 'mgr1', '', datetime('now'))`); err != nil {
		t.Fatalf("seed shrinkage event: %v", err)
	}

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/reports/tab/shrinkage?days=14", nil), auth.User{ID: "cashier1", Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Shrinkage Apple") {
		t.Fatalf("a cashier session must not see shrinkage data, got: %.2000s", rec.Body.String())
	}

	// A manager session (granted void_comp_waste by 035_shrinkage_events.sql)
	// sees the real data.
	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/reports/tab/shrinkage?days=14", nil), auth.User{ID: "mgr1", Role: "manager"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Shrinkage Apple") {
		t.Fatalf("a manager session must see shrinkage data, got: %.2000s", rec.Body.String())
	}
}

// TestReportsPage_CanViewShrinkageGatesTabButton (ut-docs#1465): the
// /reports page itself only renders the Shrinkage & Loss tab BUTTON for a
// session holding void_comp_waste, same "eod" tab model.
func TestReportsPage_CanViewShrinkageGatesTabButton(t *testing.T) {
	mux, _ := newReportsPageTestDeps(t)

	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/reports", nil), auth.User{ID: "cashier1", Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "/ui/reports/tab/shrinkage") {
		t.Fatalf("cashier session must not see the Shrinkage & Loss tab button: %.2000s", rec.Body.String())
	}

	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/reports", nil), auth.User{ID: "mgr1", Role: "manager"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "/ui/reports/tab/shrinkage") {
		t.Fatalf("manager session must see the Shrinkage & Loss tab button: %.2000s", rec.Body.String())
	}
}
