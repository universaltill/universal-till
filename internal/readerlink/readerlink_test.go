package readerlink

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/paybroker"
)

// ut-docs#6538 R1b (ADR-0198 §6.4/§6.5, design-batch2 §5/§6): the main drives,
// the phone only collects, its verdict is advice.

// phoneConn is a fake phone's end of the link.
type phoneConn struct {
	in, out chan []byte
	done    chan struct{}
	once    sync.Once
}

func newPhone() *phoneConn {
	return &phoneConn{in: make(chan []byte, 64), out: make(chan []byte, 256), done: make(chan struct{})}
}

func (c *phoneConn) Read(ctx context.Context) ([]byte, error) {
	select {
	case b := <-c.in:
		return b, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, errors.New("closed")
	}
}

func (c *phoneConn) Write(ctx context.Context, b []byte) error {
	select {
	case c.out <- append([]byte(nil), b...):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return errors.New("closed")
	}
}

func (c *phoneConn) Close(fleetlink.CloseCode, string) { c.once.Do(func() { close(c.done) }) }

// recv returns the next frame the main sent, skipping pings and hello.
func (c *phoneConn) recv(t *testing.T) fleetlink.Envelope {
	t.Helper()
	for {
		select {
		case b := <-c.out:
			e, err := fleetlink.Decode(b)
			if err != nil {
				t.Fatalf("undecodable frame: %v", err)
			}
			if e.Type == fleetlink.TypePing || e.Type == fleetlink.TypeHello || e.Type == fleetlink.TypePong {
				continue
			}
			return e
		case <-time.After(3 * time.Second):
			t.Fatal("the main sent no frame")
		}
	}
}

// quiet reports whether the main sends no non-ping frame within d.
func (c *phoneConn) quiet(d time.Duration) bool {
	deadline := time.After(d)
	for {
		select {
		case b := <-c.out:
			if e, err := fleetlink.Decode(b); err == nil && (e.Type == fleetlink.TypePing || e.Type == fleetlink.TypeHello) {
				continue
			}
			return false
		case <-deadline:
			return true
		}
	}
}

var seq int

func (c *phoneConn) send(t *testing.T, typ, replyTo string, payload any) {
	t.Helper()
	seq++
	raw, _ := json.Marshal(payload)
	b, err := fleetlink.Encode(fleetlink.Envelope{ID: fmt.Sprintf("ph-%d", seq), Type: typ, ReplyTo: replyTo, Payload: raw})
	if err != nil {
		t.Fatal(err)
	}
	c.in <- b
}

func (c *phoneConn) sendError(t *testing.T, replyTo, code string) {
	t.Helper()
	c.send(t, fleetlink.TypeError, replyTo, fleetlink.ErrorPayload{Code: code, Retryable: true})
}

type brokerCall struct {
	op   string
	body map[string]any
}

type fakeBroker struct {
	mu       sync.Mutex
	calls    []brokerCall
	handlers map[string]func(body map[string]any) ([]byte, error)
}

func newFakeBroker() *fakeBroker {
	b := &fakeBroker{handlers: map[string]func(map[string]any) ([]byte, error){}}
	b.set("session_create", `{"payment_intent_id":"pi_1","client_secret":"pi_1_secret_TOPSECRET","status":"requires_payment_method","location_id":"tml_main"}`)
	b.set("session_status", statusJSON("requires_payment_method", 0))
	b.set("session_cancel", `{"status":"canceled"}`)
	b.set("connection_token", `{"secret":"pst_TOKENSECRET","location_id":"tml_main"}`)
	return b
}

func statusJSON(status string, received int64) string {
	return fmt.Sprintf(`{"payment_intent_id":"pi_1","status":%q,"amount_received_minor":%d,"currency":"gbp","idempotency_key":"att_1:1:1"}`, status, received)
}

func (b *fakeBroker) set(op, answer string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[op] = func(map[string]any) ([]byte, error) { return []byte(answer), nil }
}

func (b *fakeBroker) fail(op string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[op] = func(map[string]any) ([]byte, error) { return nil, err }
}

