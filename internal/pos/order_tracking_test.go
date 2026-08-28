package pos

import (
	"testing"
	"time"
)

// OrderTrackingVisible is the ONE tracking-link liveness rule (ut-docs#527,
// ADR-0070): a live order is always visible; a terminal one expires
// OrderTrackingExpiry after its last status write; an unparseable terminal
// timestamp fails closed. Moved here from internal/pages so the cloud relay
// push (internal/cloudsync) applies the identical rule the LAN page does.
func TestOrderTrackingVisible(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name      string
		status    string
		updatedAt string
		want      bool
	}{
		{"untracked empty status is live", "", "", true},
		{"new is live", OrderStatusNew, "", true},
		{"preparing is live regardless of timestamp", OrderStatusPreparing, "2020-01-01T00:00:00Z", true},
		{"ready is live", OrderStatusReady, now.Add(-3 * time.Hour).Format(time.RFC3339), true},
		{"collected within expiry visible", OrderStatusCollected, now.Add(-time.Hour).Format(time.RFC3339), true},
		{"collected at exactly the expiry visible", OrderStatusCollected, now.Add(-OrderTrackingExpiry).Format(time.RFC3339), true},
		{"collected past expiry gone", OrderStatusCollected, now.Add(-OrderTrackingExpiry - time.Second).Format(time.RFC3339), false},
		{"cancelled past expiry gone", OrderStatusCancelled, now.Add(-3 * time.Hour).Format(time.RFC3339), false},
		{"cancelled within expiry visible", OrderStatusCancelled, now.Add(-time.Minute).Format(time.RFC3339), true},
		{"terminal with unparseable timestamp fails closed", OrderStatusCollected, "not-a-timestamp", false},
		{"terminal with empty timestamp fails closed", OrderStatusCancelled, "", false},
	}
	for _, c := range cases {
		if got := OrderTrackingVisible(c.status, c.updatedAt, now); got != c.want {
			t.Errorf("%s: OrderTrackingVisible(%q, %q) = %v, want %v", c.name, c.status, c.updatedAt, got, c.want)
		}
	}
}

// OrderTrackingTerminal mirrors the two terminal statuses — the polling stop
// condition both the LAN page and the cloud read endpoint key off.
func TestOrderTrackingTerminal(t *testing.T) {
	for _, s := range []string{OrderStatusCollected, OrderStatusCancelled} {
		if !OrderTrackingTerminal(s) {
			t.Errorf("OrderTrackingTerminal(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", OrderStatusNew, OrderStatusPreparing, OrderStatusReady} {
		if OrderTrackingTerminal(s) {
			t.Errorf("OrderTrackingTerminal(%q) = true, want false", s)
		}
	}
}
