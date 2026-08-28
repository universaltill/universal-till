// Package fiscal implements the German TSE hard-gate policy engine
// (ADR-0048, ut-docs#715): whether a shop that has declared itself the
// system of record for a fiscalised market may complete a real sale right
// now, based purely on shop-declared settings state — never on network
// reachability (offline-first, ADR-0003; "TSE failing" is a narrower
// condition than "TSE unreachable", see ADR-0048 Decision 1).
//
// This package owns the policy only. Enforcement lives behind one shared
// helper, internal/pages.enforceFiscalGate, called by every money-moving
// completion path:
//
//   - the shared cashier/kiosk tender path (internal/pages.completeTender);
//   - the till's own refund screen (POST /api/refund, refund_page.go);
//   - the inventory page's return form (POST /api/inventory/return,
//     inventory_api.go) — a refund/return is aufzeichnungspflichtig under
//     KassenSichV the same as a sale (ut-docs#731);
//   - the Shifts page's cash adjustment/payout form
//     (POST /api/shifts/adjustment, shifts_api.go), on a negative amount
//     only — cash actually leaving the drawer, the same scope its
//     manager-PIN gate uses;
//   - the bottle-deposit payout (POST /api/shifts/pfandrueckgabe,
//     shifts_api.go), unconditionally — it is always a payout
//     (ut-docs#998). Both go through shifts_api.go's
//     enforceCashAdjustmentFiscalGate wrapper, which is this same helper
//     plus the shared localized refusal copy.
//
// These last two never call pos.CompleteSale, which is exactly why
// ut-docs#731's sweep missed them; internal/pages'
// TestFiscalGate_EveryRecordCashAdjustmentCallSiteIsGated now fails loudly
// if a pos.RecordCashAdjustment call site is added without the gate.
//
// A replica->primary journal replay (internal/pages/sync_sales.go) is
// deliberately NOT gated: that sale was already gated where it was rung.
// A skim recorded at shift close (pos.CloseShift) also moves cash out of
// the drawer but is NOT gated today — see ut-docs#998's review record.
// The owner override that can temporarily lift a configured-but-failing
// block is granted via POST /api/fiscal/tse-override
// (internal/pages/fiscal_api.go).
package fiscal

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Settings keys (ADR-0048 Decision 1/3) — stored in the existing settings
// key/value store, same as store.country; deliberately not a new table.
const (
	// KeySystemOfRecord: this shop is taking real, legally-binding sales
	// (vs shadow/trial/demo). Unset → false.
	KeySystemOfRecord = "fiscal.system_of_record"
	// KeyTSEConfigured: this shop has a TSE set up (any ownership model,
	// ADR-0045). Unset → false.
	KeyTSEConfigured = "fiscal.tse_configured"
	// KeyTSEFailingSince: RFC3339 timestamp when a configured TSE became
	// known-failing; absent/empty = healthy. Not operator-settable via any
	// UI, and — deliberately — NO production writer exists yet: the
	// fiscal.sign.ask point (ut-docs#675) does not drive this key, because
	// none of the failures it can observe (timeout, transport error,
	// plugin-declared "unreachable", unusable answer) distinguishes "the
	// TSE itself is known bad" (expired cert, dongle pulled,
	// provider-reported fault — what this key means, ADR-0048 Decision 1)
	// from "we currently can't reach it". A future fiscal.sign.ask
	// contract version adding a TSE-confirmed-broken response state would
	// be the right first writer. Set directly in tests only. Never to be
	// set from a mere network-offline condition (ADR-0048 Decision 1).
	KeyTSEFailingSince = "fiscal.tse_failing_since"
	// KeyOverrideUntil: RFC3339 end of the currently-granted owner override
	// window. Absent/empty/expired = no active override.
	KeyOverrideUntil = "fiscal.tse_override_until"
	// KeyOverrideReason: the free-text reason given when the active
	// override was granted.
	KeyOverrideReason = "fiscal.tse_override_reason"
	// KeyOverrideActor: user id of the admin who authorized the active
	// override.
	KeyOverrideActor = "fiscal.tse_override_actor"
)

// OverrideAcknowledgement is the fixed confirmation phrase an override
// request must carry, typed exactly (ADR-0048 Decision 3's typed
// acknowledgement — deliberately not a checkbox).
const OverrideAcknowledgement = "I understand this sale will not be signed"

// MaxOverrideMinutes caps an override window at 8 hours. A request above the
// cap is rejected, never silently clamped.
const MaxOverrideMinutes = 480

// SettingsReader is the read-only slice of the settings store the gate
// needs; satisfied by both *data.SettingsRepo and *settings.Store.
type SettingsReader interface {
	Get(ctx context.Context, key string) (string, bool, error)
}

// Decision is the gate's verdict for a tender attempt.
type Decision int

