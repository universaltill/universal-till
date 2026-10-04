package entitlement

import (
	"context"
	"testing"
	"time"
)

// ADR-0148 §1: the periodic cloud check-in runs only when the last
// cloud-confirmed block says active + a plan that allows cloud_sync. The
// gate deliberately has NO Grace/staleness check: a paid till offline for
// a month must still re-confirm.
func TestSyncAllowed(t *testing.T) {
	now := time.Now().UTC()
	ts := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	cache := func(plan, status, confirmed string) mapReader {
		return mapReader{
			KeyPlan:               plan,
			KeySubscriptionStatus: status,
			KeyLastConfirmedAt:    confirmed,
		}
	}
	cases := []struct {
		name string
		r    Reader
		want bool
	}{
		{"no cache (never confirmed)", mapReader{}, false},
		{"fresh active shop", cache("shop", "active", ts(-time.Minute)), true},
		{"fresh active pro", cache("pro", "active", ts(0)), true},
		{"fresh active chain", cache("chain", "active", ts(-time.Hour)), true},
		{"paid but stale by 30 days still allowed", cache("shop", "active", ts(-30*24*time.Hour)), true},
		{"paid, confirmation far in the future still allowed", cache("pro", "active", ts(30*24*time.Hour)), true},
		{"local plan", cache("local", "active", ts(-time.Minute)), false},
		{"lapsed", cache("shop", "lapsed", ts(-time.Minute)), false},
		{"status none", cache("pro", "none", ts(-time.Minute)), false},
		{"unknown status", cache("pro", "trialing", ts(-time.Minute)), false},
		{"unknown plan", cache("gold", "active", ts(-time.Minute)), false},
		{"empty plan", cache("", "active", ts(-time.Minute)), false},
		{"unparsable last_confirmed_at", cache("shop", "active", "yesterday-ish"), false},
		{"missing last_confirmed_at", mapReader{KeyPlan: "shop", KeySubscriptionStatus: "active"}, false},
		{"whitespace-padded values are trimmed", cache(" shop ", " active ", " "+ts(0)+" "), true},
		{"reader error fails closed", errReader{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SyncAllowed(context.Background(), tc.r); got != tc.want {
				t.Fatalf("SyncAllowed = %v, want %v", got, tc.want)
			}
		})
	}
}
