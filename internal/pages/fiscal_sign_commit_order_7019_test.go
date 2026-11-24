package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#7019: once fiscal.sign.ask has been dispatched the sale tender has
// exactly two outcomes — recorded, or refused on cannot-sign. The abandon
// check, the cross-till voucher reservations and the commit point therefore
// run BEFORE the ask.

const approvedSign = `{"status":"approved"}`

// signSeq orders events across goroutines (signer hit vs. primary /redeem).
var signSeq atomic.Int64

func resetBody(t *testing.T, mux http.Handler) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/pos/reset", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func wantInFlight409(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "tender_in_flight") {
		t.Fatalf("clear during a dispatched ask = %d, want 409 tender_in_flight: %s", rec.Code, rec.Body.String())
	}
}

// 1. A clear attempted from inside the approving signer is refused; the
// signed sale is recorded.
func TestTender_ClearDuringSignAskIsRefusedAndSaleRecorded_Cash(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	var asked atomic.Int32
	var reset *httptest.ResponseRecorder
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7019-clear-cash", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		reset = resetBody(t, mux)
		return json.RawMessage(approvedSign), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTender(t, mux, `{"payments":[{"method":"cash","amount":120}]}`)
	if asked.Load() != 1 {
		t.Fatalf("asked = %d, want 1", asked.Load())
	}
	wantInFlight409(t, reset)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("sales = %d, want 1 (signed sale must be recorded; response %d: %s)", n, rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM payments"); n != 1 {
		t.Fatalf("payments = %d, want 1", n)
	}
}

func TestTender_ClearDuringSignAskIsRefusedAndSaleRecorded_Card(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return approvedAnswer })
	var asked atomic.Int32
	var reset *httptest.ResponseRecorder
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7019-clear-card", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		reset = resetBody(t, mux)
		return json.RawMessage(approvedSign), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	if asked.Load() != 1 {
		t.Fatalf("asked = %d, want 1", asked.Load())
	}
	wantInFlight409(t, reset)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("sales = %d, want 1 (response %d: %s)", n, rec.Code, rec.Body.String())
	}
	if got := refunds(); len(got) != 0 {
		t.Fatalf("a recorded sale must not reverse its card leg: %+v", got)
	}
}

// abandonThenReset admits an abandon now and, once the signer is asked or
// 300 ms passed (whichever is first), performs the clear and ends it —
// the shape of a cashier's clear that raced the tender.
func abandonThenReset(t *testing.T, eng interface {
	BeginAbandon() bool
	EndAbandon()
	Reset()
}, asked *atomic.Int32) {
	t.Helper()
	if !eng.BeginAbandon() {
		t.Fatal("BeginAbandon refused")
	}
	go func() {
		deadline := time.After(300 * time.Millisecond)
		for asked.Load() == 0 {
			select {
			case <-deadline:
				eng.Reset()
				eng.EndAbandon()
				return
			case <-time.After(2 * time.Millisecond):
			}
		}
		eng.Reset()
		eng.EndAbandon()
	}()
}

