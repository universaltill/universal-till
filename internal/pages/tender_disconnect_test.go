package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#6805: the sale path used to record on the request context. A client
// that disconnects after the provider charged (closed tab, a scan or second
// Pay tap that htmx's hx-sync replace turns into an aborted XHR) must not
// cancel the writes that record the sale or send the money back. Every test
// cancels a live request ctx from INSIDE a stub, never before the request: a
// pre-cancelled ctx pins nothing (ut-docs#6784 review finding 3).

const approvedAnswer = `{"provider":"demopay","outcome":"approved"}`

// T1: card approves, the client leaves before the commit.
func TestTender_ClientDisconnectAfterCardApprovalStillRecordsSale(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return approvedAnswer })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var asked atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-6805", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		cancel()
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTenderCtx(t, mux, ctx, `{"payments":[{"method":"demopay","amount":120}]}`)
	if asked.Load() != 1 {
		t.Fatalf("fiscal.sign.ask answered %d times, want 1 (the disconnect must land mid-request)", asked.Load())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("sales = %d, want 1: the card was charged (response %d: %s)", n, rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM payments WHERE method_id = 'demopay'"); n != 1 {
		t.Fatalf("demopay payment rows = %d, want 1", n)
	}
	if got := refunds(); len(got) != 0 {
		t.Fatalf("a recorded sale must not be reversed: %+v", got)
	}
	if lines := dp.Engine.Basket().Lines; len(lines) != 0 {
		t.Fatalf("basket must be reset after the sale, has %d lines", len(lines))
	}
}

// T2: the client leaves WHILE the authorize call is in flight; the provider
// still approves, so the authorize dispatch itself must not be cancellable.
func TestTender_ClientDisconnectDuringAuthorizeStillRecordsSale(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	refunds, _ := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string {
		cancel()
		return approvedAnswer
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTenderCtx(t, mux, ctx, `{"payments":[{"method":"demopay","amount":120}]}`)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("sales = %d, want 1 (response %d: %s)", n, rec.Code, rec.Body.String())
	}
	if got := refunds(); len(got) != 0 {
		t.Fatalf("no reversal expected: %+v", got)
	}
	if lines := dp.Engine.Basket().Lines; len(lines) != 0 {
		t.Fatalf("basket must be reset, has %d lines", len(lines))
	}
}

