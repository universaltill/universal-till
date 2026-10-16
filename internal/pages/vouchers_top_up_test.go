package pages

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/print"
)

// Top up a gift voucher as a sale (ut-docs#4252): the voucher detail page's
// Top up section posts to POST /vouchers/{id}/top-up, which adds a top-up
// line to the CASHIER basket (d.Engine); paying the sale credits the
// voucher, and the receipt (screen, print, reprint) and journal show it.

// newTopUpTestDeps is the gift-voucher POS harness plus the vouchers page,
// a real auth service with a seeded manager, and an active gift voucher
// catalog item the top-up line is based on (resolved by its item-id code,
// as the real resolver does).
func newTopUpTestDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newGiftVoucherPOSDeps(t)
	if _, err := dp.Db.Exec(`INSERT INTO users(id, username, display_name, role) VALUES ('m1', 'm1', 'Manager', 'manager')`); err != nil {
		t.Fatalf("seed manager: %v", err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO items (id, sku, name, base_price, is_active, item_type, stock_untracked) VALUES ('itm-gv', 'GV', 'Gift voucher', 0, 1, 'gift_voucher', 1)`); err != nil {
		t.Fatalf("seed gift voucher item: %v", err)
	}
	resolver := stubResolver{
		data.ItemIDCode("itm-gv"): {SKU: "GV", Name: "Gift voucher", Qty: 1, ItemID: "itm-gv", TaxRateBP: 700, ItemType: "gift_voucher"},
		"PLAIN":                   {SKU: "PLAIN", Name: "Plain Item", Qty: 1, PriceCents: 200, ItemID: "itm-plain2", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)
	engine.SetChargePolicyAsker(&pluginChargePolicyAsker{db: dp.Db})
	dp.Engine = engine
	dp.AuthSvc = auth.NewService(dp.Db)
	registerVouchers(mux, dp)
	registerJournal(mux, dp)
	return mux, dp
}

func vTopUp(t *testing.T, mux *http.ServeMux, id, amount string, user *auth.User, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"amount": {amount}}
	req := httptest.NewRequest(http.MethodPost, "/vouchers/"+url.PathEscape(id)+"/top-up?lang=en", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func topUpSeedVoucher(t *testing.T, dp *common.Deps, id string, balance int64) {
	t.Helper()
	if _, err := pos.CompleteSale(context.Background(), dp.Db, pos.SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: true, AllowNegativeInventory: true,
		VoucherIssues: []pos.VoucherIssueInput{{VoucherID: id, HolderLabel: "Sam", Amount: money.FromMinor(balance)}},
		Payments:      []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(balance)}},
	}); err != nil {
		t.Fatalf("issue %s: %v", id, err)
	}
}

func TestVoucherTopUp_ForbiddenForCashierAndAnonymous(t *testing.T) {
	mux, dp := newTopUpTestDeps(t)
	topUpSeedVoucher(t, dp, "GS-1", 1000)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "Cash"}
	for _, u := range []*auth.User{nil, &cashier} {
		if rec := vTopUp(t, mux, "GS-1", "25", u, true); rec.Code != http.StatusForbidden {
			t.Fatalf("POST as %v = %d, want 403", u, rec.Code)
		}
	}
	if n := len(dp.Engine.Lines()); n != 0 {
		t.Fatalf("a refused top-up added %d line(s)", n)
	}
	// The section is not offered to a cashier either.
	body := vGet(t, mux, "/vouchers/GS-1?lang=en", &cashier, false).Body.String()
	if strings.Contains(body, "/top-up") {
		t.Fatal("a cashier must not see the Top up form")
	}
}

func TestVoucherTopUp_AddsLineToCashierBasket(t *testing.T) {
	mux, dp := newTopUpTestDeps(t)
	topUpSeedVoucher(t, dp, "GS-1", 1000)
	dp.KioskEngine = pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, stubResolver{})

	// The detail page offers the section.
	page := vGet(t, mux, "/vouchers/GS-1?lang=en", &vManager, false).Body.String()
	for _, want := range []string{httpx.T("en", "vouchers.top_up.title"), `hx-post="/vouchers/GS-1/top-up"`, `name="amount"`, httpx.T("en", "vouchers.top_up.add")} {
		if !strings.Contains(page, want) {
			t.Fatalf("detail page missing %q", want)
		}
	}

	rec := vTopUp(t, mux, "GS-1", "25,50", &vManager, true)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/" {
		t.Fatalf("top-up = %d HX-Redirect %q (%s)", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	lines := dp.Engine.Lines()
	if len(lines) != 1 {
		t.Fatalf("basket = %+v, want one top-up line", lines)
	}
	l := lines[0]
	if l.GiftVoucher == nil || !l.GiftVoucher.TopUp || l.GiftVoucher.Code != "GS-1" || l.GiftVoucher.HolderLabel != "Sam" || l.PriceCents.Minor() != 2550 || l.ItemID != "itm-gv" {
		t.Fatalf("line = %+v voucher %+v", l, l.GiftVoucher)
	}
	if n := len(dp.KioskEngine.Lines()); n != 0 {
		t.Fatalf("the top-up landed in the kiosk basket: %d lines", n)
	}
	// Nothing is credited until the sale is paid.
	if v, _ := data.NewPOSRepo(dp.Db).GetVoucherBalance(context.Background(), nil, "GS-1"); v.BalanceMinor != 1000 {
		t.Fatalf("balance moved before payment: %d", v.BalanceMinor)
	}
	// A second top-up of the same voucher in this basket is refused inline.
	rec = vTopUp(t, mux, "GS-1", "5", &vManager, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), httpx.T("en", "vouchers.top_up.error.in_sale")) {
		t.Fatalf("duplicate = %d %s", rec.Code, rec.Body.String())
	}
	// Plain (non-htmx) POST: 303 to the sale screen.
	topUpSeedVoucher(t, dp, "GS-2", 500)
	rec = vTopUp(t, mux, "GS-2", "1", &vManager, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("plain POST = %d Location %q", rec.Code, rec.Header().Get("Location"))
	}
}

func TestVoucherTopUp_RefusalsInline(t *testing.T) {
	mux, dp := newTopUpTestDeps(t)
	ctx := context.Background()
	topUpSeedVoucher(t, dp, "GS-OK", 1000)
	topUpSeedVoucher(t, dp, "GS-VOID", 1000)
	if _, err := dp.Db.Exec(`UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-VOID'`); err != nil {
		t.Fatal(err)
	}
	if _, err := pos.CompleteSale(ctx, dp.Db, pos.SaleInput{
		SaleType: "sale", Currency: "GBP", TaxInclusive: true,
		VoucherIssues: []pos.VoucherIssueInput{{VoucherID: "GS-SP", Amount: money.FromMinor(1000), Purpose: data.VoucherPurposeSingle, VATRateBasisPoints: 2000}},
		Payments:      []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(1000)}},
	}); err != nil {
		t.Fatal(err)
	}
	maxMajor := httpx.FormatMajorPlain(pos.MaxVoucherIssueAmount.Minor(), 2)
	cases := []struct {
		name, id, amount string
		code             int
		key              string
	}{
		{"unknown", "GS-NOPE", "5", http.StatusNotFound, "vouchers.error.not_found"},
		{"void", "GS-VOID", "5", http.StatusConflict, "vouchers.top_up.not_void"},
		{"single purpose", "GS-SP", "5", http.StatusConflict, "vouchers.top_up.not_single"},
		{"zero", "GS-OK", "0", http.StatusBadRequest, "vouchers.top_up.error.amount"},
		{"garbage", "GS-OK", "abc", http.StatusBadRequest, "vouchers.top_up.error.amount"},
		{"over ceiling", "GS-OK", maxMajor, http.StatusBadRequest, "vouchers.top_up.error.amount"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := vTopUp(t, mux, tc.id, tc.amount, &vManager, true)
			if rec.Code != tc.code {
				t.Fatalf("code = %d, want %d (%s)", rec.Code, tc.code, rec.Body.String())
			}
			want := httpx.T("en", tc.key)
			if i := strings.Index(want, "%"); i >= 0 {
				want = want[:i]
			}
			if !strings.Contains(rec.Body.String(), html.EscapeString(want)) || !strings.Contains(rec.Body.String(), `role="alert"`) {
				t.Fatalf("body = %s, want the inline %s message", rec.Body.String(), tc.key)
			}
		})
	}
	if n := len(dp.Engine.Lines()); n != 0 {
		t.Fatalf("refused top-ups added %d line(s)", n)
	}

	// No active gift voucher item: an inline message says so.
	if _, err := dp.Db.Exec(`UPDATE items SET is_active = 0 WHERE item_type = 'gift_voucher'`); err != nil {
		t.Fatal(err)
	}
	rec := vTopUp(t, mux, "GS-OK", "5", &vManager, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), httpx.T("en", "vouchers.top_up.error.no_item")) {
		t.Fatalf("no item = %d %s", rec.Code, rec.Body.String())
	}
}

