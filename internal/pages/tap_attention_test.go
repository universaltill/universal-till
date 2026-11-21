package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6795 (K-8/A-13): a tap attempt a restart, New sale or park left
// behind is a payment needing attention. The gate refuses a new tap on its
// provider; a manager settles it by key with a fresh PIN.

// orphan seeds an attempt the way boot leaves one: unknown/till_restarted.
func (e *tapEnv) orphan(t *testing.T, key, provider string) {
	t.Helper()
	repo := data.NewPaymentAttemptRepo(e.dp.Db)
	if _, err := repo.Begin(context.Background(), data.PaymentAttempt{Key: key, Provider: provider, AmountMinor: 120, Currency: "gbp", Deadline: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Mark(context.Background(), key, data.AttemptUnknown, data.AttemptUpdate{Reason: data.AttemptReasonTillRestarted}); err != nil {
		t.Fatal(err)
	}
}

func (e *tapEnv) attemptByKey(t *testing.T, key string) data.PaymentAttempt {
	t.Helper()
	a, ok, err := data.NewPaymentAttemptRepo(e.dp.Db).Get(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("attempt %s: %v %v", key, ok, err)
	}
	return a
}

func (e *tapEnv) bindingCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.bindings)
}

func (e *tapEnv) wantRefusedForAttention(t *testing.T, rec *httptest.ResponseRecorder, bindingsBefore int) {
	t.Helper()
	e.wantText(t, rec, "tap.attention.start_refused")
	if n := e.bindingCount(); n != bindingsBefore {
		t.Fatalf("a refused tap reached the plugin: bindings %d -> %d", bindingsBefore, n)
	}
	if e.sales(t) != 0 || e.dp.Engine.TenderLock() != "" {
		t.Fatalf("a refused tap sold or locked: sales=%d lock=%q", e.sales(t), e.dp.Engine.TenderLock())
	}
	if n := e.broker.count("session_create"); n != 0 {
		t.Fatalf("session_create called %d times", n)
	}
}

// AC2 and the mutation test: remove the tapAttentionGate call and this fails.
func TestTapGate_RefusedWhileAttemptNeedsAttention(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "orphan-1", "stripe")
	rows := countRows(t, e.dp, "SELECT COUNT(*) FROM payment_attempts")
	rec := e.tender(t)
	e.wantRefusedForAttention(t, rec, 0)
	if n := countRows(t, e.dp, "SELECT COUNT(*) FROM payment_attempts"); n != rows {
		t.Fatalf("payment_attempts rows %d -> %d", rows, n)
	}
}

