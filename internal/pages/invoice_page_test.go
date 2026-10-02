package pages

import (
	"context"
	"encoding/csv"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/settings"
)

// vatBreakdown already has thorough coverage in invoice_test.go
// (TestVATBreakdownGroupsByRecordedRate, TestVATBreakdownProratesSaleDiscount
// — both inclusive and exclusive modes). This file covers everything else
// in invoice_page.go: seller config, issuing (incl. idempotency), the
// automatic credit-note-on-refund flow, and the HTTP handlers.

func TestFormatQty(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1, "1"}, {2, "2"}, {0, "0"}, {1.5, "1.5"}, {0.333, "0.333"}, {100, "100"},
	}
	for _, c := range cases {
		if got := formatQty(c.in); got != c.want {
			t.Errorf("formatQty(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func newInvoiceTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	cfg := &config.Config{
		Theme:   "default",
		Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000},
		Marketplace: config.MarketplaceConfig{
			EndpointURL: "http://localhost:8081",
		},
	}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	state := common.LoadState(t.Context(), settings.NewStore(db), cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Pm:       pm,
		Settings: settings.NewStore(db),
	}
	// Registered AFTER the db.Close cleanup above, so LIFO order runs this
	// FIRST: the invoice/receipt print goroutines (ut-docs#425, #514)
	// finish before Close and TempDir removal can race them.
	t.Cleanup(dp.WaitForAsyncWork)
	mux := http.NewServeMux()
	registerInvoices(mux, dp)
	return mux, dp
}

func setSeller(t *testing.T, dp *common.Deps) {
	t.Helper()
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, keyInvoiceSellerName, "Task Runner Ltd"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, keyInvoiceSellerAddress, "1 High Street"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, keyInvoiceSellerVATNo, "GB123456789"); err != nil {
		t.Fatal(err)
	}
}

func TestSellerConfig_OffUntilNameSet(t *testing.T) {
	_, dp := newInvoiceTestDeps(t)
	if _, on := sellerConfig(context.Background(), dp); on {
		t.Fatal("expected the invoice feature off with no seller name configured")
	}
	setSeller(t, dp)
	seller, on := sellerConfig(context.Background(), dp)
	if !on {
		t.Fatal("expected the invoice feature on once a seller name is set")
	}
	if seller.Name != "Task Runner Ltd" || seller.VATNo != "GB123456789" {
		t.Fatalf("unexpected seller config: %+v", seller)
	}
}

func seedInvoiceableSale(t *testing.T, dp *common.Deps, id, receiptNo string, total, taxTotal int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sales(id, receipt_no, status, sale_type, currency, subtotal, discount_total, tax_total, total, created_at, completed_at)
VALUES(?, ?, 'completed', 'sale', 'GBP', ?, 0, ?, ?, datetime('now'), datetime('now'))`, id, receiptNo, total-taxTotal, taxTotal, total); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO sale_lines(id, sale_id, line_no, item_id, name_snapshot, sku_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
VALUES(?, ?, 1, 'itm1', 'Apple', 'ABC', 1, ?, 2000, ?, ?, ?)`, id+"-line1", id, total-taxTotal, taxTotal, total-taxTotal, total); err != nil {
		t.Fatal(err)
	}
}

func TestIssueInvoice_RequiresSellerConfigured(t *testing.T) {
	_, dp := newInvoiceTestDeps(t)
	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20)
	repo := data.NewPOSRepo(dp.Db)
	sale, _, err := repo.GetSaleDetail(context.Background(), "R001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issueInvoice(context.Background(), dp, sale, "invoice", "", "Jane Doe", "", "", "user1"); err == nil {
		t.Fatal("expected an error issuing an invoice with no seller configured")
	}
}

func TestIssueInvoice_ComputesNetTaxGrossFromVATBreakdown(t *testing.T) {
	_, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20)
	repo := data.NewPOSRepo(dp.Db)
	sale, _, err := repo.GetSaleDetail(context.Background(), "R001")
	if err != nil {
		t.Fatal(err)
	}

	inv, err := issueInvoice(context.Background(), dp, sale, "invoice", "", "Jane Doe", "1 Elm St", "", "user1")
	if err != nil {
		t.Fatal(err)
	}
	if inv.NetTotal != 100 || inv.TaxTotal != 20 || inv.GrossTotal != 120 {
		t.Fatalf("expected net=100 tax=20 gross=120 from the sale line, got %+v", inv)
	}
	if inv.CustomerName != "Jane Doe" {
		t.Fatalf("expected the customer name recorded, got %q", inv.CustomerName)
	}

	// Audit trail.
	var count int
	if err := dp.Db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM audit_log WHERE action = 'invoice_issued'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected 1 invoice_issued audit entry, got %d", count)
	}
}

