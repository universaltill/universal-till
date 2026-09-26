package fleetlink

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// ---- envelope --------------------------------------------------------------

func TestEnvelope_RoundTripStampsVersion(t *testing.T) {
	e := mustMsg(t, "m-1", TypeSync, "", SyncPayload{Scopes: []Scope{ScopeAdmin}})
	e.V = 0 // Encode must stamp it regardless
	b, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"v":1`) {
		t.Fatalf("encoded frame lacks v:1: %s", b)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "m-1" || got.Type != TypeSync || got.ReplyTo != "" {
		t.Fatalf("round trip lost fields: %+v", got)
	}
	if p := decodeInto[SyncPayload](t, got.Payload); len(p.Scopes) != 1 || p.Scopes[0] != ScopeAdmin {
		t.Fatalf("payload = %+v", p)
	}
}

func TestEnvelope_DecodeRejectsBadFrames(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":   `nope`,
		"no id":      `{"v":1,"type":"ping"}`,
		"no type":    `{"v":1,"id":"x"}`,
		"array":      `[1,2]`,
		"wrong type": `{"v":1,"id":1,"type":"ping"}`,
	} {
		if _, err := Decode([]byte(raw)); !errors.Is(err, errMalformed) {
			t.Errorf("%s: err = %v, want errMalformed", name, err)
		}
	}
	e, err := Decode([]byte(`{"v":2,"id":"x","type":"ping"}`))
	if !errors.Is(err, errVersion) || e.ID != "x" {
		t.Fatalf("v:2 → (%+v, %v), want errVersion with the id kept for the reply", e, err)
	}
}

func TestScopes_MaskOrderIsStable(t *testing.T) {
	m := scopeBit(ScopeOrders) | scopeBit(ScopeAdmin) | scopeBit(ScopeStock)
	got := scopesOf(m)
	want := []Scope{ScopeAdmin, ScopeStock, ScopeOrders}
	if len(got) != len(want) {
		t.Fatalf("scopes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("scopes = %v, want %v", got, want)
		}
	}
	if scopeBit("bogus") != 0 {
		t.Fatal("an unknown scope must own no bit")
	}
}

func TestReport_ClipsFields(t *testing.T) {
	long := strings.Repeat("x", 500)
	r, ok := decodeReport(json.RawMessage(`{"version":"` + long + `","update_state":"idle","push_queue_depth":-4,"tls_pinned":true}`))
	if !ok || len(r.Version) != maxReportField || r.PushQueueDepth != 0 || !r.TLSPinned {
		t.Fatalf("report = %+v ok=%v", r, ok)
	}
}

// ut-docs#2897: a replica's hello carries its own cloud device id
// (enroll.CurrentStatus, #2730) alongside the LAN pairing till_id. It is
// untrusted LAN input, display-only on my. (never used for auth), so it is
// bounded and charset-checked exactly like every other hello field before
// being kept for the link's lifetime.
func TestHello_ClipsAndValidatesCloudDeviceID(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"a normal cloud device id is kept", "till-7547e9b4-1234-4d3a-9a0b-abc123456789", "till-7547e9b4-1234-4d3a-9a0b-abc123456789"},
		{"empty (an older replica without the field) stays empty", "", ""},
		{"oversized is clipped to the same bound as every other hello field", strings.Repeat("a", 500), strings.Repeat("a", maxReportField)},
		{"garbage charset is dropped, not just truncated", "till-\x00\x01<script>oops", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := Hello{TillID: "t1", CloudDeviceID: tc.in}
			h.clipStrings()
			if h.CloudDeviceID != tc.want {
				t.Fatalf("cloud_device_id = %q, want %q", h.CloudDeviceID, tc.want)
			}
			if h.TillID != "t1" {
				t.Fatalf("till_id = %q, want it untouched", h.TillID)
			}
		})
	}
}

// An older main till (built before #2897) decodes a newer replica's hello
// fine: encoding/json silently ignores a field its own struct doesn't
// declare. legacyHello stands in for that older struct shape.
func TestHello_DecodeToleratesAnUnknownCloudDeviceIDField(t *testing.T) {
	type legacyHello struct {
		TillID string `json:"till_id"`
		Role   string `json:"role"`
	}
	raw := []byte(`{"till_id":"t1","role":"replica","cloud_device_id":"till-abc"}`)
	var h legacyHello
	if err := json.Unmarshal(raw, &h); err != nil {
		t.Fatalf("an older hello struct must tolerate the new field: %v", err)
	}
	if h.TillID != "t1" || h.Role != "replica" {
		t.Fatalf("legacy hello = %+v", h)
	}
}

// An older replica (built before #2897) never sends cloud_device_id: the
// main till's Hello decodes with till_id present and CloudDeviceID empty.
func TestHello_DecodeFromAnOlderReplicaLeavesCloudDeviceIDEmpty(t *testing.T) {
	var h Hello
	if err := json.Unmarshal([]byte(`{"till_id":"t1","role":"replica"}`), &h); err != nil {
		t.Fatal(err)
	}
	if h.TillID != "t1" || h.CloudDeviceID != "" {
		t.Fatalf("hello = %+v, want till_id kept and cloud_device_id empty", h)
	}
}

// ---- peer over a fake conn --------------------------------------------------

// servePeer runs one fake-conn link on hub and returns the conn plus a
// channel closed when ServeConn returns.
func servePeer(t *testing.T, hub *Hub, tillID string) (*fakeConn, <-chan struct{}) {
	t.Helper()
	fc := newFakeConn()
	done := make(chan struct{})
	go func() {
		defer close(done)
		hub.ServeConn(tillID, fc)
	}()
	t.Cleanup(func() {
		fc.Close(CloseNormal, "")
		<-done
	})
	return fc, done
}

func TestPeer_HelloIsTheFirstFrame(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	fc, _ := servePeer(t, hub, "till-2")
	e := fc.next(t)
	if e.Type != TypeHello {
		t.Fatalf("first frame = %s, want hello", e.Type)
	}
	h := decodeInto[Hello](t, e.Payload)
	if h.Role != "main" || h.PeerTillID != "till-2" || h.SyncProtocol != SyncProtocolLevel || h.Cursors.Stock != "s1" {
		t.Fatalf("hello = %+v", h)
	}
	raw := string(e.Payload)
	for _, k := range []string{`"update_policy":null`, `"fleet_target":null`, `"tls_pin":null`, `"stock_version":"s1"`} {
		if !strings.Contains(raw, k) {
			t.Errorf("hello payload %s lacks %s", raw, k)
		}
	}
}

func TestPeer_StoresPeerHelloAndLatestReport(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	fc, _ := servePeer(t, hub, "till-2")
	fc.next(t)
	fc.send(t, mustMsg(t, "r-1", TypeHello, "", Hello{TillID: "till-2", Role: "replica", Version: "v1",
		Cursors: Cursors{Admin: strings.Repeat("c", 10000)}}))
	fc.send(t, mustMsg(t, "r-2", TypeReport, "", Report{Version: "v1", UpdateState: "idle"}))
	fc.send(t, mustMsg(t, "r-3", TypeReport, "", Report{Version: "v2", UpdateState: "downloading", PushQueueDepth: 3}))
	waitFor(t, "latest report", func() bool {
		r, ok := hub.Report("till-2")
		return ok && r.Version == "v2" && r.PushQueueDepth == 3 && !r.ReceivedAt.IsZero()
	})
	p := hub.Peer("till-2")
	if p == nil {
		t.Fatal("peer not registered")
	}
	if h, ok := p.Hello(); !ok || h.Role != "replica" || len(h.Cursors.Admin) != maxReportField {
		t.Fatalf("peer hello = %+v, %v (strings must be clipped before being kept)", h, ok)
	}
}

func TestHub_ReportStoreIsBounded(t *testing.T) {
	cfg := fastConfig()
	cfg.MaxReports = 2
	hub := NewHub(HubOptions{Config: cfg, Hello: testHello})
	defer hub.Close()
	hub.storeReport("a", Report{Version: "1"})
	time.Sleep(time.Millisecond)
	hub.storeReport("b", Report{Version: "1"})
	time.Sleep(time.Millisecond)
	hub.storeReport("c", Report{Version: "1"})
	if _, ok := hub.Report("a"); ok {
		t.Fatal("oldest report should have been evicted at the cap")
	}
	if _, ok := hub.Report("c"); !ok {
		t.Fatal("newest report missing")
	}
	hub.storeReport("b", Report{Version: "2"}) // an update never evicts
	if r, _ := hub.Report("b"); r.Version != "2" {
		t.Fatalf("b = %+v", r)
	}
}

func TestPeer_SyncNudgesCoalesceWhileWriterIsBlocked(t *testing.T) {
	hub := NewHub(HubOptions{Config: DefaultConfig(), Hello: testHello}) // real 10 s write timeout
	defer hub.Close()
	fc := newFakeConn()
	fc.gate = make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); hub.ServeConn("till-2", fc) }()
	defer func() { fc.Close(CloseNormal, ""); <-done }()

	waitFor(t, "peer registered", func() bool { return hub.Peer("till-2") != nil })
	// The writer is now stuck on hello (gate closed). Hammer the nudges.
	for i := 0; i < 500; i++ {
		hub.Nudge(ScopeAdmin)
		hub.Nudge(ScopeStock, ScopeAdmin)
	}
	hub.Nudge(ScopeOrders)
	close(fc.gate) // let everything through

	if e := fc.next(t); e.Type != TypeHello {
		t.Fatalf("first = %s", e.Type)
	}
	e := fc.next(t)
	if e.Type != TypeSync {
		t.Fatalf("second = %s, want one coalesced sync", e.Type)
	}
	p := decodeInto[SyncPayload](t, e.Payload)
	if got := scopesString(p.Scopes); got != "admin,stock,orders" {
		t.Fatalf("scopes = %s, want admin,stock,orders", got)
	}
	select {
	case b := <-fc.out:
		if e, _ := Decode(b); e.Type == TypeSync {
			t.Fatalf("a second sync frame followed: %s", b)
		}
	case <-time.After(50 * time.Millisecond):
	}
}

func scopesString(s []Scope) string {
	parts := make([]string, len(s))
	for i, v := range s {
		parts[i] = string(v)
	}
	return strings.Join(parts, ",")
}

func TestPeer_FullQueueFailsNewRequestWithBusy(t *testing.T) {
	cfg := DefaultConfig()
	cfg.QueueMessages = 4
	hub := NewHub(HubOptions{Config: cfg, Hello: testHello})
	defer hub.Close()
	fc := newFakeConn()
	fc.gate = make(chan struct{}) // peer never takes a write
	done := make(chan struct{})
	go func() { defer close(done); hub.ServeConn("till-2", fc) }()
	defer func() { fc.Close(CloseNormal, ""); <-done }()
	waitFor(t, "peer", func() bool { return hub.Peer("till-2") != nil })
	p := hub.Peer("till-2")

	// Fill the queue with notifications (pong-shaped) so requests hit the
	// queue limit, not the in-flight one.
	for i := 0; i < cfg.QueueMessages; i++ {
		if err := p.enqueue(mustMsg(t, "n", TypePong, "", nil)); err != nil {
			t.Fatalf("enqueue %d: %v", i, err)
		}
	}
	start := time.Now()
	_, err := p.Request(context.Background(), "sat.status", nil)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v, want ErrBusy", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("busy must be immediate, not after a wait")
	}
}

func TestPeer_QueueByteLimitAndOversizeFrame(t *testing.T) {
	cfg := DefaultConfig()
	cfg.QueueBytes = 1024
	cfg.ReadLimit = 600
	hub := NewHub(HubOptions{Config: cfg, Hello: testHello})
	defer hub.Close()
	fc := newFakeConn()
	fc.gate = make(chan struct{})
	done := make(chan struct{})
	go func() { defer close(done); hub.ServeConn("till-2", fc) }()
	defer func() { fc.Close(CloseNormal, ""); <-done }()
	waitFor(t, "peer", func() bool { return hub.Peer("till-2") != nil })
	p := hub.Peer("till-2")

	if err := p.enqueue(mustMsg(t, "x", TypePong, "", map[string]string{"pad": strings.Repeat("a", 700)})); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("frame over the peer's read limit: err = %v, want ErrTooLarge", err)
	}
	pad := map[string]string{"pad": strings.Repeat("a", 400)}
	if err := p.enqueue(mustMsg(t, "x", TypePong, "", pad)); err != nil {
		t.Fatal(err)
	}
	if err := p.enqueue(mustMsg(t, "y", TypePong, "", pad)); err != nil {
		t.Fatal(err)
	}
	if err := p.enqueue(mustMsg(t, "z", TypePong, "", pad)); !errors.Is(err, ErrBusy) {
		t.Fatalf("third ~430-byte frame over a 1 KiB queue: err = %v, want ErrBusy", err)
	}
}

func TestPeer_SlowPeerIsDisconnectedAfterWriteTimeout(t *testing.T) {
	cfg := fastConfig() // PeerTimeout 2 s > WriteTimeout: isolates the write timeout
	hub := NewHub(HubOptions{Config: cfg, Hello: testHello})
	defer hub.Close()
	fc := newFakeConn()
	fc.gate = make(chan struct{}) // never opens
	done := make(chan struct{})
	start := time.Now()
	go func() { defer close(done); hub.ServeConn("till-2", fc) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a peer that takes no write was never disconnected")
	}
	if el := time.Since(start); el < cfg.WriteTimeout {
		t.Fatalf("disconnected after %v, before the %v write timeout", el, cfg.WriteTimeout)
	}
	if code, ok := fc.closeCode(); !ok || code != CloseSlow {
		t.Fatalf("close code = %v (closed=%v), want CloseSlow", code, ok)
	}
	if hub.Len() != 0 {
		t.Fatal("slow peer still registered")
	}
}

func TestPeer_UnknownRequestTypeGetsErrorReply(t *testing.T) {
	hub := NewHub(HubOptions{Config: fastConfig(), Hello: testHello})
	defer hub.Close()
	fc, _ := servePeer(t, hub, "till-2")
	fc.next(t)
	fc.send(t, mustMsg(t, "q-1", "no.such.thing", "", nil))
	e := fc.next(t)
	if e.Type != TypeError || e.ReplyTo != "q-1" {
		t.Fatalf("reply = %+v", e)
	}
	if p := decodeInto[ErrorPayload](t, e.Payload); p.Code != CodeUnknownType || p.Retryable {
		t.Fatalf("error payload = %+v", p)
	}
	fc.in <- []byte(`{"v":7,"id":"q-2","type":"hello"}`) // raw: Encode would stamp v:1
	e = fc.next(t)
	if e.Type != TypeError || e.ReplyTo != "q-2" || decodeInto[ErrorPayload](t, e.Payload).Code != CodeUnsupportedVersion {
		t.Fatalf("v:7 reply = %+v", e)
	}
}

// ---- over a real websocket --------------------------------------------------

func TestLink_AuthRejectedBeforeUpgrade(t *testing.T) {
	h := newWSHarness(t, fastConfig())
	for _, bearer := range []string{"", "bad"} {
		_, resp, err := h.dial(t, bearer)
		if err == nil {
			t.Fatalf("bearer %q: dial succeeded", bearer)
		}
		if resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("bearer %q: resp = %v, want 401", bearer, resp)
		}
	}
	if h.hub.Len() != 0 {
		t.Fatal("a rejected dial registered a peer")
	}
}

func TestLink_RequestReplyBothWaysAndErrorPayload(t *testing.T) {
	h := newWSHarness(t, fastConfig())
	h.hub.Handle("echo", func(_ context.Context, tillID string, payload json.RawMessage) (any, error) {
		return map[string]string{"till": tillID, "got": string(payload)}, nil
	})
	h.hub.Handle("fail", func(context.Context, string, json.RawMessage) (any, error) {
		return nil, &RemoteError{Code: "nope", Retryable: true}
	})
	c, _, err := h.dial(t, "good-till-2")
	if err != nil {
		t.Fatal(err)
	}
	if e := readEnv(t, c); e.Type != TypeHello {
		t.Fatalf("first frame %s", e.Type)
	}

	// replica → main
	writeEnv(t, c, mustMsg(t, "c-1", "echo", "", map[string]int{"n": 1}))
	e := readEnv(t, c)
	if e.Type != "echo" || e.ReplyTo != "c-1" || !strings.Contains(string(e.Payload), `"till":"till-2"`) {
		t.Fatalf("echo reply = %+v %s", e, e.Payload)
	}
	writeEnv(t, c, mustMsg(t, "c-2", "fail", "", nil))
	e = readEnv(t, c)
	if p := decodeInto[ErrorPayload](t, e.Payload); e.Type != TypeError || e.ReplyTo != "c-2" || p.Code != "nope" || !p.Retryable {
		t.Fatalf("fail reply = %+v %s", e, e.Payload)
	}

	// main → replica, answered and error-answered
	waitFor(t, "peer", func() bool { return h.hub.Peer("till-2") != nil })
	p := h.hub.Peer("till-2")
	type res struct {
		raw json.RawMessage
		err error
	}
	got := make(chan res, 2)
	go func() {
		r, err := p.Request(context.Background(), "sat.status", map[string]int{"q": 1})
		got <- res{r, err}
	}()
	req := readEnv(t, c)
	if req.Type != "sat.status" || req.ID == "" {
		t.Fatalf("request = %+v", req)
	}
	writeEnv(t, c, mustMsg(t, "c-3", "sat.status", req.ID, map[string]string{"printer": "ok"}))
	r := <-got
	if r.err != nil || !strings.Contains(string(r.raw), `"printer":"ok"`) {
		t.Fatalf("reply = %s, %v", r.raw, r.err)
	}

	go func() { r, err := p.Request(context.Background(), "sat.print", nil); got <- res{r, err} }()
	req = readEnv(t, c)
	writeEnv(t, c, mustMsg(t, "c-4", TypeError, req.ID, ErrorPayload{Code: "paper_out", Retryable: true}))
	r = <-got
	var re *RemoteError
	if !errors.As(r.err, &re) || re.Code != "paper_out" || !re.Retryable {
		t.Fatalf("err = %v, want RemoteError paper_out retryable", r.err)
	}
}

func TestLink_UnansweredRequestTimesOutWithoutBlockingTheOtherDirection(t *testing.T) {
	h := newWSHarness(t, fastConfig())
	h.hub.Handle("echo", func(_ context.Context, _ string, payload json.RawMessage) (any, error) {
		return json.RawMessage(payload), nil
	})
	c, _, err := h.dial(t, "good-till-2")
	if err != nil {
		t.Fatal(err)
	}
	readEnv(t, c) // hello
	waitFor(t, "peer", func() bool { return h.hub.Peer("till-2") != nil })
	p := h.hub.Peer("till-2")

	errc := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		_, err := p.Request(ctx, "sat.status", nil)
		errc <- err
	}()
	if req := readEnv(t, c); req.Type != "sat.status" {
		t.Fatalf("request = %+v", req)
	}
	// Never answer it. Meanwhile the other direction still works at once,
	// and heartbeats keep the link alive (we answer nothing but echo).
	writeEnv(t, c, mustMsg(t, "c-1", "echo", "", map[string]int{"n": 7}))
	e := readEnv(t, c)
	if e.ReplyTo != "c-1" {
		t.Fatalf("echo reply = %+v", e)
	}
	select {
	case err := <-errc:
		t.Fatalf("request returned before its deadline: %v", err)
	default:
	}
	select {
	case err := <-errc:
		if !errors.Is(err, ErrTimeout) {
			t.Fatalf("err = %v, want ErrTimeout", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request never failed at its deadline")
	}
}

func TestLink_InFlightLimitsEachWay(t *testing.T) {
	h := newWSHarness(t, fastConfig())
	release := make(chan struct{})
	var running atomic.Int32
	h.hub.Handle("slow", func(ctx context.Context, _ string, _ json.RawMessage) (any, error) {
		running.Add(1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return "ok", nil
	})
	c, _, err := h.dial(t, "good-till-2")
	if err != nil {
		t.Fatal(err)
	}
	readEnv(t, c)

	// Inbound: 8 slow requests occupy every slot; the 9th is refused busy.
	for i := 0; i < 9; i++ {
		writeEnv(t, c, mustMsg(t, "s-"+string(rune('a'+i)), "slow", "", nil))
	}
	e := readEnv(t, c)
	if e.Type != TypeError || e.ReplyTo != "s-i" || decodeInto[ErrorPayload](t, e.Payload).Code != CodeBusy || !decodeInto[ErrorPayload](t, e.Payload).Retryable {
		t.Fatalf("9th inbound = %+v %s, want retryable busy for s-i", e, e.Payload)
	}
	waitFor(t, "8 handlers running", func() bool { return running.Load() == 8 })
	close(release)

	// Outbound: 8 unanswered requests in flight; the 9th fails busy at once.
	p := h.hub.Peer("till-2")
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
			defer cancel()
			_, _ = p.Request(ctx, "sat.status", nil)
		}()
	}
	waitFor(t, "8 in flight", func() bool { return p.inFlight() == 8 })
	start := time.Now()
	if _, err := p.Request(context.Background(), "sat.status", nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("9th outbound err = %v, want ErrBusy", err)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("9th outbound should fail immediately")
	}
	wg.Wait()
}

func TestLink_HeartbeatPingsAndPeerGoneAfterSilence(t *testing.T) {
	cfg := fastConfig()
	cfg.PeerTimeout = 150 * time.Millisecond
	h := newWSHarness(t, cfg)
	// Before the dial: the hub's silence clock starts when it accepts, which
	// is before dial returns (#2852).
	start := time.Now()
	c, _, err := h.dial(t, "good-till-2")
	if err != nil {
		t.Fatal(err)
	}
	// We never send a frame. We must see app-level pings, then be dropped.
	pings := 0
	var closeErr error
	for {
		e, err := readEnvErr(c, 2*time.Second)
		if err != nil {
			closeErr = err
			break
		}
		if e.Type == TypePing {
			pings++
		}
	}
	el := time.Since(start)
	if pings < 2 {
		t.Fatalf("saw %d pings before disconnect, want ≥ 2 (every %v)", pings, cfg.PingInterval)
	}
	if el < cfg.PeerTimeout || el > cfg.PeerTimeout+time.Second {
		t.Fatalf("dropped after %v, want ≈ %v", el, cfg.PeerTimeout)
	}
	if websocket.CloseStatus(closeErr) != websocket.StatusCode(CloseGone) {
		t.Fatalf("close = %v, want status %d", closeErr, CloseGone)
	}
	waitFor(t, "peer unregistered", func() bool { return h.hub.Len() == 0 })
}

func TestLink_PongKeepsPeerAliveAndPingIsAnswered(t *testing.T) {
	cfg := fastConfig()
	cfg.PeerTimeout = 150 * time.Millisecond
	h := newWSHarness(t, cfg)
	c, _, err := h.dial(t, "good-till-2")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * cfg.PeerTimeout)
	gotPong := false
	for time.Now().Before(deadline) {
		e, err := readEnvErr(c, time.Second)
		if err != nil {
			t.Fatalf("link dropped although we answered every ping: %v", err)
		}
		switch e.Type {
		case TypePing:
			writeEnv(t, c, mustMsg(t, "p-"+e.ID, TypePong, e.ID, nil))
			writeEnv(t, c, mustMsg(t, "q-"+e.ID, TypePing, "", nil))
		case TypePong:
			gotPong = true
		}
	}
	if !gotPong {
		t.Fatal("our pings were never answered with pong")
	}
}

func TestLink_OneConnectionPerTill(t *testing.T) {
	h := newWSHarness(t, fastConfig())
	c1, _, err := h.dial(t, "good-till-2")
	if err != nil {
		t.Fatal(err)
	}
	readEnv(t, c1)
	c2, _, err := h.dial(t, "good-till-2")
	if err != nil {
		t.Fatal(err)
	}
	readEnv(t, c2)
	var closeErr error
	for closeErr == nil {
		_, closeErr = readEnvErr(c1, 2*time.Second)
	}
	if websocket.CloseStatus(closeErr) != websocket.StatusCode(CloseReplaced) {
		t.Fatalf("old conn closed with %v, want status %d", closeErr, CloseReplaced)
	}
	if h.hub.Len() != 1 {
		t.Fatalf("hub has %d peers, want 1", h.hub.Len())
	}
	// The new one is the live one.
	h.hub.Nudge(ScopeTables)
	if e := readEnv(t, c2); e.Type != TypeSync {
		t.Fatalf("new conn got %s", e.Type)
	}
}

func TestLink_ConnectionCapReturns503WithRetryAfter(t *testing.T) {
	cfg := fastConfig()
	cfg.MaxConns = 2
	h := newWSHarness(t, cfg)
	for _, id := range []string{"a", "b"} {
		c, _, err := h.dial(t, "good-"+id)
		if err != nil {
			t.Fatal(err)
		}
		readEnv(t, c)
	}
	waitFor(t, "2 peers", func() bool { return h.hub.Len() == 2 })
	_, resp, err := h.dial(t, "good-c")
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("3rd till: resp=%v err=%v, want 503", resp, err)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("503 without Retry-After")
	}
	// A till already connected may still replace its own link at the cap.
	c, _, err := h.dial(t, "good-a")
	if err != nil {
		t.Fatalf("replacement at the cap refused: %v", err)
	}
	readEnv(t, c)
}

func TestLink_DisconnectClosesAndForgets(t *testing.T) {
	h := newWSHarness(t, fastConfig())
	c, _, err := h.dial(t, "good-till-2")
	if err != nil {
		t.Fatal(err)
	}
	readEnv(t, c)
	waitFor(t, "peer", func() bool { return h.hub.Peer("till-2") != nil })
	h.hub.storeReport("till-2", Report{Version: "v1"})
	h.hub.Disconnect("till-2")
	var closeErr error
	for closeErr == nil {
		_, closeErr = readEnvErr(c, 2*time.Second)
	}
	if websocket.CloseStatus(closeErr) != websocket.StatusCode(CloseRevoked) {
		t.Fatalf("close = %v, want %d", closeErr, CloseRevoked)
	}
	if h.hub.Len() != 0 {
		t.Fatal("revoked peer still registered")
	}
	if _, ok := h.hub.Report("till-2"); ok {
		t.Fatal("revoked till's report kept")
	}
}

func TestLink_ReadLimitClosesOversizedMessage(t *testing.T) {
	cfg := fastConfig()
	h := newWSHarness(t, cfg)
	c, _, err := h.dial(t, "good-till-2")
	if err != nil {
		t.Fatal(err)
	}
	readEnv(t, c)
	big := mustMsg(t, "big", TypeReport, "", map[string]string{"pad": strings.Repeat("a", int(cfg.ReadLimit))})
	b, _ := Encode(big)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = c.Write(ctx, websocket.MessageText, b)
	var closeErr error
	for closeErr == nil {
		_, closeErr = readEnvErr(c, 2*time.Second)
	}
	if websocket.CloseStatus(closeErr) != websocket.StatusMessageTooBig {
		t.Fatalf("close = %v, want 1009 message too big", closeErr)
	}
	waitFor(t, "peer gone", func() bool { return h.hub.Len() == 0 })
}

func TestLink_ByeFromPeerClosesAndHubCloseSaysBye(t *testing.T) {
	h := newWSHarness(t, fastConfig())
	c, _, err := h.dial(t, "good-till-2")
	if err != nil {
		t.Fatal(err)
	}
	readEnv(t, c)
	writeEnv(t, c, mustMsg(t, "b", TypeBye, "", ByePayload{Reason: ByeRestarting}))
	waitFor(t, "peer gone after bye", func() bool { return h.hub.Len() == 0 })

	c2, _, err := h.dial(t, "good-till-3")
	if err != nil {
		t.Fatal(err)
	}
	readEnv(t, c2)
	go h.hub.Close()
	e := readEnv(t, c2)
	if e.Type != TypeBye || decodeInto[ByePayload](t, e.Payload).Reason != ByeShutdown {
		t.Fatalf("on hub close got %+v %s, want bye shutdown", e, e.Payload)
	}
	// A closed hub refuses new links.
	_, resp, err := h.dial(t, "good-till-4")
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("dial after close: resp=%v err=%v", resp, err)
	}
}

func TestLink_OnFrameTouchIsThrottled(t *testing.T) {
	var touches atomic.Int32
	cfg := fastConfig()
	hub := NewHub(HubOptions{Config: cfg, Hello: testHello, OnFrame: func(string) { touches.Add(1) }, OnFrameEvery: time.Hour})
	defer hub.Close()
	fc, _ := servePeer(t, hub, "till-2")
	fc.next(t)
	for i := 0; i < 20; i++ {
		fc.send(t, mustMsg(t, "p", TypePong, "", nil))
	}
	waitFor(t, "a touch", func() bool { return touches.Load() >= 1 })
	time.Sleep(30 * time.Millisecond)
	if n := touches.Load(); n != 1 {
		t.Fatalf("touches = %d, want 1 (throttled)", n)
	}
}