func TestVoucherTopUp_ReplicaRefusedWithNote(t *testing.T) {
	mux, dp := newTopUpTestDeps(t)
	topUpSeedVoucher(t, dp, "GS-1", 1000)
	if err := dp.Settings.Set(context.Background(), "sync.primary_url", "http://main-till.local:8080"); err != nil {
		t.Fatal(err)
	}
	body := vGet(t, mux, "/vouchers/GS-1?lang=en", &vManager, false).Body.String()
	if !strings.Contains(body, httpx.T("en", "vouchers.top_up.replica_note")) || strings.Contains(body, "/top-up") {
		t.Fatalf("a replica must show the note and no Top up form")
	}
	rec := vTopUp(t, mux, "GS-1", "5", &vManager, true)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), httpx.T("en", "vouchers.top_up.replica_note")) {
		t.Fatalf("replica POST = %d %s", rec.Code, rec.Body.String())
	}
	if n := len(dp.Engine.Lines()); n != 0 {
		t.Fatalf("a replica top-up added %d line(s)", n)
	}
}

// End to end: page → basket → tender → voucher credited, receipt on screen,
// printed receipt (first print = reprint), journal, history.
func TestVoucherTopUp_TenderCreditsAndReceipts(t *testing.T) {
	mux, dp := newTopUpTestDeps(t)
	ctx := context.Background()
	repo := data.NewPOSRepo(dp.Db)
	topUpSeedVoucher(t, dp, "GS-1", 1000)

	if rec := vTopUp(t, mux, "GS-1", "25", &vManager, true); rec.Code != http.StatusOK {
		t.Fatalf("top-up: %d %s", rec.Code, rec.Body.String())
	}
	if rec := posPostForm(mux, "/api/pos/scan", "code=PLAIN"); rec.Code != http.StatusOK {
		t.Fatalf("scan: %d", rec.Code)
	}
	rec := cashTender(mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	screen := rec.Body.String()
	for _, want := range []string{httpx.T("en", "receipt.vouchers.top_up"), "GS-1", "£25.00", "£35.00"} {
		if !strings.Contains(screen, want) {
			t.Fatalf("on-screen receipt missing %q:\n%s", want, screen)
		}
	}

	v, err := repo.GetVoucherBalance(ctx, nil, "GS-1")
	if err != nil {
		t.Fatal(err)
	}
	if v.BalanceMinor != 3500 || v.OriginalAmountMinor != 1000 {
		t.Fatalf("voucher after top-up sale = %+v, want balance 3500, original 1000", v)
	}
	var saleID, receiptNo string
	var subtotal, tax, total, vit int64
	if err := dp.Db.QueryRow(`SELECT id, receipt_no, subtotal, tax_total, total, voucher_issue_total FROM sales WHERE voucher_issue_total > 0 AND id IN (SELECT sale_id FROM voucher_transactions WHERE type = 'top_up')`).
		Scan(&saleID, &receiptNo, &subtotal, &tax, &total, &vit); err != nil {
		t.Fatalf("top-up sale: %v", err)
	}
	// The plain item (2.00 + 20% exclusive) is the only taxed figure.
	if subtotal != 200 || tax != 40 || vit != 2500 || total != 2740 {
		t.Fatalf("sale = subtotal %d tax %d vit %d total %d, want 200/40/2500/2740", subtotal, tax, vit, total)
	}
	if n := countRows(t, dp, `SELECT COUNT(*) FROM sale_lines WHERE sale_id = '`+saleID+`'`); n != 1 {
		t.Fatalf("a top-up is never a sale line: %d lines", n)
	}

	// Journal: the sale detail carries the top-up, flagged.
	detail, found, err := repo.GetSaleDetailByID(ctx, saleID)
	if err != nil || !found {
		t.Fatalf("sale detail: %v %v", found, err)
	}
	if len(detail.VoucherIssues) != 1 || !detail.VoucherIssues[0].TopUp || detail.VoucherIssues[0].VoucherID != "GS-1" || detail.VoucherIssues[0].Amount != 2500 {
		t.Fatalf("sale detail voucher issues = %+v", detail.VoucherIssues)
	}

	// The journal page labels it as a top-up.
	jr := vGet(t, mux, "/journal/"+receiptNo+"?lang=en", &vManager, false).Body.String()
	if !strings.Contains(jr, `data-testid="journal-voucher-issue"`) || !strings.Contains(jr, httpx.T("en", "receipt.vouchers.top_up")) {
		t.Fatalf("journal must show the top-up:\n%s", jr)
	}

	// Printed receipt (built from the database — the same for a reprint).
	doc, err := buildReceiptDoc(ctx, dp, receiptNo)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Vouchers) != 1 {
		t.Fatalf("printed vouchers = %+v", doc.Vouchers)
	}
	pv := doc.Vouchers[0]
	if pv.Label != httpx.T("en", "receipt.vouchers.top_up") || pv.Code != "GS-1" || !strings.Contains(pv.Amount, "25.00") || !strings.Contains(pv.Balance, "35.00") || pv.Void != "" {
		t.Fatalf("printed top-up = %+v", pv)
	}
	if out := print.Render(doc); !bytes.Contains(out, []byte("35.00")) {
		t.Fatal("rendered receipt must print the new balance")
	}
	// A later redemption doesn't change what the reprint says.
	if err := repo.DebitVoucherForRedemption(ctx, nil, "GS-1", 500, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordVoucherTransaction(ctx, nil, data.VoucherTransaction{ID: "r-later", VoucherID: "GS-1", SaleID: "other", Type: "redemption", AmountMinor: 500, CreatedAt: "2099-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	doc, _ = buildReceiptDoc(ctx, dp, receiptNo)
	if len(doc.Vouchers) != 1 || !strings.Contains(doc.Vouchers[0].Balance, "35.00") {
		t.Fatalf("reprint balance = %+v, want the balance right after the top-up", doc.Vouchers)
	}

	// The voucher's history shows a Top-up row with a link to the sale.
	hist := vGet(t, mux, "/vouchers/GS-1?lang=en", &vManager, false).Body.String()
	if !strings.Contains(hist, httpx.T("en", "vouchers.history.type.top_up")) || !strings.Contains(hist, "+£25.00") || !strings.Contains(hist, `href="/journal/`+receiptNo+`"`) {
		t.Fatalf("history missing the top-up row:\n%s", hist)
	}
}

// LAN-sync journal: a TopUp entry on SaleDetail.VoucherIssues reconstructs
// a top-up on replay (the primary credits its existing voucher) instead of
// trying to issue the number again.
func TestApplyJournal_TopUpMapsThrough(t *testing.T) {
	_, replicaDp := newSyncSalesTestDeps(t)
	_, primaryDp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	for _, dp := range []*common.Deps{replicaDp, primaryDp} {
		topUpSeedVoucher(t, dp, "GS-LAN", 1000)
	}
	if _, err := pos.CompleteSale(ctx, replicaDp.Db, pos.SaleInput{
		SaleType: "sale", SaleID: "lan-top-1", ReceiptNo: "T2-TOP1", Currency: "GBP", TaxInclusive: true, CashierID: "user1",
		VoucherIssues: []pos.VoucherIssueInput{{VoucherID: "GS-LAN", Amount: money.FromMinor(700), TopUp: true}},
		Payments:      []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(700)}},
	}); err != nil {
		t.Fatalf("replica top-up: %v", err)
	}
	j, found, err := buildJournal(ctx, data.NewPOSRepo(replicaDp.Db), "T2-TOP1")
	if err != nil || !found {
		t.Fatalf("buildJournal: %v %v", found, err)
	}
	if len(j.Sale.VoucherIssues) != 1 || !j.Sale.VoucherIssues[0].TopUp {
		t.Fatalf("journal voucher issues = %+v, want one top-up", j.Sale.VoucherIssues)
	}
	if applied, reason, err := applyJournal(ctx, primaryDp, "till-2", j); err != nil || !applied {
		t.Fatalf("applyJournal: applied=%v reason=%q err=%v", applied, reason, err)
	}
	if v, _ := data.NewPOSRepo(primaryDp.Db).GetVoucherBalance(ctx, nil, "GS-LAN"); v.BalanceMinor != 1700 {
		t.Fatalf("primary balance after replay = %d, want 1700", v.BalanceMinor)
	}
}

// Review M1 (ut-docs#4252): a top-up only enters a sale through the gated
// POST /vouchers/{id}/top-up basket line. The raw /api/pos/tender body's
// issue_vouchers must not carry top_up — that would let any session credit
// a voucher with no manager, and an additional till credit its local
// mirror and journal the credit to the main till.
func TestTender_TopUpFromBodyRefused(t *testing.T) {
	mux, dp := newTopUpTestDeps(t)
	topUpSeedVoucher(t, dp, "GS-B1", 1000)
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"cash","amount":2500}],"issue_vouchers":[{"amount":2500,"code":"GS-B1","top_up":true}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("tender with a body top_up: %d %s, want 400", rec.Code, rec.Body.String())
	}
	v, err := data.NewPOSRepo(dp.Db).GetVoucherBalance(context.Background(), nil, "GS-B1")
	if err != nil {
		t.Fatal(err)
	}
	if v.BalanceMinor != 1000 {
		t.Fatalf("balance after a refused body top-up = %d, want 1000", v.BalanceMinor)
	}
}

