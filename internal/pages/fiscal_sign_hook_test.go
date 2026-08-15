package pages

import (
	"context"
	"encoding/json"
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
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/logging"
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
// raise a Problem, IS queued for background retry, and (genuine failure
// while online) sets fiscal.tse_failing_since.
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

	// Genuine failure while online → tse_failing_since set (ADR-0048 D1).
	if v, _, _ := dp.Settings.Get(context.Background(), fiscal.KeyTSEFailingSince); v == "" {
		t.Fatal("expected fiscal.tse_failing_since set after a genuine signing failure")
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
// entry drains, a fiscal_signing_resolved audit row lands against the same
// sale, and tse_failing_since (set by the original failure) is cleared once
// nothing is left pending.
func TestFiscalSignRetry_ResolvesAndClearsFailingSince(t *testing.T) {
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
	if err := dp.Settings.Set(ctx, fiscal.KeyTSEFailingSince, "2026-08-15T09:00:00Z"); err != nil {
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
	if v, _, _ := dp.Settings.Get(ctx, fiscal.KeyTSEFailingSince); v != "" {
		t.Fatalf("expected tse_failing_since cleared after the backlog drained, got %q", v)
	}
}

// Backend still down: the tick keeps every entry, stops after the FIRST
// failed re-attempt (no point burning the 3s budget once per queued sale
// against a backend that just said no), and leaves tse_failing_since alone.
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
		t.Fatal("tse_failing_since must stay set while the backlog is unresolved")
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

// Once the owner is disabled, the second signer enables normally — and a
// plugin with no fiscal.sign.ask hook is never affected by the check.
func TestFiscalSignExclusivity_EnableSucceedsWithoutConflict(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	mux := http.NewServeMux()
	registerPluginAPI(mux, dp)

	// seedForPages' minimal plugins table lacks updated_at, which the real
	// SetPluginActive stamps — add it so the success path can run.
	if _, err := dp.Db.Exec(`ALTER TABLE plugins ADD COLUMN updated_at TEXT`); err != nil {
		t.Fatal(err)
	}

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
