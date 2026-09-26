package cloudlink

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ut-docs#2893 (ADR-0117 §4): the main till relays a cloud nudge to its
// replicas — once, after its own check-in that the nudge caused.

type relayRec struct {
	mu    sync.Mutex
	calls []string // "scopes@version"
}

func (r *relayRec) relay(scopes []string, v int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, strings.Join(scopes, ",")+"@"+strconv.FormatInt(v, 10))
}

func (r *relayRec) get() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.calls...)
}

func relayHarness(t *testing.T) (*harness, *relayRec, *cloudConn) {
	t.Helper()
	rec := &relayRec{}
	h := newHarness(t, func(o *Options) { o.Relay = rec.relay })
	c := h.cloud.nextConn(t) // link_version 0 = what the client starts with: no hello kick
	c.frame(t, "hello")
	eventually(t, "linked", func() bool { return h.c.State() == StateLinked })
	return h, rec, c
}

func TestNudgeIsRelayedAfterTheCheckInItCaused(t *testing.T) {
	h, rec, c := relayHarness(t)
	c.send("nudge", map[string]any{"link_version": 7, "scopes": []string{"entitlement"}})
	c.send("nudge", map[string]any{"link_version": 8, "scopes": []string{"security", "entitlement"}})
	eventually(t, "kick", func() bool { return h.kicks.Load() >= 1 })
	time.Sleep(20 * time.Millisecond) // both nudges read

	h.c.TickStarting()
	if got := rec.get(); len(got) != 0 {
		t.Fatalf("relayed before the check-in finished: %v", got)
	}
	h.c.CheckedIn(true)
	if got := rec.get(); len(got) != 1 || got[0] != "entitlement,security@8" {
		t.Fatalf("relays = %v, want one entitlement,security@8", got)
	}
	h.c.TickStarting() // the kick's extra run, or the next scheduled one
	h.c.CheckedIn(true)
	if got := rec.get(); len(got) != 1 {
		t.Fatalf("relays = %v, want still one: a burst is one relay", got)
	}
}

// A nudge that arrives while a check-in is already running is relayed
// after the next check-in (the one that started after it), not this one.
func TestNudgeDuringARunningCheckInWaitsForTheNextOne(t *testing.T) {
	h, rec, c := relayHarness(t)
	h.c.TickStarting() // a scheduled check-in starts
	c.send("nudge", map[string]any{"link_version": 3, "scopes": []string{"update"}})
	eventually(t, "kick", func() bool { return h.kicks.Load() >= 1 })
	h.c.CheckedIn(true) // …and ends: it may have read the state before the nudge
	if got := rec.get(); len(got) != 0 {
		t.Fatalf("relayed after a check-in that predates the nudge: %v", got)
	}
	h.c.TickStarting()
	h.c.CheckedIn(true)
	if got := rec.get(); len(got) != 1 || got[0] != "update@3" {
		t.Fatalf("relays = %v, want update@3", got)
	}
}

// A check-in that didn't reach the cloud relays nothing; the relay waits
// for one that did.
func TestFailedCheckInDoesNotRelay(t *testing.T) {
	h, rec, c := relayHarness(t)
	c.send("nudge", map[string]any{"link_version": 4, "scopes": []string{"users"}})
	eventually(t, "kick", func() bool { return h.kicks.Load() >= 1 })
	h.c.TickStarting()
	h.c.CheckedIn(false)
	if got := rec.get(); len(got) != 0 {
		t.Fatalf("relayed after a failed check-in: %v", got)
	}
	h.c.TickStarting()
	h.c.CheckedIn(true)
	if got := rec.get(); len(got) != 1 || got[0] != "users@4" {
		t.Fatalf("relays = %v, want users@4", got)
	}
}

// No nudge, no relay: a replica's (or a periodic till's) own check-ins —
// including one a relayed frame kicked — never relay anything onward.
func TestCheckInWithoutANudgeNeverRelays(t *testing.T) {
	rec := &relayRec{}
	c := New(Options{Relay: rec.relay})
	for i := 0; i < 3; i++ {
		c.TickStarting()
		c.CheckedIn(true)
	}
	if got := rec.get(); len(got) != 0 {
		t.Fatalf("relays = %v, want none", got)
	}
	var nilClient *Client
	nilClient.TickStarting() // nil-safe, like CheckedIn
}

// A cloud hello with a new link_version means the cloud changed while the
// link was down: relayed like a nudge (no scopes).
func TestHelloVersionChangeIsRelayed(t *testing.T) {
	rec := &relayRec{}
	h := newHarness(t, func(o *Options) { o.Relay = rec.relay })
	h.cloud.linkVersion.Store(5)
	h.cloud.nextConn(t)
	eventually(t, "kick", func() bool { return h.kicks.Load() == 1 })
	h.c.TickStarting()
	h.c.CheckedIn(true)
	if got := rec.get(); len(got) != 1 || got[0] != "@5" {
		t.Fatalf("relays = %v, want one @5", got)
	}
}
