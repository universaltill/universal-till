package entitlement

import (
	"context"
	"testing"
	"time"
)

// ADR-0117 §2: LinkTier normalises a raw wire value; only the exact string
// "realtime" is realtime, everything else (missing, unknown, a future value
// this build doesn't understand) fails closed to "periodic".
func TestLinkTier(t *testing.T) {
	cases := map[string]string{
		"realtime":  "realtime",
		"":          "periodic",
		"periodic":  "periodic",
		"REALTIME":  "periodic", // no case folding, same convention as Plan
		"real-time": "periodic",
		"on_demand": "periodic",
	}
	for raw, want := range cases {
		if got := LinkTier(raw); got != want {
			t.Errorf("LinkTier(%q) = %q, want %q", raw, got, want)
		}
	}
}

// ADR-0117 §2: on_demand is a recorded option, not built — only "realtime"
// tiers get a mode, and only the exact string "on_demand" selects it;
// anything else (missing, "always", unknown) is "always".
func TestLinkMode(t *testing.T) {
	cases := []struct {
		tier, raw, want string
	}{
		{"realtime", "always", "always"},
		{"realtime", "", "always"},
		{"realtime", "on_demand", "on_demand"},
		{"realtime", "ON_DEMAND", "always"},
		{"realtime", "bogus", "always"},
		{"periodic", "always", ""},
		{"periodic", "on_demand", ""},
		{"periodic", "", ""},
		{"", "always", ""},
	}
	for _, c := range cases {
		if got := LinkMode(c.tier, c.raw); got != c.want {
			t.Errorf("LinkMode(%q, %q) = %q, want %q", c.tier, c.raw, got, c.want)
		}
	}
}

// Block.Values always carries cloud_link + cloud_link_mode alongside the
// four ADR-0060 keys, normalised: an omitted cloud_link (older/unset cloud
// field) writes "periodic" with an empty mode (ADR-0117 §2: missing =
// periodic).
func TestBlockValuesCloudLinkDefaultsToPeriodic(t *testing.T) {
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	got, err := Block{Plan: "shop", SubscriptionStatus: "active"}.Values(now)
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	if got[KeyCloudLinkTier] != "periodic" {
		t.Fatalf("cloud_link tier = %q, want periodic", got[KeyCloudLinkTier])
	}
	if got[KeyCloudLinkMode] != "" {
		t.Fatalf("cloud_link mode = %q, want empty", got[KeyCloudLinkMode])
	}
	if len(got) != 6 {
		t.Fatalf("Values returned %d keys, want exactly 6: %v", len(got), got)
	}
}

func TestBlockValuesCloudLinkRealtime(t *testing.T) {
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	got, err := Block{Plan: "pro", SubscriptionStatus: "active", CloudLink: "realtime", CloudLinkMode: "always"}.Values(now)
	if err != nil {
		t.Fatalf("Values: %v", err)
	}
	if got[KeyCloudLinkTier] != "realtime" || got[KeyCloudLinkMode] != "always" {
		t.Fatalf("cloud_link = %q/%q, want realtime/always", got[KeyCloudLinkTier], got[KeyCloudLinkMode])
	}
}

// An invalid plan/status still rejects the whole block (cloud_link included
// — Values returns an error and the caller keeps its previous cache).
func TestBlockValuesCloudLinkDoesNotBypassPlanValidation(t *testing.T) {
	now := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	if _, err := (Block{Plan: "gold", SubscriptionStatus: "active", CloudLink: "realtime"}).Values(now); err == nil {
		t.Fatal("Values accepted an unknown plan just because cloud_link was set")
	}
}

// CloudLink mirrors EffectivePlan's staleness rule (same Grace window,
// anchored on the same last_confirmed_at) — the settings interface is
// shared, and this package doc-comments that sharing deliberately.
func TestCloudLinkReader(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	ts := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
	cache := func(tier, mode, confirmed string) mapReader {
		return mapReader{
			KeyCloudLinkTier:   tier,
			KeyCloudLinkMode:   mode,
			KeyLastConfirmedAt: confirmed,
		}
	}
	cases := []struct {
		name     string
		r        mapReader
		wantTier string
		wantMode string
	}{
		{"never confirmed", mapReader{}, "periodic", ""},
		{"fresh realtime always", cache("realtime", "always", ts(-time.Minute)), "realtime", "always"},
		{"fresh realtime on_demand", cache("realtime", "on_demand", ts(0)), "realtime", "on_demand"},
		{"fresh periodic", cache("periodic", "", ts(-time.Hour)), "periodic", ""},
		{"unknown cached tier fails closed", cache("bogus", "", ts(0)), "periodic", ""},
		{"exactly Grace old is honoured", cache("realtime", "always", ts(-Grace)), "realtime", "always"},
		{"Grace+1s degrades", cache("realtime", "always", ts(-Grace-time.Second)), "periodic", ""},
		{"unparsable last_confirmed_at", cache("realtime", "always", "yesterday-ish"), "periodic", ""},
		{"missing last_confirmed_at", mapReader{KeyCloudLinkTier: "realtime", KeyCloudLinkMode: "always"}, "periodic", ""},
		{"future-dated confirmation (clock skew) within Grace is fresh", cache("realtime", "always", ts(48*time.Hour)), "realtime", "always"},
		{"future-dated confirmation beyond Grace is not trusted", cache("realtime", "always", ts(Grace+time.Second)), "periodic", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tier, mode := CloudLink(context.Background(), tc.r, now)
			if tier != tc.wantTier || mode != tc.wantMode {
				t.Fatalf("CloudLink = (%q, %q), want (%q, %q)", tier, mode, tc.wantTier, tc.wantMode)
			}
		})
	}
}

func TestCloudLinkReaderErrorIsPeriodic(t *testing.T) {
	tier, mode := CloudLink(context.Background(), errReader{}, time.Now())
	if tier != "periodic" || mode != "" {
		t.Fatalf("CloudLink with a reader error = (%q, %q), want (periodic, \"\")", tier, mode)
	}
}
