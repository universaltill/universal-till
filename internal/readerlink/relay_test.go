package readerlink

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/devicepay"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#6538 phase 2 (ADR-0198 amendment 2026-11-25): the main relays an
// additional till's sale to a phone. The main creates no PaymentIntent and
// calls no broker operation for it; it authorises, bounds, serialises and
// forwards.

const testSecret = "pi_3QexampleAAAA_secret_exampleBBBB"

type relayRig struct {
	*rig
	mu        sync.Mutex
	roles     map[string]string // till id -> stored role; missing/"" = not a live joined till
	pairingOn bool
	audit     []RelayAudit
}

func newRelayRig(t *testing.T) *relayRig {
	t.Helper()
	rr := &relayRig{roles: map[string]string{}, pairingOn: true}
	rr.rig = newRigCfg(t, func(r *rig, c *Config) {
		c.Relay = &RelayConfig{
			TillRole: func(_ context.Context, tillID string) (string, bool) {
				rr.mu.Lock()
				defer rr.mu.Unlock()
				role, ok := rr.roles[tillID]
				return role, ok && role != ""
			},
			TillPeer:  func(id string) *fleetlink.Peer { return r.hub.TillPeer(id) },
			PairingOn: func(context.Context) bool { rr.mu.Lock(); defer rr.mu.Unlock(); return rr.pairingOn },
			Currency:  func(context.Context) string { return "GBP" },
			Readers: func(context.Context) []Reader {
				r.mu.Lock()
				defer r.mu.Unlock()
				var out []Reader
				for _, rd := range r.readers {
					out = append(out, rd)
				}
				return out
			},
			Audit: func(a RelayAudit) { rr.mu.Lock(); rr.audit = append(rr.audit, a); rr.mu.Unlock() },
		}
	})
	rr.hub.HandleRelay(rr.svc.ListHandler, rr.svc.RelayHandler)
	rr.hub.SetRelaySink(rr.svc)
	return rr
}

// till links a fake till as tillID with the stored role.
func (rr *relayRig) till(tillID, role string, secure bool) *phoneConn {
	rr.t.Helper()
	rr.mu.Lock()
	rr.roles[tillID] = role
	rr.mu.Unlock()
	c := newPhone()
	if secure {
		go rr.hub.ServeConnSecure(tillID, c)
	} else {
		go rr.hub.ServeConn(tillID, c)
	}
	deadline := time.Now().Add(2 * time.Second)
	for rr.hub.TillPeer(tillID) == nil {
		if time.Now().After(deadline) {
			rr.t.Fatal("till never linked")
		}
		time.Sleep(2 * time.Millisecond)
	}
	return c
}

// request sends a request frame and returns its id.
func (c *phoneConn) request(t *testing.T, typ string, payload any) string {
	t.Helper()
	c.send(t, typ, "", payload) // send assigns the id ph-<seq>
	return fmt.Sprintf("ph-%d", seq)
}

// reply returns the reply (or error) frame to request id, skipping others.
func (c *phoneConn) reply(t *testing.T, id string) fleetlink.Envelope {
	t.Helper()
	for {
		e := c.recv(t)
		if e.ReplyTo == id {
			return e
		}
	}
}

func errCode(t *testing.T, e fleetlink.Envelope) string {
	t.Helper()
	if e.Type != fleetlink.TypeError {
		return ""
	}
	return decode[fleetlink.ErrorPayload](t, e.Payload).Code
}

func relayReq(key string, minor int64) map[string]any {
	return map[string]any{"reader_ref": "rd_a", "provider": "stripe", "amount_minor": minor, "currency": "gbp",
		"idempotency_key": key, "timeout_ms": 60000, "client_secret": testSecret, "location_id": "tml_main"}
}

func ackCollect(t *testing.T, phone *phoneConn) fleetlink.ReaderCollect {
	t.Helper()
	e := phone.recv(t)
	if e.Type != fleetlink.TypeReaderCollect {
		t.Fatalf("phone got %q, want reader.collect", e.Type)
	}
	phone.send(t, fleetlink.TypeReaderCollect, e.ID, fleetlink.ReaderCollectAck{Accepted: true})
	return decode[fleetlink.ReaderCollect](t, e.Payload)
}

func (rr *relayRig) lastAudit() RelayAudit {
	rr.mu.Lock()
	defer rr.mu.Unlock()
	if len(rr.audit) == 0 {
		return RelayAudit{}
	}
	return rr.audit[len(rr.audit)-1]
}

