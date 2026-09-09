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
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// seedReceiptPolicyPlugin registers the rows the event bus relies on for a
// country-tax plugin answering receipt.policy.ask (ADR-0089 Decision 2): the
// plugin itself, its events:receive grant, and the hook row — same shape as
// seedChargePolicyPlugin (charge_hook_test.go), including the plugin_catalog
// parent row the composite FK needs (ut-docs#1677).
func seedReceiptPolicyPlugin(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO plugin_catalog (id, version, name, description, runtime, entrypoint, package_url, sha256, author, website, tags_json, min_pos_version, api_version, published_at)
		   VALUES ('com.universaltill.tax-xx', '1.0.0', 'XX Tax', 'desc', 'go', 'entry', 'url', 'sha', 'auth', 'site', '[]', '0.0.0', '1', datetime('now'))`,
		`INSERT INTO plugins (id, name, version, entrypoint, is_active) VALUES ('com.universaltill.tax-xx', 'XX Tax', '1.0.0', 'entry', 1)`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
		   VALUES ('perm-receipt-policy', 'com.universaltill.tax-xx', 'events:receive', 1)`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
		   VALUES ('hook-receipt-policy', 'com.universaltill.tax-xx', 'receipt.policy.ask', 'receipt.policy', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed receipt policy plugin: %v", err)
		}
	}
}

// subscribeReceiptPolicy wires an in-process handler as the one plugin
// answering receipt.policy.ask, returning whatever answer says (nil error)
// or failing with err when non-nil. Resets the shared bus on cleanup.
func subscribeReceiptPolicy(t *testing.T, db *sql.DB, answer string, err error) {
	t.Helper()
	seedReceiptPolicyPlugin(t, db)
	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()
	bus.SetEventMode(receiptPolicyAskEvent, plugins.Blocking)
	if _, subErr := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-xx",
		[]string{receiptPolicyAskEvent},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			if err != nil {
				return nil, err
			}
			return json.RawMessage(answer), nil
		}); subErr != nil {
		t.Fatalf("subscribe: %v", subErr)
	}
}

// --- ADR-0089 Decision 1: the three-way policy, resolved on read ---------

// A shop that never touched the old boolean (unset) keeps today's behaviour:
// every sale auto-prints.
func TestPrinterConfig_ReceiptPolicy_UnsetIsAlways(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	cfg := printerConfig(context.Background(), dp)
	if cfg.ReceiptPolicy != receiptPolicyAlways {
		t.Fatalf("unset policy = %q, want %q", cfg.ReceiptPolicy, receiptPolicyAlways)
	}
	if !cfg.AutoPrint {
		t.Fatal("unset policy must keep AutoPrint=true (bit-for-bit legacy behaviour)")
	}
}

// Back-compat is resolved on READ from the legacy printer.auto_print key —
// no migration: "true" → always, "false" → never.
func TestPrinterConfig_ReceiptPolicy_DerivesFromLegacyAutoPrint(t *testing.T) {
	for legacy, want := range map[string]string{"true": receiptPolicyAlways, "false": receiptPolicyNever} {
		t.Run("auto_print="+legacy, func(t *testing.T) {
			_, dp := newPrintAPITestDeps(t)
			plugins.SharedBus(dp.Db).ResetSubscribers()
			ctx := context.Background()
			if err := dp.Settings.Set(ctx, keyPrinterAuto, legacy); err != nil {
				t.Fatalf("set legacy key: %v", err)
			}
			cfg := printerConfig(ctx, dp)
			if cfg.ReceiptPolicy != want {
				t.Fatalf("legacy auto_print=%s resolved to %q, want %q", legacy, cfg.ReceiptPolicy, want)
			}
			if cfg.AutoPrint != (want == receiptPolicyAlways) {
				t.Fatalf("AutoPrint=%v does not follow the resolved policy %q", cfg.AutoPrint, want)
			}
		})
	}
}

