package pos

// Test-only accessors (ut-docs#6602): production code uses SettleTender,
// RotateTenderAttemptIDIf and the reset hand-off (PendingTender moved to
// service.go in ut-docs#6566: the reader picker reads it); these stay out of the
// shipped binary so the deadcode guard sees no unreachable function.

// RotateTenderAttemptID discards the current tender attempt's id so the
// next TenderAttemptID call mints a fresh one, WITHOUT touching the basket.
// For a tender refused after its authorized legs were reversed (ADR-0136,
// ut-docs#3309): the basket is kept for the cashier to fix, but the old
// authorize requests are spent — a retry must be a new request, never one a
// provider's own dedupe (ut-docs#1762) could answer with the memoized
// approval for money that was just sent back.
func (s *Service) RotateTenderAttemptID() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tenderAttemptID = ""
	s.tenderLock = ""
	s.pendingTender = PendingTender{}
}
