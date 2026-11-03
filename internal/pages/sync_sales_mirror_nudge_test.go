package pages

import (
	"context"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/fleetlink"

	"github.com/universaltill/universal-till/internal/data"
)

// M4: the main till nudges linked tills once per moved head, never with no
// peer or the copy off; the nudge carries no data.
func TestSalesMirrorNudge_OncePerMovedHead(t *testing.T) {
	shop := newMirrorShop(t, "B")
	n := newSalesMirrorNudger(shop.main.d)
	n.seed(t.Context())
	peers, nudges := 0, 0
	n.peers = func() int { return peers }
	n.nudge = func() { nudges++ }
	ctx := t.Context()
	shop.enable()
	shop.sell(shop.main, 100)
	if n.tick(ctx) || nudges != 0 {
		t.Fatalf("no peer: active or nudged (%d)", nudges)
	}
	peers = 1
	// The head was seeded before the sale, so the first look announces it
	// (M4 review finding 1: a sale before the first tick is not missed).
	if !n.tick(ctx) || nudges != 1 {
		t.Fatalf("first look after a sale: nudges %d, want 1", nudges)
	}
	nudges = 0
	if n.tick(ctx); nudges != 0 {
		t.Fatalf("unchanged head nudged: %d", nudges)
	}
	shop.sell(shop.main, 100)
	shop.sell(shop.main, 100)
	if n.tick(ctx); nudges != 1 {
		t.Fatalf("moved head: %d nudges, want 1 (coalesced)", nudges)
	}
	shop.setShop(data.SalesMirrorEnabledKey, "0")
	n.checked = time.Time{} // the gate is re-read every 10 s; force it now
	shop.sell(shop.main, 100)
	if n.tick(ctx) || nudges != 1 {
		t.Fatalf("with the copy off: active or nudged (%d)", nudges)
	}
	// On again: the next logged sale nudges.
	shop.setShop(data.SalesMirrorEnabledKey, "1")
	n.checked = time.Time{}
	shop.sell(shop.main, 100)
	if n.tick(ctx); nudges != 2 {
		t.Fatalf("after on again: %d nudges, want 2", nudges)
	}
}

// The replica side: a sales_mirror nudge kicks the copy's pull loop.
func TestRequestSalesMirrorPull_Coalesces(t *testing.T) {
	shop := newMirrorShop(t, "B")
	d := shop.tills["B"].d
	d.RequestSalesMirrorPull() // no loop: no-op, no panic
	d.SalesMirrorPullNow = make(chan struct{}, 1)
	d.RequestSalesMirrorPull()
	d.RequestSalesMirrorPull()
	if len(d.SalesMirrorPullNow) != 1 {
		t.Fatalf("kicks pending = %d, want 1", len(d.SalesMirrorPullNow))
	}
}

// A satellite asks once, then stays quiet through the link's nudges for the
// linked polling floor (M4 review finding 3).
func TestSalesMirrorPull_SatelliteStaysQuietThroughNudges(t *testing.T) {
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	if _, err := shop.main.d.Db.Exec(`UPDATE tills SET role = 'satellite' WHERE id = ?`, b.tillID); err != nil {
		t.Fatal(err)
	}
	before := shop.copyRequests.Load()
	for range 5 {
		shop.pull(b)
	}
	if n := shop.copyRequests.Load() - before; n != 1 {
		t.Fatalf("a satellite sent %d copy requests through 5 kicks, want 1", n)
	}
}

// The loop: a poke ticks at once, the timer ticks, ctx ends it.
func TestRunSalesMirrorNudgeLoop_PokeTimerAndStop(t *testing.T) {
	shop := newMirrorShop(t, "B")
	shop.enable()
	n := newSalesMirrorNudger(shop.main.d)
	n.seed(t.Context())
	nudged := make(chan struct{}, 10)
	n.peers = func() int { return 1 }
	n.nudge = func() { nudged <- struct{}{} }
	poke := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { runSalesMirrorNudgeLoop(ctx, n, poke, time.Hour, time.Hour); close(done) }()
	shop.sell(shop.main, 100)
	poke <- struct{}{}
	select {
	case <-nudged:
	case <-time.After(5 * time.Second):
		t.Fatal("a poke did not nudge at once")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop did not stop on cancel")
	}
}

// The scope owns a bit (else Hub.Nudge would silently drop it), and the
// link client kicks the copy's pull on it without kicking the admin pull.
func TestScopeSalesMirror_KicksOnlyTheCopyPull(t *testing.T) {
	if linkScopesNeedPull([]fleetlink.Scope{fleetlink.ScopeSalesMirror}) {
		t.Fatal("a sales_mirror nudge triggers the admin pull")
	}
	if !linkScopesKickSalesMirror([]fleetlink.Scope{fleetlink.ScopeAdmin, fleetlink.ScopeSalesMirror}) ||
		linkScopesKickSalesMirror([]fleetlink.Scope{fleetlink.ScopeAdmin}) {
		t.Fatal("sales_mirror kick detection is wrong")
	}
}
