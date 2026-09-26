// Package cloudlink is the till side of ADR-0117's cloud link: the one
// outbound socket a store's main till holds to the cloud on the realtime
// tier (ut-docs#2824). It carries nudges and small events, never directives
// or data: a `nudge`, or a cloud hello whose link_version differs from the
// last one seen, only kicks the existing cloudsync check-in (single-flight,
// cloudsync.Hooks.Kick), which stays the source of truth.
//
// The transport, envelope, heartbeat and bounds are internal/fleetlink's
// (fleetlink.Session); the wire contract is ut-cloud's internal/tilllink —
// change one side only together with the other.
//
// Offline-first (ADR-0117 §8): the link runs on its own goroutine, nothing
// a sale does waits on it (Sale never blocks and drops when it can't send),
// and a dead socket means the till behaves exactly like a periodic one.
package cloudlink

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
)

// linkPath is the cloud's endpoint under the configured cloud base URL
// (which already ends in /api, as /v1/stores/sync does for cloudsync).
const linkPath = "/v1/tills/link"

// Message types this side speaks beyond fleetlink's own (ADR-0117 §4).
const (
	typeNudge    = "nudge"     // cloud → till
	typeLiveView = "live_view" // cloud → till
	typeSale     = "sale"      // till → cloud
	typeStatus   = "status"    // till → cloud
)

// Close codes the cloud sends (ut-cloud internal/tilllink/config.go).
const (
	closeTryAgain    fleetlink.CloseCode = 1013 // the pod is full
	closeReplaced    fleetlink.CloseCode = 4001 // a newer link for this store took over
	closeRevoked     fleetlink.CloseCode = 4003
	closeTierChanged fleetlink.CloseCode = 4010
	closeNotMainTill fleetlink.CloseCode = 4011
	closeRateLimited fleetlink.CloseCode = 4029
)

// Sale-frame budget: the cloud drops beyond a 20-frame bucket refilled at
// 5/s and closes a till that keeps trying (ADR-0117 §7), so the till stays
// inside it and drops the excess itself.
const (
	saleBurst = 20
	saleEvery = 200 * time.Millisecond
)

// DefaultConfig is ADR-0117 §7's per-connection bounds on fleetlink's
// transport: ping every 30 s, gone after 75 s, 16 KiB messages, a 32
// message / 256 KiB outbound queue, 10 s write timeout.
func DefaultConfig() fleetlink.Config {
	c := fleetlink.DefaultConfig()
	c.PingInterval = 30 * time.Second
	c.PeerTimeout = 75 * time.Second
	c.WriteTimeout = 10 * time.Second
	c.ReadLimit = 16 << 10
	c.QueueMessages = 32
	c.QueueBytes = 256 << 10
	return c
}

// Target is where the cloud is and who this till is.
type Target struct {
	BaseURL  string // the enrolment's cloud endpoint, e.g. https://cloud.universaltill.com/api
	StoreID  string
	Bearer   string // this till's ADR-0116 device credential
	DeviceID string
}

// Status is the main till's link report (ut-cloud tilllink.Status).
type Status struct {
	Version     string       `json:"version"`
	UpdateState string       `json:"update_state"`
	Peers       []PeerStatus `json:"peers,omitempty"`
}

// PeerStatus is one LAN peer as the main till sees it.
type PeerStatus struct {
	TillID      string `json:"till_id"`
	Link        string `json:"link"` // up | down
	Version     string `json:"version"`
	UpdateState string `json:"update_state"`
}

// Sale is the §4 sale summary: no line items, no customer or card data.
type Sale struct {
	ID         string
	TillID     string // "" = this device
	Time       time.Time
	TotalMinor int64
	Currency   string
	TenderKind string
	ItemCount  int
	Refund     bool
	Void       bool
}

type salePayload struct {
	ID         string `json:"id"`
	TillID     string `json:"till_id"`
	Time       string `json:"time"`
	TotalMinor int64  `json:"total_minor"`
	Currency   string `json:"currency"`
	TenderKind string `json:"tender_kind"`
	ItemCount  int    `json:"item_count"`
	Refund     bool   `json:"refund"`
	Void       bool   `json:"void"`
}

type tillHello struct {
	DeviceID    string `json:"device_id"`
	Version     string `json:"version"`
	Platform    string `json:"platform"`
	Envelope    int    `json:"envelope"`
	LinkVersion int64  `json:"link_version"`
}

type cloudHello struct {
	LinkVersion int64 `json:"link_version"`
	LiveView    bool  `json:"live_view"`
}

