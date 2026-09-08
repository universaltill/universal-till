package pages

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// A shop's fiscal posture is proven for ONE market, and since ADR-0083
// (ut-docs#1767) it is also STORED for one market: Germany's and Turkey's
// signing-device rows are independent
// (fiscal.SigningDeviceConfiguredKey(country)), and fiscal.EvaluateGate for
// a country can only ever read that country's own row. Changing
// store.country therefore cannot, by itself, make one market's confirmed
// device satisfy another market's gate — that independence is a property
// of the data model now, not of anything this file does.
//
// It was not always so. ADR-0081 had merged both markets onto a single
// shared key, which made store.country — ordinary shop config that a
// manager edits — a load-bearing part of an owner-only compliance gate.
// ut-docs#1750 (third independent review) found the seam: store.country has
// FOUR writers (POST /api/settings/upsert, POST /api/settings/save, the
// cloud `set_setting` directive, and the setup wizard), and a reviewer
// reproduced a manager-only bypass through /api/settings/save end to end:
//
//	country=TR -> a cashier sale auto-confirms the device -> country=DE
//	=> German till, fiscal.Allowed, no TSE at all
//
// This file was that card's write-path mitigation, and the rule still lives
// here, in one place every writer calls, rather than being re-remembered
// per handler. ADR-0083 fixed the data model underneath it and deliberately
// KEPT both halves below, retargeted at the correct row, because requiring
// an owner's attention to change a live shop's declared tax jurisdiction is
// a reasonable safeguard on its own merits — VAT rates, receipt formats and
// fiscal posture all follow store.country — and an unrelated data-model fix
// should not silently remove a safeguard and its audit trail as a side
// effect. What this guard protects against is now general
// shop-configuration hygiene, not a shared-key exploit.
//
// Two halves, both fail-closed, both resolved against the country being
// LEFT (the shop's current store.country, read before the change is
// persisted) — never the new country's row, which is an independent,
// untouched row:
//
//   - requireFiscalAuthorityForCountryChange: while the current country's
//     signing device IS confirmed, moving the shop to another country needs
//     the same owner-only authority that writing the flag directly needs.
//     That stops a manager clearing a real German TSE posture by bouncing
//     the country, which an earlier draft of the #1750 fix accidentally
//     made possible.
//   - clearFiscalStateForCountryChange: when the country really does change,
//     the old country's posture is cleared — a shop that has left a market
//     no longer holds a live "configured" declaration for it — and the
//     transition is audited on that row. Called BEFORE the country is
//     persisted, so a failure cannot leave the country moved with the flag
//     still set.
//
// The setup wizard is deliberately not a caller: it only runs while
// svc.NeedsFirstBoot, where there is no posture to protect yet.

// countryChanging reports whether next is a real change from the shop's
// current country, normalized the way the rest of the country logic is
// (store.country is unvalidated free text; only the wizard uppercases).
func countryChanging(d *common.Deps, next string) bool {
	next = strings.TrimSpace(next)
	if next == "" {
		return false
	}
	return !strings.EqualFold(next, strings.TrimSpace(d.CurrentState().Country))
}

// signingDeviceConfigured reports whether a signer is currently proven for
// the shop's CURRENT country — the market being left when this is consulted
// for a country change (ADR-0083 point 5).
func signingDeviceConfigured(ctx context.Context, d *common.Deps) bool {
	v, _, err := d.Settings.Get(ctx, fiscal.SigningDeviceConfiguredKey(d.CurrentState().Country))
	if err != nil {
		// Unreadable: assume configured, so a broken read cannot be the
		// thing that lets a country move slip through unauthorized.
		return true
	}
	return settingIsTrue(v)
}

// requireFiscalAuthorityForCountryChange writes a 403 and returns false when
// this caller may not move the shop's country because a signing device is
// currently confirmed. Returns true when the change may proceed.
func requireFiscalAuthorityForCountryChange(w http.ResponseWriter, r *http.Request, d *common.Deps, next string) bool {
	if d == nil || d.Settings == nil || !countryChanging(d, next) {
		return true
	}
	if !signingDeviceConfigured(r.Context(), d) {
		return true
	}
	if canPerform(d, r, "fiscal_tse_override") {
		return true
	}
	http.Error(w, "owner (admin) required to change country while a fiscal signing device is confirmed", http.StatusForbidden)
	return false
}

// clearFiscalStateForCountryChange drops the PREVIOUS country's signing-device
// posture when the country actually changes, and records the transition on
// that posture row — the ADR-0048 Decision 1 fiscal-toggle audit only fires
// when the key being written IS the fiscal key, so without this a posture
// change driven by a country edit left no trail on it.
//
// Only the country being left is touched (ADR-0083 point 5): its rows are
// resolved from d.CurrentState().Country, which is still the OLD country
// because this MUST be called before the new country is persisted. The new
// country's rows are independent and are deliberately never read, written
// or created here.
func clearFiscalStateForCountryChange(ctx context.Context, d *common.Deps, actorID, next string) error {
	if d == nil || d.Settings == nil || !countryChanging(d, next) {
		return nil
	}
	prevCountry := d.CurrentState().Country
	configuredKey := fiscal.SigningDeviceConfiguredKey(prevCountry)
	prev, _, err := d.Settings.Get(ctx, configuredKey)
	if err != nil {
		return err
	}
	for _, k := range []string{configuredKey, fiscal.SigningDeviceFailingSinceKey(prevCountry)} {
		if err := d.Settings.Set(ctx, k, ""); err != nil {
			return err
		}
	}
	if strings.TrimSpace(prev) == "" {
		return nil // nothing was proven; no posture transition to record
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := data.NewPOSRepo(d.Db).InsertAudit(ctx, nil, actorID, "settings", configuredKey,
		"tse_configured_changed", map[string]any{
			"from":   prev,
			"to":     "",
			"reason": "country changed from " + strings.TrimSpace(prevCountry) + " to " + strings.TrimSpace(next) + " — a fiscal posture is proven for one market only (ut-docs#1750, ADR-0083)",
		}, now, ""); err != nil {
		// Best-effort, like every other audit write on this path: the
		// posture is already cleared, which is the fail-closed direction.
		logging.L().Errorf("audit fiscal clear on country change: %v", err)
	}
	return nil
}

// actorIDFor resolves the session user for an audit row, or "" when auth is
// off or no user is attached. Deliberately NOT the elevation approver: this
// records who moved the country, and audit_log.actor_id FKs to users(id), so
// a non-user string would make the insert fail silently.
func actorIDFor(r *http.Request) string {
	if u, ok := auth.FromContext(r.Context()); ok {
		return u.ID
	}
	return ""
}
