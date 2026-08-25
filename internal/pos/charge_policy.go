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
