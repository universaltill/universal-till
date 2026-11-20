package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#5416: every manual way of taking value off a sale (line discount
// raise, sale discount raise, tender JSON discount raise, New sale on a priced
// basket) needs a manager PIN and is audited, the same way #5411 gated a qty
// reduction. Discounting is gated on price_override, clearing on
// void_comp_waste.

const gatePIN = "445566"

type gateEnv struct {
	mux       *http.ServeMux
	dp        *common.Deps
	mgrID     string
	cashierID string
	cashier   *auth.User
	manager   *auth.User
}

func newGateEnv(t *testing.T) gateEnv {
	t.Helper()
	mux, dp := newPOSMuxRealSession(t)
	registerHoldAPI(mux, dp)
	mgrID, cashierID := newElevationTestPrincipals(t, dp, "mgr-5416", "cashier-5416", gatePIN)
	return gateEnv{mux: mux, dp: dp, mgrID: mgrID, cashierID: cashierID,
		cashier: &auth.User{ID: cashierID, Role: "cashier"},
		manager: &auth.User{ID: mgrID, Role: "manager"}}
}

// seedLine scans qty of ABC (100 minor each) and returns its line key.
func (e gateEnv) seedLine(t *testing.T, qty float64) string {
	t.Helper()
	if _, err := e.dp.Engine.ScanQty("ABC", qty); err != nil {
		t.Fatal(err)
	}
	return e.dp.Engine.Basket().Lines[0].LineKey
}

func (e gateEnv) lineDiscount() int64 { return e.dp.Engine.Basket().Lines[0].LineDiscount.Minor() }

func (e gateEnv) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.dp.Db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// approve replays the prompt's hidden fields with the manager PIN.
func approve(t *testing.T, e gateEnv, path string, prompt *httptest.ResponseRecorder, pin string) *httptest.ResponseRecorder {
	t.Helper()
	if !isElevationPrompt(prompt) {
		t.Fatalf("want a prompt to approve, got %d: %s", prompt.Code, prompt.Body.String())
	}
	retry := promptHiddenFields(t, prompt.Body.String())
	retry.Set("override_pin", pin)
	return postForm(e.mux, path, retry, e.cashier)
}

// ---- A. line discount -----------------------------------------------------

func TestPOSLine_DiscountRaise_CashierPromptedNothingChanged(t *testing.T) {
	e := newGateEnv(t)
	key := e.seedLine(t, 2)
	rec := postForm(e.mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"2"}, "discount": {"10"}}, e.cashier)
	if !isElevationPrompt(rec) {
		t.Fatalf("want prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Retarget") != "#shrinkage-hint" || rec.Header().Get("HX-Reswap") != "innerHTML" {
		t.Fatalf("prompt must land in the hint slot: %v", rec.Header())
	}
	if e.lineDiscount() != 0 {
		t.Fatalf("line must be untouched, discount %d", e.lineDiscount())
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Fatalf("audit rows = %d, want 0", n)
	}
	h := promptHiddenFields(t, rec.Body.String())
	if h.Get("discount") != "10" || h.Get("from_discount") != "0" || h.Get("key") != key {
		t.Fatalf("replay fields = %v", h)
	}
	if h.Get("reason") != "" {
		t.Fatalf("a pure raise carries no shrinkage reason: %v", h)
	}
}

