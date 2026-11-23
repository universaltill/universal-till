package pos

import (
	"testing"
	"time"
)

// ut-docs#6909: one cashier tender at a time per engine.
func TestTenderInFlight_TryBeginEnd(t *testing.T) {
	s := NewServiceWithResolver(Config{}, nil)
	if s.TenderInFlight() {
		t.Fatal("fresh engine must not be in flight")
	}
	if !s.TryBeginTender() {
		t.Fatal("first TryBeginTender must succeed")
	}
	if !s.TenderInFlight() {
		t.Fatal("flag must be set after TryBeginTender")
	}
	if s.TryBeginTender() {
		t.Fatal("second TryBeginTender must be refused while in flight")
	}
	s.EndTender()
	if s.TenderInFlight() || !s.TryBeginTender() {
		t.Fatal("EndTender must release the flag")
	}
}

// The completed sale's own Reset runs inside the guarded request: Reset,
// SettleTender and LockTender must never touch the flag.
func TestTenderInFlight_SurvivesResetAndSettle(t *testing.T) {
	s := NewServiceWithResolver(Config{}, nil)
	s.TryBeginTender()
	s.LockTender("sig", PendingTender{})
	s.SettleTender()
	s.Reset()
	if !s.TenderInFlight() {
		t.Fatal("Reset/SettleTender/LockTender must not clear the in-flight flag")
	}
}

func TestTenderInFlight_ReleasedOnPanic(t *testing.T) {
	s := NewServiceWithResolver(Config{}, nil)
	func() {
		defer func() { _ = recover() }()
		if !s.TryBeginTender() {
			t.Fatal("begin")
		}
		defer s.EndTender()
		panic("boom")
	}()
	if !s.TryBeginTender() {
		t.Fatal("a panicking tender must release the guard")
	}
}

// ut-docs#6919: basket edits are shared, a tender is exclusive and drains them.
func TestBasketEdit_RefusedWhileTender(t *testing.T) {
	s := NewServiceWithResolver(Config{}, nil)
	if !s.TryBeginTender() {
		t.Fatal("begin tender")
	}
	if s.BeginBasketEdit() {
		t.Fatal("edit must be refused while a tender runs")
	}
	s.EndTender()
	if !s.BeginBasketEdit() {
		t.Fatal("edit must be admitted after EndTender")
	}
	s.EndBasketEdit()
}

func TestTryBeginTender_WaitsForAdmittedEdit(t *testing.T) {
	s := NewServiceWithResolver(Config{}, nil)
	if !s.BeginBasketEdit() {
		t.Fatal("begin edit")
	}
	got := make(chan bool, 1)
	go func() { got <- s.TryBeginTender() }()
	select {
	case <-got:
		t.Fatal("tender must wait for the admitted edit")
	case <-time.After(50 * time.Millisecond):
	}
	if s.BeginBasketEdit() {
		t.Fatal("no new edit may be admitted while the tender drains")
	}
	s.EndBasketEdit()
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("tender must be granted once edits drained")
		}
	case <-time.After(time.Second):
		t.Fatal("tender did not return after the edit ended")
	}
}

func TestTryBeginTender_DrainTimeoutRefusesAndReleases(t *testing.T) {
	old := tenderDrainTimeout
	tenderDrainTimeout = 50 * time.Millisecond
	t.Cleanup(func() { tenderDrainTimeout = old })
	s := NewServiceWithResolver(Config{}, nil)
	s.BeginBasketEdit()
	if s.TryBeginTender() {
		t.Fatal("tender must be refused when edits never drain")
	}
	if s.TenderInFlight() {
		t.Fatal("claim must be dropped after timeout")
	}
	if !s.BeginBasketEdit() {
		t.Fatal("edits must be admitted again")
	}
}

func TestBasketEdit_NestedAndUnmatchedEnd(t *testing.T) {
	old := tenderDrainTimeout
	tenderDrainTimeout = 100 * time.Millisecond
	t.Cleanup(func() { tenderDrainTimeout = old })
	s := NewServiceWithResolver(Config{}, nil)
	s.EndBasketEdit() // unmatched: no-op
	s.BeginBasketEdit()
	s.BeginBasketEdit()
	s.EndBasketEdit()
	if s.TryBeginTender() {
		t.Fatal("one edit still admitted: tender must wait then time out")
	}
	s.EndBasketEdit()
	if !s.TryBeginTender() {
		t.Fatal("all edits ended: tender must be granted")
	}
}