func (b *fakeBroker) call(_ context.Context, provider, op string, body []byte) ([]byte, error) {
	m := map[string]any{}
	_ = json.Unmarshal(body, &m)
	b.mu.Lock()
	b.calls = append(b.calls, brokerCall{op: op, body: m})
	h := b.handlers[op]
	b.mu.Unlock()
	if provider != "stripe" || h == nil {
		return nil, paybroker.ErrNotAllowed
	}
	return h(m)
}

func (b *fakeBroker) count(op string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, c := range b.calls {
		if c.op == op {
			n++
		}
	}
	return n
}

func (b *fakeBroker) last(op string) map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.calls) - 1; i >= 0; i-- {
		if b.calls[i].op == op {
			return b.calls[i].body
		}
	}
	return nil
}

type fakeAttempts struct {
	mu sync.Mutex
	m  map[string]data.PaymentAttempt
}

func (f *fakeAttempts) Get(_ context.Context, key string) (data.PaymentAttempt, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.m[key]
	return a, ok, nil
}

type rig struct {
	t        *testing.T
	hub      *fleetlink.Hub
	svc      *Service
	broker   *fakeBroker
	attempts *fakeAttempts
	mu       sync.Mutex
	readers  map[string]Reader // by id
}

func newRig(t *testing.T) *rig { return newRigCfg(t, nil) }

// newRigCfg is newRig with a hook to adjust the Service's Config.
func newRigCfg(t *testing.T, adjust func(r *rig, c *Config)) *rig {
	t.Helper()
	cfg := fleetlink.DefaultConfig()
	cfg.PingInterval = 50 * time.Millisecond
	cfg.PeerTimeout = 10 * time.Second
	cfg.RequestTimeout = 2 * time.Second
	r := &rig{t: t, broker: newFakeBroker(), readers: map[string]Reader{},
		attempts: &fakeAttempts{m: map[string]data.PaymentAttempt{}}}
	r.hub = fleetlink.NewHub(fleetlink.HubOptions{Config: cfg})
	cfgSvc := Config{
		Link: r.hub,
		ByRef: func(_ context.Context, ref string) (Reader, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			for _, rd := range r.readers {
				if rd.Ref == ref {
					return rd, true
				}
			}
			return Reader{}, false
		},
		ByID: func(_ context.Context, id string) (Reader, bool) {
			r.mu.Lock()
			defer r.mu.Unlock()
			rd, ok := r.readers[id]
			return rd, ok
		},
		Attempts:        r.attempts,
		Broker:          r.broker.call,
		TokensPerMinute: 3,
		Margin:          time.Second,
		AckTimeout:      time.Second,
	}
	if adjust != nil {
		adjust(r, &cfgSvc)
	}
	r.svc = New(cfgSvc)
	r.hub.SetReaderSink(r.svc)
	r.hub.HandleReader(fleetlink.TypeReaderToken, r.svc.TokenHandler)
	t.Cleanup(r.hub.Close)
	return r
}

func (r *rig) addReader(id, ref string) {
	r.mu.Lock()
	r.readers[id] = Reader{ID: id, Ref: ref, Name: "Phone " + id, Provider: "stripe", Active: true}
	r.mu.Unlock()
}

