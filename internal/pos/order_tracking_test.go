package pos

import (
	"testing"
	"time"
)

// OrderTrackingVisible is the ONE tracking-link liveness rule (ut-docs#527,
// ADR-0070): a tracked live order (new/preparing/ready) is always visible; a
// terminal one expires OrderTrackingExpiry after its last status write; an
// untracked ("") order — e.g. a counter sale whose receipt carries a QR
// (ut-docs#908) — expires OrderTrackingUntrackedExpiry after the sale was
// created; any unparseable timestamp the rule needs fails closed. Moved here
// from internal/pages so the cloud relay push (internal/cloudsync) applies
// the identical rule the LAN page does.
func TestOrderTrackingVisible(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-time.Hour).Format(time.RFC3339)
	ancient := "2020-01-01T00:00:00Z"
	cases := []struct {
		name      string
		status    string
		updatedAt string
		createdAt string
		want      bool
	}{
		{"untracked fresh sale is live", "", "", fresh, true},
		{"untracked at exactly the untracked expiry visible", "", "", now.Add(-OrderTrackingUntrackedExpiry).Format(time.RFC3339), true},
		{"untracked past the untracked expiry gone", "", "", now.Add(-OrderTrackingUntrackedExpiry - time.Second).Format(time.RFC3339), false},
		{"untracked ancient sale gone", "", "", ancient, false},
		{"untracked with unparseable created_at fails closed", "", "", "not-a-timestamp", false},
		{"untracked with empty created_at fails closed", "", "", "", false},
		{"untracked accepts SQLite's datetime('now') shape", "", "", now.Add(-time.Hour).Format("2006-01-02 15:04:05"), true},
		{"untracked SQLite-shaped ancient sale gone", "", "", "2020-01-01 00:00:00", false},
		{"new is live", OrderStatusNew, "", ancient, true},
		{"preparing is live regardless of timestamp", OrderStatusPreparing, ancient, ancient, true},
		{"ready is live", OrderStatusReady, now.Add(-3 * time.Hour).Format(time.RFC3339), ancient, true},
		{"tracked live status ignores an unparseable created_at", OrderStatusPreparing, "", "garbage", true},
		{"collected within expiry visible", OrderStatusCollected, now.Add(-time.Hour).Format(time.RFC3339), ancient, true},
		{"collected at exactly the expiry visible", OrderStatusCollected, now.Add(-OrderTrackingExpiry).Format(time.RFC3339), ancient, true},
		{"collected past expiry gone", OrderStatusCollected, now.Add(-OrderTrackingExpiry - time.Second).Format(time.RFC3339), fresh, false},
		{"cancelled past expiry gone", OrderStatusCancelled, now.Add(-3 * time.Hour).Format(time.RFC3339), fresh, false},
		{"cancelled within expiry visible", OrderStatusCancelled, now.Add(-time.Minute).Format(time.RFC3339), ancient, true},
		{"terminal with unparseable timestamp fails closed", OrderStatusCollected, "not-a-timestamp", fresh, false},
		{"terminal with empty timestamp fails closed", OrderStatusCancelled, "", fresh, false},
	}
	for _, c := range cases {
		if got := OrderTrackingVisible(c.status, c.updatedAt, c.createdAt, now); got != c.want {
			t.Errorf("%s: OrderTrackingVisible(%q, %q, %q) = %v, want %v", c.name, c.status, c.updatedAt, c.createdAt, got, c.want)
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
