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
// with NO unsigned_fiscal_signing marker, nothing stored under the legacy
// retry-queue key, no receipt outage notice — the happy path is invisible.
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
	assertNoFiscalSignRetryQueue(t, dp)
	if v, _, _ := dp.Settings.Get(context.Background(), fiscal.KeyTSEFailingSince); v != "" {
		t.Fatalf("approved sale must not mark the TSE failing, got %q", v)
	}
}

// (ii) The signer declares its backend unreachable: the sale completes
// anyway, IS journaled unsigned, DOES get a receipt outage notice, DOES
// raise a Problem — permanently, with nothing queued for any later
// re-attempt (ADR-0056, ut-docs#839) — and does NOT touch
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

	// (b) receipt outage notice on the inline HTML receipt (en copy) — the
	// permanent-gap wording (ADR-0056), never a promise of later signing.
	if !strings.Contains(rec.Body.String(), "TSE unreachable") {
		t.Fatalf("expected the receipt outage notice, got: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "will not be signed later") {
		t.Fatalf("expected the permanent-gap wording on the receipt notice, got: %s", rec.Body.String())
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

	// Nothing queued: the declaration above is the sale's permanent record
	// (ADR-0056, ut-docs#839).
	assertNoFiscalSignRetryQueue(t, dp)

	// B1: even a genuine online failure must NOT stamp tse_failing_since —
	// "unreachable" means "we can't reach it", not "the TSE is known bad"
	// (ADR-0048 Decision 1), and stamping it would hard-block the NEXT sale
	// in a German system-of-record shop over a mere reachability blip.
	if v, _, _ := dp.Settings.Get(context.Background(), fiscal.KeyTSEFailingSince); v != "" {
		t.Fatalf("a fiscal.sign.ask failure must never stamp %s (ADR-0048 D1), got %q", fiscal.KeyTSEFailingSince, v)
	}
}

// ut-docs#835: a signer's explicit "cannot-sign" declares proceed-and-declare
// exactly like "unreachable" does (journal + receipt + operator alert), but
// on its OWN audit action with different, non-outage wording — the notice
// must never suggest a connectivity problem that didn't happen. Like every
// signing failure, the gap is permanent: nothing is queued (ADR-0056).
func TestFiscalSignAsk_CannotSignDeclaresWithDifferentWording(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	logging.ResetRecent()
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-cannotsign", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"status":"cannot-sign"}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}

	rec := fiscalSignTender(t, mux, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("sale must complete despite the signing refusal, got %d: %s", rec.Code, rec.Body.String())
	}
	var saleID string
	if err := dp.Db.QueryRow(`SELECT id FROM sales`).Scan(&saleID); err != nil {
		t.Fatal(err)
	}

	// (a) its OWN journal marker — never the shared outage one.
	if n := countAuditRows(t, dp, "unsigned_fiscal_cannot_sign"); n != 1 {
		t.Fatalf("expected 1 unsigned_fiscal_cannot_sign marker, got %d", n)
	}
	if n := countAuditRows(t, dp, "unsigned_fiscal_signing"); n != 0 {
		t.Fatalf("cannot-sign must not also write the outage marker, got %d", n)
	}

	// (b) receipt notice: the cannot-sign wording, never the outage one.
	if !strings.Contains(rec.Body.String(), "could not be signed as presented") {
		t.Fatalf("expected the cannot-sign receipt notice, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "TSE unreachable") {
		t.Fatalf("cannot-sign must not render the outage wording, got: %s", rec.Body.String())
	}

	// (c) operator alert still fires.
	foundProblem := false
	for _, p := range logging.Recent() {
		if strings.Contains(p.Msg, saleID) && strings.Contains(p.Msg, "UNSIGNED") {
			foundProblem = true
		}
	}
	if !foundProblem {
		t.Fatalf("expected a Problems-ring warning naming sale %s; recent: %+v", saleID, logging.Recent())
	}

	// Nothing queued — the gap is permanent (ADR-0056, ut-docs#839).
	assertNoFiscalSignRetryQueue(t, dp)
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
	// Permanently unsigned — nothing queued for a later re-attempt
	// (ADR-0056, ut-docs#839).
	assertNoFiscalSignRetryQueue(t, dp)
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
	assertNoFiscalSignRetryQueue(t, dp)
}

// assertNoFiscalSignRetryQueue asserts nothing is (or remains) stored under
// the legacy retry-queue settings key: retry-signing was removed outright
// (ADR-0056, ut-docs#839) and no code path may ever write that key again.
func assertNoFiscalSignRetryQueue(t *testing.T, dp *common.Deps) {
	t.Helper()
	if raw, ok, err := dp.Settings.Get(context.Background(), common.KeyPendingFiscalSignRetries); err != nil || (ok && strings.TrimSpace(raw) != "") {
		t.Fatalf("expected no fiscal-sign retry queue (ADR-0056), got %q (ok=%v err=%v)", raw, ok, err)
	}
}

// The acceptance criterion of ut-docs#839/ADR-0056, asserted directly:
// fiscal.sign.ask is dispatched EXACTLY ONCE per sale, at tender time — a
// sale that completes unsigned is never queued and never re-asked
// ("nachträgliche Signierung" is not permitted per fiskaly's SIGN DE
// guidance). If any enqueue-and-retry path is ever reintroduced, the
// invocation count or the queue-key assertion here breaks.
func TestFiscalSignAsk_NeverReDispatchesAfterTenderCompletes(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	var invocations atomic.Int64
	subscribeFiscalSignHandler(t, dp, "com.test.fiscal-sign-once", func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
		invocations.Add(1)
		return json.RawMessage(`{"status":"unreachable"}`), nil
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}

	rec := fiscalSignTender(t, mux, false)
	if rec.Code != http.StatusOK || countSales(t, dp) != 1 {
		t.Fatalf("sale must complete despite the signing failure, code=%d sales=%d", rec.Code, countSales(t, dp))
	}

	// 1. Exactly one dispatch for the sale — the tender-time ask, nothing else.
	if n := invocations.Load(); n != 1 {
		t.Fatalf("fiscal.sign.ask must be dispatched exactly once per sale, got %d invocations", n)
	}
	// 2. Nothing was queued for any later re-attempt.
	assertNoFiscalSignRetryQueue(t, dp)
	// 3. Nothing ever writes fiscal_signing_resolved anymore — the unsigned
	// declaration is the sale's permanent record.
	if n := countAuditRows(t, dp, "fiscal_signing_resolved"); n != 0 {
		t.Fatalf("no code path may write fiscal_signing_resolved (ADR-0056), got %d rows", n)
	}
}

// ADR-0056's one-time boot migration: a retry queue a pre-1.4.0 build left
// under common.KeyPendingFiscalSignRetries is cleared on boot — a stale
// queue must not linger as if something will still happen to it — and the
// clear is idempotent (an empty/absent key is a no-op on every later boot).
// An unparseable leftover is dropped just the same.
func TestDropStaleFiscalSignRetryQueue_ClearsPre140Queue(t *testing.T) {
	_, dp := newFiscalSignDeps(t)
	ctx := context.Background()

	// Absent key: a plain no-op.
	dropStaleFiscalSignRetryQueue(ctx, dp)
	assertNoFiscalSignRetryQueue(t, dp)

	// A pre-1.4.0 queue is dropped.
	if err := dp.Settings.Set(ctx, common.KeyPendingFiscalSignRetries, `[{"sale_id":"sale-old-1"},{"sale_id":"sale-old-2"}]`); err != nil {
		t.Fatal(err)
	}
	dropStaleFiscalSignRetryQueue(ctx, dp)
	assertNoFiscalSignRetryQueue(t, dp)

	// Idempotent: running again on the now-empty key stays a no-op.
	dropStaleFiscalSignRetryQueue(ctx, dp)
	assertNoFiscalSignRetryQueue(t, dp)

	// An unparseable leftover is dropped anyway (best-effort count only).
	if err := dp.Settings.Set(ctx, common.KeyPendingFiscalSignRetries, "not-json{"); err != nil {
		t.Fatal(err)
	}
	dropStaleFiscalSignRetryQueue(ctx, dp)
	assertNoFiscalSignRetryQueue(t, dp)
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

// ut-docs#834 (contract 1.2.0) as amended by ADR-0061 (contract 1.5.0): the
// sale-level discount is folded into Total but never reflected in
// VATBreakdown (a signer still apportions THAT itself); the service charge,
// since 1.5.0, IS apportioned into VATBreakdown by core (its net and its
// tax folded into the existing per-rate lines) — the flat service_charge
// field stays on the wire for display/reconciliation only. Zero amounts
// stay omitted (omitempty) so an existing signer that never reads these two
// fields sees no shape change on the common no-sale-level-adjustment sale.
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
		t.Fatalf("payload.ServiceCharge = %d, want 150 (retained, display-only since 1.5.0)", payload.ServiceCharge)
	}
	// Pin the reconciliation the contract doc promises (exclusive pricing:
	// total = subtotal - sale_discount + service_charge + tax-on-subtotal +
	// tax-on-service-charge): 1000 - 200 + 150 + 190 + 29 (19% of the 150
	// charge, ADR-0061) = 1169. Catches future drift between
	// buildFiscalSignPayload and pos.computeSaleTotals (ut-docs#834's
	// review, NIT 9).
	if payload.Total != 1169 {
		t.Fatalf("payload.Total = %d, want 1169", payload.Total)
	}
	// 1.5.0: the charge's apportioned net/tax are folded into the existing
	// per-rate line, not sent as a separate band or left to the signer.
	if len(payload.VATBreakdown) != 1 {
		t.Fatalf("want 1 vat band, got %+v", payload.VATBreakdown)
	}
	if b := payload.VATBreakdown[0]; b.RateBP != 1900 || b.Net != 1150 || b.Tax != 219 {
		t.Fatalf("1900bp band must fold in the charge (net 1000+150, tax 190+29), got %+v", b)
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

// ADR-0061 Decision 2 / contract 1.5.0: the service charge's apportionment
// in the sign payload must come from the SAME shared function the tender
// path folds into computeSaleTotals — pinned here by asserting the merged
// per-rate lines equal the lines-only aggregate plus exactly
// pos.ApportionServiceChargeTax's bands, across both pricing modes and
// multiple rate bands (net-value weighting, largest remainder on the
// highest band).
func TestFiscalSignPayload_ServiceChargeApportionedIntoVATBreakdown(t *testing.T) {
	lines := []pos.SaleLineInput{
		{Name: "Food", Qty: 1, UnitPrice: money.FromMinor(1000), TaxRateBasisPoints: 1900},
		{Name: "Paper", Qty: 1, UnitPrice: money.FromMinor(500), TaxRateBasisPoints: 700},
	}
	for _, tc := range []struct {
		name         string
		taxInclusive bool
	}{
		{"exclusive", false},
		{"inclusive", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := &pos.SaleInput{
				SaleID:        "sale-charge-bands-" + tc.name,
				Currency:      "EUR",
				TaxInclusive:  tc.taxInclusive,
				ServiceCharge: money.FromMinor(300),
				Lines:         lines,
			}
			payload := buildFiscalSignPayload(in, time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC))

			// Expected: per-line aggregate + the shared function's bands.
			wantBands := map[int]fiscalSignAskVATLine{}
			var lineTax, chargeTax money.Money
			for _, l := range lines {
				net := pos.AmountForQuantity(l.UnitPrice, l.Qty)
				tax, _ := pos.ComputeTaxBasisPoints(net, l.TaxRateBasisPoints, tc.taxInclusive)
				agg := wantBands[l.TaxRateBasisPoints]
				agg.RateBP = l.TaxRateBasisPoints
				agg.Net += net.Minor()
				agg.Tax += tax.Minor()
				wantBands[l.TaxRateBasisPoints] = agg
				lineTax = lineTax.Add(tax)
			}
			for _, b := range pos.ApportionServiceChargeTax(money.FromMinor(300), pos.ChargeTaxLinesFromSale(lines), tc.taxInclusive, 0) {
				agg := wantBands[b.RateBP]
				agg.RateBP = b.RateBP
				agg.Net += b.Amount.Minor()
				agg.Tax += b.Tax.Minor()
				wantBands[b.RateBP] = agg
				chargeTax = chargeTax.Add(b.Tax)
			}
			if len(payload.VATBreakdown) != len(wantBands) {
				t.Fatalf("want %d bands, got %+v", len(wantBands), payload.VATBreakdown)
			}
			for _, got := range payload.VATBreakdown {
				if want := wantBands[got.RateBP]; got != want {
					t.Fatalf("band %d: got %+v, want %+v (charge must merge via the shared apportionment)", got.RateBP, got, want)
				}
			}

			// Total mirrors computeSaleTotals: inclusive is gross-in
			// gross-out; exclusive adds line AND charge tax on top.
			want := money.FromMinor(1500 + 300)
			if !tc.taxInclusive {
				want = want.Add(lineTax).Add(chargeTax)
			}
			if payload.Total != want.Minor() {
				t.Fatalf("payload.Total = %d, want %d", payload.Total, want.Minor())
			}
		})
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
	assertNoFiscalSignRetryQueue(t, dp)

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