func TestTapGate_CashStillCompletes(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "orphan-1", "stripe")
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(`{"payments":[{"method":"cash","amount":120}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, auth.WithUser(req, e.cashier))
	if e.sales(t) != 1 {
		t.Fatalf("cash sale not saved: %d %s", rec.Code, rec.Body.String())
	}
}

func TestTapGate_OtherProviderNotRefused(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "orphan-1", "other")
	e.tender(t)
	if e.bindingCount() != 1 {
		t.Fatalf("a tap on another provider must start, bindings=%d", e.bindingCount())
	}
}

func TestTapGate_InFlightPendingDoesNotGate(t *testing.T) {
	e := tapRig(t)
	if _, err := data.NewPaymentAttemptRepo(e.dp.Db).Begin(context.Background(), data.PaymentAttempt{Key: "inflight", Provider: "stripe", AmountMinor: 120, Currency: "gbp", Deadline: time.Now().Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	e.tender(t)
	if e.bindingCount() != 1 {
		t.Fatalf("an in-flight pending attempt must not gate, bindings=%d", e.bindingCount())
	}
}

// AC9: the register's own locked leg never gates its own retry.
func TestTapGate_OwnLockedLegRetryAllowed(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	before := e.bindingCount()
	rec := e.tender(t)
	if strings.Contains(bodyText(rec), enText(t, "tap.attention.start_refused")) {
		t.Fatal("the own locked leg was refused as needing attention")
	}
	if e.bindingCount() != before+1 {
		t.Fatalf("retry did not reach the plugin: %d -> %d", before, e.bindingCount())
	}
}

// AC3: New sale / park over a locked tap leg leaves an unknown row; the next
// tap is refused.
func TestTapGate_RefusedAfterNewSaleOrPark(t *testing.T) {
	for _, path := range []string{"/api/pos/reset", "/api/pos/hold"} {
		t.Run(path, func(t *testing.T) {
			e := tapRig(t)
			e.pending(t)
			before := e.bindingCount()
			if path == "/api/pos/hold" {
				registerHoldAPI(e.mux, e.dp) // not part of the tap rig
			}
			if rec := e.do(t, e.cashier, http.MethodPost, path, url.Values{}); rec.Code >= 400 {
				t.Fatalf("%s = %d %s", path, rec.Code, rec.Body.String())
			}
			if e.dp.Engine.TenderLock() != "" {
				t.Fatalf("%s kept the lock", path)
			}
			scan(t, e.dp)
			rec := e.tender(t)
			e.wantRefusedForAttention(t, rec, before)
		})
	}
}

func TestTapGate_LookupErrorRefusesTapOnly(t *testing.T) {
	e := tapRig(t)
	if _, err := e.dp.Db.Exec(`ALTER TABLE payment_attempts RENAME TO payment_attempts_x`); err != nil {
		t.Fatal(err)
	}
	rec := e.tender(t)
	e.wantText(t, rec, "tap.attention.start_refused")
	if e.bindingCount() != 0 {
		t.Fatal("tap started although the attempt table was unreadable")
	}
}

// --- manager list / resolve ---

type attentionList struct {
	Data struct {
		Attempts []map[string]any `json:"attempts"`
		Count    int              `json:"count"`
	} `json:"data"`
	Error map[string]any `json:"error"`
}

func (e *tapEnv) list(t *testing.T, u auth.User) (*httptest.ResponseRecorder, attentionList) {
	t.Helper()
	rec := e.do(t, u, http.MethodGet, "/api/pay/attempts/attention", nil)
	var l attentionList
	_ = json.Unmarshal(rec.Body.Bytes(), &l)
	return rec, l
}

func (e *tapEnv) resolveAttn(t *testing.T, u auth.User, key, decision, reason, pin string) *httptest.ResponseRecorder {
	t.Helper()
	f := url.Values{"key": {key}, "decision": {decision}, "override_pin": {pin}}
	if reason != "" {
		f.Set("reason", reason)
	}
	return e.do(t, u, http.MethodPost, "/api/pay/attempts/attention/resolve", f)
}

func TestTapAttentionList_ManagerSeesCashierForbidden(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "orphan-1", "stripe")
	if err := data.NewPaymentAttemptRepo(e.dp.Db).Mark(context.Background(), "orphan-1", data.AttemptUnknown, data.AttemptUpdate{PIID: "pi_x", CardBrand: "visa", Last4: "4242"}); err != nil {
		t.Fatal(err)
	}
	rec, l := e.list(t, e.manager)
	if rec.Code != http.StatusOK || l.Data.Count != 1 || len(l.Data.Attempts) != 1 {
		t.Fatalf("manager list = %d %s", rec.Code, rec.Body.String())
	}
	a := l.Data.Attempts[0]
	if a["key"] != "orphan-1" || a["provider"] != "stripe" || a["amount_minor"] != float64(120) || a["currency"] != "gbp" ||
		a["state"] != "unknown" || a["reason"] != "till_restarted" || a["card_brand"] != "visa" || a["last4"] != "4242" || a["provider_payment_id"] != "pi_x" {
		t.Fatalf("row = %v", a)
	}
	for k := range a {
		switch k {
		case "key", "provider", "amount_minor", "currency", "state", "reason", "provider_payment_id", "card_brand", "last4", "created_at", "updated_at":
		default:
			t.Errorf("unexpected field %q in the DTO", k)
		}
	}
	rec, l = e.list(t, e.cashier)
	if rec.Code != http.StatusForbidden || l.Error["code"] != "forbidden" {
		t.Fatalf("cashier list = %d %s", rec.Code, rec.Body.String())
	}
}

func TestTapAttentionList_ExcludesLiveLockedLeg(t *testing.T) {
	e := tapRig(t)
	e.pending(t)
	_, l := e.list(t, e.manager)
	if l.Data.Count != 0 {
		t.Fatalf("the register's own locked leg was listed: %+v", l.Data)
	}
}

func TestTapAttentionResolve_NeedsFreshPIN(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "orphan-1", "stripe")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	for name, c := range map[string]struct {
		u   auth.User
		pin string
	}{"cashier": {e.cashier, dataAPITestManagerPIN}, "no pin": {e.manager, ""}, "wrong pin": {e.manager, "000000"}} {
		if rec := e.resolveAttn(t, c.u, "orphan-1", "not_charged", "", c.pin); rec.Code != http.StatusForbidden {
			t.Fatalf("%s = %d, want 403", name, rec.Code)
		}
	}
	if a := e.attemptByKey(t, "orphan-1"); a.State != data.AttemptUnknown {
		t.Fatalf("row changed: %s", a.State)
	}
	if e.broker.count("session_cancel") != 0 {
		t.Fatal("broker asked before the PIN was proven")
	}
}

func TestTapAttentionResolve_BrokerSucceededRefusesNotCharged(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "orphan-1", "stripe")
	e.broker.setStatus(piStatus("succeeded", 120))
	rec := e.resolveAttn(t, e.manager, "orphan-1", "not_charged", "", dataAPITestManagerPIN)
	if rec.Code != http.StatusConflict || !strings.Contains(bodyText(rec), "broker_says_paid") {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if a := e.attemptByKey(t, "orphan-1"); a.State != data.AttemptUnknown {
		t.Fatalf("row changed: %s", a.State)
	}
}

func TestTapAttentionResolve_NotPaidCancelsAndLiftsGate(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "orphan-1", "stripe")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	rec := e.resolveAttn(t, e.manager, "orphan-1", "not_charged", "", dataAPITestManagerPIN)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	if e.broker.count("session_cancel") != 1 {
		t.Fatalf("session_cancel calls = %d", e.broker.count("session_cancel"))
	}
	if a := e.attemptByKey(t, "orphan-1"); a.State != data.AttemptResolvedNotCharged {
		t.Fatalf("state = %s", a.State)
	}
	if n := countRows(t, e.dp, `SELECT COUNT(*) FROM audit_log WHERE action = 'tap_resolve' AND entity_id = 'orphan-1' AND actor_id = '`+e.manager.ID+`'`); n != 1 {
		t.Fatalf("audit rows = %d", n)
	}
	var payload string
	if err := e.dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'tap_resolve'`).Scan(&payload); err != nil || !strings.Contains(payload, "needs_attention") {
		t.Fatalf("audit payload = %q %v", payload, err)
	}
	e.tender(t)
	if e.bindingCount() != 1 {
		t.Fatal("the gate did not lift after the resolve")
	}
}

