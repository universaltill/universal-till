package pos

import "github.com/universaltill/universal-till/internal/money"

// CapturedLeg is one tender leg whose payment.<key>.authorize reached a plugin
// and approved: what a reversal must send back (ADR-0136 D2, ut-docs#6602).
type CapturedLeg struct {
	MethodID       string
	Currency       string      // leg currency, else the sale's (resolved by the caller)
	RequestID      string      // the authorize event id (idempotency key)
	Amount         money.Money // exactly what the authorize captured (incl. reader tip)
	TransactionRef string      // provider ref (ADR-0196), may be ""
	TipAmount      money.Money
	// ReaderRef is the provider reader id the authorize ran on (ut-docs#6566),
	// "" for a method with no registry reader; a reversal sends it back.
	ReaderRef string
}

// UnknownLeg is a leg whose authorize answered outcome_unknown (ut-docs#6600).
type UnknownLeg struct {
	MethodID, Currency, RequestID string
	Amount                        money.Money
	// ReaderRowID is the card reader registry row the authorize ran on (ut-docs#6566),
	// "" for a method with no registry reader: the picker shows it pinned.
	ReaderRowID string
	// Tap marks a card-present (Tap to Pay) leg whose payment attempt core
	// recorded (ut-docs#6455); RequestID is then also the attempt's key.
	// TapReason is why it is pending (PendingProcessing, PendingLookupFailed,
	// PendingAmountMismatch, ..., or "" when the plugin itself said unknown).
	Tap       bool
	TapReason string
	// TapPaid: core confirmed the payment with the broker after the till had
	// locked (Check again, manager approve); the tender is then finished by
	// paying again, which does not charge twice. TapBrand/TapLast4 are what
	// the provider reported (ADR-0127: nothing more about the card).
	TapPaid            bool
	TapBrand, TapLast4 string
}

// TapPanel is the tap-to-pay screen state the till page shows in place of the
// toast (ut-docs#6455, ux A1-A4). Per render only, like ToastMessage: never
// stored; the engine derives State "pending" from a locked tap leg.
type TapPanel struct {
	// State: pending | not_paid | paid_already | start_failed | mismatch.
	State string
	// Method is the tender method the screen's buttons pay again with.
	Method string
	Amount money.Money
	Reason string // pending reason, see UnknownLeg.TapReason
	Brand  string // paid_already: card brand as the provider reports it
	Last4  string
	// Still is set when a check found it still pending.
	Still bool
}

// PendingTender is what a locked tender leaves behind on the engine.
type PendingTender struct {
	Captured []CapturedLeg
	Unknown  []UnknownLeg
}

// TapUnknownLeg returns the first locked tap leg and its index among the
// unknown legs.
func (p PendingTender) TapUnknownLeg() (UnknownLeg, int, bool) {
	for i, l := range p.Unknown {
		if l.Tap {
			return l, i, true
		}
	}
	return UnknownLeg{}, 0, false
}

// IsZero reports whether nothing is pending.
func (p PendingTender) IsZero() bool { return len(p.Captured) == 0 && len(p.Unknown) == 0 }

// AbandonReason names the path that dropped a locked tender (Problems text).
type AbandonReason string

const (
	AbandonCleared  AbandonReason = "the basket was cleared"
	AbandonParked   AbandonReason = "the basket was parked"
	AbandonReplaced AbandonReason = "another order was opened over it"
	// Table-QR session baskets (ut-docs#6601).
	AbandonSessionExpired AbandonReason = "the table order session expired"
	AbandonSessionEnded   AbandonReason = "the table order session ended"
)

// AbandonResult is what the handler did; kept as a one-shot notice for the UI.
type AbandonResult struct{ Reversed, Failed, Unknown int }

// AbandonedTenderHandler is called (never under s.mu) with the stash a reset
// dropped. pages registers it; pos never calls plugins itself.
type AbandonedTenderHandler func(reason AbandonReason, t PendingTender) AbandonResult

// SetAbandonedTenderHandler registers the handler.
func (s *Service) SetAbandonedTenderHandler(h AbandonedTenderHandler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.abandonHandler = h
}

// TakeAbandonNotice returns and clears the last hand-off result.
func (s *Service) TakeAbandonNotice() (AbandonResult, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.abandonNotice == nil {
		return AbandonResult{}, false
	}
	r := *s.abandonNotice
	s.abandonNotice = nil
	return r, true
}

// AbandonTender drops the tender state only (the basket stays, no recompute)
// and hands what it held to the handler (ut-docs#6601). Handler runs after
// s.mu is released.
func (s *Service) AbandonTender(r AbandonReason) {
	s.mu.Lock()
	p := s.dropTenderLocked()
	s.mu.Unlock()
	s.handOffAbandoned(r, p)
}

// HasPendingTender reports whether a tender is locked or holds legs.
func (s *Service) HasPendingTender() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tenderLock != "" || !s.pendingTender.IsZero()
}

// handOffAbandoned runs the registered handler. Caller must NOT hold s.mu.
func (s *Service) handOffAbandoned(r AbandonReason, p PendingTender) {
	if p.IsZero() {
		return
	}
	s.mu.Lock()
	h := s.abandonHandler
	s.mu.Unlock()
	if h == nil {
		return // no handler wired: same silent drop as before ut-docs#6602
	}
	res := h(r, p)
	s.mu.Lock()
	s.abandonNotice = &res
	s.mu.Unlock()
}

// mergePending: a retry of the SAME tender updates the stash per RequestID.
// Captured legs are deduplicated by RequestID (next wins); an unknown leg
// that is now captured is dropped from Unknown.
func mergePending(prev, next PendingTender) PendingTender {
	var out PendingTender
	idx := map[string]int{}
	for _, l := range append(append([]CapturedLeg(nil), prev.Captured...), next.Captured...) {
		if i, ok := idx[l.RequestID]; ok {
			out.Captured[i] = l
			continue
		}
		idx[l.RequestID] = len(out.Captured)
		out.Captured = append(out.Captured, l)
	}
	seen := map[string]bool{}
	for _, u := range append(append([]UnknownLeg(nil), prev.Unknown...), next.Unknown...) {
		if _, cap := idx[u.RequestID]; cap {
			continue
		}
		if seen[u.RequestID] {
			for i := range out.Unknown {
				if out.Unknown[i].RequestID == u.RequestID {
					out.Unknown[i] = u
				}
			}
			continue
		}
		seen[u.RequestID] = true
		out.Unknown = append(out.Unknown, u)
	}
	return out
}
