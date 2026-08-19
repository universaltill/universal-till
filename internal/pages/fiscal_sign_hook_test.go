package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
)

// --- ut-docs#675: fiscal.sign.ask (ADR-0044 / ADR-0041 tender-phase point) --

// newFiscalSignDeps builds the tender harness for fiscal.sign.ask tests:
// same shape as newFiscalTestDeps, plus paths.Init (the wasm runtime needs a
// real plugin dir) and a shared-bus reset on cleanup (SharedBus is a
// process-global singleton — a subscriber leaked into the next test would
// break its zero-plugin assumptions).
func newFiscalSignDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	initTestPaths(t)
	chdirRoot(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)

	resolver := stubResolver{
		"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", TaxRateBP: 2000},
	}
	engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, resolver)

	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "EUR", TaxRate: 19}}
	pm, err := plugins.Init(t.Context(), cfg, db)
	if err != nil {
		t.Fatalf("init plugins: %v", err)
	}
	setStore := settings.NewStore(db)
	state := common.LoadState(t.Context(), setStore, cfg)
	dp := &common.Deps{
		Cfg:      cfg,
		Db:       db,
		State:    state,
		Menu:     []common.MenuItem{{Href: "/", Label: "Home"}},
		Engine:   engine,
		Pm:       pm,
		Settings: setStore,
	}
	// ResetSubscribers BEFORE WaitForAsyncWork in LIFO cleanup order doesn't
	// matter here; what matters is that the global bus is clean for the next
	// test in this package.
	t.Cleanup(func() { plugins.SharedBus(db).ResetSubscribers() })
	t.Cleanup(dp.WaitForAsyncWork)
	mux := http.NewServeMux()
	registerPOSAPI(mux, dp)
	return mux, dp
}

// fiscalSignTender posts one cash tender; offline mirrors the till's own
// declared offline state (the #offline-flag input / navigator.onLine signal
// the tender request already carries).
func fiscalSignTender(t *testing.T, mux *http.ServeMux, offline bool) *httptest.ResponseRecorder {
	t.Helper()
	body := fmt.Sprintf(`{"payments":[{"method":"cash","amount":120}],"offline":%v}`, offline)
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// buildFiscalGuest compiles one of the wasip1 fiscal-sign test guests
// (testdata/<dir>) — same mechanics as buildTaxAskGuest.
func buildFiscalGuest(t *testing.T, dir string) []byte {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	out := filepath.Join(t.TempDir(), dir+".wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/"+dir)
	cmd.Dir = filepath.Dir(file)
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build wasip1 guest %s: %v\n%s", dir, err, raw)
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read built guest: %v", err)
	}
	return raw
}

// seedFiscalSignPluginRows plants the DB rows for an installed wasm fiscal
// signing plugin (registry, hook on fiscal.sign.ask, events:receive grant).
func seedFiscalSignPluginRows(t *testing.T, dp *common.Deps, pluginID string, active bool) {
	t.Helper()
	ctx := context.Background()
	activeInt := 0
	if active {
		activeInt = 1
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level)
VALUES (?, ?, '1.0.0', 'installed', './plugin.wasm', 'wasm', ?, 'trusted')`,
		pluginID, "Fiscal Sign "+pluginID, activeInt); err != nil {
		t.Fatalf("seed plugins: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
VALUES (?, ?, 'fiscal.sign.ask', 'fiscal.sign', 1)`, "hook-"+pluginID, pluginID); err != nil {
		t.Fatalf("seed plugin_hooks: %v", err)
	}
	if _, err := dp.Db.ExecContext(ctx, `
INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
VALUES (?, ?, 'events:receive', 1)`, "perm-"+pluginID, pluginID); err != nil {
		t.Fatalf("seed plugin_permissions: %v", err)
	}
}

// installFiscalSignWasmPlugin seeds the rows AND puts the compiled guest on
// disk where the wasm runtime expects it, then loads it for real via Sync.
func installFiscalSignWasmPlugin(t *testing.T, dp *common.Deps, pluginID, guestDir string) {
	t.Helper()
	seedFiscalSignPluginRows(t, dp, pluginID, true)
	guest := buildFiscalGuest(t, guestDir)
	dir := filepath.Join(paths.Plugins(), pluginID, "1.0.0")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.wasm"), guest, 0o644); err != nil {
		t.Fatalf("write plugin.wasm: %v", err)
	}
	dp.Pm.Wasm.Sync(context.Background(), dp.Db)
}

// subscribeFiscalSignHandler registers an in-process Go handler as the
// fiscal.sign.ask answerer (rows seeded, no wasm) — for tests that need to
// count invocations or control timing precisely.
func subscribeFiscalSignHandler(t *testing.T, dp *common.Deps, pluginID string, h plugins.EventHandler) {
	t.Helper()
	seedFiscalSignPluginRows(t, dp, pluginID, true)
	bus := plugins.SharedBus(dp.Db)
	if _, err := bus.SubscribeWithHandler(context.Background(), pluginID, []string{fiscalSignAskEvent}, h); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
}

