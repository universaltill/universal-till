package pos

import "time"

// Age-restricted sales (ut-docs#3340): due-diligence support for items the
// merchant flags items.age_restricted (055_items_age_restricted.sql). Core
// owns only the MECHANISM — the flag, the till's ID-check prompt, the
// kiosk's fail-closed refusal and the outcome log — never a jurisdiction's
// legal facts: no minimum age, no cutoff date and no country name appears
// in this package (ADR-0050's core/plugin boundary). Mirrors
// shrinkage_reason.go's shape: a fixed vocabulary declared here, a Valid*
// helper, and the data/handler/UI layers only render and persist it.
//
//   - AgeVerificationAccepted: staff checked the customer's ID and allowed
//     the restricted item to be sold.
//   - AgeVerificationRefused: staff refused to sell the restricted item;
//     the sale cannot tender while that item is still in the basket.
//
// Fixed at exactly these two for this slice — the same two values
// 056_age_verifications.sql's CHECK constraint enforces.
type AgeVerificationOutcome string

const (
	AgeVerificationAccepted AgeVerificationOutcome = "accepted"
	AgeVerificationRefused  AgeVerificationOutcome = "refused"
)

// ValidAgeVerificationOutcome reports whether s is one of the two known
// outcomes — used by POST /api/pos/age-check to reject anything else with a
// 400, and by pos.CompleteSale before it ever writes an age_verifications
// row, rather than relying on the schema CHECK alone.
func ValidAgeVerificationOutcome(s string) bool {
	switch AgeVerificationOutcome(s) {
	case AgeVerificationAccepted, AgeVerificationRefused:
		return true
	default:
		return false
	}
}

// DOBBeforeCutoff reports whether a date of birth falls strictly before a
// cutoff date — the "permanent birth-date cutoff" form of an age
// restriction (a rule like "nobody born on or after <date> may ever buy
// this"), as opposed to a rolling minimum age. Pure and generic: the
// caller supplies the cutoff, so no jurisdiction's date is hardcoded in
// core (ut-docs#3340 AC3, ADR-0050). Not wired into any UI in this slice
// (DOB entry is an explicit non-goal) — it is the tested mechanism a later
// DOB-entry feature or a country plugin's age.restriction.ask answer can
// call. Born exactly ON the cutoff is NOT before it.
func DOBBeforeCutoff(dob, cutoff time.Time) bool {
	return dob.Before(cutoff)
}

// AgeCheck is one ID-check outcome the cashier recorded for an
// age-restricted item during the current sale (ut-docs#3340). ItemName is
// the line's name at the moment of the check — the snapshot persisted to
// age_verifications.item_name — and CashierID the user who answered.
type AgeCheck struct {
	ItemID    string
	ItemName  string
	Outcome   AgeVerificationOutcome
	CashierID string
}

// RecordAgeCheck records the cashier's ID-check outcome for the item on the
// line identified by lineKey, replacing any earlier answer for that item in
// this sale (a customer who first can't, then can, show ID). ok is false —
// and nothing is recorded — when no such line exists, the line carries no
// ItemID, the line is not age restricted, or outcome is outside the fixed
// vocabulary. In-memory only: nothing is persisted until the sale completes
// (pos.CompleteSale writes the age_verifications rows in the sale's own
// transaction), so a check is never recorded against a sale that never
// happened.
func (s *Service) RecordAgeCheck(lineKey string, outcome AgeVerificationOutcome, cashierID string) (*Basket, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !ValidAgeVerificationOutcome(string(outcome)) {
		return s.basketCopyLocked(), false
	}
	idx := -1
	for i := range s.lines {
		if s.lines[i].LineKey == lineKey {
			idx = i
			break
		}
	}
	if idx < 0 || s.lines[idx].ItemID == "" || !s.lines[idx].AgeRestricted {
		return s.basketCopyLocked(), false
	}
	l := s.lines[idx]
	if s.ageChecks == nil {
		s.ageChecks = map[string]AgeCheck{}
	}
	if _, seen := s.ageChecks[l.ItemID]; !seen {
		s.ageCheckOrder = append(s.ageCheckOrder, l.ItemID)
	}
	s.ageChecks[l.ItemID] = AgeCheck{ItemID: l.ItemID, ItemName: l.Name, Outcome: outcome, CashierID: cashierID}
	s.publishAgeChecksLocked()
	return s.basketCopyLocked(), true
}

