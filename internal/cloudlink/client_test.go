package cloudlink

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// AC1/AC2/AC3: the main till on realtime dials /api/v1/tills/link with the
// store id in the query, the device credential in the Authorization header
// (never the URL), no Origin, and says hello with ADR-0117 §4's fields.
func TestDialsWithBearerHeaderAndSaysHello(t *testing.T) {
	h := newHarness(t, nil)
	c := h.cloud.nextConn(t)
	hello := c.frame(t, "hello")

	h.cloud.mu.Lock()
	r := h.cloud.dials[0]
	h.cloud.mu.Unlock()
	if got := r.Header.Get("Authorization"); got != "Bearer cred-1" {
		t.Fatalf("Authorization = %q, want the device credential as a bearer", got)
	}
	if _, has := r.Header["Origin"]; has {
		t.Fatal("upgrade carried an Origin header; the cloud refuses any (ADR-0117 §9)")
	}
	if r.URL.Query().Get("store_id") != "store-1" || strings.Contains(r.URL.RawQuery, "cred-1") {
		t.Fatalf("query = %q, want store_id only and never the credential", r.URL.RawQuery)
	}
	var p map[string]any
	if err := json.Unmarshal(hello.Payload, &p); err != nil {
		t.Fatalf("hello payload: %v", err)
	}
	for k, want := range map[string]any{"device_id": "dev-1", "version": "v1.2.3", "platform": "linux/arm64", "envelope": 1.0, "link_version": 0.0} {
		if p[k] != want {
			t.Fatalf("hello[%s] = %v, want %v (payload %s)", k, p[k], want, hello.Payload)
		}
	}
	eventually(t, "linked status", func() bool { return h.c.State() == StateLinked })
}

// AC1: nothing is dialled while the gate says no (a replica, or periodic).
func TestNoDialWhileGateClosed(t *testing.T) {
	cloud := newFakeCloud(t)
	o := fastOptions()
	o.Target = func(context.Context) (Target, bool) {
		return Target{BaseURL: cloud.srv.URL + "/api", StoreID: "store-1", Bearer: "cred-1", DeviceID: "dev-1"}, false
	}
	c := New(o)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	c.CheckedIn(true)
	time.Sleep(150 * time.Millisecond)
	cancel()
	<-done
	if n := cloud.dialCount(); n != 0 {
		t.Fatalf("dials with the gate closed = %d, want 0", n)
	}
	if c.State() != StateIdle {
		t.Fatalf("state = %v, want idle", c.State())
	}
}

// AC4: a differing link_version in the cloud hello kicks the check-in; the
// same one on a reconnect does not; every nudge kicks.
func TestHelloVersionAndNudgeKickTheCheckIn(t *testing.T) {
	h := newHarness(t, nil)
	h.cloud.linkVersion.Store(5)
	h.cloud.nextConn(t) // first connect: 5 != 0 → one kick
	eventually(t, "kick on a differing link_version", func() bool { return h.kicks.Load() == 1 })

	h.stop() // restart the client against the same cloud: it remembers 5
	h.done = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { defer close(h.done); h.c.Run(ctx) }()
	c := h.cloud.nextConn(t)
	hello := c.frame(t, "hello")
	if !strings.Contains(string(hello.Payload), `"link_version":5`) {
		t.Fatalf("hello = %s, want the last link_version seen (5)", hello.Payload)
	}
	time.Sleep(50 * time.Millisecond)
	if k := h.kicks.Load(); k != 1 {
		t.Fatalf("kicks after a same-version hello = %d, want still 1", k)
	}
	c.send("nudge", map[string]any{"link_version": 6, "scopes": []string{"directives"}})
	eventually(t, "kick on a nudge", func() bool { return h.kicks.Load() == 2 })
}

