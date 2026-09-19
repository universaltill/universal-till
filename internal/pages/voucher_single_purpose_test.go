package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Single-purpose vouchers (ut-docs#1037) at the HTTP / report / sync
// boundaries: GET /api/vouchers/{id} exposes purpose, POST
// /api/vouchers/{id}/redeem is the single-purpose-only redemption,
// /api/pos/tender's issue_vouchers carries purpose + vat_rate_bp, the
// Z-report bands the issue at its rate, and the LAN-sync journal replays it
// as the same kind.

func spSeedAPIVoucher(t *testing.T, repo *data.POSRepo, id string, amount int64, rateBP int) {
	t.Helper()
	if err := repo.CreateVoucher(t.Context(), nil, data.Voucher{
		ID: id, HolderLabel: "Sample Holder", OriginalAmountMinor: amount,
		BalanceMinor: amount, Currency: "EUR", Purpose: data.VoucherPurposeSingle,
		IssueVATRateBP: &rateBP, CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed voucher %s: %v", id, err)
	}
}

type spEnvelope struct {
	Data  *data.Voucher `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func spDecode(t *testing.T, rec *httptest.ResponseRecorder) spEnvelope {
	t.Helper()
	var env spEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope %q: %v", rec.Body.String(), err)
	}
	return env
}

func spRedeem(mux *http.ServeMux, id, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/vouchers/"+id+"/redeem", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestVoucherAPI_BalanceQueryExposesPurpose(t *testing.T) {
	mux, repo := newVoucherTestMux(t)
	spSeedAPIVoucher(t, repo, "GS-SP-API", 2500, 1900)
	if err := repo.CreateVoucher(t.Context(), nil, data.Voucher{
		ID: "GS-MP-API", OriginalAmountMinor: 1500, BalanceMinor: 1500, Currency: "EUR",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed multi-purpose: %v", err)
	}
	for id, want := range map[string]string{
		"GS-SP-API": `"purpose":"single_purpose"`,
		"GS-MP-API": `"purpose":"multi_purpose"`,
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/vouchers/"+id, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("GET /api/vouchers/%s = %d %s, want 200 containing %s", id, rec.Code, rec.Body.String(), want)
		}
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/vouchers/GS-SP-API", nil))
	if !strings.Contains(rec.Body.String(), `"issue_vat_rate_bp":1900`) {
		t.Fatalf("single-purpose payload lacks the issue rate: %s", rec.Body.String())
	}
}

func TestVoucherAPI_RedeemSinglePurpose(t *testing.T) {
	mux, repo := newVoucherTestMux(t)
	spSeedAPIVoucher(t, repo, "GS-SP-RD", 2500, 1900)

	rec := spRedeem(mux, "GS-SP-RD", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST redeem = %d: %s", rec.Code, rec.Body.String())
	}
	env := spDecode(t, rec)
	if env.Error != nil || env.Data == nil {
		t.Fatalf("envelope = %s, want data and null error", rec.Body.String())
	}
	if env.Data.ID != "GS-SP-RD" || env.Data.BalanceMinor != 0 || env.Data.Status != "redeemed" || env.Data.Purpose != data.VoucherPurposeSingle {
		t.Fatalf("redeemed voucher = %+v, want balance 0 / redeemed / single_purpose", *env.Data)
	}
	v, err := repo.GetVoucherBalance(t.Context(), nil, "GS-SP-RD")
	if err != nil || v.BalanceMinor != 0 || v.Status != "redeemed" {
		t.Fatalf("persisted voucher = %+v (err %v)", v, err)
	}

	// Second tap: already redeemed.
	rec = spRedeem(mux, "GS-SP-RD", `{}`)
	if env := spDecode(t, rec); rec.Code != http.StatusConflict || env.Error == nil || env.Error.Code != "voucher_not_active" {
		t.Fatalf("second redeem = %d %s, want 409 voucher_not_active", rec.Code, rec.Body.String())
	}
}

func TestVoucherAPI_RedeemRefusesMultiPurposeAndUnknown(t *testing.T) {
	mux, repo := newVoucherTestMux(t)
	if err := repo.CreateVoucher(t.Context(), nil, data.Voucher{
		ID: "GS-MP-RD", OriginalAmountMinor: 1500, BalanceMinor: 1500, Currency: "EUR",
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed multi-purpose: %v", err)
	}
	rec := spRedeem(mux, "GS-MP-RD", `{}`)
	if env := spDecode(t, rec); rec.Code != http.StatusConflict || env.Error == nil || env.Error.Code != "voucher_not_single_purpose" {
		t.Fatalf("multi-purpose redeem = %d %s, want 409 voucher_not_single_purpose", rec.Code, rec.Body.String())
	}
	v, err := repo.GetVoucherBalance(t.Context(), nil, "GS-MP-RD")
	if err != nil || v.BalanceMinor != 1500 || v.Status != "active" {
		t.Fatalf("multi-purpose voucher touched: %+v (err %v)", v, err)
	}

	rec = spRedeem(mux, "GS-NOPE", `{}`)
	if env := spDecode(t, rec); rec.Code != http.StatusNotFound || env.Error == nil || env.Error.Code != "voucher_not_found" {
		t.Fatalf("unknown redeem = %d %s, want 404 voucher_not_found", rec.Code, rec.Body.String())
	}

	// An empty body is fine (sale_id is optional); a malformed one is not.
	spSeedAPIVoucher(t, repo, "GS-SP-EB", 500, 700)
	if rec := spRedeem(mux, "GS-SP-EB", ``); rec.Code != http.StatusOK {
		t.Fatalf("empty-body redeem = %d %s, want 200", rec.Code, rec.Body.String())
	}
	spSeedAPIVoucher(t, repo, "GS-SP-BB", 500, 700)
	if rec := spRedeem(mux, "GS-SP-BB", `{not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed-body redeem = %d %s, want 400", rec.Code, rec.Body.String())
	}
}

// /api/pos/tender: issue_vouchers[].purpose / vat_rate_bp reach
// pos.CompleteSale. The tender fixture is 20% EXCLUSIVE, so a 25.00
// single-purpose voucher costs the customer 30.00 (tax 5.00 at issue).
func TestPOSTender_SinglePurposeVoucherIssue(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)

	rec := postTenderJSON(t, mux, `{"payments":[{"method":"cash","amount":3000}],"issue_vouchers":[{"amount":2500,"code":"GS-SP-T","purpose":"single_purpose","vat_rate_bp":2000}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("single-purpose tender failed: code %d body %s", rec.Code, rec.Body.String())
	}
	var purpose string
	var rate *int
	if err := dp.Db.QueryRow(`SELECT purpose, issue_vat_rate_bp FROM vouchers WHERE id = 'GS-SP-T'`).Scan(&purpose, &rate); err != nil {
		t.Fatalf("voucher row after issue: %v", err)
	}
	if purpose != data.VoucherPurposeSingle || rate == nil || *rate != 2000 {
		t.Fatalf("voucher = purpose %q rate %v, want single_purpose/2000", purpose, rate)
	}
	var subtotal, taxTotal, total, vit int64
	if err := dp.Db.QueryRow(`SELECT subtotal, tax_total, total, voucher_issue_total FROM sales`).Scan(&subtotal, &taxTotal, &total, &vit); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if subtotal != 2500 || taxTotal != 500 || total != 3000 || vit != 0 {
		t.Fatalf("sale figures subtotal=%d tax=%d total=%d voucher_issue_total=%d, want 2500/500/3000/0", subtotal, taxTotal, total, vit)
	}

	// Validation at the boundary: an unknown purpose or an out-of-range
	// rate is a 400 before any DB work.
	for _, body := range []string{
		`{"payments":[{"method":"cash","amount":100}],"issue_vouchers":[{"amount":100,"purpose":"gift"}]}`,
		`{"payments":[{"method":"cash","amount":100}],"issue_vouchers":[{"amount":100,"purpose":"single_purpose","vat_rate_bp":-1}]}`,
		`{"payments":[{"method":"cash","amount":100}],"issue_vouchers":[{"amount":100,"purpose":"single_purpose","vat_rate_bp":10001}]}`,
	} {
		if rec := postTenderJSON(t, mux, body); rec.Code != http.StatusBadRequest {
			t.Fatalf("tender %s = %d, want 400", body, rec.Code)
		}
	}
	var count int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM vouchers`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("vouchers after refused tenders = %d (err %v), want 1", count, err)
	}
}

// The day-close Z-report bands a single-purpose issue at its own rate —
// the voucher has no sale_lines row, so without the dedicated read the
// band would miss it while TaxNet carried it, breaking sum(band.Tax) ==
// TaxNet. 25.00 @19% inclusive alongside a 15.00 multi-purpose voucher:
// one 19% band 2101/399/2500; Net 4000 (both vouchers are in total) with
// the multi-purpose 1500 reconciled by the GUTSCHEINE section, not a band.
func TestEODTaxBands_SinglePurposeVoucherIssueThroughCompleteSale(t *testing.T) {
	d := etbOpenDB(t, "eod-bands-single-purpose.db")
	rate := 1900
	day := etbCompleteSale(t, d, pos.SaleInput{
		SaleType: "sale", Currency: "EUR", TaxInclusive: true,
		AllowNegativeInventory: true,
		VoucherIssues: []pos.VoucherIssueInput{
			{VoucherID: "GS-SP-EOD", Amount: money.FromMinor(2500), Purpose: data.VoucherPurposeSingle, VATRateBasisPoints: rate},
			{VoucherID: "GS-MP-EOD", Amount: money.FromMinor(1500)},
		},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(4000)}},
	})

	rep := etbEndOfDay(t, d, day)
	if rep.TaxNet != 399 || rep.Net != 4000 {
		t.Fatalf("engine totals: TaxNet=%d Net=%d, want 399/4000", rep.TaxNet, rep.Net)
	}
	if len(rep.TaxBands) != 1 {
		t.Fatalf("expected 1 band, got %+v", rep.TaxBands)
	}
	if want := (data.TaxBand{RateBP: 1900, Net: 2101, Tax: 399, Gross: 2500}); rep.TaxBands[0] != want {
		t.Fatalf("band = %+v, want %+v", rep.TaxBands[0], want)
	}
	var sumTax, sumGross int64
	for _, b := range rep.TaxBands {
		sumTax += b.Tax
		sumGross += b.Gross
	}
	if sumTax != rep.TaxNet {
		t.Fatalf("sum of band tax %d != TaxNet %d", sumTax, rep.TaxNet)
	}
	// The voucher-day identity (eod_tax_bands.go header): sum(band.Gross)
	// == Net − vouchers issued — which only holds if the GUTSCHEINE section
	// counts the multi-purpose 1500 alone (the single-purpose 2500 is
	// inside the 19% band, not a liability).
	if rep.VouchersIssued != 1500 || rep.VouchersIssuedCount != 1 {
		t.Fatalf("GUTSCHEINE issued = %d (count %d), want the multi-purpose 1500 (count 1) only", rep.VouchersIssued, rep.VouchersIssuedCount)
	}
	if sumGross != rep.Net-rep.VouchersIssued {
		t.Fatalf("sum of band gross %d != Net %d − vouchers issued %d", sumGross, rep.Net, rep.VouchersIssued)
	}
}

