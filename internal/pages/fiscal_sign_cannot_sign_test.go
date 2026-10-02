package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// --- ut-docs#3309 / ADR-0136: fiscal.sign.ask "cannot-sign" is refused at
// tender (refuse-and-reverse), never completed unsigned. ---

// seedCannotSignCardPlugin installs a card-style payment plugin ("demopay")
// whose blocking payment.demopay.authorize approves and whose
// payment.demopay.refund records every payload it receives (and answers
// with refundErr, when non-nil). Returns a func that yields the recorded
// refund payloads and the authorize request IDs the bus delivered.
func seedCannotSignCardPlugin(t *testing.T, dp *common.Deps, refundErr error) (refunds func() []map[string]any, authorizeIDs func() []string) {
	t.Helper()
	return seedCannotSignCardPluginAnswering(t, dp, refundErr, `{"provider":"demopay","outcome":"approved"}`)
}

// seedCannotSignCardPluginAnswering is seedCannotSignCardPlugin with the
// plugin's authorize answer under the test's control (e.g. a reader that
// reports a customer-selected tip_amount, ut-docs#2571).
func seedCannotSignCardPluginAnswering(t *testing.T, dp *common.Deps, refundErr error, authorizeResp string) (refunds func() []map[string]any, authorizeIDs func() []string) {
	t.Helper()
	const pluginID = "com.universaltill.payment-demo"
	mustExec := func(q string) {
		t.Helper()
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	mustExec(`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.universaltill.payment-demo', '1.0.0', 'Demo Pay', 'demo', 'wasm', 'demo.wasm', 'https://example.test/demo.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`)
	mustExec(`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.payment-demo', 'Demo Pay', '1.0.0', 'demo.wasm', 'wasm', 1)`)
	mustExec(`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e1', 'com.universaltill.payment-demo', 'demopay', 'Demo Pay', 'payment', 'payment.demopay.requested', 1)`)
	mustExec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h1', 'com.universaltill.payment-demo', 'payment.demopay.authorize', 'handle_authorize', 1)`)
	mustExec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h2', 'com.universaltill.payment-demo', 'payment.demopay.refund', 'handle_refund', 1)`)
	mustExec(`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p1', 'com.universaltill.payment-demo', 'events:receive', 1)`)

	var mu sync.Mutex
	var gotRefunds []map[string]any
	var gotAuthIDs []string
	bus := plugins.SharedBus(dp.Db)
	bus.SetEventMode("payment.demopay.authorize", plugins.Blocking)
	bus.SetEventMode("payment.demopay.refund", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), pluginID,
		[]string{"payment.demopay.authorize", "payment.demopay.refund"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			mu.Lock()
			defer mu.Unlock()
			switch ev.Type {
			case "payment.demopay.authorize":
				gotAuthIDs = append(gotAuthIDs, ev.ID)
				return json.RawMessage(authorizeResp), nil
			case "payment.demopay.refund":
				var p map[string]any
				_ = json.Unmarshal(ev.Payload, &p)
				gotRefunds = append(gotRefunds, p)
				if refundErr != nil {
					return nil, refundErr
				}
				return json.RawMessage(`{"outcome":"refunded"}`), nil
			}
			return nil, nil
		}); err != nil {
		t.Fatal(err)
	}
	return func() []map[string]any {
			mu.Lock()
			defer mu.Unlock()
			return append([]map[string]any(nil), gotRefunds...)
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), gotAuthIDs...)
		}
}

func subscribeCannotSignSigner(t *testing.T, dp *common.Deps) {
	t.Helper()
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-cannotsign", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"cannot-sign"}`), nil
	})
}

