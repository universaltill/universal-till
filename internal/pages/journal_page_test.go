package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// newJournalMux wires registerJournal over the seeded pages fixture.
func newJournalMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRate: 20}}
	setStore := settings.NewStore(db)
	d := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    common.LoadState(t.Context(), setStore, cfg),
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Settings: setStore,
	}
	mux := http.NewServeMux()
	registerJournal(mux, d)
	return mux, d
}

// seedJournalPageSale inserts a completed sale with one line and one payment.
func seedJournalPageSale(t *testing.T, d *common.Deps, id, receipt, saleType string) {
	t.Helper()
	if _, err := d.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, tender_type, offline, sync_status, currency, subtotal, discount_total, tax_total, total, created_at)
		VALUES (?, ?, 'completed', ?, 'cash', 1, 'queued', 'GBP', 250, 0, 50, 300, datetime('now'))`, id, receipt, saleType); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		VALUES (?, ?, 1, 'itm1', 'Journal Apple', 1, 250, 2000, 50, 250, 300)`, id+"-l1", id); err != nil {
		t.Fatalf("seed sale line: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO payments(id, sale_id, method_id, amount, currency, paid_at)
		VALUES (?, ?, 'cash', 300, 'GBP', datetime('now'))`, id+"-p1", id); err != nil {
		t.Fatalf("seed payment: %v", err)
	}
}

func TestJournalPageRendersAndGatesInvoiceLink(t *testing.T) {
	mux, d := newJournalMux(t)

	// Seller identity unset: no invoice list link, regardless of role.
	rec := httptest.NewRecorder()
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/journal", nil), auth.User{ID: "m1", Role: "manager"})
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/journal = %d (%s)", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `href="/invoices"`) {
		t.Fatalf("invoice link shown with no seller configured")
	}

	// Seller configured + manager session: the invoices link appears.
	if err := d.Settings.Set(t.Context(), "invoice.seller_name", "Task Runner Ltd"); err != nil {
		t.Fatalf("set seller: %v", err)
	}
	rec = httptest.NewRecorder()
	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/journal", nil), auth.User{ID: "m1", Role: "manager"})
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `href="/invoices"`) {
		t.Fatalf("manager with seller configured should see invoices link (code=%d)", rec.Code)
	}

	// Cashier session (auth on, not a manager): link hidden even with seller set.
	rec = httptest.NewRecorder()
	req = auth.WithUser(httptest.NewRequest(http.MethodGet, "/journal", nil), auth.User{ID: "c1", Role: "cashier"})
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("/journal cashier = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `href="/invoices"`) {
		t.Fatalf("cashier should not see the invoices link")
	}
}

func TestJournalDetailRendersSaleReturnsAndOriginal(t *testing.T) {
	mux, d := newJournalMux(t)
	seedJournalPageSale(t, d, "sale-1", "R-100", "sale")
	seedJournalPageSale(t, d, "sale-2", "R-101", "return")
	// sale-2 is a return of sale-1.
	if _, err := d.Db.Exec(`INSERT INTO sale_links(id, sale_id, original_sale_id, reason) VALUES ('lnk-1','sale-2','sale-1','refund')`); err != nil {
		t.Fatalf("seed sale link: %v", err)
	}

	// Original sale: shows its line, payment, and the return cross-link.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/journal/R-100", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/journal/R-100 = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"R-100", "Journal Apple", "R-101"} {
		if !strings.Contains(body, want) {
			t.Fatalf("detail missing %q: %s", want, body)
		}
	}

	// The return: points back at its original receipt.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/journal/R-101", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/journal/R-101 = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "R-100") {
		t.Fatalf("return detail missing original receipt link: %s", rec.Body.String())
	}

	// Unknown receipt -> 404.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/journal/NOPE", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("/journal/NOPE = %d, want 404", rec.Code)
	}
}

func TestJournalFragmentLimitsAndFullView(t *testing.T) {
	mux, d := newJournalMux(t)
	for i := 1; i <= 7; i++ {
		id := fmt.Sprintf("sale-%d", i)
		receipt := fmt.Sprintf("R-%03d", i)
		if _, err := d.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, tender_type, offline, sync_status, currency, subtotal, discount_total, tax_total, total, created_at)
			VALUES (?, ?, 'completed', 'sale', 'cash', 0, 'synced', 'GBP', 100, 0, 20, 120, datetime('now', ?))`, id, receipt, fmt.Sprintf("-%d minutes", i)); err != nil {
			t.Fatalf("seed sale %d: %v", i, err)
		}
	}

	// Default view: the 5 most recent only.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "R-001") || !strings.Contains(body, "R-005") {
		t.Fatalf("default journal missing recent receipts: %s", body)
	}
	if strings.Contains(body, "R-006") || strings.Contains(body, "R-007") {
		t.Fatalf("default journal should cap at 5 entries: %s", body)
	}

	// Full view: all seven.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal?limit=full", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal?limit=full = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "R-007") {
		t.Fatalf("full journal missing oldest receipt: %s", rec.Body.String())
	}
}

func TestJournalDBErrorsSurfaceAs500(t *testing.T) {
	mux, d := newJournalMux(t)
	if _, err := d.Db.Exec(`DROP TABLE sales`); err != nil {
		t.Fatalf("drop sales: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/journal/R-100", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("detail with broken db = %d, want 500", rec.Code)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("fragment with broken db = %d, want 500", rec.Code)
	}
}