func countAuditRows(t *testing.T, dp *common.Deps, action string) int {
	t.Helper()
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='sale' AND action=?`, action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// (i) A till whose installed signer answers "approved" completes the sale
// with NO unsigned_fiscal_signing marker, no pending retry, no receipt
// outage notice — the happy path is invisible.
func TestFiscalSignAsk_ApprovedSaleHasNoMarker(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	installFiscalSignWasmPlugin(t, dp, "com.test.fiscal-sign-ok", "fiscalsign_guest")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}

	rec := fiscalSignTender(t, mux, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countSales(t, dp); got != 1 {
		t.Fatalf("expected 1 sale, got %d", got)
	}
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 0 {
		t.Fatalf("approved sale must carry no unsigned_fiscal_signing marker, got %d", n)
	}
	if strings.Contains(rec.Body.String(), "receipt.fiscal.unsigned_signing") {
		t.Fatalf("approved sale must not render the outage notice: %s", rec.Body.String())
	}
	if pending, err := loadPendingFiscalSignRetries(context.Background(), dp); err != nil || len(pending) != 0 {
		t.Fatalf("expected no pending retries, got %v (err %v)", pending, err)
	}
	if v, _, _ := dp.Settings.Get(context.Background(), fiscal.KeyTSEFailingSince); v != "" {
		t.Fatalf("approved sale must not mark the TSE failing, got %q", v)
	}
}

// (ii) The signer declares its backend unreachable: the sale completes
// anyway, IS journaled unsigned, DOES get a receipt outage notice, DOES
// raise a Problem, IS queued for background retry — and does NOT touch
// fiscal.tse_failing_since (B1, review of ut-docs#675: every failure this
// card can observe is a reachability outcome, and ADR-0048 Decision 1
// reserves that key for a TSE known bad, a strictly narrower condition).
func TestFiscalSignAsk_UnreachableDeclaredProceedsAndDeclares(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	installFiscalSignWasmPlugin(t, dp, "com.test.fiscal-sign-down", "fiscalsign_unreachable_guest")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}

	rec := fiscalSignTender(t, mux, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("sale must complete despite the signing failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countSales(t, dp); got != 1 {
		t.Fatalf("expected 1 sale, got %d", got)
	}

	// (a) journal marker, attached to the real sale row.
	var saleID string
	if err := dp.Db.QueryRow(`SELECT id FROM sales`).Scan(&saleID); err != nil {
		t.Fatal(err)
	}
	var markerSaleID, markerPayload string
	if err := dp.Db.QueryRow(`SELECT entity_id, data_json FROM audit_log WHERE entity_type='sale' AND action='unsigned_fiscal_signing'`).
		Scan(&markerSaleID, &markerPayload); err != nil {
		t.Fatalf("expected a sale/unsigned_fiscal_signing audit row: %v", err)
	}
	if markerSaleID != saleID {
		t.Fatalf("marker not attached to the sale: %q != %q", markerSaleID, saleID)
	}
	if !strings.Contains(markerPayload, "unreachable") {
		t.Fatalf("marker payload should carry the failure reason, got %s", markerPayload)
	}

	// (b) receipt outage notice on the inline HTML receipt (en copy).
	if !strings.Contains(rec.Body.String(), "TSE signing was unavailable") {
		t.Fatalf("expected the receipt outage notice, got: %s", rec.Body.String())
	}

	// (c) operator alert in the Problems ring.
	foundProblem := false
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, saleID) && strings.Contains(p.Msg, "UNSIGNED") {
			foundProblem = true
		}
	}
	if !foundProblem {
		t.Fatalf("expected a Problems-ring warning naming sale %s; recent: %+v", saleID, logging.Recent())
	}

	// (d) queued for background retry, keyed by the same sale id.
	pending, err := loadPendingFiscalSignRetries(context.Background(), dp)
	if err != nil || len(pending) != 1 || pending[0].SaleID != saleID {
		t.Fatalf("expected 1 pending retry for sale %s, got %+v (err %v)", saleID, pending, err)
	}

	// B1: even a genuine online failure must NOT stamp tse_failing_since —
	// "unreachable" means "we can't reach it", not "the TSE is known bad"
	// (ADR-0048 Decision 1), and stamping it would hard-block the NEXT sale
	// in a German system-of-record shop over a mere reachability blip.
	if v, _, _ := dp.Settings.Get(context.Background(), fiscal.KeyTSEFailingSince); v != "" {
		t.Fatalf("a fiscal.sign.ask failure must never stamp %s (ADR-0048 D1), got %q", fiscal.KeyTSEFailingSince, v)
	}
}

// B1 (review of ut-docs#675), the consequence that makes the wiring a
// blocker: with the ADR-0048 hard gate armed (DE, system-of-record, TSE
// configured), repeated fiscal.sign.ask failures must never trip the gate —
// a shop whose signing backend is merely unreachable keeps selling
// (proceed-and-declare), it does not get hard-blocked on its next sale
// pending an owner override. Pre-fix, the first failed sale stamped
// fiscal.tse_failing_since and the second tender was refused outright.
func TestFiscalSignAsk_RepeatedFailuresNeverTripADR0048Gate(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	dp.UpdateState(func(st *common.RuntimeState) { st.Country = "DE" })
	if err := dp.Settings.Set(ctx, fiscal.KeySystemOfRecord, "true"); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, fiscal.KeyTSEConfigured, "true"); err != nil {
		t.Fatal(err)
	}
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-flaky", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"unreachable"}`), nil
	})

	for i := 1; i <= 2; i++ {
		if _, err := dp.Engine.Scan("ABC"); err != nil {
			t.Fatal(err)
		}
		rec := fiscalSignTender(t, mux, false)
		if rec.Code != http.StatusOK {
			t.Fatalf("sale %d must complete despite the signing failure (never hard-block via ADR-0048), got %d: %s", i, rec.Code, rec.Body.String())
		}
		if v, _, _ := dp.Settings.Get(ctx, fiscal.KeyTSEFailingSince); v != "" {
			t.Fatalf("sale %d stamped %s = %q — fiscal.sign.ask must never drive that key", i, fiscal.KeyTSEFailingSince, v)
		}
	}
	if got := countSales(t, dp); got != 2 {
		t.Fatalf("expected both sales completed, got %d", got)
	}
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 2 {
		t.Fatalf("both sales must still be declared unsigned, got %d markers", n)
	}
}

