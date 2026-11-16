package plugins

import (
	"errors"
	"fmt"
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

func TestDeclineCodeFrom(t *testing.T) {
	cases := map[string]string{
		`{"outcome":"declined","decline_code":"outcome_unknown"}` + "\n": "outcome_unknown",
		`{"decline_code":"card_declined"}`:                               "card_declined",
		`{"outcome":"declined"}`:                                         "",
		`{"decline_code":"x\ny"}`:                                        "",
		`not json`:                                                       "",
		``:                                                               "",
	}
	for in, want := range cases {
		if got := declineCodeFrom(in); got != want {
			t.Errorf("declineCodeFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestPaymentDeclineError_UnwrapsThroughGateChain(t *testing.T) {
	base := errors.New("exit_code(2)")
	err := fmt.Errorf("blocking event x failed: %w", fmt.Errorf("wasm handler: %w", &PaymentDeclineError{Code: DeclineOutcomeUnknown, Err: base}))
	var de *PaymentDeclineError
	if !errors.As(err, &de) || de.Code != DeclineOutcomeUnknown {
		t.Fatalf("decline code lost in chain: %v", err)
	}
	if !errors.Is(err, base) {
		t.Fatal("must still unwrap to the exit error")
	}
}
