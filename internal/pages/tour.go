package pages

import (
	"context"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Sale-screen guided tour (ut-docs#3710). web/public/tour.js walks a new
// operator through the sale screen once; the steps and their strings live in
// web/ui/partials/tour.html. Finishing or skipping it records
// tour.done.<user id> (data.TourDoneSettingsKey), after which "/" no longer
// starts it on its own; /?tour=1 (Help → "Take the tour") always does.

// tourShouldStart reports whether the sale screen starts the tour for this
// request. Only a signed-in operator gets the automatic start — with no
// session (UT_AUTH=off) there is no user to remember it for, and it would
// otherwise reopen on every visit. ?tour=1 is the explicit restart.
// The caller has already sent a self-order kiosk elsewhere (registerIndex).
func tourShouldStart(ctx context.Context, d *common.Deps, r *http.Request) bool {
	if r.URL.Query().Get("tour") == "1" {
		return true
	}
	u, ok := auth.FromContext(r.Context())
	if !ok || u.ID == "" || d.Settings == nil {
		return false
	}
	done, ok, err := d.Settings.Get(ctx, data.TourDoneSettingsKey(u.ID))
	// A read error must not trap an operator in a tour on every visit.
	if err != nil {
		return false
	}
	return !ok || done == ""
}

// registerTour wires POST /api/tour/done. Called from registerIndex, so every
// harness serving the sale screen serves its tour's endpoint too.
func registerTour(mux *http.ServeMux, d *common.Deps) {
	// Any signed-in operator, cashier included: it records only the
	// caller's own key, taken from the session — never from the request —
	// so nobody can mark the tour done for someone else. A POST (not a GET)
	// so a prefetch or link preview can't mark it; the session cookie is
	// SameSite=Lax, so a cross-site form can't send it.
	mux.HandleFunc("POST /api/tour/done", func(w http.ResponseWriter, r *http.Request) {
		u, ok := auth.FromContext(r.Context())
		if !ok || u.ID == "" {
			common.LocalizedError(w, r, http.StatusUnauthorized, "tour.error.signed_out")
			return
		}
		// Written locally even on a till that follows a main till: the
		// write-through needs the "settings" permission on the main till,
		// which a cashier doesn't hold, and dismissing a tour must never
		// depend on the network. Nothing is lost: the admin pull never
		// deletes settings rows, so this marker stays; if the main till
		// holds the same user's marker its value simply replaces this one.
		// settings-write:allow per-user tour-seen marker, any role, offline-safe (ut-docs#3710)
		if err := d.Settings.Set(r.Context(), data.TourDoneSettingsKey(u.ID), time.Now().UTC().Format(time.RFC3339)); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "common.error.server", "tour: mark done", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]bool{"done": true}, "error": nil})
	})
}