func TestMaybeIssueCreditNote_AutoIssuesOnceForAnInvoicedSale(t *testing.T) {
	_, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	repo := data.NewPOSRepo(dp.Db)
	invRepo := data.NewInvoiceRepo(dp.Db)
	ctx := context.Background()

	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20)
	sale, _, err := repo.GetSaleDetail(ctx, "R001")
	if err != nil {
		t.Fatal(err)
	}
	origInv, err := issueInvoice(ctx, dp, sale, "invoice", "", "Jane Doe", "", "", "user1")
	if err != nil {
		t.Fatal(err)
	}

	// A refund against sale1. maybeIssueCreditNote takes the original sale
	// id as a direct argument (matching the real caller, refund_page.go's
	// POST /api/refund -> maybeIssueCreditNote(ctx, d, newReceipt,
	// detail.ID, actorID)) -- it never reads sale_links itself, so no
	// sale_links row is needed here to exercise it.
	seedInvoiceableSale(t, dp, "return1", "R002", 120, 20)
	if _, err := dp.Db.ExecContext(ctx, `UPDATE sales SET sale_type = 'return' WHERE id = 'return1'`); err != nil {
		t.Fatal(err)
	}

	maybeIssueCreditNote(ctx, dp, "R002", "sale1", "user1")

	cn, found, err := invRepo.BySale(ctx, "return1", "credit_note")
	if err != nil || !found {
		t.Fatalf("expected a credit note auto-issued, got found=%v err=%v", found, err)
	}
	if cn.OriginalInvoiceID != origInv.ID {
		t.Fatalf("expected the credit note to reference the original invoice, got %+v", cn)
	}

	// Calling it again must NOT issue a second credit note for the same refund.
	maybeIssueCreditNote(ctx, dp, "R002", "sale1", "user1")
	var count int
	if err := dp.Db.QueryRowContext(ctx, `SELECT COUNT(*) FROM invoices WHERE sale_id = 'return1' AND kind = 'credit_note'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one credit note after calling maybeIssueCreditNote twice, got %d", count)
	}
}

func TestMaybeIssueCreditNote_NoOpWhenOriginalSaleWasNeverInvoiced(t *testing.T) {
	_, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	ctx := context.Background()
	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20) // never invoiced
	seedInvoiceableSale(t, dp, "return1", "R002", 120, 20)
	if _, err := dp.Db.ExecContext(ctx, `UPDATE sales SET sale_type = 'return' WHERE id = 'return1'`); err != nil {
		t.Fatal(err)
	}

	// Must not panic or error — just silently does nothing.
	maybeIssueCreditNote(ctx, dp, "R002", "sale1", "user1")

	invRepo := data.NewInvoiceRepo(dp.Db)
	if _, found, _ := invRepo.BySale(ctx, "return1", "credit_note"); found {
		t.Fatal("expected no credit note for a sale that was never invoiced")
	}
}

// --- HTTP handlers ---

func TestPostInvoicesIssue_RequiresReceiptAndCustomerName(t *testing.T) {
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/invoices/issue", strings.NewReader("receipt_no=&customer_name="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing receipt/customer, got %d", rec.Code)
	}
}

func TestPostInvoicesIssue_RejectsNonSaleOrIncompleteSale(t *testing.T) {
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/invoices/issue", strings.NewReader("receipt_no=NO-SUCH&customer_name=Jane"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for an unknown receipt, got %d", rec.Code)
	}
}

func TestPostInvoicesIssue_SuccessThenIdempotentOnRetry(t *testing.T) {
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/invoices/issue", strings.NewReader("receipt_no=R001&customer_name=Jane+Doe"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("expected a success indicator, got %s", rec.Body.String())
	}

	// Re-issuing for the same sale returns the EXISTING invoice, not a
	// duplicate (UNIQUE(sale_id,kind) backs this at the DB layer too).
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/invoices/issue", strings.NewReader("receipt_no=R001&customer_name=Jane+Doe"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on retry, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already") {
		t.Fatalf("expected an 'already issued' message on retry, got %s", rec.Body.String())
	}

	var count int
	if err := dp.Db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM invoices WHERE sale_id = 'sale1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one invoice row after issuing twice, got %d", count)
	}
}

// TestPostSettingsInvoice_SavesSellerIdentity (ut-docs#866): the seller
// identity endpoint is wired onto the #557 manager-override elevation
// mechanism, same as settings_page.go's own sites — no prior test existed
// for it at all.
func TestPostSettingsInvoice_SavesSellerIdentity(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newInvoiceTestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/invoice",
		strings.NewReader("seller_name=Acme+Ltd&seller_address=1+High+St&seller_vat_no=GB123"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for an authorized session, got %d: %s", rec.Code, rec.Body.String())
	}
	name, _, _ := dp.Settings.Get(context.Background(), keyInvoiceSellerName)
	addr, _, _ := dp.Settings.Get(context.Background(), keyInvoiceSellerAddress)
	vat, _, _ := dp.Settings.Get(context.Background(), keyInvoiceSellerVATNo)
	if name != "Acme Ltd" || addr != "1 High St" || vat != "GB123" {
		t.Fatalf("expected seller identity persisted, got name=%q address=%q vat=%q", name, addr, vat)
	}
}

// TestPostSettingsInvoice_BlockedSessionGetsElevationPrompt (ut-docs#866): a
// blocked cashier gets an in-place PIN prompt instead of a flat 403, and
// the seller identity is NOT changed until it's approved.
func TestPostSettingsInvoice_BlockedSessionGetsElevationPrompt(t *testing.T) {
	mux, dp := newInvoiceTestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/invoice",
		strings.NewReader("seller_name=Blocked+Co&seller_address=&seller_vat_no="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") ||
		!strings.Contains(rec.Body.String(), `name="override_pin"`) {
		t.Fatalf("blocked invoice save = %d, want 200 with the elevation prompt: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-UT-Response"); got != "elevation-prompt" {
		t.Fatalf("blocked invoice save: X-UT-Response = %q, want \"elevation-prompt\"", got)
	}
	if !strings.Contains(rec.Body.String(), `name="seller_name" value="Blocked Co"`) {
		t.Fatalf("expected seller_name to round-trip via Hidden fields, got: %s", rec.Body.String())
	}
	if name, _, _ := dp.Settings.Get(context.Background(), keyInvoiceSellerName); name != "" {
		t.Fatalf("seller identity must not have been changed without approval, got %q", name)
	}
}

func TestGetInvoices_RedirectsWithoutSellerConfigured(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newInvoiceTestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/invoices", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected a redirect when the invoice feature isn't configured, got %d", rec.Code)
	}
}

func TestGetInvoices_RequiresManager(t *testing.T) {
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/invoices", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected a redirect for a non-manager, got %d", rec.Code)
	}
}

func TestGetInvoices_ListsIssuedInvoicesWithCreditNotesNegated(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20)
	repo := data.NewPOSRepo(dp.Db)
	sale, _, err := repo.GetSaleDetail(context.Background(), "R001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issueInvoice(context.Background(), dp, sale, "invoice", "", "Jane Doe", "", "", "user1"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/invoices", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Jane Doe") {
		t.Fatalf("expected the issued invoice listed, got body without it")
	}
}

// ut-docs#1321: a bare page load (no ?from=/?to=) used to mean "every
// invoice ever issued" — an unbounded full-history scan on a shop with
// years of trading. It now defaults `from` to the start of the current
// calendar month; `to` stays open (see TestGetInvoices_DefaultDoesNotHide
// TodaysInvoiceAcrossTimezones for why `to` is deliberately NOT also
// defaulted to "today").
func TestGetInvoices_DefaultsToCurrentMonthWhenNoFilterGiven(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20)
	repo := data.NewPOSRepo(dp.Db)
	sale, _, err := repo.GetSaleDetail(context.Background(), "R001")
	if err != nil {
		t.Fatal(err)
	}
	// Bypass issueInvoice (hardcodes time.Now() as IssuedAt) to plant an
	// invoice well outside the current month, the same direct-repo seeding
	// small_repos_test.go's TestInvoiceRepo_List uses.
	if _, err := data.NewInvoiceRepo(dp.Db).Create(context.Background(), data.InvoiceInput{
		Kind: "invoice", SaleID: sale.ID, CustomerName: "Old Customer", SellerJSON: "{}", VATBreakdownJSON: "[]",
		NetTotal: 100, TaxTotal: 20, GrossTotal: 120, IssuedAt: "2020-01-15T10:00:00Z", IssuedBy: "user1",
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/invoices", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Old Customer") {
		t.Fatalf("a bare page load must default to the current month, not show a 2020 invoice: %s", rec.Body.String())
	}
	now := time.Now()
	wantFrom := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).Format("2006-01-02")
	if !strings.Contains(rec.Body.String(), `name="from" value="`+wantFrom+`"`) {
		t.Fatalf("expected the from-date picker defaulted to %q (start of this month), got: %s", wantFrom, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `name="to" value="">`) {
		t.Fatalf("expected `to` to stay OPEN (empty picker value), not defaulted — a `to=today` bound is exactly the timezone bug TestGetInvoices_DefaultDoesNotHideTodaysInvoiceAcrossTimezones guards against: %s", rec.Body.String())
	}

	// An explicit range still overrides the default and reaches the 2020 invoice.
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodGet, "/invoices?from=2020-01-01&to=2020-01-31", nil)
	mux.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}
	if !strings.Contains(rec2.Body.String(), "Old Customer") {
		t.Fatalf("an explicit ?from=/?to= must still reach older invoices: %s", rec2.Body.String())
	}
}

// TestGetInvoices_DefaultDoesNotHideTodaysInvoiceAcrossTimezones is the
// independent review's regression (ut-docs#1321): the default handler
// builds a LOCAL calendar date for `from`, but InvoiceRow.IssuedAt is
// stored UTC RFC3339 and compared lexicographically
// (invoiceRangeBound/List/Totals) — defaulting `to` to "today" as well
// (an earlier draft of this fix did) would have silently excluded an
// invoice issued minutes ago whenever the local and UTC calendar dates
// disagree at that instant (any timezone west of UTC in the evening; the
// first ~2h of a month in a timezone east of UTC, e.g. the Germany
// pilot). This is exactly why the fix leaves `to` open rather than also
// defaulting it — this test reproduces the west-of-UTC case with a real
// TZ and a real "issued this instant" timestamp, the shape a live sale
// actually produces, and would fail if `to` were ever re-defaulted.
func TestGetInvoices_DefaultDoesNotHideTodaysInvoiceAcrossTimezones(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	// UTC-10 — the local calendar date can trail UTC's by a full day. Pinned
	// through invoiceLoc: TZ set inside a test doesn't move time.Local.
	pinInvoiceClock(t, time.Now(), time.FixedZone("UTC-10", -10*3600))
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20)
	repo := data.NewPOSRepo(dp.Db)
	sale, _, err := repo.GetSaleDetail(context.Background(), "R001")
	if err != nil {
		t.Fatal(err)
	}
	// Issued "right now" in UTC — exactly what a real, live issueInvoice
	// call (time.Now().UTC()) stores for a sale completed this instant.
	issuedAt := time.Now().UTC().Format(time.RFC3339)
	if _, err := data.NewInvoiceRepo(dp.Db).Create(context.Background(), data.InvoiceInput{
		Kind: "invoice", SaleID: sale.ID, CustomerName: "Just Now Customer", SellerJSON: "{}", VATBreakdownJSON: "[]",
		NetTotal: 100, TaxTotal: 20, GrossTotal: 120, IssuedAt: issuedAt, IssuedBy: "user1",
	}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/invoices", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Just Now Customer") {
		t.Fatalf("an invoice issued THIS INSTANT (UTC %s) must never be hidden by the bare-load default, regardless of local timezone: %s", issuedAt, rec.Body.String())
	}
}

func TestGetInvoicesExport_RequiresManager(t *testing.T) {
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/invoices/export", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for a non-manager, got %d", rec.Code)
	}
}

func TestGetInvoicesExport_CSVHasCreditNoteAsNegative(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	ctx := context.Background()
	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20)
	repo := data.NewPOSRepo(dp.Db)
	sale, _, err := repo.GetSaleDetail(ctx, "R001")
	if err != nil {
		t.Fatal(err)
	}
	invRow, err := issueInvoice(ctx, dp, sale, "invoice", "", "Jane Doe", "", "", "user1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issueInvoice(ctx, dp, sale, "credit_note", invRow.ID, "Jane Doe", "", "", "user1"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/invoices/export", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "text/csv; charset=utf-8" {
		t.Fatalf("expected a CSV content type, got %q", rec.Header().Get("Content-Type"))
	}

	// Parse the actual columns rather than substring-matching the raw
	// body — a "negate everything" regression (e.g. the invoice row also
	// coming out negative) would still contain "-1.20" somewhere and slip
	// past a substring check.
	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("expected valid CSV, got parse error: %v\nbody:\n%s", err, rec.Body.String())
	}
	const kindCol, grossCol = 1, 8
	var sawInvoice, sawCreditNote bool
	for _, row := range rows[1:] { // skip header
		switch row[kindCol] {
		case "invoice":
			sawInvoice = true
			if row[grossCol] != "1.20" {
				t.Fatalf("expected the invoice row's gross to be 1.20, got %q (row: %v)", row[grossCol], row)
			}
		case "credit_note":
			sawCreditNote = true
			if row[grossCol] != "-1.20" {
				t.Fatalf("expected the credit note row's gross to be -1.20, got %q (row: %v)", row[grossCol], row)
			}
		}
	}
	if !sawInvoice || !sawCreditNote {
		t.Fatalf("expected both an invoice row and a credit_note row, got %d data rows: %v", len(rows)-1, rows)
	}
}

// ut-docs#195: CustomerName/CustomerVATNo are free-typed by whoever issues
// an invoice, so a crafted formula-shaped value must come out defused
// (leading apostrophe) rather than reaching Excel/LibreOffice as a live
// formula — while the invoice's own signed amounts (a credit note is
// legitimately negative, per the test above) must NOT be touched by the
// same mitigation.
func TestGetInvoicesExport_CustomerFieldsAreCSVFormulaSafe(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	ctx := context.Background()
	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20)
	repo := data.NewPOSRepo(dp.Db)
	sale, _, err := repo.GetSaleDetail(ctx, "R001")
	if err != nil {
		t.Fatal(err)
	}
	const malicious = `=cmd|'/c calc'!A1`
	if _, err := issueInvoice(ctx, dp, sale, "invoice", "", malicious, "", malicious, "user1"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/invoices/export", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("expected valid CSV, got parse error: %v\nbody:\n%s", err, rec.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 data row, got %d: %v", len(rows), rows)
	}
	const customerCol, vatNoCol, grossCol = 3, 4, 8
	data := rows[1]
	if got := data[customerCol]; got != "'"+malicious {
		t.Fatalf("CustomerName not defused: got %q, want %q", got, "'"+malicious)
	}
	if got := data[vatNoCol]; got != "'"+malicious {
		t.Fatalf("CustomerVATNo not defused: got %q, want %q", got, "'"+malicious)
	}
	if got := data[grossCol]; got != "1.20" {
		t.Fatalf("Gross must be untouched by the sanitizer, got %q", got)
	}
}

// ut-docs#195 review: DisplayNo and ReceiptNo look system-generated but
// aren't fully — DisplayNo embeds sync.receipt_prefix (a setting writable as
// free text via the generic /api/settings/upsert, no allowlist) and
// ReceiptNo is the sale's own receipt number, generated from that same
// prefix in production. Both must come out defused too, or the ticket's own
// acceptance criterion ("a crafted value in ANY exported field") isn't met.
func TestGetInvoicesExport_DisplayNoAndReceiptNoAreCSVFormulaSafe(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	ctx := context.Background()
	const malicious = `=cmd|'/c calc'!A1`
	// A manager can set this via POST /api/settings/upsert with no
	// validation (settings_page.go's generic upsert) — issueInvoice reads
	// it straight into the new invoice's Series/DisplayNo.
	if err := dp.Settings.Set(ctx, "sync.receipt_prefix", malicious); err != nil {
		t.Fatal(err)
	}
	// The sale's own receipt_no, in production also derived from the same
	// prefix (pos_repo.go's nextReceiptNo) — seeded directly here to
	// exercise the export's defusing independent of that generation path.
	seedInvoiceableSale(t, dp, "sale1", malicious, 120, 20)
	repo := data.NewPOSRepo(dp.Db)
	sale, _, err := repo.GetSaleDetail(ctx, malicious)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := issueInvoice(ctx, dp, sale, "invoice", "", "Jane Doe", "", "", "user1"); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/invoices/export", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rows, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("expected valid CSV, got parse error: %v\nbody:\n%s", err, rec.Body.String())
	}
	if len(rows) != 2 {
		t.Fatalf("expected header + 1 data row, got %d: %v", len(rows), rows)
	}
	const invoiceCol, receiptCol = 0, 5
	data := rows[1]
	if got := data[invoiceCol]; !strings.HasPrefix(got, "'"+malicious) {
		t.Fatalf("DisplayNo not defused: got %q, want prefix %q", got, "'"+malicious)
	}
	if got := data[receiptCol]; got != "'"+malicious {
		t.Fatalf("ReceiptNo not defused: got %q, want %q", got, "'"+malicious)
	}
}

func TestGetInvoiceByDisplayNo_NotFound(t *testing.T) {
	mux, _ := newInvoiceTestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/invoice/NO-SUCH", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown display number, got %d", rec.Code)
	}
}

func TestGetInvoiceByDisplayNo_RendersIssuedInvoice(t *testing.T) {
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	seedInvoiceableSale(t, dp, "sale1", "R001", 120, 20)
	repo := data.NewPOSRepo(dp.Db)
	sale, _, err := repo.GetSaleDetail(context.Background(), "R001")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := issueInvoice(context.Background(), dp, sale, "invoice", "", "Jane Doe", "", "", "user1")
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/invoice/"+inv.DisplayNo, nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Jane Doe") {
		t.Fatalf("expected the customer name rendered, got body without it")
	}
}

// A discounted line's net÷qty figure is the post-discount effective price,
// not the catalogue unit price — invoiceLineSub must label it rather than
// let it pass as an undiscounted "unit price" (review finding S2,
// ut-docs#3337). A line with no discount gets no such marker (regression
// guard against always appending it).
func TestInvoiceLineSub_AfterDiscountMarker(t *testing.T) {
	discounted := data.SaleDetailLine{
		Name: "Wine", Qty: 3, TaxRateBP: 2000, TaxAmount: 500,
		LineTotal: 3000, LineDiscount: 600,
	}
	got := invoiceLineSub(discounted, "en", " - ")
	want := "Unit price (excl. VAT): £8.33 (after discount) - VAT 20.00%"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}

	undiscounted := data.SaleDetailLine{
		Name: "Bread", Qty: 2, TaxRateBP: 0, TaxAmount: 0,
		LineTotal: 300, LineDiscount: 0,
	}
	got = invoiceLineSub(undiscounted, "en", " - ")
	if strings.Contains(got, "after discount") {
		t.Fatalf("an undiscounted line must not carry the after-discount marker, got %q", got)
	}
}

// ut-docs#3337: each invoice line carries its unit price excl. VAT
// ((LineTotal-TaxAmount)/Qty) and its own recorded VAT rate.
func TestBuildInvoiceDoc_LineUnitPriceExclVATAndRate(t *testing.T) {
	_, dp := newInvoiceTestDeps(t)
	ctx := context.Background()
	setSeller(t, dp)
	seedMixedRateSale(t, dp, "sale-mix", "R-MIX", time.Now().UTC().Format(time.RFC3339))
	sale, _, err := data.NewPOSRepo(dp.Db).GetSaleDetail(ctx, "R-MIX")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := issueInvoice(ctx, dp, sale, "invoice", "", "Jane Doe", "", "", "user1")
	if err != nil {
		t.Fatal(err)
	}
	doc := buildInvoiceDoc(ctx, dp, inv, sale)
	want := map[string]string{
		"Bread": "Unit price (excl. VAT): £1.50 - VAT 0.00%",
		"Wine":  "Unit price (excl. VAT): £10.00 - VAT 20.00%",
	}
	if len(doc.Lines) != 2 {
		t.Fatalf("expected 2 lines, got %+v", doc.Lines)
	}
	for _, l := range doc.Lines {
		if l.Sub != want[l.Name] {
			t.Fatalf("%s Sub: got %q, want %q", l.Name, l.Sub, want[l.Name])
		}
	}
}

// ut-docs#3337: the time of supply (the sale's own date) prints only when
// it falls on a different calendar date than the invoice's issue date.
func TestBuildInvoiceDoc_TimeOfSupplyOnlyWhenDifferentDay(t *testing.T) {
	_, dp := newInvoiceTestDeps(t)
	ctx := context.Background()
	setSeller(t, dp)
	saleAt := time.Now().Add(-72 * time.Hour).UTC()
	seedMixedRateSale(t, dp, "sale-old", "R-OLD", saleAt.Format(time.RFC3339))
	seedMixedRateSale(t, dp, "sale-new", "R-NEW", time.Now().UTC().Format(time.RFC3339))
	repo := data.NewPOSRepo(dp.Db)
	locale := httpx.DefaultLocale()
	supplyLine := "Time of supply: " + httpx.FormatDate(saleAt.Local(), locale)

	old, _, _ := repo.GetSaleDetail(ctx, "R-OLD")
	invOld, err := issueInvoice(ctx, dp, old, "invoice", "", "Jane Doe", "", "", "user1")
	if err != nil {
		t.Fatal(err)
	}
	if meta := strings.Join(buildInvoiceDoc(ctx, dp, invOld, old).Meta, "\n"); !strings.Contains(meta, supplyLine) {
		t.Fatalf("expected %q in Meta for an earlier sale, got %q", supplyLine, meta)
	}

	same, _, _ := repo.GetSaleDetail(ctx, "R-NEW")
	invNew, err := issueInvoice(ctx, dp, same, "invoice", "", "Jane Doe", "", "", "user1")
	if err != nil {
		t.Fatal(err)
	}
	if meta := strings.Join(buildInvoiceDoc(ctx, dp, invNew, same).Meta, "\n"); strings.Contains(meta, "Time of supply") {
		t.Fatalf("expected no time-of-supply line for a same-day invoice, got %q", meta)
	}
}

// ut-docs#3337: the on-screen invoice mirrors both additions.
func TestGetInvoiceByDisplayNo_UnitPriceRateAndTimeOfSupply(t *testing.T) {
	mux, dp := newInvoiceTestDeps(t)
	ctx := context.Background()
	setSeller(t, dp)
	saleAt := time.Now().Add(-72 * time.Hour).UTC()
	seedMixedRateSale(t, dp, "sale-old", "R-OLD", saleAt.Format(time.RFC3339))
	seedMixedRateSale(t, dp, "sale-new", "R-NEW", time.Now().UTC().Format(time.RFC3339))
	repo := data.NewPOSRepo(dp.Db)

	get := func(receiptNo string) string {
		t.Helper()
		sale, _, err := repo.GetSaleDetail(ctx, receiptNo)
		if err != nil {
			t.Fatal(err)
		}
		inv, err := issueInvoice(ctx, dp, sale, "invoice", "", "Jane Doe", "", "", "user1")
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/invoice/"+inv.DisplayNo, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", rec.Code)
		}
		return rec.Body.String()
	}

	body := get("R-OLD")
	for _, want := range []string{
		"Unit price (excl. VAT): £10.00 · VAT 20.00%",
		"Unit price (excl. VAT): £1.50 · VAT 0.00%",
		"Time of supply: " + httpx.FormatDate(saleAt.Local(), "en"),
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("expected %q in the on-screen invoice", want)
		}
	}
	if body := get("R-NEW"); strings.Contains(body, "Time of supply") {
		t.Fatal("expected no time-of-supply line on a same-day invoice")
	}
}

// pinInvoiceClock fixes the invoice register's clock and shop timezone for
// one test (ut-docs#3300). Setting TZ in a test does not move time.Local
// once the runtime has loaded it, so the register reads both through
// injectable vars instead.
func pinInvoiceClock(t *testing.T, now time.Time, loc *time.Location) {
	t.Helper()
	prevNow, prevLoc := invoiceNow, invoiceLoc
	invoiceNow = func() time.Time { return now }
	invoiceLoc = func() *time.Location { return loc }
	t.Cleanup(func() { invoiceNow, invoiceLoc = prevNow, prevLoc })
}

// plantInvoice stores an invoice with an exact issued_at on its own sale
// (one invoice per sale; issueInvoice stamps the real clock, so it can't
// place one at a chosen instant).
func plantInvoice(t *testing.T, dp *common.Deps, receiptNo, customer, issuedAt string) {
	t.Helper()
	seedInvoiceableSale(t, dp, "sale-"+receiptNo, receiptNo, 120, 20)
	sale, _, err := data.NewPOSRepo(dp.Db).GetSaleDetail(context.Background(), receiptNo)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := data.NewInvoiceRepo(dp.Db).Create(context.Background(), data.InvoiceInput{
		Kind: "invoice", SaleID: sale.ID, CustomerName: customer, SellerJSON: "{}", VATBreakdownJSON: "[]",
		NetTotal: 100, TaxTotal: 20, GrossTotal: 120, IssuedAt: issuedAt, IssuedBy: "user1",
	}); err != nil {
		t.Fatal(err)
	}
}

// ut-docs#3300: in a timezone east of UTC, the first local hour(s) of a
// month are still the previous month in UTC. The bare-load default `from`
// (start of this LOCAL month) was compared as a date string against UTC
// issued_at, so an invoice issued at 00:20 local on the 1st was hidden.
func TestGetInvoices_DefaultShowsInvoiceIssuedAfterLocalMidnightOnTheFirst(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	plus1 := time.FixedZone("UTC+1", 3600)
	// 2026-10-01 00:30 local = 2026-09-30 23:30 UTC.
	pinInvoiceClock(t, time.Date(2026, 9, 30, 23, 30, 0, 0, time.UTC), plus1)
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	plantInvoice(t, dp, "R001", "This Month Customer", "2026-09-30T23:20:00Z") // 00:20 local, 1 Oct
	plantInvoice(t, dp, "R002", "Last Month Customer", "2026-09-30T22:50:00Z") // 23:50 local, 30 Sep

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/invoices", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "This Month Customer") {
		t.Fatalf("an invoice issued at 00:20 local on the 1st must be in this month's default list: %s", body)
	}
	if strings.Contains(body, "Last Month Customer") {
		t.Fatalf("an invoice issued at 23:50 local on the 30th belongs to last month: %s", body)
	}
	if !strings.Contains(body, `name="from" value="2026-10-01"`) {
		t.Fatalf("the from picker must show the LOCAL first of the month (2026-10-01): %s", body)
	}
}

// ut-docs#3300 review: the register's Issued column must show the same
// LOCAL date the row is filtered under — not the UTC date (30 Sep for an
// invoice issued at 00:20 on 1 Oct in UTC+1). The `date` template helper
// reads time.Local, so this test swaps it (no t.Parallel in this package).
func TestGetInvoices_IssuedColumnShowsLocalDate(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	plus1 := time.FixedZone("UTC+1", 3600)
	prevLocal := time.Local
	time.Local = plus1
	t.Cleanup(func() { time.Local = prevLocal })
	pinInvoiceClock(t, time.Date(2026, 9, 30, 23, 30, 0, 0, time.UTC), plus1)
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	plantInvoice(t, dp, "R001", "This Month Customer", "2026-09-30T23:20:00Z")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/invoices", nil))
	body := rec.Body.String()
	local := httpx.FormatDate(time.Date(2026, 10, 1, 0, 0, 0, 0, plus1), httpx.RequestLocale(httptest.NewRequest(http.MethodGet, "/invoices", nil)))
	utc := httpx.FormatDate(time.Date(2026, 9, 30, 0, 0, 0, 0, time.UTC), httpx.RequestLocale(httptest.NewRequest(http.MethodGet, "/invoices", nil)))
	if !strings.Contains(body, local) || strings.Contains(body, utc) {
		t.Fatalf("Issued column must read %q (local), never %q (UTC): %s", local, utc, body)
	}
}

// ut-docs#3300: an explicit ?from=/?to= range is a range of LOCAL days —
// both ends convert to UTC, so a UTC+3 shop's "1 October" covers
// 2026-09-30T21:00Z up to (not including) 2026-10-01T21:00Z, in the list,
// the totals and the accountant CSV alike.
func TestGetInvoices_ExplicitRangeIsLocalDays(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	plus3 := time.FixedZone("UTC+3", 3*3600)
	pinInvoiceClock(t, time.Date(2026, 10, 2, 9, 0, 0, 0, time.UTC), plus3)
	mux, dp := newInvoiceTestDeps(t)
	setSeller(t, dp)
	plantInvoice(t, dp, "R003", "Before Day", "2026-09-30T20:59:59Z") // 23:59:59 local, 30 Sep
	plantInvoice(t, dp, "R004", "Day Start", "2026-09-30T21:00:00Z")  // 00:00 local, 1 Oct
	plantInvoice(t, dp, "R005", "Day End", "2026-10-01T20:59:59Z")    // 23:59:59 local, 1 Oct
	plantInvoice(t, dp, "R006", "Next Day", "2026-10-01T21:00:00Z")   // 00:00 local, 2 Oct

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/invoices?from=2026-10-01&to=2026-10-01", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, in := range []string{"Day Start", "Day End"} {
		if !strings.Contains(body, in) {
			t.Errorf("%q was issued on local 1 October and must be listed", in)
		}
	}
	for _, out := range []string{"Before Day", "Next Day"} {
		if strings.Contains(body, out) {
			t.Errorf("%q was not issued on local 1 October and must not be listed", out)
		}
	}
	if !strings.Contains(body, `name="from" value="2026-10-01"`) || !strings.Contains(body, `name="to" value="2026-10-01"`) {
		t.Errorf("the pickers must keep showing the local dates the user chose: %s", body)
	}

	from, to := invoiceUTCBounds("2026-10-01", "2026-10-01")
	net, _, gross, err := data.NewInvoiceRepo(dp.Db).Totals(context.Background(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	if net != 200 || gross != 240 {
		t.Errorf("totals for local 1 October = net %d gross %d, want the two in-day invoices (200 / 240)", net, gross)
	}

	csvRec := httptest.NewRecorder()
	mux.ServeHTTP(csvRec, httptest.NewRequest(http.MethodGet, "/api/invoices/export?from=2026-10-01&to=2026-10-01", nil))
	if csvRec.Code != http.StatusOK {
		t.Fatalf("export: expected 200, got %d: %s", csvRec.Code, csvRec.Body.String())
	}
	csvBody := csvRec.Body.String()
	if !strings.Contains(csvBody, "Day Start") || !strings.Contains(csvBody, "Day End") ||
		strings.Contains(csvBody, "Before Day") || strings.Contains(csvBody, "Next Day") {
		t.Errorf("the CSV must cover the same local day as the list: %s", csvBody)
	}
}

func TestInvoiceUTCBounds(t *testing.T) {
	plus1 := time.FixedZone("UTC+1", 3600)
	minus10 := time.FixedZone("UTC-10", -10*3600)
	cases := []struct {
		name             string
		loc              *time.Location
		from, to         string
		wantFrom, wantTo string
	}{
		{"east of UTC", plus1, "2026-10-01", "2026-10-31", "2026-09-30T23:00:00Z", "2026-10-31T22:59:59Z"},
		{"west of UTC", minus10, "2026-10-01", "2026-10-01", "2026-10-01T10:00:00Z", "2026-10-02T09:59:59Z"},
		{"open ends stay open", plus1, "", "", "", ""},
		{"non-date input passes through", plus1, "2026-10-01T05:00:00Z", "garbage", "2026-10-01T05:00:00Z", "garbage"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pinInvoiceClock(t, time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC), c.loc)
			gotFrom, gotTo := invoiceUTCBounds(c.from, c.to)
			if gotFrom != c.wantFrom || gotTo != c.wantTo {
				t.Fatalf("invoiceUTCBounds(%q, %q) = (%q, %q), want (%q, %q)", c.from, c.to, gotFrom, gotTo, c.wantFrom, c.wantTo)
			}
		})
	}
	// A DST day is 23 or 25 hours long: the bound must be the NEXT local
	// midnight, not "+24h". Skipped where the zone database is missing.
	if london, err := time.LoadLocation("Europe/London"); err == nil {
		pinInvoiceClock(t, time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC), london)
		// 25 Oct 2026: clocks go back at 02:00 BST, so the day runs
		// 2026-10-24T23:00Z .. 2026-10-26T00:00Z (exclusive).
		gotFrom, gotTo := invoiceUTCBounds("2026-10-25", "2026-10-25")
		if gotFrom != "2026-10-24T23:00:00Z" || gotTo != "2026-10-25T23:59:59Z" {
			t.Fatalf("DST day bounds = (%q, %q), want (2026-10-24T23:00:00Z, 2026-10-25T23:59:59Z)", gotFrom, gotTo)
		}
	}
}
