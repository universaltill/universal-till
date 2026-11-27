package readerlink

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/fleetlink"
)

// The additional till's half: the same Service, a relayPeer for its peer. The
// main is the real relay rig; the till's link is a bridge onto the fake till
// connection the hub serves, so frames cross the real hub both ways.

// bridgeLink is a RelayConn over a fake till connection.
type bridgeLink struct {
	t      *testing.T
	conn   *phoneConn
	rc     *RelayClient
	pin    string
	relay  bool
	closed chan struct{}
	once   sync.Once

	mu      sync.Mutex
	pending map[string]chan fleetlink.Envelope
	n       atomic.Int64
}

func newBridge(t *testing.T, conn *phoneConn, pin string, relay bool) *bridgeLink {
	b := &bridgeLink{t: t, conn: conn, pin: pin, relay: relay, closed: make(chan struct{}), pending: map[string]chan fleetlink.Envelope{}}
	return b
}

// start begins reading what the main sends this till; rc gets the one-way frames.
func (b *bridgeLink) start(rc *RelayClient) {
	b.rc = rc
	go func() {
		for {
			select {
			case raw := <-b.conn.out:
				e, err := fleetlink.Decode(raw)
				if err != nil {
					continue
				}
				switch {
				case e.ReplyTo != "":
					b.mu.Lock()
					ch := b.pending[e.ReplyTo]
					b.mu.Unlock()
					if ch != nil {
						ch <- e
					}
				case e.Type == fleetlink.TypeReaderRelayResult:
					if r, err := fleetlink.DecodeReaderResult(e.Payload); err == nil {
						rc.OnResult(b, r, true)
					}
				case e.Type == fleetlink.TypeReaderRelayLost:
					if l, err := fleetlink.DecodeReaderRelayLost(e.Payload); err == nil {
						rc.OnLost(b, l)
					}
				}
			case <-b.closed:
				return
			}
		}
	}()
}

func (b *bridgeLink) Request(ctx context.Context, typ string, payload any) (json.RawMessage, error) {
	id := fmt.Sprintf("br-%d", b.n.Add(1))
	ch := make(chan fleetlink.Envelope, 1)
	b.mu.Lock()
	b.pending[id] = ch
	b.mu.Unlock()
	raw, _ := json.Marshal(payload)
	enc, _ := fleetlink.Encode(fleetlink.Envelope{ID: id, Type: typ, Payload: raw})
	b.conn.in <- enc
	select {
	case e := <-ch:
		if e.Type == fleetlink.TypeError {
			var ep fleetlink.ErrorPayload
			_ = json.Unmarshal(e.Payload, &ep)
			return nil, &fleetlink.RemoteError{Code: ep.Code, Retryable: ep.Retryable}
		}
		return e.Payload, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("%w: %s", fleetlink.ErrTimeout, typ)
	case <-b.closed:
		return nil, fleetlink.ErrClosed
	}
}

func (b *bridgeLink) Notify(typ string, payload any) error {
	raw, _ := json.Marshal(payload)
	enc, _ := fleetlink.Encode(fleetlink.Envelope{ID: fmt.Sprintf("br-%d", b.n.Add(1)), Type: typ, Payload: raw})
	b.conn.in <- enc
	return nil
}

func (b *bridgeLink) Closed() <-chan struct{} { return b.closed }
func (b *bridgeLink) VerifiedPin() string     { return b.pin }
func (b *bridgeLink) Hello() (fleetlink.Hello, bool) {
	return fleetlink.Hello{Role: "main", ReaderRelay: b.relay}, true
}

// drop is the till's link to the main going away.
func (b *bridgeLink) drop() {
	b.once.Do(func() { close(b.closed) })
	b.conn.Close(0, "till link dropped")
}

// addRig is an additional till wired to a main's relay rig.
type addRig struct {
	*relayRig
	bridge *bridgeLink
	rc     *RelayClient
	svc    *Service
	broker *fakeBroker
	atts   *fakeAttempts
	phone  *phoneConn
	nowMu  sync.Mutex
	now    time.Time
	tillID string
}

