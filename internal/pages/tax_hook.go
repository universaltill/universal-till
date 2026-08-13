package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// taxRateAskEvent is the generic "compute a tax rate override" hook
// (EventBus.Ask). Any installed plugin — country-specific tax rules are
// entirely a plugin's job, core has none built in (see pos.TaxRateAsker) —
// may subscribe to it. The ".ask" suffix is what makes wasm_runtime.go
// dispatch it as a blocking, value-returning hook rather than fire-and-forget.
const taxRateAskEvent = "tax.rate.ask"

// taxRateAskPayload is the event payload a subscribing plugin receives.
// It is the hook's ENTIRE input contract: a plugin's answer must be a pure
// function of these fields, which is what makes the answer cacheable (see
// pluginTaxRateAsker) — a wasm ask boots the plugin's whole module (~90ms
// on a Pi4 for a standard-Go build, ut-docs#222), so recomputeTotals asking
// once per line per recompute made scan/tender latency grow linearly with
// basket size until the till felt seconds-slow at ordinary basket sizes.
type taxRateAskPayload struct {
	ItemID    string `json:"item_id"`
	TaxCodeID string `json:"tax_code_id"`
	TaxRateBP int    `json:"tax_rate_bp"`
	OrderType string `json:"order_type"`
}

// taxRateAskResponse is the JSON a plugin writes to stdout to answer.
type taxRateAskResponse struct {
	RateBP int `json:"rate_bp"`
}

// taxAskAnswer is one cached plugin verdict — an override (ok), a clean
// "no opinion" (!ok), or a fail-closed "blocked" verdict (ut-docs#368: a
// registered tax plugin is broken, so "no opinion" cannot be trusted for
// this payload). All three are deterministic per payload per generation, so
// all three are cached; transport/handler errors are NOT represented here
// (they decline once, uncached, so the next recompute retries the plugin).
type taxAskAnswer struct {
	rateBP  int
	ok      bool
	blocked bool
}

// taxAskCacheMax bounds the cache (catalog items × order types in practice;
// the bound only exists so a pathological catalog can't grow it unchecked).
// On overflow the whole map is dropped — simplest possible eviction, and
// hitting it at all means one full recompute's worth of re-asks, not a wedge.
const taxAskCacheMax = 4096

// pluginTaxRateAsker implements pos.TaxRateAsker by asking installed
// plugins via the event bus — internal/pos itself never depends on the
// plugin subsystem, this is the seam where "does any plugin have an
// opinion on this line's tax rate" is answered.
//
// Answers are memoized per payload for as long as EventBus.Generation is
// unchanged. The generation moves on every path that can change an answer
// without the payload changing: plugin install/update/enable/disable
// (Manager.Reload → WasmRuntime.Sync → ResetSubscribers), a plugin_settings
// save (both shipped tax plugins derive their rate from a setting via the
// settings_get host fn — the settings endpoint and the sync/directive
// rederive path both call BumpGeneration), and permission grant/revoke.
// Inputs that ARE in the payload — the item's tax code and base rate, the
// basket's order type — miss the cache naturally when they change.
//
// The "is any registered tax plugin broken" check (ut-docs#368) rides the
// same generation: WasmRuntime.Sync flips install_state broken↔installed
// AND resets subscribers in the same pass, so a state transition always
// bumps the generation and invalidates both the per-payload cache and the
// memoized broken-plugin lookup below.
type pluginTaxRateAsker struct {
	db *sql.DB

	mu    sync.Mutex
	gen   uint64 // bus generation the cache was filled under
	cache map[taxRateAskPayload]taxAskAnswer
	// brokenKnown/brokenExists memoize ListBrokenPluginsForHook for the
	// current generation, so a till with no (broken) tax plugin pays one
	// cheap query per generation, not one per basket line per recompute.
	brokenKnown  bool
	brokenExists bool
}

