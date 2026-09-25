package fleetlink

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LinkPath is the main till's link endpoint (ADR-0114 §1).
const LinkPath = "/api/sync/link"

// ErrUnauthorized is what a Probe returns when the main till refused this
// till's bearer: the pairing is gone (revoked, or the main till was
// re-installed). The client stops dialling until the pairing changes.
var ErrUnauthorized = errors.New("fleetlink: bearer refused by the main till")

// Target is where an additional till's main till is and how to prove who
// we are: sync.primary_url and sync.bearer.
type Target struct {
	BaseURL string
	Bearer  string
}

// ClientOptions configures a Client. Zero durations take
// DefaultClientOptions' values; zero Config fields take ADR-0114's.
type ClientOptions struct {
	Config
	BackoffMin   time.Duration // first reconnect ceiling (§4: 1 s)
	BackoffMax   time.Duration // reconnect ceiling (§4: 30 s)
	ReportEvery  time.Duration // periodic report (§2: 5 min)
	RecheckEvery time.Duration // idle re-check / linked housekeeping (30 s)
	DialTimeout  time.Duration // upgrade handshake bound

	// Target returns the main till to link to; false = this till has no
	// main till (it is one, or is unpaired) and nothing is dialled.
	Target func(ctx context.Context) (Target, bool)
	// Probe asks the main till which link level it serves (GET
	// /api/sync/ping's "link"); 0 = an older main till — never dial it
	// (§11). ErrUnauthorized = the pairing is gone.
	Probe func(ctx context.Context, t Target) (int, error)
	// Hello builds this till's hello; Role defaults to "replica".
	Hello func(ctx context.Context) Hello
	// Report builds this till's report (on connect, on change, every
	// ReportEvery).
	Report func(ctx context.Context) Report

	// Callbacks, all on the client's own goroutine (never the link's
	// reader), so they may touch the database. They must not block for
	// long: the link's housekeeping waits on them.
	OnHello     func(ctx context.Context, main Hello)     // the main till's hello: the link is up
	OnSync      func(ctx context.Context, scopes []Scope) // a coalesced sync nudge
	OnLost      func(ctx context.Context, cause string)   // an established link died without a bye
	OnRevoked   func(ctx context.Context)                 // the main till revoked this till; dialling stops
	WhileLinked func(ctx context.Context)                 // every RecheckEvery while linked
}

// DefaultClientOptions returns ADR-0114's client timings.
func DefaultClientOptions() ClientOptions {
	return ClientOptions{
		Config:       DefaultConfig(),
		BackoffMin:   time.Second,
		BackoffMax:   30 * time.Second,
		ReportEvery:  5 * time.Minute,
		RecheckEvery: 30 * time.Second,
		DialTimeout:  10 * time.Second,
	}
}

// Client is an additional till's side of the link (ADR-0114 §2–§4): it
// dials its main till only when advertised, says hello, turns `sync`
// frames into pull kicks, reports, and reconnects with backoff.
//
// Goroutines: Run's own, plus the Peer's reader and writer while a link is
// open — three at most, all ended by Run's ctx. Nothing queues without
// bound: kicks and link-state signals are capacity-1 channels, sync scopes
// an OR-ed mask.
//
// Offline-first (§3): the link is a nudge. Nothing a sale does waits on it;
// every (re)connect's hello carries full state, so a missed frame can't
// leave the till behind.
type Client struct {
	opts ClientOptions
	cfg  Config

	linked      atomic.Bool
	linkChanged chan struct{} // cap 1
	redial      chan struct{} // cap 1
	helloIn     chan struct{} // cap 1: the current peer's hello arrived
	syncIn      chan struct{} // cap 1: syncMask has bits
	syncMask    atomic.Uint32

	mu         sync.Mutex
	revokedFor *Target // dialling stopped for this pairing

	// Status (ADR-0114 §10, ut-docs#2742): what the connectivity chip
	// reads. mode is a LinkMode; cur is the open link, if any; the rest
	// under smu.
	mode        atomic.Int32
	cur         atomic.Pointer[Peer]
	smu         sync.Mutex
	mainVersion string
	linkedSince time.Time
	lostAt      time.Time
	lostSeen    time.Time
	failedAt    time.Time
}

