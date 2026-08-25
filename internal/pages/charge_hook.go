package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"

	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// chargePolicyAskEvent is the "declare your market's service-charge/tip
// policy" hook (ADR-0061 Decision 1; EventBus.Ask, non-exclusive,
// best-effort — same registration shape as tax.rate.ask, and like that hook
// it is governed by its ADR rather than a reference/contracts doc). Any
// installed country-tax plugin may subscribe. The ".ask" suffix is what
// makes wasm_runtime.go dispatch it as a blocking, value-returning hook.
//
// Deliberately unlike tax.rate.ask, there is NO fail-closed blocked state
// here (no ut-docs#368-style broken-plugin check): no answer — no plugin,
// a broken plugin, a transient error — is a NORMAL case, because the safe
// default is always available and always correct to apply (the charge stays
// permitted and is taxed at the sale's own per-line rates,
// pos.ApportionServiceChargeTax). Taxing-by-default can never
// under-declare, so nothing here may ever block a sale (ADR-0061 D1/D2).
const chargePolicyAskEvent = "charge.policy.ask"

// chargePolicyAskPayload is the event payload a subscribing plugin receives
// — deliberately empty: this is a whole-store policy ask (the plugin
// already knows its own country; no per-line or per-sale data is needed),
// asked once per totals recompute, which is also what makes the single
// cached answer below correct for as long as the bus generation stands.
type chargePolicyAskPayload struct{}

// chargePolicyAskResponse is the JSON a plugin writes to stdout to answer.
// ServiceChargePermitted is a pointer so "field absent" (a plugin declaring
// only, say, a tip policy) reads as permitted — the researched-market
// default — rather than a silent false forbidding the charge.
type chargePolicyAskResponse struct {
	ServiceChargePermitted     *bool  `json:"service_charge_permitted"`
	ServiceChargeDefaultRateBP int    `json:"service_charge_default_rate_bp"`
	ServiceChargeTaxBasisBP    int    `json:"service_charge_tax_basis_bp"`
	TipDefaultRecipient        string `json:"tip_default_recipient"`
	FiscalBusinessCase         string `json:"fiscal_business_case"`
}

// chargePolicyAnswer is the cached verdict for one bus generation — a real
// policy (answered) or a clean no-opinion (!answered). Both cost the same
// module boot, so both are cached; transport/handler errors and unparseable
// answers are NOT cached (decline once, retry next recompute) — same
// discipline as pluginTaxRateAsker.
type chargePolicyAnswer struct {
	policy   pos.ChargePolicy
	answered bool
}

// pluginChargePolicyAsker implements pos.ChargePolicyAsker by asking
// installed plugins via the event bus — internal/pos itself never depends
// on the plugin subsystem; this is the seam where "does any plugin have an
// opinion on this market's charge/tip policy" is answered (ADR-0061,
// mirroring pluginTaxRateAsker's shape).
//
// The answer is memoized (one entry — the payload carries no inputs) for as
// long as EventBus.Generation is unchanged. The generation moves on every
// path that can change an answer: plugin install/update/enable/disable,
// a plugin_settings save, and permission grant/revoke — see
// pluginTaxRateAsker's doc comment for the full list.
type pluginChargePolicyAsker struct {
	db *sql.DB

	mu     sync.Mutex
	gen    uint64 // bus generation the cache was filled under
	known  bool
	cached chargePolicyAnswer
}

// AskChargePolicy answers (policy, ok) for the store. ok=false — no
// subscriber, a clean decline, or a transient failure — always means "apply
// core's fail-closed default", never an error surface (ADR-0061 D1).
func (a *pluginChargePolicyAsker) AskChargePolicy() (pos.ChargePolicy, bool) {
	bus := plugins.SharedBus(a.db)
	if !bus.HasSubscribers(chargePolicyAskEvent) {
		// Nobody CAN answer — the zero-plugin fast path: one map lookup,
		// no allocation, no DB access.
		return pos.ChargePolicy{}, false
	}
	gen := bus.Generation()

	a.mu.Lock()
	if a.known && a.gen == gen {
		ans := a.cached
		a.mu.Unlock()
		return ans.policy, ans.answered
	}
	a.mu.Unlock()

	// Ask outside the lock: a blocking wasm ask is milliseconds-to-~100ms,
	// and holding the lock across it would serialize concurrent recomputes.
	// A concurrent double-ask of the same generation is benign.
	resp, ok, err := bus.Ask(context.Background(), chargePolicyAskEvent, chargePolicyAskPayload{})
	if err != nil {
		// Transient failure: decline now, uncached, so the next recompute
		// retries — the fail-closed default applies meanwhile.
		return pos.ChargePolicy{}, false
	}
	ans := chargePolicyAnswer{}
	if ok {
		var parsed chargePolicyAskResponse
		if json.Unmarshal(resp, &parsed) != nil {
			// Answered, but with JSON core can't read: a plugin bug the
			// merchant can't see. Decline this recompute WITHOUT caching so
			// the next one retries — unlike a clean empty-response decline,
			// which is a deterministic answer and cacheable.
			return pos.ChargePolicy{}, false
		}
		ans = chargePolicyAnswer{policy: validateChargePolicy(parsed), answered: true}
	}

	a.mu.Lock()
	if a.gen != gen || !a.known {
		a.gen, a.known = gen, true
	}
	a.cached = ans
	a.mu.Unlock()
	return ans.policy, ans.answered
}

// validateChargePolicy maps a parsed answer into pos.ChargePolicy,
// validating every plugin-supplied field at this boundary (untrusted
// external input): negative rates clamp to 0 (0 = "no default" / "apportion
// per-line"), an unknown tip recipient clamps to no-opinion (core then
// defaults to employee), and an absent permitted field means permitted.
func validateChargePolicy(parsed chargePolicyAskResponse) pos.ChargePolicy {
	policy := pos.ChargePolicy{
		ServiceChargePermitted:     parsed.ServiceChargePermitted == nil || *parsed.ServiceChargePermitted,
		ServiceChargeDefaultRateBP: parsed.ServiceChargeDefaultRateBP,
		ServiceChargeTaxBasisBP:    parsed.ServiceChargeTaxBasisBP,
		FiscalBusinessCase:         parsed.FiscalBusinessCase,
	}
	if policy.ServiceChargeDefaultRateBP < 0 {
		policy.ServiceChargeDefaultRateBP = 0
	}
	if policy.ServiceChargeTaxBasisBP < 0 {
		policy.ServiceChargeTaxBasisBP = 0
	}
	switch parsed.TipDefaultRecipient {
	case pos.TipRecipientEmployee, pos.TipRecipientBusiness:
		policy.TipDefaultRecipient = parsed.TipDefaultRecipient
	}
	return policy
}