// An explicit printer.receipt_policy wins over whatever the legacy key says.
func TestPrinterConfig_ReceiptPolicy_NewKeyWinsOverLegacy(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	ctx := context.Background()
	if err := dp.Settings.SetMany(ctx, map[string]string{
		keyPrinterAuto:          "true",
		keyPrinterReceiptPolicy: receiptPolicyAsk,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	cfg := printerConfig(ctx, dp)
	if cfg.ReceiptPolicy != receiptPolicyAsk {
		t.Fatalf("policy = %q, want the explicit %q to win over legacy auto_print=true", cfg.ReceiptPolicy, receiptPolicyAsk)
	}
	if cfg.AutoPrint {
		t.Fatal("ask must not auto-print")
	}
}

// A corrupted/unknown stored value is treated exactly like an unset one
// (falls back to the legacy derivation), never left in effect.
func TestPrinterConfig_ReceiptPolicy_GarbageStoredValueFallsBack(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	ctx := context.Background()
	if err := dp.Settings.SetMany(ctx, map[string]string{
		keyPrinterAuto:          "false",
		keyPrinterReceiptPolicy: "sometimes",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := printerConfig(ctx, dp).ReceiptPolicy; got != receiptPolicyNever {
		t.Fatalf("garbage stored policy resolved to %q, want the legacy-derived %q", got, receiptPolicyNever)
	}
}

// --- ADR-0089 Decision 2: the plugin clamp -------------------------------

// A merchant's stored choice outside the plugin's allowed set is clamped at
// read time — preferring "always" when the plugin permits it (the value that
// never skips a receipt), else the plugin's first listed value.
func TestPrinterConfig_ReceiptPolicy_PluginClampsStoredValue(t *testing.T) {
	cases := []struct {
		name    string
		stored  string
		allowed string
		want    string
	}{
		{"ask outside {always} clamps to always", receiptPolicyAsk, `["always"]`, receiptPolicyAlways},
		{"never outside {ask,always} prefers always", receiptPolicyNever, `["ask","always"]`, receiptPolicyAlways},
		{"always outside {never} clamps to first entry", receiptPolicyAlways, `["never"]`, receiptPolicyNever},
		{"always outside {ask,never} clamps to first entry", receiptPolicyAlways, `["ask","never"]`, receiptPolicyAsk},
		{"stored value inside the set is kept", receiptPolicyAsk, `["always","ask"]`, receiptPolicyAsk},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, dp := newPrintAPITestDeps(t)
			ctx := context.Background()
			if err := dp.Settings.Set(ctx, keyPrinterReceiptPolicy, tc.stored); err != nil {
				t.Fatalf("seed: %v", err)
			}
			subscribeReceiptPolicy(t, dp.Db, `{"allowed_policies":`+tc.allowed+`}`, nil)
			cfg := printerConfig(ctx, dp)
			if cfg.ReceiptPolicy != tc.want {
				t.Fatalf("stored %q with allowed %s resolved to %q, want %q", tc.stored, tc.allowed, cfg.ReceiptPolicy, tc.want)
			}
			if cfg.AutoPrint != (tc.want == receiptPolicyAlways) {
				t.Fatalf("AutoPrint=%v does not follow the clamped policy %q", cfg.AutoPrint, tc.want)
			}
		})
	}
}

// No answer, a transient failure, or a malformed/empty answer all mean
// UNRESTRICTED — the merchant's own choice stands (ADR-0050 Decision 2: the
// plugin's absence must never be catastrophic).
func TestPrinterConfig_ReceiptPolicy_UnansweredOrMalformedIsUnrestricted(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		err    error
	}{
		{"handler error", "", errors.New("wasm handler: transient crash")},
		{"garbage", `log line, not JSON`, nil},
		{"empty list", `{"allowed_policies":[]}`, nil},
		{"only unknown values", `{"allowed_policies":["digital","maybe"]}`, nil},
		{"empty response", ``, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, dp := newPrintAPITestDeps(t)
			ctx := context.Background()
			if err := dp.Settings.Set(ctx, keyPrinterReceiptPolicy, receiptPolicyAsk); err != nil {
				t.Fatalf("seed: %v", err)
			}
			subscribeReceiptPolicy(t, dp.Db, tc.answer, tc.err)
			if got := printerConfig(ctx, dp).ReceiptPolicy; got != receiptPolicyAsk {
				t.Fatalf("%s: policy = %q, want the merchant's own %q to stand (unrestricted)", tc.name, got, receiptPolicyAsk)
			}
		})
	}
}

