package plugins

import (
	"context"
	"strings"
	"sync"
)

// wasmConcurrencyLimits returns the host's ceiling on concurrent WASM calls
// per plugin and across all plugins (ADR-0121 §2 platform ceilings,
// ut-docs#3154). Host constants, never manifest-overridable. Mobile gets
// half: the interpreter is slower and the tablet has less RAM.
func wasmConcurrencyLimits(goos string) (perPlugin, global int) {
	if goos == "android" || goos == "ios" {
		return 2, 8
	}
	return 4, 16
}

// JobCaps returns how many plugin jobs (ADR-0121 §8, ut-docs#3908) may
// run at once per plugin and across all plugins on goos, derived from the
// call gate's slots so the knowledge stays here. A job holds one ordinary
// slot for its whole run, so per plugin it may take at most perPlugin-1
// (one ordinary slot stays free for the plugin's own page asks; the
// reserved sale-path slot is never a job's) and never more than two;
// across plugins at most (global-1)/2, half of the ordinary slots. Mobile:
// 1 and 3; desktop: 2 and 7. A job past either cap is refused, never
// queued — a queued job would wait for a slot with its deadline ticking.
func JobCaps(goos string) (perPlugin, global int) {
	per, glob := wasmConcurrencyLimits(goos)
	return max(min(2, per-1), 1), max((glob-1)/2, 1)
}

// isSalePathEvent reports whether eventType may use the slot reserved per
// plugin and globally, so a plugin (or all plugins) saturated by ordinary
// work can never block checkout. ADR-0121 §2: any ".ask" / ".authorize"
// point and "fiscal.sign.*" (plus ".refund", the payment gate's other
// half). The export/import data transfers are ".ask" by name but run for
// up to 30 s / 5 min; they are job-like work, and letting them hold the
// reserved slot would defeat it, so they stay ordinary. So do the "ui.*"
// asks (ui.view.ask / ui.action.ask, ut-docs#3160): ADR-0121 §2 says
// "ui.* calls ... never take a reserved slot".
func isSalePathEvent(eventType string) bool {
	if strings.HasPrefix(eventType, "ui.") || isExportClassEvent(eventType) || isImportClassEvent(eventType) {
		return false
	}
	return strings.HasSuffix(eventType, ".ask") ||
		strings.HasPrefix(eventType, "fiscal.sign.") ||
		isPaymentGateClassEvent(eventType)
}

// wasmCallGate caps concurrent WASM calls (ADR-0121 §2). One slot per
// plugin and one globally are reserved for sale-path events: an ordinary
// call is admitted only while at least two slots remain free at both
// levels, a sale-path call while one does. A call that can't be admitted
// waits for a release or its own deadline.
//
// event_publish (ADR-0121 §3, ut-docs#3871) is asynchronous by design: a
// published event is enqueued only after the publishing instance exits and
// runs off the subscriber's drainer as ordinary work (hop > 0 is never
// sale-path — isSalePathCall), so a publish chain never waits on a slot
// while holding one and can't starve checkout (mobile has one ordinary slot).
type wasmCallGate struct {
	mu        sync.Mutex
	perCap    int
	globalCap int
	total     int
	perPlugin map[string]int
	changed   chan struct{} // closed and replaced on every release
}

func newWasmCallGate(perCap, globalCap int) *wasmCallGate {
	return &wasmCallGate{
		perCap:    perCap,
		globalCap: globalCap,
		perPlugin: map[string]int{},
		changed:   make(chan struct{}),
	}
}

// acquire blocks until pluginID may start one more call, or ctx ends. The
// returned release is idempotent.
func (g *wasmCallGate) acquire(ctx context.Context, pluginID string, salePath bool) (func(), error) {
	if g == nil {
		return func() {}, nil
	}
	reserve := 1
	if salePath {
		reserve = 0
	}
	for {
		g.mu.Lock()
		if g.perPlugin[pluginID] < g.perCap-reserve && g.total < g.globalCap-reserve {
			g.perPlugin[pluginID]++
			g.total++
			g.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { g.release(pluginID) }) }, nil
		}
		wait := g.changed
		g.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func (g *wasmCallGate) release(pluginID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.total--
	if g.perPlugin[pluginID]--; g.perPlugin[pluginID] <= 0 {
		delete(g.perPlugin, pluginID)
	}
	close(g.changed)
	g.changed = make(chan struct{})
}
