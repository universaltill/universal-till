package fleetlink

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// ResendHello (ut-docs#4423): a hello whose content changed while the
// links were up — a key rotation's next pin — reaches every live link now,
// not at its next reconnect.

func TestHub_ResendHelloCarriesTheCurrentHello(t *testing.T) {
	var next atomic.Value
	next.Store("")
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: func(ctx context.Context, tillID string) Hello {
		h := testHello(ctx, tillID)
		h.NextPin = next.Load().(string)
		return h
	}})
	defer hub.Close()
	a := linkedReplica(t, hub, "till-2", "replica")
	b := linkedReplica(t, hub, "till-3", "replica")

	next.Store("ab12")
	if n := hub.ResendHello(context.Background()); n != 2 {
		t.Fatalf("ResendHello reached %d links, want 2", n)
	}
	for id, fc := range map[string]*fakeConn{"till-2": a, "till-3": b} {
		e, ok := fc.nextOfType(t, TypeHello, time.Second)
		if !ok {
			t.Fatalf("%s got no second hello", id)
		}
		h := decodeInto[Hello](t, e.Payload)
		if h.NextPin != "ab12" || h.PeerTillID != id {
			t.Fatalf("%s hello = %+v; want next_pin ab12 for %s", id, h, id)
		}
	}
}

func TestHub_ResendHelloNoLinks(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	if n := hub.ResendHello(context.Background()); n != 0 {
		t.Fatalf("ResendHello with no links = %d", n)
	}
}

// The replica side takes a hello at any point of a live link: OnHello
// runs again, so a next pin announced later is held without a reconnect.
func TestClient_SecondHelloOnLiveLinkReachesOnHello(t *testing.T) {
	cfg := fastConfig()
	h := newSwapHarness(t, cfg)
	tt := &testTarget{}
	tt.set(Target{BaseURL: h.srv.URL, Bearer: "good-till-2"})
	rec := &clientRec{}
	c := NewClient(fastClientOptions(cfg, tt, rec, advertised()))
	startClient(t, c)
	waitFor(t, "linked", c.Linked)
	hellos := func() int {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		return len(rec.hellos)
	}
	waitFor(t, "first hello", func() bool { return hellos() == 1 })
	dials := h.dials.Load()

	if n := h.currentHub().ResendHello(context.Background()); n != 1 {
		t.Fatalf("ResendHello reached %d links, want 1", n)
	}
	waitFor(t, "second hello", func() bool { return hellos() == 2 })
	if got := h.dials.Load(); got != dials {
		t.Fatalf("the link redialled (%d → %d dials); a resent hello must not drop it", dials, got)
	}
}
