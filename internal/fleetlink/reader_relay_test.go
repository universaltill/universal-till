package fleetlink

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// ut-docs#6538 phase 2 (ADR-0198 amendment 2026-11-25): the relay frames an
// additional till uses to reach a phone reader through its main till. The
// goldens are Go-owned; the Swift and Kotlin readers never see these frames.

func TestReaderRelayFramesMatchGoldens(t *testing.T) {
	cases := []struct {
		file, typ string
		into      func() any
	}{
		{"reader_list_request.json", TypeReaderList, func() any { return &struct{}{} }},
		{"reader_list_reply.json", TypeReaderList, func() any { return &ReaderListReply{} }},
		{"reader_relay_request.json", TypeReaderRelay, func() any { return &ReaderRelay{} }},
		{"reader_relay_ack.json", TypeReaderRelay, func() any { return &ReaderCollectAck{} }},
		{"reader_relay_cancel.json", TypeReaderRelayCancel, func() any { return &ReaderCancel{} }},
		{"reader_relay_result.json", TypeReaderRelayResult, func() any { return &ReaderResult{} }},
		{"reader_relay_lost.json", TypeReaderRelayLost, func() any { return &ReaderRelayLost{} }},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			e := readGolden(t, tc.file)
			if e.Type != tc.typ {
				t.Fatalf("type = %q, want %q", e.Type, tc.typ)
			}
			dec := json.NewDecoder(bytes.NewReader(e.Payload))
			dec.DisallowUnknownFields()
			v := tc.into()
			if err := dec.Decode(v); err != nil {
				t.Fatalf("payload does not decode strictly into %T: %v", v, err)
			}
			back, _ := json.Marshal(v)
			var a, b any
			_ = json.Unmarshal(back, &a)
			_ = json.Unmarshal(e.Payload, &b)
			x, _ := json.Marshal(a)
			y, _ := json.Marshal(b)
			if !bytes.Equal(x, y) {
				t.Fatalf("round trip changed the payload: %s vs %s", back, e.Payload)
			}
		})
	}
	rq, err := DecodeReaderRelay(readGolden(t, "reader_relay_request.json").Payload)
	if err != nil {
		t.Fatal(err)
	}
	if rq.AmountMinor != 1000 || rq.IdempotencyKey != "att_9:1:1" || rq.ReaderRef != "rd_0a1b2c3d4e5f" {
		t.Fatalf("relay = %+v", rq)
	}
	l, err := DecodeReaderListReply(readGolden(t, "reader_list_reply.json").Payload)
	if err != nil || len(l.Readers) != 2 || l.Readers[1].State != ReaderBusy {
		t.Fatalf("list = %+v err=%v", l, err)
	}
}

func relayPayload(mut func(m map[string]any)) json.RawMessage {
	m := map[string]any{"reader_ref": "rd_0a1b2c3d4e5f", "provider": "stripe", "amount_minor": 1000, "currency": "gbp",
		"idempotency_key": "k1", "timeout_ms": 60000, "client_secret": "pi_3QexampleAAAA_secret_exampleBBBB", "location_id": "loc_abc"}
	mut(m)
	b, _ := json.Marshal(m)
	return b
}

