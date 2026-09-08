package fiscal

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeSettings is an in-memory SettingsReader; a nil map reads as "no key
// set" (a brand-new shop). err, when set, is returned from every Get —
// simulating a genuinely broken settings store.
type fakeSettings struct {
	vals map[string]string
	err  error
}

func (f fakeSettings) Get(_ context.Context, key string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	v, ok := f.vals[key]
	return v, ok, nil
}

func TestRequiresHardGate(t *testing.T) {
	cases := []struct {
		country string
		want    bool
	}{
		{"DE", true},
		{"TR", true}, // ut-docs#1208 — Law No. 3100's YN ÖKC mandate, no ut-plugin-tax-tr yet
		{"GB", false},
		{"", false},
		{"de", false}, // store.country is persisted uppercase (setup wizard); no fuzzy matching
		{"tr", false}, // same casing rule as "de"
		{"AT", false}, // not fiscalised yet — one-line addition when it is
	}
	for _, c := range cases {
		if got := RequiresHardGate(c.country); got != c.want {
			t.Errorf("RequiresHardGate(%q) = %v, want %v", c.country, got, c.want)
		}
	}
}

func TestEvaluateGate_NonGatedCountryNeverReadsSettings(t *testing.T) {
	// A shop in a non-gated market (GB here — not DE/TR) must be completely
	// unaffected: EvaluateGate must not even depend on a working settings
	// store for it (regression pin for the "existing tender flow unchanged"
	// acceptance criterion).
	g, err := EvaluateGate(context.Background(), fakeSettings{err: errors.New("boom")}, "GB", time.Now())
	if err != nil {
		t.Fatalf("EvaluateGate(GB) = %v, want nil error", err)
	}
	if g.Decision != Allowed {
		t.Fatalf("EvaluateGate(GB) decision = %v, want Allowed", g.Decision)
	}
}

func TestEvaluateGate_ShadowModeIsAllowed(t *testing.T) {
	// fiscal.system_of_record unset → false → shadow/trial/demo: allowed,
	// even with nothing else configured.
	g, err := EvaluateGate(context.Background(), fakeSettings{}, "DE", time.Now())
	if err != nil || g.Decision != Allowed {
		t.Fatalf("unset system_of_record: got (%v, %v), want (Allowed, nil)", g.Decision, err)
	}

	// Explicit false, same outcome.
	g, err = EvaluateGate(context.Background(), fakeSettings{vals: map[string]string{
		KeySystemOfRecord: "false",
	}}, "DE", time.Now())
	if err != nil || g.Decision != Allowed {
		t.Fatalf("system_of_record=false: got (%v, %v), want (Allowed, nil)", g.Decision, err)
	}
}

func TestEvaluateGate_SystemOfRecordWithoutTSEIsHardBlocked(t *testing.T) {
	g, err := EvaluateGate(context.Background(), fakeSettings{vals: map[string]string{
		KeySystemOfRecord: "true",
	}}, "DE", time.Now())
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if g.Decision != BlockedNeverConfigured {
		t.Fatalf("decision = %v, want BlockedNeverConfigured", g.Decision)
	}
}

// ut-docs#1208: a Turkish shop declaring itself the system of record with no
// signer configured must fail closed exactly like a German one — there is no
// ut-plugin-tax-tr yet, so this is the only thing standing between "no
// plugin installed" and a silently unsigned sale.
func TestEvaluateGate_TurkeySystemOfRecordWithoutSignerIsHardBlocked(t *testing.T) {
	g, err := EvaluateGate(context.Background(), fakeSettings{vals: map[string]string{
		KeySystemOfRecord: "true",
	}}, "TR", time.Now())
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if g.Decision != BlockedNeverConfigured {
		t.Fatalf("decision = %v, want BlockedNeverConfigured", g.Decision)
	}
}