func waitAudit(t *testing.T, rr *relayRig, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rr.mu.Lock()
		got := len(rr.audit)
		rr.mu.Unlock()
		if got >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("audit rows < %d", n)
}

func TestRelayForwardsCollectAndResultToTheCallingTill(t *testing.T) {
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	till := rr.till("t1", "additional", true)

	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 1000))
	col := ackCollect(t, phone)
	if col.AmountMinor != 1000 || col.Currency != "gbp" || col.IdempotencyKey != "k1" || col.ClientSecret != testSecret ||
		col.ReaderID != "rd_a" || col.Provider != "stripe" || col.LocationID != "tml_main" {
		t.Fatalf("collect frame = %+v", col)
	}
	rep := till.reply(t, id)
	if ack := decode[fleetlink.ReaderCollectAck](t, rep.Payload); rep.Type != fleetlink.TypeReaderRelay || !ack.Accepted {
		t.Fatalf("reply = %+v", rep)
	}
	phone.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: "k1", Status: fleetlink.ResultApproved})
	e := till.recv(t)
	if r := decode[fleetlink.ReaderResult](t, e.Payload); e.Type != fleetlink.TypeReaderRelayResult || r.Status != fleetlink.ResultApproved || r.IdempotencyKey != "k1" {
		t.Fatalf("till got %+v", e)
	}
	if n := len(rr.broker.calls); n != 0 {
		t.Fatalf("the main made %d broker calls for a relay, want 0 (it creates no PaymentIntent)", n)
	}
	// The lock is free again once the verdict went through.
	waitAudit(t, rr, 1)
	id = till.request(t, fleetlink.TypeReaderRelay, relayReq("k2", 500))
	ackCollect(t, phone)
	if rep := till.reply(t, id); errCode(t, rep) != "" {
		t.Fatalf("second relay after a finished one refused: %+v", rep)
	}
}

func TestRelayRefusedForSatelliteKioskUnknownAndRevokedTill(t *testing.T) {
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	for _, role := range []string{"satellite", "kiosk", "main", "unknown_role"} {
		till := rr.till("t-"+role, role, true)
		id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k-"+role, 1000))
		if code := errCode(t, till.reply(t, id)); code != fleetlink.CodeRelayForbidden {
			t.Errorf("role %q: code = %q, want forbidden", role, code)
		}
	}
	// Revoked / unknown: the role lookup says no.
	till := rr.till("t-gone", "", true)
	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k-gone", 1000))
	if code := errCode(t, till.reply(t, id)); code != fleetlink.CodeRelayForbidden {
		t.Errorf("unknown till: code = %q, want forbidden", code)
	}
	id = till.request(t, fleetlink.TypeReaderList, struct{}{})
	if code := errCode(t, till.reply(t, id)); code != fleetlink.CodeRelayForbidden {
		t.Errorf("reader.list from an unknown till: code = %q, want forbidden", code)
	}
	if !phone.quiet(150 * time.Millisecond) {
		t.Fatal("the phone was sent something for a refused till")
	}
}

func TestRelayRefusedOnPlainHTTPLink(t *testing.T) {
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	till := rr.till("t1", "additional", false)
	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 1000))
	if code := errCode(t, till.reply(t, id)); code != fleetlink.CodeRelayForbidden {
		t.Fatalf("code = %q, want forbidden (the frame carries a PaymentIntent secret)", code)
	}
	if !phone.quiet(150 * time.Millisecond) {
		t.Fatal("phone got a frame over a plain link")
	}
}