func TestPOSLine_DiscountRaise_ManagerPINAppliesElevatedAudit(t *testing.T) {
	e := newGateEnv(t)
	key := e.seedLine(t, 2)
	prompt := postForm(e.mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"2"}, "discount": {"10"}}, e.cashier)
	rec := approve(t, e, "/api/pos/line", prompt, gatePIN)
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("approved retry: %d prompt=%v", rec.Code, isElevationPrompt(rec))
	}
	if e.lineDiscount() != 10 {
		t.Fatalf("discount = %d, want 10", e.lineDiscount())
	}
	var actor, blocked, detail string
	if err := e.dp.Db.QueryRow(`SELECT actor_id, blocked_actor_id, data_json FROM audit_log WHERE entity_type='pos_line' AND entity_id=? AND action='line_discount'`, key).Scan(&actor, &blocked, &detail); err != nil {
		t.Fatalf("want one elevated discount audit row: %v", err)
	}
	if actor != e.mgrID || blocked != e.cashierID {
		t.Fatalf("actor/blocked = %s/%s, want %s/%s", actor, blocked, e.mgrID, e.cashierID)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(detail), &d); err != nil || d["from_discount"] != float64(0) || d["to_discount"] != float64(10) {
		t.Fatalf("detail = %s (%v)", detail, err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM shrinkage_events`); n != 0 {
		t.Fatalf("a discount is not shrinkage, got %d rows", n)
	}
}

func TestPOSLine_DiscountLower_FreeAndAudited(t *testing.T) {
	e := newGateEnv(t)
	key := e.seedLine(t, 2)
	if err := e.dp.Engine.UpdateLineByKey(key, 2, 50); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"20", "0"} {
		rec := postForm(e.mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"2"}, "discount": {to}}, e.cashier)
		if rec.Code != http.StatusOK || isElevationPrompt(rec) {
			t.Fatalf("lowering to %s: %d prompt=%v", to, rec.Code, isElevationPrompt(rec))
		}
	}
	if e.lineDiscount() != 0 {
		t.Fatalf("discount = %d, want 0", e.lineDiscount())
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_line' AND action='line_discount' AND blocked_actor_id IS NULL AND actor_id=?`, e.cashierID); n != 2 {
		t.Fatalf("want 2 plain audit rows, got %d", n)
	}
}

func TestPOSLine_SameDiscountStepper_NotGated(t *testing.T) {
	e := newGateEnv(t)
	key := e.seedLine(t, 2)
	if err := e.dp.Engine.UpdateLineByKey(key, 2, 30); err != nil {
		t.Fatal(err)
	}
	for _, f := range []url.Values{
		{"key": {key}, "delta": {"1"}, "discount": {"30"}},
		{"key": {key}, "qty": {"5"}, "discount": {"30"}},
		{"key": {key}, "qty": {"5"}, "discount": {"30"}, "from_discount": {"30"}},
	} {
		rec := postForm(e.mux, "/api/pos/line", f, e.cashier)
		if rec.Code != http.StatusOK || isElevationPrompt(rec) {
			t.Fatalf("%v: %d prompt=%v", f, rec.Code, isElevationPrompt(rec))
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Fatalf("an unchanged discount writes no audit, got %d", n)
	}
}

func TestPOSLine_DiscountRetryStaleFromDiscount_409NoAudit(t *testing.T) {
	e := newGateEnv(t)
	key := e.seedLine(t, 2)
	prompt := postForm(e.mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"2"}, "discount": {"10"}}, e.cashier)
	// Another screen sets a discount while the prompt is open.
	if err := e.dp.Engine.UpdateLineByKey(key, 2, 5); err != nil {
		t.Fatal(err)
	}
	rec := approve(t, e, "/api/pos/line", prompt, gatePIN)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "data-line-changed") {
		t.Fatalf("want 409 with note, got %d: %s", rec.Code, rec.Body.String())
	}
	if e.lineDiscount() != 5 {
		t.Fatalf("line must stay as the other screen left it, got %d", e.lineDiscount())
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Fatalf("audit rows = %d, want 0", n)
	}
}

// A reduce-and-raise post needs both permissions; it verifies the PIN once
// (five failed checks lock the device, so three wrong retries must not).
func TestPOSLine_ReduceAndRaise_ApproverNeedsBoth_OnePINAttempt(t *testing.T) {
	e := newGateEnv(t)
	key := e.seedLine(t, 3)
	form := url.Values{"key": {key}, "qty": {"1"}, "discount": {"10"}}
	prompt := postForm(e.mux, "/api/pos/line", form, e.cashier)
	if !isElevationPrompt(prompt) {
		t.Fatalf("want one prompt, got %d", prompt.Code)
	}
	h := promptHiddenFields(t, prompt.Body.String())
	if h.Get("reason") == "" || h.Get("from_qty") != "3" || h.Get("from_discount") != "0" {
		t.Fatalf("replay fields = %v", h)
	}
	for i := 0; i < 3; i++ {
		if rec := approve(t, e, "/api/pos/line", prompt, "000000"); !isElevationPrompt(rec) {
			t.Fatalf("wrong PIN must re-prompt, got %d", rec.Code)
		}
	}
	// A manager who lacks price_override cannot approve the raise half.
	if _, err := e.dp.Db.Exec(`DELETE FROM role_permissions WHERE role='manager' AND action='price_override'`); err != nil {
		t.Fatal(err)
	}
	rec := approve(t, e, "/api/pos/line", prompt, gatePIN)
	if !isElevationPrompt(rec) || !strings.Contains(rec.Body.String(), "doesn&#39;t have permission") && !strings.Contains(rec.Body.String(), "doesn't have permission") {
		t.Fatalf("approver missing one action must be refused with a prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	if ls := e.dp.Engine.Basket().Lines; ls[0].Qty != 3 || ls[0].LineDiscount != 0 {
		t.Fatalf("nothing may apply: %+v", ls[0])
	}
	if _, err := e.dp.Db.Exec(`INSERT OR REPLACE INTO role_permissions(role,action,granted) VALUES('manager','price_override',1)`); err != nil {
		t.Fatal(err)
	}
	rec = approve(t, e, "/api/pos/line", prompt, gatePIN)
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("approver holding both: %d prompt=%v", rec.Code, isElevationPrompt(rec))
	}
	if l := e.dp.Engine.Basket().Lines[0]; l.Qty != 1 || l.LineDiscount != 10 {
		t.Fatalf("line = %+v", l)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM shrinkage_events`); n != 1 {
		t.Fatalf("shrinkage rows = %d, want 1", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_line' AND blocked_actor_id=?`, e.cashierID); n != 2 {
		t.Fatalf("want a reduction row and a discount row, got %d", n)
	}
}

