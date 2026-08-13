package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// seedTaxPlugin registers the rows WasmRuntime.Sync relies on for a real
// tax plugin: the plugin itself, its events:receive grant, and its
// tax.rate.ask hook (subscription is refused without an active hook row).
func seedTaxPlugin(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO plugins (id, name, version, is_active) VALUES ('com.universaltill.tax-uk', 'UK VAT', '1.0.0', 1)`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
		   VALUES ('perm-tax', 'com.universaltill.tax-uk', 'events:receive', 1)`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
		   VALUES ('hook-tax', 'com.universaltill.tax-uk', 'tax.rate.ask', 'tax.rate', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed tax plugin: %v", err)
		}
	}
}

func TestAskTaxRateBP_CachesPerPayload(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	seedTaxPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers() // drop any subscriber leaked by another test

	calls := 0
	bus.SetEventMode("tax.rate.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"tax.rate.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{"rate_bp":500}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginTaxRateAsker{db: db}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}

	// The wasm boot behind a real ask costs ~90ms on a Pi4 (ut-docs#222);
	// recomputeTotals asks once per line per recompute, so a repeated
	// payload MUST be answered from cache, not by re-running the plugin.
	for i := 0; i < 3; i++ {
		rate, ok, _ := asker.AskTaxRateBP(line, "eat_in")
		if !ok || rate != 500 {
			t.Fatalf("ask %d: got (%d,%v), want (500,true)", i, rate, ok)
		}
	}
	if calls != 1 {
		t.Fatalf("same payload asked 3x: plugin ran %d times, want 1", calls)
	}

	// A different order type is a different payload — must reach the plugin.
	if _, ok, _ := asker.AskTaxRateBP(line, "takeaway"); !ok {
		t.Fatal("takeaway ask should still resolve")
	}
	if calls != 2 {
		t.Fatalf("distinct payload: plugin ran %d times, want 2", calls)
	}
}

func TestAskTaxRateBP_NoOpinionAnswerIsCachedToo(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	seedTaxPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	calls := 0
	bus.SetEventMode("tax.rate.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"tax.rate.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			calls++
			return nil, nil // plugin ran fine, has no opinion on this line
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginTaxRateAsker{db: db}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}

	// "No opinion" costs the same wasm boot as an override — a till whose
	// plugin declines most items must not pay that boot per line per
	// recompute either.
	for i := 0; i < 3; i++ {
		if rate, ok, _ := asker.AskTaxRateBP(line, "eat_in"); ok || rate != 0 {
			t.Fatalf("ask %d: got (%d,%v), want (0,false)", i, rate, ok)
		}
	}
	if calls != 1 {
		t.Fatalf("declined payload asked 3x: plugin ran %d times, want 1", calls)
	}
}

func TestAskTaxRateBP_PluginReloadInvalidatesCache(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	seedTaxPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	subscribe := func(rateBP int) {
		bus.SetEventMode("tax.rate.ask", plugins.Blocking)
		if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
			[]string{"tax.rate.ask"},
			func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
				return json.RawMessage(fmt.Sprintf(`{"rate_bp":%d}`, rateBP)), nil
			}); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
	}

	asker := &pluginTaxRateAsker{db: db}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}

	subscribe(500)
	if rate, _, _ := asker.AskTaxRateBP(line, "eat_in"); rate != 500 {
		t.Fatalf("v1 answer: got %d, want 500", rate)
	}

	// Plugin updated: Manager.Reload → WasmRuntime.Sync → ResetSubscribers →
	// fresh subscription. The old answer must not survive the reload.
	bus.ResetSubscribers()
	subscribe(1250)
	if rate, _, _ := asker.AskTaxRateBP(line, "eat_in"); rate != 1250 {
		t.Fatalf("post-reload answer: got %d, want 1250 (stale cache?)", rate)
	}
}

