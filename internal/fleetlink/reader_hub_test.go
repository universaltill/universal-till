package fleetlink

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// ut-docs#6538 R1b (ADR-0198 §6.3/§6.5, design-batch2 §4/§6): the reader peer
// class of the hub and the frames of the card reader link.

// readerHarness serves the hub's reader class over a real httptest server:
// "Bearer good-<id>" is reader <id>, "Bearer till-<id>" is till <id>.
type readerHarness struct {
	hub *Hub
	srv *httptest.Server
}

func newReaderHarness(t *testing.T, cfg Config) *readerHarness {
	t.Helper()
	hub := NewHub(HubOptions{Config: cfg, Hello: testHello})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if id, ok := strings.CutPrefix(tok, "good-"); ok && id != "" {
			hub.ServeReader(w, r, id)
			return
		}
		if id, ok := strings.CutPrefix(tok, "till-"); ok && id != "" {
			hub.Serve(w, r, id)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	t.Cleanup(func() { hub.Close(); srv.Close() })
	return &readerHarness{hub: hub, srv: srv}
}

func (h *readerHarness) dial(t *testing.T, bearer string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(h.srv.URL, "http"),
		&websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + bearer}}})
	if c != nil {
		c.SetReadLimit(1 << 20)
		t.Cleanup(func() { _ = c.CloseNow() })
	}
	return c, resp, err
}

func (h *readerHarness) phone(t *testing.T, id string) *websocket.Conn {
	t.Helper()
	c, _, err := h.dial(t, "good-"+id)
	if err != nil {
		t.Fatalf("dial reader %s: %v", id, err)
	}
	return c
}

func readGolden(t *testing.T, name string) Envelope {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	e, err := Decode(raw)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return e
}

func TestReaderFrameGoldens(t *testing.T) {
	cases := []struct {
		file, typ string
		into      func() any
	}{
		{"reader_hello_main.json", TypeHello, func() any { return &ReaderHello{} }},
		{"reader_hello_main_rotating.json", TypeHello, func() any { return &ReaderHello{} }},
		{"reader_hello_phone.json", TypeHello, func() any { return &ReaderHello{} }},
		{"reader_status_ready.json", TypeReaderStatus, func() any { return &ReaderStatus{} }},
		{"reader_status_needs_setup.json", TypeReaderStatus, func() any { return &ReaderStatus{} }},
		{"reader_token_reply.json", TypeReaderToken, func() any { return &ReaderTokenReply{} }},
		{"reader_collect.json", TypeReaderCollect, func() any { return &ReaderCollect{} }},
		{"reader_collect_ack.json", TypeReaderCollect, func() any { return &ReaderCollectAck{} }},
		{"reader_cancel.json", TypeReaderCancel, func() any { return &ReaderCancel{} }},
		{"reader_result_approved.json", TypeReaderResult, func() any { return &ReaderResult{} }},
		{"reader_result_declined.json", TypeReaderResult, func() any { return &ReaderResult{} }},
		{"reader_result_pending.json", TypeReaderResult, func() any { return &ReaderResult{} }},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			e := readGolden(t, tc.file)
			if e.Type != tc.typ {
				t.Fatalf("type = %q, want %q", e.Type, tc.typ)
			}
			// The shared fixture and the Go structs must say the same thing
			// both ways: no field the struct lacks, none the fixture lacks.
			dec := json.NewDecoder(bytes.NewReader(e.Payload))
			dec.DisallowUnknownFields()
			v := tc.into()
			if err := dec.Decode(v); err != nil {
				t.Fatalf("payload does not decode strictly into %T: %v", v, err)
			}
			back, _ := json.Marshal(v)
			var a, b map[string]any
			_ = json.Unmarshal(back, &a)
			_ = json.Unmarshal(e.Payload, &b)
			if !jsonEqual(a, b) {
				t.Fatalf("round trip changed the payload: %s vs %s", back, e.Payload)
			}
		})
	}
	e := readGolden(t, "reader_token_request.json")
	if e.Type != TypeReaderToken || e.ReplyTo != "" {
		t.Fatalf("token request = %+v", e)
	}
	if e := readGolden(t, "reader_collect_busy.json"); e.Type != TypeError || e.ReplyTo == "" {
		t.Fatalf("busy = %+v", e)
	}
}

