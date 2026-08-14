package fiscal

import (
	"context"
	"errors"
	"testing"
	"time"
)

// mapSettings is a pure in-memory SettingsReader — the gate must be
// decidable from stored settings alone (ADR-0048 §2: no network, no live
// TSE round trip), so a plain map is a complete test double for it.
type mapSettings map[string]string

func (m mapSettings) Get(_ context.Context, key string) (string, bool, error) {
	v, ok := m[key]
	return v, ok, nil
}

var now = time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

func TestRequiresHardGate(t *testing.T) {
	if !RequiresHardGate("DE") {
		t.Fatalf("DE must require the hard gate (ADR-0048)")
	}
	for _, c := range []string{"GB", "US", "AT", "de", ""} {
		if RequiresHardGate(c) {
			t.Fatalf("country %q must not require the hard gate", c)
		}
	}
}

func TestIsOwnerRole(t *testing.T) {
	for role, want := range map[string]bool{
		"admin":       true,
		"super_admin": true,
		"manager":     false, // ADR-0048 §3: must NOT become manager-or-above
		"cashier":     false,
		"":            false,
	} {
		if got := IsOwnerRole(role); got != want {
			t.Fatalf("IsOwnerRole(%q) = %v, want %v", role, got, want)
		}
	}
}

func TestGate_NonGatedCountryProceeds(t *testing.T) {
	s := mapSettings{
		KeySystemOfRecord: "true",
		// deliberately never-configured: a non-gated market must still pass
	}
	res, err := CheckSaleAllowed(context.Background(), s, "GB", now)
	if err != nil {
		t.Fatalf("GB shop must never be gated, got %v", err)
	}
	if res.OverrideActive {
		t.Fatalf("no override should be reported for an ungated sale")
	}
}

func TestGate_ShadowShopProceeds(t *testing.T) {
	// fiscal.system_of_record unset -> false: shadow/trial/demo is never gated.
	s := mapSettings{}
	if _, err := CheckSaleAllowed(context.Background(), s, "DE", now); err != nil {
		t.Fatalf("shadow DE shop must proceed, got %v", err)
	}
	s[KeySystemOfRecord] = "false"
	if _, err := CheckSaleAllowed(context.Background(), s, "DE", now); err != nil {
		t.Fatalf("explicit shadow DE shop must proceed, got %v", err)
	}
}

func TestGate_NeverConfiguredHardBlocks(t *testing.T) {
	s := mapSettings{KeySystemOfRecord: "true"}
	_, err := CheckSaleAllowed(context.Background(), s, "DE", now)
	var never *NeverConfiguredError
	if !errors.As(err, &never) {
		t.Fatalf("expected NeverConfiguredError, got %v", err)
	}

	// Even a (bogus) active override window must not unblock the
	// never-configured branch — ADR-0048 §2: "no code path that checks
	// for one".
	s[KeyOverrideUntil] = now.Add(time.Hour).Format(time.RFC3339)
	s[KeyOverrideReason] = "should be ignored"
	_, err = CheckSaleAllowed(context.Background(), s, "DE", now)
	if !errors.As(err, &never) {
		t.Fatalf("override state must be unreachable from never-configured, got %v", err)
	}
}

func TestGate_ConfiguredHealthyProceeds(t *testing.T) {
	s := mapSettings{
		KeySystemOfRecord: "true",
		KeyTSEConfigured:  "true",
	}
	res, err := CheckSaleAllowed(context.Background(), s, "DE", now)
	if err != nil {
		t.Fatalf("configured+healthy must proceed, got %v", err)
	}
	if res.OverrideActive {
		t.Fatalf("healthy sale must not be marked as override-window")
	}
}

func TestGate_FailingWithoutOverrideBlocks(t *testing.T) {
	s := mapSettings{
		KeySystemOfRecord:  "true",
		KeyTSEConfigured:   "true",
		KeyTSEFailingSince: now.Add(-time.Hour).Format(time.RFC3339),
	}
	_, err := CheckSaleAllowed(context.Background(), s, "DE", now)
	var failing *FailingWithoutOverrideError
	if !errors.As(err, &failing) {
		t.Fatalf("expected FailingWithoutOverrideError, got %v", err)
	}
	var never *NeverConfiguredError
	if errors.As(err, &never) {
		t.Fatalf("failing must be a distinct error from never-configured")
	}
}