func newAddRig(t *testing.T, relay bool, pin string) *addRig {
	t.Helper()
	rr := newRelayRig(t)
	ar := &addRig{relayRig: rr, now: time.Now(), tillID: "t1"}
	ar.phone = rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	till := rr.till("t1", "additional", true)
	ar.bridge = newBridge(t, till, pin, relay)
	ar.rc = NewRelayClient(func() RelayConn { return ar.bridge }, func() time.Time { ar.nowMu.Lock(); defer ar.nowMu.Unlock(); return ar.now })
	ar.bridge.start(ar.rc)
	ar.broker = newFakeBroker()
	ar.broker.set("session_create", `{"payment_intent_id":"pi_1","client_secret":"`+testSecret+`","status":"requires_payment_method","location_id":"tml_main"}`)
	ar.atts = &fakeAttempts{m: map[string]data.PaymentAttempt{}}
	ar.svc = New(Config{Phones: ar.rc, ByRef: ar.rc.ByRef, ByID: ar.rc.ByID, Attempts: ar.atts, Broker: ar.broker.call,
		Margin: time.Second, AckTimeout: time.Second})
	ar.rc.Bind(ar.svc)
	t.Cleanup(ar.bridge.drop)
	return ar
}

func (ar *addRig) advance(d time.Duration) {
	ar.nowMu.Lock()
	ar.now = ar.now.Add(d)
	ar.nowMu.Unlock()
}

func (ar *addRig) attempt(key string, minor int64) {
	ar.atts.mu.Lock()
	ar.atts.m[key] = data.PaymentAttempt{Key: key, Provider: "stripe", AmountMinor: minor, Currency: "gbp", State: data.AttemptPending}
	ar.atts.mu.Unlock()
}

func (ar *addRig) refresh(t *testing.T) {
	t.Helper()
	ctx, cancel := ctxFor(2 * time.Second)
	defer cancel()
	if err := ar.rc.Refresh(ctx); err != nil {
		t.Fatalf("reader.list: %v", err)
	}
}

func (ar *addRig) collect(ctx context.Context, rq devicepay.Request) <-chan devicepay.Result {
	ch := make(chan devicepay.Result, 1)
	go func() { ch <- ar.svc.CollectRemote(ctx, "rd_a", rq) }()
	return ch
}

const pinned = "sha256:pin"

func TestAdditionalTillCollectViaRelayCreatesPIWithOwnCredentialAndGetsTheVerdict(t *testing.T) {
	ar := newAddRig(t, true, pinned)
	ar.refresh(t)
	ar.attempt(key, 1000)
	ctx, cancel := ctxFor(8 * time.Second)
	defer cancel()
	res := ar.collect(ctx, req(key, 1000))
	col := ackCollect(t, ar.phone)
	if col.AmountMinor != 1000 || col.ClientSecret != testSecret || col.IdempotencyKey != key || col.ReaderID != "rd_a" {
		t.Fatalf("phone got %+v", col)
	}
	waitPhase(t, ar.svc, PhaseOnPhone)
	ar.phone.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultApproved})
	got := await(t, res)
	if got.Status != devicepay.StatusApproved || got.ProviderTxnID != "pi_1" {
		t.Fatalf("result = %+v", got)
	}
	if ar.broker.count("session_create") != 1 {
		t.Fatalf("the additional till created %d PaymentIntents, want 1 (its own)", ar.broker.count("session_create"))
	}
	if n := len(ar.relayRig.broker.calls); n != 0 {
		t.Fatalf("the main made %d broker calls for the relay, want 0", n)
	}
}

func TestLinkLostIsPendingNeverDeclinedOnAdditional(t *testing.T) {
	ar := newAddRig(t, true, pinned)
	ar.refresh(t)
	ar.attempt(key, 1000)
	ctx, cancel := ctxFor(8 * time.Second)
	defer cancel()
	res := ar.collect(ctx, req(key, 1000))
	ackCollect(t, ar.phone)
	waitPhase(t, ar.svc, PhaseOnPhone)
	ar.phone.Close(0, "phone gone")
	got := await(t, res)
	if got.Status != devicepay.StatusPending || got.Reason != devicepay.ReasonLinkLost {
		t.Fatalf("result = %+v, want pending link_lost", got)
	}
	if !ar.svc.LostContact(key) {
		t.Fatal("LostContact not recorded")
	}
}

