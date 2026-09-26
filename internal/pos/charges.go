package pos

import "github.com/universaltill/universal-till/internal/money"

// BuildCharges is ADR-0062's single shared charge-list builder (ut-docs#985):
// the basket preview (Service.computeTotals) and the tender handler
// (internal/pages/pos_api.go) both call it, so what the screen quotes and
// what the tender demands — and CompleteSale then enforces — are built from
// the identical list and cannot drift.
//
//   - base is the sale's lines-only, post-discount net subtotal (the
//     "net_lines" base, the only one implemented).
//   - merchantRateBP is the till's own Settings rate for core's
//     ServiceChargeKey item (already 0 for a banned country via
//     common.EffectiveServiceChargeRateBP).
//   - forbidden (the ut-docs#962 Turkey ban) suppresses the WHOLE list,
//     plugin-declared items included (ADR-0062 Decision 3) — an installed
//     plugin must never be a side door for a banned charge line.
//   - policy/answered is the charge.policy.ask answer. Answered with
//     ServiceChargePermitted=false drops only the merchant item; its
//     ServiceChargeTaxBasisBP becomes that item's TaxBasisBP. Every
//     policy.Charges item is appended in order at its DefaultRateBP, applied
//     verbatim (no merchant-editable settings row exists for it; the rate
//     was already clamped to [0, 10000] at validateChargePolicy).
//
// Non-positive amounts are dropped: a zero charge is not a charge and gets
// no sale_charges row (so a zero-charge sale persists
// service_charge_tax_basis_bp 0 even if the policy named a basis — nothing
// was taxed at it), and a negative one (an over-discounted base) would be a
// discount wearing a charge's clothes, which computeSaleTotals rejects.
// Returns nil when nothing remains.
func BuildCharges(base money.Money, merchantRateBP int, forbidden bool, policy ChargePolicy, answered bool) []ChargeInput {
	if forbidden {
		return nil
	}
	var out []ChargeInput
	add := func(c ChargeInput) {
		if c.Amount.IsPositive() {
			out = append(out, c)
		}
	}
	if !answered || policy.ServiceChargePermitted {
		amount, _ := ComputeTaxBasisPoints(base, merchantRateBP, false)
		item := ChargeInput{Key: ServiceChargeKey, Amount: amount, Base: ChargeBaseNetLines}
		if answered {
			item.TaxBasisBP = policy.ServiceChargeTaxBasisBP
		}
		add(item)
	}
	if !answered {
		return out
	}
	for _, p := range policy.Charges {
		// Base: every value is computed as net_lines today — the reserved
		// "net_lines_plus_prior_charges" has no code path (ADR-0062
		// Decision 1; validateChargePolicy already logged anything else) —
		// so the row records the base actually applied, not the one asked.
		amount, _ := ComputeTaxBasisPoints(base, p.DefaultRateBP, false)
		add(ChargeInput{
			Key:        p.Key,
			Label:      p.Label,
			Amount:     amount,
			TaxBasisBP: p.TaxBasisBP,
			Base:       ChargeBaseNetLines,
		})
	}
	return out
}
