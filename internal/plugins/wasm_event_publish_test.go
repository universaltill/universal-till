package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
)

// ADR-0121 §3 event_publish (ut-docs#3871).

const (
	publishTestPlugin = "com.test.pub"
	publishTestSub    = "com.test.sub"
	publishTypeOff    = 0
	publishPayloadOff = 1024
)

// publishTestMemory is a guest stand-in: an instantiated module exporting
// three pages (192 KiB) of linear memory — room for a type and a payload
// just over the 64 KiB cap.
func publishTestMemory(t *testing.T) api.Module {
	t.Helper()
	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	t.Cleanup(func() { _ = rt.Close(ctx) })
	// (module (memory (export "memory") 3))
	mod, err := rt.Instantiate(ctx, []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x05, 0x03, 0x01, 0x00, 0x03,
		0x07, 0x0a, 0x01, 0x06, 'm', 'e', 'm', 'o', 'r', 'y', 0x02, 0x00,
	})
	if err != nil {
		t.Fatalf("instantiate memory module: %v", err)
	}
	return mod
}

// fakeClock is an injectable clock for the publish rate limiter.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func newPublishTestState(t *testing.T, d *sql.DB, clock *fakeClock) *hostState {
	t.Helper()
	if clock == nil {
		clock = &fakeClock{now: time.Unix(1_700_000_000, 0)}
	}
	return &hostState{pluginID: publishTestPlugin, db: d, publishRate: newPublishRateLimiter(clock.Now)}
}

func callPublish(t *testing.T, m api.Module, hs *hostState, typ string, payload []byte) int32 {
	t.Helper()
	if !m.Memory().Write(publishTypeOff, []byte(typ)) {
		t.Fatal("write type")
	}
	if len(payload) > 0 && !m.Memory().Write(publishPayloadOff, payload) {
		t.Fatal("write payload")
	}
	ctx := withHostState(context.Background(), hs)
	return hostEventPublish(ctx, m, publishTypeOff, uint32(len(typ)), publishPayloadOff, uint32(len(payload)))
}

