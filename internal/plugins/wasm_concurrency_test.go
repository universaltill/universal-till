package plugins

import (
	"context"
	"errors"
	"testing"
	"time"
)

// ADR-0121 §2 platform ceilings (ut-docs#3154): 4 calls per plugin / 16 in
// all on Pi and desktop, 2 / 8 on Android and iOS.
func TestWasmConcurrencyLimitsPerPlatform(t *testing.T) {
	cases := []struct {
		goos              string
		perPlugin, global int
	}{
		{"linux", 4, 16},
		{"windows", 4, 16},
		{"darwin", 4, 16},
		{"android", 2, 8},
		{"ios", 2, 8},
	}
	for _, c := range cases {
		p, g := wasmConcurrencyLimits(c.goos)
		if p != c.perPlugin || g != c.global {
			t.Errorf("wasmConcurrencyLimits(%q) = %d/%d, want %d/%d", c.goos, p, g, c.perPlugin, c.global)
		}
	}
}

func TestIsSalePathEvent(t *testing.T) {
	for _, ev := range []string{"tax.rate.ask", "charge.policy.ask", "receipt.policy.ask", FiscalSignAskEvent, FiscalSignStartEvent, FiscalSignReconcileAskEvent, "payment.card.authorize", "payment.card.refund"} {
		if !isSalePathEvent(ev) {
			t.Errorf("isSalePathEvent(%q) = false, want true", ev)
		}
	}
	for _, ev := range []string{"sale.completed", "export.requested.ask", "import.requested.ask", "plugin.button.pressed", "ui.view.render", "ui.view.ask", "ui.action.ask",
		// ADR-0121 R2b (ut-docs#4007): the pick's targeted event takes an ordinary slot.
		"catalog.identify", "catalog.identify.confirmed"} {
		if isSalePathEvent(ev) {
			t.Errorf("isSalePathEvent(%q) = true, want false", ev)
		}
	}
}

func shortCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	return ctx
}

// A plugin saturated with ordinary calls still has the reserved slot for a
// sale-path call; one more ordinary call waits and gives up at its deadline.
func TestWasmConcurrencyPerPluginReservesSaleSlot(t *testing.T) {
	g := newWasmCallGate(4, 16)
	var releases []func()
	for i := 0; i < 3; i++ {
		rel, err := g.acquire(context.Background(), "p", false)
		if err != nil {
			t.Fatalf("ordinary call %d: %v", i, err)
		}
		releases = append(releases, rel)
	}
	if _, err := g.acquire(shortCtx(t), "p", false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("4th ordinary call: err = %v, want deadline exceeded (reserved slot kept free)", err)
	}
	rel, err := g.acquire(shortCtx(t), "p", true)
	if err != nil {
		t.Fatalf("sale-path call on a saturated plugin: %v", err)
	}
	if _, err := g.acquire(shortCtx(t), "p", true); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("5th call on a 4-cap plugin: err = %v, want deadline exceeded", err)
	}
	// Other plugins are unaffected by p's saturation.
	relQ, err := g.acquire(shortCtx(t), "q", false)
	if err != nil {
		t.Fatalf("other plugin: %v", err)
	}
	relQ()
	rel()
	for _, r := range releases {
		r()
	}
}

// Globally, ordinary calls across many plugins can never take the last
// slot, so a sale-path call always gets one.
func TestWasmConcurrencyGlobalReservesSaleSlot(t *testing.T) {
	g := newWasmCallGate(4, 8)
	var releases []func()
	for i := 0; i < 7; i++ {
		plugin := string(rune('a' + i/3)) // 3 ordinary calls per plugin max
		rel, err := g.acquire(context.Background(), plugin, false)
		if err != nil {
			t.Fatalf("ordinary call %d: %v", i, err)
		}
		releases = append(releases, rel)
	}
	if _, err := g.acquire(shortCtx(t), "z", false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("8th ordinary call: err = %v, want deadline exceeded", err)
	}
	rel, err := g.acquire(shortCtx(t), "z", true)
	if err != nil {
		t.Fatalf("sale-path call with 7/8 global slots busy: %v", err)
	}
	rel()
	for _, r := range releases {
		r()
	}
}