// link connects a fake phone as reader id and has it report state.
func (r *rig) link(id, ref, state string) *phoneConn {
	r.t.Helper()
	r.addReader(id, ref)
	p := newPhone()
	go r.hub.ServeReaderConn(id, p)
	deadline := time.Now().Add(2 * time.Second)
	for r.hub.ReaderPeer(id) == nil {
		if time.Now().After(deadline) {
			r.t.Fatal("reader never linked")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if state != "" {
		p.send(r.t, fleetlink.TypeReaderStatus, "", fleetlink.ReaderStatus{State: state})
		for s, _ := r.hub.ReaderState(id); s.State != state; s, _ = r.hub.ReaderState(id) {
			if time.Now().After(deadline) {
				r.t.Fatal("state never arrived")
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	return p
}

func (r *rig) attempt(key string, minor int64) {
	r.attempts.mu.Lock()
	r.attempts.m[key] = data.PaymentAttempt{Key: key, Provider: "stripe", AmountMinor: minor, Currency: "gbp", State: data.AttemptPending}
	r.attempts.mu.Unlock()
}

func req(key string, minor int64) devicepay.Request {
	return devicepay.Request{Provider: "stripe", AmountMinor: minor, Currency: "gbp", IdempotencyKey: key}
}

func (r *rig) collect(ctx context.Context, ref string, rq devicepay.Request) <-chan devicepay.Result {
	ch := make(chan devicepay.Result, 1)
	go func() { ch <- r.svc.CollectRemote(ctx, ref, rq) }()
	return ch
}

func await(t *testing.T, ch <-chan devicepay.Result) devicepay.Result {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(6 * time.Second):
		t.Fatal("CollectRemote did not return")
	}
	return devicepay.Result{}
}

func decode[T any](t *testing.T, raw json.RawMessage) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func ctxFor(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), d)
}

const key = "att_1:1:1"

func waitPhase(t *testing.T, s *Service, phase string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if p, ok := s.Progress(); ok && p.Phase == phase {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	p, ok := s.Progress()
	t.Fatalf("progress = %+v ok=%v, want phase %s", p, ok, phase)
}

// A full collect: create in-process, relay, phone acks and approves.
func TestCollectDrivesCreateCollectAndAdvisoryResult(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 1250)
	ctx, cancel := ctxFor(6 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 1250))

	f := ph.recv(t)
	if f.Type != fleetlink.TypeReaderCollect {
		t.Fatalf("phone saw %q, want reader.collect", f.Type)
	}
	c := decode[fleetlink.ReaderCollect](t, f.Payload)
	if c.ReaderID != "rd_aa" || c.Provider != "stripe" || c.AmountMinor != 1250 || c.Currency != "gbp" || c.IdempotencyKey != key {
		t.Fatalf("collect frame = %+v", c)
	}
	if c.ClientSecret != "pi_1_secret_TOPSECRET" || c.LocationID != "tml_main" {
		t.Fatalf("collect frame must carry the broker's client secret and the Location: %+v", c)
	}
	if c.TimeoutMs <= 0 || c.TimeoutMs >= 6000 || c.TimeoutMs < 3000 {
		t.Fatalf("timeout_ms = %d, want the relative window a margin short of the deadline (~5000)", c.TimeoutMs)
	}
	body := r.broker.last("session_create")
	if body["idempotency_key"] != key || body["amount_minor"] != float64(1250) || body["currency"] != "gbp" {
		t.Fatalf("session_create body = %v", body)
	}

	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	waitPhase(t, r.svc, PhaseOnPhone)
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultApproved})
	got := await(t, res)
	if got.Status != devicepay.StatusApproved || got.ProviderTxnID != "pi_1" {
		t.Fatalf("result = %+v, want approved with the broker's PaymentIntent", got)
	}
	if got.CardBrand != "" || got.Last4 != "" {
		t.Fatalf("result carries card facts %+v: brand/last4 come from the broker's confirm only", got)
	}
	if _, ok := r.svc.Progress(); ok {
		t.Fatal("progress still shown after the collect ended")
	}
}

func TestCollectFrameAmountFromEventNotPhone(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 1250)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	// A request that disagrees with core's own attempt row is refused before
	// anything is created or sent.
	got := await(t, r.collect(ctx, "rd_aa", req(key, 9999)))
	if got.Status != devicepay.StatusDeclined || got.Reason != devicepay.ReasonBadRequest {
		t.Fatalf("mismatched request = %+v, want declined/bad_request", got)
	}
	if got := await(t, r.collect(ctx, "rd_aa", req("att_unknown:1:1", 1250))); got.Reason != devicepay.ReasonBadRequest {
		t.Fatalf("a key core never issued = %+v, want bad_request", got)
	}
	if r.broker.count("session_create") != 0 || !ph.quiet(100*time.Millisecond) {
		t.Fatal("a refused request created a PaymentIntent or reached the phone")
	}
}

func TestClientSecretOnlyInCollectFrame(t *testing.T) {
	var logs bytes.Buffer
	defer logging.CaptureForTest(&logs)()
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultApproved})
	got := await(t, res)
	blob, _ := json.Marshal(got)
	prog := fmt.Sprintf("%+v", got)
	for _, leak := range []string{"TOPSECRET", "client_secret"} {
		if strings.Contains(string(blob), leak) || strings.Contains(prog, leak) || strings.Contains(logs.String(), leak) {
			t.Fatalf("the client secret leaked (%q) into the result or the log", leak)
		}
	}
}