func TestRelayBoundsTable(t *testing.T) {
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	till := rr.till("t1", "additional", true)
	set := func(k string, v any) map[string]any { m := relayReq("kb", 1000); m[k] = v; return m }
	bad := map[string]map[string]any{
		"amount 0":         set("amount_minor", 0),
		"amount -1":        set("amount_minor", -1),
		"amount 10000001":  set("amount_minor", 10_000_001),
		"currency EUR":     set("currency", "EUR"),
		"key with space":   set("idempotency_key", "a b"),
		"secret x":         set("client_secret", "x"),
		"secret oversize":  set("client_secret", "pi_3QexampleAAAA_secret_"+strings.Repeat("a", 300)),
		"unknown key":      set("basket", "x"),
		"unknown provider": set("provider", "sumup"),
	}
	for name, m := range bad {
		id := till.request(t, fleetlink.TypeReaderRelay, m)
		if code := errCode(t, till.reply(t, id)); code != fleetlink.CodeRelayBadRequest {
			t.Errorf("%s: code = %q, want bad_request", name, code)
		}
	}
	if !phone.quiet(150 * time.Millisecond) {
		t.Fatal("the phone was touched by a bad request")
	}
	m := relayReq("kt", 1000)
	m["timeout_ms"] = 1
	id := till.request(t, fleetlink.TypeReaderRelay, m)
	if col := ackCollect(t, phone); col.TimeoutMs != 5000 {
		t.Fatalf("timeout 1 ms reached the phone as %d, want the 5000 clamp", col.TimeoutMs)
	}
	till.reply(t, id)
}

func TestMacAndAdditionalRaceForOnePhoneSecondGetsBusy(t *testing.T) {
	t.Run("Mac first, additional second", func(t *testing.T) {
		rr := newRelayRig(t)
		phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
		till := rr.till("t1", "additional", true)
		rr.attempt(key, 1000)
		ctx, cancel := ctxFor(5 * time.Second)
		defer cancel()
		mac := rr.collect(ctx, "rd_a", req(key, 1000))
		ackCollect(t, phone)
		waitPhase(t, rr.svc, PhaseOnPhone)
		id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 700))
		if code := errCode(t, till.reply(t, id)); code != fleetlink.CodeReaderBusy {
			t.Fatalf("additional got %q, want busy", code)
		}
		if !phone.quiet(150 * time.Millisecond) {
			t.Fatal("a second reader.collect reached the phone")
		}
		cancel()
		await(t, mac)
	})
	t.Run("additional first, Mac second", func(t *testing.T) {
		rr := newRelayRig(t)
		phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
		till := rr.till("t1", "additional", true)
		id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 700))
		ackCollect(t, phone)
		if e := till.reply(t, id); errCode(t, e) != "" {
			t.Fatalf("relay refused: %+v", e)
		}
		rr.attempt(key, 1000)
		ctx, cancel := ctxFor(3 * time.Second)
		defer cancel()
		res := await(t, rr.collect(ctx, "rd_a", req(key, 1000)))
		if res.Status != devicepay.StatusDeclined || res.Reason != devicepay.ReasonReaderBusy {
			t.Fatalf("Mac result = %+v, want a not-started reader_busy", res)
		}
		if rr.broker.count("session_create") != 0 {
			t.Fatal("the Mac created a PaymentIntent for a busy reader")
		}
		if !phone.quiet(150 * time.Millisecond) {
			t.Fatal("a second reader.collect reached the phone")
		}
	})
}

func TestSecondRelayFromTheSameTillWhileOneIsInFlightIsBusy(t *testing.T) {
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	rr.link("rd2", "rd_b", fleetlink.ReaderReady)
	till := rr.till("t1", "additional", true)
	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 700))
	ackCollect(t, phone)
	till.reply(t, id)
	m := relayReq("k2", 700)
	m["reader_ref"] = "rd_b"
	id = till.request(t, fleetlink.TypeReaderRelay, m)
	if code := errCode(t, till.reply(t, id)); code != fleetlink.CodeReaderBusy {
		t.Fatalf("code = %q, want busy (1 in flight per till)", code)
	}
}

func TestRelayResultOnlyFromTheCollectLinkAndOnce(t *testing.T) {
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	rogue := rr.link("rd2", "rd_b", fleetlink.ReaderReady)
	till := rr.till("t1", "additional", true)
	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 1000))
	ackCollect(t, phone)
	till.reply(t, id)

	rogue.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: "k1", Status: fleetlink.ResultApproved})
	deadline := time.Now().Add(2 * time.Second)
	for rr.svc.Dropped() < 1 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if rr.svc.Dropped() != 1 {
		t.Fatalf("Dropped() = %d after a rogue phone's result, want 1", rr.svc.Dropped())
	}
	phone.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: "k1", Status: fleetlink.ResultDeclined, DeclineCode: "card_declined"})
	if e := till.recv(t); e.Type != fleetlink.TypeReaderRelayResult || decode[fleetlink.ReaderResult](t, e.Payload).Status != fleetlink.ResultDeclined {
		t.Fatalf("till got %+v", e)
	}
	phone.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: "k1", Status: fleetlink.ResultApproved})
	for rr.svc.Dropped() < 2 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	if rr.svc.Dropped() != 2 || !till.quiet(150*time.Millisecond) {
		t.Fatalf("a replay was forwarded or not counted: dropped=%d", rr.svc.Dropped())
	}
}