// brokenTaxPluginExists reports (memoized per bus generation) whether an
// active plugin registered for tax.rate.ask is currently install_state
// 'broken'. Errors fail closed only when they must: a query error is
// treated as "not known broken" — the pre-#368 behavior — rather than
// blocking every sale on a transient DB hiccup.
func (a *pluginTaxRateAsker) brokenTaxPluginExists(gen uint64) bool {
	a.mu.Lock()
	if a.gen == gen && a.brokenKnown {
		exists := a.brokenExists
		a.mu.Unlock()
		return exists
	}
	a.mu.Unlock()

	rows, err := data.NewPluginRepo(a.db).ListBrokenPluginsForHook(context.Background(), taxRateAskEvent)
	exists := err == nil && len(rows) > 0

	a.mu.Lock()
	if a.gen != gen || a.cache == nil {
		a.gen = gen
		a.cache = make(map[taxRateAskPayload]taxAskAnswer)
		a.brokenKnown = false
	}
	if a.gen == gen && err == nil {
		a.brokenKnown = true
		a.brokenExists = exists
	}
	a.mu.Unlock()
	return exists
}

func (a *pluginTaxRateAsker) AskTaxRateBP(l pos.BasketLine, orderType string) (int, bool, bool) {
	bus := plugins.SharedBus(a.db)
	gen := bus.Generation()
	if !bus.HasSubscribers(taxRateAskEvent) {
		// No live subscriber. Pre-#368 this always meant "no tax plugin at
		// all" — but a broken plugin (binary missing/failed to load) never
		// subscribes either, and its lines must fail closed rather than
		// silently falling back to the base rate (two tills printing
		// different tax for the same item). The lookup is memoized per
		// generation, so the common no-tax-plugin till pays ~nothing.
		if a.brokenTaxPluginExists(gen) {
			return 0, false, true
		}
		return 0, false, false
	}
	payload := taxRateAskPayload{
		ItemID:    l.ItemID,
		TaxCodeID: l.TaxCodeID,
		TaxRateBP: l.TaxRateBP,
		OrderType: orderType,
	}

	a.mu.Lock()
	if a.gen != gen || a.cache == nil {
		a.gen = gen
		a.cache = make(map[taxRateAskPayload]taxAskAnswer)
		a.brokenKnown = false
	}
	if ans, hit := a.cache[payload]; hit {
		a.mu.Unlock()
		return ans.rateBP, ans.ok, ans.blocked
	}
	a.mu.Unlock()

	// Ask outside the lock: a blocking wasm ask is milliseconds-to-~100ms,
	// and holding the lock across it would serialize unrelated lines.
	// Concurrent recomputes may double-ask the same payload; that's benign.
	resp, ok, err := bus.Ask(context.Background(), taxRateAskEvent, payload)
	if err != nil {
		// Transient failure: decline now WITHOUT caching so the next
		// recompute retries — but still fail closed if a broken tax plugin
		// is registered alongside (its absence is what makes the fallback
		// rate untrustworthy, regardless of this sibling's hiccup).
		return 0, false, a.brokenTaxPluginExists(gen)
	}
	ans := taxAskAnswer{}
	if ok {
		var parsed taxRateAskResponse
		if json.Unmarshal(resp, &parsed) != nil {
			// Answered, but with JSON core can't read: a plugin bug the
			// merchant can't see. Decline this recompute WITHOUT caching so
			// the next one retries — unlike a clean empty-response decline,
			// which is a deterministic answer and cacheable.
			return 0, false, a.brokenTaxPluginExists(gen)
		}
		if parsed.RateBP > 0 {
			ans = taxAskAnswer{rateBP: parsed.RateBP, ok: true}
		}
	}
	if !ans.ok {
		// A clean "no opinion" from the live subscribers is only safe when
		// no registered tax plugin is broken: the broken one might be the
		// plugin that owns this line's tax code, and its answer is
		// unknowable until it's restored (ut-docs#368, fail closed).
		ans.blocked = a.brokenTaxPluginExists(gen)
	}

	a.mu.Lock()
	if a.gen == gen && a.cache != nil {
		if len(a.cache) >= taxAskCacheMax {
			a.cache = make(map[taxRateAskPayload]taxAskAnswer)
		}
		a.cache[payload] = ans
	}
	a.mu.Unlock()
	return ans.rateBP, ans.ok, ans.blocked
}