func TestSecondCollectWhileBusyAnswersBusy(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	r.attempt("att_2:1:1", 700)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	first := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	waitPhase(t, r.svc, PhaseOnPhone)
	second := await(t, r.collect(ctx, "rd_aa", req("att_2:1:1", 700)))
	if second.Status != devicepay.StatusDeclined || second.Reason != devicepay.ReasonReaderBusy {
		t.Fatalf("second collect on a busy reader = %+v, want declined/reader_busy (nothing charged)", second)
	}
	if n := r.broker.count("session_create"); n != 1 {
		t.Fatalf("session_create ran %d times, want 1 (the busy refusal comes first)", n)
	}
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultCancelled})
	await(t, first)
}

func TestReaderNotReadyIsNotStarted(t *testing.T) {
	for name, state := range map[string]string{"unavailable": fleetlink.ReaderUnavailable, "needs setup": fleetlink.ReaderNeedsSetup, "no status yet": ""} {
		t.Run(name, func(t *testing.T) {
			r := newRig(t)
			r.link("rd-1", "rd_aa", state)
			r.attempt(key, 500)
			ctx, cancel := ctxFor(3 * time.Second)
			defer cancel()
			got := await(t, r.collect(ctx, "rd_aa", req(key, 500)))
			if got.Status != devicepay.StatusDeclined || got.Reason != devicepay.ReasonReaderUnreachable {
				t.Fatalf("result = %+v, want declined/reader_unreachable", got)
			}
			if r.broker.count("session_create") != 0 {
				t.Fatal("a PaymentIntent was created for a reader that is not ready")
			}
		})
	}
	t.Run("not linked", func(t *testing.T) {
		r := newRig(t)
		r.addReader("rd-1", "rd_aa")
		r.attempt(key, 500)
		ctx, cancel := ctxFor(3 * time.Second)
		defer cancel()
		if got := await(t, r.collect(ctx, "rd_aa", req(key, 500))); got.Reason != devicepay.ReasonReaderUnreachable {
			t.Fatalf("result = %+v", got)
		}
	})
	t.Run("unknown ref", func(t *testing.T) {
		r := newRig(t)
		r.attempt(key, 500)
		ctx, cancel := ctxFor(3 * time.Second)
		defer cancel()
		if got := await(t, r.collect(ctx, "rd_nope", req(key, 500))); got.Reason != devicepay.ReasonReaderUnreachable {
			t.Fatalf("result = %+v", got)
		}
	})
}

func TestPhoneBusyAnswerIsNotStartedAndCancelsThePaymentIntent(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	ph.sendError(t, f.ID, fleetlink.CodeReaderBusy)
	got := await(t, res)
	if got.Status != devicepay.StatusDeclined || got.DeclineCode != "start_failed" || got.Reason != devicepay.ReasonReaderBusy {
		t.Fatalf("busy phone = %+v, want declined start_failed/reader_busy (nothing charged)", got)
	}
	if r.broker.count("session_cancel") != 1 {
		t.Fatal("the unused PaymentIntent was not cancelled")
	}
}

func TestCreateFailureIsStartFailedNotDeclined(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	r.broker.fail("session_create", paybroker.ErrOffline)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	got := await(t, r.collect(ctx, "rd_aa", req(key, 500)))
	if got.Status != devicepay.StatusDeclined || got.DeclineCode != "start_failed" {
		t.Fatalf("create failure = %+v, want start_failed (no secret ever reached a phone)", got)
	}
	if !ph.quiet(100 * time.Millisecond) {
		t.Fatal("the phone was asked to collect without a PaymentIntent")
	}
}

func TestResultForUnissuedKeyDropped(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(3 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	waitPhase(t, r.svc, PhaseOnPhone)
	before := r.svc.Dropped()
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: "att_other:9:9", Status: fleetlink.ResultApproved})
	deadline := time.Now().Add(2 * time.Second)
	for r.svc.Dropped() == before && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if r.svc.Dropped() != before+1 {
		t.Fatal("a result for a key this main never issued was not dropped (and counted)")
	}
	select {
	case got := <-res:
		t.Fatalf("a foreign key's result ended the collect: %+v", got)
	case <-time.After(150 * time.Millisecond):
	}
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultCancelled})
	await(t, res)
}