func TestTillLinkDropIsPendingOnAdditional(t *testing.T) {
	ar := newAddRig(t, true, pinned)
	ar.refresh(t)
	ar.attempt(key, 1000)
	ctx, cancel := ctxFor(8 * time.Second)
	defer cancel()
	res := ar.collect(ctx, req(key, 1000))
	ackCollect(t, ar.phone)
	waitPhase(t, ar.svc, PhaseOnPhone)
	ar.bridge.drop()
	if got := await(t, res); got.Status != devicepay.StatusPending || got.Reason != devicepay.ReasonLinkLost {
		t.Fatalf("result = %+v, want pending link_lost", got)
	}
}

func TestPhoneBusyAnswerOnAdditionalShowsReaderBusyAndCancelsItsPI(t *testing.T) {
	ar := newAddRig(t, true, pinned)
	ar.refresh(t)
	// The Mac's own collect holds the phone.
	ar.relayRig.rig.attempt("mac_1", 500)
	mctx, mcancel := ctxFor(5 * time.Second)
	defer mcancel()
	mac := ar.relayRig.rig.collect(mctx, "rd_a", req("mac_1", 500))
	ackCollect(t, ar.phone)
	waitPhase(t, ar.relayRig.svc, PhaseOnPhone)

	ar.attempt(key, 1000)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	got := await(t, ar.collect(ctx, req(key, 1000)))
	if got.Status != devicepay.StatusDeclined || got.Reason != devicepay.ReasonReaderBusy || got.DeclineCode != "start_failed" {
		t.Fatalf("result = %+v, want a not-started reader_busy", got)
	}
	if !ar.svc.WasBusy(key) {
		t.Fatal("WasBusy not recorded: the screen would not say Nothing was charged")
	}
	if ar.broker.count("session_cancel") != 1 {
		t.Fatalf("session_cancel = %d, want 1: the requesting till cancels its own PaymentIntent", ar.broker.count("session_cancel"))
	}
	mcancel()
	await(t, mac)
}

func TestAmountMismatchResultIsPendingOnAdditional(t *testing.T) {
	ar := newAddRig(t, true, pinned)
	ar.refresh(t)
	ar.attempt(key, 1000)
	ctx, cancel := ctxFor(8 * time.Second)
	defer cancel()
	res := ar.collect(ctx, req(key, 1000))
	ackCollect(t, ar.phone)
	waitPhase(t, ar.svc, PhaseOnPhone)
	ar.phone.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultPending, Reason: "amount_mismatch"})
	if got := await(t, res); got.Status != devicepay.StatusPending || got.Reason != "amount_mismatch" {
		t.Fatalf("result = %+v, want pending amount_mismatch", got)
	}
}

func TestCashierCancelOnAdditionalStopsThePhoneThroughTheMain(t *testing.T) {
	ar := newAddRig(t, true, pinned)
	ar.refresh(t)
	ar.attempt(key, 1000)
	ctx, cancel := ctxFor(8 * time.Second)
	defer cancel()
	res := ar.collect(ctx, req(key, 1000))
	ackCollect(t, ar.phone)
	waitPhase(t, ar.svc, PhaseOnPhone)
	if !ar.svc.CancelActive() {
		t.Fatal("CancelActive found nothing")
	}
	if e := ar.phone.recv(t); e.Type != fleetlink.TypeReaderCancel {
		t.Fatalf("phone got %q, want reader.cancel", e.Type)
	}
	if got := await(t, res); got.Status != devicepay.StatusCancelled {
		t.Fatalf("result = %+v", got)
	}
}

func TestRelayDirectoryNeedsPinnedLinkAndAdvertisedCapability(t *testing.T) {
	for name, tc := range map[string]struct {
		relay bool
		pin   string
	}{"plain link": {true, ""}, "older main (no reader_relay)": {false, pinned}} {
		ar := newAddRig(t, tc.relay, tc.pin)
		ctx, cancel := ctxFor(time.Second)
		_ = ar.rc.Refresh(ctx)
		cancel()
		if ar.rc.Offered() || len(ar.rc.Snapshot()) != 0 || ar.rc.ReaderPeer("rd_a") != nil {
			t.Errorf("%s: relay offered", name)
		}
	}
}

