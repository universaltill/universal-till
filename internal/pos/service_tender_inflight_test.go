package pos

import "testing"

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