func TestReaderRelayRejectsUnknownKeysAndBadSecretShape(t *testing.T) {
	if _, err := DecodeReaderRelay(relayPayload(func(map[string]any) {})); err != nil {
		t.Fatalf("a good frame was refused: %v", err)
	}
	bad := map[string]func(m map[string]any){
		"unknown key":     func(m map[string]any) { m["basket"] = "x" },
		"secret x":        func(m map[string]any) { m["client_secret"] = "x" },
		"secret not a PI": func(m map[string]any) { m["client_secret"] = "seti_3QexampleAAAA_secret_exampleBBBB" },
		"secret huge":     func(m map[string]any) { m["client_secret"] = "pi_3QexampleAAAA_secret_" + strings.Repeat("a", 200) },
		"amount 0":        func(m map[string]any) { m["amount_minor"] = 0 },
		"amount -1":       func(m map[string]any) { m["amount_minor"] = -1 },
		"amount too big":  func(m map[string]any) { m["amount_minor"] = MaxRelayAmountMinor + 1 },
		"key with space":  func(m map[string]any) { m["idempotency_key"] = "a b" },
		"currency":        func(m map[string]any) { m["currency"] = "pounds" },
		"ref":             func(m map[string]any) { m["reader_ref"] = "x y" },
		"location":        func(m map[string]any) { m["location_id"] = "x y" },
		"provider":        func(m map[string]any) { m["provider"] = "Stripe!" },
	}
	for name, mut := range bad {
		if _, err := DecodeReaderRelay(relayPayload(mut)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// A empty location is fine; the timeout is clamped, not refused.
	rq, err := DecodeReaderRelay(relayPayload(func(m map[string]any) { m["location_id"] = ""; m["timeout_ms"] = 1 }))
	if err != nil || rq.TimeoutMs != MinRelayTimeoutMs {
		t.Fatalf("timeout 1 -> %d err=%v, want clamp to %d", rq.TimeoutMs, err, MinRelayTimeoutMs)
	}
	rq, _ = DecodeReaderRelay(relayPayload(func(m map[string]any) { m["timeout_ms"] = 999999999 }))
	if rq.TimeoutMs != MaxRelayTimeoutMs {
		t.Fatalf("timeout big -> %d", rq.TimeoutMs)
	}
}

func TestReaderListCapsAt32(t *testing.T) {
	var rs []RelayReader
	for i := 0; i < 40; i++ {
		rs = append(rs, RelayReader{Ref: "rd_" + strings.Repeat("a", 4) + string(rune('a'+i%26)), Name: "p", Provider: "stripe", State: ReaderReady})
	}
	raw, _ := json.Marshal(ReaderListReply{Readers: rs})
	l, err := DecodeReaderListReply(raw)
	if err != nil || len(l.Readers) != MaxRelayReaders {
		t.Fatalf("len = %d err=%v, want %d", len(l.Readers), err, MaxRelayReaders)
	}
	raw, _ = json.Marshal(ReaderListReply{Readers: []RelayReader{{Ref: "rd_a", Name: "p", Provider: "stripe", State: "weird"}}})
	if l, err := DecodeReaderListReply(raw); err != nil || len(l.Readers) != 0 {
		t.Fatalf("a row with an unknown state must be dropped: %+v err=%v", l, err)
	}
}

func TestReaderRelayLostDecode(t *testing.T) {
	l, err := DecodeReaderRelayLost(readGolden(t, "reader_relay_lost.json").Payload)
	if err != nil || l.Cause != RelayCausePhoneLinkLost {
		t.Fatalf("lost = %+v err=%v", l, err)
	}
	if _, err := DecodeReaderRelayLost([]byte(`{"idempotency_key":"k","cause":"whatever"}`)); err == nil {
		t.Fatal("unknown cause accepted")
	}
}

type cancelSink struct {
	mu  sync.Mutex
	got []string
}

func (c *cancelSink) RelayCancel(tillID string, r ReaderCancel) {
	c.mu.Lock()
	c.got = append(c.got, tillID+"/"+r.IdempotencyKey)
	c.mu.Unlock()
}

func (c *cancelSink) list() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.got...)
}

func TestRelayCancelReachesTheSinkFromALiveTillOnly(t *testing.T) {
	h := NewHub(HubOptions{Config: DefaultConfig(), Hello: testHello})
	t.Cleanup(h.Close)
	sink := &cancelSink{}
	h.SetRelaySink(sink)
	conn := newFakeConn()
	go h.ServeConnSecure("t1", conn)
	for h.TillPeer("t1") == nil {
		time.Sleep(time.Millisecond)
	}
	if !h.TillPeer("t1").Secure() {
		t.Fatal("a link served as secure must say so")
	}
	conn.send(t, mustMsg(t, "c1", TypeReaderRelayCancel, "", ReaderCancel{IdempotencyKey: "k1"}))
	conn.send(t, mustMsg(t, "c2", TypeReaderRelayCancel, "", map[string]any{"idempotency_key": "bad key"}))
	deadline := time.Now().Add(2 * time.Second)
	for len(sink.list()) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if got := sink.list(); len(got) != 1 || got[0] != "t1/k1" {
		t.Fatalf("sink got %v, want exactly [t1/k1] (the malformed one is dropped)", got)
	}
	plain := newFakeConn()
	go h.ServeConn("t2", plain)
	for h.TillPeer("t2") == nil {
		time.Sleep(time.Millisecond)
	}
	if h.TillPeer("t2").Secure() {
		t.Fatal("a plain link must not be secure")
	}
}

