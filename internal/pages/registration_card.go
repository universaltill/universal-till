package pages

import (
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// The Settings "Cloud record" card (ADR-0200, ut-docs#6559): status only
// (registered, not registered, paused), the last time the cloud answered, the
// notice (what this till tells Universal Till) and the "Stop sending" switch.
// There is no Register button and no opt-in: the till registers itself.
//
// NOTICE TEXT PENDING OWNER APPROVAL: the registration.notice.* strings are the
// DRAFT checkin-notice-v1 (legal-reviews/2026-11-17-auto-registration-6559.md).
//
// Showing the card is what "the notice was shown" means for a till that was
// set up before the notice existed: the card renders the full text, so the
// first render stores shown-at (enroll.MarkNoticeShown) and wakes the
// registration loop. The wizard stores it when its last screen is submitted.

// registrationCardView is the card's template data; nil for a session that
// may not open Settings (a cashier sees nothing and the notice is not marked
// shown on their behalf).
func registrationCardView(r *http.Request, d *common.Deps, allowed bool) map[string]any {
	if !allowed {
		return nil
	}
	ctx := r.Context()
	if err := enroll.MarkNoticeShown(ctx, d.Settings, time.Now()); err != nil {
		logging.L().Warnf("settings: remember that the registration notice was shown: %v", err)
	}
	cs := enroll.CurrentCardStatus(ctx, d.Settings)
	v := map[string]any{
		"state":  string(cs.State),
		"paused": cs.State == enroll.StatePaused,
	}
	if !cs.LastCheckin.IsZero() {
		v["lastCheckin"] = cs.LastCheckin.UTC().Format(time.RFC3339)
	}
	return v
}

// registerRegistrationStop wires POST /api/settings/registration-stop: the
// shop-wide "Stop sending" / "Resume sending" switch. Elevation-gated like
// the other shop settings (a cashier gets the in-place PIN prompt), audited,
// persisted per till under registration.stopped. Stopping halts registration,
// change reports and the start-up refresh of an unpaid till; it deletes
// nothing in the cloud (the erasure route does).
func registerRegistrationStop(mux *http.ServeMux, d *common.Deps, posRepo *data.POSRepo) {
	mux.HandleFunc("POST /api/settings/registration-stop", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		_ = r.ParseForm()
		stop := r.Form.Get("stop") == "1"
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			summaryKey := "elevation.summary.registration_resume"
			if stop {
				summaryKey = "elevation.summary.registration_stop"
			}
			renderElevationPrompt(w, r, "/api/settings/registration-stop", "#registration-msg",
				httpx.T(locale, summaryKey),
				[]elevationHiddenField{{Name: "stop", Value: r.Form.Get("stop")}}, elev)
			return
		}
		if err := enroll.SetStopped(r.Context(), d.Settings, stop); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "settings.error.save_failed", "settings_registration_stop", err)
			return
		}
		action := "registration_resumed"
		if stop {
			action = "registration_stopped"
		}
		settingsAudit(r, posRepo, elev, "settings", enroll.KeyStopped, action, map[string]any{"stopped": stop})
		settingsRespondSaved(w, r, elev)
	})
}
