package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/diagnostics"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// withDiagnosticsTestState isolates the process-wide diagnostics state
// (session flag, ring, pending dir) for one test and leaves no session
// behind for the next.
func withDiagnosticsTestState(t *testing.T) {
	t.Helper()
	orig := diagnostics.PendingDir
	diagnostics.PendingDir = t.TempDir()
	_, _ = diagnostics.Stop(context.Background(), noopKV{}, diagnostics.EndedStopped)
	t.Cleanup(func() {
		_, _ = diagnostics.Stop(context.Background(), noopKV{}, diagnostics.EndedStopped)
		diagnostics.PendingDir = orig
	})
}

type noopKV struct{}

func (noopKV) Get(context.Context, string) (string, bool, error) { return "", false, nil }
func (noopKV) Set(context.Context, string, string) error         { return nil }

// fakeActivateCloud answers /v1/stores/diagnostics/activate with the given
// status/code (200 = success with a fixed session id).
func fakeActivateCloud(t *testing.T, status int, code string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/stores/diagnostics/activate" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": code, "message": "x"}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"session_id": "0f3c2a1b-9d8e-4f7a-b6c5-d4e3f2a1b0c9"}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newDiagnosticsDeps(t *testing.T, cloudURL string) (*http.ServeMux, *common.Deps) {
	t.Helper()
	withDiagnosticsTestState(t)
	mux, _, d := newFullAuthDeps(t)
	d.Cfg.Marketplace.EndpointURL = cloudURL
	d.Cfg.Marketplace.StoreID = "store-1"
	d.Cfg.Marketplace.MerchantToken = "tok-1"
	registerDiagnosticsSettings(mux, d)
	return mux, d
}

func getAs(mux *http.ServeMux, path string, user *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func auditActions(t *testing.T, d *common.Deps, action string) int {
	t.Helper()
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, action).Scan(&n); err != nil {
		t.Fatalf("count audit %s: %v", action, err)
	}
	return n
}

// Activation is gated by the PLAIN "settings" role check (canPerform) that
// every ordinary settings mutation uses — a cashier is refused flat, no
// elevation prompt — and on success the session lands in the settings
// store (so it survives restart), the card re-renders ON, the nav chip is
// pushed out-of-band, and an audit row is written.
func TestDiagnosticsActivate_ManagerGateAndPersistence(t *testing.T) {
	cloud := fakeActivateCloud(t, http.StatusOK, "")
	mux, d := newDiagnosticsDeps(t, cloud.URL)

	rec := postForm(mux, "/api/settings/diagnostics/activate", url.Values{"code": {"abc"}}, &cashUser)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier activate: code=%d body=%s, want 403", rec.Code, rec.Body.String())
	}
	if diagnostics.Active() {
		t.Fatal("cashier activated diagnostics")
	}

	rec = postForm(mux, "/api/settings/diagnostics/activate", url.Values{"code": {" abc123 "}}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager activate: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !diagnostics.Active() {
		t.Fatal("not active after a successful redemption")
	}
	if v, _, _ := d.Settings.Get(t.Context(), "diagnostics.active"); v != "true" {
		t.Fatalf("diagnostics.active = %q, want true (persisted in the settings store)", v)
	}
	if v, _, _ := d.Settings.Get(t.Context(), "diagnostics.session_id"); v != "0f3c2a1b-9d8e-4f7a-b6c5-d4e3f2a1b0c9" {
		t.Fatalf("diagnostics.session_id = %q", v)
	}
	if !strings.Contains(body, `data-testid="diagnostics-on"`) || !strings.Contains(body, "Diagnostic mode: ON since") {
		t.Fatalf("card must render ON with the since-date, got %s", body)
	}
	if !strings.Contains(body, `data-testid="diagnostics-stop"`) {
		t.Fatalf("card must render the local stop control, got %s", body)
	}
	if !strings.Contains(body, `id="diagnostics-chip" hx-swap-oob="innerHTML"`) || !strings.Contains(body, `data-testid="diagnostics-chip-on"`) {
		t.Fatalf("activation must push the nav chip out-of-band, got %s", body)
	}
	if strings.Contains(body, "abc123") {
		t.Fatalf("the activation code must never be echoed back, got %s", body)
	}
	if auditActions(t, d, "diagnostics_activated") != 1 {
		t.Fatal("activation must write an audit row")
	}

	// Inventory events (environment + plugin state) are emitted on
	// activation so the session opens with the till's identity.
	_, events := diagnostics.RecentForIssueReport()
	var sawEnv bool
	for _, raw := range events {
		var obj map[string]any
		_ = json.Unmarshal(raw, &obj)
		if obj["type"] == "environment" {
			sawEnv = true
		}
	}
	if !sawEnv {
		t.Fatalf("activation must emit the environment inventory event, got %d events", len(events))
	}
}