// 2. An abandon admitted before the commit point: the tender is refused
// before the signer is ever asked.
func TestTender_AbandonAdmittedBeforeCommitRefusesWithoutAsking_Cash(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	var asked atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7019-abandon-cash", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		return json.RawMessage(approvedSign), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	abandonThenReset(t, dp.Engine, &asked)
	rec := postTender(t, mux, `{"payments":[{"method":"cash","amount":120}]}`)
	if asked.Load() != 0 {
		t.Fatalf("signer asked %d times, want 0 (abandon must be decided before the ask; response %d)", asked.Load(), rec.Code)
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
}

func TestTender_AbandonAdmittedBeforeCommitRefusesWithoutAsking_Card(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	var asked atomic.Int32
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string {
		if !dp.Engine.BeginAbandon() {
			t.Error("BeginAbandon refused inside authorize")
		} else {
			go func() {
				deadline := time.After(300 * time.Millisecond)
				for asked.Load() == 0 {
					select {
					case <-deadline:
						dp.Engine.Reset()
						dp.Engine.EndAbandon()
						return
					case <-time.After(2 * time.Millisecond):
					}
				}
				dp.Engine.Reset()
				dp.Engine.EndAbandon()
			}()
		}
		return approvedAnswer
	})
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7019-abandon-card", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		return json.RawMessage(approvedSign), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTender(t, mux, `{"payments":[{"method":"demopay","amount":120}]}`)
	if asked.Load() != 0 {
		t.Fatalf("signer asked %d times, want 0 (response %d)", asked.Load(), rec.Code)
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
	if got := refunds(); len(got) != 1 {
		t.Fatalf("card leg reversals = %d, want exactly 1", len(got))
	}
}

// 3. A cross-till voucher the primary refuses is refused before the ask.
func TestPOSTender_CrossTillVoucherRefusalRefusedBeforeSignAsk(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	primary, primaryDp, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-SHORT7019", "", 100)
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")
	var asked atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7019-voucher", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		return json.RawMessage(approvedSign), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-SHORT7019"}]}`)
	if asked.Load() != 0 {
		t.Fatalf("signer asked %d times, want 0 (refusal must precede the ask; response %d: %s)", asked.Load(), rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
	if n := primaryRedemptionRows(t, primaryDp, "GS-SHORT7019"); n != 0 {
		t.Fatalf("primary redemption rows = %d, want 0", n)
	}
}

func TestPOSTender_CrossTillVoucherRefusalRefusedBeforeSignAsk_Card(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return approvedAnswer })
	primary, _, primaryRepo := newVoucherPrimaryServer(t, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-SHORT7019C", "", 50)
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")
	var asked atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7019-voucher-card", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		return json.RawMessage(approvedSign), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"demopay","amount":120},{"method":"voucher","amount":180,"voucher_id":"GS-SHORT7019C"}]}`)
	if asked.Load() != 0 {
		t.Fatalf("signer asked %d times, want 0 (response %d: %s)", asked.Load(), rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
	if got := refunds(); len(got) != 1 {
		t.Fatalf("card leg reversals = %d, want exactly 1", len(got))
	}
}

func countCalls(calls []string, prefix string) int {
	n := 0
	for _, c := range calls {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// 4. cannot-sign with a reserved voucher: refused, reservation released,
// commit point ended, and the reservation preceded the ask.
func TestFiscalSignAsk_CannotSignReleasesVoucherAndEndsCommit(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	primary, primaryDp, primaryRepo, calls := newRecordingVoucherPrimaryServer(t, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-CS7019", "", 2000)
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")
	var redeemSeq, signSeqAt atomic.Int64
	calls.onCall = func(r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/redeem") {
			redeemSeq.Store(signSeq.Add(1))
		}
	}
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7019-cs", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		signSeqAt.Store(signSeq.Add(1))
		return json.RawMessage(`{"status":"cannot-sign"}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTenderJSON(t, mux, `{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-CS7019"}]}`)
	if !strings.Contains(rec.Body.String(), "be signed as presented") {
		t.Fatalf("want the cannot-sign refusal, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
	if n := countAuditRows(t, dp, fiscalSignGapActionCannotSign); n != 0 {
		t.Fatalf("cannot-sign audit rows = %d, want 0", n)
	}
	if n := countAuditRows(t, dp, fiscalSignGapActionSigning); n != 0 {
		t.Fatalf("signing audit rows = %d, want 0", n)
	}
	if redeemSeq.Load() == 0 || redeemSeq.Load() > signSeqAt.Load() {
		t.Fatalf("/redeem (seq %d) must precede the sign ask (seq %d); calls %v", redeemSeq.Load(), signSeqAt.Load(), calls.all())
	}
	if countCalls(calls.all(), "POST /api/sync/vouchers/GS-CS7019/release ") != 1 {
		t.Fatalf("want exactly one release call; calls %v", calls.all())
	}
	pv, err := primaryRepo.GetVoucherBalance(context.Background(), nil, "GS-CS7019")
	if err != nil || pv.BalanceMinor != 2000 {
		t.Fatalf("primary balance = %d (err %v), want 2000 (released)", pv.BalanceMinor, err)
	}
	if n := primaryRedemptionRows(t, primaryDp, "GS-CS7019"); n != 0 {
		t.Fatalf("primary redemption rows = %d, want 0", n)
	}
	if !dp.Engine.BeginAbandon() {
		t.Fatal("the commit point must have ended: a clear is refused after cannot-sign")
	}
	dp.Engine.EndAbandon()
}

// 5. The client leaves just before the ask: nothing signed, reservation
// released, commit point ended.
func TestTender_ClientDisconnectBeforeSignReleasesVoucherAndEndsCommit(t *testing.T) {
	mux, dp := newVoucherTenderDeps(t)
	t.Cleanup(func() { plugins.SharedBus(dp.Db).ResetSubscribers() })
	primary, primaryDp, primaryRepo, calls := newRecordingVoucherPrimaryServer(t, "b-replica")
	seedPrimaryVoucher(t, primaryRepo, "GS-DC7019", "", 2000)
	setReplicaSettings(t, dp.Settings, primary.URL, "b-replica")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tenderBeforeFiscalSignHook = cancel
	t.Cleanup(func() { tenderBeforeFiscalSignHook = nil })
	var asked atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-7019-dc", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		return json.RawMessage(approvedSign), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"voucher","amount":300,"voucher_id":"GS-DC7019"}]}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	mux.ServeHTTP(httptest.NewRecorder(), req)

	if asked.Load() != 0 {
		t.Fatalf("asked = %d, want 0", asked.Load())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
	if countCalls(calls.all(), "POST /api/sync/vouchers/GS-DC7019/release ") != 1 {
		t.Fatalf("want the reservation released; calls %v", calls.all())
	}
	if n := primaryRedemptionRows(t, primaryDp, "GS-DC7019"); n != 0 {
		t.Fatalf("primary redemption rows = %d, want 0", n)
	}
	if !dp.Engine.BeginAbandon() {
		t.Fatal("the commit point must have ended")
	}
	dp.Engine.EndAbandon()
}

// 6. Kiosk caller: same refusal before the ask, cashier engine untouched.
func TestSelfOrderShop_AbandonAdmittedBeforeCommitRefusesWithoutAsking(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	ctx := context.Background()
	const pluginID = "com.test.kiosk-fiscal-7019"
	for _, q := range []string{
		`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
VALUES ('` + pluginID + `', '1.0.0', 'Kiosk Fiscal Sign', 'wasm', './plugin.wasm', 'https://example.invalid', 'deadbeef', '0.0.1', '1', datetime('now'))`,
		`INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level)
VALUES ('` + pluginID + `', 'Kiosk Fiscal Sign', '1.0.0', 'installed', './plugin.wasm', 'wasm', 1, 'trusted')`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
VALUES ('hook-7019', '` + pluginID + `', 'fiscal.sign.ask', 'fiscal.sign', 1)`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
VALUES ('perm-7019', '` + pluginID + `', 'events:receive', 1)`,
	} {
		if _, err := d.DB.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	var asked atomic.Int32
	bus := plugins.SharedBus(d.DB)
	t.Cleanup(bus.ResetSubscribers)
	if _, err := bus.SubscribeWithHandler(ctx, pluginID, []string{"fiscal.sign.ask"}, func(context.Context, plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		return json.RawMessage(approvedSign), nil
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
	cashierLines := len(dp.Engine.Basket().Lines)
	cashierAttempt := dp.Engine.TenderAttemptID()

	abandonThenReset(t, dp.KioskEngine, &asked)
	rec := post("/api/self-order/checkout", "method=card")
	if asked.Load() != 0 {
		t.Fatalf("signer asked %d times, want 0 (response %d: %s)", asked.Load(), rec.Code, rec.Body.String())
	}
	var n int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n)
	if n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
	if len(dp.Engine.Basket().Lines) != cashierLines || dp.Engine.TenderAttemptID() != cashierAttempt {
		t.Fatal("the cashier engine must be untouched by a kiosk checkout")
	}
}
