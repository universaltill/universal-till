package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
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

// taxAskAnswer is one cached plugin verdict — an override (ok) or a clean
// "no opinion" (!ok). Both cost the same module boot, so both are cached;
// transport/handler errors are NOT represented here (they decline once,
// uncached, so the next recompute retries the plugin).
type taxAskAnswer struct {
	rateBP int
	ok     bool
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
type pluginTaxRateAsker struct {
	db *sql.DB

	mu    sync.Mutex
	gen   uint64 // bus generation the cache was filled under
	cache map[taxRateAskPayload]taxAskAnswer
	// brokenGen/brokenKnown/broken memoize the ut-docs#368 fail-closed
	// check ("is an active plugin registered for tax.rate.ask sitting in
	// install_state='broken'?") for one bus generation — it runs on every
	// non-override answer, per line per recompute, and must not cost a DB
	// query each time on the common no-tax-plugin till. WasmRuntime.Sync
	// bumps the generation AFTER flipping install states, so a cached
	// verdict can never outlive the state it was read under.
	brokenGen   uint64
	brokenKnown bool
	broken      bool
	// cacheMax overrides taxAskCacheMax when non-zero — test-only hook
	// (ut-docs#648) so the overflow-eviction test can exercise the same
	// eviction path with a small bound instead of 4096 real round-trips.
	// Zero (every real construction site, e.g. init.go) keeps production
	// behaviour exactly as before.
	cacheMax int
}

// maxCache returns the cache's real eviction bound: cacheMax when a test
// has overridden it, else the production constant.
func (a *pluginTaxRateAsker) maxCache() int {
	if a.cacheMax > 0 {
		return a.cacheMax
	}
	return taxAskCacheMax
}

// AskTaxRateBP answers (rate, ok, blocked) for one line. blocked=true is
// the ut-docs#368 fail-closed signal: no working plugin produced an answer
// AND an active plugin registered for tax.rate.ask (a manifest-registration
// fact from plugin_hooks — independent of whether it's currently loaded,
// which is exactly why it still answers while the plugin can't subscribe)
// is broken right now. The caller must treat that as "this line cannot be
// rung up", never as the plain "no opinion" fallback.
func (a *pluginTaxRateAsker) AskTaxRateBP(l pos.BasketLine, orderType string) (int, bool, bool) {
	bus := plugins.SharedBus(a.db)
	gen := bus.Generation()
	if !bus.HasSubscribers(taxRateAskEvent) {
		// Nobody CAN answer. Distinguish "no tax plugin exists" (sell
		// normally at base rates) from "the tax plugin exists but its
		// binary is broken" (fail closed).
		return 0, false, a.taxAuthorityBroken(gen)
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
	}
	if ans, hit := a.cache[payload]; hit {
		a.mu.Unlock()
		if ans.ok {
			return ans.rateBP, true, false
		}
		return 0, false, a.taxAuthorityBroken(gen)
	}
	a.mu.Unlock()

	// Ask outside the lock: a blocking wasm ask is milliseconds-to-~100ms,
	// and holding the lock across it would serialize unrelated lines.
	// Concurrent recomputes may double-ask the same payload; that's benign.
	resp, ok, err := bus.Ask(context.Background(), taxRateAskEvent, payload)
	if err != nil {
		// transient failure: decline now, retry next recompute — but still
		// fail closed if a registered tax plugin is broken (a second,
		// working plugin erroring doesn't clear the broken one's block).
		return 0, false, a.taxAuthorityBroken(gen)
	}
	ans := taxAskAnswer{}
	if ok {
		var parsed taxRateAskResponse
		if json.Unmarshal(resp, &parsed) != nil {
			// Answered, but with JSON core can't read: a plugin bug the
			// merchant can't see. Decline this recompute WITHOUT caching so
			// the next one retries — unlike a clean empty-response decline,
			// which is a deterministic answer and cacheable.
			return 0, false, a.taxAuthorityBroken(gen)
		}
		if parsed.RateBP > 0 {
			ans = taxAskAnswer{rateBP: parsed.RateBP, ok: true}
		}
	}

	a.mu.Lock()
	if a.gen == gen && a.cache != nil {
		if len(a.cache) >= a.maxCache() {
			a.cache = make(map[taxRateAskPayload]taxAskAnswer)
		}
		a.cache[payload] = ans
	}
	a.mu.Unlock()
	if ans.ok {
		return ans.rateBP, true, false
	}
	return 0, false, a.taxAuthorityBroken(gen)
}

// taxAuthorityBroken reports whether an ACTIVE plugin registered for
// tax.rate.ask is currently install_state='broken' (ut-docs#368), memoized
// per bus generation. A DB error fails OPEN (not blocked) but is logged —
// it is uncached, so the very next ask retries; wedging checkout on a
// bookkeeping read would trade one failure mode for another, and a DB that
// can't answer a COUNT can't record a sale either.
func (a *pluginTaxRateAsker) taxAuthorityBroken(gen uint64) bool {
	a.mu.Lock()
	if a.brokenKnown && a.brokenGen == gen {
		v := a.broken
		a.mu.Unlock()
		return v
	}
	a.mu.Unlock()

	broken, err := data.NewPluginRepo(a.db).HasBrokenActivePluginForEvent(context.Background(), taxRateAskEvent)
	if err != nil {
		// Fail open, but never silently (round-2 review MINOR): a
		// persistent read failure here disables the whole fail-closed
		// protection, and with no signal nobody would ever know.
		logging.L().Errorf("tax fail-closed check (ut-docs#368): broken-plugin read failed — failing open for this ask: %v", err)
		return false
	}
	a.mu.Lock()
	a.brokenGen, a.brokenKnown, a.broken = gen, true, broken
	a.mu.Unlock()
	return broken
}