// Each documented failure maps to a localized message on the card; the
// code is never echoed, and nothing is activated.
func TestDiagnosticsActivate_ErrorsAreLocalized(t *testing.T) {
	cases := []struct {
		name   string
		status int
		code   string
		key    string
	}{
		{"invalid", http.StatusForbidden, "invalid_code", "settings.diagnostics.err_invalid_code"},
		{"used", http.StatusConflict, "code_unavailable", "settings.diagnostics.err_code_unavailable"},
		{"unauthorized", http.StatusUnauthorized, "unauthorized", "settings.diagnostics.err_unauthorized"},
		// A documented-but-not-specially-handled cloud error (e.g. this
		// 400) is a real answer from a reachable cloud, so it must NOT
		// map to err_unreachable (that would be false — the cloud did
		// respond) — it maps to the generic err_refused instead
		// (ut-docs#2169 review).
		{"other 4xx", http.StatusBadRequest, "invalid_request", "settings.diagnostics.err_refused"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cloud := fakeActivateCloud(t, tc.status, tc.code)
			mux, _ := newDiagnosticsDeps(t, cloud.URL)
			rec := postForm(mux, "/api/settings/diagnostics/activate", url.Values{"code": {"zzz"}}, &mgrUser)
			if rec.Code != http.StatusOK {
				t.Fatalf("code=%d (an htmx swap target answers 200 with the message)", rec.Code)
			}
			// html/template escapes the apostrophe in "till's", so compare
			// against the escaped form the browser actually receives.
			want := template.HTMLEscapeString(httpx.T("en", tc.key))
			if !strings.Contains(rec.Body.String(), want) || strings.Contains(rec.Body.String(), "zzz") {
				t.Fatalf("body = %s, want %q and no echoed code", rec.Body.String(), want)
			}
			if diagnostics.Active() {
				t.Fatal("activated despite the cloud refusing")
			}
		})
	}

	// Unregistered till: refused before any network call.
	mux, d := newDiagnosticsDeps(t, "http://127.0.0.1:1")
	d.Cfg.Marketplace.MerchantToken = ""
	rec := postForm(mux, "/api/settings/diagnostics/activate", url.Values{"code": {"zzz"}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), httpx.T("en", "settings.diagnostics.err_not_registered")) {
		t.Fatalf("unregistered: %s", rec.Body.String())
	}
	// Blank code: refused before any network call.
	rec = postForm(mux, "/api/settings/diagnostics/activate", url.Values{"code": {"  "}}, &mgrUser)
	if !strings.Contains(rec.Body.String(), httpx.T("en", "settings.diagnostics.err_code_required")) {
		t.Fatalf("blank code: %s", rec.Body.String())
	}

	// Genuinely unreachable (registered, but the endpoint refuses the TCP
	// connection outright — no CloudError at all, unlike every case
	// above where the cloud DID answer with a documented error code).
	// err_unreachable is reserved for exactly this shape (ut-docs#2169
	// review) — none of the answered-4xx cases above may map to it.
	mux2, _ := newDiagnosticsDeps(t, "http://127.0.0.1:1")
	rec2 := postForm(mux2, "/api/settings/diagnostics/activate", url.Values{"code": {"zzz"}}, &mgrUser)
	if !strings.Contains(rec2.Body.String(), httpx.T("en", "settings.diagnostics.err_unreachable")) {
		t.Fatalf("unreachable cloud: %s", rec2.Body.String())
	}
}

