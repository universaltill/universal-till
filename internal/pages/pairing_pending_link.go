package pages

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
)

// The joining till's side of the pending link (ADR-0114 amendment
// 2026-11-03, ut-docs#5330 slice B2). While a pinned attempt waits, one
// background watch holds fleetlink's pending link to the main till; the
// waiting screen then polls THIS till every second, and only asks the main
// till when the link reports approval (or as a once-a-minute safety net).
// Without a link (older main till, plain HTTP, link refused or ended) it is
// today's 15 s poll. The link is a nudge: it never carries a token, and the
// token still comes from the per-id GET.

// Timings: the browser poll literals (never input) and the upstream throttle.
const (
	pairPollFast           = "1s"
	pairPollSlow           = "15s"
	pairUpstreamConnecting = 15 * time.Second // link not up yet: today's cadence
	pairUpstreamLinked     = 60 * time.Second // link up: safety net
	pairApprovedRetryEvery = 5 * time.Second  // approved, but the GET got 429 or a network error
)

type pendingLinkWatch struct {
	up, approved, ended atomic.Bool
	terminal            atomic.Pointer[string] // PairingDenied / PairingExpired from the link
	cancel              context.CancelFunc
	done                chan struct{}
}

// stop ends the watch and waits for its goroutine. Nil-safe, idempotent.
func (w *pendingLinkWatch) stop() {
	if w == nil {
		return
	}
	w.cancel()
	<-w.done
}

// pendingLinkRunner owns every watch goroutine. It is registered once by
// registerPairingJoinAPI (Init time, single-threaded): wg.Add(1) there, and a
// goroutine that waits for bgCtx.Done(), sets closed under mu, waits for
// r.wg, then calls wg.Done(). start() calls r.wg.Add(1) only under mu while
// !closed, so no Add can race the Wait (the "Add concurrent with Wait"
// misuse common/deps.go's AsyncWork note warns about).
type pendingLinkRunner struct {
	ctx    context.Context
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
}

func newPendingLinkRunner(bgCtx context.Context, wg *sync.WaitGroup) *pendingLinkRunner {
	r := &pendingLinkRunner{ctx: bgCtx}
	wg.Add(1)
	go func() {
		defer logging.RecoverAndLog("pairing.pendingLinkRunner")
		defer wg.Done()
		<-bgCtx.Done()
		r.mu.Lock()
		r.closed = true
		r.mu.Unlock()
		r.wg.Wait()
	}()
	return r
}

// pendingLinkAttempt is a value snapshot taken at pair-start. The goroutine
// never reads *pendingJoinState, which other goroutines copy and mutate
// under rp.mu.
type pendingLinkAttempt struct {
	dialURL, pin, id, secret string
	ttlLeft                  time.Duration
}

func attemptOf(s *pendingJoinState) pendingLinkAttempt {
	return pendingLinkAttempt{
		dialURL: s.dialURL, pin: s.pin, id: s.id, secret: s.requestSecret,
		ttlLeft: pairingRequestTTL - pairingJoinNow().Sub(s.requestedAt),
	}
}

// Test seams.
var watchPending = fleetlink.WatchPending
var pendingLinkOptions = func() fleetlink.PendingOptions { return fleetlink.PendingOptions{} }

// start runs a watch for a; nil once the runner is closed (shutdown).
func (r *pendingLinkRunner) start(a pendingLinkAttempt) *pendingLinkWatch {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	watchCtx, cancel := context.WithCancel(r.ctx)
	w := &pendingLinkWatch{cancel: cancel, done: make(chan struct{})}
	r.wg.Add(1)
	go func() {
		// Recover first (TestNoUnrecoveredGoroutines): it runs last, after
		// the bookkeeping defers below have marked the watch ended.
		defer logging.RecoverAndLog("pairing.pendingLink")
		defer r.wg.Done()
		defer close(w.done)
		defer func() { w.up.Store(false); w.ended.Store(true) }()
		defer cancel()
		ctx, tcancel := context.WithTimeout(watchCtx, a.ttlLeft)
		defer tcancel()
		o := pendingLinkOptions()
		o.OnUp = w.up.Store
		res := watchPending(ctx, fleetlink.PendingTarget{BaseURL: a.dialURL, ID: a.id, Secret: a.secret,
			HTTPClient: newPairPinnedClient(a.pin).Client}, o)
		if res.End == fleetlink.PendingResolved {
			switch res.Status {
			case fleetlink.PairingApproved:
				w.approved.Store(true)
			case fleetlink.PairingDenied, fleetlink.PairingExpired:
				s := res.Status
				w.terminal.Store(&s)
			}
		}
		// Integers only: never the URL, id, secret, a header or error text.
		logging.L().Debugf("pairing: pending link ended: end %d http %d close %d", res.End, res.HTTPStatus, int(res.CloseCode))
	}()
	return w
}

// linkTerminal: the link reported the request denied or expired.
func (s *pendingJoinState) linkTerminal() bool {
	return s.link != nil && s.link.terminal.Load() != nil
}

// upstreamDue: whether this pair-status tick asks the main till (the
// per-id GET). Called with rp.mu held.
func (s *pendingJoinState) upstreamDue(now time.Time) bool {
	switch l := s.link; {
	case l == nil:
		return true
	case l.approved.Load():
		return s.lastApprovedTry.IsZero() || now.Sub(s.lastApprovedTry) >= pairApprovedRetryEvery
	case l.ended.Load():
		return true
	case l.up.Load():
		return now.Sub(s.lastUpstream) >= pairUpstreamLinked
	default:
		return now.Sub(s.lastUpstream) >= pairUpstreamConnecting
	}
}

// pollEvery is the browser's poll interval for a waiting render: one second
// while a link can still bring news (or an approval is being collected).
func (s *pendingJoinState) pollEvery() string {
	if s.status == "waiting" && s.link != nil && (!s.link.ended.Load() || s.link.approved.Load()) {
		return pairPollFast
	}
	return pairPollSlow
}

// renderKey names what a waiting render shows: the attempt's code plus the
// poll interval. The browser echoes it back (hx-vals "have"), and a poll
// whose key still matches gets 204 and no swap — an aria-live region must
// not re-announce an identical screen every second (UX gate, ut-docs#5330).
func (s *pendingJoinState) renderKey() string { return s.code() + "/" + s.pollEvery() }