// Review m1: a top-up that fails at tender (the voucher was voided, or
// topped up past the ceiling elsewhere, while the line sat in the basket)
// gets its own toast telling the cashier to remove the line — not the
// redemption wording.
func TestClassifyTenderError_TopUpRefused(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("voucher %q: %w: %w", "GS-1", data.ErrVoucherTopUpRefused, data.ErrVoucherNotActive),
		fmt.Errorf("voucher %q: %w: %w", "GS-1", data.ErrVoucherTopUpRefused, data.ErrVoucherNotMultiPurpose),
		fmt.Errorf("voucher %q: %w: %w", "GS-1", data.ErrVoucherTopUpRefused, data.ErrVoucherTopUpOverCeiling),
		fmt.Errorf("voucher %q: %w: %w", "GS-1", data.ErrVoucherTopUpRefused, data.ErrVoucherNotFound),
	} {
		if got := classifyTenderError(err); got != "pos.toast.voucher_top_up_failed" {
			t.Errorf("classifyTenderError(%v) = %q, want pos.toast.voucher_top_up_failed", err, got)
		}
	}
}

// Review m1, end to end: the voucher is voided while its top-up line sits
// in the basket; the tender is refused with the top-up toast and nothing
// is credited.
func TestVoucherTopUp_VoidedBeforeTenderToast(t *testing.T) {
	mux, dp := newTopUpTestDeps(t)
	topUpSeedVoucher(t, dp, "GS-V1", 1000)
	if rec := vTopUp(t, mux, "GS-V1", "5", &vManager, true); rec.Code != http.StatusOK {
		t.Fatalf("top-up: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := dp.Db.Exec(`UPDATE vouchers SET status = 'void', balance = 0 WHERE id = 'GS-V1'`); err != nil {
		t.Fatal(err)
	}
	// A refused tender re-renders the basket (200) with an error toast.
	rec := cashTender(mux)
	if want := httpx.T("en", "pos.toast.voucher_top_up_failed"); !strings.Contains(html.UnescapeString(rec.Body.String()), want) {
		t.Fatalf("tender refusal does not carry the top-up toast %q: %d %s", want, rec.Code, rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM voucher_transactions WHERE voucher_id = 'GS-V1' AND type = 'top_up'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("refused tender recorded %d top-up rows (err %v)", n, err)
	}
}