// Local stop: first POST shows the disposition (how many unsent batches /
// events will be discarded) and changes nothing; the confirming POST turns
// it off immediately (no cloud involved — the endpoint here is a dead
// port), discards the queue, clears the chip and writes an audit row.
func TestDiagnosticsStop_ShowsDispositionThenDiscards(t *testing.T) {
	mux, d := newDiagnosticsDeps(t, "http://127.0.0.1:1")
	if err := diagnostics.Activate(t.Context(), d.Settings, "sess-local", time.Now()); err != nil {
		t.Fatal(err)
	}
	diagnostics.Emit(diagnostics.Gap{DroppedCount: 1})
	if err := diagnostics.Flush(t.Context(), d.Settings); err != nil {
		t.Fatal(err)
	}
	diagnostics.Emit(diagnostics.Gap{DroppedCount: 2}) // in the ring only

	if rec := postForm(mux, "/api/settings/diagnostics/stop", url.Values{"confirm": {"1"}}, &cashUser); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier stop: code=%d", rec.Code)
	}
	if !diagnostics.Active() {
		t.Fatal("cashier stopped diagnostics")
	}

	rec := postForm(mux, "/api/settings/diagnostics/stop", nil, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop (confirm step): code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="diagnostics-stop-confirm"`) || !strings.Contains(body, "1 unsent batch") || !strings.Contains(body, "2 events") {
		t.Fatalf("confirm step must state the disposition (1 batch, 2 events), got %s", body)
	}
	if !diagnostics.Active() {
		t.Fatal("the confirm step must not stop anything yet")
	}
	if b, _ := diagnostics.PendingSummary(); b != 1 {
		t.Fatal("the confirm step must not discard anything yet")
	}
	// Cancel backs out of the confirmation: still on, nothing discarded.
	rec = postForm(mux, "/api/settings/diagnostics/cancel-stop", nil, &mgrUser)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `data-testid="diagnostics-stop-confirm"`) || !strings.Contains(rec.Body.String(), `data-testid="diagnostics-stop"`) {
		t.Fatalf("cancel-stop: code=%d body=%s, want the plain ON card back", rec.Code, rec.Body.String())
	}
	if !diagnostics.Active() {
		t.Fatal("cancel-stop stopped diagnostics")
	}

	rec = postForm(mux, "/api/settings/diagnostics/stop", url.Values{"confirm": {"1"}}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("stop: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body = rec.Body.String()
	if diagnostics.Active() {
		t.Fatal("still active after the confirmed stop")
	}
	if v, _, _ := d.Settings.Get(t.Context(), "diagnostics.active"); v != "false" {
		t.Fatalf("diagnostics.active = %q, want false", v)
	}
	if pending, _ := diagnostics.Pending(); len(pending) != 0 {
		t.Fatalf("pending batches survived the stop: %+v", pending)
	}
	if !strings.Contains(body, `data-testid="diagnostics-off"`) || !strings.Contains(body, `data-testid="diagnostics-stopped-notice"`) {
		t.Fatalf("card must render OFF with the stopped notice, got %s", body)
	}
	if !strings.Contains(body, `id="diagnostics-chip" hx-swap-oob="innerHTML"></span>`) {
		t.Fatalf("stop must clear the nav chip out-of-band, got %s", body)
	}
	if auditActions(t, d, "diagnostics_stopped") != 1 {
		t.Fatal("stop must write an audit row")
	}
	if v, _, _ := d.Settings.Get(t.Context(), "diagnostics.stop_report"); v != "sess-local" {
		t.Fatalf("stop_report = %q, want the stopped session queued for the next cloudsync tick", v)
	}
}

// The nav chip renders empty while inactive (the chips' shared zero-state
// convention) and, while active, for EVERY role — it is a disclosure that
// capture is on, not a manager control — linking to the Settings card only
// for a session that may open it.
func TestDiagnosticsChip(t *testing.T) {
	mux, d := newDiagnosticsDeps(t, "http://127.0.0.1:1")
	if rec := getAs(mux, "/ui/diagnostics-chip", &cashUser); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("inactive chip: code=%d body=%q, want empty 200", rec.Code, rec.Body.String())
	}
	if err := diagnostics.Activate(t.Context(), d.Settings, "sess-chip", time.Now()); err != nil {
		t.Fatal(err)
	}
	rec := getAs(mux, "/ui/diagnostics-chip", &cashUser)
	body := rec.Body.String()
	if !strings.Contains(body, `data-testid="diagnostics-chip-on"`) || !strings.Contains(body, "nav-badge") {
		t.Fatalf("cashier must still see the active indicator, got %s", body)
	}
	if strings.Contains(body, `href="/settings#settings-diagnostics"`) {
		t.Fatalf("cashier chip must not link to a page that 403s them, got %s", body)
	}
	rec = getAs(mux, "/ui/diagnostics-chip", &mgrUser)
	if !strings.Contains(rec.Body.String(), `href="/settings#settings-diagnostics"`) {
		t.Fatalf("manager chip must link to the Settings card, got %s", rec.Body.String())
	}
}

// The Settings page carries the card (and its sidebar row) for a manager
// only — same gating as the Report-an-issue card.
func TestSettingsPage_DiagnosticsCardGated(t *testing.T) {
	mux, _ := newDiagnosticsDeps(t, "http://127.0.0.1:1")
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); !strings.Contains(body, `id="settings-diagnostics"`) || !strings.Contains(body, `data-testid="diagnostics-off"`) {
		t.Fatalf("manager /settings lacks the diagnostics card")
	}
	if body := getAs(mux, "/settings", &cashUser).Body.String(); strings.Contains(body, `id="settings-diagnostics"`) || strings.Contains(body, "Diagnostic mode") {
		t.Fatalf("cashier /settings leaks the diagnostics card")
	}
}

// diagnostics' order_status vocabulary is restated (not imported) so that
// package stays leaf-level; this pins it to pos's real constants.
func TestOrderStatusEnumMatchesPOS(t *testing.T) {
	want := []string{pos.OrderStatusNew, pos.OrderStatusPreparing, pos.OrderStatusReady, pos.OrderStatusCollected, pos.OrderStatusCancelled}
	got := diagnostics.EnumValues("order_status", "status")
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("diagnostics order_status.status = %v, pos = %v", got, want)
	}
}

// seedTaxRateAskHook registers the seeded UK-VAT test plugin for the
// tax.rate.ask hook (SubscribeWithHandler refuses an event the plugin's
// manifest never declared).
func seedTaxRateAskHook(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
	   VALUES ('hook-tax-ask', 'com.universaltill.tax-uk', 'tax.rate.ask', 'tax.rate', 1)`); err != nil {
		t.Fatalf("seed tax.rate.ask hook: %v", err)
	}
}