func jsonEqual(a, b map[string]any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func TestReaderResultDecodesTheGoldens(t *testing.T) {
	r, err := DecodeReaderResult(readGolden(t, "reader_result_approved.json").Payload)
	if err != nil || r.Status != ResultApproved || r.IdempotencyKey != "att_7:1:1" {
		t.Fatalf("approved golden = %+v, %v", r, err)
	}
	r, err = DecodeReaderResult(readGolden(t, "reader_result_declined.json").Payload)
	if err != nil || r.Status != ResultDeclined || r.DeclineCode != "card_declined" {
		t.Fatalf("declined golden = %+v, %v", r, err)
	}
	s, err := DecodeReaderStatus(readGolden(t, "reader_status_needs_setup.json").Payload)
	if err != nil || s.State != ReaderNeedsSetup || s.Reason != "reader_needs_setup" {
		t.Fatalf("status golden = %+v, %v", s, err)
	}
}

func TestReaderResultUnknownFieldRefused(t *testing.T) {
	for name, body := range map[string]string{
		"extra field":          `{"idempotency_key":"k","status":"approved","pan":"4242424242424242"}`,
		"brand from the phone": `{"idempotency_key":"k","status":"approved","card_brand":"visa"}`,
		"last4 from the phone": `{"idempotency_key":"k","status":"approved","last4":"4242"}`,
		"unknown status":       `{"idempotency_key":"k","status":"succeeded"}`,
		"no key":               `{"status":"approved"}`,
		"trailing data":        `{"idempotency_key":"k","status":"approved"} {}`,
		"not an object":        `["k"]`,
		"huge key":             `{"idempotency_key":"` + strings.Repeat("k", 300) + `","status":"approved"}`,
		"bad decline code":     `{"idempotency_key":"k","status":"declined","decline_code":"has space & <b>"}`,
	} {
		if r, err := DecodeReaderResult(json.RawMessage(body)); err == nil {
			t.Errorf("%s: accepted %+v, want refusal", name, r)
		}
	}
}

func TestBrandLast4NeverFromPhone(t *testing.T) {
	// There is no field a phone can put a brand or last four in: the struct
	// that a result decodes into has none, so there is nothing to record.
	typ := reflectFieldNames(ReaderResult{})
	for _, bad := range []string{"CardBrand", "Last4", "Brand", "PAN", "Card"} {
		if typ[bad] {
			t.Fatalf("ReaderResult has a %s field: card facts come from the broker only (ADR-0127)", bad)
		}
	}
}

func TestReaderStatusStrict(t *testing.T) {
	if _, err := DecodeReaderStatus(json.RawMessage(`{"state":"weird"}`)); err == nil {
		t.Error("unknown state accepted")
	}
	if _, err := DecodeReaderStatus(json.RawMessage(`{"state":"ready","extra":1}`)); err == nil {
		t.Error("unknown field accepted")
	}
	s, err := DecodeReaderStatus(json.RawMessage(`{"state":"ready","reason":"<script>"}`))
	if err != nil || s.Reason != "" {
		t.Errorf("a reason outside the closed set must be dropped, got %+v, %v", s, err)
	}
}

func TestReaderGetsAHelloWithoutShopData(t *testing.T) {
	h := newReaderHarness(t, fastConfig())
	c := h.phone(t, "rd-1")
	e := readEnv(t, c)
	if e.Type != TypeHello {
		t.Fatalf("first frame = %q, want hello", e.Type)
	}
	body := string(e.Payload)
	for _, leak := range []string{"cursors", "update_policy", "fleet_target", "admin", "plugins", "stock_version", "sync_protocol", "till_id"} {
		if strings.Contains(body, leak) {
			t.Fatalf("reader hello leaks %q: %s", leak, body)
		}
	}
	hello := decodeInto[ReaderHello](t, e.Payload)
	if hello.Role != "main" || hello.ReaderProtocol != ReaderProtocol || hello.ReaderID != "rd-1" {
		t.Fatalf("hello = %+v", hello)
	}
}

func TestReaderPresenceFollowsStatusAndLink(t *testing.T) {
	h := newReaderHarness(t, fastConfig())
	if _, ok := h.hub.ReaderState("rd-1"); ok {
		t.Fatal("a reader with no link has a state")
	}
	c := h.phone(t, "rd-1")
	readEnv(t, c) // hello
	waitFor(t, "reader linked", func() bool { return h.hub.ReaderPeer("rd-1") != nil })
	writeEnv(t, c, mustMsg(t, "p-1", TypeReaderStatus, "", ReaderStatus{State: ReaderReady}))
	waitFor(t, "ready", func() bool { s, ok := h.hub.ReaderState("rd-1"); return ok && s.State == ReaderReady })
	writeEnv(t, c, mustMsg(t, "p-2", TypeReaderStatus, "", ReaderStatus{State: ReaderBusy}))
	waitFor(t, "busy", func() bool { s, _ := h.hub.ReaderState("rd-1"); return s.State == ReaderBusy })
	_ = c.Close(websocket.StatusNormalClosure, "bye")
	waitFor(t, "link gone", func() bool { _, ok := h.hub.ReaderState("rd-1"); return !ok })
	if h.hub.ReaderCount() != 0 {
		t.Fatalf("ReaderCount = %d after the phone left", h.hub.ReaderCount())
	}
}

func TestReaderCannotSendSatFrames(t *testing.T) {
	h := newReaderHarness(t, fastConfig())
	var called atomic.Bool
	h.hub.Handle("sat.start", func(context.Context, string, json.RawMessage) (any, error) {
		called.Store(true)
		return map[string]string{"ok": "yes"}, nil
	})
	c := h.phone(t, "rd-1")
	readEnv(t, c)
	writeEnv(t, c, mustMsg(t, "p-1", "sat.start", "", map[string]string{"x": "y"}))
	e := readEnv(t, c)
	if e.Type != TypeError || e.ReplyTo != "p-1" || decodeInto[ErrorPayload](t, e.Payload).Code != CodeUnknownType {
		t.Fatalf("a reader's sat.start was answered %+v, want unknown_type", e)
	}
	if called.Load() {
		t.Fatal("a reader reached a till handler")
	}
}

func TestUnknownReaderFrameAnswersUnknownType(t *testing.T) {
	h := newReaderHarness(t, fastConfig())
	c := h.phone(t, "rd-1")
	readEnv(t, c)
	for i, typ := range []string{"reader.bogus", "sync", "fleet", "cloud_checkin", "pairing"} {
		id := "p-" + string(rune('a'+i))
		writeEnv(t, c, mustMsg(t, id, typ, "", map[string]string{}))
		if typ == "reader.bogus" {
			e := readEnv(t, c)
			if e.Type != TypeError || decodeInto[ErrorPayload](t, e.Payload).Code != CodeUnknownType {
				t.Fatalf("%s answered %+v, want unknown_type", typ, e)
			}
		}
	}
	// A till frame type from a reader never changes the reader's state or
	// the hub's till registry.
	if len(h.hub.Peers()) != 0 {
		t.Fatalf("a reader appears in the till list: %+v", h.hub.Peers())
	}
}

func TestTillFramesNotAddressableToReader(t *testing.T) {
	h := newReaderHarness(t, fastConfig())
	h.hub.HandleReader(TypeReaderToken, func(context.Context, string, json.RawMessage) (any, error) {
		return ReaderTokenReply{Secret: "s"}, nil
	})
	// A till (not a reader) asking for a reader frame type: unknown_type.
	c, _, err := h.dial(t, "till-t1")
	if err != nil {
		t.Fatal(err)
	}
	readEnv(t, c)
	writeEnv(t, c, mustMsg(t, "t-1", TypeReaderToken, "", nil))
	e := readEnv(t, c)
	if e.Type != TypeError || decodeInto[ErrorPayload](t, e.Payload).Code != CodeUnknownType {
		t.Fatalf("a till reached a reader handler: %+v", e)
	}
	// And a reader's sync nudge never reaches it: Nudge is for tills only.
	r := h.phone(t, "rd-1")
	readEnv(t, r)
	h.hub.Nudge(ScopeAdmin)
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		e, err := readEnvErr(r, 100*time.Millisecond)
		if err == nil && e.Type != TypePing {
			t.Fatalf("a reader received a till frame: %+v", e)
		}
	}
}

