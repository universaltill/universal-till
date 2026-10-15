package agentaction

import (
	"strconv"
	"testing"
	"time"
)

// ADR-0153 Decision 6 (ut-docs#4143): at most MaxToolCallsPerRequest tool
// calls per operator request (one chat turn, across its events and jobs).
func TestToolCallBudget_25PerRequest_4143(t *testing.T) {
	b := NewToolCallBudget()
	op := Operator{UserID: "u1", Role: "manager", Session: "s1", Request: "r1"}
	for i := 1; i <= MaxToolCallsPerRequest; i++ {
		if !b.Take(op) {
			t.Fatalf("call %d refused, want allowed", i)
		}
	}
	if b.Take(op) {
		t.Fatalf("call %d allowed, want refused", MaxToolCallsPerRequest+1)
	}
	// Another turn of the same session, and the same request id on another
	// session, each have their own budget.
	if !b.Take(Operator{UserID: "u1", Session: "s1", Request: "r2"}) {
		t.Fatal("a new request on the same session was refused")
	}
	if !b.Take(Operator{UserID: "u2", Session: "s2", Request: "r1"}) {
		t.Fatal("the same request id on another session was refused")
	}
}

func TestToolCallBudget_IncompleteOperatorRefused_4143(t *testing.T) {
	b := NewToolCallBudget()
	for _, op := range []Operator{{Session: "s"}, {Request: "r"}, {}} {
		if b.Take(op) {
			t.Fatalf("operator %+v without session and request was allowed", op)
		}
	}
}

func TestToolCallBudget_SweepsAndBounds_4143(t *testing.T) {
	b := NewToolCallBudget()
	now := time.Unix(1_700_000_000, 0)
	b.now = func() time.Time { return now }
	first := Operator{Session: "s", Request: "first"}
	if !b.Take(first) {
		t.Fatal("first turn refused")
	}
	for i := 1; i < maxTrackedTurns; i++ {
		now = now.Add(time.Millisecond)
		if !b.Take(Operator{Session: "s", Request: strconv.Itoa(i)}) {
			t.Fatalf("turn %d refused below the bound", i)
		}
	}
	// Full: a new turn of another session is still allowed — the oldest
	// turn is evicted, never every session locked out.
	now = now.Add(time.Millisecond)
	if !b.Take(Operator{Session: "other", Request: "new"}) {
		t.Fatal("a new turn was refused with the tracker full")
	}
	if n := len(b.turns); n != maxTrackedTurns {
		t.Fatalf("tracked turns = %d, want the bound %d", n, maxTrackedTurns)
	}
	if _, kept := b.turns["s\x00first"]; kept {
		t.Fatal("the oldest turn was not the one evicted")
	}
	// Past the window every old turn is swept at once.
	now = now.Add(turnWindow)
	if !b.Take(Operator{Session: "s", Request: "late"}) {
		t.Fatal("a new turn was refused after the window")
	}
	if n := len(b.turns); n != 1 {
		t.Fatalf("tracked turns after the sweep = %d, want 1", n)
	}
}
