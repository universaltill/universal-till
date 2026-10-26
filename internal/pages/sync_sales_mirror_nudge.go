package pages

// The sales copy's fast path (ut-docs#5074 M4; ADR-0114 amendment
// 2026-10-26, design §5.2). Within about a second of a sale or close being
// logged on the main till, every linked additional till is told over the
// existing link to pull its copy: a `sync` nudge with the sales_mirror scope.
// The records themselves still travel over the HTTP feed, the authority, so
// a missed, duplicated or reordered nudge can only delay a pull, never
// change the copy (deviation from the design's record-carrying frames,
// recorded in the review: it keeps ADR-0114 §1's "bulk data on HTTP" and the
// link's 64-message queue out of the copy's correctness; the frames stay
// reserved in the contract).
//
// Wake-ups: a poke right after the journal applies another till's sales
// (Deps.PokeSalesMirrorNudge, called from POST /api/sync/sales — never from
// this till's own checkout), and a once-a-second look at the log head while
// a peer is linked and the copy is on (every 10 s otherwise).
//
// Never on the sale path: the watcher runs in its own goroutine, after the
// sale committed. With no linked peer it touches nothing; with peers but the
// copy off it reads the settings once every 10 s and nothing else; on, it
// reads MAX(seq) once a second.

import (
	"context"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

const (
	salesMirrorNudgeEvery     = time.Second
	salesMirrorNudgeEveryIdle = 10 * time.Second
	salesMirrorNudgeErrEvery  = time.Minute
)

// salesMirrorNudger remembers the head it last announced.
type salesMirrorNudger struct {
	d       *common.Deps
	repo    *data.SalesMirrorRepo
	peers   func() int
	nudge   func()
	last    int64
	lastErr time.Time
	// on / checked: the gate, re-read at most every salesMirrorNudgeEveryIdle.
	on      bool
	checked time.Time
}

func newSalesMirrorNudger(d *common.Deps) *salesMirrorNudger {
	n := &salesMirrorNudger{d: d, repo: data.NewSalesMirrorRepo(d.Db)}
	n.peers = func() int {
		if d.Link == nil {
			return 0
		}
		return d.Link.Len()
	}
	n.nudge = func() { d.NudgeLink(fleetlink.ScopeSalesMirror) }
	return n
}

// seed reads the head once at start, so a sale logged before the first
// tick still counts as a move (M4 review finding 1).
func (n *salesMirrorNudger) seed(ctx context.Context) {
	if head, err := n.repo.LogHead(ctx); err == nil {
		n.last = head
	}
}

// tick announces a moved head; it returns whether the copy is active here
// (a peer linked and the copy on: look again in a second, else in ten). The
// gate is re-read at most every ten seconds, so a till with peers but the
// copy off does nothing else.
func (n *salesMirrorNudger) tick(ctx context.Context) bool {
	if n.peers() == 0 {
		return false
	}
	if n.checked.IsZero() || time.Since(n.checked) >= salesMirrorNudgeEveryIdle {
		n.on, n.checked = salesMirrorMainGate(ctx, n.d), time.Now()
	}
	if !n.on {
		return false
	}
	head, err := n.repo.LogHead(ctx)
	if err != nil {
		if time.Since(n.lastErr) >= salesMirrorNudgeErrEvery {
			n.lastErr = time.Now()
			logging.L().Warnf("sales mirror: read the change-log head: %v", err)
		}
		return true
	}
	if head > n.last {
		n.last = head
		n.nudge()
	}
	return true
}

// StartSalesMirrorNudge runs the watcher on the main till; wg joins it to
// app.Run's drain.
func StartSalesMirrorNudge(ctx context.Context, d *common.Deps, wg *sync.WaitGroup) {
	n := newSalesMirrorNudger(d)
	if d.SalesMirrorNudgeNow == nil {
		d.SalesMirrorNudgeNow = make(chan struct{}, 1)
	}
	poke := d.SalesMirrorNudgeNow
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("pages.salesMirrorNudge")
		defer wg.Done()
		n.seed(ctx)
		runSalesMirrorNudgeLoop(ctx, n, poke, salesMirrorNudgeEvery, salesMirrorNudgeEveryIdle)
	}()
}

// runSalesMirrorNudgeLoop is the loop with its intervals injectable for
// tests: tick on every poke and on the timer, every `linked` while a peer is
// linked, else every `idle`; it returns when ctx ends.
func runSalesMirrorNudgeLoop(ctx context.Context, n *salesMirrorNudger, poke <-chan struct{}, linked, idle time.Duration) {
	wait := idle
	for {
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		case <-poke:
			t.Stop()
		}
		if n.tick(ctx) {
			wait = linked
		} else {
			wait = idle
		}
	}
}