// AC6: 4011 not_main_till parks the client until a successful check-in.
func TestNotMainTillWaitsForACheckIn(t *testing.T) {
	h := newHarness(t, nil)
	c := h.cloud.nextConn(t)
	c.frame(t, "hello")
	c.closeWith(4011, "not_main_till")
	time.Sleep(200 * time.Millisecond) // far past RedialSpread/backoff
	if n := h.cloud.dialCount(); n != 1 {
		t.Fatalf("dials after 4011 = %d, want 1 (no redial before a check-in)", n)
	}
	// ut-docs#2895: the status surfaces distinguish this from a tier
	// change or a busy pod.
	if h.c.State() != StateWaiting || h.c.Reason() != WaitReasonNotMainTill {
		t.Fatalf("state/reason after 4011 = %v/%v, want waiting/not-main-till", h.c.State(), h.c.Reason())
	}
	h.c.CheckedIn(false) // a FAILED check-in proves nothing
	time.Sleep(100 * time.Millisecond)
	if n := h.cloud.dialCount(); n != 1 {
		t.Fatalf("dials after a failed check-in = %d, want 1", n)
	}
	h.c.CheckedIn(true)
	h.cloud.nextConn(t)
}

// AC6: 4010 tier_changed parks the same way.
func TestTierChangedWaitsForACheckIn(t *testing.T) {
	h := newHarness(t, nil)
	c := h.cloud.nextConn(t)
	c.frame(t, "hello")
	c.closeWith(4010, "tier_changed")
	time.Sleep(200 * time.Millisecond)
	if n := h.cloud.dialCount(); n != 1 {
		t.Fatalf("dials after 4010 = %d, want 1", n)
	}
	if h.c.State() != StateWaiting || h.c.Reason() != WaitReasonTierChanged {
		t.Fatalf("state/reason after 4010 = %v/%v, want waiting/tier-changed", h.c.State(), h.c.Reason())
	}
	h.c.CheckedIn(true)
	h.cloud.nextConn(t)
}

// AC6: 4003 revoked stops dialling until the credential changes; a 401
// upgrade refusal is the same.
func TestRevokedStopsUntilTheCredentialChanges(t *testing.T) {
	h := newHarness(t, nil)
	c := h.cloud.nextConn(t)
	c.frame(t, "hello")
	c.closeWith(4003, "revoked")
	time.Sleep(150 * time.Millisecond)
	h.c.CheckedIn(true) // a check-in does not lift a revocation
	time.Sleep(100 * time.Millisecond)
	if n := h.cloud.dialCount(); n != 1 {
		t.Fatalf("dials after 4003 = %d, want 1", n)
	}
	if h.c.State() != StateRevoked {
		t.Fatalf("state = %v, want revoked", h.c.State())
	}
	h.cloud.queueRefusal(refusal{status: http.StatusUnauthorized})
	h.bearer.Store("cred-2")
	eventually(t, "a dial with the new credential", func() bool { return h.cloud.dialCount() == 2 })
	time.Sleep(150 * time.Millisecond)
	if n := h.cloud.dialCount(); n != 2 {
		t.Fatalf("dials after a 401 = %d, want 2 (a refused credential is not retried)", n)
	}
}

// AC6: a 403/409 upgrade refusal waits for the next check-in.
func TestRefusedUpgradeWaits(t *testing.T) {
	for _, st := range []int{http.StatusForbidden, http.StatusConflict} {
		t.Run(http.StatusText(st), func(t *testing.T) {
			cloud := newFakeCloud(t)
			cloud.queueRefusal(refusal{status: st})
			o := fastOptions()
			o.Target = func(context.Context) (Target, bool) {
				return Target{BaseURL: cloud.srv.URL + "/api", StoreID: "store-1", Bearer: "cred-1", DeviceID: "dev-1"}, true
			}
			c := New(o)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); c.Run(ctx) }()
			defer func() { cancel(); <-done }()
			eventually(t, "the first dial", func() bool { return cloud.dialCount() == 1 })
			time.Sleep(200 * time.Millisecond)
			if n := cloud.dialCount(); n != 1 {
				t.Fatalf("dials after %d = %d, want 1 until a check-in", st, n)
			}
			c.CheckedIn(true)
			cloud.nextConn(t)
		})
	}
}

