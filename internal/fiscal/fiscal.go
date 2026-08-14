// Package fiscal implements the ADR-0048 German TSE hard-gate: the
// policy engine that decides, from shop-declared settings alone, whether a
// real (system-of-record) sale may be tendered at all. It runs BEFORE the
// tender path and before ADR-0044's fiscal.sign.ask insertion point, and
// deliberately reads nothing but the local settings store — no network, no
// plugin round trip — so it can never conflict with the offline-first rule
// (a till that is merely offline is NOT a failing TSE; see
// KeyTSEFailingSince below).
package fiscal

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Settings keys (ADR-0048 Decision 1: three state keys plus the override
// window, all in the existing settings store — no new table).
const (
	// KeySystemOfRecord: this shop is taking real, legally-binding sales
	// (vs. shadow/trial/demo). Unset -> false. Owner-only toggle; every
	// write is audit-logged (entity_type fiscal_settings).
	KeySystemOfRecord = "fiscal.system_of_record"
	// KeyTSEConfigured: a TSE is set up for this shop, any ownership model
	// (ADR-0045). Unset -> false. Owner-only toggle. Will be written by the
	// real provisioning flow once ut-docs#663 lands.
	KeyTSEConfigured = "fiscal.tse_configured"
	// KeyTSEFailingSince: RFC3339 timestamp if a configured TSE is
	// currently known-failing; absent/empty = healthy. NOT operator-settable
	// (no UI control ships for it — ADR-0048 Decision 1 explains why), and
	// explicitly NOT the same signal as network connectivity: ADR-0044's
	// known-offline short-circuit degrades to proceed-and-declare and must
	// never write this key. Written only by a real fiscal.sign.ask failure
	// callback (future, ut-docs#675) or directly in tests.
	KeyTSEFailingSince = "fiscal.tse_failing_since"
	// KeyOverrideUntil/Reason/Actor: the active owner-override window
	// (ADR-0048 Decision 3). Until is RFC3339; expiry is enforced by
	// re-checking it against wall-clock time on every gate evaluation, so
	// blocking resumes on the very next sale attempt after expiry.
	KeyOverrideUntil  = "fiscal.tse_override_until"
	KeyOverrideReason = "fiscal.tse_override_reason"
	KeyOverrideActor  = "fiscal.tse_override_actor"
)

// ConfirmationPhrase is the exact typed acknowledgement an owner must enter
// to grant a TSE-failure override (ADR-0048 Decision 3: a typed phrase,
// not a checkbox). Compared case-sensitively after trimming surrounding
// whitespace. The settings UI renders this constant verbatim so the shown
// phrase is always the required one, whatever the display locale.
const ConfirmationPhrase = "I understand these sales will not be TSE-signed"

// MaxOverrideDuration is the hard cap on an override window (ut-docs#715:
// time-boxed, max 8 hours). Requests above it are rejected, not clamped.
const MaxOverrideDuration = 8 * time.Hour

// SettingsReader is the read-only slice of the settings store the gate
// needs (satisfied by *settings.Store and *data.SettingsRepo).
type SettingsReader interface {
	Get(ctx context.Context, key string) (string, bool, error)
}

// RequiresHardGate reports whether country mandates the TSE hard gate.
// Only "DE" today; the next fiscalised market (ADR-0047's Italy/Spain/
// France/Austria list) is a one-line addition here, not a redesign.
func RequiresHardGate(country string) bool {
	return country == "DE"
}

// IsOwnerRole reports whether role maps to "the shop's owner" for the
// ADR-0048 override: admin, or super_admin (which naturally satisfies an
// admin-or-above check). Deliberately NOT manager-or-above — do not swap
// this for auth.User.IsManager(), which accepts managers.
func IsOwnerRole(role string) bool {
	return role == "admin" || role == "super_admin"
}

// NeverConfiguredError blocks a system-of-record sale in a gated country
// with no TSE configured. Hard, unconditional: no override path reads this
// branch at all (ADR-0048 Decision 2.2).
type NeverConfiguredError struct{}

func (e *NeverConfiguredError) Error() string {
	return "fiscal: sale blocked: shop is declared system-of-record in a TSE-mandated country but no TSE is configured"
}

// FailingWithoutOverrideError blocks a system-of-record sale while the
// configured TSE is known-failing and no owner override window is active.
type FailingWithoutOverrideError struct {
	// FailingSince is the stored fiscal.tse_failing_since value (RFC3339).
	FailingSince string
}

func (e *FailingWithoutOverrideError) Error() string {
	return "fiscal: sale blocked: configured TSE failing since " + e.FailingSince + " and no active owner override"
}

// Override describes the stored owner-override window.
type Override struct {
	Active bool
	Until  time.Time
	Reason string
	Actor  string
}

// GateResult is what CheckSaleAllowed reports for a sale that MAY proceed.
// OverrideActive means the sale is only allowed because of an active
// owner-override window — the caller must flag the completed sale in its
// audit trail (action "unsigned_override") and on its receipt.
type GateResult struct {
	OverrideActive bool
	OverrideUntil  time.Time
	OverrideReason string
	OverrideActor  string
}

// CheckSaleAllowed evaluates the ADR-0048 hard gate for a sale about to be
// tendered. It returns nil error when the sale may proceed, a
// *NeverConfiguredError or *FailingWithoutOverrideError when it must not,
// and a plain error only for a settings-store read failure. Inputs are the
// shop's stored settings and country plus the caller's clock — nothing
// else, by design (a known-offline sale is never blocked here; offline
// handling stays ADR-0044's proceed-and-declare, untouched).
func CheckSaleAllowed(ctx context.Context, s SettingsReader, country string, now time.Time) (GateResult, error) {
	if !RequiresHardGate(country) {
		return GateResult{}, nil
	}
	systemOfRecord, err := boolSetting(ctx, s, KeySystemOfRecord)
	if err != nil {
		return GateResult{}, err
	}
	if !systemOfRecord {
		// Shadow/trial/demo: never gated (ADR-0048 Decision 2.1).
		return GateResult{}, nil
	}
	configured, err := boolSetting(ctx, s, KeyTSEConfigured)
	if err != nil {
		return GateResult{}, err
	}
	if !configured {
		// Hard block, unconditionally — the override keys are not even
		// read on this branch (ADR-0048 Decision 2.2).
		return GateResult{}, &NeverConfiguredError{}
	}
	failingSince, err := stringSetting(ctx, s, KeyTSEFailingSince)
	if err != nil {
		return GateResult{}, err
	}
	if failingSince == "" {
		// Configured and healthy (Decision 2.3). Any live signing failure
		// downstream is ADR-0044's proceed-and-declare, not this gate's.
		return GateResult{}, nil
	}
	ov, err := ActiveOverride(ctx, s, now)
	if err != nil {
		return GateResult{}, err
	}
	if !ov.Active {
		return GateResult{}, &FailingWithoutOverrideError{FailingSince: failingSince}
	}
	return GateResult{
		OverrideActive: true,
		OverrideUntil:  ov.Until,
		OverrideReason: ov.Reason,
		OverrideActor:  ov.Actor,
	}, nil
}

// ActiveOverride reads the stored override window and reports whether it is
// active at now. A missing, empty, malformed, or expired
// fiscal.tse_override_until fails closed (inactive) — corrupt state must
// block, never unblock.
func ActiveOverride(ctx context.Context, s SettingsReader, now time.Time) (Override, error) {
	untilRaw, err := stringSetting(ctx, s, KeyOverrideUntil)
	if err != nil {
		return Override{}, err
	}
	if untilRaw == "" {
		return Override{}, nil
	}
	until, parseErr := time.Parse(time.RFC3339, untilRaw)
	if parseErr != nil || !now.Before(until) {
		return Override{}, nil
	}
	reason, err := stringSetting(ctx, s, KeyOverrideReason)
	if err != nil {
		return Override{}, err
	}
	actor, err := stringSetting(ctx, s, KeyOverrideActor)
	if err != nil {
		return Override{}, err
	}
	return Override{Active: true, Until: until, Reason: reason, Actor: actor}, nil
}

func stringSetting(ctx context.Context, s SettingsReader, key string) (string, error) {
	v, ok, err := s.Get(ctx, key)
	if err != nil {
		return "", fmt.Errorf("fiscal: read %s: %w", key, err)
	}
	if !ok {
		return "", nil
	}
	return strings.TrimSpace(v), nil
}

func boolSetting(ctx context.Context, s SettingsReader, key string) (bool, error) {
	v, err := stringSetting(ctx, s, key)
	if err != nil || v == "" {
		return false, err
	}
	b, parseErr := strconv.ParseBool(v)
	if parseErr != nil {
		// Unparseable stored state defaults to false — for both keys this
		// is the conservative reading (not system-of-record; not
		// configured), never a way to skip the gate for a shop that had
		// declared itself in scope with a well-formed "true".
		return false, nil
	}
	return b, nil
}