func TestAskTaxRateBP_HandlerErrorIsNotCached(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	seedTaxPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	fail := true
	calls := 0
	bus.SetEventMode("tax.rate.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"tax.rate.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			calls++
			if fail {
				return nil, errors.New("wasm handler: transient crash")
			}
			return json.RawMessage(`{"rate_bp":500}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginTaxRateAsker{db: db}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}

	// A transient failure declines this recompute but must NOT be pinned:
	// the next recompute retries the plugin.
	if _, ok, _ := asker.AskTaxRateBP(line, "eat_in"); ok {
		t.Fatal("failed ask should decline")
	}
	fail = false
	if rate, ok, _ := asker.AskTaxRateBP(line, "eat_in"); !ok || rate != 500 {
		t.Fatalf("recovered ask: got (%d,%v), want (500,true)", rate, ok)
	}
	if calls != 2 {
		t.Fatalf("plugin ran %d times, want 2 (error not cached)", calls)
	}
}

// A manager saving a plugin's settings changes what the plugin will answer
// (both shipped tax plugins read a setting inside the ask handler via the
// settings_get host fn) — the save endpoint must drop cached answers, or the
// till keeps charging the old rate until an unrelated restart. Found by the
// independent review of ut-docs#222; this is mischarged tax, not a nicety.
func TestAskTaxRateBP_PluginSettingsSaveInvalidatesCache(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedTaxPlugin(t, dp.Db)
	seedPluginSetting(t, dp, "com.universaltill.tax-uk", "eatin_rate", "2000", "global")

	bus := plugins.SharedBus(dp.Db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	// The handler's answer tracks the (simulated) setting, like the real
	// plugin's settings_get-driven rate does.
	settingRate := 2000
	bus.SetEventMode("tax.rate.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"tax.rate.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(fmt.Sprintf(`{"rate_bp":%d}`, settingRate)), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginTaxRateAsker{db: dp.Db}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}
	if rate, _, _ := asker.AskTaxRateBP(line, "eat_in"); rate != 2000 {
		t.Fatalf("pre-save answer: got %d, want 2000", rate)
	}

	settingRate = 1250
	form := "setting_eatin_rate=1250"
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/com.universaltill.tax-uk/settings", strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings save: code %d body %s", rec.Code, rec.Body.String())
	}

	if rate, _, _ := asker.AskTaxRateBP(line, "eat_in"); rate != 1250 {
		t.Fatalf("post-save answer: got %d, want 1250 (stale cache — settings save must invalidate)", rate)
	}
}

// Revoking events:receive must stop a cached override on the next recompute,
// exactly as it did pre-cache (EventBus.Ask skips permission-denied
// subscribers). Grant gets the same bump (a fresh grant can change answers
// from "decline" to a real override); revoke is the dangerous direction, so
// it's the one pinned by a test.
func TestAskTaxRateBP_PermissionRevokeStopsCachedOverride(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedTaxPlugin(t, db)

	// Build the API mux BEFORE priming the cache: plugins.Init resets the
	// shared bus (bumping the generation), which would mask a missing bump
	// in the revoke handler itself.
	mux := http.NewServeMux()
	registerPluginAPI(mux, newPluginAPIDeps(t, db, nil))

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	bus.SetEventMode("tax.rate.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"tax.rate.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"rate_bp":500}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginTaxRateAsker{db: db}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}
	if rate, ok, _ := asker.AskTaxRateBP(line, "eat_in"); !ok || rate != 500 {
		t.Fatalf("pre-revoke: got (%d,%v), want (500,true)", rate, ok)
	}

	rec := postForm(mux, "/api/plugins/permissions/revoke",
		url.Values{"plugin_id": {"com.universaltill.tax-uk"}, "permission": {"events:receive"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: code %d body %s", rec.Code, rec.Body.String())
	}

	if rate, ok, _ := asker.AskTaxRateBP(line, "eat_in"); ok || rate != 0 {
		t.Fatalf("post-revoke: got (%d,%v), want (0,false) — revoked plugin's cached override survived", rate, ok)
	}
}

// A handler that ANSWERED but with JSON core can't parse is a plugin bug the
// merchant can't see — it declines this recompute but must not be pinned
// (unlike a clean empty-response decline, which is cacheable).
func TestAskTaxRateBP_UnparseableAnswerIsNotCached(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedTaxPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	garbage := true
	calls := 0
	bus.SetEventMode("tax.rate.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"tax.rate.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			calls++
			if garbage {
				return json.RawMessage(`log line, not JSON`), nil
			}
			return json.RawMessage(`{"rate_bp":500}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginTaxRateAsker{db: db}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}
	if _, ok, _ := asker.AskTaxRateBP(line, "eat_in"); ok {
		t.Fatal("garbage answer should decline")
	}
	garbage = false
	if rate, ok, _ := asker.AskTaxRateBP(line, "eat_in"); !ok || rate != 500 {
		t.Fatalf("recovered ask: got (%d,%v), want (500,true) — garbage answer was pinned", rate, ok)
	}
	if calls != 2 {
		t.Fatalf("plugin ran %d times, want 2 (unparseable answer not cached)", calls)
	}
}

// The overflow eviction (full map drop at taxAskCacheMax) must keep answers
// correct, and concurrent asks must be race-free (run under -race in CI).
func TestAskTaxRateBP_OverflowAndConcurrency(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedTaxPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	bus.SetEventMode("tax.rate.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"tax.rate.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			var p taxRateAskPayload
			if err := json.Unmarshal(extractPayload(t, ev), &p); err != nil {
				return nil, err
			}
			return json.RawMessage(fmt.Sprintf(`{"rate_bp":%d}`, p.TaxRateBP+1)), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginTaxRateAsker{db: db}
	// Overflow: one more distinct payload than the cache holds.
	for i := 0; i < taxAskCacheMax+1; i++ {
		line := pos.BasketLine{ItemID: fmt.Sprintf("itm-%d", i), TaxCodeID: "tax_std", TaxRateBP: i + 1}
		if rate, ok, _ := asker.AskTaxRateBP(line, "eat_in"); !ok || rate != i+2 {
			t.Fatalf("ask %d: got (%d,%v), want (%d,true)", i, rate, ok, i+2)
		}
	}
	// Concurrency: hammer one payload from several goroutines.
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			line := pos.BasketLine{ItemID: "itm-hot", TaxCodeID: "tax_std", TaxRateBP: 100}
			for i := 0; i < 50; i++ {
				if rate, ok, _ := asker.AskTaxRateBP(line, "eat_in"); !ok || rate != 101 {
					t.Errorf("concurrent ask: got (%d,%v), want (101,true)", rate, ok)
					return
				}
			}
		}()
	}
	wg.Wait()
}

// extractPayload pulls the raw payload object out of the wire-format event
// JSON a handler receives.
func extractPayload(t *testing.T, ev plugins.Event) []byte {
	t.Helper()
	return ev.Payload
}