func TestTapAttentionResolve_ChargedNoSale(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "orphan-1", "stripe")
	e.broker.setStatus(piStatus("succeeded", 120))
	rec := e.resolveAttn(t, e.manager, "orphan-1", "charged_refund_at_provider", "", dataAPITestManagerPIN)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d %s", rec.Code, rec.Body.String())
	}
	a := e.attemptByKey(t, "orphan-1")
	if a.State != data.AttemptApproved || a.Reason != data.AttemptReasonChargedNoSale || a.PIID != "pi_k1b" || a.CardBrand != "visa" || a.Last4 != "4242" {
		t.Fatalf("row = %+v", a)
	}
	if e.sales(t) != 0 {
		t.Fatal("a sale was invented")
	}
	if countRows(t, e.dp, `SELECT COUNT(*) FROM audit_log WHERE action = 'tap_resolve'`) != 1 {
		t.Fatal("no audit row")
	}
	if rec2 := e.resolveAttn(t, e.manager, "orphan-1", "charged_refund_at_provider", "", dataAPITestManagerPIN); rec2.Code != http.StatusConflict {
		t.Fatalf("second resolve = %d, want 409", rec2.Code)
	}
	e.tender(t)
	if e.bindingCount() != 1 {
		t.Fatal("the gate did not lift")
	}
}

func TestTapAttentionResolve_NotPaidCannotBeApprovedAsCharged(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "orphan-1", "stripe")
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	if rec := e.resolveAttn(t, e.manager, "orphan-1", "charged_refund_at_provider", "", dataAPITestManagerPIN); rec.Code != http.StatusConflict {
		t.Fatalf("= %d", rec.Code)
	}
}