// AC6: a 503's Retry-After holds the redial for that long.
func TestRetryAfterIsHonoured(t *testing.T) {
	cloud := newFakeCloud(t)
	cloud.queueRefusal(refusal{status: http.StatusServiceUnavailable, retryAfter: 1})
	o := fastOptions()
	o.Target = func(context.Context) (Target, bool) {
		return Target{BaseURL: cloud.srv.URL + "/api", StoreID: "store-1", Bearer: "cred-1", DeviceID: "dev-1"}, true
	}
	c := New(o)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	defer func() { cancel(); <-done }()
	eventually(t, "the first dial", func() bool { return cloud.dialCount() == 1 })
	start := time.Now()
	c.CheckedIn(true) // a check-in does not cut a Retry-After short
	cloud.nextConn(t)
	if el := time.Since(start); el < 900*time.Millisecond {
		t.Fatalf("redialled %v after a 503 Retry-After: 1, want ≥ 1 s", el)
	}
}

// AC6: any other abnormal close (no bye, bye{deploying}) redials after the
// spread, and the link comes back.
func TestAbnormalCloseRedials(t *testing.T) {
	h := newHarness(t, nil)
	c := h.cloud.nextConn(t)
	c.frame(t, "hello")
	c.send("bye", map[string]any{"reason": "deploying"})
	c2 := h.cloud.nextConn(t)
	c2.frame(t, "hello")
	_ = c2.ws.CloseNow() // a reset, no close frame
	h.cloud.nextConn(t)
}

// ut-docs#2895: NextAttempt backs the "reconnecting" status surfaces' next-
// attempt hint while a timed redial is pending, and clears once the link
// is back up (or dialling right now, with nothing scheduled to report).
func TestNextAttemptDuringBackoff(t *testing.T) {
	h := newHarness(t, nil)
	c := h.cloud.nextConn(t)
	c.frame(t, "hello")
	eventually(t, "linked", func() bool { return h.c.State() == StateLinked })
	if at := h.c.NextAttempt(); !at.IsZero() {
		t.Fatalf("NextAttempt while linked = %v, want zero", at)
	}
	before := time.Now()
	c.send("bye", map[string]any{"reason": "deploying"})
	eventually(t, "a next-attempt time after an abnormal close", func() bool { return !h.c.NextAttempt().IsZero() })
	if h.c.State() != StateConnecting {
		t.Fatalf("state while a redial is pending = %v, want connecting", h.c.State())
	}
	if at := h.c.NextAttempt(); at.Before(before) {
		t.Fatalf("NextAttempt = %v, want at or after the close (%v)", at, before)
	}
	h.cloud.nextConn(t)
	eventually(t, "linked again", func() bool { return h.c.State() == StateLinked })
	if at := h.c.NextAttempt(); !at.IsZero() {
		t.Fatalf("NextAttempt once linked again = %v, want zero", at)
	}
}

// AC6: the first redial after an abnormal close waits U(0, spread), and
// failed dials back off with full jitter from BackoffMin to BackoffMax.
func TestRedialDelays(t *testing.T) {
	o := fastOptions()
	o.RedialSpread = 60 * time.Second
	o.BackoffMin, o.BackoffMax = time.Second, 5*time.Minute
	c := New(o)
	c.rng = func() float64 { return 0.999999 }
	if d := c.spread(); d < 59*time.Second || d > 60*time.Second {
		t.Fatalf("spread at r≈1 = %v, want ≈ 60 s", d)
	}
	c.rng = func() float64 { return 0 }
	if d := c.spread(); d != 0 {
		t.Fatalf("spread at r=0 = %v, want 0", d)
	}
	c.rng = func() float64 { return 0.999999 }
	for n, want := range map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 4: 8 * time.Second, 20: 5 * time.Minute} {
		if d := c.backoff(n); d > want || d < want*99/100 {
			t.Fatalf("backoff(%d) at r≈1 = %v, want ≈ %v", n, d, want)
		}
	}
}

