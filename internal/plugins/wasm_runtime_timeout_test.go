package plugins

import (
	"testing"
	"time"
)

// TestWasmRuntimeTimeoutFor proves export.requested.ask gets a longer
// deadline than the blanket 2s every other ".ask" event uses (ut-docs#221)
// -- gathering real sales/tax/payment data and building a file is
// legitimately slower than a small value-returning hook like tax.rate.ask,
// which must keep its existing 2s timeout unchanged.
func TestWasmRuntimeTimeoutFor(t *testing.T) {
	w := NewWasmRuntime(t.TempDir())
	const pluginID = "com.test.timeout"

	if got := w.timeoutFor(pluginID, "tax.rate.ask"); got != 2*time.Second {
		t.Fatalf("tax.rate.ask timeout = %v, want unchanged 2s", got)
	}
	if got := w.timeoutFor(pluginID, "export.requested.ask"); got != 30*time.Second {
		t.Fatalf("export.requested.ask timeout = %v, want 30s", got)
	}

	// A plugin holding net:* already gets the wider netTimeout (10s) for
	// ordinary events -- that must be untouched by this change.
	w.mu.Lock()
	w.hasNet[pluginID] = true
	w.mu.Unlock()
	if got := w.timeoutFor(pluginID, "tax.rate.ask"); got != 10*time.Second {
		t.Fatalf("net-permitted tax.rate.ask timeout = %v, want unchanged netTimeout 10s", got)
	}
	// export.requested.ask must still get at least its own 30s, not be
	// capped down to netTimeout.
	if got := w.timeoutFor(pluginID, "export.requested.ask"); got != 30*time.Second {
		t.Fatalf("net-permitted export.requested.ask timeout = %v, want 30s", got)
	}
}