// A waiting call proceeds as soon as a slot is released, and release is
// idempotent (a double release must not free a slot someone else holds).
func TestWasmConcurrencyWaiterWakesOnRelease(t *testing.T) {
	g := newWasmCallGate(2, 8)
	rel, err := g.acquire(context.Background(), "p", false)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		r, err := g.acquire(ctx, "p", false)
		if err == nil {
			r()
		}
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	rel()
	rel() // idempotent
	if err := <-done; err != nil {
		t.Fatalf("waiter: %v", err)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.total != 0 || len(g.perPlugin) != 0 {
		t.Fatalf("after all releases: total=%d perPlugin=%v, want 0/empty", g.total, g.perPlugin)
	}
}

// Through HandleEvent: with a plugin's ordinary slots all busy, an ordinary
// event fails at its deadline while a sale-path event still runs.
func TestWasmHandleEventSalePathGetsReservedSlot(t *testing.T) {
	w := NewWasmRuntime(t.TempDir())
	defer w.rt.Close(context.Background())
	const pluginID = "com.test.caps"
	// (module) — an empty module instantiates and returns at once.
	compiled, err := w.rt.CompileModule(context.Background(), []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.modules[pluginID] = compiled
	w.mu.Unlock()

	perCap, _ := wasmConcurrencyLimits("linux")
	w.calls = newWasmCallGate(perCap, 16)
	for i := 0; i < perCap-1; i++ {
		rel, err := w.calls.acquire(context.Background(), pluginID, false)
		if err != nil {
			t.Fatal(err)
		}
		defer rel()
	}
	if _, err := w.HandleEvent(shortCtx(t), pluginID, Event{Type: "sale.completed"}); err == nil {
		t.Fatal("ordinary event on a saturated plugin ran; want it refused at its deadline")
	}
	if _, err := w.HandleEvent(shortCtx(t), pluginID, Event{Type: "tax.rate.ask"}); err != nil {
		t.Fatalf("sale-path event on a saturated plugin: %v", err)
	}
}

func TestWasmCallGateNilIsUnlimited(t *testing.T) {
	var g *wasmCallGate
	rel, err := g.acquire(context.Background(), "p", false)
	if err != nil {
		t.Fatal(err)
	}
	rel()
}

// queuedTestRuntime is a runtime with one loaded no-op module on a mobile
// gate (2 per plugin: one ordinary slot, one reserved) and a short default
// deadline, with that one ordinary slot held. It returns the hold's release.
func queuedTestRuntime(t *testing.T, pluginID string) (*WasmRuntime, func()) {
	t.Helper()
	w := NewWasmRuntime(t.TempDir())
	t.Cleanup(func() {
		w.Close(context.Background())
		_ = w.rt.Close(context.Background())
	})
	compiled, err := w.rt.CompileModule(context.Background(), []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00})
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.modules[pluginID] = compiled
	w.mu.Unlock()
	w.timeout = 50 * time.Millisecond
	perCap, global := wasmConcurrencyLimits("android")
	w.calls = newWasmCallGate(perCap, global)
	hold, err := w.calls.acquire(context.Background(), pluginID, false) // the long export/import
	if err != nil {
		t.Fatal(err)
	}
	return w, hold
}

// ut-docs#3171: a non-blocking sale.completed queued behind a long ordinary
// call waits past its own deadline and is delivered once the slot frees;
// a blocking call in the same spot still fails at its deadline.
func TestWasmQueuedEventDeliveredWhenOrdinarySlotFrees(t *testing.T) {
	const pluginID = "com.test.queued"
	w, hold := queuedTestRuntime(t, pluginID)

	if _, err := w.HandleEvent(context.Background(), pluginID, Event{Type: "sale.completed"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocking call on a saturated plugin: err = %v, want refused at its deadline", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := w.handleQueuedEvent(context.Background(), pluginID, Event{Type: "sale.completed"})
		done <- err
	}()
	// Held for 4× the event's own deadline: the pre-fix code failed here.
	time.Sleep(4 * w.timeout)
	select {
	case err := <-done:
		t.Fatalf("queued event returned while the slot was still held: %v", err)
	default:
	}
	hold()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("queued event after the slot freed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued event not delivered after the slot freed")
	}
}

// The queue wait is bounded: a slot that never frees fails the event after
// queueWait, not never.
func TestWasmQueuedEventWaitIsBounded(t *testing.T) {
	const pluginID = "com.test.queued-bound"
	w, hold := queuedTestRuntime(t, pluginID)
	defer hold()
	w.queueWait = 150 * time.Millisecond

	start := time.Now()
	_, err := w.handleQueuedEvent(context.Background(), pluginID, Event{Type: "stock.adjusted"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a queue-wait deadline", err)
	}
	if took := time.Since(start); took < w.queueWait {
		t.Fatalf("gave up after %s, before queueWait %s", took, w.queueWait)
	}
}

// Close ends a drainer's slot wait at once instead of holding shutdown for
// the whole queue wait.
func TestWasmQueuedEventWaitEndsOnClose(t *testing.T) {
	const pluginID = "com.test.queued-close"
	w, hold := queuedTestRuntime(t, pluginID)
	defer hold()
	w.mu.Lock()
	drainCtx := w.drainCtx
	w.mu.Unlock()

	done := make(chan error, 1)
	go func() {
		_, err := w.handleQueuedEvent(drainCtx, pluginID, Event{Type: "sale.completed"})
		done <- err
	}()
	time.Sleep(2 * w.timeout)
	w.Close(context.Background())
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not end the queued wait")
	}
	w.mu.Lock()
	fresh := w.drainCtx
	w.mu.Unlock()
	if fresh.Err() != nil {
		t.Fatal("Close left a cancelled drain context for the next Sync")
	}
}

// Close ends only the slot wait: an event already admitted runs on to its
// own deadline, so shutdown's grace window still lets it finish (#380).
func TestWasmQueuedEventAdmittedRunSurvivesDrainCancel(t *testing.T) {
	const pluginID = "com.test.queued-admitted"
	w, hold := queuedTestRuntime(t, pluginID)
	hold()
	// (module (func (local i32) (loop … i < 50e6 …)) (start 0)): runs long
	// enough that a cancelled run context aborts it (WithCloseOnContextDone).
	compiled, err := w.rt.CompileModule(context.Background(), []byte{
		0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00,
		0x01, 0x04, 0x01, 0x60, 0x00, 0x00, // type: () -> ()
		0x03, 0x02, 0x01, 0x00, // func 0: type 0
		0x08, 0x01, 0x00, // start: func 0
		0x0a, 0x1a, 0x01, 0x18, 0x01, 0x01, 0x7f,
		0x03, 0x40, 0x20, 0x00, 0x41, 0x01, 0x6a, 0x21, 0x00,
		0x20, 0x00, 0x41, 0x80, 0xe1, 0xeb, 0x17, 0x49, 0x0d, 0x00, 0x0b, 0x0b,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.mu.Lock()
	w.modules[pluginID] = compiled
	w.mu.Unlock()
	w.timeout = 30 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled: a free slot is still admitted at once
	if _, err := w.handleQueuedEvent(ctx, pluginID, Event{Type: "sale.completed"}); err != nil {
		t.Fatalf("admitted event with a cancelled drain context: %v", err)
	}
}