// LinkMode is what the client is doing about its main till.
type LinkMode int32

const (
	// ModeIdle: this till has no main till (it is one, or is unpaired).
	ModeIdle LinkMode = iota
	// ModeConnecting: dialling, or backing off after a failed attempt.
	ModeConnecting
	// ModePolling: the main till does not offer the link (an older build,
	// §11); this till polls it as before.
	ModePolling
	// ModeLinked: a link is open and the main till's hello arrived.
	ModeLinked
	// ModeRevoked: the main till refused this till's pairing.
	ModeRevoked
)

// ClientStatus is a snapshot of the link for the status chip. Linked is
// ADR-0114 §4's presence: the main till's hello arrived and a frame came
// within PeerTimeout (12 s) — it turns false the moment the frames stop,
// before the heartbeat check closes the link.
type ClientStatus struct {
	Mode   LinkMode
	Linked bool
	// MainVersion is the version in the main till's last hello (kept
	// after the link drops; "" until the first hello).
	MainVersion string
	// LinkedSince is when the current link came up (zero when not linked).
	LinkedSince time.Time
	// LostAt is when the main till was last heard before an established
	// link was lost without a bye; zero while linked, or when the last
	// link ended with a bye (a restart or an update is not an outage).
	LostAt time.Time
	// LostSeen is when this till noticed the loss (the heartbeat check or
	// the socket closing, up to PeerTimeout after LostAt). A contact with
	// the main till only ends the outage if it came after this — a pull
	// that was already in flight when the frames stopped proves nothing.
	LostSeen time.Time
	// FailedAt is when attempts to reach the main till started failing
	// without an answer (a probe or dial that got no HTTP response); zero
	// once the main till answers anything. A replica restarted while its
	// main till is down has no LostAt — this is what its chip goes on.
	FailedAt time.Time
}

// Status returns the link's current state. Safe for concurrent use.
func (c *Client) Status() ClientStatus { return c.statusAt(time.Now()) }

func (c *Client) statusAt(now time.Time) ClientStatus {
	c.smu.Lock()
	s := ClientStatus{
		Mode:        LinkMode(c.mode.Load()),
		MainVersion: c.mainVersion,
		LinkedSince: c.linkedSince,
		LostAt:      c.lostAt,
		LostSeen:    c.lostSeen,
		FailedAt:    c.failedAt,
	}
	c.smu.Unlock()
	s.Linked = c.linked.Load()
	if s.Linked {
		if p := c.cur.Load(); p != nil {
			last := time.Unix(0, p.lastFrame.Load())
			if now.Sub(last) > c.cfg.PeerTimeout {
				s.Linked = false
				s.LostAt, s.LostSeen = last, now
			}
		}
	}
	if !s.Linked {
		s.LinkedSince = time.Time{}
	}
	return s
}

func (c *Client) setMode(m LinkMode) { c.mode.Store(int32(m)) }

// markLinkUp records the main till's hello: the link is up, any outage over.
func (c *Client) markLinkUp(h Hello) {
	c.smu.Lock()
	c.mainVersion = h.Version
	c.linkedSince = time.Now()
	c.lostAt, c.lostSeen = time.Time{}, time.Time{}
	c.failedAt = time.Time{}
	c.smu.Unlock()
	c.setMode(ModeLinked)
}

// markAttemptFailed records that the main till gave no answer (the first
// such failure since it last answered).
func (c *Client) markAttemptFailed() {
	c.smu.Lock()
	if c.failedAt.IsZero() {
		c.failedAt = time.Now()
	}
	c.smu.Unlock()
}

// markAnswered forgets failed attempts: the main till answered something
// (any HTTP status), so it is reachable even if no link came of it.
func (c *Client) markAnswered() {
	c.smu.Lock()
	c.failedAt = time.Time{}
	c.smu.Unlock()
}

