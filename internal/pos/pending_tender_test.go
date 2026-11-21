package pos

import (
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

// ut-docs#6602: what a locked tender leaves behind is handed to the handler
// exactly once when a reset drops it.

func testPending() PendingTender {
	return PendingTender{
		Captured: []CapturedLeg{{MethodID: "demopay", Currency: "EUR", RequestID: "a:0:x", Amount: money.FromMinor(60), TransactionRef: "ch_1"}},
		Unknown:  []UnknownLeg{{MethodID: "demopay", Currency: "EUR", RequestID: "a:1:y", Amount: money.FromMinor(60)}},
	}
}

type handoffRecorder struct {
	mu      sync.Mutex
	reasons []AbandonReason
	got     []PendingTender
}

func (h *handoffRecorder) handler(r AbandonReason, p PendingTender) AbandonResult {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reasons = append(h.reasons, r)
	h.got = append(h.got, p)
	return AbandonResult{Reversed: len(p.Captured), Unknown: len(p.Unknown)}
}

func newLockedService(t *testing.T) (*Service, *handoffRecorder) {
	t.Helper()
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{})
	h := &handoffRecorder{}
	s.SetAbandonedTenderHandler(h.handler)
	s.LockTender("sig", testPending())
	return s, h
}

func TestLockTender_ResetHandsOffStashOnce(t *testing.T) {
	s, h := newLockedService(t)
	s.Reset()
	if len(h.got) != 1 || h.reasons[0] != AbandonCleared {
		t.Fatalf("want one Cleared hand-off, got %v", h.reasons)
	}
	if len(h.got[0].Captured) != 1 || h.got[0].Captured[0].RequestID != "a:0:x" || len(h.got[0].Unknown) != 1 {
		t.Fatalf("wrong stash: %+v", h.got[0])
	}
	if s.TenderLock() != "" || !s.PendingTender().IsZero() {
		t.Fatalf("lock and stash must be cleared")
	}
	s.Reset()
	if len(h.got) != 1 {
		t.Fatalf("second reset must not hand off again")
	}
}

func TestResetAndSnapshotIf_HandsOffCleared(t *testing.T) {
	s, h := newLockedService(t)
	snap := resetAndSnapshotNow(t, s)
	if len(h.got) != 1 || h.reasons[0] != AbandonCleared {
		t.Fatalf("want one Cleared hand-off, got %v", h.reasons)
	}
	_ = snap
}

func TestResetBecause_ParkedReason(t *testing.T) {
	s, h := newLockedService(t)
	s.ResetBecause(AbandonParked)
	if len(h.reasons) != 1 || h.reasons[0] != AbandonParked {
		t.Fatalf("want Parked, got %v", h.reasons)
	}
}

func TestRestoreHeld_HandsOffReplaced(t *testing.T) {
	s, h := newLockedService(t)
	s.RestoreHeld(BasketSnapshot{}, HeldOrigin{})
	if len(h.reasons) != 1 || h.reasons[0] != AbandonReplaced {
		t.Fatalf("want Replaced, got %v", h.reasons)
	}
}

func TestSettleTender_ReturnsAndClearsStash(t *testing.T) {
	s, h := newLockedService(t)
	p := s.SettleTender()
	if len(p.Captured) != 1 || len(p.Unknown) != 1 {
		t.Fatalf("settle must return the stash, got %+v", p)
	}
	s.Reset()
	if len(h.got) != 0 {
		t.Fatalf("a settled tender must not hand off")
	}
	t.Run("rotate clears", func(t *testing.T) {
		s, h := newLockedService(t)
		s.RotateTenderAttemptID()
		s.Reset()
		if len(h.got) != 0 || !s.PendingTender().IsZero() {
			t.Fatalf("rotate must clear the stash")
		}
	})
}

func TestLockTender_RetryMergesByRequestID(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{})
	a := CapturedLeg{RequestID: "A", Amount: money.FromMinor(1)}
	b := UnknownLeg{RequestID: "B", Amount: money.FromMinor(2)}
	s.LockTender("sig", PendingTender{Captured: []CapturedLeg{a}, Unknown: []UnknownLeg{b}})
	s.LockTender("sig", PendingTender{Unknown: []UnknownLeg{{RequestID: "A"}}})
	p := s.PendingTender()
	if len(p.Captured) != 1 || p.Captured[0].RequestID != "A" || len(p.Unknown) != 1 || p.Unknown[0].RequestID != "B" {
		t.Fatalf("retry merge wrong: %+v", p)
	}
	s.LockTender("sig", PendingTender{Captured: []CapturedLeg{a, {RequestID: "B"}}})
	p = s.PendingTender()
	if len(p.Captured) != 2 || len(p.Unknown) != 0 {
		t.Fatalf("B captured on retry must leave Unknown: %+v", p)
	}
}

func TestHandOff_RunsWithoutEngineLock(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{})
	s.SetAbandonedTenderHandler(func(AbandonReason, PendingTender) AbandonResult {
		_ = s.TenderLock()
		_ = s.Basket()
		return AbandonResult{}
	})
	s.LockTender("sig", testPending())
	done := make(chan struct{})
	go func() { s.Reset(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deadlock: handler ran under the engine lock")
	}
}

func TestTakeAbandonNotice_OneShot(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{})
	s.SetAbandonedTenderHandler(func(AbandonReason, PendingTender) AbandonResult {
		return AbandonResult{Reversed: 1, Unknown: 1}
	})
	s.LockTender("sig", testPending())
	s.Reset()
	r, ok := s.TakeAbandonNotice()
	if !ok || r.Reversed != 1 || r.Unknown != 1 {
		t.Fatalf("want notice, got %+v %v", r, ok)
	}
	if _, ok := s.TakeAbandonNotice(); ok {
		t.Fatalf("notice is one-shot")
	}
	s2 := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, mapResolver{})
	s2.LockTender("sig", testPending())
	s2.Reset()
	if _, ok := s2.TakeAbandonNotice(); ok {
		t.Fatalf("no handler, no notice")
	}
}