func TestEveryRelayFrameRefusedFromAReaderBearer(t *testing.T) {
	h := NewHub(HubOptions{Config: DefaultConfig()})
	t.Cleanup(h.Close)
	h.Handle(TypeReaderRelay, func(context.Context, string, json.RawMessage) (any, error) {
		return map[string]bool{"accepted": true}, nil
	})
	h.Handle(TypeReaderList, func(context.Context, string, json.RawMessage) (any, error) { return ReaderListReply{}, nil })
	sink := &cancelSink{}
	h.SetRelaySink(sink)
	conn := newFakeConn()
	go h.ServeReaderConn("rd1", conn)
	for h.ReaderPeer("rd1") == nil {
		time.Sleep(time.Millisecond)
	}
	for i, typ := range []string{TypeReaderRelay, TypeReaderList, TypeReaderRelayCancel, TypeReaderRelayResult, TypeReaderRelayLost} {
		id := "x" + string(rune('a'+i))
		conn.send(t, mustMsg(t, id, typ, "", map[string]string{"idempotency_key": "k1"}))
		for {
			e := conn.next(t)
			if e.Type == TypeHello {
				continue
			}
			var ep ErrorPayload
			_ = json.Unmarshal(e.Payload, &ep)
			if e.Type != TypeError || e.ReplyTo != id || ep.Code != CodeUnknownType {
				t.Fatalf("%s from a reader bearer got %+v, want unknown_type", typ, e)
			}
			break
		}
	}
	if len(sink.list()) != 0 {
		t.Fatalf("a reader's relay.cancel reached the sink: %v", sink.list())
	}
}

func TestClientTakesRelayResultAndLostFrames(t *testing.T) {
	var gotR []ReaderResult
	var gotOK []bool
	var gotL []ReaderRelayLost
	c := NewClient(ClientOptions{
		OnRelayResult: func(_ *Peer, r ReaderResult, ok bool) { gotR, gotOK = append(gotR, r), append(gotOK, ok) },
		OnRelayLost:   func(_ *Peer, l ReaderRelayLost) { gotL = append(gotL, l) },
	})
	for _, typ := range []string{TypeReaderRelayResult, TypeReaderRelayLost} {
		if !c.oneWay(typ) {
			t.Fatalf("%s must be a one-way frame on the client", typ)
		}
	}
	c.gotMessage(nil, readGolden(t, "reader_relay_result.json"))
	c.gotMessage(nil, readGolden(t, "reader_relay_lost.json"))
	c.gotMessage(nil, mustMsg(t, "b", TypeReaderRelayResult, "", map[string]any{"idempotency_key": "k9", "status": "approved", "card_number": "4242"}))
	c.gotMessage(nil, mustMsg(t, "b", TypeReaderRelayLost, "", map[string]any{"idempotency_key": "k9", "cause": "nope"}))
	if len(gotR) != 2 || gotR[0].Status != ResultApproved || !gotOK[0] || gotOK[1] || gotR[1].IdempotencyKey != "k9" {
		t.Fatalf("results = %+v ok=%v (a result with an extra field is not ok but still names its key)", gotR, gotOK)
	}
	if len(gotL) != 1 || gotL[0].Cause != RelayCausePhoneLinkLost {
		t.Fatalf("lost = %+v (an unknown cause is dropped)", gotL)
	}
}

func TestHelloAdvertisesReaderRelayOnlyWhenSet(t *testing.T) {
	b, _ := json.Marshal(Hello{TillID: "m"})
	if strings.Contains(string(b), "reader_relay") {
		t.Fatalf("an older-shaped hello must not carry reader_relay: %s", b)
	}
	b, _ = json.Marshal(Hello{TillID: "m", ReaderRelay: true})
	if !strings.Contains(string(b), `"reader_relay":true`) {
		t.Fatalf("hello = %s", b)
	}
}