// ADR-0083 (ut-docs#1767): the posture keys are per country, so the gate for
// one market must never read another market's row. This is the regression
// test for the ut-docs#1750 round-trip — a device confirmed for TR, then
// store.country flipped to DE, used to read fiscal.Allowed for Germany with
// no TSE at all, because both markets shared one key. Now they are
// different rows: confirming (or failing) a device in one country leaves
// the other's gate exactly where it was.
func TestEvaluateGate_PerCountryKeysAreIndependent(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct{ configured, other string }{
		{"TR", "DE"},
		{"DE", "TR"},
	} {
		t.Run(tc.configured+" configured does not unblock "+tc.other, func(t *testing.T) {
			s := fakeSettings{vals: map[string]string{
				KeySystemOfRecord:                         "true",
				SigningDeviceConfiguredKey(tc.configured): "true",
			}}
			g, err := EvaluateGate(context.Background(), s, tc.configured, now)
			if err != nil || g.Decision != Allowed {
				t.Fatalf("EvaluateGate(%s) with its own device confirmed: got (%v, %v), want (Allowed, nil)", tc.configured, g.Decision, err)
			}
			g, err = EvaluateGate(context.Background(), s, tc.other, now)
			if err != nil {
				t.Fatalf("EvaluateGate(%s): %v", tc.other, err)
			}
			if g.Decision != BlockedNeverConfigured {
				t.Fatalf("EvaluateGate(%s) = %v, want BlockedNeverConfigured — a device confirmed for %s must not satisfy %s's gate", tc.other, g.Decision, tc.configured, tc.other)
			}
		})
		t.Run(tc.configured+" failing does not block "+tc.other, func(t *testing.T) {
			// The mirror: a known-bad device in one market must not turn the
			// other market's configured, healthy device into BlockedTSEFailing.
			s := fakeSettings{vals: map[string]string{
				KeySystemOfRecord:                           "true",
				SigningDeviceConfiguredKey(tc.configured):   "true",
				SigningDeviceFailingSinceKey(tc.configured): "2026-08-14T09:00:00Z",
				SigningDeviceConfiguredKey(tc.other):        "true",
			}}
			g, err := EvaluateGate(context.Background(), s, tc.configured, now)
			if err != nil || g.Decision != BlockedTSEFailing {
				t.Fatalf("EvaluateGate(%s) with its own device failing: got (%v, %v), want (BlockedTSEFailing, nil)", tc.configured, g.Decision, err)
			}
			g, err = EvaluateGate(context.Background(), s, tc.other, now)
			if err != nil || g.Decision != Allowed {
				t.Fatalf("EvaluateGate(%s) = (%v, %v), want (Allowed, nil) — %s's failing device must not leak into %s's gate", tc.other, g.Decision, err, tc.configured, tc.other)
			}
		})
	}
}

// The key functions normalise the country the same way for every caller
// (lower-case, trimmed), so a shop carrying "tr" or " TR " in store.country
// — a real state, since /api/settings/upsert stores it as free text — lands
// on the same row as one carrying "TR".
func TestSigningDeviceKeys_NormaliseCountry(t *testing.T) {
	for _, in := range []string{"TR", "tr", " TR ", "Tr"} {
		if got := SigningDeviceConfiguredKey(in); got != "fiscal.signing_device_configured.tr" {
			t.Errorf("SigningDeviceConfiguredKey(%q) = %q", in, got)
		}
		if got := SigningDeviceFailingSinceKey(in); got != "fiscal.signing_device_failing_since.tr" {
			t.Errorf("SigningDeviceFailingSinceKey(%q) = %q", in, got)
		}
	}
	if SigningDeviceConfiguredKey("DE") == SigningDeviceConfiguredKey("TR") {
		t.Fatal("DE and TR must resolve to different rows — that is the whole point of ADR-0083")
	}
}

// The never-configured branch must not be escapable via a (fabricated)
// override window — there is literally no code path that reads the override
// keys before the signing_device_configured check.
func TestEvaluateGate_NeverConfiguredIgnoresOverrideKeys(t *testing.T) {
	g, err := EvaluateGate(context.Background(), fakeSettings{vals: map[string]string{
		KeySystemOfRecord:                "true",
		SigningDeviceConfiguredKey("DE"): "false",
		KeyOverrideUntil:                 time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		KeyOverrideReason:                "fabricated",
		KeyOverrideActor:                 "user1",
	}}, "DE", time.Now())
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if g.Decision != BlockedNeverConfigured {
		t.Fatalf("decision = %v, want BlockedNeverConfigured even with override keys set", g.Decision)
	}
}

func TestEvaluateGate_ConfiguredHealthyIsAllowed(t *testing.T) {
	g, err := EvaluateGate(context.Background(), fakeSettings{vals: map[string]string{
		KeySystemOfRecord:                "true",
		SigningDeviceConfiguredKey("DE"): "true",
	}}, "DE", time.Now())
	if err != nil || g.Decision != Allowed {
		t.Fatalf("healthy TSE: got (%v, %v), want (Allowed, nil)", g.Decision, err)
	}
}