func TestTillLinkDropMidCollectCancelsPhoneAndFreesLock(t *testing.T) {
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	till := rr.till("t1", "additional", true)
	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 1000))
	ackCollect(t, phone)
	till.reply(t, id)
	till.Close(0, "till gone")
	e := phone.recv(t)
	if e.Type != fleetlink.TypeReaderCancel || decode[fleetlink.ReaderCancel](t, e.Payload).IdempotencyKey != "k1" {
		t.Fatalf("phone got %+v, want reader.cancel k1", e)
	}
	// A second till can use the phone at once: the lock is free.
	till2 := rr.till("t2", "additional", true)
	deadline := time.Now().Add(2 * time.Second)
	for {
		id = till2.request(t, fleetlink.TypeReaderRelay, relayReq("k2", 300))
		rep := till2.reply(t, id)
		if errCode(t, rep) == "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lock never freed after the till dropped: %q", errCode(t, rep))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPhoneLinkDropMidCollectSendsRelayLostAndNeverDeclines(t *testing.T) {
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	till := rr.till("t1", "additional", true)
	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 1000))
	ackCollect(t, phone)
	till.reply(t, id)
	phone.Close(0, "phone gone")
	e := till.recv(t)
	if l := decode[fleetlink.ReaderRelayLost](t, e.Payload); e.Type != fleetlink.TypeReaderRelayLost || l.IdempotencyKey != "k1" || l.Cause != fleetlink.RelayCausePhoneLinkLost {
		t.Fatalf("till got %+v", e)
	}
	if !till.quiet(150 * time.Millisecond) {
		t.Fatal("more than one frame: the main must not invent a verdict")
	}
	waitAudit(t, rr, 1)
	if got := rr.lastAudit().Outcome; got != "phone_link_lost" {
		t.Fatalf("audit outcome = %q", got)
	}
}

func TestRevokeMidRelaySendsRelayLostReaderRevoked(t *testing.T) {
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	till := rr.till("t1", "additional", true)
	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 1000))
	ackCollect(t, phone)
	till.reply(t, id)
	rr.hub.DisconnectReader("rd1")
	e := till.recv(t)
	if l := decode[fleetlink.ReaderRelayLost](t, e.Payload); e.Type != fleetlink.TypeReaderRelayLost || l.Cause != fleetlink.RelayCauseReaderRevoked {
		t.Fatalf("till got %+v", e)
	}
}

func TestRelayCancelFromTheTillIsForwardedToThePhone(t *testing.T) {
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	till := rr.till("t1", "additional", true)
	other := rr.till("t2", "additional", true)
	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 1000))
	ackCollect(t, phone)
	till.reply(t, id)
	other.send(t, fleetlink.TypeReaderRelayCancel, "", fleetlink.ReaderCancel{IdempotencyKey: "k1"})
	if !phone.quiet(150 * time.Millisecond) {
		t.Fatal("another till cancelled this till's collect")
	}
	till.send(t, fleetlink.TypeReaderRelayCancel, "", fleetlink.ReaderCancel{IdempotencyKey: "k1"})
	if e := phone.recv(t); e.Type != fleetlink.TypeReaderCancel {
		t.Fatalf("phone got %+v, want reader.cancel", e)
	}
}

func TestRelayRateLimit10PerMinutePerTill(t *testing.T) {
	rr := newRelayRig(t)
	till := rr.till("t1", "additional", true)
	for i := 0; i < 10; i++ {
		m := relayReq(fmt.Sprintf("k%d", i), 100)
		m["reader_ref"] = "rd_zzz" // unknown: refused, but each decoded attempt counts
		id := till.request(t, fleetlink.TypeReaderRelay, m)
		if code := errCode(t, till.reply(t, id)); code != fleetlink.CodeRelayUnknownReader {
			t.Fatalf("attempt %d: code = %q", i, code)
		}
	}
	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k11", 100))
	if code := errCode(t, till.reply(t, id)); code != fleetlink.CodeReaderRateLimited {
		t.Fatalf("11th: code = %q, want rate_limited", code)
	}
	other := rr.till("t2", "additional", true)
	id = other.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 100))
	if code := errCode(t, other.reply(t, id)); code == fleetlink.CodeReaderRateLimited {
		t.Fatal("one till's burst rate-limited another")
	}
}

