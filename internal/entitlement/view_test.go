package entitlement

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// ut-docs#2569: Describe is the one derivation every subscription surface
// (status-bar chip, Settings → Subscription card, the paused banners)
// renders from. Table-driven over the four states.
func TestDescribe(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	cache := func(plan, status, confirmed string) mapReader {
		return mapReader{
			KeyPlan:               plan,
			KeySubscriptionStatus: status,
			KeyLastConfirmedAt:    confirmed,
		}
	}
	cases := []struct {
		name      string
		r         Reader
		state     State
		plan      Plan
		effective Plan
	}{
		{"no keys (never enrolled)", mapReader{}, StateFree, PlanLocal, PlanLocal},
		{"local plan", cache("local", "active", ts(0)), StateFree, PlanLocal, PlanLocal},
		{"local plan lapsed is still free", cache("local", "lapsed", ts(0)), StateFree, PlanLocal, PlanLocal},
		{"unknown cached plan fails closed to free", cache("platinum", "active", ts(0)), StateFree, PlanLocal, PlanLocal},
		{"empty cached plan", cache("", "active", ts(0)), StateFree, PlanLocal, PlanLocal},
		{"paid plan never confirmed is free", mapReader{KeyPlan: "shop", KeySubscriptionStatus: "active"}, StateFree, PlanLocal, PlanLocal},
		{"paid plan with an empty last_confirmed_at is free", mapReader{KeyPlan: "shop", KeySubscriptionStatus: "active", KeyLastConfirmedAt: ""}, StateFree, PlanLocal, PlanLocal},
		{"reader error is free", errReader{}, StateFree, PlanLocal, PlanLocal},
		{"fresh shop", cache("shop", "active", ts(-time.Minute)), StateActive, PlanShop, PlanShop},
		{"chain within Grace", cache("chain", "active", ts(-Grace)), StateActive, PlanChain, PlanChain},
		{"past Grace", cache("shop", "active", ts(-Grace-time.Second)), StateStale, PlanShop, PlanLocal},
		{"future skew beyond Grace", cache("pro", "active", ts(Grace+time.Second)), StateStale, PlanPro, PlanLocal},
		{"empty status", cache("shop", "", ts(0)), StateStale, PlanShop, PlanLocal},
		{"unknown status", cache("shop", "trialing", ts(0)), StateStale, PlanShop, PlanLocal},
		{"unparsable confirmation", cache("shop", "active", "yesterday-ish"), StateStale, PlanShop, PlanLocal},
		{"confirmed lapse", cache("shop", "lapsed", ts(-time.Minute)), StateLapsed, PlanShop, PlanLocal},
		{"status none", cache("pro", "none", ts(-time.Minute)), StateLapsed, PlanPro, PlanLocal},
		{"lapsed and stale reads lapsed", cache("chain", "lapsed", ts(-30*24*time.Hour)), StateLapsed, PlanChain, PlanLocal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := Describe(context.Background(), tc.r, now)
			if v.State != tc.state || v.Plan != tc.plan || v.Effective != tc.effective {
				t.Fatalf("Describe = {State:%q Plan:%q Effective:%q}, want {%q %q %q}", v.State, v.Plan, v.Effective, tc.state, tc.plan, tc.effective)
			}
			if (v.State == StateFree || v.State == StateActive) && len(v.Paused) != 0 {
				t.Fatalf("%s state has Paused %v, want none", v.State, v.Paused)
			}
		})
	}
}

func TestDescribePaused(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	stale := func(plan string) mapReader {
		return mapReader{KeyPlan: plan, KeySubscriptionStatus: "active", KeyLastConfirmedAt: now.Add(-30 * 24 * time.Hour).Format(time.RFC3339)}
	}
	shop := Describe(context.Background(), stale("shop"), now)
	wantShop := []Capability{CapBrowserCatalog, CapCloudBackup, CapCloudSync, CapManagedTSE}
	if !reflect.DeepEqual(shop.Paused, wantShop) {
		t.Fatalf("shop→local Paused = %v, want %v", shop.Paused, wantShop)
	}
	chain := Describe(context.Background(), stale("chain"), now)
	wantChain := []Capability{CapBrowserCatalog, CapCentralCatalog, CapCloudBackup, CapCloudSync, CapConsolidatedReporting, CapManagedTSE}
	if !reflect.DeepEqual(chain.Paused, wantChain) {
		t.Fatalf("chain→local Paused = %v, want %v", chain.Paused, wantChain)
	}
}

// ExpiresAt and LastConfirmedAt are parsed for display; ExpiresAt is never
// compared to now (ADR-0060 §5) — a long-past expiry on a fresh "active"
// confirmation is still active.
func TestDescribeDates(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	expires := now.Add(-90 * 24 * time.Hour)
	confirmed := now.Add(-time.Hour)
	v := Describe(context.Background(), mapReader{
		KeyPlan:               "shop",
		KeySubscriptionStatus: "active",
		KeyExpiresAt:          expires.Format(time.RFC3339),
		KeyLastConfirmedAt:    confirmed.Format(time.RFC3339),
	}, now)
	if v.State != StateActive {
		t.Fatalf("State = %q, want active (expires_at is display-only)", v.State)
	}
	if !v.ExpiresAt.Equal(expires) || !v.LastConfirmedAt.Equal(confirmed) {
		t.Fatalf("dates = %v / %v, want %v / %v", v.ExpiresAt, v.LastConfirmedAt, expires, confirmed)
	}
	v = Describe(context.Background(), mapReader{
		KeyPlan:               "shop",
		KeySubscriptionStatus: "active",
		KeyExpiresAt:          "not a date",
		KeyLastConfirmedAt:    confirmed.Format(time.RFC3339),
	}, now)
	if !v.ExpiresAt.IsZero() {
		t.Fatalf("unparsable expires_at = %v, want zero", v.ExpiresAt)
	}
}
