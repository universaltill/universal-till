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

// A shop's fiscal posture is proven for ONE market. ADR-0081 merged Germany's
// and Turkey's posture onto a single settings key
// (fiscal.KeySigningDeviceConfigured, which fiscal.EvaluateGate reads for
// both), so `store.country` became a load-bearing part of a compliance gate
// — and it is ordinary shop config that a manager edits.
//
// ut-docs#1750, third independent review. Two earlier attempts put this
// invariant inside one HTTP handler, and both were defeated because
// store.country has FOUR writers: POST /api/settings/upsert, POST
// /api/settings/save, the cloud `set_setting` directive, and the setup
// wizard. Guarding one of them left the others open — a reviewer reproduced
// a manager-only bypass through /api/settings/save end to end:
//
//	country=TR -> a cashier sale auto-confirms the device -> country=DE
//	=> German till, fiscal.Allowed, no TSE at all
//
// So the rule lives here, in one place every writer calls, rather than being
// re-remembered per handler.
//
// Two halves, both fail-closed:
//
//   - requireFiscalAuthorityForCountryChange: while a signing device IS
//     confirmed, moving the shop to another country needs the same owner-only
//     authority that writing the flag directly needs. That closes the
//     round-trip above (the manager cannot make the second move) AND stops a
//     manager clearing a real German TSE posture by bouncing the country,
//     which an earlier draft of this fix accidentally made possible.
//   - clearFiscalStateForCountryChange: when the country really does change,
//     the posture does not travel with it. Called BEFORE the country is
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

// signingDeviceConfigured reports whether a signer is currently proven.
func signingDeviceConfigured(ctx context.Context, d *common.Deps) bool {
	v, _, err := d.Settings.Get(ctx, fiscal.KeySigningDeviceConfigured)
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

// clearFiscalStateForCountryChange drops the signing-device posture when the
// country actually changes, and records the transition on the posture key —
// the ADR-0048 Decision 1 fiscal-toggle audit only fires when the key being
// written IS the fiscal key, so without this a posture change driven by a
// country edit left no trail on it.
//
// MUST be called before the new country is persisted.
func clearFiscalStateForCountryChange(ctx context.Context, d *common.Deps, actorID, next string) error {
	if d == nil || d.Settings == nil || !countryChanging(d, next) {
		return nil
	}
	prev, _, err := d.Settings.Get(ctx, fiscal.KeySigningDeviceConfigured)
	if err != nil {
		return err
	}
	for _, k := range []string{fiscal.KeySigningDeviceConfigured, fiscal.KeySigningDeviceFailingSince} {
		if err := d.Settings.Set(ctx, k, ""); err != nil {
			return err
		}
	}
	if strings.TrimSpace(prev) == "" {
		return nil // nothing was proven; no posture transition to record
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := data.NewPOSRepo(d.Db).InsertAudit(ctx, nil, actorID, "settings", fiscal.KeySigningDeviceConfigured,
		"tse_configured_changed", map[string]any{
			"from":   prev,
			"to":     "",
			"reason": "country changed to " + strings.TrimSpace(next) + " — a fiscal posture is proven for one market only (ut-docs#1750)",
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