func TestPOSLine_ZeroPriceOrVoucherDiscount_NotGated(t *testing.T) {
	e := newGateEnv(t)
	if _, err := e.dp.Engine.Scan("FREE"); err != nil {
		t.Fatal(err)
	}
	e.dp.Engine.AddLineWithModifiers(pos.BasketLine{SKU: "GV", ItemID: "gv", Name: "Voucher", PriceCents: 1000,
		NoMerge: true, GiftVoucher: &pos.GiftVoucherLine{Code: "GV-1"}}, 1, nil)
	for _, l := range e.dp.Engine.Basket().Lines {
		rec := postForm(e.mux, "/api/pos/line", url.Values{"key": {l.LineKey}, "qty": {"1"}, "discount": {"5"}}, e.cashier)
		if isElevationPrompt(rec) {
			t.Fatalf("%s: zero-price and voucher lines are never gated", l.Name)
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_line' AND blocked_actor_id IS NOT NULL`); n != 0 {
		t.Fatalf("no elevated audit expected, got %d", n)
	}
}

func TestPOSLine_DiscountTooLarge_NotGatedNotAudited(t *testing.T) {
	e := newGateEnv(t)
	key := e.seedLine(t, 1)
	rec := postForm(e.mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"1"}, "discount": {"999999"}}, e.cashier)
	if isElevationPrompt(rec) {
		t.Fatal("a discount the engine refuses is not gated")
	}
	if e.lineDiscount() != 0 {
		t.Fatalf("line must be unchanged, got %d", e.lineDiscount())
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Fatalf("audit rows = %d, want 0", n)
	}
}

func TestPOSLine_ManagerDiscount_NoPromptAudited(t *testing.T) {
	e := newGateEnv(t)
	key := e.seedLine(t, 2)
	rec := postForm(e.mux, "/api/pos/line", url.Values{"key": {key}, "qty": {"2"}, "discount": {"10"}}, e.manager)
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("manager: %d prompt=%v", rec.Code, isElevationPrompt(rec))
	}
	if e.lineDiscount() != 10 {
		t.Fatalf("discount = %d", e.lineDiscount())
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_line' AND action='line_discount' AND actor_id=? AND blocked_actor_id IS NULL`, e.mgrID); n != 1 {
		t.Fatalf("want 1 plain audit row, got %d", n)
	}
}

// ---- B. sale discount -----------------------------------------------------

func TestPOSDiscount_RaiseCashierPrompted(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	rec := postForm(e.mux, "/api/pos/discount", url.Values{"discount": {"15"}}, e.cashier)
	if !isElevationPrompt(rec) {
		t.Fatalf("want prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Retarget") != "#shrinkage-hint" {
		t.Fatalf("headers %v", rec.Header())
	}
	if got := e.dp.Engine.SaleDiscount().Minor(); got != 0 {
		t.Fatalf("discount = %d, want 0", got)
	}
	h := promptHiddenFields(t, rec.Body.String())
	if h.Get("discount") != "15" || h.Get("from_discount") != "0" {
		t.Fatalf("replay = %v", h)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Fatalf("audit rows = %d", n)
	}
}

func TestPOSDiscount_ElevatedAppliesAudit(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	prompt := postForm(e.mux, "/api/pos/discount", url.Values{"discount": {"15"}}, e.cashier)
	rec := approve(t, e, "/api/pos/discount", prompt, gatePIN)
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("approved: %d", rec.Code)
	}
	if rec.Header().Get("HX-Retarget") != "#basket" {
		t.Fatalf("success must retarget the basket: %v", rec.Header())
	}
	if got := e.dp.Engine.SaleDiscount().Minor(); got != 15 {
		t.Fatalf("discount = %d, want 15", got)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_discount' AND actor_id=? AND blocked_actor_id=?`, e.mgrID, e.cashierID); n != 1 {
		t.Fatalf("want 1 elevated audit row, got %d", n)
	}
}