func TestTapAttentionResolve_BrokerUnreachableNeedsReason(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "orphan-1", "stripe")
	e.broker.setStatus("")
	if rec := e.resolveAttn(t, e.manager, "orphan-1", "not_charged", "", dataAPITestManagerPIN); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no reason = %d, want 422", rec.Code)
	}
	if a := e.attemptByKey(t, "orphan-1"); a.State != data.AttemptUnknown {
		t.Fatalf("row changed without a reason: %s", a.State)
	}
	if rec := e.resolveAttn(t, e.manager, "orphan-1", "not_charged", "customer_left", dataAPITestManagerPIN); rec.Code != http.StatusOK {
		t.Fatalf("with reason = %d %s", rec.Code, rec.Body.String())
	}
	if a := e.attemptByKey(t, "orphan-1"); a.State != data.AttemptResolvedNotCharged || a.Reason != "manager_override" {
		t.Fatalf("row = %+v", a)
	}
	var payload string
	if err := e.dp.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'tap_resolve'`).Scan(&payload); err != nil ||
		!strings.Contains(payload, "manager_override") || !strings.Contains(payload, "customer_left") {
		t.Fatalf("audit = %q %v", payload, err)
	}
}

func TestTapAttentionResolve_SettledUnknownOrLiveKey(t *testing.T) {
	e := tapRig(t)
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	e.orphan(t, "done", "stripe")
	if err := data.NewPaymentAttemptRepo(e.dp.Db).Mark(context.Background(), "done", data.AttemptResolvedNotCharged, data.AttemptUpdate{}); err != nil {
		t.Fatal(err)
	}
	if rec := e.resolveAttn(t, e.manager, "done", "not_charged", "", dataAPITestManagerPIN); rec.Code != http.StatusConflict {
		t.Fatalf("settled = %d, want 409", rec.Code)
	}
	if rec := e.resolveAttn(t, e.manager, "nope", "not_charged", "", dataAPITestManagerPIN); rec.Code != http.StatusNotFound {
		t.Fatalf("absent = %d, want 404", rec.Code)
	}
	if rec := e.resolveAttn(t, e.manager, "done", "bogus", "", dataAPITestManagerPIN); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad decision = %d, want 400", rec.Code)
	}
	e.pending(t)
	key := e.dp.Engine.PendingTender().Unknown[0].RequestID
	if rec := e.resolveAttn(t, e.manager, key, "not_charged", "", dataAPITestManagerPIN); rec.Code != http.StatusConflict {
		t.Fatalf("live key = %d, want 409", rec.Code)
	}
	if a := e.attemptByKey(t, key); a.State != data.AttemptUnknown {
		t.Fatalf("live row changed: %s", a.State)
	}
	if e.dp.Engine.TenderLock() == "" {
		t.Fatal("the live lock was touched")
	}
}

// --- review findings (ut-docs#6795 review) ---

// R1: a key a manager settled as charged with no sale is never a sale's
// payment: a live engine that still holds the key (a kiosk or table-QR
// basket's lock, a lock dropped with its attempt id kept) must not have
// tapAuthorize hand back the row's approval as confirmation.
func TestTapAuthorize_ChargedNoSaleKeyNeverConfirmsASale(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "k-cns", "stripe")
	if err := data.NewPaymentAttemptRepo(e.dp.Db).Mark(context.Background(), "k-cns", data.AttemptApproved, data.AttemptUpdate{PIID: "pi_x", Reason: data.AttemptReasonChargedNoSale}); err != nil {
		t.Fatal(err)
	}
	e.broker.setStatus(piStatus("succeeded", 120))
	_, _, conf, err := tapAuthorize(context.Background(), e.dp, "tapstripe", "k-cns", money.FromMinor(120), "gbp", map[string]any{"method": "tapstripe", "amount": int64(120)})
	if err == nil || conf != nil {
		t.Fatalf("a charged_no_sale key confirmed a payment: conf=%v err=%v", conf, err)
	}
	if e.bindingCount() != 0 {
		t.Fatalf("a charged_no_sale key reached the plugin: bindings=%d", e.bindingCount())
	}
}

// R2 (Tester point 1): an unattended engine's locked tap leg (self-order
// kiosk, table-QR basket) gates the cashier's taps on its provider, so it
// must be listed and resolvable by a manager — the customer was sent to the
// counter and there is no staff pending panel for it.
func TestTapAttention_KioskLockedLegListedAndResolvable(t *testing.T) {
	e := tapRig(t)
	e.dp.KioskEngine = pos.NewServiceWithResolver(pos.Config{}, nil)
	e.orphan(t, "kiosk-leg", "stripe")
	e.dp.KioskEngine.LockTender("kiosk-sig", pos.PendingTender{Unknown: []pos.UnknownLeg{{MethodID: "tapstripe", Currency: "gbp", RequestID: "kiosk-leg", Amount: money.FromMinor(120), Tap: true}}})
	e.wantRefusedForAttention(t, e.tender(t), 0)
	if _, l := e.list(t, e.manager); l.Data.Count != 1 || l.Data.Attempts[0]["key"] != "kiosk-leg" {
		t.Fatalf("the kiosk's locked leg gates the cashier but is not listed: %+v", l.Data)
	}
	e.broker.setStatus(piStatus("requires_payment_method", 0))
	if rec := e.resolveAttn(t, e.manager, "kiosk-leg", "not_charged", "", dataAPITestManagerPIN); rec.Code != http.StatusOK {
		t.Fatalf("resolve = %d %s", rec.Code, rec.Body.String())
	}
	e.tender(t)
	if e.bindingCount() != 1 {
		t.Fatal("the gate did not lift after the kiosk leg was settled")
	}
}

// R4 (Tester point 2): an unreadable plugin list must refuse at the gate
// (fail closed), never skip it: a transient error that clears before
// tapAuthorize reads the list again would start a tap past the gate.
func TestTapAttentionGate_PluginListErrorFailsClosed(t *testing.T) {
	e := tapRig(t)
	e.orphan(t, "orphan-1", "stripe")
	if _, err := e.dp.Db.Exec(`ALTER TABLE plugin_entries RENAME TO plugin_entries_x`); err != nil {
		t.Fatal(err)
	}
	if err := tapAttentionGate(context.Background(), e.dp, e.dp.Engine, []pos.PaymentInput{{MethodID: "tapstripe", Amount: money.FromMinor(120)}}); err == nil {
		t.Fatal("the gate let a tender through with the plugin list unreadable")
	}
}
