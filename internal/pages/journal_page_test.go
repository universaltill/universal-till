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
		AuthSvc:  auth.NewService(db),
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

// ut-docs#72: the journal detail Totals card must show a distinct Service
// Charge line when non-zero -- otherwise Subtotal + Tax visibly doesn't add
// up to Total, since a service charge (unlike tip) is folded into Total.
func TestJournalDetail_ShowsServiceChargeDistinctFromTotal(t *testing.T) {
	mux, d := newJournalMux(t)
	// subtotal 1000, tax 0, service_charge 100 -> total 1100.
	if _, err := d.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, tender_type, offline, sync_status, currency, subtotal, discount_total, tax_total, total, service_charge_amount, created_at)
		VALUES ('sale-sc', 'R-SC-1', 'completed', 'sale', 'cash', 1, 'queued', 'GBP', 1000, 0, 0, 1100, 100, datetime('now'))`); err != nil {
		t.Fatalf("seed sale: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		VALUES ('sale-sc-l1', 'sale-sc', 1, 'itm1', 'Steak', 1, 1000, 0, 0, 1000, 1000)`); err != nil {
		t.Fatalf("seed sale line: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO payments(id, sale_id, method_id, amount, currency, paid_at)
		VALUES ('sale-sc-p1', 'sale-sc', 'cash', 1100, 'GBP', datetime('now'))`); err != nil {
		t.Fatalf("seed payment: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/journal/R-SC-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/journal/R-SC-1 = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Service Charge") {
		t.Fatalf("expected a Service Charge line, got: %s", body)
	}
	if !strings.Contains(body, "£1.00") {
		t.Fatalf("expected £1.00 service charge amount, got: %s", body)
	}

	// A sale with no service charge (the existing seedJournalPageSale
	// fixture, service_charge_amount defaults to 0) must NOT show the line.
	seedJournalPageSale(t, d, "sale-nosc", "R-NOSC-1", "sale")
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/journal/R-NOSC-1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/journal/R-NOSC-1 = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Service Charge") {
		t.Fatalf("expected no Service Charge line when service_charge_amount is 0, got: %s", rec.Body.String())
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

// TestJournalUIFilters_TillAndDay covers ut-docs#550 plus the B3 review fix:
// /ui/journal with no "till" param at all defaults to EVERY till's sales
// (preserving pre-#550 behavior, matching ListRecentSales and the
// sale-screen mini-widget); an explicit till=all is behaviorally identical;
// an explicit till= (empty, "This till" picked from the dropdown) narrows to
// local-only; till=<id> narrows to one till; and day narrows to a calendar
// day.
func TestJournalUIFilters_TillAndDay(t *testing.T) {
	mux, d := newJournalMux(t)
	if _, err := d.Db.Exec(`INSERT INTO tills (id, name, bearer_hash, last_seen_at) VALUES ('till-x', 'Kiosk 2', 'bh-x', '2026-08-15T08:00:00Z')`); err != nil {
		t.Fatalf("seed till: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, tender_type, offline, sync_status, currency, subtotal, discount_total, tax_total, total, created_at, till_id)
		VALUES ('sale-local', 'R-LOCAL', 'completed', 'sale', 'cash', 0, 'synced', 'GBP', 100, 0, 20, 120, '2026-08-15T09:00:00Z', '')`); err != nil {
		t.Fatalf("seed local sale: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, tender_type, offline, sync_status, currency, subtotal, discount_total, tax_total, total, created_at, till_id)
		VALUES ('sale-x', 'R-TILLX', 'completed', 'sale', 'cash', 0, 'synced', 'GBP', 100, 0, 20, 120, '2026-08-14T09:00:00Z', 'till-x')`); err != nil {
		t.Fatalf("seed till-x sale: %v", err)
	}

	// Default (no till param at all): every till's sales -- B3, this must
	// NOT be silently narrowed to local-only, matching the page's
	// pre-#550 behavior (ListRecentSales) and the sale-screen mini-widget,
	// which still calls ListRecentSales unfiltered.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal?limit=full", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "R-LOCAL") || !strings.Contains(body, "R-TILLX") {
		t.Fatalf("default /ui/journal (no till param) must show every till's sales: %s", body)
	}
	// Filter widgets themselves are present (ShowFilters=true on the full handler).
	if !strings.Contains(body, `name="till"`) || !strings.Contains(body, `name="day"`) {
		t.Fatalf("expected till/day filter controls in /ui/journal: %s", body)
	}
	// Staleness line for the enrolled till.
	if !strings.Contains(body, "Kiosk 2") || !strings.Contains(body, "2026-08-15T08:00:00Z") {
		t.Fatalf("expected staleness line for enrolled till: %s", body)
	}

	// till=all: behaviorally identical to no param -- both sales, with a
	// Till column showing provenance.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal?limit=full&till=all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal?till=all = %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "R-LOCAL") || !strings.Contains(body, "R-TILLX") {
		t.Fatalf("till=all must show every till's sales: %s", body)
	}
	if !strings.Contains(body, "Kiosk 2") {
		t.Fatalf("till=all row must show the till name: %s", body)
	}

	// till= (empty string, "This till" explicitly picked from the
	// dropdown): narrows to local-only -- B3, this is now the ONLY way to
	// get the old "local only" behavior, and it must be reachable.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal?limit=full&till=", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal?till= = %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "R-LOCAL") || strings.Contains(body, "R-TILLX") {
		t.Fatalf("till= (This till) must show only this till's local sale: %s", body)
	}

	// till=till-x: only that till's sale.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal?limit=full&till=till-x", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal?till=till-x = %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "R-TILLX") || strings.Contains(body, "R-LOCAL") {
		t.Fatalf("till=till-x must show only till-x's sale: %s", body)
	}

	// day filter: only 2026-08-15's sale.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal?limit=full&till=all&day=2026-08-15", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal?day=2026-08-15 = %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "R-LOCAL") || strings.Contains(body, "R-TILLX") {
		t.Fatalf("day=2026-08-15 must show only that day's sale: %s", body)
	}
}

// TestJournalUIFilters_RevokedTillShowsUnknown covers B1 from the ut-docs#550
// review: a sale journaled from a till that's since been revoked
// (TillsRepo.DeleteTill hard-deletes the tills row but sales.till_id is
// retained) must render as "Unknown till", never as "This till" -- the old
// template branched on TillName alone, which is also empty for a genuinely
// local sale, misattributing another register's sale as this till's own.
func TestJournalUIFilters_RevokedTillShowsUnknown(t *testing.T) {
	mux, d := newJournalMux(t)
	if _, err := d.Db.Exec(`INSERT INTO tills (id, name, bearer_hash) VALUES ('till-gone', 'Kiosk Gone', 'bh-gone')`); err != nil {
		t.Fatalf("seed till: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, tender_type, offline, sync_status, currency, subtotal, discount_total, tax_total, total, created_at, till_id)
		VALUES ('sale-revoked', 'R-REVOKED', 'completed', 'sale', 'cash', 0, 'synced', 'GBP', 100, 0, 20, 120, '2026-08-15T09:00:00Z', 'till-gone')`); err != nil {
		t.Fatalf("seed revoked-till sale: %v", err)
	}
	// Revoke the till -- the row is gone but sales.till_id still points at it.
	if _, err := d.Db.Exec(`DELETE FROM tills WHERE id = 'till-gone'`); err != nil {
		t.Fatalf("revoke till: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal?limit=full&till=all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal?till=all = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "R-REVOKED") {
		t.Fatalf("expected the revoked-till sale to still be listed: %s", body)
	}
	if !strings.Contains(body, "Unknown till") {
		t.Fatalf("expected 'Unknown till' for a sale from a revoked till: %s", body)
	}
	// Must not be misattributed as this till's own sale.
	rows := strings.Split(body, "R-REVOKED")
	if len(rows) < 2 || strings.Contains(rows[1][:min(200, len(rows[1]))], ">This till<") {
		t.Fatalf("revoked-till sale must not render as 'This till': %s", body)
	}
}

// TestJournalUIFilters_UnrecognizedTillFallsBackToAll covers S6 from the
// ut-docs#550 review: an unrecognized till id (not "", not "all", not an
// enrolled till) must not silently filter to a dead id -- it falls back to
// the all-tills default instead, matching the visible dropdown state.
func TestJournalUIFilters_UnrecognizedTillFallsBackToAll(t *testing.T) {
	mux, d := newJournalMux(t)
	if _, err := d.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, tender_type, offline, sync_status, currency, subtotal, discount_total, tax_total, total, created_at, till_id)
		VALUES ('sale-local', 'R-LOCAL', 'completed', 'sale', 'cash', 0, 'synced', 'GBP', 100, 0, 20, 120, '2026-08-15T09:00:00Z', '')`); err != nil {
		t.Fatalf("seed local sale: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal?limit=full&till=no-such-till", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal?till=no-such-till = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "R-LOCAL") {
		t.Fatalf("unrecognized till id must fall back to all-tills (still showing local sales), got: %s", body)
	}
	// The dropdown must show "All tills" selected, not the dead id (which
	// has no matching <option> and would otherwise silently default the
	// <select> to its first option, "This till" -- desyncing the visible
	// control from what's actually filtered).
	if !strings.Contains(body, `value="all" selected`) {
		t.Fatalf("dropdown must show 'All tills' selected for an unrecognized till id: %s", body)
	}
}

// TestJournalUIFilters_InvalidDayIgnored covers S5 from the ut-docs#550
// review: a malformed "day" value must not reach the SQL date(?) filter
// unvalidated (which would silently match nothing) -- it's treated as "no
// day filter" instead.
func TestJournalUIFilters_InvalidDayIgnored(t *testing.T) {
	mux, d := newJournalMux(t)
	if _, err := d.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, tender_type, offline, sync_status, currency, subtotal, discount_total, tax_total, total, created_at, till_id)
		VALUES ('sale-local', 'R-LOCAL', 'completed', 'sale', 'cash', 0, 'synced', 'GBP', 100, 0, 20, 120, '2026-08-15T09:00:00Z', '')`); err != nil {
		t.Fatalf("seed local sale: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal?limit=full&day=not-a-date", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal?day=not-a-date = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "R-LOCAL") {
		t.Fatalf("malformed day must be ignored (no day filter), got: %s", body)
	}
	// The <input> must not echo the malformed value back either.
	if strings.Contains(body, `value="not-a-date"`) {
		t.Fatalf("malformed day value must not be echoed back into the day input: %s", body)
	}
}

// TestJournalUIFilters_ReplicaCrossTillNotice covers B2 from the ut-docs#550
// review: on a replica (sync.primary_url set, the same "is this till a
// replica" simulation internal/pages/sync_admin_test.go uses), an explicit
// cross-till ask (till=all) shows an honest notice instead of a possibly-
// empty table with no explanation; the bare default view (no till param at
// all) does NOT show it, since local-only-by-default is honest by
// construction on a replica; and the staleness line renders an em-dash for
// a till whose LastSeenAt is empty (redacted on a replica's synced copy, or
// genuinely never-seen), mirroring the tills.html convention.
func TestJournalUIFilters_ReplicaCrossTillNotice(t *testing.T) {
	mux, d := newJournalMux(t)
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://primary.example"); err != nil {
		t.Fatalf("set primary_url: %v", err)
	}
	// A sibling till whose last_seen_at is empty -- as it would be on a
	// replica's redacted copy of the tills roster (ut-docs#405).
	if _, err := d.Db.Exec(`INSERT INTO tills (id, name, bearer_hash, last_seen_at) VALUES ('till-y', 'Kiosk 3', 'bh-y', '')`); err != nil {
		t.Fatalf("seed till: %v", err)
	}
	if _, err := d.Db.Exec(`INSERT INTO sales(id, receipt_no, status, sale_type, tender_type, offline, sync_status, currency, subtotal, discount_total, tax_total, total, created_at, till_id)
		VALUES ('sale-local', 'R-LOCAL', 'completed', 'sale', 'cash', 0, 'synced', 'GBP', 100, 0, 20, 120, '2026-08-15T09:00:00Z', '')`); err != nil {
		t.Fatalf("seed local sale: %v", err)
	}

	// Bare default view on a replica: no notice.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal (replica, default) = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "Cross-till sales are only available") {
		t.Fatalf("bare default view on a replica must not show the cross-till notice: %s", body)
	}
	// Staleness line still renders, with an em-dash for the empty LastSeenAt.
	if !strings.Contains(body, "Kiosk 3") {
		t.Fatalf("expected staleness line for the sibling till: %s", body)
	}
	if !strings.Contains(body, "Kiosk 3: —") && !strings.Contains(body, "Kiosk 3‏: —") {
		t.Fatalf("expected an em-dash for the sibling till's empty LastSeenAt: %s", body)
	}

	// Explicit till=all on a replica: the notice appears.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal?till=all", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal?till=all (replica) = %d", rec.Code)
	}
	body = rec.Body.String()
	if !strings.Contains(body, "Cross-till sales are only available") {
		t.Fatalf("explicit till=all on a replica must show the cross-till notice: %s", body)
	}

	// Explicit till= (This till) on a replica: no notice -- local-only
	// always works even on a replica.
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/journal?till=", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/journal?till= (replica) = %d", rec.Code)
	}
	body = rec.Body.String()
	if strings.Contains(body, "Cross-till sales are only available") {
		t.Fatalf("explicit 'This till' on a replica must not show the cross-till notice: %s", body)
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