// markLinkLost records an outage starting at last (the last frame heard),
// unless one is already running: "since" is when the main till was last
// reachable, not the latest failed attempt.
func (c *Client) markLinkLost(last time.Time) {
	c.smu.Lock()
	if c.lostAt.IsZero() {
		c.lostAt, c.lostSeen = last, time.Now()
	}
	c.linkedSince = time.Time{}
	c.smu.Unlock()
}

// clearOutage forgets a running outage: the link ended with a bye (the
// main till restarting or updating), or this till has no main till.
func (c *Client) clearOutage() {
	c.smu.Lock()
	c.lostAt, c.lostSeen = time.Time{}, time.Time{}
	c.linkedSince = time.Time{}
	c.failedAt = time.Time{}
	c.smu.Unlock()
}

// NewClient builds a Client; call Run to start it.
func NewClient(opts ClientOptions) *Client {
	d := DefaultClientOptions()
	opts.Config = withDefaults(opts.Config)
	for _, f := range []struct{ v, d *time.Duration }{
		{&opts.BackoffMin, &d.BackoffMin}, {&opts.BackoffMax, &d.BackoffMax},
		{&opts.ReportEvery, &d.ReportEvery}, {&opts.RecheckEvery, &d.RecheckEvery},
		{&opts.DialTimeout, &d.DialTimeout},
	} {
		if *f.v <= 0 {
			*f.v = *f.d
		}
	}
	return &Client{
		opts:        opts,
		cfg:         opts.Config,
		linkChanged: make(chan struct{}, 1),
		redial:      make(chan struct{}, 1),
		helloIn:     make(chan struct{}, 1),
		syncIn:      make(chan struct{}, 1),
	}
}

// Linked reports whether a link is up (the main till's hello arrived and
// the link has not dropped since).
func (c *Client) Linked() bool { return c.linked.Load() }

// LinkChanged is signalled (coalesced, capacity 1) whenever Linked flips —
// the pull loop re-reads its polling floor on it. One consumer.
func (c *Client) LinkChanged() <-chan struct{} { return c.linkChanged }

// Redial asks the client to re-read its Target now: after re-discovery
// switched sync.primary_url, or a re-pairing. Non-blocking.
func (c *Client) Redial() { poke(c.redial) }

func poke(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (c *Client) setLinked(v bool) {
	if c.linked.Swap(v) != v {
		poke(c.linkChanged)
	}
}

// backoff is ADR-0114 §4's full jitter: uniform in [0, min(max, min·2^(n-1))].
func (c *Client) backoff(attempt int) time.Duration {
	ceil := c.opts.BackoffMax
	if attempt < 1 {
		attempt = 1
	}
	if attempt <= 30 {
		if d := c.opts.BackoffMin << (attempt - 1); d > 0 && d < ceil {
			ceil = d
		}
	}
	return rand.N(ceil + 1)
}

// linkURL turns sync.primary_url into the link's ws(s) URL; "" if it isn't
// an http(s) URL.
func linkURL(base string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		return "", errors.New("fleetlink: main till address is not a URL")
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", errors.New("fleetlink: main till address is not http(s)")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + LinkPath
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

// wait sleeps d, cut short by ctx or Redial. It returns false only when ctx
// ended; redialled tells the caller to skip what remains of its backoff.
func (c *Client) wait(ctx context.Context, d time.Duration) (ok, redialled bool) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false, false
	case <-t.C:
		return true, false
	case <-c.redial:
		return true, true
	}
}

func (c *Client) isRevoked(t Target) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.revokedFor != nil && *c.revokedFor == t
}

func (c *Client) markRevoked(ctx context.Context, t Target) {
	c.setMode(ModeRevoked)
	c.mu.Lock()
	first := c.revokedFor == nil || *c.revokedFor != t
	c.revokedFor = &t
	c.mu.Unlock()
	if first && c.opts.OnRevoked != nil {
		c.opts.OnRevoked(ctx)
	}
}

