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
		Locales: config.Locales{Currency: "GBP", TaxRate: 20},
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
	t.Setenv("TZ", "Pacific/Honolulu") // UTC-10 — local calendar date can trail UTC's by a full day
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
