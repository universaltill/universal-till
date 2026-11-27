package entitlement

import (
	"context"
	"testing"
	"time"
)

type kv map[string]string

func (m kv) Get(_ context.Context, k string) (string, bool, error) { v, ok := m[k]; return v, ok, nil }

// ut-docs#7101 (ADR-0219): on_demand wake-up. The cloud says "wanted for N seconds";
// the till turns that into a deadline it can compare to its own clock.
func TestLinkWantedUntil_Bounded(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		secs int64
		want string
	}{
		{0, ""}, {-5, ""},
		{300, now.Add(300 * time.Second).Format(time.RFC3339)},
		{100000, now.Add(MaxLinkWanted).Format(time.RFC3339)}, // a bad cloud cannot pin the socket open
	} {
		if got := LinkWantedUntil(now, c.secs); got != c.want {
			t.Errorf("secs %d: %q want %q", c.secs, got, c.want)
		}
	}
}

func TestLinkWanted(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	f := func(v string) kv { return kv{KeyCloudLinkWantedUntil: v} }
	for name, c := range map[string]struct {
		s    kv
		want bool
	}{
		"future":          {f(now.Add(time.Minute).Format(time.RFC3339)), true},
		"past":            {f(now.Add(-time.Second).Format(time.RFC3339)), false},
		"empty":           {f(""), false},
		"unset":           {kv{}, false},
		"junk":            {f("soon"), false},
		"too far ahead":   {f(now.Add(MaxLinkWanted + time.Minute).Format(time.RFC3339)), false}, // clock tampering / skew
		"exactly the cap": {f(now.Add(MaxLinkWanted).Format(time.RFC3339)), true},
	} {
		if got := LinkWanted(context.Background(), c.s, now); got != c.want {
			t.Errorf("%s: %v want %v", name, got, c.want)
		}
	}
}

func TestBlockValues_LinkWantedForS(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	b := Block{Plan: "shop", SubscriptionStatus: "active", CloudLink: "realtime", CloudLinkMode: "on_demand"}
	s := int64(300)
	b.LinkWantedForS = &s
	m, err := b.Values(now)
	if err != nil {
		t.Fatal(err)
	}
	if m[KeyCloudLinkMode] != "on_demand" || m[KeyCloudLinkWantedUntil] != now.Add(300*time.Second).Format(time.RFC3339) {
		t.Fatalf("values %v", m)
	}
	zero := int64(0)
	b.LinkWantedForS = &zero
	if m, _ = b.Values(now); m[KeyCloudLinkWantedUntil] != "" {
		t.Fatalf("0 must clear the deadline, got %q", m[KeyCloudLinkWantedUntil])
	}
	b.LinkWantedForS = nil
	if m, _ = b.Values(now); len(m) != 9 {
		t.Fatalf("absent field must add no key: %v", m)
	}
}
