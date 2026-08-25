package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// Compile-time proof the hook satisfies the seam pos wires it into.
var _ pos.ChargePolicyAsker = (*pluginChargePolicyAsker)(nil)

// seedChargePolicyPlugin registers the rows WasmRuntime.Sync relies on for a
// country-tax plugin answering charge.policy.ask: the plugin itself, its
// events:receive grant, and the hook row (subscription is refused without an
// active hook row) — same shape as seedTaxPlugin.
func seedChargePolicyPlugin(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO plugins (id, name, version, is_active) VALUES ('com.universaltill.tax-uk', 'UK VAT', '1.0.0', 1)`,
		`INSERT INTO plugin_permissions (id, plugin_id, permission, granted)
		   VALUES ('perm-charge', 'com.universaltill.tax-uk', 'events:receive', 1)`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
		   VALUES ('hook-charge', 'com.universaltill.tax-uk', 'charge.policy.ask', 'charge.policy', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed charge policy plugin: %v", err)
		}
	}
}

// With no plugin subscribed, the asker declines cleanly — a NORMAL case
// (ADR-0061 Decision 1): not an error, never a blocked sale; the caller
// applies core's fail-closed taxed default.
func TestAskChargePolicy_NoSubscribers(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	plugins.SharedBus(db).ResetSubscribers()

	asker := &pluginChargePolicyAsker{db: db}
	if policy, ok := asker.AskChargePolicy(); ok {
		t.Fatalf("expected no answer with no subscribers, got %+v", policy)
	}
}

// An answer is parsed into pos.ChargePolicy and cached for the bus
// generation — this hook is asked once per totals recompute, and a wasm ask
// boots the whole module (~90ms on a Pi4, ut-docs#222), so a repeat ask
// must be a cache hit.
func TestAskChargePolicy_AnswerParsedAndCached(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedChargePolicyPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	calls := 0
	bus.SetEventMode("charge.policy.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"charge.policy.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			calls++
			return json.RawMessage(`{
				"service_charge_permitted": true,
				"service_charge_default_rate_bp": 1250,
				"service_charge_tax_basis_bp": 2000,
				"tip_default_recipient": "business",
				"fiscal_business_case": "TrinkgeldAG"
			}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginChargePolicyAsker{db: db}
	for i := 0; i < 3; i++ {
		policy, ok := asker.AskChargePolicy()
		if !ok {
			t.Fatalf("ask %d: expected an answer", i)
		}
		if !policy.ServiceChargePermitted ||
			policy.ServiceChargeDefaultRateBP != 1250 ||
			policy.ServiceChargeTaxBasisBP != 2000 ||
			policy.TipDefaultRecipient != pos.TipRecipientBusiness ||
			policy.FiscalBusinessCase != "TrinkgeldAG" {
			t.Fatalf("ask %d: fields not mapped, got %+v", i, policy)
		}
	}
	if calls != 1 {
		t.Fatalf("asked 3x: plugin ran %d times, want 1 (cached per generation)", calls)
	}
}