const (
	// Allowed: proceed normally — non-gated market, shadow/trial/demo, or a
	// configured, healthy TSE.
	Allowed Decision = iota
	// BlockedNeverConfigured: hard block, no override path exists
	// (ADR-0048 Decision 2.2).
	BlockedNeverConfigured
	// BlockedTSEFailing: blocked; an owner override could lift it
	// (ADR-0048 Decision 2.4).
	BlockedTSEFailing
	// AllowedWithOverride: proceed, but the sale must be flagged in the
	// audit trail as completed during an active override window.
	AllowedWithOverride
)

// Gate is one evaluation's outcome. The Override* fields are populated only
// when Decision == AllowedWithOverride, for the per-sale audit marker.
type Gate struct {
	Decision       Decision
	OverrideUntil  time.Time
	OverrideReason string
	OverrideActor  string
}

// RequiresHardGate reports whether country is a market whose sales are
// hard-gated on fiscal readiness. Only Germany today; the next fiscalised
// market (ADR-0047's list) is a one-line addition here.
func RequiresHardGate(country string) bool {
	return country == "DE"
}

// EvaluateGate applies ADR-0048 Decision 2, in order. It reads only the
// local settings store — never the network — and re-checks the override
// window against now on every call, so expiry needs no background job:
// blocking resumes on the very next sale attempt.
//
// For a non-gated country it returns Allowed without touching s at all. For
// a gated country a settings read error fails closed (returned to the
// caller, which refuses the tender) — the gate cannot know the shop's
// declared posture, and guessing "open" would defeat its purpose.
func EvaluateGate(ctx context.Context, s SettingsReader, country string, now time.Time) (Gate, error) {
	if !RequiresHardGate(country) {
		return Gate{Decision: Allowed}, nil
	}

	systemOfRecord, err := boolSetting(ctx, s, KeySystemOfRecord)
	if err != nil {
		return Gate{}, err
	}
	if !systemOfRecord {
		// Shadow/trial/demo: fully unblocked regardless of TSE state.
		return Gate{Decision: Allowed}, nil
	}

	configured, err := boolSetting(ctx, s, KeyTSEConfigured)
	if err != nil {
		return Gate{}, err
	}
	if !configured {
		// Hard block, unconditionally. Deliberately no override lookup on
		// this branch — not "no override offered", no code path that checks
		// for one (ADR-0048 Decision 2.2).
		return Gate{Decision: BlockedNeverConfigured}, nil
	}

	failingSince, _, err := s.Get(ctx, KeyTSEFailingSince)
	if err != nil {
		return Gate{}, fmt.Errorf("fiscal gate: read %s: %w", KeyTSEFailingSince, err)
	}
	if strings.TrimSpace(failingSince) == "" {
		// Configured and healthy.
		return Gate{Decision: Allowed}, nil
	}

	untilRaw, _, err := s.Get(ctx, KeyOverrideUntil)
	if err != nil {
		return Gate{}, fmt.Errorf("fiscal gate: read %s: %w", KeyOverrideUntil, err)
	}
	until, perr := time.Parse(time.RFC3339, strings.TrimSpace(untilRaw))
	if strings.TrimSpace(untilRaw) == "" || perr != nil || !now.Before(until) {
		// No override, a malformed one (fail closed), or an expired one.
		return Gate{Decision: BlockedTSEFailing}, nil
	}

	// Active override: proceed, carrying the grant details so the caller
	// can flag the sale in the audit trail. Reason/actor reads are
	// best-effort — their absence must not turn an otherwise-valid
	// override into a block.
	reason, _, _ := s.Get(ctx, KeyOverrideReason)
	actor, _, _ := s.Get(ctx, KeyOverrideActor)
	return Gate{
		Decision:       AllowedWithOverride,
		OverrideUntil:  until,
		OverrideReason: reason,
		OverrideActor:  actor,
	}, nil
}

// IsSystemOfRecord reports whether the shop has declared itself
// fiscal.system_of_record, using the same truthy parsing EvaluateGate uses
// internally — so a caller outside this package can never read this
// setting differently than the gate does.
func IsSystemOfRecord(ctx context.Context, s SettingsReader) (bool, error) {
	return boolSetting(ctx, s, KeySystemOfRecord)
}

// boolSetting reads key and interprets it with parseBoolSetting; a missing
// key is false.
func boolSetting(ctx context.Context, s SettingsReader, key string) (bool, error) {
	v, ok, err := s.Get(ctx, key)
	if err != nil {
		return false, fmt.Errorf("fiscal gate: read %s: %w", key, err)
	}
	if !ok {
		return false, nil
	}
	return parseBoolSetting(v), nil
}

// parseBoolSetting mirrors the settings page's own truthy convention
// (settings_page.go's upsert handler): "true"/"1"/"on", case-insensitive.
// Anything else — including garbage — reads as false, the safe default for
// flags whose unset meaning is false.
func parseBoolSetting(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "on":
		return true
	}
	return false
}
