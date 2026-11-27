package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#7134: POST /api/refund takes a manager PIN from a cashier without
// the Refunds permission only with the prompt's single-use approval, bound to
// the receipt, method, chosen payment, every line quantity, the refund total
// and what was already refunded. Same mechanism as #7085/#7096.

const refundBindCash = `[{"method":"cash","amount":240}]`
const refundBindCard = `[{"method":"demopay","amount":240}]`

type refundBindEnv struct {
	*refCardRig
	mgrID, cashierID string
	cashier, manager *auth.User
}

func refundBindRig(t *testing.T) refundBindEnv {
	t.Helper()
	elevationApprovals.reset()
	t.Cleanup(elevationApprovals.reset)
	rig := newRefCardRigAuth(t, func(n int) string { return fmt.Sprintf(`"pi_%d"`, n) }, "", true)
	mgrID, cashierID := newElevationTestPrincipals(t, rig.dp, "mgr-7134", "cashier-7134", gatePIN)
	return refundBindEnv{refCardRig: rig, mgrID: mgrID, cashierID: cashierID,
		cashier: &auth.User{ID: cashierID, Role: "cashier"},
		manager: &auth.User{ID: mgrID, Role: "manager"}}
}

// sellTwo records a sale of 2 x ABC (240 with tax) paid as payments.
func (e refundBindEnv) sellTwo(t *testing.T, payments string) string {
	t.Helper()
	if _, err := e.dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	e.sell(t, payments)
	var no string
	if err := e.dp.Db.QueryRow(`SELECT receipt_no FROM sales WHERE sale_type != 'return' ORDER BY rowid DESC LIMIT 1`).Scan(&no); err != nil {
		t.Fatal(err)
	}
	return no
}

