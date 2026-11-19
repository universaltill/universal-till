package plugins

import (
	"testing"
	"time"
)

// ut-docs#6439: one stalled http_request must not eat the time a payment
// plugin needs to cancel the reader.
func TestGateRequestTimeout(t *testing.T) {
	now := time.Now()
	if _, ok := gateRequestTimeout(time.Time{}, now); ok {
		t.Fatal("non-gate event must not be bounded")
	}
	d, ok := gateRequestTimeout(now.Add(30*time.Second), now)
	if !ok || d != 30*time.Second-gateCancelReserve {
		t.Fatalf("fresh gate: got %v %v, want %v", d, ok, 30*time.Second-gateCancelReserve)
	}
	// A call that stalls would be cut before the deadline, leaving the reserve.
	if now.Add(d).After(now.Add(30*time.Second - gateCancelReserve)) {
		t.Fatal("bounded call must end before the reserve starts")
	}
	// Inside the reserve the call (the cancel) gets all that is left.
	d, ok = gateRequestTimeout(now.Add(3*time.Second), now)
	if !ok || d != 3*time.Second {
		t.Fatalf("inside reserve: got %v %v, want 3s", d, ok)
	}
	// Past the deadline: tiny positive timeout, never zero/negative.
	if d, _ = gateRequestTimeout(now.Add(-time.Second), now); d <= 0 {
		t.Fatalf("past deadline: got %v", d)
	}
}