// Run links to the main till and keeps the link up until ctx ends.
func (c *Client) Run(ctx context.Context) {
	attempt := 0
	for ctx.Err() == nil {
		var delay time.Duration
		t, ok := c.opts.Target(ctx)
		switch {
		case !ok || t.BaseURL == "" || t.Bearer == "":
			c.setMode(ModeIdle)
			c.clearOutage()
			delay = c.opts.RecheckEvery
		case c.isRevoked(t):
			c.setMode(ModeRevoked)
			delay = c.opts.RecheckEvery
		default:
			delay, attempt = c.attempt(ctx, t, attempt)
		}
		if delay <= 0 {
			continue
		}
		ok, redialled := c.wait(ctx, delay)
		if !ok {
			return
		}
		if redialled {
			attempt = 0
		}
	}
}

// attempt probes, dials and runs one link; it returns how long to wait
// before the next try and the new backoff attempt count.
func (c *Client) attempt(ctx context.Context, t Target, attempt int) (time.Duration, int) {
	u, err := linkURL(t.BaseURL)
	if err != nil {
		return c.opts.RecheckEvery, attempt
	}
	level, err := c.opts.Probe(ctx, t)
	switch {
	case errors.Is(err, ErrUnauthorized):
		c.markRevoked(ctx, t)
		return c.opts.RecheckEvery, 0
	case err != nil:
		c.setMode(ModeConnecting)
		c.markAttemptFailed()
		attempt++
		return c.backoff(attempt), attempt
	case level < 1:
		c.setMode(ModePolling)
		c.clearOutage()               // it answered: reachable, whatever it offers
		return c.opts.RecheckEvery, 0 // an older main till: poll as today (§11)
	}
	c.markAnswered()
	if LinkMode(c.mode.Load()) != ModeLinked {
		c.setMode(ModeConnecting)
	}

	dctx, cancel := context.WithTimeout(ctx, c.opts.DialTimeout)
	conn, err := dial(dctx, u, t.Bearer, c.cfg.ReadLimit)
	cancel()
	if err != nil {
		attempt++
		var de *DialError
		if !errors.As(err, &de) || de.Status == 0 {
			c.markAttemptFailed() // no HTTP answer at all
		}
		if errors.As(err, &de) {
			switch de.Status {
			case http.StatusUnauthorized, http.StatusForbidden:
				c.markRevoked(ctx, t)
				return c.opts.RecheckEvery, 0
			case http.StatusServiceUnavailable:
				return max(de.RetryAfter, c.backoff(attempt)), attempt
			case http.StatusNotFound, http.StatusConflict:
				// Not (or no longer) a main till serving links; the pull
				// loop will find out what it is.
				return c.opts.RecheckEvery, attempt
			}
		}
		return c.backoff(attempt), attempt
	}

	end, established := c.runLink(ctx, t, conn)
	switch end {
	case endShutdown:
		return 0, 0
	case endRetarget:
		return 0, 0 // the main till moved: dial the new address now
	case endRevoked:
		c.markRevoked(ctx, t)
		return c.opts.RecheckEvery, 0
	case endReplaced:
		// Another connection holds this till id: don't fight it (§1).
		half := c.opts.BackoffMax / 2
		return half + rand.N(half+1), attempt + 1
	case endBye:
		return c.backoff(1), 1 // the main till is restarting/updating
	default: // endLost, endTryAgain
		if established || attempt < 1 {
			attempt = 1 // a new outage: start over at BackoffMin (§4)
		} else {
			attempt++
		}
		return c.backoff(attempt), attempt
	}
}

type linkEnd int

const (
	endLost linkEnd = iota
	endShutdown
	endRetarget
	endRevoked
	endReplaced
	endBye
	endTryAgain
)