func TestGate_FailingWithActiveOverrideProceeds(t *testing.T) {
	until := now.Add(2 * time.Hour)
	s := mapSettings{
		KeySystemOfRecord:  "true",
		KeyTSEConfigured:   "true",
		KeyTSEFailingSince: now.Add(-time.Hour).Format(time.RFC3339),
		KeyOverrideUntil:   until.Format(time.RFC3339),
		KeyOverrideReason:  "TSE dongle failed, replacement ordered",
		KeyOverrideActor:   "adm1",
	}
	res, err := CheckSaleAllowed(context.Background(), s, "DE", now)
	if err != nil {
		t.Fatalf("active override must let the sale proceed, got %v", err)
	}
	if !res.OverrideActive {
		t.Fatalf("gate must report the override window so the sale can be flagged")
	}
	if res.OverrideReason != "TSE dongle failed, replacement ordered" {
		t.Fatalf("override reason not carried through: %q", res.OverrideReason)
	}
	if !res.OverrideUntil.Equal(until) {
		t.Fatalf("override until = %v, want %v", res.OverrideUntil, until)
	}
	if res.OverrideActor != "adm1" {
		t.Fatalf("override actor not carried through: %q", res.OverrideActor)
	}
}

func TestGate_ExpiredOverrideBlocksAgain(t *testing.T) {
	s := mapSettings{
		KeySystemOfRecord:  "true",
		KeyTSEConfigured:   "true",
		KeyTSEFailingSince: now.Add(-24 * time.Hour).Format(time.RFC3339),
		KeyOverrideUntil:   now.Add(-time.Minute).Format(time.RFC3339),
		KeyOverrideReason:  "yesterday's window",
	}
	_, err := CheckSaleAllowed(context.Background(), s, "DE", now)
	var failing *FailingWithoutOverrideError
	if !errors.As(err, &failing) {
		t.Fatalf("expired override must block again with no one remembering, got %v", err)
	}
}

func TestGate_MalformedOverrideTimestampBlocks(t *testing.T) {
	s := mapSettings{
		KeySystemOfRecord:  "true",
		KeyTSEConfigured:   "true",
		KeyTSEFailingSince: now.Add(-time.Hour).Format(time.RFC3339),
		KeyOverrideUntil:   "not-a-timestamp",
	}
	_, err := CheckSaleAllowed(context.Background(), s, "DE", now)
	var failing *FailingWithoutOverrideError
	if !errors.As(err, &failing) {
		t.Fatalf("corrupt override state must fail closed (block), got %v", err)
	}
}

func TestActiveOverride(t *testing.T) {
	until := now.Add(time.Hour)
	s := mapSettings{
		KeyOverrideUntil:  until.Format(time.RFC3339),
		KeyOverrideReason: "r",
		KeyOverrideActor:  "a",
	}
	ov, err := ActiveOverride(context.Background(), s, now)
	if err != nil {
		t.Fatalf("ActiveOverride: %v", err)
	}
	if !ov.Active || !ov.Until.Equal(until) || ov.Reason != "r" || ov.Actor != "a" {
		t.Fatalf("unexpected override info: %+v", ov)
	}

	ov, err = ActiveOverride(context.Background(), s, now.Add(2*time.Hour))
	if err != nil || ov.Active {
		t.Fatalf("expired window must report inactive, got %+v err %v", ov, err)
	}

	ov, err = ActiveOverride(context.Background(), mapSettings{}, now)
	if err != nil || ov.Active {
		t.Fatalf("no stored window must report inactive, got %+v err %v", ov, err)
	}
}

func TestMaxOverrideDuration(t *testing.T) {
	if MaxOverrideDuration != 8*time.Hour {
		t.Fatalf("override cap is 8h per ut-docs#715, got %v", MaxOverrideDuration)
	}
}