// subscribeTestPlugin seeds a subscriber plugin holding events:receive and an
// active hook for each event type, and subscribes it on bus (channel only).
func subscribeTestPlugin(t *testing.T, d *sql.DB, bus *EventBus, pluginID string, events ...string) <-chan Event {
	t.Helper()
	seedPlugin(t, d, pluginID)
	grantPerm(t, d, pluginID, "events:receive")
	for i, ev := range events {
		if _, err := d.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES (?, ?, ?, 'guest.handle', 1)`,
			pluginID+"-h"+string(rune('a'+i)), pluginID, ev); err != nil {
			t.Fatalf("seed hook: %v", err)
		}
	}
	ch, err := bus.Subscribe(context.Background(), pluginID, events)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return ch
}

func TestEventPublishRefusesOutsideOwnNamespace(t *testing.T) {
	d := hostfnTestDB(t)
	m := publishTestMemory(t)
	hs := newPublishTestState(t, d, nil)
	for _, typ := range []string{
		"com.test.other.tick", // another plugin's namespace
		"sale.x",              // core namespace
		"sale.completed",      // a real core event
		"com.test.pubx.tick",  // own id without the dot boundary
		"com.test.pub",        // the bare id, no event name
		"com.test.pub.",       // empty name segment
		"COM.TEST.PUB.TICK",   // not lower-case
		"",                    // empty
	} {
		if got := callPublish(t, m, hs, typ, []byte(`{}`)); got != hostErrDenied {
			t.Errorf("publish %q = %d, want %d (denied)", typ, got, hostErrDenied)
		}
	}
	if len(hs.published) != 0 {
		t.Fatalf("refused publishes were queued: %+v", hs.published)
	}
	if got := callPublish(t, m, hs, "com.test.pub.tick", []byte(`{}`)); got != 0 {
		t.Fatalf("own-namespace publish = %d, want 0", got)
	}
}

// A plugin id whose namespace could reach a core root can't exist
// (validatePluginID); the core-root guard is the second, clearer check.
func TestEventPublishRefusesCoreRootEvenForMatchingID(t *testing.T) {
	d := hostfnTestDB(t)
	m := publishTestMemory(t)
	hs := newPublishTestState(t, d, nil)
	hs.pluginID = "sale"
	if got := callPublish(t, m, hs, "sale.completed", []byte(`{}`)); got != hostErrDenied {
		t.Fatalf("core-root publish = %d, want %d", got, hostErrDenied)
	}
}

func TestEventPublishPayloadCapAndJSON(t *testing.T) {
	d := hostfnTestDB(t)
	m := publishTestMemory(t)
	hs := newPublishTestState(t, d, nil)

	jsonString := func(n int) []byte { // a JSON string literal exactly n bytes long
		return []byte(`"` + strings.Repeat("a", n-2) + `"`)
	}
	if got := callPublish(t, m, hs, "com.test.pub.big", jsonString(65536)); got != 0 {
		t.Fatalf("65536-byte payload = %d, want 0", got)
	}
	if got := callPublish(t, m, hs, "com.test.pub.big", jsonString(65537)); got != hostErrInvalid {
		t.Fatalf("65537-byte payload = %d, want %d", got, hostErrInvalid)
	}
	if got := callPublish(t, m, hs, "com.test.pub.bad", []byte(`{"a":`)); got != hostErrInvalid {
		t.Fatalf("invalid JSON payload = %d, want %d", got, hostErrInvalid)
	}
	if got := callPublish(t, m, hs, "com.test.pub.empty", nil); got != 0 {
		t.Fatalf("empty payload = %d, want 0", got)
	}
	if len(hs.published) != 2 {
		t.Fatalf("queued %d events, want 2", len(hs.published))
	}
	if len(hs.published[0].Payload) != 65536 {
		t.Fatalf("queued payload is %d bytes, want 65536", len(hs.published[0].Payload))
	}
	if string(hs.published[1].Payload) != "null" {
		t.Fatalf("empty payload queued as %q, want null", hs.published[1].Payload)
	}
}

// The queued payload is a copy: the guest reusing its buffer after the call
// must not change what subscribers get.
func TestEventPublishCopiesPayloadOutOfGuestMemory(t *testing.T) {
	d := hostfnTestDB(t)
	m := publishTestMemory(t)
	hs := newPublishTestState(t, d, nil)
	if got := callPublish(t, m, hs, "com.test.pub.tick", []byte(`{"n":1}`)); got != 0 {
		t.Fatalf("publish = %d", got)
	}
	m.Memory().Write(publishPayloadOff, []byte(`{"n":9}`))
	if string(hs.published[0].Payload) != `{"n":1}` {
		t.Fatalf("queued payload aliased guest memory: %s", hs.published[0].Payload)
	}
}

func TestEventPublishRateLimit(t *testing.T) {
	d := hostfnTestDB(t)
	m := publishTestMemory(t)
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	limiter := newPublishRateLimiter(clock.Now)
	state := func(id string) *hostState {
		return &hostState{pluginID: id, db: d, publishRate: limiter}
	}

	// A burst of 40 is accepted, the 41st is over quota — across instances,
	// since the bucket is per plugin, not per event.
	first, second := state(publishTestPlugin), state(publishTestPlugin)
	for i := 0; i < 40; i++ {
		hs := first
		if i%2 == 1 {
			hs = second
		}
		if got := callPublish(t, m, hs, "com.test.pub.tick", nil); got != 0 {
			t.Fatalf("burst publish %d = %d, want 0", i+1, got)
		}
	}
	if got := callPublish(t, m, second, "com.test.pub.tick", nil); got != hostErrQuota {
		t.Fatalf("41st publish = %d, want %d (quota)", got, hostErrQuota)
	}
	// Another plugin has its own bucket.
	if got := callPublish(t, m, state("com.test.other"), "com.test.other.tick", nil); got != 0 {
		t.Fatalf("other plugin's publish = %d, want 0", got)
	}

	// 20/s refill: 50 ms buys one token, not two.
	clock.Advance(50 * time.Millisecond)
	hs := state(publishTestPlugin)
	if got := callPublish(t, m, hs, "com.test.pub.tick", nil); got != 0 {
		t.Fatalf("publish after 50ms refill = %d, want 0", got)
	}
	if got := callPublish(t, m, hs, "com.test.pub.tick", nil); got != hostErrQuota {
		t.Fatalf("second publish after 50ms refill = %d, want %d", got, hostErrQuota)
	}

	// A long idle refills to the burst, never past it.
	clock.Advance(time.Hour)
	hs = state(publishTestPlugin)
	for i := 0; i < 40; i++ {
		if got := callPublish(t, m, hs, "com.test.pub.tick", nil); got != 0 {
			t.Fatalf("refilled publish %d = %d, want 0", i+1, got)
		}
	}
	if got := callPublish(t, m, hs, "com.test.pub.tick", nil); got != hostErrQuota {
		t.Fatalf("publish past the refilled burst = %d, want %d", got, hostErrQuota)
	}
}

func TestEventPublishDepthLimit(t *testing.T) {
	d := hostfnTestDB(t)
	m := publishTestMemory(t)

	// Handling a hop-2 event: the publish is accepted at hop 3.
	hs := newPublishTestState(t, d, nil)
	hs.hop = 2
	if got := callPublish(t, m, hs, "com.test.pub.tick", []byte(`{"secret":"do-not-audit"}`)); got != 0 {
		t.Fatalf("publish at hop 2 = %d, want 0", got)
	}
	if len(hs.published) != 1 || hs.published[0].Hop != 3 {
		t.Fatalf("published = %+v, want one event at hop 3", hs.published)
	}

	// Handling a hop-3 event: dropped, audited, over quota.
	hs = newPublishTestState(t, d, nil)
	hs.hop = 3
	if got := callPublish(t, m, hs, "com.test.pub.tick", []byte(`{"secret":"do-not-audit"}`)); got != hostErrQuota {
		t.Fatalf("publish at hop 3 = %d, want %d (dropped)", got, hostErrQuota)
	}
	if len(hs.published) != 0 {
		t.Fatalf("dropped publish was queued: %+v", hs.published)
	}
	var n int
	var details string
	if err := d.QueryRow(`SELECT COUNT(*), COALESCE(MAX(data_json), '') FROM audit_log WHERE action = 'event_publish_dropped' AND entity_type = 'event'`).Scan(&n, &details); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if n != 1 {
		t.Fatalf("event_publish_dropped audit rows = %d, want 1", n)
	}
	for _, want := range []string{publishTestPlugin, "com.test.pub.tick", `"depth":3`} {
		if !strings.Contains(details, want) {
			t.Errorf("audit details %s do not name %q", details, want)
		}
	}
	if strings.Contains(details, "do-not-audit") {
		t.Fatalf("audit details leak the payload: %s", details)
	}
}

// The host call never delivers: nothing reaches a subscriber until the
// runtime flushes the instance's buffer, and the flush only ever enqueues —
// even for an event type a plugin's hook made Blocking.
func TestEventPublishIsQueuedUntilFlush(t *testing.T) {
	d := hostfnTestDB(t)
	m := publishTestMemory(t)
	bus := NewEventBus(d)
	ch := subscribeTestPlugin(t, d, bus, publishTestSub, "com.test.pub.tick")

	hs := newPublishTestState(t, d, nil)
	hs.hop = 1
	if got := callPublish(t, m, hs, "com.test.pub.tick", []byte(`{"n":1}`)); got != 0 {
		t.Fatalf("publish = %d, want 0", got)
	}
	select {
	case ev := <-ch:
		t.Fatalf("subscriber got %+v while the publishing call was still the caller's", ev)
	default:
	}

	w := NewWasmRuntime(t.TempDir())
	t.Cleanup(func() { _ = w.rt.Close(context.Background()) })
	w.bus = bus
	w.flushPublished(context.Background(), d, hs)
	select {
	case ev := <-ch:
		if ev.Type != "com.test.pub.tick" || ev.Hop != 2 || string(ev.Payload) != `{"n":1}` || ev.ID == "" {
			t.Fatalf("delivered %+v, want com.test.pub.tick at hop 2 with the payload", ev)
		}
	default:
		t.Fatal("flush did not enqueue the published event")
	}
	if len(hs.published) != 0 {
		t.Fatal("flush left the buffer populated")
	}
}

func TestEnqueuePublishedNeverRunsBlockingHandler(t *testing.T) {
	d := hostfnTestDB(t)
	bus := NewEventBus(d)
	const typ = "com.test.pub.thing.ask"
	seedPlugin(t, d, publishTestSub)
	grantPerm(t, d, publishTestSub, "events:receive")
	if _, err := d.Exec(`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active) VALUES ('bh1', ?, ?, 'guest.handle', 1)`, publishTestSub, typ); err != nil {
		t.Fatal(err)
	}
	bus.SetEventMode(typ, Blocking)
	var called bool
	ch, err := bus.SubscribeWithHandler(context.Background(), publishTestSub, []string{typ}, func(context.Context, Event) (json.RawMessage, error) {
		called = true
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	bus.EnqueuePublished(context.Background(), publishTestPlugin, Event{ID: "e1", Type: typ, Payload: []byte("null"), Hop: 1})
	if called {
		t.Fatal("EnqueuePublished ran a Blocking handler inline")
	}
	select {
	case ev := <-ch:
		if ev.Hop != 1 {
			t.Fatalf("hop = %d, want 1", ev.Hop)
		}
	default:
		t.Fatal("event not enqueued")
	}
}

// A subscriber without events:receive gets nothing (same rule as Publish).
func TestEnqueuePublishedChecksEventsReceive(t *testing.T) {
	d := hostfnTestDB(t)
	bus := NewEventBus(d)
	ch := subscribeTestPlugin(t, d, bus, publishTestSub, "com.test.pub.tick")
	if err := RevokePermission(context.Background(), d, publishTestSub, "events:receive"); err != nil {
		t.Fatal(err)
	}
	bus.EnqueuePublished(context.Background(), publishTestPlugin, Event{ID: "e1", Type: "com.test.pub.tick", Payload: []byte("null"), Hop: 1})
	select {
	case ev := <-ch:
		t.Fatalf("denied subscriber got %+v", ev)
	default:
	}
}

// End to end with a real guest: the call returns 0, and the event reaches
// the subscriber only once the instance has exited — also when the guest
// then fails (the host already accepted the publish).
func TestEventPublishDeliveredAfterInstanceExits(t *testing.T) {
	guest := buildHostfnGuest(t)
	for _, mode := range []string{"publish", "publish_fail"} {
		t.Run(mode, func(t *testing.T) {
			d := hostfnTestDB(t)
			bus := NewEventBus(d)
			ch := subscribeTestPlugin(t, d, bus, publishTestSub, "com.test.pub.tick")
			seedPlugin(t, d, publishTestPlugin)

			w := NewWasmRuntime(t.TempDir())
			t.Cleanup(func() { _ = w.rt.Close(context.Background()) })
			w.bus = bus
			if err := w.load(publishTestPlugin, "1.0.0", guest); err != nil {
				t.Fatal(err)
			}
			w.mu.Lock()
			w.db = d
			w.mu.Unlock()
			var seen *hostState
			onHostState = func(hs *hostState) { seen = hs }
			t.Cleanup(func() { onHostState = nil })

			payload, _ := json.Marshal(map[string]any{
				"mode": mode, "publish_type": "com.test.pub.tick", "publish_payload": map[string]int{"n": 7},
			})
			out, err := w.HandleEvent(context.Background(), publishTestPlugin, Event{ID: "core-1", Type: "test.event", Payload: payload})
			if mode == "publish" {
				if err != nil {
					t.Fatalf("HandleEvent: %v", err)
				}
				if !strings.Contains(string(out), `"publish_code":0`) {
					t.Fatalf("guest output %s, want publish_code 0", out)
				}
			} else if err == nil {
				t.Fatal("publish_fail guest succeeded; want its exit error")
			}
			if seen == nil || seen.hop != 0 {
				t.Fatalf("core event's hostState hop = %+v, want 0", seen)
			}
			select {
			case ev := <-ch:
				if ev.Type != "com.test.pub.tick" || ev.Hop != 1 || string(ev.Payload) != `{"n":7}` {
					t.Fatalf("delivered %+v, want com.test.pub.tick hop 1 {\"n\":7}", ev)
				}
			default:
				t.Fatal("published event not delivered after the instance exited")
			}
		})
	}
}

// A published event is ordinary work: never the reserved sale-path slot,
// never the payment-gate/export/import deadline floors.
func TestPublishedEventNeverSalePath(t *testing.T) {
	if !isSalePathCall(Event{Type: "com.test.pub.thing.ask"}) {
		t.Fatal("a hop-0 .ask event should be sale-path")
	}
	for _, ev := range []Event{
		{Type: "com.test.pub.thing.ask", Hop: 1},
		{Type: "com.test.pub.thing.authorize", Hop: 2},
		{Type: "com.test.pub.fiscal.sign.x", Hop: 3},
	} {
		if isSalePathCall(ev) {
			t.Errorf("published %+v treated as sale-path", ev)
		}
	}

	w := NewWasmRuntime(t.TempDir())
	t.Cleanup(func() { _ = w.rt.Close(context.Background()) })
	w.mu.Lock()
	w.hasNet[publishTestPlugin] = true
	w.mu.Unlock()
	if got := w.timeoutForEvent(publishTestPlugin, Event{Type: "com.test.pub.x.authorize"}); got != paymentGateTimeout {
		t.Fatalf("hop-0 authorize timeout = %s, want %s", got, paymentGateTimeout)
	}
	if got := w.timeoutForEvent(publishTestPlugin, Event{Type: "com.test.pub.x.authorize", Hop: 1}); got != w.netTimeout {
		t.Fatalf("published authorize timeout = %s, want the plain net timeout %s", got, w.netTimeout)
	}

	// Through HandleEvent: with every ordinary slot taken, a hop-0 ".ask"
	// still runs on the reserved slot; the same type at hop 1 does not.
	compiled, err := w.rt.CompileModule(context.Background(), []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.modules[publishTestPlugin] = compiled
	w.mu.Unlock()
	perCap, _ := wasmConcurrencyLimits("linux")
	w.calls = newWasmCallGate(perCap, 16)
	for i := 0; i < perCap-1; i++ {
		rel, err := w.calls.acquire(context.Background(), publishTestPlugin, false)
		if err != nil {
			t.Fatal(err)
		}
		defer rel()
	}
	if _, err := w.HandleEvent(shortCtx(t), publishTestPlugin, Event{Type: "com.test.pub.thing.ask"}); err != nil {
		t.Fatalf("hop-0 sale-path event on a saturated plugin: %v", err)
	}
	if _, err := w.HandleEvent(shortCtx(t), publishTestPlugin, Event{Type: "com.test.pub.thing.ask", Hop: 1}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("published .ask on a saturated plugin: err = %v, want refused at its deadline (no reserved slot)", err)
	}
}

// A long-running instance can't buffer without bound: at most
// maxPendingPublishes events wait for its exit.
func TestEventPublishPendingCap(t *testing.T) {
	d := hostfnTestDB(t)
	m := publishTestMemory(t)
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	hs := newPublishTestState(t, d, clock)
	for i := 0; i < maxPendingPublishes; i++ {
		if i%publishBurst == 0 {
			clock.Advance(time.Hour) // keep the rate limiter out of the way
		}
		if got := callPublish(t, m, hs, "com.test.pub.tick", nil); got != 0 {
			t.Fatalf("publish %d = %d, want 0", i+1, got)
		}
	}
	clock.Advance(time.Hour)
	if got := callPublish(t, m, hs, "com.test.pub.tick", nil); got != hostErrQuota {
		t.Fatalf("publish past the pending cap = %d, want %d", got, hostErrQuota)
	}
}

// A type over 256 bytes is refused before it is read (review finding: the
// type was unbounded and is copied into audit rows and log lines).
func TestEventPublishTypeLengthCap(t *testing.T) {
	d := hostfnTestDB(t)
	m := publishTestMemory(t)
	hs := newPublishTestState(t, d, nil)

	ok := publishTestPlugin + "." + strings.Repeat("a", maxPublishTypeLen-len(publishTestPlugin)-1)
	if got := callPublish(t, m, hs, ok, nil); got != 0 {
		t.Fatalf("publish with a %d-byte type = %d, want 0", len(ok), got)
	}
	if got := callPublish(t, m, hs, ok+"a", nil); got != hostErrInvalid {
		t.Fatalf("publish with a %d-byte type = %d, want %d", len(ok)+1, got, hostErrInvalid)
	}
	if len(hs.published) != 1 {
		t.Fatalf("published = %d events, want 1", len(hs.published))
	}
}

// A denied publish still costs a token, so a guest looping on a foreign
// prefix is held to the rate like any other caller.
func TestEventPublishDeniedCostsAToken(t *testing.T) {
	d := hostfnTestDB(t)
	m := publishTestMemory(t)
	hs := newPublishTestState(t, d, nil)

	for i := 0; i < publishBurst; i++ {
		if got := callPublish(t, m, hs, "sale.x", nil); got != hostErrDenied {
			t.Fatalf("denied publish %d = %d, want %d", i+1, got, hostErrDenied)
		}
	}
	if got := callPublish(t, m, hs, "com.test.pub.tick", nil); got != hostErrQuota {
		t.Fatalf("publish after %d denied calls = %d, want %d (bucket spent)", publishBurst, got, hostErrQuota)
	}
}

// Hop-limit drops are audited once per second per plugin, not per call
// (review finding: 20 audit rows/s from one looping plugin).
func TestEventPublishDropAuditCoalesced(t *testing.T) {
	d := hostfnTestDB(t)
	m := publishTestMemory(t)
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	hs := newPublishTestState(t, d, clock)
	hs.hop = maxPublishHop

	count := func() int {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'event_publish_dropped'`).Scan(&n); err != nil {
			t.Fatalf("read audit: %v", err)
		}
		return n
	}
	for i := 0; i < 5; i++ {
		if got := callPublish(t, m, hs, "com.test.pub.tick", nil); got != hostErrQuota {
			t.Fatalf("drop %d = %d, want %d", i+1, got, hostErrQuota)
		}
	}
	if n := count(); n != 1 {
		t.Fatalf("audit rows after 5 drops within 1s = %d, want 1", n)
	}
	var details string
	if err := d.QueryRow(`SELECT data_json FROM audit_log WHERE action = 'event_publish_dropped'`).Scan(&details); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	if !strings.Contains(details, "coalesced") {
		t.Errorf("audit row %s does not say later drops are coalesced", details)
	}
	clock.Advance(publishDropAuditInterval)
	if got := callPublish(t, m, hs, "com.test.pub.tick", nil); got != hostErrQuota {
		t.Fatalf("drop after the window = %d, want %d", got, hostErrQuota)
	}
	if n := count(); n != 2 {
		t.Fatalf("audit rows after the window = %d, want 2", n)
	}
}

