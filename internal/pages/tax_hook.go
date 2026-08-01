package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"

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
}

func (a *pluginTaxRateAsker) AskTaxRateBP(l pos.BasketLine, orderType string) (int, bool) {
	bus := plugins.SharedBus(a.db)
	if !bus.HasSubscribers(taxRateAskEvent) {
		return 0, false
	}
	payload := taxRateAskPayload{
		ItemID:    l.ItemID,
		TaxCodeID: l.TaxCodeID,
		TaxRateBP: l.TaxRateBP,
		OrderType: orderType,
	}
	gen := bus.Generation()

	a.mu.Lock()
	if a.gen != gen || a.cache == nil {
		a.gen = gen
		a.cache = make(map[taxRateAskPayload]taxAskAnswer)
	}
	if ans, hit := a.cache[payload]; hit {
		a.mu.Unlock()
		return ans.rateBP, ans.ok
	}
	a.mu.Unlock()

	// Ask outside the lock: a blocking wasm ask is milliseconds-to-~100ms,
	// and holding the lock across it would serialize unrelated lines.
	// Concurrent recomputes may double-ask the same payload; that's benign.
	resp, ok, err := bus.Ask(context.Background(), taxRateAskEvent, payload)
	if err != nil {
		return 0, false // transient failure: decline now, retry next recompute
	}
	ans := taxAskAnswer{}
	if ok {
		var parsed taxRateAskResponse
		if json.Unmarshal(resp, &parsed) != nil {
			// Answered, but with JSON core can't read: a plugin bug the
			// merchant can't see. Decline this recompute WITHOUT caching so
			// the next one retries — unlike a clean empty-response decline,
			// which is a deterministic answer and cacheable.
			return 0, false
		}
		if parsed.RateBP > 0 {
			ans = taxAskAnswer{rateBP: parsed.RateBP, ok: true}
		}
	}

	a.mu.Lock()
	if a.gen == gen && a.cache != nil {
		if len(a.cache) >= taxAskCacheMax {
			a.cache = make(map[taxRateAskPayload]taxAskAnswer)
		}
		a.cache[payload] = ans
	}
	a.mu.Unlock()
	return ans.rateBP, ans.ok
}