// AC1: the gate flipping closed (tier → periodic, or this till became a
// replica) closes the link with bye{shutdown}.
func TestGateFlipClosesWithBye(t *testing.T) {
	h := newHarness(t, nil)
	c := h.cloud.nextConn(t)
	c.frame(t, "hello")
	h.gateOK.Store(false)
	h.c.CheckedIn(true)
	bye := c.frame(t, "bye")
	if !strings.Contains(string(bye.Payload), `"shutdown"`) {
		t.Fatalf("bye = %s, want reason shutdown", bye.Payload)
	}
	eventually(t, "idle state", func() bool { return h.c.State() == StateIdle })
}

// AC2: plain ws only to a loopback cloud; https → wss.
func TestLinkURL(t *testing.T) {
	for _, tc := range []struct{ base, want string }{
		{"http://127.0.0.1:8081/api", "ws://127.0.0.1:8081/api/v1/tills/link?store_id=s+1"},
		{"http://localhost:8081/api/", "ws://localhost:8081/api/v1/tills/link?store_id=s+1"},
		{"http://[::1]:8081/api", "ws://[::1]:8081/api/v1/tills/link?store_id=s+1"},
		{"https://cloud.universaltill.com/api", "wss://cloud.universaltill.com/api/v1/tills/link?store_id=s+1"},
		{"http://cloud.universaltill.com/api", ""},
		{"http://192.168.1.10:8081/api", ""},
		{"ftp://127.0.0.1/api", ""},
		{"not a url", ""},
	} {
		got, err := linkURL(tc.base, "s 1")
		if tc.want == "" {
			if err == nil {
				t.Fatalf("linkURL(%q) = %q, want an error", tc.base, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("linkURL(%q) = %q, %v; want %q", tc.base, got, err, tc.want)
		}
	}
}

// AC8: sale frames only while live_view is on; dropped (never queued) when
// off or down; a summary only; rate-limited to the cloud's bucket.
func TestSaleFramesFollowLiveView(t *testing.T) {
	h := newHarness(t, nil)
	sale := Sale{ID: "sale-1", TillID: "till-1", Time: time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC), TotalMinor: 1234, Currency: "EUR", TenderKind: "card", ItemCount: 3}

	h.c.Sale(sale) // not linked yet: dropped, never queued
	c := h.cloud.nextConn(t)
	c.frame(t, "hello")
	eventually(t, "linked", func() bool { return h.c.State() == StateLinked })
	h.c.Sale(sale) // live_view off (cloud hello said false)
	c.noFrame(t, "sale", 100*time.Millisecond)

	c.send("live_view", map[string]any{"on": true})
	eventually(t, "live view on", func() bool { return h.c.liveView.Load() })
	h.c.Sale(sale)
	got := c.frame(t, "sale")
	var p map[string]any
	_ = json.Unmarshal(got.Payload, &p)
	if p["id"] != "sale-1" || p["till_id"] != "till-1" || p["time"] != "2026-09-26T10:00:00Z" ||
		p["total_minor"] != 1234.0 || p["currency"] != "EUR" || p["tender_kind"] != "card" ||
		p["item_count"] != 3.0 || p["refund"] != false || p["void"] != false || len(p) != 9 {
		t.Fatalf("sale payload = %s, want exactly the §4 summary", got.Payload)
	}

	c.send("live_view", map[string]any{"on": false})
	eventually(t, "live view off", func() bool { return !h.c.liveView.Load() })
	h.c.Sale(sale)
	c.noFrame(t, "sale", 100*time.Millisecond)
}

func TestSaleRateStaysInsideTheCloudBucket(t *testing.T) {
	h := newHarness(t, nil)
	h.cloud.liveView.Store(true)
	c := h.cloud.nextConn(t)
	c.frame(t, "hello")
	eventually(t, "live view on", func() bool { return h.c.liveView.Load() && h.c.State() == StateLinked })
	for i := 0; i < 100; i++ {
		h.c.Sale(Sale{ID: "s", Time: time.Now(), Currency: "GBP"})
	}
	n := 0
	deadline := time.After(300 * time.Millisecond)
loop:
	for {
		select {
		case env := <-c.frames:
			if env.Type == "sale" {
				n++
			}
		case <-deadline:
			break loop
		}
	}
	if n < 20 || n > 22 { // burst 20, + ≤ 5/s refill over the window
		t.Fatalf("sale frames from a burst of 100 = %d, want the 20-frame burst (+ refill)", n)
	}
}

// AC8: status on connect, then only on change, at most every StatusEvery.
func TestStatusOnConnectAndOnChange(t *testing.T) {
	h := newHarness(t, nil)
	c := h.cloud.nextConn(t)
	c.frame(t, "hello")
	first := c.frame(t, "status")
	if !strings.Contains(string(first.Payload), `"version":"v1.2.3"`) {
		t.Fatalf("status = %s, want the till's version", first.Payload)
	}
	c.noFrame(t, "status", 120*time.Millisecond) // unchanged: not resent
	h.status.Store(Status{Version: "v1.2.3", UpdateState: "idle", Peers: []PeerStatus{{TillID: "t2", Link: "up", Version: "v1.2.3", UpdateState: "idle"}}})
	next := c.frame(t, "status")
	if !strings.Contains(string(next.Payload), `"till_id":"t2"`) {
		t.Fatalf("status = %s, want the new peer", next.Payload)
	}
}

// ut-docs#2897: a peer's cloud device id rides the status frame's wire
// shape as device_id, alongside till_id — and is left off the frame
// entirely (omitempty) for a peer with none, same as an older replica or a
// down till the tills table has no device id for.
func TestStatusFrameCarriesPeerDeviceIDAndOmitsItWhenEmpty(t *testing.T) {
	h := newHarness(t, nil)
	c := h.cloud.nextConn(t)
	c.frame(t, "hello")
	c.frame(t, "status") // the connect-time status; not what this test checks
	h.status.Store(Status{Version: "v1.2.3", Peers: []PeerStatus{
		{TillID: "t2", DeviceID: "till-cloud-2", Link: "up"},
		{TillID: "t3", Link: "down"},
	}})
	next := c.frame(t, "status")
	if !strings.Contains(string(next.Payload), `"till_id":"t2","device_id":"till-cloud-2"`) {
		t.Fatalf("status = %s, want t2's device id on the wire", next.Payload)
	}
	if strings.Contains(string(next.Payload), `"till_id":"t3","device_id"`) {
		t.Fatalf("status = %s, want t3's empty device id omitted from the wire", next.Payload)
	}
}

// AC9: Sale never blocks, even with the link down or its queue full.
func TestSaleNeverBlocks(t *testing.T) {
	c := New(fastOptions())
	c.liveView.Store(true)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 10000; i++ {
			c.Sale(Sale{ID: "x", Time: time.Now(), Currency: "EUR"})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Sale blocked with no link")
	}
}

// ADR-0117 §7 (review finding 1): a pod that admits and then cuts with
// 1013 try_again (full) or 4029 rate_limited must not be redialled on a
// timer — the till waits for the next successful check-in instead.
func TestTryAgainAndRateLimitedWaitForACheckIn(t *testing.T) {
	for _, code := range []int{1013, 4029} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			h := newHarness(t, nil)
			c := h.cloud.nextConn(t)
			c.frame(t, "hello")
			c.closeWith(code, "go away")
			time.Sleep(200 * time.Millisecond) // far past RedialSpread/backoff
			if n := h.cloud.dialCount(); n != 1 {
				t.Fatalf("dials after %d = %d, want 1 until a check-in", code, n)
			}
			if h.c.State() != StateWaiting {
				t.Fatalf("state after %d = %v, want waiting", code, h.c.State())
			}
			if h.c.Reason() != WaitReasonBusy {
				t.Fatalf("reason after %d = %v, want busy", code, h.c.Reason())
			}
			h.c.CheckedIn(true)
			h.cloud.nextConn(t)
		})
	}
}

