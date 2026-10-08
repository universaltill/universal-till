package fleetlink

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ut-docs#2945 (ADR-0114 §5, §8): "Update now" from the main till's Tills
// page is a one-way fleet frame naming the target version. Coalesced into a
// per-peer pending flag (latest target wins), never queued per press.

func TestHub_RequestUpdateSendsFleetFrame(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	fc := linkedReplica(t, hub, "till-2", "replica")

	if err := hub.RequestUpdate("till-2", "v1.4.0"); err != nil {
		t.Fatalf("RequestUpdate: %v", err)
	}
	e, ok := fc.nextOfType(t, TypeFleet, time.Second)
	if !ok {
		t.Fatal("no fleet frame reached the linked replica")
	}
	p := decodeInto[FleetPayload](t, e.Payload)
	if p.Target != "v1.4.0" || !p.Now {
		t.Fatalf("payload = %+v, want target v1.4.0 now=true", p)
	}
}

// No live link, or a link whose hello has not arrived: the caller is told,
// so the page can say "not linked" instead of pretending.
func TestHub_RequestUpdateNotLinked(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	if err := hub.RequestUpdate("till-9", "v1.4.0"); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("no link: err = %v, want ErrNotLinked", err)
	}
	fc, _ := servePeer(t, hub, "till-3")
	if e := fc.next(t); e.Type != TypeHello {
		t.Fatalf("first frame = %s", e.Type)
	}
	waitFor(t, "peer registered", func() bool { return hub.Peer("till-3") != nil })
	if err := hub.RequestUpdate("till-3", "v1.4.0"); !errors.Is(err, ErrNotLinked) {
		t.Fatalf("no hello yet: err = %v, want ErrNotLinked", err)
	}
	if _, ok := fc.nextOfType(t, TypeFleet, 100*time.Millisecond); ok {
		t.Fatal("a fleet frame went to a link with no hello")
	}
}

// Only the named till is asked.
func TestHub_RequestUpdateTargetsOneTill(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	a := linkedReplica(t, hub, "till-2", "replica")
	b := linkedReplica(t, hub, "till-3", "replica")
	if err := hub.RequestUpdate("till-3", "v2.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.nextOfType(t, TypeFleet, time.Second); !ok {
		t.Fatal("till-3 got no fleet frame")
	}
	if _, ok := a.nextOfType(t, TypeFleet, 100*time.Millisecond); ok {
		t.Fatal("till-2 was sent a fleet frame meant for till-3")
	}
}

// Presses while the writer is held coalesce into one frame carrying the
// latest target; the target is clipped so a caller can't grow the frame.
func TestHub_RequestUpdateCoalescesLatestWinsAndClips(t *testing.T) {
	hub := NewHub(HubOptions{Config: DefaultConfig(), Hello: testHello}) // real 10 s write timeout
	defer hub.Close()
	fc := newFakeConn()
	fc.gate = make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); hub.ServeConn("till-2", fc) }()
	defer func() { fc.Close(CloseNormal, ""); <-done }()

	// The writer is stuck on our hello (gate closed); the reader still
	// takes the replica's.
	fc.send(t, mustMsg(t, "h-1", TypeHello, "", Hello{TillID: "till-2", Role: "replica"}))
	waitFor(t, "peer hello", func() bool {
		p := hub.Peer("till-2")
		if p == nil {
			return false
		}
		_, ok := p.Hello()
		return ok
	})
	for i := 0; i < 20; i++ {
		if err := hub.RequestUpdate("till-2", "v1.0."+string(rune('a'+i))); err != nil {
			t.Fatal(err)
		}
	}
	long := "v3.0.0-" + strings.Repeat("x", 300)
	if err := hub.RequestUpdate("till-2", long); err != nil {
		t.Fatal(err)
	}
	close(fc.gate)

	if e := fc.next(t); e.Type != TypeHello {
		t.Fatalf("first = %s", e.Type)
	}
	e := fc.next(t)
	if e.Type != TypeFleet {
		t.Fatalf("second = %s, want one coalesced fleet", e.Type)
	}
	p := decodeInto[FleetPayload](t, e.Payload)
	if p.Target != clip(long, maxReportField) || !p.Now {
		t.Fatalf("payload = %+v, want the latest target clipped to %d bytes", p, maxReportField)
	}
	if _, ok := fc.nextOfType(t, TypeFleet, 100*time.Millisecond); ok {
		t.Fatal("a second fleet frame followed one burst")
	}
}

// A replica turns fleet frames into OnFleet calls with the decoded,
// clipped payload; a malformed payload is ignored and the link stays up.
func TestClient_FleetFrameCallsOnFleet(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	rec := &clientRec{}
	opts := fastClientOptions(cfg, tt, rec, advertised())
	var mu sync.Mutex
	var got []FleetPayload
	opts.OnFleet = func(_ context.Context, p FleetPayload) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	}
	c := NewClient(opts)
	startClient(t, c)
	waitFor(t, "linked", c.Linked)
	hub := h.currentHub()
	waitFor(t, "replica hello", func() bool { _, ok := hub.Peer("till-2").Hello(); return ok })

	// A malformed payload first: ignored, nothing called.
	p := hub.Peer("till-2")
	if err := p.notify(TypeFleet, "not an object"); err != nil {
		t.Fatal(err)
	}
	if err := hub.RequestUpdate("till-2", "v1.4.0"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "OnFleet", func() bool { mu.Lock(); defer mu.Unlock(); return len(got) > 0 })
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0].Target != "v1.4.0" || !got[0].Now {
		t.Fatalf("OnFleet calls = %+v, want one {v1.4.0 true}", got)
	}
	if hub.Peer("till-2") == nil {
		t.Fatal("link dropped")
	}
}

// The client clips a target it is sent, whatever the sender.
func TestClient_FleetTargetIsClipped(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	rec := &clientRec{}
	opts := fastClientOptions(cfg, tt, rec, advertised())
	var target atomic.Value
	opts.OnFleet = func(_ context.Context, p FleetPayload) { target.Store(p.Target) }
	c := NewClient(opts)
	startClient(t, c)
	waitFor(t, "linked", c.Linked)
	hub := h.currentHub()
	waitFor(t, "replica hello", func() bool { _, ok := hub.Peer("till-2").Hello(); return ok })
	if err := hub.Peer("till-2").notify(TypeFleet, FleetPayload{Target: strings.Repeat("9", 500), Now: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "OnFleet", func() bool { return target.Load() != nil })
	if s := target.Load().(string); len(s) > maxReportField {
		t.Fatalf("target of %d bytes reached OnFleet, want ≤ %d", len(s), maxReportField)
	}
}

// The main till never acts on a fleet frame a peer sends it.
func TestHub_IgnoresFleetFromAPeer(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	fc := linkedReplica(t, hub, "till-2", "replica")
	fc.send(t, mustMsg(t, "f-1", TypeFleet, "", FleetPayload{Target: "v1", Now: true}))
	deadline := time.After(150 * time.Millisecond)
	for quiet := false; !quiet; {
		select {
		case b := <-fc.out:
			if e, _ := Decode(b); e.Type != TypePing {
				t.Fatalf("hub answered a peer's fleet frame with %s", b)
			}
		case <-deadline:
			quiet = true
		}
	}
	if hub.Peer("till-2") == nil {
		t.Fatal("link dropped")
	}
}
