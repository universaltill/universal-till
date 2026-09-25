package entitlement

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mapReader is the in-memory Reader the table tests drive EffectivePlan
// through — no DB needed, which is the point of the interface.
type mapReader map[string]string

func (m mapReader) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := m[key]
	return v, ok, nil
}

type errReader struct{}

func (errReader) Get(context.Context, string) (string, bool, error) {
	return "", false, errors.New("disk gone")
}

func TestAllows(t *testing.T) {
	shopPlus := []Capability{CapCloudBackup, CapCloudSync, CapBrowserCatalog, CapManagedTSE}
	chainOnly := []Capability{CapCentralCatalog, CapConsolidatedReporting}
	for _, c := range shopPlus {
		for plan, want := range map[Plan]bool{PlanLocal: false, PlanShop: true, PlanPro: true, PlanChain: true, Plan("gold"): false, Plan(""): false} {
			if got := Allows(plan, c); got != want {
				t.Errorf("Allows(%q, %q) = %v, want %v", plan, c, got, want)
			}
		}
	}
	for _, c := range chainOnly {
		for plan, want := range map[Plan]bool{PlanLocal: false, PlanShop: false, PlanPro: false, PlanChain: true, Plan("gold"): false} {
			if got := Allows(plan, c); got != want {
				t.Errorf("Allows(%q, %q) = %v, want %v", plan, c, got, want)
			}
		}
	}
	for _, plan := range []Plan{PlanLocal, PlanShop, PlanPro, PlanChain} {
		if Allows(plan, Capability("teleport")) {
			t.Errorf("Allows(%q, unknown capability) = true, want false", plan)
		}
	}
}

func TestGraceIsSevenDays(t *testing.T) {
	if Grace != 7*24*time.Hour {
		t.Fatalf("Grace = %v, want 7 days (ADR-0060 §5)", Grace)
	}
}

func TestEffectivePlan(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	cache := func(plan, status, confirmed string) mapReader {
		return mapReader{
			KeyPlan:               plan,
			KeySubscriptionStatus: status,
			KeyExpiresAt:          "",
			KeyLastConfirmedAt:    confirmed,
		}
	}
	cases := []struct {
		name string
		r    mapReader
		want Plan
	}{
		{"no keys (never enrolled)", mapReader{}, PlanLocal},
		{"fresh active shop", cache("shop", "active", ts(-time.Minute)), PlanShop},
		{"fresh active pro", cache("pro", "active", ts(0)), PlanPro},
		{"fresh active chain", cache("chain", "active", ts(-24*time.Hour)), PlanChain},
		{"exactly Grace old is honoured", cache("shop", "active", ts(-Grace)), PlanShop},
		{"Grace+1s degrades", cache("shop", "active", ts(-Grace-time.Second)), PlanLocal},
		{"30 days stale degrades", cache("chain", "active", ts(-30*24*time.Hour)), PlanLocal},
		{"confirmed lapse takes effect on receipt", cache("shop", "lapsed", ts(-time.Minute)), PlanLocal},
		{"status none", cache("pro", "none", ts(-time.Minute)), PlanLocal},
		{"unknown status", cache("pro", "trialing", ts(-time.Minute)), PlanLocal},
		{"missing status", mapReader{KeyPlan: "shop", KeyLastConfirmedAt: ts(0)}, PlanLocal},
		{"unparsable last_confirmed_at", cache("shop", "active", "yesterday-ish"), PlanLocal},
		{"missing last_confirmed_at", mapReader{KeyPlan: "shop", KeySubscriptionStatus: "active"}, PlanLocal},
		{"unknown cached plan", cache("platinum", "active", ts(0)), PlanLocal},
		{"empty cached plan", cache("", "active", ts(0)), PlanLocal},
		{"future-dated last_confirmed_at (clock skew) is fresh", cache("shop", "active", ts(48*time.Hour)), PlanShop},
		{"far-future last_confirmed_at does not panic and is not trusted", cache("pro", "active", "9999-12-31T23:59:59Z"), PlanLocal},
		{"last_confirmed_at exactly Grace ahead is honoured", cache("pro", "active", ts(Grace)), PlanPro},
		{"last_confirmed_at more than Grace ahead (clock was wrong when cached) degrades", cache("pro", "active", ts(Grace+time.Second)), PlanLocal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EffectivePlan(context.Background(), tc.r, now); got != tc.want {
				t.Fatalf("EffectivePlan = %q, want %q", got, tc.want)
			}
		})
	}
}

// ADR-0060 §5: expires_at is display-only and never evaluated. A long-past
// expires_at alongside a fresh "active" confirmation must still honour the
// cached plan — the cloud's subscription_status is what the till acts on.
func TestEffectivePlanIgnoresExpiresAt(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	r := mapReader{
		KeyPlan:               "pro",
		KeySubscriptionStatus: "active",
		KeyExpiresAt:          now.Add(-90 * 24 * time.Hour).Format(time.RFC3339),
		KeyLastConfirmedAt:    now.Add(-time.Hour).Format(time.RFC3339),
	}
	if got := EffectivePlan(context.Background(), r, now); got != PlanPro {
		t.Fatalf("EffectivePlan with past expires_at = %q, want pro (expires_at is display-only)", got)
	}
	r[KeyExpiresAt] = "not a date"
	if got := EffectivePlan(context.Background(), r, now); got != PlanPro {
		t.Fatalf("EffectivePlan with garbage expires_at = %q, want pro", got)
	}
}

func TestEffectivePlanReaderErrorIsLocal(t *testing.T) {
	if got := EffectivePlan(context.Background(), errReader{}, time.Now()); got != PlanLocal {
		t.Fatalf("EffectivePlan on reader error = %q, want local", got)
	}
}