// Review finding 1: an abnormal close shortly after the link came up (a
// pod that admits then cuts, 4001 replaced in a loop) grows the backoff;
// only a link that stayed up for StableLink resets it to the §7 spread.
func TestShortLivedLinksGrowTheRedialBackoff(t *testing.T) {
	o := fastOptions()
	o.RedialSpread = 60 * time.Second
	o.BackoffMin, o.BackoffMax = time.Second, 5*time.Minute
	o.StableLink = 30 * time.Second
	c := New(o)
	c.rng = func() float64 { return 0.999999 }

	d, next := c.abnormalRedial(5*time.Second, 0)
	if next != 1 || d < 60*time.Second || d > 61*time.Second {
		t.Fatalf("first short-lived close: delay %v attempt %d, want ≈ spread + 1 s, attempt 1", d, next)
	}
	d, next = c.abnormalRedial(5*time.Second, 3)
	if next != 4 || d < 67*time.Second || d > 68*time.Second {
		t.Fatalf("4th short-lived close: delay %v attempt %d, want ≈ spread + 8 s, attempt 4", d, next)
	}
	d, next = c.abnormalRedial(5*time.Second, 40)
	if d > 6*time.Minute {
		t.Fatalf("delay %v past spread + BackoffMax", d)
	}
	d, next = c.abnormalRedial(10*time.Minute, 7)
	if next != 0 || d > 60*time.Second {
		t.Fatalf("close after a stable link: delay %v attempt %d, want the spread alone and attempt 0", d, next)
	}
}