// T3: the same through the kiosk caller of completeTender.
func TestSelfOrderShop_ClientDisconnectDuringAuthorizeStillRecordsSale(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-coffee", "COFFEE", "5000001", "Flat White", 320)
	seedStock(t, d, "itm-coffee", 10)
	for _, q := range []string{
		`INSERT INTO plugin_catalog (id, version, name, runtime, entrypoint, package_url, sha256, min_pos_version, api_version, published_at)
		 VALUES ('com.universaltill.payment-demo', '1.0.0', 'Demo Pay', 'wasm', 'demo.wasm', 'https://example.test/demo.wasm', 'deadbeef', '0.1.0', '1', datetime('now'))`,
		`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.universaltill.payment-demo', 'Demo Pay', '1.0.0', 'demo.wasm', 'wasm', 1)`,
		`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
		 VALUES ('e1', 'com.universaltill.payment-demo', 'demopay', 'Demo Pay', 'payment', 'payment.demopay.requested', 1)`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
		 VALUES ('h1', 'com.universaltill.payment-demo', 'payment.demopay.authorize', 'handle_authorize', 1)`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
		 VALUES ('p1', 'com.universaltill.payment-demo', 'events:receive', 1)`,
	} {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := data.NewPluginRepo(d.DB).SyncPluginPaymentMethods(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := plugins.SharedBus(d.DB)
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("payment.demopay.authorize", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.payment-demo",
		[]string{"payment.demopay.authorize"},
		func(hctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			cancel()
			if hctx.Err() != nil {
				return nil, hctx.Err()
			}
			return json.RawMessage(approvedAnswer), nil
		}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)

	scan := httptest.NewRequest(http.MethodPost, "/api/self-order/scan", strings.NewReader("code=5000001"))
	scan.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(httptest.NewRecorder(), scan)

	cashierLines := len(dp.Engine.Basket().Lines)
	cashierAttempt := dp.Engine.TenderAttemptID()

	req := httptest.NewRequest(http.MethodPost, "/api/self-order/checkout", strings.NewReader("method=demopay")).WithContext(ctx)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var n int
	_ = d.DB.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&n)
	if n != 1 {
		t.Fatalf("sales = %d, want 1 (response %d: %s)", n, rec.Code, rec.Body.String())
	}
	if lines := dp.KioskEngine.Basket().Lines; len(lines) != 0 {
		t.Fatalf("kiosk basket must be reset, has %d lines", len(lines))
	}
	if len(dp.Engine.Basket().Lines) != cashierLines || dp.Engine.TenderAttemptID() != cashierAttempt {
		t.Fatal("the cashier engine must be untouched by a kiosk checkout")
	}
}

// T4 (ut-docs#6910, replaces the #6805 guard that pinned "records nothing"):
// a signer has been asked and approved, then the client leaves. The TSE holds
// a signed transaction, so the sale MUST be recorded (ADR-0044 D1); a gone
// client is no third outcome next to "recorded" and "refused by cannot-sign".
func TestTender_ClientDisconnectAfterCashSignStillRecordsSale(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var asked atomic.Int32
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-6910-cash", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		cancel()
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTenderCtx(t, mux, ctx, `{"payments":[{"method":"cash","amount":120}]}`)
	if asked.Load() != 1 {
		t.Fatalf("fiscal.sign.ask answered %d times, want 1", asked.Load())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("a signed cash sale must be recorded despite the disconnect, got %d sales (response %d: %s)", n, rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM payments WHERE method_id = 'cash'"); n != 1 {
		t.Fatalf("cash payment rows = %d, want 1", n)
	}
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 0 {
		t.Fatalf("an approved sale carries no unsigned marker, got %d", n)
	}
	if lines := dp.Engine.Basket().Lines; len(lines) != 0 {
		t.Fatalf("basket must be reset after the sale, has %d lines", len(lines))
	}
}

// ut-docs#6910: the client is already gone BEFORE the ask is dispatched.
// Nothing was signed, so nothing is recorded and the
// ask is never sent.
func TestTender_ClientDisconnectBeforeCashSignRecordsNothing(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var asked atomic.Int32
	// The client leaves exactly at the last cancellable point (a pre-cancelled
	// ctx would be refused far earlier, by the age-restricted read).
	tenderBeforeFiscalSignHook = cancel
	t.Cleanup(func() { tenderBeforeFiscalSignHook = nil })
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-6910-before-ask", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		asked.Add(1)
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTenderCtx(t, mux, ctx, `{"payments":[{"method":"cash","amount":120}]}`)
	if asked.Load() != 0 {
		t.Fatalf("fiscal.sign.ask answered %d times, want 0 (client was gone before dispatch)", asked.Load())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
	if lines := dp.Engine.Basket().Lines; len(lines) != 1 {
		t.Fatalf("basket must be intact, has %d lines", len(lines))
	}
}

// ut-docs#6910 AC3: no sign.ask subscriber (nothing can have signed): a gone
// client still records nothing, so the cashier's retry cannot double-count.
func TestTender_ClientDisconnectCashNoSignerRecordsNothing(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// gone at the last cancellable point; no sign.ask subscriber is installed at all
	tenderBeforeFiscalSignHook = cancel
	t.Cleanup(func() { tenderBeforeFiscalSignHook = nil })
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTenderCtx(t, mux, ctx, `{"payments":[{"method":"cash","amount":120}]}`)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0", n)
	}
	if lines := dp.Engine.Basket().Lines; len(lines) != 1 {
		t.Fatalf("basket must be intact, has %d lines", len(lines))
	}
}

// ut-docs#6910 / ADR-0136: cannot-sign signed nothing, so it still refuses
// with no row even though the client left; the detach changes nothing there.
func TestTender_ClientDisconnectAfterCashSignCannotSignRefused(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-6910-cannot", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		cancel()
		return json.RawMessage(`{"status":"cannot-sign"}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTenderCtx(t, mux, ctx, `{"payments":[{"method":"cash","amount":120}]}`)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("cannot-sign must refuse with no sale row, got %d", n)
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM payments"); n != 0 {
		t.Fatalf("cannot-sign must leave no payment row, got %d", n)
	}
	if lines := dp.Engine.Basket().Lines; len(lines) != 1 {
		t.Fatalf("basket must be intact, has %d lines", len(lines))
	}
}

// ut-docs#6910: a backend failure answer after the client left is the
// proceed-and-declare path (ADR-0044): the sale is recorded and declared.
func TestTender_ClientDisconnectAfterCashSignBackendFailureDeclares(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-6910-down", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		cancel()
		return json.RawMessage(`{"status":"unreachable"}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTenderCtx(t, mux, ctx, `{"payments":[{"method":"cash","amount":120}]}`)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("sales = %d, want 1 (response %d: %s)", n, rec.Code, rec.Body.String())
	}
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 1 {
		t.Fatalf("unsigned_fiscal_signing rows = %d, want 1", n)
	}
}

// ut-docs#6910: the detached ask is still bounded by fiscalSignAskBudget, so
// a wedged signer cannot hold checkout (ADR-0003); the sale is declared.
func TestTender_ClientDisconnectWedgedSignerStaysBounded(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	orig := fiscalSignAskBudget
	fiscalSignAskBudget = 50 * time.Millisecond
	t.Cleanup(func() { fiscalSignAskBudget = orig })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-6910-wedged", func(hctx context.Context, _ plugins.Event) (json.RawMessage, error) {
		cancel()
		<-hctx.Done()
		return nil, hctx.Err()
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	rec := postTenderCtx(t, mux, ctx, `{"payments":[{"method":"cash","amount":120}]}`)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("request took %v, want < 1s (budget bounds the detached ask)", d)
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("sales = %d, want 1 (response %d: %s)", n, rec.Code, rec.Body.String())
	}
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 1 {
		t.Fatalf("unsigned_fiscal_signing rows = %d, want 1", n)
	}
}

// T5: the card is charged, the client leaves, and CompleteSale then fails for
// a reason that is not the ctx: the money must really be sent back.
func TestTender_ClientDisconnectSaleFailureStillReversesCapturedLeg(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return approvedAnswer })
	if _, err := dp.Db.Exec(`ALTER TABLE payments RENAME COLUMN voucher_id TO voucher_id_disabled`); err != nil {
		t.Fatalf("rename payments.voucher_id: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-6805-rev", func(_ context.Context, _ plugins.Event) (json.RawMessage, error) {
		cancel()
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	postTenderCtx(t, mux, ctx, `{"payments":[{"method":"demopay","amount":120}]}`)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 0 {
		t.Fatalf("sales = %d, want 0 (CompleteSale was made to fail)", n)
	}
	got := refunds()
	ids := authIDs()
	if len(got) != 1 || len(ids) != 1 || got[0]["reversal"] != true || got[0]["authorize_request_id"] != ids[0] {
		t.Fatalf("want exactly one reversal of the captured leg, got %+v (authorize ids %v)", got, ids)
	}
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, "reversing captured payment FAILED") {
			t.Fatalf("the reversal must not fail: %s", p.Msg)
		}
	}
}