// runLink runs one connected link until it ends, on Run's goroutine.
// established reports whether the main till's hello ever arrived — the
// difference between an outage that just ended and one that continues.
func (c *Client) runLink(ctx context.Context, t Target, conn Conn) (end linkEnd, established bool) {
	// Signals from a previous link must not leak into this one.
	for _, ch := range []chan struct{}{c.helloIn, c.syncIn} {
		select {
		case <-ch:
		default:
		}
	}
	c.syncMask.Store(0)

	p := newPeer(c, c.cfg, "r", "main", conn)
	c.cur.Store(p)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.run()
	}()
	defer func() {
		c.cur.CompareAndSwap(p, nil)
		c.setLinked(false)
		switch {
		case end == endLost && established:
			c.markLinkLost(time.Unix(0, p.lastFrame.Load()))
		case end == endBye:
			c.clearOutage()
		}
		if LinkMode(c.mode.Load()) == ModeLinked {
			c.setMode(ModeConnecting)
		}
	}()

	report := time.NewTicker(c.opts.ReportEvery)
	defer report.Stop()
	recheck := time.NewTicker(c.opts.RecheckEvery)
	defer recheck.Stop()

	var sent Report
	haveSent := false
	sendReport := func(force bool) {
		if c.opts.Report == nil || !c.linked.Load() {
			return
		}
		r := c.opts.Report(ctx)
		r.ReceivedAt = time.Time{}
		if !force && haveSent && r == sent {
			return
		}
		if p.notify(TypeReport, r) == nil {
			sent, haveSent = r, true
		}
	}
	retarget := func() bool {
		nt, ok := c.opts.Target(ctx)
		if ok && nt == t {
			return false
		}
		p.shutdown(CloseNormal, "main till address changed", "")
		<-done
		return true
	}

	for {
		select {
		case <-ctx.Done():
			p.shutdown(CloseShutdown, "shutting down", ByeShutdown)
			<-done
			return endShutdown, established
		case <-done:
			return c.classify(ctx, p), established
		case <-c.helloIn:
			h, ok := p.Hello()
			if !ok {
				continue
			}
			established = true
			c.markLinkUp(h)
			c.setLinked(true)
			if c.opts.OnHello != nil {
				c.opts.OnHello(ctx, h)
			}
			sendReport(true)
		case <-c.syncIn:
			if s := scopesOf(c.syncMask.Swap(0)); len(s) > 0 && c.opts.OnSync != nil {
				c.opts.OnSync(ctx, s)
			}
		case <-report.C:
			sendReport(true)
		case <-recheck.C:
			if retarget() {
				return endRetarget, established
			}
			if c.linked.Load() && c.opts.WhileLinked != nil {
				c.opts.WhileLinked(ctx)
			}
			sendReport(false) // "on change" (§2), at most every RecheckEvery
		case <-c.redial:
			if retarget() {
				return endRetarget, established
			}
		}
	}
}

// classify says why a link ended, and reports a lost one.
func (c *Client) classify(ctx context.Context, p *Peer) linkEnd {
	if ctx.Err() != nil {
		return endShutdown // this till is stopping: not the main till's fault
	}
	if p.peerBye != "" {
		return endBye
	}
	switch remoteCloseCode(p.readErr) {
	case CloseRevoked:
		return endRevoked
	case CloseReplaced:
		return endReplaced
	case CloseTryAgain:
		return endTryAgain
	case CloseShutdown:
		return endBye // going away without a bye frame: same as bye
	}
	cause := "link to the main till dropped"
	if p.closeCode == CloseGone {
		cause = "no heartbeat from the main till"
	}
	if c.opts.OnLost != nil {
		c.opts.OnLost(ctx, cause)
	}
	return endLost
}

// Client's peerHost side. These run on the link's reader/writer
// goroutines: they only signal Run's goroutine.

func (c *Client) helloFor(ctx context.Context, _ string) Hello {
	var h Hello
	if c.opts.Hello != nil {
		h = c.opts.Hello(ctx)
	}
	if h.Role == "" {
		h.Role = "replica"
	}
	return h
}

func (c *Client) onFrame() (func(string), time.Duration) { return nil, 0 }

func (c *Client) handler(string) RequestHandler { return nil }

func (c *Client) gotHello(*Peer, Hello) { poke(c.helloIn) }

func (c *Client) gotMessage(_ *Peer, env Envelope) {
	if env.Type != TypeSync {
		return // fleet/pairing: their own cards (#2726, pairing push); report is → main only
	}
	var sp SyncPayload
	if json.Unmarshal(env.Payload, &sp) != nil {
		return
	}
	var mask uint32
	for _, s := range sp.Scopes {
		mask |= scopeBit(s)
	}
	if mask == 0 {
		return
	}
	c.syncMask.Or(mask)
	poke(c.syncIn)
}