// Review finding 1, end to end: a cloud that cuts every link with 4001
// right after its hello is redialled with a growing attempt count.
func TestReplacedLoopKeepsTheAttemptCount(t *testing.T) {
	h := newHarness(t, func(o *Options) { o.StableLink = time.Hour })
	for i := 0; i < 3; i++ {
		c := h.cloud.nextConn(t)
		c.frame(t, "hello")
		c.closeWith(4001, "replaced")
	}
	h.cloud.nextConn(t)
	if a := h.c.lastAttempt.Load(); a < 3 {
		t.Fatalf("attempt after three short-lived links = %d, want ≥ 3", a)
	}
}

// ut-docs#2827 (ADR-0117 §3): the newest link_version a hello or nudge
// carried is readable by the check-in, and is recorded BEFORE the kick —
// so the check-in the kick runs sends it (a stale one would get a 304 and
// skip the POST that picks the new directive up).
func TestLinkVersionIsRecordedBeforeTheKick(t *testing.T) {
	var atKick []int64
	var mu sync.Mutex
	var h *harness
	h = newHarness(t, func(o *Options) {
		o.Kick = func() {
			mu.Lock()
			atKick = append(atKick, h.c.LinkVersion())
			mu.Unlock()
			h.kicks.Add(1)
		}
	})
	if v := h.c.LinkVersion(); v != 0 {
		t.Fatalf("LinkVersion before any hello = %d, want 0", v)
	}
	h.cloud.linkVersion.Store(5)
	c := h.cloud.nextConn(t)
	eventually(t, "kick on hello", func() bool { return h.kicks.Load() == 1 })
	c.send("nudge", map[string]any{"link_version": 9, "scopes": []string{"directives"}})
	eventually(t, "kick on nudge", func() bool { return h.kicks.Load() == 2 })
	mu.Lock()
	defer mu.Unlock()
	if len(atKick) != 2 || atKick[0] != 5 || atKick[1] != 9 {
		t.Fatalf("LinkVersion seen at each kick = %v, want [5 9]", atKick)
	}
	if v := h.c.LinkVersion(); v != 9 {
		t.Fatalf("LinkVersion = %d, want 9", v)
	}
	var nilClient *Client
	if nilClient.LinkVersion() != 0 {
		t.Fatal("nil Client LinkVersion must be 0")
	}
}