// --- ADR-0089 Decision 3: the interim Germany carve-out ------------------

// store.country == "DE" forces "always" — after the plugin clamp, so it is the
// final word regardless of the stored value AND of any plugin's answer.
func TestPrinterConfig_ReceiptPolicy_GermanyForcesAlways(t *testing.T) {
	for _, country := range []string{"DE", "de", " De "} {
		t.Run(fmt.Sprintf("country=%q", country), func(t *testing.T) {
			_, dp := newPrintAPITestDeps(t)
			ctx := context.Background()
			if err := dp.Settings.SetMany(ctx, map[string]string{
				keyPrinterReceiptPolicy: receiptPolicyNever,
				keyStoreCountry:         country,
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			// A plugin that would only ever permit "never" must still lose.
			subscribeReceiptPolicy(t, dp.Db, `{"allowed_policies":["never"]}`, nil)
			cfg := printerConfig(ctx, dp)
			if cfg.ReceiptPolicy != receiptPolicyAlways {
				t.Fatalf("DE shop resolved to %q, want %q regardless of stored value and plugin answer", cfg.ReceiptPolicy, receiptPolicyAlways)
			}
			if !cfg.AutoPrint {
				t.Fatal("DE shop must auto-print")
			}
		})
	}
}

// Any other country is untouched by the carve-out.
func TestPrinterConfig_ReceiptPolicy_NonGermanyKeepsStoredValue(t *testing.T) {
	_, dp := newPrintAPITestDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	ctx := context.Background()
	if err := dp.Settings.SetMany(ctx, map[string]string{
		keyPrinterReceiptPolicy: receiptPolicyNever,
		keyStoreCountry:         "GB",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if got := printerConfig(ctx, dp).ReceiptPolicy; got != receiptPolicyNever {
		t.Fatalf("GB shop resolved to %q, want the stored %q", got, receiptPolicyNever)
	}
}

// --- pluginReceiptPolicyAsker: the hook itself, at the untrusted-input boundary ---

func TestAskReceiptPolicy_NoSubscribers(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	plugins.SharedBus(db).ResetSubscribers()

	asker := &pluginReceiptPolicyAsker{db: db}
	if allowed, ok := asker.AskReceiptPolicy(context.Background()); ok {
		t.Fatalf("expected no answer with no subscribers, got %v", allowed)
	}
}

func TestAskReceiptPolicy_ValidAnswerParsed(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	subscribeReceiptPolicy(t, db, `{"allowed_policies":["always","never"]}`, nil)

	asker := &pluginReceiptPolicyAsker{db: db}
	allowed, ok := asker.AskReceiptPolicy(context.Background())
	if !ok {
		t.Fatal("expected an answer")
	}
	if strings.Join(allowed, ",") != "always,never" {
		t.Fatalf("allowed = %v, want [always never] in the plugin's own order", allowed)
	}
}

// Unknown values are dropped (logged, not fatal), duplicates and
// whitespace/case variants are folded, and an answer with nothing valid left
// declines cleanly (ok=false → unrestricted).
func TestAskReceiptPolicy_ValidatesPluginInput(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	subscribeReceiptPolicy(t, db, `{"allowed_policies":["digital"," Always ","ask","ASK","always","maybe"]}`, nil)

	asker := &pluginReceiptPolicyAsker{db: db}
	allowed, ok := asker.AskReceiptPolicy(context.Background())
	if !ok {
		t.Fatal("expected an answer")
	}
	if strings.Join(allowed, ",") != "always,ask" {
		t.Fatalf("allowed = %v, want [always ask] (unknowns dropped, dupes folded, order kept)", allowed)
	}
}

func TestAskReceiptPolicy_GarbageErrorAndEmptyDecline(t *testing.T) {
	cases := []struct {
		name   string
		answer string
		err    error
	}{
		{"garbage", `not json`, nil},
		{"handler error", "", errors.New("boom")},
		{"empty list", `{"allowed_policies":[]}`, nil},
		{"only unknown", `{"allowed_policies":["digital"]}`, nil},
		{"wrong type", `{"allowed_policies":"always"}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openPagesTestDB(t)
			defer db.Close()
			seedForPages(t, db)
			subscribeReceiptPolicy(t, db, tc.answer, tc.err)
			asker := &pluginReceiptPolicyAsker{db: db}
			if allowed, ok := asker.AskReceiptPolicy(context.Background()); ok {
				t.Fatalf("%s: expected a clean decline, got %v", tc.name, allowed)
			}
		})
	}
}

// --- pluginReceiptPolicyAsker: per-generation memoization (ut-docs#1924) ---

// An answer is parsed and cached for the bus generation — this hook is
// asked from 12 printerConfig/printerConfigChecked call sites including the
// checkout/tender handler, so a repeat ask within the same generation must
// be a cache hit, not a fresh wasm dispatch.
func TestAskReceiptPolicy_AnswerCachedPerGeneration(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedReceiptPolicyPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	calls := 0
	bus.SetEventMode(receiptPolicyAskEvent, plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-xx",
		[]string{receiptPolicyAskEvent},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{"allowed_policies":["always","ask"]}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginReceiptPolicyAsker{db: db}
	for i := 0; i < 3; i++ {
		allowed, ok := asker.AskReceiptPolicy(context.Background())
		if !ok || strings.Join(allowed, ",") != "always,ask" {
			t.Fatalf("ask %d: got (%v, %v), want ([always ask], true)", i, allowed, ok)
		}
	}
	if calls != 1 {
		t.Fatalf("asked 3x: plugin ran %d times, want 1 (cached per generation)", calls)
	}
}

// A clean no-opinion (nothing recognized survives validation) IS cacheable —
// it costs the same module boot as a real answer, and it's a deterministic
// result for as long as the plugin/generation don't change.
func TestAskReceiptPolicy_NoOpinionIsCachedToo(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedReceiptPolicyPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	calls := 0
	bus.SetEventMode(receiptPolicyAskEvent, plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-xx",
		[]string{receiptPolicyAskEvent},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{"allowed_policies":["digital","maybe"]}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginReceiptPolicyAsker{db: db}
	for i := 0; i < 3; i++ {
		if _, ok := asker.AskReceiptPolicy(context.Background()); ok {
			t.Fatalf("ask %d: expected no-opinion (ok=false)", i)
		}
	}
	if calls != 1 {
		t.Fatalf("declined 3x: plugin ran %d times, want 1 (no-opinion cached)", calls)
	}
}

// A transient handler error, or an answered-but-unparseable response, must
// decline THIS call without being pinned in the cache — the next call
// retries the plugin (same discipline as pluginChargePolicyAsker).
func TestAskReceiptPolicy_ErrorsAndGarbageAreNotCached(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedReceiptPolicyPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	mode := "error"
	calls := 0
	bus.SetEventMode(receiptPolicyAskEvent, plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-xx",
		[]string{receiptPolicyAskEvent},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			calls++
			switch mode {
			case "error":
				return nil, errors.New("wasm handler: transient crash")
			case "garbage":
				return json.RawMessage(`not json`), nil
			default:
				return json.RawMessage(`{"allowed_policies":["never"]}`), nil
			}
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginReceiptPolicyAsker{db: db}
	if _, ok := asker.AskReceiptPolicy(context.Background()); ok {
		t.Fatal("errored ask should decline")
	}
	mode = "garbage"
	if _, ok := asker.AskReceiptPolicy(context.Background()); ok {
		t.Fatal("garbage answer should decline")
	}
	mode = "good"
	if allowed, ok := asker.AskReceiptPolicy(context.Background()); !ok || strings.Join(allowed, ",") != "never" {
		t.Fatalf("recovered ask: got (%v, %v), want ([never], true) (failures were pinned?)", allowed, ok)
	}
	if calls != 3 {
		t.Fatalf("plugin ran %d times, want 3 (neither failure cached)", calls)
	}
}

// A plugin reload (Manager.Reload -> ResetSubscribers, which bumps the bus
// generation) must invalidate the cached answer — a plugin update or
// settings change can legitimately change the policy.
func TestAskReceiptPolicy_ReloadInvalidatesCache(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedReceiptPolicyPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	subscribe := func(policies string) {
		bus.SetEventMode(receiptPolicyAskEvent, plugins.Blocking)
		if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-xx",
			[]string{receiptPolicyAskEvent},
			func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
				return json.RawMessage(`{"allowed_policies":[` + policies + `]}`), nil
			}); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
	}

	asker := &pluginReceiptPolicyAsker{db: db}
	subscribe(`"always"`)
	if allowed, ok := asker.AskReceiptPolicy(context.Background()); !ok || strings.Join(allowed, ",") != "always" {
		t.Fatalf("v1 answer: got (%v, %v), want ([always], true)", allowed, ok)
	}
	bus.ResetSubscribers()
	subscribe(`"never"`)
	if allowed, ok := asker.AskReceiptPolicy(context.Background()); !ok || strings.Join(allowed, ",") != "never" {
		t.Fatalf("post-reload answer: got (%v, %v), want ([never], true) (stale cache?)", allowed, ok)
	}
}

// --- printReceiptAsync honours the resolved policy ------------------------

// "always" prints; "ask" and "never" do NOT auto-print (manual print stays
// available, exactly as the legacy false did).
func TestPrintReceiptAsync_HonoursReceiptPolicy(t *testing.T) {
	for _, policy := range []string{receiptPolicyAlways, receiptPolicyAsk, receiptPolicyNever} {
		t.Run(policy, func(t *testing.T) {
			dp := newPrintFlagTestDeps(t)
			plugins.SharedBus(dp.Db).ResetSubscribers()
			seedReceiptSale(t, dp, "sale-rp-"+policy, "R-RP-"+policy, "sale", "", 120, 0, 0)
			ctx := context.Background()
			if err := dp.Settings.SetMany(ctx, map[string]string{
				keyPrinterMode:          "network",
				keyPrinterAddress:       "unused.invalid:9100", // never dialed: printReceiptFn is stubbed below
				keyPrinterReceiptPolicy: policy,
			}); err != nil {
				t.Fatalf("seed: %v", err)
			}
			restore := printReceiptFn
			t.Cleanup(func() { printReceiptFn = restore })
			printed := 0
			printReceiptFn = func(ctx context.Context, _ *common.Deps, _ string) error {
				printed++
				return nil
			}

			printReceiptAsync(dp, "R-RP-"+policy, "")
			dp.WaitForAsyncWork()

			want := 0
			if policy == receiptPolicyAlways {
				want = 1
			}
			if printed != want {
				t.Fatalf("policy %q: receipt printed %d times, want %d", policy, printed, want)
			}
		})
	}
}

// --- POST /api/settings/printer --------------------------------------------

func TestPostSettingsPrinter_ReceiptPolicyRoundTrips(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	for _, policy := range []string{receiptPolicyAlways, receiptPolicyAsk, receiptPolicyNever} {
		t.Run(policy, func(t *testing.T) {
			mux, dp := newPrintAPITestDeps(t)
			plugins.SharedBus(dp.Db).ResetSubscribers()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/settings/printer",
				strings.NewReader("mode=network&address=192.168.1.50:9100&receiptPolicy="+policy))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
			}
			if got := printerConfig(context.Background(), dp).ReceiptPolicy; got != policy {
				t.Fatalf("expected policy %q to persist, got %q", policy, got)
			}
		})
	}
}

// An omitted/unknown receiptPolicy saves as "always" — the value that never
// skips a receipt — same "unknown → safe default" shape as charset.
func TestPostSettingsPrinter_UnknownReceiptPolicyDefaultsToAlways(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPrintAPITestDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, keyPrinterReceiptPolicy, receiptPolicyNever); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/printer",
		strings.NewReader("mode=off&receiptPolicy=sometimes"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if got := printerConfig(ctx, dp).ReceiptPolicy; got != receiptPolicyAlways {
		t.Fatalf("unknown receiptPolicy stored as %q, want %q", got, receiptPolicyAlways)
	}
}

// A choice outside the installed plugin's allowed set is rejected outright
// (400), not silently stored-then-clamped.
func TestPostSettingsPrinter_RejectsReceiptPolicyOutsidePluginSet(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPrintAPITestDeps(t)
	subscribeReceiptPolicy(t, dp.Db, `{"allowed_policies":["always","ask"]}`, nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/printer",
		strings.NewReader("mode=off&receiptPolicy=never"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a policy the plugin forbids, got %d: %s", rec.Code, rec.Body.String())
	}
	if v, ok, _ := dp.Settings.Get(context.Background(), keyPrinterReceiptPolicy); ok && v != "" {
		t.Fatalf("rejected save must not persist, stored %q", v)
	}

	// The allowed values still save.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/settings/printer",
		strings.NewReader("mode=off&receiptPolicy=ask"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for an allowed policy, got %d: %s", rec.Code, rec.Body.String())
	}
}

// A shop in Germany can only save "always" (ADR-0089 Decision 3).
func TestPostSettingsPrinter_GermanyOnlyAcceptsAlways(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPrintAPITestDeps(t)
	plugins.SharedBus(dp.Db).ResetSubscribers()
	ctx := context.Background()
	if err := dp.Settings.Set(ctx, keyStoreCountry, "DE"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	for _, policy := range []string{receiptPolicyAsk, receiptPolicyNever} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/settings/printer",
			strings.NewReader("mode=off&receiptPolicy="+policy))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("DE shop saving %q: expected 400, got %d: %s", policy, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/printer",
		strings.NewReader("mode=off&receiptPolicy=always"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DE shop saving always: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- Settings page --------------------------------------------------------

// The printer card offers the three-way control, pre-selecting the stored
// policy; for a DE shop it is locked to "always" with the explanatory text.
func TestSettingsPage_ReceiptPolicyControl(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	plugins.SharedBus(d.Db).ResetSubscribers()
	ctx := context.Background()
	if err := d.Settings.Set(ctx, keyPrinterReceiptPolicy, receiptPolicyAsk); err != nil {
		t.Fatalf("seed: %v", err)
	}
	get := func() string {
		req := httptest.NewRequest(http.MethodGet, "/settings", nil)
		req = auth.WithUser(req, mgrUser)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /settings = %d", rec.Code)
		}
		return rec.Body.String()
	}
	body := get()
	if !strings.Contains(body, `name="receiptPolicy"`) {
		t.Fatalf("settings page has no receiptPolicy control:\n%s", body)
	}
	if strings.Contains(body, `name="autoPrint"`) {
		t.Fatal("the legacy autoPrint checkbox must be gone")
	}
	if !strings.Contains(body, `value="ask" selected`) {
		t.Fatalf("stored policy ask is not pre-selected:\n%s", body)
	}
	if strings.Contains(body, "settings.printer.receipt_policy.locked_de") || strings.Contains(body, "receipt-policy-locked") {
		t.Fatal("a non-DE shop must not see the Germany lock")
	}

	if err := d.Settings.Set(ctx, common.KeyCountry, "DE"); err != nil {
		t.Fatalf("set country: %v", err)
	}
	body = get()
	if !strings.Contains(body, "receipt-policy-locked") {
		t.Fatalf("DE shop: expected the locked explanation, got:\n%s", body)
	}
	if !strings.Contains(body, `value="always" selected`) {
		t.Fatalf("DE shop: always must be the selected option:\n%s", body)
	}
	if !strings.Contains(body, `name="receiptPolicy" disabled`) {
		t.Fatalf("DE shop: the control must be disabled:\n%s", body)
	}
}

// Belt and braces for the DE lock: even with a bogus/unknown policy stored
// (or a hand-edited settings row), the settings page's own POST path for a DE
// shop cannot store anything but "always" through the real registered mux.
func TestSettingsPage_GermanyLockSurvivesFormReplay(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	plugins.SharedBus(d.Db).ResetSubscribers()
	registerPrintAPI(mux, d)
	if err := d.Settings.Set(context.Background(), common.KeyCountry, "DE"); err != nil {
		t.Fatalf("set country: %v", err)
	}
	if rec := postForm(mux, "/api/settings/printer", url.Values{"mode": {"off"}, "receiptPolicy": {"never"}}, &mgrUser); rec.Code != http.StatusBadRequest {
		t.Fatalf("DE replay of never = %d, want 400", rec.Code)
	}
}

// --- The sale-completion prompt ------------------------------------------

// Only the "ask" policy renders the prompt block; always/never render the
// receipt exactly as before. The prompt's Print button reuses the very same
// endpoint the existing Print button already posts to, and the existing
// action row is untouched in every case.
func TestRenderReceipt_AskPromptOnlyForAskPolicy(t *testing.T) {
	chdirRoot(t)
	funcs := map[string]any{
		"money":      func(v int64) string { return fmt.Sprintf("$%.2f", float64(v)/100) },
		"barcodesvg": httpx.BarcodeSVG,
		"bpPercent":  func(bp int64) string { return fmt.Sprintf("%.2f%%", float64(bp)/100.0) },
		"T":          func(key string) string { return key },
	}
	lines := []pos.SaleLineInput{{Name: "Apple", Qty: 1, UnitPrice: 100, TaxRateBasisPoints: 0}}
	payments := []pos.PaymentInput{{MethodID: "cash", Amount: 100, Reference: "REF"}}

	render := func(policy string) string {
		html, err := renderReceipt(funcs, "R-ASK", lines, payments, 100, 0, 100, false, 0, "", 0, nil, false, false, false, false, nil, nil, "My Store", receiptDesign{ShowTax: true}, "", nil, policy)
		if err != nil {
			t.Fatalf("renderReceipt(%q): %v", policy, err)
		}
		return html
	}

	ask := render(receiptPolicyAsk)
	// id="receipt-ask" rather than the bare class name: the receipt's own
	// <style> block carries a .receipt-ask rule for every policy.
	for _, want := range []string{`id="receipt-ask"`, "receipt.ask.title", "receipt.ask.paper", "receipt.ask.none"} {
		if !strings.Contains(ask, want) {
			t.Fatalf("ask policy: expected %q in the rendered receipt, got:\n%s", want, ask)
		}
	}
	// The prompt's Print button posts to the same reprint endpoint the
	// existing Print button uses — no new endpoint.
	if strings.Count(ask, "/api/print/receipt/R-ASK") != 2 {
		t.Fatalf("ask policy: expected both the prompt and the existing Print button to post to /api/print/receipt/R-ASK, got:\n%s", ask)
	}
	// The existing action row stays.
	if !strings.Contains(ask, "receipt.new_customer") || !strings.Contains(ask, "receipt.print") {
		t.Fatalf("ask policy: the existing action row must remain, got:\n%s", ask)
	}

	for _, policy := range []string{receiptPolicyAlways, receiptPolicyNever, ""} {
		html := render(policy)
		if strings.Contains(html, `id="receipt-ask"`) || strings.Contains(html, "receipt.ask.title") {
			t.Fatalf("policy %q: prompt block must not render, got:\n%s", policy, html)
		}
		if strings.Count(html, "/api/print/receipt/R-ASK") != 1 {
			t.Fatalf("policy %q: exactly the existing Print button expected, got:\n%s", policy, html)
		}
	}
}