func TestReaderTokenHandlerAnswersOnlyOnItsLink(t *testing.T) {
	h := newReaderHarness(t, fastConfig())
	var gotID atomic.Value
	h.hub.HandleReader(TypeReaderToken, func(_ context.Context, id string, _ json.RawMessage) (any, error) {
		gotID.Store(id)
		return ReaderTokenReply{Secret: "sek", LocationID: "tml_1"}, nil
	})
	c := h.phone(t, "rd-1")
	readEnv(t, c)
	writeEnv(t, c, mustMsg(t, "p-1", TypeReaderToken, "", nil))
	e := readEnv(t, c)
	if e.Type != TypeReaderToken || e.ReplyTo != "p-1" {
		t.Fatalf("token reply = %+v", e)
	}
	if r := decodeInto[ReaderTokenReply](t, e.Payload); r.Secret != "sek" || r.LocationID != "tml_1" {
		t.Fatalf("reply = %+v", r)
	}
	if gotID.Load() != "rd-1" {
		t.Fatalf("handler saw reader %v, want rd-1 (the link's id, never a frame field)", gotID.Load())
	}
}

func TestHubCapCountsBothClasses(t *testing.T) {
	cfg := fastConfig()
	cfg.MaxConns = 2
	h := newReaderHarness(t, cfg)
	till, _, err := h.dial(t, "till-t1")
	if err != nil {
		t.Fatal(err)
	}
	readEnv(t, till)
	rd := h.phone(t, "rd-1")
	readEnv(t, rd)
	// The cap is shared: a third link of either class is refused.
	if _, resp, err := h.dial(t, "good-rd-2"); err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a third (reader) link over the shared cap: err=%v resp=%v, want 503", err, resp)
	}
	if _, resp, err := h.dial(t, "till-t2"); err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("a third (till) link over the shared cap: err=%v resp=%v, want 503", err, resp)
	}
	// A reconnect of an existing reader replaces its link, it is not a new one.
	if _, _, err := h.dial(t, "good-rd-1"); err != nil {
		t.Fatalf("a reader reconnect over the cap: %v", err)
	}
}

