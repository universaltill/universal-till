package fiscal

import "strings"

// This file is the table of ADR-0129 Group A legal obligations — the place
// core may list countries for a legal safeguard (ADR-0050 Decision 2: the
// enforcement point that notices a plugin's absence must be core's, not the
// absent plugin's — which is why DE is gated even while ut-plugin-tax-de is
// a skeleton). The rest of core asks the Requires* functions. A few older
// per-country rules still carry their own markers outside this table
// (AllowedTaxRateSetBP, common.ServiceChargeForbidden, the menu fiscal
// tiles) until their own move cards land. The next fiscalised market
// (ADR-0047's list) is one more table line: the per-country settings key
// functions take any country string, so it needs no key of its own.
//
// Lookups for gates that BLOCK a sale (HardGate, PerSaleDeviceReceipt) are
// deliberately case-sensitive: a loose match must never widen a blocking
// gate (store.country is persisted uppercase). Only the advisory
// TaxRateSwitch banner upper-cases its input.

// MarketObligations are the legal safeguards core enforces for one market.
type MarketObligations struct {
	// HardGate: ADR-0048 — refuse a real sale with no working signer.
	// DE (ADR-0048); TR (Law No. 3100 YN ÖKC mandate, ut-docs#1208): its
	// signer is the ÖKC device itself, driven by the ut-plugin-tax-tr plugin
	// (reference/turkey-compliance.md §1). A TR shop that declares
	// fiscal.system_of_record with no fiscal.signing_device_configured.tr
	// hits BlockedNeverConfigured instead of completing an unsigned sale.
	HardGate bool
	// PerSaleDeviceReceipt: a system-of-record sale needs evidence THIS sale
	// went through the fiscal device (ut-docs#1768) — TR's ÖKC prints the
	// mali fiş at the point of sale. DE's TSE signs after the fact
	// (ADR-0044), already per-sale, so it is excluded. HardGate alone is
	// not enough to prove a given TR sale is compliant.
	PerSaleDeviceReceipt bool
	// TaxRateSwitch: ADR-0068 — a tax.rate.ask plugin is expected to answer;
	// warn when none is active. Explicit and reviewable, never inferred from
	// marketplace metadata (same convention as ADR-0067's list).
	TaxRateSwitch bool
}

var marketObligations = map[string]MarketObligations{
	"DE": {HardGate: true, TaxRateSwitch: true},        // core-neutral:allow ADR-0050 D2 legal floor
	"TR": {HardGate: true, PerSaleDeviceReceipt: true}, // core-neutral:allow ADR-0050 D2 legal floor
}

// RequiresTaxRateSwitch reports whether country expects a tax.rate.ask
// answerer (ADR-0068). Unlike the other lookups it is case-insensitive
// (strings.ToUpper only, no trimming), preserving the Settings banner's
// behaviour.
func RequiresTaxRateSwitch(country string) bool {
	return marketObligations[strings.ToUpper(country)].TaxRateSwitch
}