// (iii) Known-offline short-circuit: with a signer installed, a tender the
// till already knows is offline never dispatches to the plugin at all — but
// still proceeds-and-declares, and must NOT trip the TSE-failing state
// (ADR-0048 Decision 1: network-offline is not a failing TSE).
func TestFiscalSignAsk_KnownOfflineShortCircuits(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	var invocations atomic.Int64
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-counting", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		invocations.Add(1)
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}

	rec := fiscalSignTender(t, mux, true)
	if rec.Code != http.StatusOK || countSales(t, dp) != 1 {
		t.Fatalf("offline sale must complete, code=%d sales=%d body=%s", rec.Code, countSales(t, dp), rec.Body.String())
	}
	if n := invocations.Load(); n != 0 {
		t.Fatalf("known-offline tender must never dispatch to the signer, got %d invocations", n)
	}
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 1 {
		t.Fatalf("offline sale must still be journaled unsigned, got %d markers", n)
	}
	if pending, err := loadPendingFiscalSignRetries(context.Background(), dp); err != nil || len(pending) != 1 {
		t.Fatalf("offline sale must be queued for retry, got %+v (err %v)", pending, err)
	}
	if v, _, _ := dp.Settings.Get(context.Background(), fiscal.KeyTSEFailingSince); v != "" {
		t.Fatalf("known-offline must NEVER set tse_failing_since (ADR-0048 D1), got %q", v)
	}
}

// A signer explicitly answering "not-this-terminal" is a clean decline —
// ADR-0041 Decision F semantics: same as no answer, NOT a failure.
func TestFiscalSignAsk_NotThisTerminalIsNotAFailure(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-notme", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"not-this-terminal"}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := fiscalSignTender(t, mux, false)
	if rec.Code != http.StatusOK || countSales(t, dp) != 1 {
		t.Fatalf("sale must complete, code=%d sales=%d", rec.Code, countSales(t, dp))
	}
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 0 {
		t.Fatalf("a clean not-this-terminal must not be declared as a failure, got %d markers", n)
	}
	if v, _, _ := dp.Settings.Get(context.Background(), fiscal.KeyTSEFailingSince); v != "" {
		t.Fatalf("not-this-terminal must not mark the TSE failing, got %q", v)
	}
}

// A handler that blows the 3s budget (shrunk to 50ms via the test seam) is a
// failure → proceed-and-declare.
func TestFiscalSignAsk_TimeoutDeclares(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	orig := fiscalSignAskBudget
	fiscalSignAskBudget = 50 * time.Millisecond
	t.Cleanup(func() { fiscalSignAskBudget = orig })

	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-slow", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := fiscalSignTender(t, mux, false)
	if rec.Code != http.StatusOK || countSales(t, dp) != 1 {
		t.Fatalf("sale must complete despite the timeout, code=%d sales=%d", rec.Code, countSales(t, dp))
	}
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 1 {
		t.Fatalf("timeout must be declared, got %d markers", n)
	}
}

// (iv) Zero-plugin till: the dispatch fast path allocates nothing and does
// no work beyond one subscriber lookup — ADR-0041 Decision A's "a
// zero-plugin till pays nothing", asserted the falsifiable way (allocs/op).
func TestFiscalSignAsk_ZeroPluginTillAllocatesNothing(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers() // belt-and-braces: no leaked subscriber
	in := pos.SaleInput{Currency: "EUR", Offline: false}
	allocs := testing.AllocsPerRun(100, func() {
		res := dispatchFiscalSignAsk(context.Background(), dp, &in)
		if res.Outcome != fiscalSignNoSigner {
			t.Fatalf("expected fiscalSignNoSigner, got %v", res.Outcome)
		}
	})
	if allocs != 0 {
		t.Fatalf("zero-plugin fast path must not allocate, got %v allocs/op", allocs)
	}
}

// Companion benchmark for the same fast path (run with -benchmem to see the
// allocs/op figure CI's test above asserts).
func BenchmarkFiscalSignAsk_NoSubscribers(b *testing.B) {
	bus := plugins.SharedBus(nil)
	bus.ResetSubscribers()
	dp := &common.Deps{}
	in := pos.SaleInput{Currency: "EUR"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = dispatchFiscalSignAsk(context.Background(), dp, &in)
	}
}

// Zero-plugin till end-to-end: behaves exactly like today — sale completes,
// no marker, no pending list, no receipt notice.
func TestFiscalSignAsk_ZeroPluginTillUnchanged(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := fiscalSignTender(t, mux, false)
	if rec.Code != http.StatusOK || countSales(t, dp) != 1 {
		t.Fatalf("expected the sale to complete, code=%d sales=%d", rec.Code, countSales(t, dp))
	}
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 0 {
		t.Fatalf("zero-plugin till must not write signing markers, got %d", n)
	}
	if pending, err := loadPendingFiscalSignRetries(context.Background(), dp); err != nil || len(pending) != 0 {
		t.Fatalf("zero-plugin till must not queue retries, got %+v (err %v)", pending, err)
	}
}

// --- background retry ------------------------------------------------------

// A queued unsigned sale is re-signed by the background tick: the pending
// entry drains and a fiscal_signing_resolved audit row lands against the
// same sale. B1 (review of ut-docs#675): the tick must NOT touch
// fiscal.tse_failing_since either way — this mechanism never sets the key
// (ADR-0048 Decision 1), so clearing it on drain would be a false "TSE is
// fine now" signal against whatever OTHER mechanism legitimately set it.
func TestFiscalSignRetry_ResolvesAndLeavesTSEFailingKeyAlone(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-recovered", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	if err := savePendingFiscalSignRetries(ctx, dp, []pendingFiscalSignRetry{{
		SaleID:   "sale-123",
		FailedAt: "2026-08-15T09:00:00Z",
		Payload:  fiscalSignAskPayload{SaleID: "sale-123", Currency: "EUR", Total: 120},
	}}); err != nil {
		t.Fatal(err)
	}
	// Simulate ANOTHER mechanism having legitimately marked the TSE bad
	// (e.g. a future provider-reported-fault signal, or an admin tool).
	const externallySet = "2026-08-15T09:00:00Z"
	if err := dp.Settings.Set(ctx, fiscal.KeyTSEFailingSince, externallySet); err != nil {
		t.Fatal(err)
	}

	fiscalSignRetryTick(ctx, dp)

	if pending, err := loadPendingFiscalSignRetries(ctx, dp); err != nil || len(pending) != 0 {
		t.Fatalf("expected the pending list drained, got %+v (err %v)", pending, err)
	}
	var resolvedID string
	if err := dp.Db.QueryRow(`SELECT entity_id FROM audit_log WHERE entity_type='sale' AND action='fiscal_signing_resolved'`).
		Scan(&resolvedID); err != nil {
		t.Fatalf("expected a fiscal_signing_resolved audit row: %v", err)
	}
	if resolvedID != "sale-123" {
		t.Fatalf("resolved marker on wrong sale: %q", resolvedID)
	}
	if v, _, _ := dp.Settings.Get(ctx, fiscal.KeyTSEFailingSince); v != externallySet {
		t.Fatalf("draining the backlog must not clear a %s value this mechanism never set, got %q", fiscal.KeyTSEFailingSince, v)
	}
}