func TestReaderReplacedLinkClosesTheOldOne(t *testing.T) {
	h := newReaderHarness(t, fastConfig())
	a := h.phone(t, "rd-1")
	readEnv(t, a)
	waitFor(t, "first link", func() bool { return h.hub.ReaderPeer("rd-1") != nil })
	first := h.hub.ReaderPeer("rd-1")
	b := h.phone(t, "rd-1")
	readEnv(t, b)
	waitFor(t, "replaced", func() bool { p := h.hub.ReaderPeer("rd-1"); return p != nil && p != first })
	select {
	case <-first.Closed():
	case <-time.After(2 * time.Second):
		t.Fatal("the replaced reader link was not closed")
	}
}

func TestDisconnectReaderClosesTheLinkAtOnce(t *testing.T) {
	h := newReaderHarness(t, fastConfig())
	c := h.phone(t, "rd-1")
	readEnv(t, c)
	waitFor(t, "linked", func() bool { return h.hub.ReaderPeer("rd-1") != nil })
	p := h.hub.ReaderPeer("rd-1")
	h.hub.DisconnectReader("rd-1")
	select {
	case <-p.Closed():
	case <-time.After(time.Second):
		t.Fatal("link still open after DisconnectReader")
	}
	if h.hub.ReaderPeer("rd-1") != nil {
		t.Fatal("reader still registered after DisconnectReader")
	}
	if _, err := readEnvErr(c, 2*time.Second); err == nil {
		t.Fatal("the phone's socket stayed readable")
	}
}

type captureSink struct {
	mu  sync.Mutex
	got []ReaderResult
	oks []bool
	ps  []*Peer
}

func (c *captureSink) ReaderResult(p *Peer, r ReaderResult, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got, c.oks, c.ps = append(c.got, r), append(c.oks, ok), append(c.ps, p)
}