// ut-docs#6923: the tender's commit point excludes abandon operations.
func TestTenderCommit_RefusesAbandonUntilEnded(t *testing.T) {
	s := NewServiceWithResolver(Config{}, nil)
	id := s.TenderAttemptID()
	if !s.BeginTenderCommit(id) {
		t.Fatal("commit must be granted for the live attempt")
	}
	if s.BeginAbandon() {
		t.Fatal("abandon must be refused while a tender commits")
	}
	s.EndTenderCommit()
	s.EndTenderCommit() // idempotent
	if !s.BeginAbandon() {
		t.Fatal("abandon must be admitted after EndTenderCommit")
	}
	s.EndAbandon()
}

func TestTenderCommit_WaitsForAdmittedAbandonThenRefuses(t *testing.T) {
	s := NewServiceWithResolver(Config{}, nil)
	id := s.TenderAttemptID()
	if !s.BeginAbandon() {
		t.Fatal("begin abandon")
	}
	got := make(chan bool, 1)
	go func() { got <- s.BeginTenderCommit(id) }()
	select {
	case <-got:
		t.Fatal("commit must wait for the admitted abandon")
	case <-time.After(50 * time.Millisecond):
	}
	if s.BeginAbandon() {
		t.Fatal("no new abandon may be admitted while the commit drains")
	}
	s.ResetBecause(AbandonParked)
	s.EndAbandon()
	select {
	case ok := <-got:
		if ok {
			t.Fatal("commit must refuse: the basket was abandoned first")
		}
	case <-time.After(time.Second):
		t.Fatal("commit did not return")
	}
	if !s.BeginAbandon() {
		t.Fatal("flag must be dropped after a refused commit")
	}
	s.EndAbandon()
}

func TestTenderCommit_AdmittedAbandonThatResetsNothingLetsCommit(t *testing.T) {
	s := NewServiceWithResolver(Config{}, nil)
	id := s.TenderAttemptID()
	s.BeginAbandon()
	got := make(chan bool, 1)
	go func() { got <- s.BeginTenderCommit(id) }()
	time.Sleep(20 * time.Millisecond)
	s.EndAbandon()
	select {
	case ok := <-got:
		if !ok {
			t.Fatal("an abandon that reset nothing must not kill the sale")
		}
	case <-time.After(time.Second):
		t.Fatal("commit did not return")
	}
	s.EndTenderCommit()
}

func TestTenderCommit_StaleAttemptRefused(t *testing.T) {
	s := NewServiceWithResolver(Config{}, nil)
	id := s.TenderAttemptID()
	s.Reset()
	if s.BeginTenderCommit(id) {
		t.Fatal("stale attempt must be refused")
	}
	if s.BeginTenderCommit("") {
		t.Fatal("empty attempt must be refused")
	}
	if !s.BeginAbandon() {
		t.Fatal("refused commit must leave abandons admitted")
	}
	s.EndAbandon()
}

func TestTenderCommit_DrainTimeoutRefusesAndReleases(t *testing.T) {
	old := tenderDrainTimeout
	tenderDrainTimeout = 50 * time.Millisecond
	t.Cleanup(func() { tenderDrainTimeout = old })
	s := NewServiceWithResolver(Config{}, nil)
	id := s.TenderAttemptID()
	s.BeginAbandon()
	if s.BeginTenderCommit(id) {
		t.Fatal("commit must be refused when abandons never drain")
	}
	s.EndAbandon()
	if !s.BeginAbandon() {
		t.Fatal("committing must be cleared after timeout")
	}
	s.EndAbandon()
}

func TestTenderCommit_IndependentOfTenderInFlight(t *testing.T) {
	s := NewServiceWithResolver(Config{}, nil)
	id := s.TenderAttemptID()
	if !s.TryBeginTender() {
		t.Fatal("begin tender")
	}
	if !s.BeginAbandon() {
		t.Fatal("abandon must be admitted while a tender only authorizes")
	}
	s.EndAbandon()
	if !s.BeginTenderCommit(id) {
		t.Fatal("commit")
	}
	s.EndTender()
	if s.BeginAbandon() {
		t.Fatal("EndTender must not clear the commit flag")
	}
	s.EndTenderCommit()
	if !s.BeginAbandon() {
		t.Fatal("abandon after EndTenderCommit")
	}
	s.EndAbandon()
}

func TestAbandon_NestedAndUnmatchedEnd(t *testing.T) {
	old := tenderDrainTimeout
	tenderDrainTimeout = 100 * time.Millisecond
	t.Cleanup(func() { tenderDrainTimeout = old })
	s := NewServiceWithResolver(Config{}, nil)
	id := s.TenderAttemptID()
	s.EndAbandon() // unmatched: no-op
	s.BeginAbandon()
	s.BeginAbandon()
	s.EndAbandon()
	s.EndAbandon()
	s.EndAbandon()
	start := time.Now()
	if !s.BeginTenderCommit(id) {
		t.Fatal("all abandons ended: commit must be granted")
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("commit must not wait when no abandon is admitted")
	}
	s.EndTenderCommit()
}
