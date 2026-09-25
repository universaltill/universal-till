package fleetlink

// Test-only accessors (kept out of the build so the deadcode baseline
// guard sees no test-only production code).

func (p *Peer) inFlight() int { return len(p.outSem) }

func (h *Hub) storeReport(tillID string, r Report) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.storeReportLocked(tillID, r)
}
