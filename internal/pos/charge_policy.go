package pos

// Tip recipient values (ADR-0061 Decision 3): whose money a tip is for tax
// purposes, persisted per payment at capture time. "employee" is the one
// default every researched market agrees on (including the UK, where the
// Allocation of Tips Act 2023 makes it matter most), so it is what applies
// whenever nothing answers charge.policy.ask.
const (
	TipRecipientEmployee = "employee"
	TipRecipientBusiness = "business"
)

// ChargePolicy is an installed country-tax plugin's answer to the
// charge.policy.ask hook (ADR-0061 Decision 1) — the country-law dimensions
// of a service charge and a tip that core deliberately has no built-in
// opinion on, mirroring TaxRateAsker's split of responsibilities.
type ChargePolicy struct {
	// ServiceChargePermitted reports whether a service charge is a lawful
	// bill line in the plugin's market. false suppresses the till-configured
	// charge; the no-plugin default is permitted (every researched market
	// except Turkey, which is handled by a separate core-only check,
	// ut-docs#962 — NOT through this hook).
	ServiceChargePermitted bool
	// ServiceChargeDefaultRateBP is an informational default for the
	// settings UI only — the till's own configured rate stays the source of
	// truth for what is actually charged, exactly as
	// Config.ServiceChargeRateBasisPoints is today. Core never applies it.
	ServiceChargeDefaultRateBP int
	// ServiceChargeTaxBasisBP, when > 0, taxes the whole service charge at
	// this one flat rate. 0 means "use the sale's own per-line rates,
	// apportioned by net line value" — the fail-closed default
	// (ADR-0061 Decision 2, ApportionServiceChargeTax).
	ServiceChargeTaxBasisBP int
	// TipDefaultRecipient is TipRecipientEmployee or TipRecipientBusiness —
	// the market's default for whose money a captured tip is. Empty means
	// no opinion (core defaults to employee).
	TipDefaultRecipient string
	// FiscalBusinessCase is an opaque passthrough for a country plugin's own
	// export mapping (e.g. DSFinV-K's TrinkgeldAN/TrinkgeldAG). Core never
	// interprets it.
	FiscalBusinessCase string
	// Charges is an ordered list of additional additive statutory charges
	// (ADR-0062, ut-docs#963) beyond the one merchant-rate service charge
	// above — e.g. a GCC market's municipality/tourism levies. Empty for
	// every market researched except the GCC; a plugin answering only the
	// legacy scalar fields above needs no code change (ADR-0062 Decision 3:
	// core treats a non-empty legacy answer as a one-item Charges list
	// whenever a plugin's own Charges is empty). Unlike
	// ServiceChargeDefaultRateBP, each item's DefaultRateBP here IS the
	// applied rate, taken as-is — there is no settings row for a
	// plugin-declared levy the merchant could override.
	Charges []ChargeItem
}

// ChargeItem is one plugin-declared additive statutory charge (ADR-0062
// Decision 3) — the same shape as ChargeInput minus Amount, since a policy
// answer supplies a rate the caller applies to the sale's net, not an
// already-computed amount. ChargeInput itself lands in internal/pos/sales.go
// at ADR-0062 step 2 (ut-docs#985), not yet present as of this step.
type ChargeItem struct {
	// Key is a stable id for this charge, e.g. "municipality_tax". The
	// reserved key "service_charge" is core's own merchant-rate item above
	// — a plugin answer that includes it here is rejected at the validation
	// boundary (internal/pages/charge_hook.go's validateChargePolicy), not
	// silently overwritten. Never empty — also rejected at that boundary.
	Key string
	// Label is the receipt/journal display text for this charge. Unlike
	// core's own service_charge item (whose "" falls back to core's
	// T "journal.detail.service_charge" copy — see Decision 6), a
	// plugin-declared item can never carry Key: "service_charge" (rejected
	// above), so "" here has no core fallback to render; step 3
	// (ut-docs#986) must settle what an empty plugin-declared Label renders
	// as, e.g. defaulting it to Key.
	Label string
	// DefaultRateBP is the rate APPLIED VERBATIM to the sale's net — unlike
	// ServiceChargeDefaultRateBP, there is no merchant-editable settings row
	// for anything in this list. Validated/clamped to [0, 10000] at the
	// parse boundary; never trusted past that range.
	DefaultRateBP int
	// TaxBasisBP, when > 0, taxes this charge at this one flat rate; 0 means
	// apportion at the sale's own per-line rates (mirrors
	// ServiceChargeTaxBasisBP's semantics, per-charge instead of per-sale).
	TaxBasisBP int
	// Base is "net_lines" (the only value core implements; anything else
	// clamps to it and logs — see validateChargePolicy) or
	// "net_lines_plus_prior_charges", reserved for a future cascading levy
	// no researched market needs yet.
	Base string
}

// ChargePolicyAsker lets an installed country-tax plugin declare its
// market's service-charge/tip policy (ADR-0061). ok=false means nothing
// answered — a NORMAL case (no plugin installed, or none with an opinion),
// never an error and never a blocked sale: the caller applies core's
// fail-closed default (charge permitted, taxed at the sale's own per-line
// rates, tip to the employee). Deliberately unlike TaxRateAsker's blocked
// signal — this hook has no "authority present but broken" state to
// represent, because the safe default is always available and always
// correct to apply (ADR-0061 Decision 1). Wired from internal/pages via
// Service.SetChargePolicyAsker, calling into the plugin event bus —
// internal/pos itself never talks to the plugin subsystem.
type ChargePolicyAsker interface {
	AskChargePolicy() (ChargePolicy, bool)
}

// SetChargePolicyAsker installs (or clears, with nil) the plugin-backed
// charge-policy hook and recomputes totals so the change is reflected
// immediately — same wiring shape as SetTaxRateAsker.
func (s *Service) SetChargePolicyAsker(a ChargePolicyAsker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chargeAsker = a
	s.recomputeTotals()
}

// ChargePolicy returns the installed asker's current answer, or ok=false
// when no asker is set or nothing answers. The asker is read under the
// service lock but asked outside it — a wasm-backed ask can take ~100ms on
// a cold module and must not serialize unrelated basket operations.
func (s *Service) ChargePolicy() (ChargePolicy, bool) {
	s.mu.Lock()
	a := s.chargeAsker
	s.mu.Unlock()
	if a == nil {
		return ChargePolicy{}, false
	}
	return a.AskChargePolicy()
}