// LAN-sync: a single-purpose voucher issued on a replica replays on the
// primary as single-purpose, with the same rate and the same sale totals
// (tax at issue included) — without purpose on the wire the primary would
// re-derive it as a 0% multi-purpose liability, land a different
// tax_total/total, and reject the entry as underpayment.
func TestApplyJournal_ReplicatesSinglePurposeVoucherIssue(t *testing.T) {
	_, replicaDp := newSyncSalesTestDeps(t)
	_, primaryDp := newSyncSalesTestDeps(t)
	ctx := context.Background()
	replicaRepo := data.NewPOSRepo(replicaDp.Db)

	if _, err := pos.CompleteSale(ctx, replicaDp.Db, pos.SaleInput{
		SaleType: "sale", SaleID: "lan-sp-1", ReceiptNo: "T2-SP01",
		Currency: "GBP", TaxInclusive: true, CashierID: "user1",
		AllowNegativeInventory: true,
		VoucherIssues: []pos.VoucherIssueInput{{
			VoucherID: "GS-LAN-SP", HolderLabel: "Sample Holder", Amount: money.FromMinor(2500),
			Purpose: data.VoucherPurposeSingle, VATRateBasisPoints: 2000,
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2500)}},
	}); err != nil {
		t.Fatalf("replica issue sale: %v", err)
	}
	j, found, err := buildJournal(ctx, replicaRepo, "T2-SP01")
	if err != nil || !found {
		t.Fatalf("buildJournal: found=%v err=%v", found, err)
	}
	if len(j.Sale.VoucherIssues) != 1 || j.Sale.VoucherIssues[0].Purpose != data.VoucherPurposeSingle ||
		j.Sale.VoucherIssues[0].VATRateBP == nil || *j.Sale.VoucherIssues[0].VATRateBP != 2000 {
		t.Fatalf("journal voucher issues = %+v, want single_purpose @ 2000", j.Sale.VoucherIssues)
	}
	applied, _, err := applyJournal(ctx, primaryDp, "till-2", j)
	if err != nil || !applied {
		t.Fatalf("applyJournal: applied=%v err=%v", applied, err)
	}
	var purpose string
	var rate *int
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT purpose, issue_vat_rate_bp FROM vouchers WHERE id = 'GS-LAN-SP'`).Scan(&purpose, &rate); err != nil {
		t.Fatalf("primary voucher: %v", err)
	}
	if purpose != data.VoucherPurposeSingle || rate == nil || *rate != 2000 {
		t.Fatalf("primary voucher = purpose %q rate %v, want single_purpose/2000", purpose, rate)
	}
	var rTax, rTotal, pTax, pTotal int64
	if err := replicaDp.Db.QueryRowContext(ctx, `SELECT tax_total, total FROM sales WHERE id = 'lan-sp-1'`).Scan(&rTax, &rTotal); err != nil {
		t.Fatalf("replica sale: %v", err)
	}
	if err := primaryDp.Db.QueryRowContext(ctx, `SELECT tax_total, total FROM sales WHERE id = 'lan-sp-1'`).Scan(&pTax, &pTotal); err != nil {
		t.Fatalf("primary sale: %v", err)
	}
	if rTax != pTax || rTotal != pTotal || pTax == 0 {
		t.Fatalf("totals drifted across replay: replica tax=%d total=%d, primary tax=%d total=%d", rTax, rTotal, pTax, pTotal)
	}
}

// spExclusiveTender posts one quick-tender (zero-amount payment, so the
// handler fills in what it DEMANDS) issuing a single-purpose voucher under
// the harness's default EXCLUSIVE pricing, and returns the persisted
// sale total / tax_total and the payment the handler filled in.
func spExclusiveTender(t *testing.T, serviceChargeBP int, body string) (total, taxTotal, paid int64) {
	t.Helper()
	mux, dp := newPOSTestDeps(t)
	if serviceChargeBP != 0 {
		dp.UpdateState(func(s *common.RuntimeState) { s.ServiceChargeRateBasisPoints = serviceChargeBP })
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: HTTP %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "does not cover the sale total") {
		t.Fatalf("tender refused its own demanded amount — the handler's total disagrees with pos.computeSaleTotals")
	}
	if err := dp.Db.QueryRow(`SELECT total, tax_total FROM sales`).Scan(&total, &taxTotal); err != nil {
		t.Fatalf("query sale: %v", err)
	}
	if err := dp.Db.QueryRow(`SELECT amount FROM payments`).Scan(&paid); err != nil {
		t.Fatalf("query payment: %v", err)
	}
	return total, taxTotal, paid
}

// Reviewer regression (ut-docs#1037): under EXCLUSIVE pricing the
// quick-tender handler must demand the single-purpose voucher's VAT too.
// It used to add the face value as if it were the multi-purpose 0%
// liability, so the demanded total came out short by exactly that VAT and
// CompleteSale — which taxes the issue via computeSaleTotals — rejected the
// handler's own figure, making the sale impossible to complete at all.
// Every pre-existing test on this path priced tax-inclusive, where the two
// happen to agree, which is why it was not caught.
func TestTenderHandler_SinglePurposeVoucherExclusivePricingDemandsItsVAT(t *testing.T) {
	// ABC: 100 @20% -> 120. Voucher 2500 @19% -> 2975. Total 3095, tax 495.
	total, taxTotal, paid := spExclusiveTender(t, 0,
		`{"payments":[{"method":"cash","amount":0}],"offline":true,`+
			`"issue_vouchers":[{"amount":2500,"code":"GS-SP-EXCL","purpose":"single_purpose","vat_rate_bp":1900}]}`)
	if total != 3095 || taxTotal != 495 {
		t.Fatalf("sale total=%d tax_total=%d, want 3095/495", total, taxTotal)
	}
	if paid != total {
		t.Fatalf("handler demanded %d but the sale total is %d — the quick-tender figure must equal what CompleteSale enforces", paid, total)
	}
}

// Same path with a SERVICE CHARGE: computeSaleTotals apportions the
// charge's tax across the single-purpose voucher's rate band as well as the
// lines', so the handler must weigh the same set or the two drift by a
// rounding unit and the sale is refused again.
func TestTenderHandler_SinglePurposeVoucherExclusiveWithServiceCharge(t *testing.T) {
	total, taxTotal, paid := spExclusiveTender(t, 1000,
		`{"payments":[{"method":"cash","amount":0}],"offline":true,`+
			`"issue_vouchers":[{"amount":2500,"code":"GS-SP-SC","purpose":"single_purpose","vat_rate_bp":700}]}`)
	if paid != total {
		t.Fatalf("handler demanded %d but the sale total is %d (service-charge apportionment drifted)", paid, total)
	}
	if taxTotal == 0 {
		t.Fatalf("tax_total must carry the voucher's VAT and the charge's, got 0")
	}
}

// The MULTI-PURPOSE path under the same exclusive pricing is unchanged: its
// face value is a 0% liability that rides on top of the taxed total and
// contributes no tax of its own.
func TestTenderHandler_MultiPurposeVoucherExclusivePricingUnchanged(t *testing.T) {
	// ABC: 100 @20% -> 120, plus the 2500 liability = 2620, tax still 20.
	total, taxTotal, paid := spExclusiveTender(t, 0,
		`{"payments":[{"method":"cash","amount":0}],"offline":true,`+
			`"issue_vouchers":[{"amount":2500,"code":"GS-MP-EXCL","purpose":"multi_purpose"}]}`)
	if total != 2620 || taxTotal != 20 {
		t.Fatalf("sale total=%d tax_total=%d, want 2620/20 (the 0%% liability adds no tax)", total, taxTotal)
	}
	if paid != total {
		t.Fatalf("handler demanded %d but the sale total is %d", paid, total)
	}
}