func TestBlockValues(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.FixedZone("CEST", 2*3600))
	exp := "2026-10-24T00:00:00+02:00"
	got, err := Block{Plan: "shop", SubscriptionStatus: "active", ExpiresAt: &exp, RefreshedAt: "2026-09-24T10:00:00Z"}.Values(now)
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	want := map[string]string{
		KeyPlan:               "shop",
		KeySubscriptionStatus: "active",
		KeyExpiresAt:          "2026-10-23T22:00:00Z",
		KeyLastConfirmedAt:    "2026-09-24T10:00:00Z",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != 4 {
		t.Errorf("Values returned %d keys, want exactly 4: %v", len(got), got)
	}

	// null expiry → empty string.
	got, err = Block{Plan: "local", SubscriptionStatus: "none"}.Values(now)
	if err != nil {
		t.Fatalf("Values (null expiry): %v", err)
	}
	if v, ok := got[KeyExpiresAt]; !ok || v != "" {
		t.Fatalf("expires_at = %q (present=%v), want empty string", v, ok)
	}

	for name, b := range map[string]Block{
		"unknown plan":   {Plan: "gold", SubscriptionStatus: "active"},
		"empty plan":     {Plan: "", SubscriptionStatus: "active"},
		"unknown status": {Plan: "shop", SubscriptionStatus: "trialing"},
		"empty status":   {Plan: "shop"},
	} {
		if _, err := b.Values(now); err == nil {
			t.Errorf("%s: Values accepted an invalid block", name)
		}
	}
}

// ADR-0060 §5: expires_at is display-only, so an unparsable value must not
// cost the till its refresh — dropping the whole block would stop
// last_confirmed_at moving and degrade every paid till after 7 days over a
// field that is never evaluated. The plan and status are kept; the expiry
// is stored empty.
func TestBlockValuesUnparsableExpiresIsDropped(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	bad := "2026-10-24"
	got, err := Block{Plan: "shop", SubscriptionStatus: "active", ExpiresAt: &bad}.Values(now)
	if err != nil {
		t.Fatalf("Values rejected a block over its display-only expires_at: %v", err)
	}
	if got[KeyPlan] != "shop" || got[KeySubscriptionStatus] != "active" || got[KeyExpiresAt] != "" {
		t.Fatalf("Values = %v, want plan shop, status active, empty expires_at", got)
	}
}

func TestCapabilitiesListsEveryCapability(t *testing.T) {
	want := []Capability{CapBrowserCatalog, CapCentralCatalog, CapCloudBackup, CapCloudSync, CapConsolidatedReporting, CapManagedTSE}
	got := Capabilities()
	if len(got) != len(want) {
		t.Fatalf("Capabilities() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Capabilities() = %v, want %v (sorted)", got, want)
		}
	}
}

// ut-docs#2792: a replica with no cloud token of its own gets the main
// till's cache relayed over the LAN, verbatim — last_confirmed_at included,
// so the grace window stays anchored to the main till's last real
// confirmation and a replica can never keep a lapsed main's plan alive.
func TestReadCachedRoundTripsThroughRelayValues(t *testing.T) {
	src := mapReader{
		KeyPlan:               "pro",
		KeySubscriptionStatus: StatusActive,
		KeyExpiresAt:          "2026-12-31T00:00:00Z",
		KeyLastConfirmedAt:    "2026-09-20T10:00:00Z",
	}
	c, ok := ReadCached(context.Background(), src)
	if !ok {
		t.Fatal("a complete cache read back as absent")
	}
	kv, err := c.RelayValues()
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range src {
		if kv[k] != want {
			t.Errorf("%s = %q, want %q (verbatim)", k, kv[k], want)
		}
	}
	if len(kv) != 4 {
		t.Errorf("relay writes %d keys, want the 4 entitlement keys", len(kv))
	}
}

func TestReadCachedAbsentWithoutConfirmation(t *testing.T) {
	if _, ok := ReadCached(context.Background(), mapReader{KeyPlan: "shop", KeySubscriptionStatus: StatusActive}); ok {
		t.Fatal("a cache never confirmed by the cloud must not be relayed")
	}
	if _, ok := ReadCached(context.Background(), errReader{}); ok {
		t.Fatal("an unreadable cache must not be relayed")
	}
}

// The replica validates what its main till sends: an unknown plan/status or
// an unparsable confirmation time is refused whole (the replica keeps its
// cache); an unparsable expires_at is display-only and dropped.
func TestCachedRelayValuesValidation(t *testing.T) {
	good := Cached{Plan: "shop", SubscriptionStatus: StatusActive, LastConfirmedAt: "2026-09-20T10:00:00Z"}
	for name, c := range map[string]Cached{
		"unknown plan":     {Plan: "platinum", SubscriptionStatus: StatusActive, LastConfirmedAt: good.LastConfirmedAt},
		"unknown status":   {Plan: "shop", SubscriptionStatus: "trial", LastConfirmedAt: good.LastConfirmedAt},
		"bad confirmed_at": {Plan: "shop", SubscriptionStatus: StatusActive, LastConfirmedAt: "yesterday"},
		"empty confirmed":  {Plan: "shop", SubscriptionStatus: StatusActive},
	} {
		if _, err := c.RelayValues(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	bad := good
	bad.ExpiresAt = "soon"
	kv, err := bad.RelayValues()
	if err != nil || kv[KeyExpiresAt] != "" {
		t.Fatalf("unparsable expires_at: kv=%v err=%v, want it stored empty", kv, err)
	}
}
