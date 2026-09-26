package fleetlink

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// ut-docs#2893 (ADR-0117 §4): the main till relays "check in with the cloud
// now" to its linked replicas as a one-way cloud_checkin frame.

// linkedReplica serves a fake replica on hub and has it say hello as role.
func linkedReplica(t *testing.T, hub *Hub, tillID, role string) *fakeConn {
	t.Helper()
	fc, _ := servePeer(t, hub, tillID)
	if e := fc.next(t); e.Type != TypeHello {
		t.Fatalf("first frame = %s, want hello", e.Type)
	}
	fc.send(t, mustMsg(t, "h-1", TypeHello, "", Hello{TillID: tillID, Role: role}))
	waitFor(t, "peer hello", func() bool {
		p := hub.Peer(tillID)
		if p == nil {
			return false
		}
		_, ok := p.Hello()
		return ok
	})
	return fc
}

// nextOfType returns the next frame of typ, or fails after d.
func (f *fakeConn) nextOfType(t *testing.T, typ string, d time.Duration) (Envelope, bool) {
	t.Helper()
	deadline := time.After(d)
	for {
		select {
		case b := <-f.out:
			e, err := Decode(b)
			if err != nil {
				t.Fatalf("undecodable frame %s: %v", b, err)
			}
			if e.Type == typ {
				return e, true
			}
		case <-deadline:
			return Envelope{}, false
		}
	}
}

func TestHub_RelayCloudCheckinReachesLinkedReplicas(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	a := linkedReplica(t, hub, "till-2", "replica")
	b := linkedReplica(t, hub, "till-3", "replica")

	hub.RelayCloudCheckin([]string{"entitlement", "security"}, 42)

	for _, fc := range []*fakeConn{a, b} {
		e, ok := fc.nextOfType(t, TypeCloudCheckin, time.Second)
		if !ok {
			t.Fatal("no cloud_checkin frame reached a linked replica")
		}
		p := decodeInto[CloudCheckinPayload](t, e.Payload)
		if strings.Join(p.Scopes, ",") != "entitlement,security" || p.LinkVersion != 42 {
			t.Fatalf("payload = %+v", p)
		}
	}
}

// Only replicas run a cloud check-in; a satellite (or a link whose hello
// has not arrived yet) is never sent the frame.
func TestHub_RelayCloudCheckinSkipsNonReplicas(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	sat := linkedReplica(t, hub, "sat-1", "satellite")
	hub.RelayCloudCheckin(nil, 1)
	if _, ok := sat.nextOfType(t, TypeCloudCheckin, 150*time.Millisecond); ok {
		t.Fatal("a satellite was sent cloud_checkin")
	}
}

// No links: the relay is a no-op, and a replica that links afterwards is
// not sent a stale frame.
func TestHub_RelayCloudCheckinWithNoLinksIsANoOp(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	hub.RelayCloudCheckin([]string{"update"}, 3)
	fc := linkedReplica(t, hub, "till-2", "replica")
	if _, ok := fc.nextOfType(t, TypeCloudCheckin, 150*time.Millisecond); ok {
		t.Fatal("a replica linked after the relay got a stale cloud_checkin")
	}
}

// A nudge burst is at most one frame per CloudCheckinEvery per replica: the
// first goes at once, the rest coalesce (scopes merged, newest link_version)
// into one more after the window — never dropped, never queued per nudge.
func TestHub_RelayCloudCheckinIsRateLimitedAndCoalesced(t *testing.T) {
	cfg := fastConfig()
	cfg.CloudCheckinEvery = 300 * time.Millisecond
	hub := NewHub(HubOptions{Config: cfg, Hello: testHello})
	defer hub.Close()
	fc := linkedReplica(t, hub, "till-2", "replica")

	start := time.Now()
	hub.RelayCloudCheckin([]string{"directives"}, 1)
	if _, ok := fc.nextOfType(t, TypeCloudCheckin, time.Second); !ok {
		t.Fatal("first relay not sent at once")
	}
	for i := 0; i < 50; i++ {
		hub.RelayCloudCheckin([]string{"entitlement"}, int64(2+i))
	}
	hub.RelayCloudCheckin([]string{"users", "entitlement"}, 99)
	e, ok := fc.nextOfType(t, TypeCloudCheckin, 2*time.Second)
	if !ok {
		t.Fatal("the coalesced relay never went out")
	}
	if since := time.Since(start); since < cfg.CloudCheckinEvery {
		t.Fatalf("second frame after %v, want ≥ %v", since, cfg.CloudCheckinEvery)
	}
	p := decodeInto[CloudCheckinPayload](t, e.Payload)
	if strings.Join(p.Scopes, ",") != "entitlement,users" || p.LinkVersion != 99 {
		t.Fatalf("coalesced payload = %+v, want entitlement,users @99", p)
	}
	if _, ok := fc.nextOfType(t, TypeCloudCheckin, 2*cfg.CloudCheckinEvery); ok {
		t.Fatal("a third cloud_checkin frame followed one burst")
	}
}

