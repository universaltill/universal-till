package pages

import (
	"os"
	"sync"
	"time"
)

// The catalog.identify seam's pending-confirm slot (ADR-0121 amendment
// 2026-10-09 R2a, ut-docs#4006). When an identify job finishes with a
// valid result, core takes its photo back from the upload registry
// (plugins.TakeUpload, before the job releases its uploads) and keeps it
// here, so a pick of one of the suggestions can store it as the item's
// newest ai_ref. One slot per plugin, keyed by the job id: a newer capture
// or result replaces it and deletes the old file. The slot expires
// identifySlotTTL after the result is handed out (before that, after the
// job's unpolled TTL plus identifySlotTTL), and its file is deleted then.
// The file stays in the till's temp dir, where it was staged (≤ 8 MiB).

// identifySlotTTL is how long a handed-out result's photo waits for a
// pick. A var so tests can shorten it. The pre-hand-out TTL
// (pluginJobUnpolledTTL + identifySlotTTL) must outlive the finished job's
// pluginJobResultTTL, so a hand-out never meets an already-fired expiry
// (TestIdentifySlotTTLOutlivesJobResult_4006).
var identifySlotTTL = 120 * time.Second

type identifySlot struct {
	jobID, path, ctype string
	timer              *time.Timer
}

type identifySlotRegistry struct {
	mu    sync.Mutex
	slots map[string]*identifySlot // plugin → slot
}

func newIdentifySlotRegistry() *identifySlotRegistry {
	return &identifySlotRegistry{slots: map[string]*identifySlot{}}
}

// identifySlots is the process-wide registry.
var identifySlots = newIdentifySlotRegistry()

// put makes path (owned by the registry from now on) pluginID's slot for
// jobID, replacing — and deleting — any earlier one. It expires after ttl
// unless handedOut restarts the clock.
func (r *identifySlotRegistry) put(pluginID, jobID, path, ctype string, ttl time.Duration) {
	s := &identifySlot{jobID: jobID, path: path, ctype: ctype}
	r.mu.Lock()
	old := r.slots[pluginID]
	r.slots[pluginID] = s
	s.timer = time.AfterFunc(ttl, func() { r.expire(pluginID, s) })
	r.mu.Unlock()
	discardIdentifySlot(old)
}

// handedOut restarts jobID's expiry at identifySlotTTL: the overlay now
// shows the suggestions a pick comes from.
func (r *identifySlotRegistry) handedOut(pluginID, jobID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s := r.slots[pluginID]; s != nil && s.jobID == jobID {
		s.timer.Reset(identifySlotTTL)
	}
}

// take removes and returns the slot whose job is jobID, at most once; the
// caller owns — and must delete — its file. A slot is found by job id
// alone: ids are core's 128-bit random values, unique across plugins.
func (r *identifySlotRegistry) take(jobID string) (path, ctype string, ok bool) {
	if jobID == "" {
		return "", "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for pluginID, s := range r.slots {
		if s.jobID == jobID {
			s.timer.Stop()
			delete(r.slots, pluginID)
			return s.path, s.ctype, true
		}
	}
	return "", "", false
}

// clear drops pluginID's slot and deletes its file (a new capture).
func (r *identifySlotRegistry) clear(pluginID string) {
	r.mu.Lock()
	s := r.slots[pluginID]
	delete(r.slots, pluginID)
	r.mu.Unlock()
	discardIdentifySlot(s)
}

func (r *identifySlotRegistry) expire(pluginID string, s *identifySlot) {
	r.mu.Lock()
	if r.slots[pluginID] != s {
		r.mu.Unlock()
		return // replaced or taken meanwhile
	}
	delete(r.slots, pluginID)
	r.mu.Unlock()
	discardIdentifySlot(s)
}

func discardIdentifySlot(s *identifySlot) {
	if s == nil {
		return
	}
	s.timer.Stop()
	_ = os.Remove(s.path)
}