func TestRelayDirectoryExpiresAfterTTLAndRefreshes(t *testing.T) {
	ar := newAddRig(t, true, pinned)
	ar.refresh(t)
	rows := ar.rc.Snapshot()
	if len(rows) != 1 || rows[0].Ref != "rd_a" || rows[0].ID != "rd_a" || rows[0].Provider != "stripe" {
		t.Fatalf("snapshot = %+v", rows)
	}
	if st, ok := ar.rc.ReaderState("rd_a"); !ok || st.State != fleetlink.ReaderReady {
		t.Fatalf("state = %+v ok=%v", st, ok)
	}
	ar.advance(DirectoryTTL + time.Second)
	if len(ar.rc.Snapshot()) != 0 || ar.rc.ReaderPeer("rd_a") != nil {
		t.Fatal("a directory older than the TTL must offer nothing")
	}
	ctx, cancel := ctxFor(2 * time.Second)
	defer cancel()
	ar.rc.Ensure(ctx, 5*time.Second)
	if len(ar.rc.Snapshot()) != 1 {
		t.Fatal("Ensure did not refresh a stale directory")
	}
}

func TestRelayPeerKeysAreBounded(t *testing.T) {
	ar := newAddRig(t, true, pinned)
	for i := 0; i < maxRelayPeers*3; i++ {
		p := &relayPeer{rc: ar.rc, conn: ar.bridge, key: fmt.Sprintf("k%d", i), closed: make(chan struct{})}
		ar.rc.register(p)
	}
	ar.rc.mu.Lock()
	n, m := len(ar.rc.peers), len(ar.rc.order)
	ar.rc.mu.Unlock()
	if n > maxRelayPeers || m > maxRelayPeers {
		t.Fatalf("peers=%d order=%d, bound is %d", n, m, maxRelayPeers)
	}
}

func TestRelayedVerdictForAnUnknownKeyIsDropped(t *testing.T) {
	ar := newAddRig(t, true, pinned)
	ar.refresh(t)
	ar.rc.OnResult(ar.bridge, fleetlink.ReaderResult{IdempotencyKey: "never_relayed", Status: fleetlink.ResultApproved}, true)
	if ar.svc.Dropped() != 0 { // never reaches the Service at all
		t.Fatalf("dropped = %d", ar.svc.Dropped())
	}
}

// addTill builds one more additional till on rr's main (its own bridge,
// directory, service and broker).
func addTill(t *testing.T, rr *relayRig, tillID string) *addRig {
	t.Helper()
	ar := &addRig{relayRig: rr, now: time.Now(), tillID: tillID}
	ar.phone = nil
	till := rr.till(tillID, "additional", true)
	ar.bridge = newBridge(t, till, pinned, true)
	ar.rc = NewRelayClient(func() RelayConn { return ar.bridge }, nil)
	ar.bridge.start(ar.rc)
	ar.broker = newFakeBroker()
	ar.broker.set("session_create", `{"payment_intent_id":"pi_`+tillID+`","client_secret":"`+testSecret+`","status":"requires_payment_method","location_id":"tml_main"}`)
	ar.atts = &fakeAttempts{m: map[string]data.PaymentAttempt{}}
	ar.svc = New(Config{Phones: ar.rc, ByRef: ar.rc.ByRef, ByID: ar.rc.ByID, Attempts: ar.atts, Broker: ar.broker.call,
		Margin: time.Second, AckTimeout: time.Second})
	ar.rc.Bind(ar.svc)
	t.Cleanup(ar.bridge.drop)
	return ar
}

