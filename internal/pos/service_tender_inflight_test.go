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