// T6: after the commit every write is detached, cash-less legs included: a
// settle subscriber that drops the client must not lose sale.completed.
func TestTender_ClientDisconnectAfterCommitKeepsPostCommitWrites(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	const pluginID = "com.test.postsettle6805"
	for _, q := range []string{
		`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, is_deprecated, min_pos_version, api_version, published_at)
		 VALUES ('com.test.postsettle6805', '1.0.0', 'Post Settle', 'demo', 'wasm', 'p.wasm', 'https://example.test/p.wasm', 'deadbeef', 'auth', 'site', '[]', 0, '0.0.0', '1', datetime('now'))`,
		`INSERT INTO plugins (id, name, version, entrypoint, runtime, is_active) VALUES ('com.test.postsettle6805', 'Post Settle', '1.0.0', 'p.wasm', 'wasm', 1)`,
		`INSERT INTO plugin_entries (id, plugin_id, key, label, type, trigger_event, is_active)
		 VALUES ('e-ps6805', 'com.test.postsettle6805', 'qrpay', 'QR Pay', 'payment', 'payment.qrpay.requested', 1)`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
		 VALUES ('h-ps6805a', 'com.test.postsettle6805', 'payment.qrpay.requested', 'handle', 1)`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
		 VALUES ('h-ps6805b', 'com.test.postsettle6805', 'sale.completed', 'handle', 1)`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
		 VALUES ('p-ps6805', 'com.test.postsettle6805', 'events:receive', 1)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var completed []plugins.SaleCompletedEvent
	bus := plugins.SharedBus(dp.Db)
	bus.SetEventMode("payment.qrpay.requested", plugins.Blocking)
	bus.SetEventMode("sale.completed", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), pluginID, []string{"payment.qrpay.requested", "sale.completed"},
		func(_ context.Context, ev plugins.Event) (json.RawMessage, error) {
			switch ev.Type {
			case "payment.qrpay.requested":
				cancel() // the settle publish drops the client, after the commit
			case "sale.completed":
				var e plugins.SaleCompletedEvent
				_ = json.Unmarshal(ev.Payload, &e)
				mu.Lock()
				completed = append(completed, e)
				mu.Unlock()
			}
			return json.RawMessage(`{}`), nil
		}); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := postTenderCtx(t, mux, ctx, `{"payments":[{"method":"qrpay","amount":120}]}`)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("sales = %d, want 1 (response %d: %s)", n, rec.Code, rec.Body.String())
	}
	var receiptNo string
	if err := dp.Db.QueryRow(`SELECT receipt_no FROM sales`).Scan(&receiptNo); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(completed) != 1 || completed[0].ReceiptNo != receiptNo || receiptNo == "" {
		t.Fatalf("want exactly one sale.completed with receipt_no %q after the commit, got %+v", receiptNo, completed)
	}
}