func postTender(t *testing.T, mux *http.ServeMux, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// assertCannotSignRefused is the shared post-condition of every
// cannot-sign tender (ADR-0136 Decisions 1, 3, 4): no sale row, no
// payment row, no audit marker of either unsigned kind, the localized
// refusal toast, and the basket still holding its line so the cashier can
// fix it (or void it).
func assertCannotSignRefused(t *testing.T, dp *common.Deps, rec *httptest.ResponseRecorder) {
	t.Helper()
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("cannot-sign must refuse the tender: want 0 sale rows, got %d (body: %s)", n, rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM payments"); n != 0 {
		t.Fatalf("cannot-sign must refuse the tender: want 0 payment rows, got %d", n)
	}
	if n := countAuditRows(t, dp, fiscalSignGapActionCannotSign); n != 0 {
		t.Fatalf("a refused tender has no sale to journal: want 0 %s rows, got %d", fiscalSignGapActionCannotSign, n)
	}
	if n := countAuditRows(t, dp, fiscalSignGapActionSigning); n != 0 {
		t.Fatalf("cannot-sign is not an outage: want 0 %s rows, got %d", fiscalSignGapActionSigning, n)
	}
	if !strings.Contains(rec.Body.String(), "be signed as presented. Change the tip, discount or rate, or void the sale") {
		t.Fatalf("want the pos.toast.fiscal_cannot_sign toast in the re-rendered basket, got %d: %s", rec.Code, rec.Body.String())
	}
	if lines := dp.Engine.Basket().Lines; len(lines) != 1 {
		t.Fatalf("the basket must be kept so the cashier can fix or void it: want 1 line, got %d", len(lines))
	}
}

// A card leg already captured by payment.demopay.authorize is reversed via
// payment.demopay.refund (reversal:true, authorize_request_id = that leg's
// own idempotency key) before the tender is refused (ADR-0136 Decision 2).
func TestFiscalSignAsk_CannotSignRefusesTenderAndReversesCapturedCard(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, authIDs := seedCannotSignCardPlugin(t, dp, nil)
	subscribeCannotSignSigner(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}

	rec := postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	assertCannotSignRefused(t, dp, rec)

	got := refunds()
	if len(got) != 1 {
		t.Fatalf("want exactly one reversal refund for the one captured leg, got %d: %+v", len(got), got)
	}
	r := got[0]
	if r["method"] != "demopay" {
		t.Errorf("refund method = %v, want demopay", r["method"])
	}
	if amt, _ := r["amount"].(float64); int64(amt) != 120 {
		t.Errorf("refund amount = %v, want 120", r["amount"])
	}
	if want := dp.CurrentState().Currency; want == "" || r["currency"] != want {
		t.Errorf("refund currency = %v, want the shop's %q", r["currency"], want)
	}
	if r["reversal"] != true {
		t.Errorf("refund reversal = %v, want true", r["reversal"])
	}
	ids := authIDs()
	if len(ids) != 1 || ids[0] == "" {
		t.Fatalf("want one authorize request id observed, got %v", ids)
	}
	if r["authorize_request_id"] != ids[0] {
		t.Errorf("refund authorize_request_id = %v, want the leg's authorize request id %q", r["authorize_request_id"], ids[0])
	}
}

// The reversal sends back EXACTLY what the leg's authorize captured
// (ADR-0136 Decision 2, independent review): the amount the authorize
// payload asked for — which, for a non-fiscal-device method, is the gross
// Amount, NOT net of change — plus the tip the reader reported adding on
// top. Here the card leg is asked for 150 (120 sale + 30 change handed
// back) and the reader adds a 50 tip, so the plugin took 200; a reversal
// computed as Amount-ChangeGiven after the tip was folded in would send
// back only 170 and leave 30 of the customer's money on the card.
func TestFiscalSignAsk_CannotSignReversesExactlyWhatAuthorizeCaptured(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, _ := seedCannotSignCardPluginAnswering(t, dp, nil, `{"provider":"demopay","outcome":"approved","tip_amount":50}`)
	subscribeCannotSignSigner(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}

	rec := postTender(t, mux, `{"payments":[{"method":"demopay","amount":150,"change":30}]}`)
	assertCannotSignRefused(t, dp, rec)

	got := refunds()
	if len(got) != 1 {
		t.Fatalf("want exactly one reversal refund, got %d: %+v", len(got), got)
	}
	if amt, _ := got[0]["amount"].(float64); int64(amt) != 200 {
		t.Fatalf("reversal amount = %v, want 200 (the 150 the authorize asked for + the reader's 50 tip — exactly what the plugin captured)", got[0]["amount"])
	}
}

// A reversal that itself fails does not change the outcome: the tender is
// still refused, and the un-reversed capture is surfaced as a named
// Problems-ring warning (ADR-0136 Decision 2).
func TestFiscalSignAsk_CannotSignReversalFailureStillRefusesAndWarns(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	refunds, authIDs := seedCannotSignCardPlugin(t, dp, errors.New("terminal offline"))
	subscribeCannotSignSigner(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}

	rec := postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	assertCannotSignRefused(t, dp, rec)

	if n := len(refunds()); n != 1 {
		t.Fatalf("want one reversal attempt, got %d", n)
	}
	ids := authIDs()
	found := false
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "demopay") && strings.Contains(p.Msg, ids[0]) && strings.Contains(p.Msg, "120") {
			found = true
		}
	}
	if !found {
		t.Fatalf("want a Problems-ring warning naming method, amount and authorize_request_id; recent: %+v", logging.Recent())
	}
}