// Untrusted plugin input is validated at the parse boundary: an unknown
// tip_default_recipient never reaches core (clamped to no-opinion → the
// employee default), a negative rate/basis never reaches core either, and
// an absent service_charge_permitted means permitted (the researched-market
// default) — a plugin declaring only a tip policy must not silently forbid
// the charge.
func TestAskChargePolicy_ValidatesPluginInput(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedChargePolicyPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	bus.SetEventMode("charge.policy.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"charge.policy.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{
				"service_charge_default_rate_bp": -100,
				"service_charge_tax_basis_bp": -700,
				"tip_default_recipient": "the-void"
			}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginChargePolicyAsker{db: db}
	policy, ok := asker.AskChargePolicy()
	if !ok {
		t.Fatal("expected an answer")
	}
	if !policy.ServiceChargePermitted {
		t.Fatalf("absent service_charge_permitted must default to permitted, got %+v", policy)
	}
	if policy.ServiceChargeDefaultRateBP != 0 || policy.ServiceChargeTaxBasisBP != 0 {
		t.Fatalf("negative rates must clamp to 0, got %+v", policy)
	}
	if policy.TipDefaultRecipient != "" {
		t.Fatalf("unknown tip recipient must clamp to no-opinion, got %q", policy.TipDefaultRecipient)
	}
}

// ADR-0062 (ut-docs#963/#984): a plugin's Charges list is validated at the
// same untrusted-input boundary as the legacy scalar fields — a rate
// outside [0, 10000] bp clamps (never drops the item), the reserved
// "service_charge" key is dropped, a duplicate key within the same answer
// is dropped, and an unrecognized base defaults to "net_lines". Order among
// the surviving items is preserved.
func TestAskChargePolicy_ValidatesChargesList(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedChargePolicyPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	bus.SetEventMode("charge.policy.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"charge.policy.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{
				"charges": [
					{"key": "municipality_tax", "label": "Municipality Tax", "default_rate_bp": 500, "tax_basis_bp": 0, "base": "net_lines"},
					{"key": "tourism_tax", "label": "Tourism Tax", "default_rate_bp": 99999},
					{"key": "negative_rate", "default_rate_bp": -50},
					{"key": "service_charge", "label": "sneaky", "default_rate_bp": 100},
					{"key": "municipality_tax", "label": "duplicate", "default_rate_bp": 100},
					{"key": "weird_base", "default_rate_bp": 100, "base": "made_up"}
				]
			}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginChargePolicyAsker{db: db}
	policy, ok := asker.AskChargePolicy()
	if !ok {
		t.Fatal("expected an answer")
	}
	// service_charge (reserved) and the duplicate municipality_tax must both
	// be dropped -- 4 of the original 6 survive.
	if len(policy.Charges) != 4 {
		t.Fatalf("want 4 surviving charges, got %d: %+v", len(policy.Charges), policy.Charges)
	}
	wantKeys := []string{"municipality_tax", "tourism_tax", "negative_rate", "weird_base"}
	for i, want := range wantKeys {
		if policy.Charges[i].Key != want {
			t.Fatalf("charge %d: want key %q, got %q (order not preserved): %+v", i, want, policy.Charges[i].Key, policy.Charges)
		}
	}
	if policy.Charges[0].DefaultRateBP != 500 || policy.Charges[0].Label != "Municipality Tax" || policy.Charges[0].Base != "net_lines" {
		t.Fatalf("municipality_tax not mapped cleanly: %+v", policy.Charges[0])
	}
	if policy.Charges[1].DefaultRateBP != 10000 {
		t.Fatalf("tourism_tax rate 99999 must clamp to 10000 (100%%), got %d", policy.Charges[1].DefaultRateBP)
	}
	if policy.Charges[2].DefaultRateBP != 0 {
		t.Fatalf("negative_rate must clamp to 0, got %d", policy.Charges[2].DefaultRateBP)
	}
	if policy.Charges[3].Base != "net_lines" {
		t.Fatalf("unrecognized base must default to net_lines, got %q", policy.Charges[3].Base)
	}
}

// TaxBasisBP is just as plugin-supplied and applied-verbatim as
// DefaultRateBP, so it gets the same [0, 10000] bp clamp both directions —
// and the reserved/duplicate key checks fold case and trim whitespace,
// treating an empty key (after trimming) as invalid too.
func TestAskChargePolicy_ChargesKeyHygieneAndTaxBasisClamp(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedChargePolicyPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	bus.SetEventMode("charge.policy.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"charge.policy.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{
				"charges": [
					{"key": "  Municipality_Tax  ", "default_rate_bp": 500, "tax_basis_bp": 250000},
					{"key": "municipality_tax", "default_rate_bp": 100},
					{"key": " Service_Charge", "default_rate_bp": 100},
					{"key": "negative_basis", "tax_basis_bp": -700},
					{"key": "   "}
				]
			}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginChargePolicyAsker{db: db}
	policy, ok := asker.AskChargePolicy()
	if !ok {
		t.Fatal("expected an answer")
	}
	// Only the first "Municipality_Tax" (trimmed) and negative_basis survive:
	// the second municipality_tax is a case/whitespace-fold duplicate, the
	// " Service_Charge" entry is a fold of the reserved key, and the
	// whitespace-only key is empty after trimming.
	if len(policy.Charges) != 2 {
		t.Fatalf("want 2 surviving charges, got %d: %+v", len(policy.Charges), policy.Charges)
	}
	if got := policy.Charges[0].Key; got != "Municipality_Tax" {
		t.Fatalf("want trimmed key %q, got %q", "Municipality_Tax", got)
	}
	if got := policy.Charges[0].TaxBasisBP; got != 10000 {
		t.Fatalf("tax_basis_bp 250000 must clamp to 10000 (100%%), got %d", got)
	}
	if got := policy.Charges[1].TaxBasisBP; got != 0 {
		t.Fatalf("negative tax_basis_bp must clamp to 0, got %d", got)
	}
}

// An empty/absent charges array must map to a nil Charges list, not an
// empty-but-non-nil one -- the zero-behavior-change case for every market
// researched except the GCC.
func TestAskChargePolicy_NoChargesIsNil(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedChargePolicyPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	bus.SetEventMode("charge.policy.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"charge.policy.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"service_charge_permitted": true}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginChargePolicyAsker{db: db}
	policy, ok := asker.AskChargePolicy()
	if !ok {
		t.Fatal("expected an answer")
	}
	if policy.Charges != nil {
		t.Fatalf("want nil Charges for a legacy-shaped answer, got %+v", policy.Charges)
	}
}

// A plugin reload (Manager.Reload → ResetSubscribers, which bumps the bus
// generation) must invalidate the cached answer — a plugin update or
// settings change can legitimately change the policy.
func TestAskChargePolicy_ReloadInvalidatesCache(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedChargePolicyPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	subscribe := func(basisBP int) {
		bus.SetEventMode("charge.policy.ask", plugins.Blocking)
		if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
			[]string{"charge.policy.ask"},
			func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
				return json.RawMessage(fmt.Sprintf(`{"service_charge_permitted":true,"service_charge_tax_basis_bp":%d}`, basisBP)), nil
			}); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
	}

	asker := &pluginChargePolicyAsker{db: db}
	subscribe(700)
	if policy, ok := asker.AskChargePolicy(); !ok || policy.ServiceChargeTaxBasisBP != 700 {
		t.Fatalf("v1 answer: got (%+v, %v), want basis 700", policy, ok)
	}
	bus.ResetSubscribers()
	subscribe(2000)
	if policy, ok := asker.AskChargePolicy(); !ok || policy.ServiceChargeTaxBasisBP != 2000 {
		t.Fatalf("post-reload answer: got (%+v, %v), want basis 2000 (stale cache?)", policy, ok)
	}
}

// A transient handler error, or an answered-but-unparseable response, must
// decline THIS recompute without being pinned in the cache — the next
// recompute retries the plugin (same discipline as pluginTaxRateAsker).
func TestAskChargePolicy_ErrorsAndGarbageAreNotCached(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedChargePolicyPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	mode := "error"
	calls := 0
	bus.SetEventMode("charge.policy.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"charge.policy.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			calls++
			switch mode {
			case "error":
				return nil, errors.New("wasm handler: transient crash")
			case "garbage":
				return json.RawMessage(`log line, not JSON`), nil
			default:
				return json.RawMessage(`{"service_charge_permitted":true,"tip_default_recipient":"employee"}`), nil
			}
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginChargePolicyAsker{db: db}
	if _, ok := asker.AskChargePolicy(); ok {
		t.Fatal("errored ask should decline")
	}
	mode = "garbage"
	if _, ok := asker.AskChargePolicy(); ok {
		t.Fatal("garbage answer should decline")
	}
	mode = "good"
	if policy, ok := asker.AskChargePolicy(); !ok || policy.TipDefaultRecipient != pos.TipRecipientEmployee {
		t.Fatalf("recovered ask: got (%+v, %v), want an employee answer (failures were pinned?)", policy, ok)
	}
	if calls != 3 {
		t.Fatalf("plugin ran %d times, want 3 (neither failure cached)", calls)
	}
}

// A clean no-opinion (empty response) IS cacheable — it costs the same
// module boot as an answer, and it is a deterministic result.
func TestAskChargePolicy_NoOpinionIsCachedToo(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedChargePolicyPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	calls := 0
	bus.SetEventMode("charge.policy.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"charge.policy.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			calls++
			return nil, nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginChargePolicyAsker{db: db}
	for i := 0; i < 3; i++ {
		if _, ok := asker.AskChargePolicy(); ok {
			t.Fatalf("ask %d: empty response must be no-opinion", i)
		}
	}
	if calls != 1 {
		t.Fatalf("declined 3x: plugin ran %d times, want 1 (no-opinion cached)", calls)
	}
}
