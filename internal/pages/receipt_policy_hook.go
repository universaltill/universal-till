package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"strings"
	"sync"

	"github.com/universaltill/universal-till/internal/plugins"
)

// Receipt policy values (ADR-0089 Decision 1) — what happens to the paper
// receipt when a sale completes. "always" is today's default and the legacy
// printer.auto_print=true behaviour; "never" is the legacy false; "ask"
// prompts the cashier at sale completion (print or don't — paper or none
// only in this iteration, ADR-0089 Decision 4).
const (
	receiptPolicyAlways = "always"
	receiptPolicyAsk    = "ask"
	receiptPolicyNever  = "never"
)

// isReceiptPolicy reports whether s is one of the three recognized values.
func isReceiptPolicy(s string) bool {
	return s == receiptPolicyAlways || s == receiptPolicyAsk || s == receiptPolicyNever
}

// receiptPolicyAskEvent is the "declare which receipt policies your market's
// law permits" hook (ADR-0089 Decision 2; EventBus.Ask, non-exclusive,
// best-effort — same registration shape as charge.policy.ask and
// tax.rate.ask, and like those hooks it is governed by its ADR rather than a
// reference/contracts doc). Any installed country-tax plugin may subscribe.
// The ".ask" suffix is what makes wasm_runtime.go dispatch it as a blocking,
// value-returning hook.
//
// Like charge.policy.ask there is NO fail-closed blocked state here: no
// answer — no plugin, a broken plugin, a transient error, a malformed answer
// — is a NORMAL case and means UNRESTRICTED (all three policies allowed),
// because the merchant's own stored choice is always a valid thing to apply
// and nothing here may ever block a sale or a settings save (ADR-0050
// Decision 2: "the plugin's absence must not be catastrophic").
const receiptPolicyAskEvent = "receipt.policy.ask"

// receiptPolicyAskPayload is the event payload a subscribing plugin receives
// — deliberately empty: a whole-store policy ask (the plugin already knows
// its own country; no per-sale data is needed).
type receiptPolicyAskPayload struct{}

// receiptPolicyAskResponse is the JSON a plugin writes to stdout to answer:
// the subset of {always, ask, never} its market permits, in the plugin's
// own preference order (the first entry is what an out-of-set stored value
// is clamped to when "always" itself is not permitted — see
// clampReceiptPolicy).
type receiptPolicyAskResponse struct {
	AllowedPolicies []string `json:"allowed_policies"`
}

// receiptPolicyAnswer is the cached verdict for one bus generation — a real
// answer or a clean no-opinion (!answered). Both cost the same module boot,
// so both are cached; a transient bus.Ask failure and an answered-but-
// unparseable response are NOT cached (decline this call, retry next
// recompute) — same discipline as pluginChargePolicyAsker.
type receiptPolicyAnswer struct {
	allowed  []string
	answered bool
}

// pluginReceiptPolicyAsker memoizes the receipt.policy.ask hook per bus
// generation — same shape as pluginChargePolicyAsker (charge_hook.go).
// printerConfig/printerConfigChecked has 12 call sites, including the
// tender/checkout handler, kitchen-ticket printing, EOD and invoice
// rendering — not just the settings read/save paths a stale version of this
// comment used to claim. The zero-plugin fast path stays one map lookup
// (HasSubscribers), so this costs nothing while no plugin answers
// receipt.policy.ask yet; once one does (most likely ut-plugin-tax-de, once
// ut-docs#1908 resolves), every one of those call sites would otherwise pay
// a synchronous WASM dispatch plus an event_dispatch audit INSERT
// (EventBus.Ask -> auditDispatch) on its own hot path, including checkout,
// per sale — this memoization is what prevents that (review finding,
// 2026-09-09, ut-docs#1924).
type pluginReceiptPolicyAsker struct {
	db *sql.DB

	mu     sync.Mutex
	gen    uint64 // bus generation the cache was filled under
	known  bool
	cached receiptPolicyAnswer
}

// receiptPolicyAskers caches one pluginReceiptPolicyAsker per *sql.DB.
// Unlike charge.policy.ask/tax.rate.ask, receipt.policy.ask has no
// pos.Service-level interface to hang a single shared instance on (it's
// asked directly from within this package, never from internal/pos) — so
// instead of threading a new field through common.Deps and every test
// helper that builds one, the asker lives here, keyed by db. Production
// wires exactly one long-lived *sql.DB (mirroring pluginChargePolicyAsker/
// pluginTaxRateAsker's single shared-instance construction in Init), so
// this is effectively one asker for the process; each test's own *sql.DB
// gets an isolated entry, so no test can observe a cached answer left
// behind by another test's plugin/generation state.
var (
	receiptPolicyAskersMu sync.Mutex
	receiptPolicyAskers   = map[*sql.DB]*pluginReceiptPolicyAsker{}
)

// receiptPolicyAskerFor returns the (lazily created) asker for db.
func receiptPolicyAskerFor(db *sql.DB) *pluginReceiptPolicyAsker {
	receiptPolicyAskersMu.Lock()
	defer receiptPolicyAskersMu.Unlock()
	a, ok := receiptPolicyAskers[db]
	if !ok {
		a = &pluginReceiptPolicyAsker{db: db}
		receiptPolicyAskers[db] = a
	}
	return a
}