// Backend still down (a plugin-declared "unreachable" — an authoritative
// backend-level signal, unlike the per-entry protocol failures B3 covers
// below): the tick keeps every entry, stops after the FIRST such re-attempt
// (no point burning the 3s budget once per queued sale against a backend
// that just said it's down), and leaves tse_failing_since alone (B1: this
// mechanism neither sets nor clears it).
func TestFiscalSignRetry_BackendStillDownKeepsPending(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	var invocations atomic.Int64
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-stilldown", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		invocations.Add(1)
		return json.RawMessage(`{"status":"unreachable"}`), nil
	})
	if err := savePendingFiscalSignRetries(ctx, dp, []pendingFiscalSignRetry{
		{SaleID: "sale-1", FailedAt: "2026-08-15T09:00:00Z", Payload: fiscalSignAskPayload{SaleID: "sale-1"}},
		{SaleID: "sale-2", FailedAt: "2026-08-15T09:01:00Z", Payload: fiscalSignAskPayload{SaleID: "sale-2"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := dp.Settings.Set(ctx, fiscal.KeyTSEFailingSince, "2026-08-15T09:00:00Z"); err != nil {
		t.Fatal(err)
	}

	fiscalSignRetryTick(ctx, dp)

	if n := invocations.Load(); n != 1 {
		t.Fatalf("tick must stop after the first failed re-attempt, got %d invocations", n)
	}
	if pending, err := loadPendingFiscalSignRetries(ctx, dp); err != nil || len(pending) != 2 {
		t.Fatalf("expected both entries kept, got %+v (err %v)", pending, err)
	}
	if v, _, _ := dp.Settings.Get(ctx, fiscal.KeyTSEFailingSince); v == "" {
		t.Fatal("tse_failing_since (set here by something else) must be left untouched by the tick")
	}
}

// B3 (review of ut-docs#675): a PROTOCOL-level failure on one queue entry —
// the plugin answered in-budget, but with a status core doesn't recognize
// (e.g. a backend that now reports "duplicate" for a sale that actually got
// signed during the outage) — must not starve the entries behind it. Only a
// genuine backend-level signal (transport error, budget timeout, declared
// "unreachable") may abort the tick early; a per-entry protocol problem
// keeps that entry queued and CONTINUES with the rest in the same tick.
func TestFiscalSignRetry_ProtocolFailureDoesNotStarveQueue(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	var asked []string
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-confused", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p fiscalSignAskPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Errorf("unmarshal ask payload: %v", err)
		}
		asked = append(asked, p.SaleID)
		if p.SaleID == "sale-1" {
			// Unknown status — a per-entry protocol answer, not backend-down.
			return json.RawMessage(`{"status":"duplicate"}`), nil
		}
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	if err := savePendingFiscalSignRetries(ctx, dp, []pendingFiscalSignRetry{
		{SaleID: "sale-1", FailedAt: "2026-08-15T09:00:00Z", Payload: fiscalSignAskPayload{SaleID: "sale-1"}},
		{SaleID: "sale-2", FailedAt: "2026-08-15T09:01:00Z", Payload: fiscalSignAskPayload{SaleID: "sale-2"}},
		{SaleID: "sale-3", FailedAt: "2026-08-15T09:02:00Z", Payload: fiscalSignAskPayload{SaleID: "sale-3"}},
	}); err != nil {
		t.Fatal(err)
	}

	fiscalSignRetryTick(ctx, dp)

	if len(asked) != 3 || asked[0] != "sale-1" || asked[1] != "sale-2" || asked[2] != "sale-3" {
		t.Fatalf("entries 2 and 3 must still be attempted in the SAME tick after entry 1's protocol failure, asked: %v", asked)
	}
	pending, err := loadPendingFiscalSignRetries(ctx, dp)
	if err != nil || len(pending) != 1 || pending[0].SaleID != "sale-1" {
		t.Fatalf("expected only the protocol-failing entry kept queued, got %+v (err %v)", pending, err)
	}
	var resolved int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE entity_type='sale' AND action='fiscal_signing_resolved'`).Scan(&resolved); err != nil {
		t.Fatal(err)
	}
	if resolved != 2 {
		t.Fatalf("expected sales 2 and 3 resolved this tick, got %d resolved markers", resolved)
	}
}

// A plugin handler error that is NOT a budget timeout (e.g. a wasm guest
// trap on one specific payload) must be classified entry-level, same as an
// unparseable/unknown response — it proves nothing about the OTHER queued
// sales, so it must not starve them either. Only a genuine
// context.DeadlineExceeded is backend-level (fiscal_sign_hook.go's
// askFiscalSign). Regression test for the residual gap the 2026-08-15
// scoped re-review flagged: originally every bus.Ask error, deadline or
// not, was lumped into fiscalSignFailedBackend.
func TestFiscalSignRetry_HandlerErrorDoesNotStarveQueue(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	var asked []string
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-buggy", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p fiscalSignAskPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Errorf("unmarshal ask payload: %v", err)
		}
		asked = append(asked, p.SaleID)
		if p.SaleID == "sale-1" {
			// A real handler error, well within budget — not a timeout.
			return nil, errors.New("guest trapped on this payload")
		}
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	if err := savePendingFiscalSignRetries(ctx, dp, []pendingFiscalSignRetry{
		{SaleID: "sale-1", FailedAt: "2026-08-15T09:00:00Z", Payload: fiscalSignAskPayload{SaleID: "sale-1"}},
		{SaleID: "sale-2", FailedAt: "2026-08-15T09:01:00Z", Payload: fiscalSignAskPayload{SaleID: "sale-2"}},
	}); err != nil {
		t.Fatal(err)
	}

	fiscalSignRetryTick(ctx, dp)

	if len(asked) != 2 || asked[0] != "sale-1" || asked[1] != "sale-2" {
		t.Fatalf("a non-timeout handler error on entry 1 must not starve entry 2, asked: %v", asked)
	}
	pending, err := loadPendingFiscalSignRetries(ctx, dp)
	if err != nil || len(pending) != 1 || pending[0].SaleID != "sale-1" {
		t.Fatalf("expected only the erroring entry kept queued, got %+v (err %v)", pending, err)
	}
}

// No signer subscribed (uninstalled/broken): the tick keeps the backlog and
// never dispatches.
func TestFiscalSignRetry_NoSignerKeepsPending(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	if err := savePendingFiscalSignRetries(ctx, dp, []pendingFiscalSignRetry{
		{SaleID: "sale-1", FailedAt: "2026-08-15T09:00:00Z", Payload: fiscalSignAskPayload{SaleID: "sale-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	fiscalSignRetryTick(ctx, dp)
	if pending, err := loadPendingFiscalSignRetries(ctx, dp); err != nil || len(pending) != 1 {
		t.Fatalf("expected the entry kept with no signer, got %+v (err %v)", pending, err)
	}
}

// --- exclusivity (ADR-0041 Decision B) -------------------------------------

// Enabling a second plugin that also declares fiscal.sign.ask while one is
// already active is refused with a 409 naming the owning plugin, and the
// second plugin stays inactive.
func TestFiscalSignExclusivity_SecondActiveSignerRefused(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)

	seedFiscalSignPluginRows(t, dp, "com.test.signer-a", true)
	seedFiscalSignPluginRows(t, dp, "com.test.signer-b", false)

	req := httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.signer-b/enable", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 enabling a second fiscal.sign.ask answerer, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "com.test.signer-a") {
		t.Fatalf("refusal must name the owning plugin, got: %s", rec.Body.String())
	}
	var active int
	if err := dp.Db.QueryRow(`SELECT is_active FROM plugins WHERE id='com.test.signer-b'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("the refused plugin must stay inactive")
	}
}

// B2 (review of ut-docs#675): the enable-time exclusivity check must fail
// CLOSED — a DB error while checking whether the plugin declares (or who
// owns) fiscal.sign.ask refuses the enable and surfaces the error, it never
// silently skips the check and activates a second answerer on an exclusive,
// compliance-relevant point.
func TestFiscalSignExclusivity_EnableFailsClosedOnDBError(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)

	// seedForPages' plugins table carries updated_at since ut-docs#625, so
	// the real SetPluginActive stamp (which the pre-fix, fail-open enable
	// path would need to genuinely SUCCEED) is already available -- no
	// ALTER TABLE workaround needed here anymore.
	seedFiscalSignPluginRows(t, dp, "com.test.signer-a", true)
	seedFiscalSignPluginRows(t, dp, "com.test.signer-b", false)
	// Break the hook lookup: both HasActiveHook and ActiveHookOwner read
	// plugin_hooks, so dropping it makes the exclusivity check itself error.
	if _, err := dp.Db.Exec(`DROP TABLE plugin_hooks`); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/plugins/com.test.signer-b/enable", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("a DB error during the exclusivity check must refuse the enable (fail closed), got %d: %s", rec.Code, rec.Body.String())
	}
	var active int
	if err := dp.Db.QueryRow(`SELECT is_active FROM plugins WHERE id='com.test.signer-b'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("the plugin must stay inactive when the check could not run")
	}
}

// B4 (review of ut-docs#675): each payment's reported amount is NET of the
// change handed back — a €20.00 cash tender against a €12.00 sale reports
// €12.00 collected, exactly what netPayments/CompleteSale/the receipt all
// record for the same sale. Reporting the gross tender would corrupt the
// signed record's payment-type breakdown.
func TestFiscalSignPayload_PaymentAmountNetsChangeGiven(t *testing.T) {
	in := &pos.SaleInput{
		SaleID:   "sale-change",
		Currency: "EUR",
		Lines: []pos.SaleLineInput{
			{Name: "Thing", Qty: 1, UnitPrice: money.FromMinor(1200), TaxRateBasisPoints: 1900},
		},
		Payments: []pos.PaymentInput{{
			MethodID:    "cash",
			Amount:      money.FromMinor(2000),
			ChangeGiven: money.FromMinor(800),
		}},
	}
	payload := buildFiscalSignPayload(in, time.Date(2026, 8, 15, 10, 0, 0, 0, time.UTC))
	if len(payload.Payments) != 1 {
		t.Fatalf("expected 1 payment, got %+v", payload.Payments)
	}
	if got := payload.Payments[0].Amount; got != 1200 {
		t.Fatalf("payment amount must net ChangeGiven (2000-800=1200 minor units), got %d", got)
	}
}

// ut-docs#834 (contract 1.2.0): the payload must carry an explicit
// tax_inclusive flag rather than leaving a signer to infer the pricing mode
// by testing which reading of net/tax reconciles with Total.
func TestFiscalSignPayload_TaxInclusiveFlagMirrorsSaleInput(t *testing.T) {
	cases := []struct {
		name         string
		taxInclusive bool
	}{
		{name: "exclusive pricing", taxInclusive: false},
		{name: "inclusive pricing (German norm)", taxInclusive: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := &pos.SaleInput{
				SaleID:       "sale-tax-inclusive",
				Currency:     "EUR",
				TaxInclusive: tc.taxInclusive,
				Lines: []pos.SaleLineInput{
					{Name: "Thing", Qty: 1, UnitPrice: money.FromMinor(1190), TaxRateBasisPoints: 1900},
				},
			}
			payload := buildFiscalSignPayload(in, time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC))
			if payload.TaxInclusive != tc.taxInclusive {
				t.Fatalf("payload.TaxInclusive = %v, want %v", payload.TaxInclusive, tc.taxInclusive)
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]any
			if err := json.Unmarshal(raw, &wire); err != nil {
				t.Fatal(err)
			}
			if got, ok := wire["tax_inclusive"]; !ok || got != tc.taxInclusive {
				t.Fatalf("wire payload tax_inclusive = %v (present=%v), want %v", got, ok, tc.taxInclusive)
			}
		})
	}
}

// ut-docs#834 (contract 1.2.0): sale-level discount/service charge are
// folded into Total but never reflected in VATBreakdown (which is aggregated
// purely from lines) — a signer needs the raw amounts to apportion them
// across rates itself. Zero amounts stay omitted (omitempty) so an existing
// signer that never reads these two fields sees no shape change on the
// common no-sale-level-adjustment sale.
func TestFiscalSignPayload_SaleDiscountAndServiceChargeBreakout(t *testing.T) {
	in := &pos.SaleInput{
		SaleID:        "sale-discount-service",
		Currency:      "EUR",
		SaleDiscount:  money.FromMinor(200),
		ServiceCharge: money.FromMinor(150),
		Lines: []pos.SaleLineInput{
			{Name: "Thing", Qty: 1, UnitPrice: money.FromMinor(1000), TaxRateBasisPoints: 1900},
		},
	}
	payload := buildFiscalSignPayload(in, time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC))
	if payload.SaleDiscount != 200 {
		t.Fatalf("payload.SaleDiscount = %d, want 200", payload.SaleDiscount)
	}
	if payload.ServiceCharge != 150 {
		t.Fatalf("payload.ServiceCharge = %d, want 150", payload.ServiceCharge)
	}
	// Pin the reconciliation the contract doc promises (exclusive pricing:
	// total = subtotal - sale_discount + service_charge + tax-on-subtotal):
	// 1000 - 200 + 150 + 190 (19% of the undiscounted 1000 line net) = 1140.
	// Catches future drift between buildFiscalSignPayload and
	// pos.computeSaleTotals (ut-docs#834's review, NIT 9).
	if payload.Total != 1140 {
		t.Fatalf("payload.Total = %d, want 1140", payload.Total)
	}
	// The non-zero amounts must actually reach the wire under their
	// contract-documented snake_case keys, not just the Go struct fields —
	// a wrong/renamed json tag would pass the Go-field assertions above but
	// silently break every real consumer (ut-docs#834's review,
	// SHOULD-FIX 6).
	rawNonZero, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var wireNonZero map[string]any
	if err := json.Unmarshal(rawNonZero, &wireNonZero); err != nil {
		t.Fatal(err)
	}
	if got := wireNonZero["sale_discount"]; got != float64(200) {
		t.Fatalf(`wire "sale_discount" = %v, want 200`, got)
	}
	if got := wireNonZero["service_charge"]; got != float64(150) {
		t.Fatalf(`wire "service_charge" = %v, want 150`, got)
	}

	// A sale with neither omits both fields on the wire.
	plain := &pos.SaleInput{
		SaleID:   "sale-no-sale-level-adjustments",
		Currency: "EUR",
		Lines: []pos.SaleLineInput{
			{Name: "Thing", Qty: 1, UnitPrice: money.FromMinor(1000), TaxRateBasisPoints: 1900},
		},
	}
	raw, err := json.Marshal(buildFiscalSignPayload(plain, time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if _, ok := wire["sale_discount"]; ok {
		t.Fatalf("expected sale_discount omitted on the wire when zero, got %v", wire["sale_discount"])
	}
	if _, ok := wire["service_charge"]; ok {
		t.Fatalf("expected service_charge omitted on the wire when zero, got %v", wire["service_charge"])
	}
}

// ut-docs#834's review (SHOULD-FIX 3): an entry a PRE-1.2.0 build queued
// carries no ContractVersion, so its persisted TaxInclusive is a Go zero
// value, not real sale-time data. Replaying it verbatim would emit a
// confidently WRONG tax_inclusive=false for an inclusive-pricing (German)
// shop. The tick must refresh such a legacy entry's TaxInclusive from the
// CURRENTLY configured pricing mode before replay, while leaving a
// current-version entry's genuine (possibly also false) value untouched.
func TestFiscalSignRetry_LegacyEntryRefreshesTaxInclusiveFromCurrentConfig(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	dp.UpdateState(func(s *common.RuntimeState) { s.TaxInclusive = true })

	var got []fiscalSignAskPayload
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-legacy", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		var p fiscalSignAskPayload
		if err := json.Unmarshal(ev.Payload, &p); err != nil {
			t.Fatal(err)
		}
		got = append(got, p)
		return json.RawMessage(`{"status":"approved"}`), nil
	})
	if err := savePendingFiscalSignRetries(ctx, dp, []pendingFiscalSignRetry{
		// No ContractVersion set -- simulates an entry a pre-1.2.0 build
		// persisted, where TaxInclusive: false is a zero value, not data.
		{SaleID: "sale-legacy", FailedAt: "2026-08-15T09:00:00Z",
			Payload: fiscalSignAskPayload{SaleID: "sale-legacy", TaxInclusive: false}},
		// Current-version entry: TaxInclusive: false is real sale-time data
		// and must survive the tick unchanged even though the current
		// config (set above) is true.
		{SaleID: "sale-current", FailedAt: "2026-08-15T09:01:00Z",
			Payload:         fiscalSignAskPayload{SaleID: "sale-current", TaxInclusive: false},
			ContractVersion: fiscalSignContractVersion},
	}); err != nil {
		t.Fatal(err)
	}

	fiscalSignRetryTick(ctx, dp)

	if len(got) != 2 {
		t.Fatalf("expected 2 replayed payloads, got %d: %+v", len(got), got)
	}
	byID := map[string]fiscalSignAskPayload{}
	for _, p := range got {
		byID[p.SaleID] = p
	}
	if !byID["sale-legacy"].TaxInclusive {
		t.Fatal("legacy entry (no ContractVersion) must have TaxInclusive refreshed from the current config (true), got false")
	}
	if byID["sale-current"].TaxInclusive {
		t.Fatal("current-version entry's genuine TaxInclusive=false must not be overwritten by the current config")
	}
}

// --- ut-docs#585: §6 KassenSichV TSE evidence on "approved" (contract 1.1.0)

// The parsed dispatch result carries the optional `tse` evidence through to
// the tender path; a bare {"status":"approved"} (still fully valid, 1.1.0 is
// additive) carries none; an evidence object WITHOUT the signature itself is
// treated as absent (a signature-less object proves nothing worth keeping).
func TestFiscalSignAsk_ApprovedCarriesTSEEvidence(t *testing.T) {
	cases := []struct {
		name     string
		response string
		want     *fiscalTSEEvidence
	}{
		{
			name: "full evidence",
			response: `{"status":"approved","tse":{"transaction_number":4711,"signature_counter":12345,` +
				`"serial_number":"TSE-TEST-SERIAL-1","start_time":"2026-08-15T10:31:00Z",` +
				`"log_time":"2026-08-15T10:31:02Z","signature":"TESTSIGBASE64==",` +
				`"signature_algorithm":"ecdsa-plain-SHA256"}}`,
			want: &fiscalTSEEvidence{
				TransactionNumber:  4711,
				SignatureCounter:   12345,
				SerialNumber:       "TSE-TEST-SERIAL-1",
				StartTime:          "2026-08-15T10:31:00Z",
				LogTime:            "2026-08-15T10:31:02Z",
				Signature:          "TESTSIGBASE64==",
				SignatureAlgorithm: "ecdsa-plain-SHA256",
			},
		},
		{name: "bare approved", response: `{"status":"approved"}`, want: nil},
		{
			name:     "evidence without signature is ignored",
			response: `{"status":"approved","tse":{"transaction_number":1,"serial_number":"x"}}`,
			want:     nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, dp := newFiscalSignDeps(t)
			subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-evidence", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
				return json.RawMessage(tc.response), nil
			})
			in := pos.SaleInput{
				Currency: "EUR",
				Lines:    []pos.SaleLineInput{{Name: "Thing", Qty: 1, UnitPrice: money.FromMinor(100), TaxRateBasisPoints: 2000}},
			}
			res := dispatchFiscalSignAsk(context.Background(), dp, &in)
			if res.Outcome != fiscalSignApproved {
				t.Fatalf("expected fiscalSignApproved, got %v (%s)", res.Outcome, res.Reason)
			}
			if tc.want == nil {
				if res.Evidence != nil {
					t.Fatalf("expected no evidence, got %+v", res.Evidence)
				}
				return
			}
			if res.Evidence == nil {
				t.Fatal("expected the evidence carried through, got nil")
			}
			if *res.Evidence != *tc.want {
				t.Fatalf("evidence mismatch:\n got %+v\nwant %+v", *res.Evidence, *tc.want)
			}
		})
	}
}

// End-to-end through the REAL wazero runtime: a signer answering approved +
// TSE evidence completes the sale clean (no unsigned marker, no retry queue
// — the evidence is additive and changes no outcome), persists the evidence
// keyed on the sale id, and the rendered receipt carries the actual field
// values plus a QR code — never placeholders.
func TestFiscalSignAsk_ApprovedWithTSEEvidencePersistsAndRenders(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	installFiscalSignWasmPlugin(t, dp, "com.test.fiscal-sign-tse", "fiscalsign_tse_guest")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}

	rec := fiscalSignTender(t, mux, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countSales(t, dp); got != 1 {
		t.Fatalf("expected 1 sale, got %d", got)
	}
	// Additive: an approved-with-evidence sale is exactly as clean as a
	// bare-approved one.
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 0 {
		t.Fatalf("approved-with-evidence sale must carry no unsigned_fiscal_signing marker, got %d", n)
	}
	if pending, err := loadPendingFiscalSignRetries(context.Background(), dp); err != nil || len(pending) != 0 {
		t.Fatalf("expected no pending retries, got %+v (err %v)", pending, err)
	}

	// Persisted, keyed on the real sale row's id.
	var saleID string
	if err := dp.Db.QueryRow(`SELECT id FROM sales`).Scan(&saleID); err != nil {
		t.Fatal(err)
	}
	sig, ok, err := data.NewPOSRepo(dp.Db).GetFiscalTSESignature(context.Background(), saleID)
	if err != nil || !ok {
		t.Fatalf("expected persisted TSE evidence for sale %s, ok=%v err=%v", saleID, ok, err)
	}
	if sig.TransactionNumber != 4711 || sig.SignatureCounter != 12345 ||
		sig.SerialNumber != "TSE-TEST-SERIAL-1" || sig.Signature != "TESTSIGBASE64==" ||
		sig.StartTime != "2026-08-15T10:31:00Z" || sig.LogTime != "2026-08-15T10:31:02Z" ||
		sig.SignatureAlgorithm != "ecdsa-plain-SHA256" {
		t.Fatalf("persisted evidence mismatch: %+v", sig)
	}

	// Rendered on the inline HTML receipt: real values + a QR image.
	body := rec.Body.String()
	for _, want := range []string{"TSE-TEST-SERIAL-1", "4711", "12345", "TESTSIGBASE64==", "ecdsa-plain-SHA256", "data:image/png;base64,"} {
		if !strings.Contains(body, want) {
			t.Fatalf("receipt must render TSE evidence value %q, got: %s", want, body)
		}
	}
}

// ut-docs#763: a sale that WAS signed but then failed to persist its §6
// evidence must not go silent — unlike the pre-fix behaviour, it now gets
// the same journal marker + Problems-ring treatment declareUnsignedFiscalSale
// gives an actual signing failure. Forces the failure by dropping the
// evidence table out from under RecordFiscalTSESignature (same DB-error
// injection technique as TestFiscalSignExclusivity_EnableFailsClosedOnDBError)
// rather than mocking the repository — real error, real path.
func TestFiscalSignAsk_EvidencePersistFailureIsObservable(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	installFiscalSignWasmPlugin(t, dp, "com.test.fiscal-sign-tse-fail", "fiscalsign_tse_guest")
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`DROP TABLE fiscal_tse_signatures`); err != nil {
		t.Fatal(err)
	}

	rec := fiscalSignTender(t, mux, false)

	// (0) Never unwinds or blocks the sale — additive observability only.
	if rec.Code != http.StatusOK {
		t.Fatalf("sale must complete despite the evidence persist failure, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := countSales(t, dp); got != 1 {
		t.Fatalf("expected 1 sale, got %d", got)
	}
	var saleID string
	if err := dp.Db.QueryRow(`SELECT id FROM sales`).Scan(&saleID); err != nil {
		t.Fatal(err)
	}
	// The evidence itself really is gone — this is what makes the failure
	// worth flagging in the first place, not just an incidental side effect.
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 0 {
		t.Fatalf("a persist failure is not a signing failure, must carry no unsigned_fiscal_signing marker, got %d", n)
	}

	// (a) journal marker, attached to the real sale row.
	var markerSaleID, markerPayload string
	if err := dp.Db.QueryRow(`SELECT entity_id, data_json FROM audit_log WHERE entity_type='sale' AND action='fiscal_evidence_persist_failed'`).
		Scan(&markerSaleID, &markerPayload); err != nil {
		t.Fatalf("expected a sale/fiscal_evidence_persist_failed audit row: %v", err)
	}
	if markerSaleID != saleID {
		t.Fatalf("marker not attached to the sale: %q != %q", markerSaleID, saleID)
	}
	if !strings.Contains(markerPayload, "no such table") {
		t.Fatalf("marker payload should carry the failure reason, got %s", markerPayload)
	}

	// (c) operator alert in the Problems ring.
	foundProblem := false
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, saleID) && strings.Contains(p.Msg, "evidence failed to persist") {
			foundProblem = true
		}
	}
	if !foundProblem {
		t.Fatalf("expected a Problems-ring warning naming sale %s; recent: %+v", saleID, logging.Recent())
	}
}

// A till with no signer (or a signer that returns no evidence) renders
// NEITHER the TSE block NOR placeholder values — absence is absence.
func TestFiscalSignAsk_NoEvidenceRendersNoTSEBlock(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	rec := fiscalSignTender(t, mux, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	// `receipt-tse"` (closing quote) matches the block's class attribute but
	// not the style sheet's .receipt-tse-* rules, which are always emitted.
	for _, forbidden := range []string{`receipt-tse"`, "TSE serial number", "TSE transaction no."} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("a sale with no TSE evidence must not render the TSE block (%q found): %s", forbidden, body)
		}
	}
}

