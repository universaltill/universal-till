package pages

import (
	"fmt"
	"testing"
	"time"
)

// ut-docs#7152: the approval store caps tokens per actor so one session's
// refusal spam cannot evict another cashier's live prompt token.

func newTickStore() *approvalStore {
	t0 := time.Now()
	n := 0
	return &approvalStore{m: map[string]approvalEntry{}, now: func() time.Time {
		n++
		return t0.Add(time.Duration(n) * time.Millisecond)
	}}
}

func TestApprovalStore7152_ActorSpamDoesNotEvictOthers(t *testing.T) {
	s := newTickStore()
	b := s.mint("B", "bind-B")
	for i := 0; i < 300; i++ {
		s.mint("A", fmt.Sprintf("bind-A%d", i))
	}
	if !s.valid(b, "bind-B") {
		t.Fatal("B's token was evicted by A's spam")
	}
	n := 0
	for _, e := range s.m {
		if e.actor == "A" {
			n++
		}
	}
	if n != approvalPerActorCap {
		t.Fatalf("A holds %d, want %d", n, approvalPerActorCap)
	}
	if len(s.m) > approvalCap {
		t.Fatalf("store holds %d", len(s.m))
	}
}

func TestApprovalStore7152_ActorEvictsOwnOldest(t *testing.T) {
	s := newTickStore()
	var toks []string
	for i := 0; i < approvalPerActorCap+1; i++ {
		toks = append(toks, s.mint("A", fmt.Sprintf("b%d", i)))
	}
	if s.valid(toks[0], "b0") {
		t.Fatal("A's first token survived the 9th mint")
	}
	for i := 1; i < len(toks); i++ {
		if !s.valid(toks[i], fmt.Sprintf("b%d", i)) {
			t.Fatalf("token %d should be valid", i+1)
		}
	}
}

func TestApprovalStore7152_GlobalCapStillBounds(t *testing.T) {
	s := newTickStore()
	for a := 0; a < 40; a++ {
		for i := 0; i < approvalPerActorCap; i++ {
			s.mint(fmt.Sprintf("actor%d", a), fmt.Sprintf("b%d-%d", a, i))
			if len(s.m) > approvalCap {
				t.Fatalf("store holds %d, cap %d", len(s.m), approvalCap)
			}
		}
	}
}
