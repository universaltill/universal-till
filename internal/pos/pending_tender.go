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
}

// UnknownLeg is a leg whose authorize answered outcome_unknown (ut-docs#6600).
type UnknownLeg struct {
	MethodID, Currency, RequestID string
	Amount                        money.Money
}

// PendingTender is what a locked tender leaves behind on the engine.
type PendingTender struct {
	Captured []CapturedLeg
	Unknown  []UnknownLeg
}

// IsZero reports whether nothing is pending.
func (p PendingTender) IsZero() bool { return len(p.Captured) == 0 && len(p.Unknown) == 0 }

// AbandonReason names the path that dropped a locked tender (Problems text).
type AbandonReason string

const (
	AbandonCleared  AbandonReason = "the basket was cleared"
	AbandonParked   AbandonReason = "the basket was parked"
	AbandonReplaced AbandonReason = "another order was opened over it"
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