func TestResultOnOtherLinkDropped(t *testing.T) {
	r := newRig(t)
	ph1 := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	ph2 := r.link("rd-2", "rd_bb", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(3 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph1.recv(t)
	ph1.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	waitPhase(t, r.svc, PhaseOnPhone)
	// A rogue (or merely other) enrolled phone claims the first reader's key.
	ph2.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultApproved})
	select {
	case got := <-res:
		t.Fatalf("a result from another reader's link ended the collect: %+v", got)
	case <-time.After(200 * time.Millisecond):
	}
	if r.svc.Dropped() != 1 {
		t.Fatalf("dropped = %d, want 1", r.svc.Dropped())
	}
	ph1.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultApproved})
	if got := await(t, res); got.Status != devicepay.StatusApproved {
		t.Fatalf("the real result = %+v", got)
	}
}

func TestSecondResultForSettledKeyNoop(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(3 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultApproved})
	if got := await(t, res); got.Status != devicepay.StatusApproved {
		t.Fatal(got)
	}
	before := r.svc.Dropped()
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultApproved})
	deadline := time.Now().Add(2 * time.Second)
	for r.svc.Dropped() == before && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if r.svc.Dropped() != before+1 {
		t.Fatal("a replayed result was not dropped")
	}
}

func TestLinkDropMidCollectIsPendingNeverDeclined(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	waitPhase(t, r.svc, PhaseOnPhone)
	ph.Close(fleetlink.CloseNormal, "wifi gone")
	got := await(t, res)
	if got.Status != devicepay.StatusPending || got.Reason != devicepay.ReasonLinkLost {
		t.Fatalf("link drop = %+v, want pending/link_lost (the customer may have been charged)", got)
	}
	if !r.svc.LostContact(key) {
		t.Fatal("LostContact(key) = false, the till must show the lost-contact wording")
	}
	if r.broker.count("session_cancel") != 0 {
		t.Fatal("a possibly-charged PaymentIntent was cancelled on a link drop")
	}
}

func TestRevokeMidCollectIsPending(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	waitPhase(t, r.svc, PhaseOnPhone)
	r.hub.DisconnectReader("rd-1")
	if got := await(t, res); got.Status != devicepay.StatusPending {
		t.Fatalf("revoke mid-collect = %+v, want pending", got)
	}
}

func TestPhoneDeclinedIsDeclinedOnlyWhenTheBrokerAgrees(t *testing.T) {
	cases := []struct {
		name, brokerStatus string
		brokerDown         bool
		want               string
	}{
		{"broker says not paid", "requires_payment_method", false, devicepay.StatusDeclined},
		{"broker says paid", "succeeded", false, devicepay.StatusApproved},
		{"broker says processing", "processing", false, devicepay.StatusPending},
		{"broker down", "", true, devicepay.StatusPending},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t)
			ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
			r.attempt(key, 500)
			if tc.brokerDown {
				r.broker.fail("session_status", paybroker.ErrOffline)
			} else {
				r.broker.set("session_status", statusJSON(tc.brokerStatus, 500))
			}
			ctx, cancel := ctxFor(5 * time.Second)
			defer cancel()
			res := r.collect(ctx, "rd_aa", req(key, 500))
			f := ph.recv(t)
			ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
			ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultDeclined, DeclineCode: "card_declined"})
			got := await(t, res)
			if got.Status != tc.want {
				t.Fatalf("phone declined, %s: result %+v, want %s", tc.name, got, tc.want)
			}
			if got.Status == devicepay.StatusDeclined && got.DeclineCode != "card_declined" {
				t.Fatalf("decline code = %q", got.DeclineCode)
			}
		})
	}
}

func TestPhonePendingResultStaysPending(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultPending, Reason: "amount_mismatch"})
	if got := await(t, res); got.Status != devicepay.StatusPending {
		t.Fatalf("phone pending = %+v", got)
	}
}

func TestMalformedResultForTheCollectEndsPendingAtOnce(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	ph.send(t, fleetlink.TypeReaderResult, "", map[string]any{"idempotency_key": key, "status": "approved", "last4": "4242", "card_brand": "visa"})
	if got := await(t, res); got.Status != devicepay.StatusPending || got.Last4 != "" || got.CardBrand != "" {
		t.Fatalf("a result with card fields = %+v, want pending with none of them kept", got)
	}
}