func ringEventsOfType(t *testing.T, typ string) []map[string]any {
	t.Helper()
	_, events := diagnostics.RecentForIssueReport()
	var out []map[string]any
	for _, raw := range events {
		var obj map[string]any
		if err := json.Unmarshal(raw, &obj); err != nil {
			t.Fatal(err)
		}
		if obj["type"] == typ {
			out = append(out, obj)
		}
	}
	return out
}

// tax_hook wiring: a cache miss and the following cache hit each emit a
// plugin_ask event carrying the plugin identity, cache flag, generation,
// duration, a correlation id and the outcome CATEGORY — never the payload
// or the response.
func TestTaxHookEmitsPluginAsk(t *testing.T) {
	withDiagnosticsTestState(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedChargePolicyPlugin(t, db) // an installed plugin with events:receive granted
	seedTaxRateAskHook(t, db)
	kv := data.NewSettingsRepo(db)
	if err := diagnostics.Activate(t.Context(), kv, "sess-tax", time.Now()); err != nil {
		t.Fatal(err)
	}
	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk", []string{taxRateAskEvent},
		func(context.Context, plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"rate_bp":700,"debug":"PIN 4321 card tok_4242424242424242"}`), nil
		}); err != nil {
		t.Fatal(err)
	}
	asker := &pluginTaxRateAsker{db: db}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}
	for i := 0; i < 2; i++ {
		if rate, ok, _ := asker.AskTaxRateBP(line, "takeaway"); !ok || rate != 700 {
			t.Fatalf("ask %d: rate=%d ok=%v", i, rate, ok)
		}
	}
	evs := ringEventsOfType(t, "plugin_ask")
	if len(evs) != 2 {
		t.Fatalf("plugin_ask events = %d, want 2 (miss then hit)", len(evs))
	}
	miss, hit := evs[0], evs[1]
	if miss["cache_hit"] != false || hit["cache_hit"] != true {
		t.Fatalf("cache flags: miss=%v hit=%v", miss["cache_hit"], hit["cache_hit"])
	}
	if miss["plugin_id"] != "com.universaltill.tax-uk" || miss["plugin_version"] != "1.0.0" || miss["event"] != taxRateAskEvent {
		t.Fatalf("identity: %v", miss)
	}
	if miss["outcome"] != diagnostics.OutcomeAnswer || hit["outcome"] != diagnostics.OutcomeAnswer {
		t.Fatalf("outcomes: %v / %v", miss["outcome"], hit["outcome"])
	}
	if cid, _ := miss["correlation_id"].(string); cid == "" {
		t.Fatalf("missing correlation id: %v", miss)
	}
	for _, ev := range evs {
		raw, _ := json.Marshal(ev)
		if strings.Contains(string(raw), "4321") || strings.Contains(string(raw), "tok_") || strings.Contains(string(raw), "700") {
			t.Fatalf("plugin_ask leaked payload/response content: %s", raw)
		}
	}
}

// The "clean no_opinion" that motivated ADR-0092 is exactly what must be
// visible: an empty answer is emitted as outcome no_opinion.
func TestTaxHookEmitsNoOpinion(t *testing.T) {
	withDiagnosticsTestState(t)
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedChargePolicyPlugin(t, db)
	seedTaxRateAskHook(t, db)
	if err := diagnostics.Activate(t.Context(), data.NewSettingsRepo(db), "sess-tax2", time.Now()); err != nil {
		t.Fatal(err)
	}
	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk", []string{taxRateAskEvent},
		func(context.Context, plugins.Event) (json.RawMessage, error) { return nil, nil }); err != nil {
		t.Fatal(err)
	}
	asker := &pluginTaxRateAsker{db: db}
	if _, ok, _ := asker.AskTaxRateBP(pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}, "takeaway"); ok {
		t.Fatal("expected no opinion")
	}
	evs := ringEventsOfType(t, "plugin_ask")
	if len(evs) != 1 || evs[0]["outcome"] != diagnostics.OutcomeNoOpinion {
		t.Fatalf("events = %v, want one no_opinion", evs)
	}
}

// Table claim wiring: the write-through helper emits the outcome with the
// table ID only — never the label.
func TestClaimTableWriteThroughEmitsTableAssignment(t *testing.T) {
	withDiagnosticsTestState(t)
	_, dp := newPOSTestDeps(t)
	if err := diagnostics.Activate(t.Context(), dp.Settings, "sess-table", time.Now()); err != nil {
		t.Fatal(err)
	}
	repo := data.NewPOSRepo(dp.Db)
	tableID := createTestTable(t, dp, "Window Seat PIN 4321")
	if claimed, err := claimTableWriteThrough(context.Background(), dp, repo, tableID); err != nil || !claimed {
		t.Fatalf("first claim: %v %v", claimed, err)
	}
	// A second local claim of a table THIS till holds is a refresh (the
	// own-'' row disjunct, ut-docs#1704), so to exercise the refused
	// outcome another, still-live till must hold it: release ours, then
	// let a fresh replica claim it.
	if err := repo.ReleaseTableClaim(context.Background(), tableID); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO tills (id, name, bearer_hash, last_seen_at) VALUES ('till-other', 'Other', 'bh-o', ?)`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if claimed, err := repo.ClaimTableForTill(context.Background(), tableID, "till-other", time.Now().Add(-time.Hour)); err != nil || !claimed {
		t.Fatalf("other till's claim: %v %v", claimed, err)
	}
	if claimed, err := claimTableWriteThrough(context.Background(), dp, repo, tableID); err != nil || claimed {
		t.Fatalf("second claim: claimed=%v err=%v, want refused", claimed, err)
	}
	evs := ringEventsOfType(t, "table_assignment")
	if len(evs) != 2 {
		t.Fatalf("table_assignment events = %d, want 2", len(evs))
	}
	if evs[0]["table_id"] != tableID || evs[0]["outcome"] != diagnostics.TableOutcomeClaimed || evs[0]["via"] != diagnostics.ViaLocal {
		t.Fatalf("first: %v", evs[0])
	}
	if evs[1]["outcome"] != diagnostics.TableOutcomeRefused {
		t.Fatalf("second: %v", evs[1])
	}
	for _, ev := range evs {
		raw, _ := json.Marshal(ev)
		if strings.Contains(string(raw), "Window") || strings.Contains(string(raw), "4321") {
			t.Fatalf("table label leaked: %s", raw)
		}
	}
}

// Order-status wiring: the one-tap status POST emits the change with the
// receipt number and the closed status value.
func TestOrderStatusPostEmitsOrderStatus(t *testing.T) {
	withDiagnosticsTestState(t)
	mux, dp, dbase := newOrderStatusTestDeps(t)
	seedOrderStatusTestSale(t, dbase, "sale-1", "R-0001")
	if err := diagnostics.Activate(t.Context(), dp.Settings, "sess-order", time.Now()); err != nil {
		t.Fatal(err)
	}
	if rec := postOrderStatus(mux, "R-0001", "preparing"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	evs := ringEventsOfType(t, "order_status")
	if len(evs) != 1 || evs[0]["order_id"] != "R-0001" || evs[0]["status"] != "preparing" || evs[0]["applied"] != true || evs[0]["via"] != diagnostics.ViaLocal {
		t.Fatalf("events = %v", evs)
	}
}

// Tax-provenance wiring: folding a catalog-suggested takeaway rate into an
// active override emits one transition per tax code that actually changed.
func TestMergeTakeawayOverridesEmitsTaxProvenance(t *testing.T) {
	withDiagnosticsTestState(t)
	isolatePluginsDir(t)
	db := openRealSchemaPagesDB(t)
	// The plugin row the setting hangs off (FK) — same real PersistManifest
	// path TestReconcileTaxDeTakeawayOverridesOnActivate_Unit uses.
	seedInstalledPlugin(t, db, taxDePluginID, "1.0.0")
	if err := diagnostics.Activate(t.Context(), data.NewSettingsRepo(db), "sess-prov", time.Now()); err != nil {
		t.Fatal(err)
	}
	if added, failed := mergeTakeawayOverrides(context.Background(), db, map[string]int{"tax_std": 700}); failed || added != 1 {
		t.Fatalf("merge: added=%d failed=%v", added, failed)
	}
	// Same key again: add-only, nothing changes, nothing emitted.
	if added, failed := mergeTakeawayOverrides(context.Background(), db, map[string]int{"tax_std": 1900}); failed || added != 0 {
		t.Fatalf("re-merge: added=%d failed=%v", added, failed)
	}
	evs := ringEventsOfType(t, "tax_provenance")
	if len(evs) != 1 || evs[0]["tax_code_id"] != "tax_std" || evs[0]["from"] != diagnostics.ProvenanceCatalogSuggestion || evs[0]["to"] != diagnostics.ProvenanceActiveOverride {
		t.Fatalf("events = %v", evs)
	}
}