func TestReaderResultReachesTheSinkWithItsLink(t *testing.T) {
	h := newReaderHarness(t, fastConfig())
	sink := &captureSink{}
	h.hub.SetReaderSink(sink)
	c := h.phone(t, "rd-1")
	readEnv(t, c)
	waitFor(t, "linked", func() bool { return h.hub.ReaderPeer("rd-1") != nil })
	writeEnv(t, c, mustMsg(t, "p-1", TypeReaderResult, "", ReaderResult{IdempotencyKey: "k1", Status: ResultApproved}))
	waitFor(t, "result", func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.got) == 1 })
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if !sink.oks[0] || sink.got[0].IdempotencyKey != "k1" || sink.ps[0] != h.hub.ReaderPeer("rd-1") {
		t.Fatalf("sink got %+v ok=%v peer=%v", sink.got[0], sink.oks[0], sink.ps[0])
	}
	// A malformed result still tells the sink which key it named (so the
	// collect can end pending at once) but is flagged not ok.
	sink.mu.Unlock()
	writeEnv(t, c, mustMsg(t, "p-2", TypeReaderResult, "", map[string]any{"idempotency_key": "k2", "status": "approved", "last4": "4242"}))
	waitFor(t, "bad result", func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.got) == 2 })
	sink.mu.Lock()
	if sink.oks[1] || sink.got[1].IdempotencyKey != "k2" {
		t.Fatalf("malformed result = %+v ok=%v, want key k2 and not ok", sink.got[1], sink.oks[1])
	}
}

func TestReaderRequestFromMainGetsReplyByReplyTo(t *testing.T) {
	h := newReaderHarness(t, fastConfig())
	c := h.phone(t, "rd-1")
	readEnv(t, c)
	waitFor(t, "linked", func() bool { return h.hub.ReaderPeer("rd-1") != nil })
	p := h.hub.ReaderPeer("rd-1")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan json.RawMessage, 1)
	go func() {
		raw, err := p.Request(ctx, TypeReaderCollect, ReaderCollect{IdempotencyKey: "k1"})
		if err != nil {
			t.Errorf("request: %v", err)
		}
		done <- raw
	}()
	e := readEnv(t, c)
	if e.Type != TypeReaderCollect || decodeInto[ReaderCollect](t, e.Payload).IdempotencyKey != "k1" {
		t.Fatalf("phone saw %+v", e)
	}
	writeEnv(t, c, mustMsg(t, "p-1", TypeReaderCollect, e.ID, ReaderCollectAck{Accepted: true}))
	select {
	case raw := <-done:
		if !decodeInto[ReaderCollectAck](t, raw).Accepted {
			t.Fatalf("ack = %s", raw)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no reply")
	}
	if err := p.Notify(TypeReaderCancel, ReaderCancel{IdempotencyKey: "k1"}); err != nil {
		t.Fatal(err)
	}
	if e := readEnv(t, c); e.Type != TypeReaderCancel {
		t.Fatalf("phone saw %+v, want reader.cancel", e)
	}
}

func reflectFieldNames(v any) map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(v)
	for i := 0; i < t.NumField(); i++ {
		out[t.Field(i).Name] = true
	}
	return out
}

// ut-docs#6796: the reader hello carries the pin of the key this till serves
// next during a rotation only; nil option or "" omit it.
func TestReaderHello_NextPin(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func() string
		want string
	}{
		{"nil", nil, ""},
		{"empty", func() string { return "" }, ""},
		{"rotating", func() string { return "sha256:abc" }, "sha256:abc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rh := readerHost{h: &Hub{opts: HubOptions{ReaderNextPin: tc.fn}}}
			h, ok := rh.helloFor(context.Background(), "rid").(ReaderHello)
			if !ok || h.NextPin != tc.want {
				t.Fatalf("hello = %+v ok=%v", h, ok)
			}
			b, _ := json.Marshal(h)
			if has := strings.Contains(string(b), "next_pin"); has != (tc.want != "") {
				t.Fatalf("next_pin presence wrong in %s", b)
			}
		})
	}
}