// sellTwoLines records a sale with two lines (1 x ABC, 1 x DEF = 360 with
// tax) paid in cash, so qty_1 exists on the refund form.
func (e refundBindEnv) sellTwoLines(t *testing.T) string {
	t.Helper()
	if _, err := e.dp.Db.Exec(`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itm2','DEF','Pear',200,'tax_std',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.dp.Db.Exec(`INSERT INTO item_barcodes(barcode,item_id,is_primary) VALUES('DEF','itm2',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.dp.Db.Exec(`INSERT INTO inventory(id,item_id,variant_id,location_id,quantity,updated_at) VALUES('inv3','itm2',NULL,'loc_main',100000,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
	e.dp.Engine = pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, stubResolver{
		"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", TaxRateBP: 2000},
		"DEF": {SKU: "DEF", Name: "Pear", Qty: 1, PriceCents: 200, ItemID: "itm2", TaxRateBP: 2000},
	})
	for _, code := range []string{"ABC", "DEF"} {
		if _, err := e.dp.Engine.Scan(code); err != nil {
			t.Fatal(err)
		}
	}
	rec := postTender(t, e.mux, `{"payments":[{"method":"cash","amount":360}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("two-line tender = %d: %s", rec.Code, rec.Body.String())
	}
	var no string
	if err := e.dp.Db.QueryRow(`SELECT receipt_no FROM sales WHERE sale_type != 'return' ORDER BY rowid DESC LIMIT 1`).Scan(&no); err != nil {
		t.Fatal(err)
	}
	return no
}

func (e refundBindEnv) form(receipt, method string, qty0 string) url.Values {
	return url.Values{"receipt": {receipt}, "method": {method}, "qty_0": {qty0},
		"refund_attempt_id": {"attempt-7134"}, "offline": {"0"}}
}

func (e refundBindEnv) post(f url.Values, u *auth.User) *httptest.ResponseRecorder {
	return postFormHtmx(e.mux, "/api/refund", f, u)
}

func (e refundBindEnv) auditRefunds(t *testing.T) int {
	t.Helper()
	return e.count(t, `SELECT COUNT(*) FROM audit_log WHERE action='refund'`)
}

func (e refundBindEnv) noSideEffects(t *testing.T) {
	t.Helper()
	if n := returnCount(t, e.refCardRig); n != 0 {
		t.Fatalf("return sales = %d, want 0", n)
	}
	if n := len(e.refunds()); n != 0 {
		t.Fatalf("provider refunds = %d, want 0", n)
	}
	if rows := refundLegRowsByID(t, e.refCardRig); len(rows) != 0 {
		t.Fatalf("leg rows = %v, want none", rows)
	}
	if n := e.auditRefunds(t); n != 0 {
		t.Fatalf("refund audit rows = %d, want 0", n)
	}
}

// promptFor posts form as the cashier, asserts a prompt and returns its
// hidden fields with the manager PIN set.
func (e refundBindEnv) promptFor(t *testing.T, form url.Values) url.Values {
	t.Helper()
	rec := e.post(form, e.cashier)
	if !isElevationPrompt(rec) {
		t.Fatalf("want prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	h := promptHiddenFields(t, rec.Body.String())
	h.Set("override_pin", gatePIN)
	return h
}

func cloneValues(v url.Values) url.Values {
	out := url.Values{}
	for k, vs := range v {
		out[k] = append([]string(nil), vs...)
	}
	return out
}

func okRefund(rec *httptest.ResponseRecorder) bool {
	return rec.Code == http.StatusOK && !isElevationPrompt(rec) && strings.HasPrefix(rec.Header().Get("HX-Redirect"), "/journal/")
}

func TestRefundBind7134_CashierGetsPromptNothingRecorded(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwo(t, refundBindCard)
	rec := e.post(e.form(receipt, "demopay", "1"), e.cashier)
	if rec.Code != http.StatusOK || !isElevationPrompt(rec) {
		t.Fatalf("want 200 prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Retarget") != "#refund-msg" || rec.Header().Get("HX-Reswap") != "innerHTML" {
		t.Fatalf("prompt must land in #refund-msg: %v", rec.Header())
	}
	h := promptHiddenFields(t, rec.Body.String())
	if h.Get("approval") == "" || h.Get("receipt") != receipt || h.Get("method") != "demopay" || h.Get("qty_0") != "1" {
		t.Fatalf("hidden fields = %v", h)
	}
	body := rec.Body.String()
	if !strings.Contains(body, receipt) || !strings.Contains(body, "1.20") {
		t.Fatalf("summary must name the receipt and the 1.20 total: %s", body)
	}
	e.noSideEffects(t)
}

func TestRefundBind7134_SummaryUsesOnScreenMethodName(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwo(t, refundBindCash)
	rec := e.post(e.form(receipt, "cash", "1"), e.cashier)
	if want := "paid back by " + httpx.T("en", "tender.cash"); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("summary lacks %q: %s", want, rec.Body.String())
	}
	e.noSideEffects(t)
}

func TestRefundBind7134_UnchangedRetryRefundsOnceWithApprover(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwo(t, refundBindCard)
	retry := e.promptFor(t, e.form(receipt, "demopay", "1"))
	rec := e.post(retry, e.cashier)
	if !okRefund(rec) {
		t.Fatalf("approved retry: %d hx=%q: %s", rec.Code, rec.Header().Get("HX-Redirect"), rec.Body.String())
	}
	if n := returnCount(t, e.refCardRig); n != 1 {
		t.Fatalf("return sales = %d, want 1", n)
	}
	if n := len(e.refunds()); n != 1 {
		t.Fatalf("provider refunds = %d, want 1", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE action='refund' AND actor_id=? AND blocked_actor_id=?`, e.mgrID, e.cashierID); n != 1 {
		t.Fatalf("elevated refund audit rows = %d, want 1", n)
	}
}

func TestRefundBind7134_EditedFieldRefused(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwo(t, refundBindCash)
	other := e.sellTwo(t, refundBindCash)
	twoLeg := e.sellTwo(t, `[{"method":"demopay","amount":120},{"method":"demopay","amount":120}]`)
	ids := legPaymentIDs(t, e.refCardRig, twoLeg)
	cases := []struct {
		name   string
		base   url.Values
		tamper func(h url.Values)
	}{
		{"receipt", e.form(receipt, "cash", "1"), func(h url.Values) { h.Set("receipt", other) }},
		{"method", e.form(receipt, "cash", "1"), func(h url.Values) { h.Set("method", "demopay") }},
		{"refund_leg", func() url.Values { f := e.form(twoLeg, "demopay", "1"); f.Set("refund_leg", ids[0]); return f }(),
			func(h url.Values) { h.Set("refund_leg", ids[1]) }},
		{"qty_0", e.form(receipt, "cash", "1"), func(h url.Values) { h.Set("qty_0", "2") }},
		{"offline", e.form(receipt, "cash", "1"), func(h url.Values) { h.Set("offline", "1") }},
		{"refund_attempt_id", e.form(receipt, "cash", "1"), func(h url.Values) { h.Set("refund_attempt_id", "attempt-other") }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			retry := e.promptFor(t, c.base)
			c.tamper(retry)
			rec := e.post(retry, e.cashier)
			if !refused(rec) {
				t.Fatalf("edited retry not refused: %d %s", rec.Code, rec.Body.String())
			}
			e.noSideEffects(t)
		})
	}
	// The PIN was never looked at (>5 refusals would have locked the device):
	// a fresh prompt and the right PIN still succeed.
	fresh := e.promptFor(t, e.form(receipt, "cash", "1"))
	if rec := e.post(fresh, e.cashier); !okRefund(rec) {
		t.Fatalf("valid retry after refusals: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRefundBind7134_ReturnedStateChangedRefused(t *testing.T) {
	for _, tc := range []struct{ name, payments, method string }{
		{"cash", refundBindCash, "cash"},
		{"card_single_leg", refundBindCard, "demopay"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := refundBindRig(t)
			receipt := e.sellTwo(t, tc.payments)
			retry := e.promptFor(t, e.form(receipt, tc.method, "1"))
			// A manager refunds one of the two units in between.
			if rec := e.post(e.form(receipt, tc.method, "1"), e.manager); !okRefund(rec) {
				t.Fatalf("manager refund: %d %s", rec.Code, rec.Body.String())
			}
			wantRefunds := len(e.refunds())
			rec := e.post(retry, e.cashier)
			if !refused(rec) {
				t.Fatalf("stale approval not refused: %d %s", rec.Code, rec.Body.String())
			}
			if n := returnCount(t, e.refCardRig); n != 1 {
				t.Fatalf("return sales = %d, want 1", n)
			}
			if n := len(e.refunds()); n != wantRefunds {
				t.Fatalf("provider refunds %d -> %d: a second refund went out", wantRefunds, n)
			}
		})
	}
}

func TestRefundBind7134_ReplayRefusedBeforePIN(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwo(t, refundBindCard)
	retry := e.promptFor(t, e.form(receipt, "demopay", "1"))
	tok := retry.Get("approval")
	if rec := e.post(retry, e.cashier); !okRefund(rec) {
		t.Fatalf("first retry: %d %s", rec.Code, rec.Body.String())
	}
	elevationApprovals.mu.Lock()
	_, live := elevationApprovals.m[tok]
	elevationApprovals.mu.Unlock()
	if live {
		t.Fatalf("approval still live after use")
	}
	rec := e.post(retry, e.cashier)
	if !refused(rec) {
		t.Fatalf("replay not refused: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(e.refunds()); n != 1 {
		t.Fatalf("provider refunds = %d, want 1", n)
	}
}

func TestRefundBind7134_SecondLineQtyBound(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwoLines(t)
	base := e.form(receipt, "cash", "1")
	base.Set("qty_1", "0")
	retry := e.promptFor(t, base)
	if retry.Get("qty_1") != "0" {
		t.Fatalf("prompt did not carry qty_1: %v", retry)
	}
	retry.Set("qty_1", "1") // cashier adds line B behind the manager's back
	rec := e.post(retry, e.cashier)
	if !refused(rec) {
		t.Fatalf("qty_1 edit not refused: %d %s", rec.Code, rec.Body.String())
	}
	e.noSideEffects(t)
	// The unedited approval of the same shape still works.
	fresh := e.promptFor(t, base)
	if rec := e.post(fresh, e.cashier); !okRefund(rec) {
		t.Fatalf("valid two-line retry: %d %s", rec.Code, rec.Body.String())
	}
}

// The approval is spent when the PIN verifies, before the provider call. A
// declined refund leaves the binding unchanged, so only the consumed token
// stops the cashier replaying the same dialog without a manager.
func TestRefundBind7134_SpentApprovalRefusedAfterProviderDecline(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwo(t, refundBindCard)
	retry := e.promptFor(t, e.form(receipt, "demopay", "1"))
	e.setRefundError(&plugins.HandlerExitError{Code: 2, Stdout: []byte(`{"decline_code":"card_declined"}`)})
	rec := e.post(cloneValues(retry), e.cashier)
	if rec.Code != http.StatusPaymentRequired {
		t.Fatalf("declined approved retry = %d: %s", rec.Code, rec.Body.String())
	}
	refundErrOverrides.Delete(e.dp.Db)
	if n := returnCount(t, e.refCardRig); n != 0 {
		t.Fatalf("return sales = %d after a decline, want 0", n)
	}
	elevationApprovals.mu.Lock()
	_, live := elevationApprovals.m[retry.Get("approval")]
	elevationApprovals.mu.Unlock()
	if live {
		t.Fatalf("approval still live after the PIN was accepted")
	}
	want := len(e.refunds())
	rec = e.post(cloneValues(retry), e.cashier) // same unchanged form
	if !refused(rec) {
		t.Fatalf("replay of a spent, still-matching approval not refused: %d %s", rec.Code, rec.Body.String())
	}
	if n := returnCount(t, e.refCardRig); n != 0 {
		t.Fatalf("return sales = %d after replay, want 0", n)
	}
	if n := len(e.refunds()); n != want {
		t.Fatalf("provider refund calls %d -> %d on replay", want, n)
	}
}

func TestRefundBind7134_MissingForgedOtherUserExpiredRefusedBeforePIN(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwo(t, refundBindCash)
	good := e.promptFor(t, e.form(receipt, "cash", "1"))
	tok := good.Get("approval")
	cases := map[string]func(h url.Values) *auth.User{
		"missing": func(h url.Values) *auth.User { h.Del("approval"); return e.cashier },
		"garbage": func(h url.Values) *auth.User { h.Set("approval", "garbage"); return e.cashier },
		"suffix":  func(h url.Values) *auth.User { h.Set("approval", tok+"x"); return e.cashier },
		"other_user": func(h url.Values) *auth.User {
			return &auth.User{ID: "cashier-b-7134", Role: "cashier"}
		},
	}
	for name, mod := range cases {
		t.Run(name, func(t *testing.T) {
			h := cloneValues(good)
			u := mod(h)
			if rec := e.post(h, u); !refused(rec) {
				t.Fatalf("not refused: %d %s", rec.Code, rec.Body.String())
			}
			e.noSideEffects(t)
		})
	}
	t.Run("expired", func(t *testing.T) {
		elevationApprovals.mu.Lock()
		elevationApprovals.now = func() time.Time { return time.Now().Add(approvalTTL + time.Minute) }
		elevationApprovals.mu.Unlock()
		t.Cleanup(func() {
			elevationApprovals.mu.Lock()
			elevationApprovals.now = time.Now
			elevationApprovals.mu.Unlock()
		})
		if rec := e.post(cloneValues(good), e.cashier); !refused(rec) {
			t.Fatalf("expired not refused: %d %s", rec.Code, rec.Body.String())
		}
		e.noSideEffects(t)
	})
	// Wrong PINs behind a bad token must not tick the shared lockout.
	for i := 0; i < 6; i++ {
		h := cloneValues(good)
		h.Set("approval", "garbage")
		h.Set("override_pin", "000000")
		if rec := e.post(h, e.cashier); !refused(rec) {
			t.Fatalf("wrong-PIN attempt %d not refused: %d", i, rec.Code)
		}
	}
	fresh := e.promptFor(t, e.form(receipt, "cash", "1"))
	if rec := e.post(fresh, e.cashier); !okRefund(rec) {
		t.Fatalf("valid retry after refusals: %d %s", rec.Code, rec.Body.String())
	}
}

func TestRefundBind7134_WrongPINKeepsTokenThenRightPIN(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwo(t, refundBindCard)
	retry := e.promptFor(t, e.form(receipt, "demopay", "1"))
	tok := retry.Get("approval")
	wrong := cloneValues(retry)
	wrong.Set("override_pin", "000000")
	rec := e.post(wrong, e.cashier)
	if !isElevationPrompt(rec) || refused(rec) {
		t.Fatalf("wrong PIN should re-prompt with the PIN error: %d %s", rec.Code, rec.Body.String())
	}
	if got := promptHiddenFields(t, rec.Body.String()).Get("approval"); got != tok {
		t.Fatalf("approval changed after a wrong PIN: %q -> %q", tok, got)
	}
	e.noSideEffects(t)
	if rec := e.post(retry, e.cashier); !okRefund(rec) {
		t.Fatalf("right PIN: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(e.refunds()); n != 1 {
		t.Fatalf("provider refunds = %d, want 1", n)
	}
}

func TestRefundBind7134_ManagerNoPrompt(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwo(t, refundBindCash)
	rec := e.post(e.form(receipt, "cash", "1"), e.manager)
	if !okRefund(rec) {
		t.Fatalf("manager refund: %d %s", rec.Code, rec.Body.String())
	}
	elevationApprovals.mu.Lock()
	n := len(elevationApprovals.m)
	elevationApprovals.mu.Unlock()
	if n != 0 {
		t.Fatalf("manager refund minted %d approvals", n)
	}
	// UT_AUTH=off (no prompt, no PIN) stays covered by the existing
	// refund_*_test.go files, which all run with auth off.
}

func TestRefundBind7134_ValidationErrorBeforePrompt(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwo(t, refundBindCash)
	rec := e.post(e.form(receipt, "cash", "5"), e.cashier)
	if isElevationPrompt(rec) || rec.Code != http.StatusConflict {
		t.Fatalf("want 409 without a prompt, got %d prompt=%v: %s", rec.Code, isElevationPrompt(rec), rec.Body.String())
	}
	elevationApprovals.mu.Lock()
	n := len(elevationApprovals.m)
	elevationApprovals.mu.Unlock()
	if n != 0 {
		t.Fatalf("a refused input minted %d approvals", n)
	}
	e.noSideEffects(t)
}

func TestRefundBind7134_NonHTMXCashierRefusedBeforePIN(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwo(t, refundBindCash)
	f := e.form(receipt, "cash", "1")
	nonHTMX := func(pin string) *httptest.ResponseRecorder {
		f.Set("override_pin", pin)
		req := httptest.NewRequest(http.MethodPost, "/api/refund", strings.NewReader(f.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req = auth.WithUser(req, *e.cashier)
		rec := httptest.NewRecorder()
		e.mux.ServeHTTP(rec, req)
		return rec
	}
	// Six wrong PINs would lock the device if the PIN were looked at; the
	// correct one must still get the 403, not a refund.
	for i, pin := range []string{"000000", "000000", "000000", "000000", "000000", "000000", gatePIN} {
		rec := nonHTMX(pin)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "manager PIN required") {
			t.Fatalf("attempt %d: want 403, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}
	e.noSideEffects(t)
	fresh := e.promptFor(t, e.form(receipt, "cash", "1"))
	if rec := e.post(fresh, e.cashier); !okRefund(rec) {
		t.Fatalf("PIN looked at by the non-HTMX path (lockout?): %d %s", rec.Code, rec.Body.String())
	}
}

func TestRefundBind7134_CanonicalBindingDeterministic(t *testing.T) {
	guard := refundGuardState{returnedQtyByLine: map[string]float64{}}
	for i := 0; i < 20; i++ {
		guard.returnedQtyByLine[fmt.Sprintf("line-%02d", i)] = float64(i) + 0.5
	}
	detail := data.SaleDetail{ID: "sale-1", Currency: "EUR"}
	want := fmt.Sprint(refundApprovalExtras(detail, guard, 0, nil, nil))
	for i := 0; i < 50; i++ {
		if got := fmt.Sprint(refundApprovalExtras(detail, guard, 0, nil, nil)); got != want {
			t.Fatalf("extras differ between calls:\n%s\n%s", want, got)
		}
	}
	for in, out := range map[string]string{"": "0", "1.50": "1.5", " 2 ": "2", "1": "1"} {
		if got := refundQtyCanon(in); got != out {
			t.Errorf("refundQtyCanon(%q) = %q, want %q", in, got, out)
		}
	}
}

func TestRefundPage_NoInlineManagerPINField(t *testing.T) {
	e := refundBindRig(t)
	receipt := e.sellTwo(t, refundBindCash)
	req := httptest.NewRequest(http.MethodGet, "/refund/"+receipt, nil)
	req = auth.WithUser(req, *e.cashier)
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET refund page = %d: %s", rec.Code, rec.Body.String())
	}
	if regexp.MustCompile(`<input[^>]*name="manager_pin"`).MatchString(rec.Body.String()) {
		t.Fatalf("refund page still renders the inline manager PIN field")
	}
}