// ut-docs#2895: a 403 upgrade refusal carries ut-cloud's JSON error code
// (stores_link.go: not_main_till / tier_periodic); the status surfaces
// show the real reason, not "cloud busy". A 403 with no parseable code
// stays busy.
func TestRefusedUpgradeReasonFromBody(t *testing.T) {
	for _, tc := range []struct {
		code string
		want WaitReason
	}{
		{"not_main_till", WaitReasonNotMainTill},
		{"tier_periodic", WaitReasonTierChanged},
		{"", WaitReasonBusy},
		{"device_credential_required", WaitReasonBusy},
	} {
		t.Run(tc.code, func(t *testing.T) {
			cloud := newFakeCloud(t)
			cloud.queueRefusal(refusal{status: http.StatusForbidden, code: tc.code})
			o := fastOptions()
			o.Target = func(context.Context) (Target, bool) {
				return Target{BaseURL: cloud.srv.URL + "/api", StoreID: "store-1", Bearer: "cred-1", DeviceID: "dev-1"}, true
			}
			c := New(o)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { defer close(done); c.Run(ctx) }()
			defer func() { cancel(); <-done }()
			eventually(t, "waiting after the 403", func() bool { return c.State() == StateWaiting })
			if got := c.Reason(); got != tc.want {
				t.Fatalf("reason after 403 %q = %v, want %v", tc.code, got, tc.want)
			}
		})
	}
}

// ut-docs#2895: a 503 with Retry-After redials on a timer, not after a
// check-in — its own reason, with the time it retries at; and when that
// redial fails in transport, the row shows Reconnecting, not busy.
func TestRetryAfterReasonThenFailedDialReconnects(t *testing.T) {
	cloud := newFakeCloud(t)
	cloud.queueRefusal(refusal{status: http.StatusServiceUnavailable, retryAfter: 1})
	for range 200 { // every redial after it fails in transport
		cloud.queueRefusal(refusal{drop: true})
	}
	o := fastOptions()
	o.BackoffMin, o.BackoffMax = 200*time.Millisecond, 200*time.Millisecond
	o.Target = func(context.Context) (Target, bool) {
		return Target{BaseURL: cloud.srv.URL + "/api", StoreID: "store-1", Bearer: "cred-1", DeviceID: "dev-1"}, true
	}
	c := New(o)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); c.Run(ctx) }()
	defer func() { cancel(); <-done }()
	eventually(t, "the first dial", func() bool { return cloud.dialCount() == 1 })
	eventually(t, "a Retry-After next attempt", func() bool { return !c.NextAttempt().IsZero() })
	if c.State() != StateWaiting || c.Reason() != WaitReasonRetryAfter {
		t.Fatalf("state/reason after 503 Retry-After = %v/%v, want waiting/retry-after", c.State(), c.Reason())
	}
	eventually(t, "the dropped redial", func() bool { return cloud.dialCount() >= 2 })
	eventually(t, "reconnecting after the failed redial", func() bool {
		return c.State() == StateConnecting && !c.NextAttempt().IsZero()
	})
	if c.Reason() != WaitReasonNone {
		t.Fatalf("reason while reconnecting = %v, want none", c.Reason())
	}
}

// ut-docs#2895: once the redial timer fires the next-attempt time is in
// the past; it must clear rather than show a stale time while dialling.
func TestNextAttemptClearsWhenTheTimerFires(t *testing.T) {
	var block atomic.Bool
	gate := make(chan struct{})
	h := newHarness(t, func(o *Options) {
		o.RedialSpread = 50 * time.Millisecond
		inner := o.Target
		o.Target = func(ctx context.Context) (Target, bool) {
			if block.Load() {
				select {
				case <-gate:
				case <-ctx.Done():
				}
			}
			return inner(ctx)
		}
	})
	c := h.cloud.nextConn(t)
	c.frame(t, "hello")
	eventually(t, "linked", func() bool { return h.c.State() == StateLinked })
	block.Store(true)
	c.send("bye", map[string]any{"reason": "deploying"})
	eventually(t, "a next-attempt time", func() bool { return !h.c.NextAttempt().IsZero() })
	// The timer fires within the spread; Run then blocks in Target, i.e.
	// about to dial.
	eventually(t, "next attempt cleared once the timer fired", func() bool { return h.c.NextAttempt().IsZero() })
	if h.c.State() != StateConnecting {
		t.Fatalf("state while about to redial = %v, want connecting", h.c.State())
	}
	block.Store(false)
	close(gate)
	h.cloud.nextConn(t)
}
