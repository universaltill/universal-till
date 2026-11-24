package pos

import (
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/money"
)

type abandonCall struct {
	reason AbandonReason
	p      PendingTender
}

// recordingAbandonHandler records calls. Inside the handler it takes m.Len()
// and svc.TenderLock() on a goroutine with a 2 s timeout, so a hand-off made
// while m.mu or s.mu is held deadlocks and fails the test (ut-docs#6601).
func recordingAbandonHandler(t *testing.T, m *SessionBasketManager, svc func() *Service) (AbandonedTenderHandler, func() []abandonCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []abandonCall
	h := func(r AbandonReason, p PendingTender) AbandonResult {
		done := make(chan struct{})
		go func() {
			_ = m.Len()
			if s := svc(); s != nil {
				_ = s.TenderLock()
			}
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("handler ran under a manager or service lock")
		}
		mu.Lock()
		calls = append(calls, abandonCall{r, p})
		mu.Unlock()
		return AbandonResult{}
	}
	return h, func() []abandonCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]abandonCall(nil), calls...)
	}
}

func twoLegPending() PendingTender {
	return PendingTender{
		Captured: []CapturedLeg{{MethodID: "demopay", Currency: "EUR", RequestID: "att:0:abcd", Amount: money.FromMinor(60)}},
		Unknown:  []UnknownLeg{{MethodID: "demopay", Currency: "EUR", RequestID: "att:1:ef01", Amount: money.FromMinor(40)}},
	}
}

func TestSessionManager_SweepHandsOffPendingTender(t *testing.T) {
	m, now := newTestSessionManager(t)
	_, svc, _ := m.Create()
	h, calls := recordingAbandonHandler(t, m, func() *Service { return svc })
	m.SetAbandonedTenderHandler(h)
	svc.LockTender("sig", twoLegPending())

	*now = now.Add(3 * time.Hour)
	if n := m.Sweep(2*time.Hour, *now); n != 1 {
		t.Fatalf("Sweep = %d, want 1", n)
	}
	c := calls()
	if len(c) != 1 || c[0].reason != AbandonSessionExpired || len(c[0].p.Captured) != 1 || len(c[0].p.Unknown) != 1 {
		t.Fatalf("calls = %+v, want one AbandonSessionExpired with both legs", c)
	}
	if svc.TenderLock() != "" {
		t.Fatal("tender lock must be cleared")
	}
}

func TestSessionManager_RemoveHandsOffPendingTender(t *testing.T) {
	m, _ := newTestSessionManager(t)
	tok, svc, _ := m.Create()
	h, calls := recordingAbandonHandler(t, m, func() *Service { return svc })
	m.SetAbandonedTenderHandler(h)
	svc.LockTender("sig", twoLegPending())

	m.Remove(tok)
	c := calls()
	if len(c) != 1 || c[0].reason != AbandonSessionEnded {
		t.Fatalf("calls = %+v, want one AbandonSessionEnded", c)
	}
	m.Remove(tok) // unknown token: no second call, no panic
	if len(calls()) != 1 {
		t.Fatal("removing an unknown token must not call the handler")
	}
}

func TestSessionManager_RemoveSettledSessionCallsNoHandler(t *testing.T) {
	m, _ := newTestSessionManager(t)
	tok, svc, _ := m.Create()
	h, calls := recordingAbandonHandler(t, m, func() *Service { return svc })
	m.SetAbandonedTenderHandler(h)
	svc.LockTender("sig", twoLegPending())
	svc.SettleTender()

	m.Remove(tok)
	if n := len(calls()); n != 0 {
		t.Fatalf("handler called %d times for a settled session, want 0", n)
	}
}

func TestSessionManager_HandlerReachesEverySession(t *testing.T) {
	m, _ := newTestSessionManager(t)
	_, s1, _ := m.Create() // before the handler is set
	var mu sync.Mutex
	n := 0
	m.SetAbandonedTenderHandler(func(AbandonReason, PendingTender) AbandonResult {
		mu.Lock()
		n++
		mu.Unlock()
		return AbandonResult{}
	})
	_, s2, _ := m.Create()
	_, s3, _ := m.Create()
	s4, ok := m.NewDetached()
	if !ok {
		t.Fatal("NewDetached failed")
	}
	for _, s := range []*Service{s1, s2, s3, s4} {
		s.LockTender("sig", twoLegPending())
		s.ResetBecause(AbandonCleared)
	}
	if n != 4 {
		t.Fatalf("handler calls = %d, want 4", n)
	}
}

func TestService_AbandonTenderKeepsBasketAndMarksHandedOff(t *testing.T) {
	s := NewServiceWithResolver(Config{TaxRateBasisPoints: 2000}, nil)
	s.mu.Lock()
	s.lines = []BasketLine{{ItemID: "i1", Name: "Tea", Qty: 1, PriceCents: money.FromMinor(100)}}
	s.mu.Unlock()
	old := s.TenderAttemptID()
	p := twoLegPending()
	s.LockTender("sig", p)

	s.AbandonTender(AbandonSessionExpired)

	if len(s.Lines()) != 1 {
		t.Fatalf("Lines = %d, want basket kept", len(s.Lines()))
	}
	if s.TenderLock() != "" || s.HasPendingTender() {
		t.Fatal("tender state must be cleared")
	}
	if left := s.NotHandedOff(p.Captured); len(left) != 0 {
		t.Fatalf("NotHandedOff = %v, want empty", left)
	}
	if s.TenderAttemptIs(old) {
		t.Fatal("attempt id must be cleared")
	}
}

// Review L1 (ut-docs#6601): a panicking hand-off must not stop the sweep —
// every other dropped session still gets its tender handed off.
func TestSessionManager_SweepSurvivesHandlerPanic(t *testing.T) {
	m, now := newTestSessionManager(t)
	_, a, _ := m.Create()
	_, b, _ := m.Create()
	var mu sync.Mutex
	handed := 0
	m.SetAbandonedTenderHandler(func(r AbandonReason, p PendingTender) AbandonResult {
		mu.Lock()
		handed++
		first := handed == 1
		mu.Unlock()
		if first {
			panic("plugin blew up")
		}
		return AbandonResult{}
	})
	a.LockTender("sig", twoLegPending())
	b.LockTender("sig", twoLegPending())

	*now = now.Add(3 * time.Hour)
	if n := m.Sweep(2*time.Hour, *now); n != 2 {
		t.Fatalf("Sweep = %d, want 2", n)
	}
	mu.Lock()
	defer mu.Unlock()
	if handed != 2 {
		t.Fatalf("handler calls = %d, want 2 (a panic must not skip the rest)", handed)
	}
}
