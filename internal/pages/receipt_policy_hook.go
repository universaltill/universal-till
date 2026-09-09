package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"strings"

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
//
// Deliberately NOT memoized the way pluginChargePolicyAsker is: this is
// asked from printerConfigChecked (settings read) and the printer-settings
// save handler, not from the per-keystroke totals recompute, so a plain
// one-shot ask is the right shape. The zero-plugin fast path is still one
// map lookup (HasSubscribers) with no module boot.
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

// askReceiptPolicy asks installed plugins which receipt policies this
// store's market permits. ok=false — no subscriber, a transient failure, an
// unparseable answer, or an answer with no recognized value left after
// validation — always means "unrestricted", never an error surface.
func askReceiptPolicy(ctx context.Context, db *sql.DB) (allowed []string, ok bool) {
	bus := plugins.SharedBus(db)
	if !bus.HasSubscribers(receiptPolicyAskEvent) {
		// Nobody CAN answer — the zero-plugin fast path: one map lookup,
		// no allocation, no DB access.
		return nil, false
	}
	resp, ok, err := bus.Ask(ctx, receiptPolicyAskEvent, receiptPolicyAskPayload{})
	if err != nil || !ok {
		return nil, false
	}
	var parsed receiptPolicyAskResponse
	if json.Unmarshal(resp, &parsed) != nil {
		// Answered, but with JSON core can't read: a plugin bug the merchant
		// can't see. Logged so it isn't silent; treated as no answer.
		log.Printf("receipt.policy.ask: plugin answer is not valid JSON — treated as unrestricted")
		return nil, false
	}
	allowed = validateReceiptPolicies(parsed.AllowedPolicies)
	if len(allowed) == 0 {
		return nil, false
	}
	return allowed, true
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