func TestRelayAuditRowHasNoSecret(t *testing.T) {
	var logs bytes.Buffer
	defer logging.CaptureForTest(&logs)()
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	till := rr.till("t1", "additional", true)
	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 1000))
	ackCollect(t, phone)
	till.reply(t, id)
	phone.send(t, fleetlink.TypeReaderResult, "", fleetlink.ReaderResult{IdempotencyKey: "k1", Status: fleetlink.ResultApproved})
	till.recv(t)
	waitAudit(t, rr, 1)
	row := fmt.Sprintf("%+v", rr.lastAudit())
	a := rr.lastAudit()
	if a.TillID != "t1" || a.ReaderID != "rd1" || a.Key != "k1" || a.AmountMinor != 1000 || a.Currency != "gbp" || a.Outcome != "approved" {
		t.Fatalf("audit = %+v", a)
	}
	if strings.Contains(row, testSecret) || strings.Contains(row, "secret") {
		t.Fatalf("the audit row carries the secret: %s", row)
	}
	if strings.Contains(logs.String(), testSecret) {
		t.Fatalf("the secret is in the logs: %s", logs.String())
	}
	rr.mu.Lock()
	n := len(rr.audit)
	rr.mu.Unlock()
	if n != 1 {
		t.Fatalf("%d audit rows for one relay, want 1", n)
	}
}

func TestRelayNotAdvertisedWhenPhonePairingOff(t *testing.T) {
	rr := newRelayRig(t)
	phone := rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	till := rr.till("t1", "additional", true)
	rr.mu.Lock()
	rr.pairingOn = false
	rr.mu.Unlock()
	if rr.svc.RelayOffered(context.Background()) {
		t.Fatal("RelayOffered must follow phone_pairing")
	}
	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 1000))
	if code := errCode(t, till.reply(t, id)); code != fleetlink.CodeReaderNotReady {
		t.Fatalf("code = %q, want not_ready", code)
	}
	if !phone.quiet(150 * time.Millisecond) {
		t.Fatal("phone got a frame while phone_pairing is off")
	}
}

func TestRelayNeedsAReadyPhone(t *testing.T) {
	rr := newRelayRig(t)
	rr.link("rd1", "rd_a", fleetlink.ReaderNeedsSetup)
	till := rr.till("t1", "additional", true)
	id := till.request(t, fleetlink.TypeReaderRelay, relayReq("k1", 1000))
	if code := errCode(t, till.reply(t, id)); code != fleetlink.CodeReaderNotReady {
		t.Fatalf("needs_setup: code = %q", code)
	}
	rr.link("rd3", "rd_c", fleetlink.ReaderBusy)
	m := relayReq("k2", 1000)
	m["reader_ref"] = "rd_c"
	id = till.request(t, fleetlink.TypeReaderRelay, m)
	if code := errCode(t, till.reply(t, id)); code != fleetlink.CodeReaderBusy {
		t.Fatalf("busy: code = %q", code)
	}
}

func TestReaderListReturnsPairedPhonesWithStates(t *testing.T) {
	rr := newRelayRig(t)
	rr.link("rd1", "rd_a", fleetlink.ReaderReady)
	rr.link("rd2", "rd_b", fleetlink.ReaderBusy)
	rr.addReader("rd3", "rd_c") // paired, no link
	till := rr.till("t1", "additional", true)
	id := till.request(t, fleetlink.TypeReaderList, struct{}{})
	rep := till.reply(t, id)
	got := map[string]string{}
	for _, r := range decode[fleetlink.ReaderListReply](t, rep.Payload).Readers {
		got[r.Ref] = r.State
	}
	want := map[string]string{"rd_a": "ready", "rd_b": "busy", "rd_c": "unavailable"}
	if len(got) != 3 || got["rd_a"] != want["rd_a"] || got["rd_b"] != want["rd_b"] || got["rd_c"] != want["rd_c"] {
		t.Fatalf("list = %v, want %v", got, want)
	}
	if strings.Contains(string(rep.Payload), `"rd1"`) {
		t.Fatalf("the list leaks a registry id: %s", rep.Payload)
	}
}