func TestEvaluateGate_FailingTSEWithoutOverrideIsBlocked(t *testing.T) {
	g, err := EvaluateGate(context.Background(), fakeSettings{vals: map[string]string{
		KeySystemOfRecord:                  "true",
		SigningDeviceConfiguredKey("DE"):   "true",
		SigningDeviceFailingSinceKey("DE"): "2026-08-14T09:00:00Z",
	}}, "DE", time.Now())
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if g.Decision != BlockedTSEFailing {
		t.Fatalf("decision = %v, want BlockedTSEFailing", g.Decision)
	}
}

func TestEvaluateGate_ActiveOverrideAllowsAndCarriesAudit(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	until := now.Add(30 * time.Minute)
	g, err := EvaluateGate(context.Background(), fakeSettings{vals: map[string]string{
		KeySystemOfRecord:                  "true",
		SigningDeviceConfiguredKey("DE"):   "true",
		SigningDeviceFailingSinceKey("DE"): "2026-08-14T09:00:00Z",
		KeyOverrideUntil:                   until.Format(time.RFC3339),
		KeyOverrideReason:                  "provider outage, tickets queueing",
		KeyOverrideActor:                   "admin1",
	}}, "DE", now)
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if g.Decision != AllowedWithOverride {
		t.Fatalf("decision = %v, want AllowedWithOverride", g.Decision)
	}
	if !g.OverrideUntil.Equal(until) {
		t.Fatalf("OverrideUntil = %v, want %v", g.OverrideUntil, until)
	}
	if g.OverrideReason != "provider outage, tickets queueing" || g.OverrideActor != "admin1" {
		t.Fatalf("override audit fields not carried: %+v", g)
	}
}

// Expiry is wall-clock, re-checked on every evaluation — one second past the
// window and blocking resumes with no background job involved.
func TestEvaluateGate_ExpiredOverrideBlocksAgain(t *testing.T) {
	until := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	vals := map[string]string{
		KeySystemOfRecord:                  "true",
		SigningDeviceConfiguredKey("DE"):   "true",
		SigningDeviceFailingSinceKey("DE"): "2026-08-14T09:00:00Z",
		KeyOverrideUntil:                   until.Format(time.RFC3339),
	}

	// One second before expiry: still allowed.
	g, err := EvaluateGate(context.Background(), fakeSettings{vals: vals}, "DE", until.Add(-time.Second))
	if err != nil || g.Decision != AllowedWithOverride {
		t.Fatalf("just before expiry: got (%v, %v), want (AllowedWithOverride, nil)", g.Decision, err)
	}

	// At/after expiry: blocked again.
	g, err = EvaluateGate(context.Background(), fakeSettings{vals: vals}, "DE", until)
	if err != nil || g.Decision != BlockedTSEFailing {
		t.Fatalf("at expiry: got (%v, %v), want (BlockedTSEFailing, nil)", g.Decision, err)
	}
}

// A malformed override timestamp fails closed (blocked), never open.
func TestEvaluateGate_MalformedOverrideUntilFailsClosed(t *testing.T) {
	g, err := EvaluateGate(context.Background(), fakeSettings{vals: map[string]string{
		KeySystemOfRecord:                  "true",
		SigningDeviceConfiguredKey("DE"):   "true",
		SigningDeviceFailingSinceKey("DE"): "2026-08-14T09:00:00Z",
		KeyOverrideUntil:                   "not-a-timestamp",
	}}, "DE", time.Now())
	if err != nil {
		t.Fatalf("EvaluateGate: %v", err)
	}
	if g.Decision != BlockedTSEFailing {
		t.Fatalf("decision = %v, want BlockedTSEFailing for a malformed override window", g.Decision)
	}
}

// A shop in a gated market (DE here; TR behaves identically) with a broken
// settings store fails closed with an error, not silently open: the gate
// can't know the shop's declared posture.
func TestEvaluateGate_SettingsErrorPropagates(t *testing.T) {
	_, err := EvaluateGate(context.Background(), fakeSettings{err: errors.New("disk gone")}, "DE", time.Now())
	if err == nil {
		t.Fatal("expected the settings error to propagate for a gated country")
	}
}

func TestParseBoolSetting(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"true", true}, {"TRUE", true}, {"1", true}, {"on", true},
		{"false", false}, {"0", false}, {"", false}, {"garbage", false},
	}
	for _, c := range cases {
		if got := parseBoolSetting(c.in); got != c.want {
			t.Errorf("parseBoolSetting(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