// auditCount counts audit_log rows by action whose details contain every
// substring in like.
func auditCount(t *testing.T, d *sql.DB, action string, like ...string) int {
	t.Helper()
	q := `SELECT COUNT(*) FROM audit_log WHERE action = ?`
	args := []any{action}
	for _, l := range like {
		q += ` AND data_json LIKE ?`
		args = append(args, "%"+l+"%")
	}
	var n int
	if err := d.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("read audit: %v", err)
	}
	return n
}

func auditDetails(t *testing.T, d *sql.DB, action string) []string {
	t.Helper()
	rows, err := d.Query(`SELECT data_json FROM audit_log WHERE action = ? ORDER BY rowid`, action)
	if err != nil {
		t.Fatalf("read audit: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func publishN(bus *EventBus, typ string, n int) {
	for i := 0; i < n; i++ {
		bus.EnqueuePublished(context.Background(), publishTestPlugin, Event{ID: "ev-" + strconv.Itoa(i), Type: typ, Payload: []byte("null"), Hop: 1})
	}
}

// ut-docs#3887: 100 published events (20/s for 5s) write ONE event_published
// row and no per-subscriber enqueued rows; every event is still delivered.
func TestEnqueuePublishedCoalescesPublishAudit(t *testing.T) {
	d := hostfnTestDB(t)
	bus := NewEventBus(d)
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	bus.now = clock.Now
	const typ = "com.test.pub.tick"
	ch := subscribeTestPlugin(t, d, bus, publishTestSub, typ)

	publishN(bus, typ, 100)
	if got := len(ch); got != 100 {
		t.Fatalf("delivered %d events, want 100", got)
	}
	if n := auditCount(t, d, "event_published"); n != 1 {
		t.Fatalf("event_published rows = %d, want 1", n)
	}
	if n := auditCount(t, d, "event_dispatch", "status=enqueued"); n != 0 {
		t.Fatalf("enqueued rows = %d, want 0", n)
	}
	first := auditDetails(t, d, "event_published")[0]
	for _, want := range []string{"event_type=" + typ, "subscribers=1", "plugin_id=" + publishTestPlugin, "coalesced=0", "coalesced"} {
		if !strings.Contains(first, want) {
			t.Errorf("first row %q lacks %q", first, want)
		}
	}

	// After the interval, the next publish writes a second row carrying the
	// 99 publishes that had no row of their own.
	clock.Advance(publishedAuditInterval)
	publishN(bus, typ, 1)
	rows := auditDetails(t, d, "event_published")
	if len(rows) != 2 {
		t.Fatalf("event_published rows after the interval = %d, want 2", len(rows))
	}
	if !strings.Contains(rows[1], "coalesced=99") {
		t.Errorf("second row %q lacks coalesced=99", rows[1])
	}

	// A different publisher has its own throttle.
	bus.EnqueuePublished(context.Background(), "com.test.other", Event{ID: "o1", Type: typ, Payload: []byte("null"), Hop: 1})
	if n := auditCount(t, d, "event_published"); n != 3 {
		t.Fatalf("event_published rows with a second publisher = %d, want 3", n)
	}
}

// ut-docs#3887: a denied subscriber gets one coalesced denied row per
// (subscriber, type) per interval.
func TestEnqueuePublishedCoalescesDeniedAudit(t *testing.T) {
	d := hostfnTestDB(t)
	bus := NewEventBus(d)
	clock := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	bus.now = clock.Now
	const typ = "com.test.pub.tick"
	_ = subscribeTestPlugin(t, d, bus, publishTestSub, typ)
	if err := RevokePermission(context.Background(), d, publishTestSub, "events:receive"); err != nil {
		t.Fatal(err)
	}
	publishN(bus, typ, 50)
	if n := auditCount(t, d, "event_dispatch", "status=denied"); n != 1 {
		t.Fatalf("denied rows = %d, want 1", n)
	}
	if n := auditCount(t, d, "event_dispatch", "status=denied", "coalesced"); n != 1 {
		t.Fatalf("denied row does not mention coalescing")
	}
	clock.Advance(publishedAuditInterval)
	publishN(bus, typ, 1)
	if n := auditCount(t, d, "event_dispatch", "status=denied"); n != 2 {
		t.Fatalf("denied rows after the interval = %d, want 2", n)
	}
	// The second row carries the 49 denials the first one stood for.
	if n := auditCount(t, d, "event_dispatch", "status=denied", "coalesced=49 "); n != 1 {
		t.Fatalf("no denied row carries coalesced=49")
	}
}

// ut-docs#3887: a full channel still leaves a (throttled) dropped row.
func TestEnqueuePublishedFullChannelStillAuditsDropped(t *testing.T) {
	d := hostfnTestDB(t)
	bus := NewEventBus(d)
	const typ = "com.test.pub.tick"
	_ = subscribeTestPlugin(t, d, bus, publishTestSub, typ)
	publishN(bus, typ, 105) // channel holds 100
	if n := auditCount(t, d, "event_dispatch", "status=dropped"); n != 1 {
		t.Fatalf("dropped rows = %d, want 1", n)
	}
}

// ut-docs#3887 regression guard: core (hop-0) Publish keeps one
// event_published row per event and one enqueued row per subscriber.
func TestCorePublishKeepsPerEventAudit(t *testing.T) {
	d := hostfnTestDB(t)
	bus := NewEventBus(d)
	const typ = "com.test.core.tick"
	_ = subscribeTestPlugin(t, d, bus, publishTestSub, typ)
	for i := 0; i < 3; i++ {
		if _, err := bus.Publish(context.Background(), typ, map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	if n := auditCount(t, d, "event_published"); n != 3 {
		t.Fatalf("event_published rows = %d, want 3", n)
	}
	if n := auditCount(t, d, "event_dispatch", "status=enqueued"); n != 3 {
		t.Fatalf("enqueued rows = %d, want 3", n)
	}
}