// AskReceiptPolicy asks installed plugins which receipt policies this
// store's market permits, memoized for as long as EventBus.Generation is
// unchanged. ok=false — no subscriber, a transient failure, an unparseable
// answer, or an answer with no recognized value left after validation —
// always means "unrestricted", never an error surface.
func (a *pluginReceiptPolicyAsker) AskReceiptPolicy(ctx context.Context) (allowed []string, ok bool) {
	bus := plugins.SharedBus(a.db)
	if !bus.HasSubscribers(receiptPolicyAskEvent) {
		// Nobody CAN answer — the zero-plugin fast path: one map lookup,
		// no allocation, no DB access.
		return nil, false
	}
	gen := bus.Generation()

	a.mu.Lock()
	if a.known && a.gen == gen {
		ans := a.cached
		a.mu.Unlock()
		return ans.allowed, ans.answered
	}
	a.mu.Unlock()

	// Ask outside the lock: a blocking wasm ask is milliseconds-to-~100ms,
	// and holding the lock across it would serialize concurrent callers. A
	// concurrent double-ask of the same generation is benign.
	resp, ok, err := bus.Ask(ctx, receiptPolicyAskEvent, receiptPolicyAskPayload{})
	if err != nil {
		// Transient failure: decline now, uncached, so the next call
		// retries — "unrestricted" applies meanwhile.
		return nil, false
	}
	ans := receiptPolicyAnswer{}
	if ok {
		var parsed receiptPolicyAskResponse
		if json.Unmarshal(resp, &parsed) != nil {
			// Answered, but with JSON core can't read: a plugin bug the
			// merchant can't see. Logged so it isn't silent; decline THIS
			// call without caching so the next one retries — unlike a
			// clean/recognized-empty decline, which is deterministic for
			// this generation and cacheable below.
			log.Printf("receipt.policy.ask: plugin answer is not valid JSON — treated as unrestricted")
			return nil, false
		}
		if validated := validateReceiptPolicies(parsed.AllowedPolicies); len(validated) > 0 {
			ans = receiptPolicyAnswer{allowed: validated, answered: true}
		}
	}

	a.mu.Lock()
	if a.gen != gen || !a.known {
		a.gen, a.known = gen, true
	}
	a.cached = ans
	a.mu.Unlock()
	return ans.allowed, ans.answered
}

// validateReceiptPolicies maps a plugin's allowed_policies list at the
// untrusted-input boundary: values are trimmed and case-folded, anything
// that isn't one of the three recognized policies is dropped (logged — a
// plugin bug here is invisible to the merchant otherwise), duplicates are
// folded, and the plugin's own order among the survivors is preserved.
func validateReceiptPolicies(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	seen := make(map[string]bool, 3)
	out := make([]string, 0, 3)
	for _, v := range raw {
		p := strings.ToLower(strings.TrimSpace(v))
		if !isReceiptPolicy(p) {
			log.Printf("receipt.policy.ask: plugin answer declared unknown policy %q — dropped", v)
			continue
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// clampReceiptPolicy returns policy if the plugin's allowed set permits it,
// else the value it is clamped down to (ADR-0089 Decision 2): "always" when
// the set permits it — the one value that never skips a receipt, i.e. the
// most restrictive reading of a Belegausgabepflicht-style rule — otherwise
// the plugin's own first-listed value. allowed must be non-empty (callers
// only reach this with ok=true from askReceiptPolicy).
func clampReceiptPolicy(policy string, allowed []string) string {
	hasAlways := false
	for _, a := range allowed {
		if a == policy {
			return policy
		}
		if a == receiptPolicyAlways {
			hasAlways = true
		}
	}
	if hasAlways {
		return receiptPolicyAlways
	}
	return allowed[0]
}

// receiptPolicyPermitted reports whether the plugin-allowed set (ok=false
// meaning unrestricted) permits policy — the save-side twin of
// clampReceiptPolicy: the read path clamps silently, the save path rejects.
func receiptPolicyPermitted(policy string, allowed []string, ok bool) bool {
	if !ok {
		return true
	}
	for _, a := range allowed {
		if a == policy {
			return true
		}
	}
	return false
}

// receiptPolicyLockedForCountry is the INTERIM core-only Germany carve-out
// (ADR-0089 Decision 3), mirroring the Turkey/service-charge precedent in
// internal/pos/charge_policy.go (ut-docs#962): whether "ask, and print
// nothing on decline" satisfies §146a Abs. 2 AO is an open accountant
// question (ut-docs#1908), and no plugin should encode a legal answer nobody
// has confirmed — so core itself forces "always" for a shop whose
// store.country is DE, applied AFTER the plugin clamp so it is the final
// word regardless of any plugin's answer or the merchant's own setting.
//
// This is TEMPORARY and explicitly not the long-term mechanism: once
// ut-docs#1908 returns an answer, ut-plugin-tax-de answers
// receipt.policy.ask itself (Decision 2) and this function — and its three
// call sites (printerConfigChecked, the printer-settings save handler, and
// the settings page's locked control) — should be deleted, NOT extended to
// a second country. Same case-fold/trim as fiscal_device_page.go's own TR
// check: store.country is free text via /api/settings/upsert.
func receiptPolicyLockedForCountry(country string) bool {
	return strings.EqualFold(strings.TrimSpace(country), "DE")
}