// The frame is bounded: scopes are deduplicated, clipped and capped, so a
// relay can never grow a peer's pending state or the frame.
func TestHub_RelayCloudCheckinBoundsScopes(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	fc := linkedReplica(t, hub, "till-2", "replica")
	var many []string
	for i := 0; i < 100; i++ {
		many = append(many, strings.Repeat("x", 200)+string(rune('a'+i%26)))
	}
	hub.RelayCloudCheckin(many, 1)
	e, ok := fc.nextOfType(t, TypeCloudCheckin, time.Second)
	if !ok {
		t.Fatal("no frame")
	}
	p := decodeInto[CloudCheckinPayload](t, e.Payload)
	if len(p.Scopes) > maxCheckinScopes {
		t.Fatalf("%d scopes, want ≤ %d", len(p.Scopes), maxCheckinScopes)
	}
	for _, s := range p.Scopes {
		if len(s) > maxReportField {
			t.Fatalf("scope of %d bytes, want ≤ %d", len(s), maxReportField)
		}
	}
}

// An older replica (a build before #2893) treats cloud_checkin as an
// unknown request and answers unknown_type. The main till drops that reply
// (nobody waits on it) and the link stays up and useful.
func TestHub_OldReplicaUnknownTypeReplyIsHarmless(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	fc := linkedReplica(t, hub, "till-2", "replica")
	hub.RelayCloudCheckin([]string{"update"}, 7)
	e, ok := fc.nextOfType(t, TypeCloudCheckin, time.Second)
	if !ok {
		t.Fatal("no frame")
	}
	fc.send(t, mustMsg(t, "old-1", TypeError, e.ID, ErrorPayload{Code: CodeUnknownType}))
	hub.Nudge(ScopeAdmin)
	if _, ok := fc.nextOfType(t, TypeSync, time.Second); !ok {
		t.Fatal("the link stopped working after the old replica's unknown_type reply")
	}
	if hub.Peer("till-2") == nil {
		t.Fatal("the old replica's link was dropped")
	}
}

// And the other way round: a replica-side peer that doesn't know a type
// (what cloud_checkin is to a pre-#2893 build) answers unknown_type and
// keeps the link — the property the relay's rollout relies on.
func TestClient_UnknownOneWayTypeFromMainKeepsTheLink(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	rec := &clientRec{}
	c := NewClient(fastClientOptions(cfg, tt, rec, advertised()))
	startClient(t, c)
	waitFor(t, "linked", c.Linked)
	p := h.currentHub().Peer("till-2")
	if _, err := p.Request(context.Background(), "cloud.something_newer", nil); err == nil {
		t.Fatal("an unknown type was answered as if handled")
	} else if re, ok := err.(*RemoteError); !ok || re.Code != CodeUnknownType {
		t.Fatalf("err = %v, want unknown_type", err)
	}
	h.currentHub().Nudge(ScopeStock)
	waitFor(t, "sync still delivered", func() bool { _, s, _, _ := rec.get(); return len(s) > 0 })
}

// A replica turns cloud_checkin frames into OnCloudCheckin calls, single-
// flight: frames arriving while one call runs make exactly one more call.
func TestClient_CloudCheckinKicksOncePerBurst(t *testing.T) {
	cfg := fastConfig()
	cfg.CloudCheckinEvery = time.Millisecond // let every relay through to test the client's own coalescing
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	rec := &clientRec{}
	opts := fastClientOptions(cfg, tt, rec, advertised())
	var calls atomic.Int32
	release := make(chan struct{})
	opts.OnCloudCheckin = func(context.Context) {
		if calls.Add(1) == 1 {
			<-release
		}
	}
	c := NewClient(opts)
	startClient(t, c)
	waitFor(t, "linked", c.Linked)
	hub := h.currentHub()
	waitFor(t, "replica hello", func() bool { _, ok := hub.Peer("till-2").Hello(); return ok })

	hub.RelayCloudCheckin([]string{"entitlement"}, 1)
	waitFor(t, "first kick", func() bool { return calls.Load() == 1 })
	for i := 0; i < 5; i++ { // a burst while the first call is still running
		hub.RelayCloudCheckin([]string{"entitlement"}, int64(2+i))
		time.Sleep(5 * time.Millisecond)
	}
	close(release)
	waitFor(t, "one coalesced kick", func() bool { return calls.Load() == 2 })
	time.Sleep(100 * time.Millisecond)
	if n := calls.Load(); n != 2 {
		t.Fatalf("OnCloudCheckin calls = %d, want 2 (one, then one for the burst)", n)
	}
}

// The main till never acts on a cloud_checkin a peer sends it: replicas
// don't relay, and the frame is main → replica only.
func TestHub_IgnoresCloudCheckinFromAPeer(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	fc := linkedReplica(t, hub, "till-2", "replica")
	fc.send(t, mustMsg(t, "c-1", TypeCloudCheckin, "", CloudCheckinPayload{LinkVersion: 1}))
	deadline := time.After(150 * time.Millisecond)
	for quiet := false; !quiet; {
		select {
		case b := <-fc.out:
			if e, _ := Decode(b); e.Type != TypePing {
				t.Fatalf("hub answered a peer's cloud_checkin with %s", b)
			}
		case <-deadline:
			quiet = true
		}
	}
	if hub.Peer("till-2") == nil {
		t.Fatal("link dropped")
	}
}