// A queued unsigned sale re-signed by the background retry with evidence
// gets that evidence persisted too — a recovered sale's later reprints show
// the same §6 field set a live-signed sale's do.
func TestFiscalSignRetry_ResolvedSalePersistsEvidence(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-tse-retry", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"approved","tse":{"transaction_number":99,"signature":"RETRYSIG=="}}`), nil
	})
	if err := savePendingFiscalSignRetries(ctx, dp, []pendingFiscalSignRetry{{
		SaleID:   "sale-retry-1",
		FailedAt: "2026-08-15T09:00:00Z",
		Payload:  fiscalSignAskPayload{SaleID: "sale-retry-1", Currency: "EUR", Total: 120},
	}}); err != nil {
		t.Fatal(err)
	}

	fiscalSignRetryTick(ctx, dp)

	if pending, err := loadPendingFiscalSignRetries(ctx, dp); err != nil || len(pending) != 0 {
		t.Fatalf("expected the pending list drained, got %+v (err %v)", pending, err)
	}
	sig, ok, err := data.NewPOSRepo(dp.Db).GetFiscalTSESignature(ctx, "sale-retry-1")
	if err != nil || !ok {
		t.Fatalf("expected evidence persisted for the retried sale, ok=%v err=%v", ok, err)
	}
	if sig.TransactionNumber != 99 || sig.Signature != "RETRYSIG==" {
		t.Fatalf("retried sale's evidence mismatch: %+v", sig)
	}
}

// Once the owner is disabled, the second signer enables normally — and a
// plugin with no fiscal.sign.ask hook is never affected by the check.
func TestFiscalSignExclusivity_EnableSucceedsWithoutConflict(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)

	// seedForPages' plugins table carries updated_at since ut-docs#625, so
	// the real SetPluginActive stamp is already available -- no ALTER TABLE
	// workaround needed here anymore.
	seedFiscalSignPluginRows(t, dp, "com.test.signer-a", false)
	seedFiscalSignPluginRows(t, dp, "com.test.signer-b", false)
	if _, err := dp.Db.Exec(`INSERT INTO plugins (id, name, version, install_state, entrypoint, runtime, is_active, trust_level)
VALUES ('com.test.no-hook', 'No Hook', '1.0.0', 'installed', './plugin.wasm', 'wasm', 0, 'trusted')`); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{"com.test.signer-b", "com.test.no-hook"} {
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/"+id+"/enable", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 enabling %s with no active conflict, got %d: %s", id, rec.Code, rec.Body.String())
		}
	}
}