func TestPOSDiscount_LowerFree(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	e.dp.Engine.SetDiscount(50)
	for _, to := range []string{"20", "0"} {
		rec := postForm(e.mux, "/api/pos/discount", url.Values{"discount": {to}}, e.cashier)
		if rec.Code != http.StatusOK || isElevationPrompt(rec) {
			t.Fatalf("lowering to %s: %d prompt=%v", to, rec.Code, isElevationPrompt(rec))
		}
	}
	if got := e.dp.Engine.SaleDiscount().Minor(); got != 0 {
		t.Fatalf("discount = %d", got)
	}
}

func TestPOSDiscount_Stale409(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	prompt := postForm(e.mux, "/api/pos/discount", url.Values{"discount": {"15"}}, e.cashier)
	e.dp.Engine.SetDiscount(5)
	rec := approve(t, e, "/api/pos/discount", prompt, gatePIN)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := e.dp.Engine.SaleDiscount().Minor(); got != 5 {
		t.Fatalf("discount must stay 5, got %d", got)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Fatalf("audit rows = %d", n)
	}
}

// ---- D. tender JSON discount ----------------------------------------------

func gatePostTender(e gateEnv, body string, u *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

const gateTenderWithDiscount = `{"payments":[{"method":"cash","amount":300}],"discount":%d,"offline":true}`

func gateTenderBody(discount string) string {
	return strings.Replace(gateTenderWithDiscount, "%d", discount, 1)
}

func TestPOSTender_DiscountAboveEngine_Cashier403BeforeSale(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	rec := gatePostTender(e, gateTenderBody("50"), e.cashier)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", rec.Code, rec.Body.String())
	}
	var env struct {
		Data  any `json:"data"`
		Error *struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil || env.Data != nil || env.Error == nil || env.Error.Code != "forbidden" || env.Error.Message == "" {
		t.Fatalf("envelope = %s (%v)", rec.Body.String(), err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM sales`); n != 0 {
		t.Fatalf("sale rows = %d, want 0", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM payments`); n != 0 {
		t.Fatalf("payment rows = %d, want 0", n)
	}
	if len(e.dp.Engine.Basket().Lines) != 1 {
		t.Fatal("basket must be untouched")
	}
}

func TestPOSTender_DiscountEqualOrLower_Accepted(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	e.dp.Engine.SetDiscount(50)
	for _, disc := range []string{"50", "20"} {
		rec := gatePostTender(e, gateTenderBody(disc), e.cashier)
		if rec.Code != http.StatusOK {
			t.Fatalf("discount %s: %d %s", disc, rec.Code, rec.Body.String())
		}
		e.seedLine2(t)
		e.dp.Engine.SetDiscount(50)
	}
	// Absent discount is "use the engine's" and never gated.
	rec := gatePostTender(e, `{"payments":[{"method":"cash","amount":300}],"offline":true}`, e.cashier)
	if rec.Code != http.StatusOK {
		t.Fatalf("no discount field: %d %s", rec.Code, rec.Body.String())
	}
}

// seedLine2 refills the basket after a completed tender.
func (e gateEnv) seedLine2(t *testing.T) {
	t.Helper()
	if _, err := e.dp.Engine.ScanQty("ABC", 2); err != nil {
		t.Fatal(err)
	}
}

func TestPOSTender_DiscountRaise_PermittedAcceptedAudited(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	rec := gatePostTender(e, gateTenderBody("50"), e.manager)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager: %d %s", rec.Code, rec.Body.String())
	}
	if n := e.count(t, `SELECT COUNT(*) FROM sales`); n != 1 {
		t.Fatalf("sale rows = %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_sale' AND action='sale_discount' AND actor_id=?`, e.mgrID); n != 1 {
		t.Fatalf("want 1 sale_discount audit row, got %d", n)
	}
}

// ---- C. New sale ----------------------------------------------------------

func TestPOSReset_PricedBasket_CashierPromptedNothingCleared(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	e.dp.Engine.SetTable("tbl-5416", "T1")
	rec := postForm(e.mux, "/api/pos/reset", url.Values{}, e.cashier)
	if !isElevationPrompt(rec) {
		t.Fatalf("want prompt, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Retarget") != "#shrinkage-hint" || rec.Header().Get("HX-Reswap") != "innerHTML" {
		t.Fatalf("prompt must land in the hint slot: %v", rec.Header())
	}
	if len(e.dp.Engine.Basket().Lines) != 1 || e.dp.Engine.TableID() != "tbl-5416" {
		t.Fatal("basket and table must be untouched")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM cleared_baskets`); n != 0 {
		t.Fatalf("cleared_baskets = %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Fatalf("audit rows = %d", n)
	}
	h := promptHiddenFields(t, rec.Body.String())
	if h.Get("from_lines") != "1" || h.Get("from_total") == "" {
		t.Fatalf("replay fields = %v", h)
	}
}

func TestPOSReset_Elevated_ClearsRecordsClearedBasketAndElevatedAudit(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	e.dp.Engine.SetTable("tbl-5416", "T1")
	prompt := postForm(e.mux, "/api/pos/reset", url.Values{}, e.cashier)
	rec := approve(t, e, "/api/pos/reset", prompt, gatePIN)
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("approved: %d prompt=%v", rec.Code, isElevationPrompt(rec))
	}
	if rec.Header().Get("HX-Retarget") != "#basket" || rec.Header().Get("HX-Reswap") != "outerHTML" {
		t.Fatalf("success must retarget the basket: %v", rec.Header())
	}
	if len(e.dp.Engine.Basket().Lines) != 0 || e.dp.Engine.TableID() != "" {
		t.Fatal("basket and table must be cleared")
	}
	var actor string
	if err := e.dp.Db.QueryRow(`SELECT actor_id FROM cleared_baskets`).Scan(&actor); err != nil || actor != e.cashierID {
		t.Fatalf("cleared_baskets actor = %q (%v), want the cashier", actor, err)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_reset' AND action='clear_sale' AND actor_id=? AND blocked_actor_id=?`, e.mgrID, e.cashierID); n != 1 {
		t.Fatalf("want 1 elevated audit row, got %d", n)
	}
}

func TestPOSReset_StaleRetry_409NothingCleared(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	prompt := postForm(e.mux, "/api/pos/reset", url.Values{}, e.cashier)
	if _, err := e.dp.Engine.Scan("PLAIN"); err != nil { // a scan lands while the prompt is open
		t.Fatal(err)
	}
	rec := approve(t, e, "/api/pos/reset", prompt, gatePIN)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(e.dp.Engine.Basket().Lines) != 2 {
		t.Fatal("nothing may be cleared")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM cleared_baskets`); n != 0 {
		t.Fatalf("cleared_baskets = %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log`); n != 0 {
		t.Fatalf("audit rows = %d", n)
	}
}

func TestPOSReset_EmptyOrZeroValue_Free(t *testing.T) {
	e := newGateEnv(t)
	if rec := postForm(e.mux, "/api/pos/reset", url.Values{}, e.cashier); rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("empty basket: %d prompt=%v", rec.Code, isElevationPrompt(rec))
	}
	if _, err := e.dp.Engine.Scan("FREE"); err != nil {
		t.Fatal(err)
	}
	if rec := postForm(e.mux, "/api/pos/reset", url.Values{}, e.cashier); rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("zero-value basket: %d prompt=%v", rec.Code, isElevationPrompt(rec))
	}
	if len(e.dp.Engine.Basket().Lines) != 0 {
		t.Fatal("zero-value basket must clear")
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_reset'`); n != 0 {
		t.Fatalf("a zero-value clear writes no gate audit, got %d", n)
	}
}

func TestPOSReset_ResumedHeld_ReparkNotGated(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	if rec := postForm(e.mux, "/api/pos/hold", url.Values{}, e.cashier); rec.Code != http.StatusOK {
		t.Fatalf("hold: %d %s", rec.Code, rec.Body.String())
	}
	id := heldID(t, e.dp)
	if rec := postForm(e.mux, "/api/pos/resume", url.Values{"id": {id}}, e.cashier); rec.Code != http.StatusOK {
		t.Fatalf("resume: %d %s", rec.Code, rec.Body.String())
	}
	rec := postForm(e.mux, "/api/pos/reset", url.Values{}, e.cashier)
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("re-park: %d prompt=%v", rec.Code, isElevationPrompt(rec))
	}
	if n := e.count(t, `SELECT COUNT(*) FROM held_sales`); n != 1 {
		t.Fatalf("order must stay held, rows=%d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM cleared_baskets`); n != 0 {
		t.Fatalf("a re-park is not a clear, cleared_baskets=%d", n)
	}
}

func TestPOSReset_Manager_NoPromptPlainAudit(t *testing.T) {
	e := newGateEnv(t)
	e.seedLine(t, 2)
	rec := postForm(e.mux, "/api/pos/reset", url.Values{}, e.manager)
	if rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("manager: %d prompt=%v", rec.Code, isElevationPrompt(rec))
	}
	if n := e.count(t, `SELECT COUNT(*) FROM cleared_baskets`); n != 1 {
		t.Fatalf("cleared_baskets = %d", n)
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type='pos_reset' AND actor_id=? AND blocked_actor_id IS NULL`, e.mgrID); n != 1 {
		t.Fatalf("want 1 plain audit row, got %d", n)
	}
}

// ---- auth off ---------------------------------------------------------------

func TestPOSGates_AuthOff_NeverPrompt(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	e := gateEnv{mux: mux, dp: dp}
	key := e.seedLine(t, 2)
	for _, c := range []struct {
		path string
		form url.Values
	}{
		{"/api/pos/line", url.Values{"key": {key}, "qty": {"2"}, "discount": {"10"}}},
		{"/api/pos/discount", url.Values{"discount": {"15"}}},
	} {
		rec := postForm(mux, c.path, c.form, nil)
		if rec.Code != http.StatusOK || isElevationPrompt(rec) {
			t.Fatalf("%s: %d prompt=%v", c.path, rec.Code, isElevationPrompt(rec))
		}
	}
	if n := e.count(t, `SELECT COUNT(*) FROM audit_log WHERE entity_type IN ('pos_line','pos_discount')`); n != 2 {
		t.Fatalf("auth off is still audited, got %d rows", n)
	}
	if rec := gatePostTender(e, gateTenderBody("50"), nil); rec.Code != http.StatusOK {
		t.Fatalf("tender: %d %s", rec.Code, rec.Body.String())
	}
	e.seedLine2(t)
	if rec := postForm(mux, "/api/pos/reset", url.Values{}, nil); rec.Code != http.StatusOK || isElevationPrompt(rec) {
		t.Fatalf("reset: %d prompt=%v", rec.Code, isElevationPrompt(rec))
	}
	if len(dp.Engine.Basket().Lines) != 0 {
		t.Fatal("reset must clear")
	}
}

// ---- elevation helper ---------------------------------------------------------

func TestCheckOrElevateAll_ApproverMissingOneActionRefused(t *testing.T) {
	e := newGateEnv(t)
	req := func() *http.Request {
		return auth.WithUser(httptest.NewRequest(http.MethodPost, "/x", nil), *e.cashier)
	}
	both := []string{"void_comp_waste", "price_override"}
	if got := checkOrElevateAll(e.dp, req(), gatePIN, both...); got.Outcome != elevated || got.ApproverID != e.mgrID {
		t.Fatalf("manager holds both: %+v", got)
	}
	if got := checkOrElevateAll(e.dp, auth.WithUser(httptest.NewRequest(http.MethodPost, "/x", nil), *e.manager), "", both...); got.Outcome != allowed {
		t.Fatalf("a session holding both needs no PIN: %+v", got)
	}
	if got := checkOrElevateAll(e.dp, req(), "", both...); got.Outcome != needsElevation || got.Err != nil {
		t.Fatalf("no PIN is a first-time prompt: %+v", got)
	}
	if _, err := e.dp.Db.Exec(`DELETE FROM role_permissions WHERE role='manager' AND action='price_override'`); err != nil {
		t.Fatal(err)
	}
	got := checkOrElevateAll(e.dp, req(), gatePIN, both...)
	if got.Outcome != needsElevation || got.Err != errApproverInsufficientRole {
		t.Fatalf("approver missing one action must be refused: %+v", got)
	}
}