func TestTimeoutSendsReaderCancelThenSessionCancel(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(1500 * time.Millisecond)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	c := ph.recv(t)
	if c.Type != fleetlink.TypeReaderCancel || decode[fleetlink.ReaderCancel](t, c.Payload).IdempotencyKey != key {
		t.Fatalf("after the window the phone saw %+v, want reader.cancel for the key", c)
	}
	got := await(t, res)
	if got.Status != devicepay.StatusCancelled && got.Status != devicepay.StatusPending {
		t.Fatalf("timeout result = %+v, want cancelled or pending, never declined", got)
	}
	if r.broker.count("session_cancel") != 1 {
		t.Fatal("the PaymentIntent was not cancelled after the timeout")
	}
}

func TestTimeoutCancelFindingSucceededIsApplied(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	// The customer tapped at the last moment: the cancel is refused and the
	// broker says succeeded (#6304).
	r.broker.fail("session_cancel", &paybroker.HTTPError{Status: 409, Code: "payment_succeeded"})
	r.broker.set("session_status", statusJSON("succeeded", 500))
	ctx, cancel := ctxFor(1500 * time.Millisecond)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	if got := await(t, res); got.Status != devicepay.StatusApproved || got.ProviderTxnID != "pi_1" {
		t.Fatalf("late success = %+v, want approved (core still confirms it)", got)
	}
}

func TestCollectPastDeadlineSendsNothing(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(time.Nanosecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	got := await(t, r.collect(ctx, "rd_aa", req(key, 500)))
	if got.Status != devicepay.StatusPending {
		t.Fatalf("expired deadline = %+v, want pending", got)
	}
	if r.broker.count("session_create") != 0 || !ph.quiet(100*time.Millisecond) {
		t.Fatal("work was started past the deadline")
	}
}

// ---- connection token relay ----

func askToken(t *testing.T, r *rig, ph *phoneConn) fleetlink.Envelope {
	t.Helper()
	ph.send(t, fleetlink.TypeReaderToken, "", map[string]any{})
	return ph.recv(t)
}

func TestReaderTokenRelayedToReadyReaderOnly(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	e := askToken(t, r, ph)
	if e.Type != fleetlink.TypeReaderToken {
		t.Fatalf("answer = %+v", e)
	}
	rep := decode[fleetlink.ReaderTokenReply](t, e.Payload)
	if rep.Secret != "pst_TOKENSECRET" || rep.LocationID != "tml_main" {
		t.Fatalf("token reply = %+v", rep)
	}
	for name, state := range map[string]string{"needs_setup": fleetlink.ReaderNeedsSetup, "unavailable": fleetlink.ReaderUnavailable} {
		r2 := newRig(t)
		ph2 := r2.link("rd-2", "rd_bb", state)
		if e := askToken(t, r2, ph2); e.Type != fleetlink.TypeError || decode[fleetlink.ErrorPayload](t, e.Payload).Code != fleetlink.CodeReaderNotReady {
			t.Fatalf("%s reader got %+v, want not_ready", name, e)
		}
		if r2.broker.count("connection_token") != 0 {
			t.Fatalf("a connection token was minted for a %s reader", name)
		}
	}
	// busy (collecting) may still get one: the SDK refreshes its token mid-session.
	r3 := newRig(t)
	ph3 := r3.link("rd-3", "rd_cc", fleetlink.ReaderBusy)
	if e := askToken(t, r3, ph3); e.Type != fleetlink.TypeReaderToken {
		t.Fatalf("busy reader got %+v, want a token", e)
	}
}

func TestReaderTokenRateLimited(t *testing.T) {
	r := newRig(t) // 3 per minute
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	for i := 0; i < 3; i++ {
		if e := askToken(t, r, ph); e.Type != fleetlink.TypeReaderToken {
			t.Fatalf("token %d = %+v", i, e)
		}
	}
	e := askToken(t, r, ph)
	if e.Type != fleetlink.TypeError || decode[fleetlink.ErrorPayload](t, e.Payload).Code != fleetlink.CodeReaderRateLimited {
		t.Fatalf("4th token = %+v, want rate_limited", e)
	}
	if n := r.broker.count("connection_token"); n != 3 {
		t.Fatalf("broker asked %d times, want 3 (a limited ask never reaches the broker)", n)
	}
}

func TestRevokedReaderGetsNoToken(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.mu.Lock()
	rd := r.readers["rd-1"]
	rd.Active = false // revoked/removed between the link and the ask
	r.readers["rd-1"] = rd
	r.mu.Unlock()
	e := askToken(t, r, ph)
	if e.Type != fleetlink.TypeError {
		t.Fatalf("a revoked reader was answered %+v", e)
	}
	if r.broker.count("connection_token") != 0 {
		t.Fatal("a token was minted for a revoked reader")
	}
}

func TestReaderCannotCallConnectionTokenForOtherLocation(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	ph.send(t, fleetlink.TypeReaderToken, "", map[string]any{"location_id": "tml_other_shop", "reader_id": "rd_bb"})
	e := ph.recv(t)
	if e.Type != fleetlink.TypeReaderToken {
		t.Fatalf("answer = %+v", e)
	}
	body := r.broker.last("connection_token")
	if len(body) != 0 {
		t.Fatalf("connection_token body = %v: nothing from the phone's frame may be forwarded (the store derives the Location)", body)
	}
	if rep := decode[fleetlink.ReaderTokenReply](t, e.Payload); rep.LocationID != "tml_main" {
		t.Fatalf("location = %q, must be the broker's (the store's), never the frame's", rep.LocationID)
	}
}

func TestReaderTokenNeverInLogs(t *testing.T) {
	var logs bytes.Buffer
	defer logging.CaptureForTest(&logs)()
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	askToken(t, r, ph)
	r.broker.fail("connection_token", errors.New("boom pst_TOKENSECRET"))
	askToken(t, r, ph)
	if strings.Contains(logs.String(), "TOKENSECRET") {
		t.Fatalf("a connection token reached the log: %s", logs.String())
	}
}

func TestProgressNamesTheReaderAndPhase(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	if p, ok := r.svc.Progress(); !ok || p.Phase != PhaseSent || p.ReaderName != "Phone rd-1" {
		t.Fatalf("before the ack: %+v %v", p, ok)
	}
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: true})
	waitPhase(t, r.svc, PhaseOnPhone)
	if p, _ := r.svc.Progress(); p.Deadline.IsZero() {
		t.Fatal("progress has no deadline for the countdown")
	}
	ph.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: key, Status: fleetlink.ResultCancelled})
	await(t, res)
}