// A cash tender has nothing captured electronically: no refund is fired
// (no subscriber), and the tender is refused all the same.
func TestFiscalSignAsk_CannotSignRefusesCashTender(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	subscribeCannotSignSigner(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := fiscalSignTender(t, mux, false)
	assertCannotSignRefused(t, dp, rec)
	assertNoFiscalSignRetryQueue(t, dp)
}

// Kiosk surface (ADR-0136 Decision 4): the anonymous customer can't fix a
// tip or rate, so the refusal is a 409 with copy pointing them to the
// counter — and, like the cashier path, no sale row is ever created.
func TestSelfOrderShop_CannotSignRefusesCheckout(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	ctx := context.Background()
	const pluginID = "com.test.kiosk-fiscal-cannotsign"
	for _, q := range []string{
		`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('` + pluginID + `', '1.0.0', 'Kiosk Fiscal Sign', 'wasm', './plugin.wasm', 'https://example.invalid', 'deadbeef', '0.0.1', '1', '2026-08-15T00:00:00Z')`,
		`INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level)
VALUES ('` + pluginID + `', 'Kiosk Fiscal Sign', '1.0.0', 'installed', './plugin.wasm', 'wasm', 1, 'trusted')`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
VALUES ('hook-cs', '` + pluginID + `', 'fiscal.sign.ask', 'fiscal.sign', 1)`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
VALUES ('perm-cs', '` + pluginID + `', 'events:receive', 1)`,
	} {
		if _, err := d.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	bus := plugins.SharedBus(d.DB)
	t.Cleanup(bus.ResetSubscribers)
	if _, err := bus.SubscribeWithHandler(ctx, pluginID, []string{"fiscal.sign.ask"}, func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"cannot-sign"}`), nil
	}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	post("/api/self-order/scan", "code=5000001")
	rec := post("/api/self-order/checkout", "method=card")
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 for a cannot-sign kiosk checkout, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Please see the counter") {
		t.Fatalf("want the selforder.checkout.fiscal_cannot_sign copy, got: %s", rec.Body.String())
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("want 0 sale rows after a refused kiosk checkout, got %d", n)
	}
}

// A leg whose method has a payment.<key>.refund hook but NO .authorize hook
// (a post-settle-only plugin) captured nothing at tender — the authorize
// gate never reached a plugin — so a refused cannot-sign tender must not
// fire a "reversal" for money that was never taken.
func TestFiscalSignAsk_CannotSignNoReversalForUnauthorizedLeg(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	for _, q := range []string{
		`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
	          VALUES ('com.test.postsettle', '1.0.0', 'Post Settle', 'demo', 'wasm', 'p.wasm', 'https://example.test/p.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`,
		`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.test.postsettle', 'Post Settle', '1.0.0', 'p.wasm', 'wasm', 1)`,
		`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
	          VALUES ('e-ps', 'com.test.postsettle', 'qrpay', 'QR Pay', 'payment', 'payment.qrpay.requested', 1)`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	          VALUES ('h-ps', 'com.test.postsettle', 'payment.qrpay.refund', 'handle_refund', 1)`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
	          VALUES ('p-ps', 'com.test.postsettle', 'events:receive', 1)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	var mu sync.Mutex
	refundCalls := 0
	bus := plugins.SharedBus(dp.Db)
	bus.SetEventMode("payment.qrpay.refund", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.test.postsettle", []string{"payment.qrpay.refund"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			mu.Lock()
			refundCalls++
			mu.Unlock()
			return json.RawMessage(`{}`), nil
		}); err != nil {
		t.Fatal(err)
	}
	subscribeCannotSignSigner(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTender(t, mux, `{"payments":[{"method":"qrpay","amount":120}]}`)
	assertCannotSignRefused(t, dp, rec)
	mu.Lock()
	defer mu.Unlock()
	if refundCalls != 0 {
		t.Fatalf("no authorize ever reached the plugin, so no reversal may be sent; got %d refund calls", refundCalls)
	}
}

// A reversed authorize must never be "replayed" by the provider's own
// idempotency dedupe (ut-docs#1762): the basket is kept, so a retry the
// cashier makes after a fix that doesn't change the authorize payload
// (e.g. a tax-rate correction in a tax-inclusive shop) would otherwise
// resend the SAME request id — and a device that dedupes on it could hand
// back the memoized approval for money that was just sent back. After a
// cannot-sign reversal the next tender must carry a fresh authorize id.
func TestFiscalSignAsk_CannotSignRotatesAuthorizeIdempotencyKey(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	_, authIDs := seedCannotSignCardPlugin(t, dp, nil)
	subscribeCannotSignSigner(t, dp)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		assertCannotSignRefused(t, dp, postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`))
	}
	ids := authIDs()
	if len(ids) != 2 {
		t.Fatalf("want two authorize calls, got %v", ids)
	}
	if ids[0] == ids[1] {
		t.Fatalf("authorize request id reused after a reversal (%q): a deduping provider could replay the reversed approval", ids[0])
	}
}