// Options configures a Client. Zero durations take ADR-0117's values.
type Options struct {
	Config fleetlink.Config // zero fields: DefaultConfig

	// Target is the dial gate, re-read before every dial and after every
	// check-in: false unless this till is the store's main till, enrolled,
	// and the cached cloud_link tier is realtime. The cloud enforces the
	// same (403 / 4010 / 4011); this keeps a replica or periodic till from
	// dialling at all.
	Target func(ctx context.Context) (Target, bool)
	// Kick asks cloudsync for a check-in now. Must not block (a non-
	// blocking send on a capacity-1 channel).
	Kick func()
	// Status builds the current status frame (on connect, then on change).
	Status func(ctx context.Context) Status

	Version, Platform string // for the hello

	RecheckEvery time.Duration // gate re-read while idle and while linked (30 s)
	RedialSpread time.Duration // first redial after an abnormal close: U(0, this) (§7: 60 s)
	BackoffMin   time.Duration // full-jitter redial backoff floor ceiling (§7: 1 s)
	BackoffMax   time.Duration // … and cap (§7: 5 min)
	DialTimeout  time.Duration // upgrade handshake bound (10 s)
	StatusEvery  time.Duration // status change check (§7: at most every 30 s)
	StableLink   time.Duration // a link up this long resets the redial backoff (30 s)
}

func (o *Options) defaults() {
	o.Config = withConfigDefaults(o.Config)
	for _, f := range []struct {
		v *time.Duration
		d time.Duration
	}{
		{&o.RecheckEvery, 30 * time.Second}, {&o.RedialSpread, 60 * time.Second},
		{&o.BackoffMin, time.Second}, {&o.BackoffMax, 5 * time.Minute},
		{&o.DialTimeout, 10 * time.Second}, {&o.StatusEvery, 30 * time.Second},
		{&o.StableLink, 30 * time.Second},
	} {
		if *f.v <= 0 {
			*f.v = f.d
		}
	}
}

func withConfigDefaults(c fleetlink.Config) fleetlink.Config {
	d := DefaultConfig()
	if c.PingInterval <= 0 {
		c.PingInterval = d.PingInterval
	}
	if c.PeerTimeout <= 0 {
		c.PeerTimeout = d.PeerTimeout
	}
	if c.WriteTimeout <= 0 {
		c.WriteTimeout = d.WriteTimeout
	}
	if c.ReadLimit <= 0 {
		c.ReadLimit = d.ReadLimit
	}
	if c.QueueMessages <= 0 {
		c.QueueMessages = d.QueueMessages
	}
	if c.QueueBytes <= 0 {
		c.QueueBytes = d.QueueBytes
	}
	return c // the rest: fleetlink's own defaults
}

// State is what the link is doing, for the status surfaces.
type State int32

const (
	// StateIdle: not eligible (not the main till, periodic tier, or not
	// enrolled) — the check-in alone, as on every periodic till.
	StateIdle State = iota
	// StateConnecting: dialling, or waiting to redial after a drop.
	StateConnecting
	// StateLinked: the socket is up and the cloud's hello arrived.
	StateLinked
	// StateWaiting: the cloud refused this till (not main, tier changed,
	// link full); it retries after the next check-in or Retry-After.
	StateWaiting
	// StateRevoked: the credential was refused; nothing is dialled until
	// it changes.
	StateRevoked
)

func (s State) String() string {
	switch s {
	case StateConnecting:
		return "connecting"
	case StateLinked:
		return "linked"
	case StateWaiting:
		return "waiting"
	case StateRevoked:
		return "revoked"
	}
	return "idle"
}

// Client is the main till's cloud-link client. Goroutines: Run's own, plus
// the Session's reader and writer while a socket is open.
type Client struct {
	o Options

	state    atomic.Int32
	liveView atomic.Bool
	cur      atomic.Pointer[fleetlink.Session]
	deviceID atomic.Value // string: the linked target's device id

	checkedIn chan struct{} // cap 1: a check-in succeeded
	recheck   chan struct{} // cap 1: a check-in ran (either way)
	helloIn   chan int64    // cap 1: the cloud's hello link_version
	nudgeIn   chan int64    // cap 1: a nudge's link_version (coalesced)

	lastVersion atomic.Int64 // the last link_version seen (hello or nudge)
	lastAttempt atomic.Int64 // the redial attempt count Run carries (read by tests)

	smu        sync.Mutex // sale bucket
	saleTokens float64
	saleAt     time.Time

	rng func() float64 // U[0,1); a stub in tests
}

// New builds a Client; Run starts it.
func New(o Options) *Client {
	o.defaults()
	return &Client{
		o:          o,
		checkedIn:  make(chan struct{}, 1),
		recheck:    make(chan struct{}, 1),
		helloIn:    make(chan int64, 1),
		nudgeIn:    make(chan int64, 1),
		saleTokens: saleBurst,
		rng:        rand.Float64,
	}
}