// G1 (review 2026-11-25): a phone that acks accepted:false started nothing, so
// the unused PaymentIntent is cancelled and the till gets start_failed/busy.
func TestPhoneAckNotAcceptedCancelsThePaymentIntent(t *testing.T) {
	r := newRig(t)
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	f := ph.recv(t)
	ph.send(t, fleetlink.TypeReaderCollect, f.ID, fleetlink.ReaderCollectAck{Accepted: false})
	got := await(t, res)
	if got.Status != devicepay.StatusDeclined || got.DeclineCode != "start_failed" || got.Reason != devicepay.ReasonReaderBusy {
		t.Fatalf("accepted:false ack = %+v, want declined start_failed/reader_busy", got)
	}
	if r.broker.count("session_cancel") != 1 {
		t.Fatal("the unused PaymentIntent was not cancelled")
	}
}

// G2 (review 2026-11-25): no ack in time means the phone may have started, so the
// answer is pending/link_lost (never declined) and the PaymentIntent is NOT cancelled.
func TestAckTimeoutIsPendingNeverDeclinedAndKeepsThePaymentIntent(t *testing.T) {
	r := newRigCfg(t, func(_ *rig, c *Config) { c.AckTimeout = 150 * time.Millisecond })
	ph := r.link("rd-1", "rd_aa", fleetlink.ReaderReady)
	r.attempt(key, 500)
	ctx, cancel := ctxFor(5 * time.Second)
	defer cancel()
	res := r.collect(ctx, "rd_aa", req(key, 500))
	_ = ph.recv(t) // the phone got the collect frame and never answers
	got := await(t, res)
	if got.Status != devicepay.StatusPending || got.Reason != devicepay.ReasonLinkLost {
		t.Fatalf("ack timeout = %+v, want pending/link_lost", got)
	}
	if r.broker.count("session_cancel") != 0 {
		t.Fatal("a possibly-started PaymentIntent was cancelled on an ack timeout")
	}
}