// publishAgeChecksLocked replaces the published Basket.AgeChecks with a
// fresh copy of s.ageChecks (never mutating the previous map, which a
// renderer may still hold). Caller must hold s.mu.
func (s *Service) publishAgeChecksLocked() {
	pub := make(map[string]string, len(s.ageChecks))
	for id, c := range s.ageChecks {
		pub[id] = string(c.Outcome)
	}
	s.basket.AgeChecks = pub
}

// AgeChecks returns the current sale's recorded ID-check outcomes in the
// order they were first recorded — the tender path turns these into
// SaleInput.AgeVerifications.
func (s *Service) AgeChecks() []AgeCheck {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]AgeCheck, 0, len(s.ageCheckOrder))
	for _, id := range s.ageCheckOrder {
		if c, ok := s.ageChecks[id]; ok {
			out = append(out, c)
		}
	}
	return out
}

// MarkAgeRestricted flags every current line whose ItemID is in restricted
// (the DB's CURRENT items.age_restricted answer, data.POSRepo.
// AgeRestrictedItemIDs) as AgeRestricted, so the tender gate's backstop can
// show the badge/sheet for a line whose flag was missing — a line restored
// from a held sale saved before the item was flagged. Only ever ADDS the
// flag (fail closed): a line the resolver already marked stays marked even
// if the item was unflagged mid-sale. Republishes the basket when anything
// changed.
func (s *Service) MarkAgeRestricted(restricted map[string]bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for i := range s.lines {
		if !s.lines[i].AgeRestricted && s.lines[i].ItemID != "" && restricted[s.lines[i].ItemID] {
			s.lines[i].AgeRestricted = true
			changed = true
		}
	}
	if changed {
		s.recomputeTotals()
	}
}

// UnresolvedAgeRestrictedLine returns the first line in lines that is age
// restricted (its own flag, or its ItemID in restricted) and whose item has
// no ACCEPTED ID check in checks — unverified or refused. ok is false when
// every restricted line is cleared to sell. Pure: the tender handler passes
// the same line snapshot it builds the sale from.
func UnresolvedAgeRestrictedLine(lines []BasketLine, restricted map[string]bool, checks []AgeCheck) (BasketLine, bool) {
	accepted := make(map[string]bool, len(checks))
	for _, c := range checks {
		if c.Outcome == AgeVerificationAccepted {
			accepted[c.ItemID] = true
		}
	}
	for _, l := range lines {
		if !(l.AgeRestricted || (l.ItemID != "" && restricted[l.ItemID])) {
			continue
		}
		if l.ItemID == "" || !accepted[l.ItemID] {
			return l, true
		}
	}
	return BasketLine{}, false
}

// AgeVerificationsForSale picks which recorded checks are persisted with a
// sale built from lines: every REFUSAL (the shop's refusals log — the item
// was removed and the rest of the sale went ahead without it), and every
// ACCEPTANCE whose item is actually being sold in lines (an accepted item
// the customer then put back was never sold, so there is no sale to vouch
// for).
func AgeVerificationsForSale(lines []BasketLine, checks []AgeCheck) []AgeCheck {
	sold := make(map[string]bool, len(lines))
	for _, l := range lines {
		if l.ItemID != "" {
			sold[l.ItemID] = true
		}
	}
	var out []AgeCheck
	for _, c := range checks {
		if c.Outcome == AgeVerificationRefused || sold[c.ItemID] {
			out = append(out, c)
		}
	}
	return out
}
