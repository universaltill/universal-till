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
	for _, ev := range []string{"sale.completed", "export.requested.ask", "import.requested.ask", "plugin.button.pressed", "ui.view.render"} {
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