// setState records a transition and logs it: the log (and so the ADR-0092
// diagnostic stream) is where the link's state shows until a status
// surface reads it.
func (c *Client) setState(s State) {
	if old := State(c.state.Swap(int32(s))); old != s {
		logging.L().Infof("cloudlink: %s -> %s", old, s)
	}
}

// CheckedIn is told every check-in's outcome (cloudsync.Hooks.AfterTick):
// the gate is re-read either way, and only a check-in that really reached
// the cloud (contacted — not a skipped or failed one) lifts a "wait for
// the next check-in" (4010/4011/4029/1013/403/409). Non-blocking; nil-safe.
func (c *Client) CheckedIn(contacted bool) {
	if c == nil {
		return
	}
	if contacted {
		poke(c.checkedIn)
	}
	poke(c.recheck)
}

func poke(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Sale sends a sale summary while the link is up and a my. viewer is
// watching (live_view on). Best-effort: dropped — never queued, never
// waited on — when the link is down, nobody watches, the queue is full or
// the §7 budget is spent. Safe from the sale path; nil-safe.
func (c *Client) Sale(s Sale) {
	if c == nil || !c.liveView.Load() {
		return
	}
	sess := c.cur.Load()
	if sess == nil || !c.takeSaleToken(time.Now()) {
		return
	}
	if s.TillID == "" {
		s.TillID, _ = c.deviceID.Load().(string)
	}
	_ = sess.Notify(typeSale, salePayload{
		ID: s.ID, TillID: s.TillID, Time: s.Time.UTC().Format(time.RFC3339),
		TotalMinor: s.TotalMinor, Currency: s.Currency, TenderKind: s.TenderKind,
		ItemCount: s.ItemCount, Refund: s.Refund, Void: s.Void,
	})
}

func (c *Client) takeSaleToken(now time.Time) bool {
	c.smu.Lock()
	defer c.smu.Unlock()
	if !c.saleAt.IsZero() {
		c.saleTokens += float64(now.Sub(c.saleAt)) / float64(saleEvery)
		if c.saleTokens > saleBurst {
			c.saleTokens = saleBurst
		}
	}
	c.saleAt = now
	if c.saleTokens < 1 {
		return false
	}
	c.saleTokens--
	return true
}

// spread is the first redial's wait after an abnormal close: U(0, RedialSpread).
func (c *Client) spread() time.Duration {
	return time.Duration(float64(c.o.RedialSpread) * c.rng())
}

// abnormalRedial is the wait after an abnormal close, given how long the
// link lived and the redial attempt count so far. A link that stayed up
// for StableLink was a working link: ADR-0117 §7's U(0, RedialSpread)
// alone, and the count resets. One cut sooner — a pod that admits and then
// cuts, a 4001 replaced loop — counts as a failed attempt: the spread plus
// the full-jitter backoff, so repeated short links back off towards
// BackoffMax instead of redialling every ≤ RedialSpread forever.
func (c *Client) abnormalRedial(lived time.Duration, attempt int) (time.Duration, int) {
	if lived >= c.o.StableLink {
		return c.spread(), 0
	}
	attempt++
	return c.spread() + c.backoff(attempt), attempt
}

// backoff is full jitter: U(0, min(BackoffMax, BackoffMin·2^(n-1))).
func (c *Client) backoff(n int) time.Duration {
	ceil := c.o.BackoffMax
	if n < 1 {
		n = 1
	}
	if n <= 30 {
		if d := c.o.BackoffMin << (n - 1); d > 0 && d < ceil {
			ceil = d
		}
	}
	return time.Duration(float64(ceil) * c.rng())
}

// linkURL derives the socket URL from the cloud base URL: https → wss;
// plain http → ws only for a loopback host (dev, e2e — ADR-0117 §1/§9).
func linkURL(base, storeID string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" {
		return "", errors.New("cloudlink: cloud address is not a URL")
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		if !isLoopback(u.Hostname()) {
			return "", errors.New("cloudlink: plain http cloud address is not loopback; the link needs https")
		}
		u.Scheme = "ws"
	default:
		return "", errors.New("cloudlink: cloud address is not http(s)")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + linkPath
	u.RawQuery = url.Values{"store_id": {storeID}}.Encode()
	u.Fragment = ""
	return u.String(), nil
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// waitKind is why Run is waiting before its next dial.
type waitKind int

const (
	waitNone    waitKind = iota
	waitTimer            // a delay (backoff, spread, Retry-After)
	waitCheckIn          // the next successful check-in (4010/4011/403/409)
	waitRecheck          // the next gate re-read (idle, revoked)
)

// Run keeps the link up while the gate allows it, until ctx ends.
func (c *Client) Run(ctx context.Context) {
	var (
		revokedFor string // bearer the cloud refused
		attempt    int    // consecutive failed dials since the last spread
	)
	for ctx.Err() == nil {
		kind, delay := waitNone, time.Duration(0)
		t, ok := c.o.Target(ctx)
		u, uerr := "", error(nil)
		if ok {
			u, uerr = linkURL(t.BaseURL, t.StoreID)
		}
		switch {
		case !ok || t.Bearer == "" || t.StoreID == "" || uerr != nil:
			c.setState(StateIdle)
			kind, delay = waitRecheck, c.o.RecheckEvery
		case revokedFor != "" && revokedFor == t.Bearer:
			c.setState(StateRevoked)
			kind, delay = waitRecheck, c.o.RecheckEvery
		default:
			revokedFor = ""
			if State(c.state.Load()) != StateWaiting {
				c.setState(StateConnecting)
			}
			var res dialResult
			res, attempt = c.attempt(ctx, t, u, attempt)
			c.lastAttempt.Store(int64(attempt))
			kind, delay = res.kind, res.delay
			if res.revoked {
				revokedFor = t.Bearer
				c.setState(StateRevoked)
				logging.L().Warnf("cloudlink: the cloud refused this till's credential; not dialling until it changes")
			}
		}
		if !c.wait(ctx, kind, delay) {
			return
		}
	}
}

type dialResult struct {
	kind    waitKind
	delay   time.Duration
	revoked bool
}

// attempt dials once and, when that works, runs the link until it ends.
func (c *Client) attempt(ctx context.Context, t Target, u string, attempt int) (dialResult, int) {
	// Signals from before this link must not leak into it.
	drain(c.checkedIn)
	select {
	case <-c.helloIn:
	default:
	}
	select {
	case <-c.nudgeIn:
	default:
	}

	dctx, cancel := context.WithTimeout(ctx, c.o.DialTimeout)
	sess, err := fleetlink.DialSession(dctx, u, t.Bearer, c.o.Config, fleetlink.SessionHandlers{
		Hello: func(context.Context) any {
			return tillHello{DeviceID: t.DeviceID, Version: c.o.Version, Platform: c.o.Platform,
				Envelope: fleetlink.Version, LinkVersion: c.lastVersion.Load()}
		},
		OneWay:    []string{typeNudge, typeLiveView},
		OnHello:   c.gotHello,
		OnMessage: c.gotMessage,
	})
	cancel()
	if err != nil {
		if ctx.Err() != nil {
			return dialResult{}, attempt
		}
		var de *fleetlink.DialError
		if errors.As(err, &de) {
			c.setState(StateWaiting)
			switch {
			case de.Status == http.StatusUnauthorized:
				return dialResult{kind: waitRecheck, delay: c.o.RecheckEvery, revoked: true}, 0
			case de.RetryAfter > 0:
				return dialResult{kind: waitTimer, delay: de.RetryAfter}, 0
			default: // 403 not_main_till / tier_periodic, 409, 503 without Retry-After, 404…
				logging.L().Infof("cloudlink: the cloud refused the link (HTTP %d); retrying after the next check-in", de.Status)
				return dialResult{kind: waitCheckIn}, 0
			}
		}
		attempt++
		return dialResult{kind: waitTimer, delay: c.backoff(attempt)}, attempt
	}

	c.deviceID.Store(t.DeviceID)
	began := time.Now()
	end := c.runLink(ctx, t, sess)
	switch {
	case ctx.Err() != nil:
		return dialResult{}, 0
	case end.gated:
		return dialResult{kind: waitNone}, 0 // re-read the gate now
	case end.code == closeRevoked:
		return dialResult{kind: waitRecheck, delay: c.o.RecheckEvery, revoked: true}, 0
	case end.code == closeNotMainTill, end.code == closeTierChanged,
		end.code == closeTryAgain, end.code == closeRateLimited:
		// A full pod (1013) or a till over its budget (4029) is not cured by
		// redialling on a timer: like a 503 without Retry-After, wait for the
		// next check-in.
		c.setState(StateWaiting)
		logging.L().Infof("cloudlink: the cloud closed the link (%d); waiting for the next check-in", end.code)
		return dialResult{kind: waitCheckIn}, 0
	}
	// Every other close — no bye, bye{deploying}, 4001 replaced, a missed
	// heartbeat, a reset — is ADR-0117 §7's abnormal close: U(0, 60 s)
	// first, so a cloud deploy's cut doesn't bring every till back in the
	// same second, growing while links keep dying young.
	c.setState(StateConnecting)
	d, next := c.abnormalRedial(time.Since(began), attempt)
	if end.code == closeReplaced {
		logging.L().Infof("cloudlink: the cloud replaced this link (4001); redialling in %v", d.Round(time.Second))
	}
	return dialResult{kind: waitTimer, delay: d}, next
}

type linkEnd struct {
	code  fleetlink.CloseCode
	gated bool // we closed it: the gate said no, or the target changed
}

// runLink runs one open socket until it ends, on Run's goroutine.
func (c *Client) runLink(ctx context.Context, t Target, sess *fleetlink.Session) (end linkEnd) {
	c.cur.Store(sess)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sess.Run()
	}()
	defer func() {
		c.cur.CompareAndSwap(sess, nil)
		c.liveView.Store(false)
	}()

	recheck := time.NewTicker(c.o.RecheckEvery)
	defer recheck.Stop()
	statusT := time.NewTicker(c.o.StatusEvery)
	defer statusT.Stop()

	var sent []byte
	sendStatus := func() {
		if c.o.Status == nil || State(c.state.Load()) != StateLinked {
			return
		}
		st := c.o.Status(ctx)
		b, err := json.Marshal(st)
		if err != nil || string(b) == string(sent) {
			return
		}
		if sess.Notify(typeStatus, st) == nil {
			sent = b
		}
	}
	gateMoved := func() bool {
		nt, ok := c.o.Target(ctx)
		return !ok || nt != t
	}

	for {
		select {
		case <-ctx.Done():
			sess.Close(fleetlink.ByeShutdown)
			<-done
			return linkEnd{}
		case <-done:
			e := sess.End()
			if e.PeerBye == "" && e.RemoteCode == 0 {
				logging.L().Infof("cloudlink: link dropped (timed out: %t); redialling", e.TimedOut)
			}
			return linkEnd{code: e.RemoteCode}
		case v := <-c.helloIn:
			c.setState(StateLinked)
			if v != c.lastVersion.Swap(v) {
				c.kick()
			}
			sendStatus()
		case v := <-c.nudgeIn:
			c.lastVersion.Store(v)
			c.kick()
		case <-statusT.C:
			sendStatus()
		case <-c.recheck:
			if gateMoved() {
				sess.Close(fleetlink.ByeShutdown)
				<-done
				return linkEnd{gated: true}
			}
		case <-recheck.C:
			if gateMoved() {
				sess.Close(fleetlink.ByeShutdown)
				<-done
				return linkEnd{gated: true}
			}
		}
	}
}

func (c *Client) kick() {
	if c.o.Kick != nil {
		c.o.Kick()
	}
}

// gotHello / gotMessage run on the link's reader goroutine: they only
// record and signal Run's goroutine.
func (c *Client) gotHello(raw json.RawMessage) {
	var h cloudHello
	if json.Unmarshal(raw, &h) != nil {
		return
	}
	c.liveView.Store(h.LiveView)
	offerLatest(c.helloIn, h.LinkVersion)
}

func (c *Client) gotMessage(env fleetlink.Envelope) {
	switch env.Type {
	case typeNudge:
		var n struct {
			LinkVersion int64 `json:"link_version"`
		}
		if json.Unmarshal(env.Payload, &n) == nil {
			offerLatest(c.nudgeIn, n.LinkVersion)
		}
	case typeLiveView:
		var lv struct {
			On bool `json:"on"`
		}
		if json.Unmarshal(env.Payload, &lv) == nil {
			c.liveView.Store(lv.On)
		}
	}
}

// offerLatest puts v on a capacity-1 channel, replacing an unread value:
// Run only ever needs the newest link_version, and a nudge must never be
// lost to a full channel (the reader never blocks).
func offerLatest(ch chan int64, v int64) {
	for {
		select {
		case ch <- v:
			return
		default:
		}
		select {
		case <-ch:
		default:
		}
	}
}

func drain(ch chan struct{}) {
	select {
	case <-ch:
	default:
	}
}

// wait sleeps for kind, cut short by ctx; false only when ctx ended.
func (c *Client) wait(ctx context.Context, kind waitKind, d time.Duration) bool {
	switch kind {
	case waitNone:
		return ctx.Err() == nil
	case waitCheckIn:
		select {
		case <-ctx.Done():
			return false
		case <-c.checkedIn:
			return true
		}
	}
	t := time.NewTimer(d)
	defer t.Stop()
	if kind == waitRecheck {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		case <-c.recheck:
		}
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