// G8: the Mac and two additional tills race for ONE phone: one winner, the
// others get busy, and no PaymentIntent is left open.
func TestMacAndAdditionalAndSecondAdditionalRaceOnePhone(t *testing.T) {
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	a1 := addTill(t, rr, "t1")
	a2 := addTill(t, rr, "t2")
	a1.refresh(t)
	a2.refresh(t)
	a1.attempt("k_a1", 1000)
	a2.attempt("k_a2", 1000)
	rr.rig.attempt("k_mac", 1000)

	ctx, cancel := ctxFor(8 * time.Second)
	defer cancel()
	r1 := a1.collect(ctx, req("k_a1", 1000))
	col := ackCollect(t, phone) // the first frame the phone sees wins the lock
	winnerKey := col.IdempotencyKey
	r2 := a2.collect(ctx, req("k_a2", 1000))
	rMac := rr.rig.collect(ctx, "rd_a", req("k_mac", 1000))

	var busy []devicepay.Result
	for _, ch := range []<-chan devicepay.Result{r2, rMac} {
		busy = append(busy, await(t, ch))
	}
	for _, b := range busy {
		if b.Status != devicepay.StatusDeclined || b.Reason != devicepay.ReasonReaderBusy {
			t.Fatalf("a loser got %+v, want a not-started reader_busy", b)
		}
	}
	if winnerKey != "k_a1" {
		t.Fatalf("the phone's one collect was %q, want k_a1", winnerKey)
	}
	if !phone.quiet(150 * time.Millisecond) {
		t.Fatal("a second reader.collect reached the phone")
	}
	waitPhase(t, a1.svc, PhaseOnPhone)
	phone.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: winnerKey, Status: fleetlink.ResultApproved})
	if got := await(t, r1); got.Status != devicepay.StatusApproved {
		t.Fatalf("winner = %+v", got)
	}
	// Nothing was left open: the loser additional till cancelled its own PI,
	// the Mac never created one, the main made no broker call.
	if a2.broker.count("session_cancel") != 1 || a1.broker.count("session_cancel") != 0 {
		t.Fatalf("session_cancel: loser %d winner %d, want 1 and 0", a2.broker.count("session_cancel"), a1.broker.count("session_cancel"))
	}
	if rr.rig.broker.count("session_create") != 0 {
		t.Fatal("the Mac created a PaymentIntent for a busy reader")
	}
	if n := len(rr.rig.broker.calls); n != 0 {
		t.Fatalf("the main made %d broker calls", n)
	}
}

func TestPhoneStateIsPresenceAndBusyWhileACollectIsHeld(t *testing.T) {
	ar := newAddRig(t, true, pinned)
	ar.refresh(t)
	ctx := context.Background()
	if st, ok := ar.svc.PhoneState(ctx, "rd_a"); !ok || st != fleetlink.ReaderReady {
		t.Fatalf("relayed state = %q ok=%v", st, ok)
	}
	if _, ok := ar.svc.PhoneState(ctx, "rd_nope"); ok {
		t.Fatal("an unknown ref has a state")
	}
	// The main's own view of the same phone, with its own collect in flight.
	ar.relayRig.rig.attempt(key, 500)
	cctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	mac := ar.relayRig.rig.collect(cctx, "rd_a", req(key, 500))
	ackCollect(t, ar.phone)
	waitPhase(t, ar.relayRig.svc, PhaseOnPhone)
	if st, _ := ar.relayRig.svc.PhoneState(ctx, "rd_a"); st != fleetlink.ReaderBusy {
		t.Fatalf("a held reader reads %q, want busy", st)
	}
	cancel()
	await(t, mac)
}

// A main that cannot answer reader.list must not be asked on every page render.
func TestEnsureDoesNotHammerAFailingMain(t *testing.T) {
	ar := newAddRig(t, true, pinned)
	var calls atomic.Int32
	failing := &countingConn{RelayConn: ar.bridge, calls: &calls}
	rc := NewRelayClient(func() RelayConn { return failing }, func() time.Time { ar.nowMu.Lock(); defer ar.nowMu.Unlock(); return ar.now })
	ctx, cancel := ctxFor(time.Second)
	defer cancel()
	for i := 0; i < 5; i++ {
		rc.Ensure(ctx, 5*time.Second)
	}
	if calls.Load() != 1 {
		t.Fatalf("%d reader.list calls in a burst, want 1", calls.Load())
	}
	ar.advance(3 * time.Second)
	rc.Ensure(ctx, 5*time.Second)
	if calls.Load() != 2 {
		t.Fatalf("%d calls after the retry delay, want 2", calls.Load())
	}
}

type countingConn struct {
	RelayConn
	calls *atomic.Int32
}

func (c *countingConn) Request(context.Context, string, any) (json.RawMessage, error) {
	c.calls.Add(1)
	return nil, fleetlink.ErrTimeout
}
