package pages

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#6977 (S1): the status bar says when this till last heard from the
// cloud — "Synced just now" / "Synced 5 min ago" / "Synced at 14:05" / "Not
// synced yet" — and a manager can press "Sync now", an operator check-in
// (ADR-0148 §2) that waits a few seconds for the answer. A status chip, never
// a modal, never a sale gate: GET reads settings only.

const (
	// cloudSyncStaleAfter: older than this, the chip turns amber. A periodic
	// till checks in about every 2 min (ADR-0117 §3, up to 144 s), so 10 min
	// means several missed check-ins.
	cloudSyncStaleAfter = 10 * time.Minute
	// cloudSyncRelativeFor: under this age the chip counts minutes; older, it
	// shows the clock (today) or the date and time.
	cloudSyncRelativeFor = time.Hour
)

// cloudSyncChipNow is the chip's clock; tests pin it.
var cloudSyncChipNow = time.Now

// Chip states (data-cloud-sync in the partial, CSS is-<state>).
const (
	cloudSyncFresh = "fresh"
	cloudSyncStale = "stale"
	cloudSyncNever = "never"
)

// cloudSyncText picks the chip's words and state. Pure, for table tests.
// last.IsZero() → never. A last in the future (clock skew) counts as just now.
func cloudSyncText(locale string, last, now time.Time) (state, text string) {
	if last.IsZero() {
		return cloudSyncNever, httpx.T(locale, "status.cloud_sync.never")
	}
	age := now.Sub(last)
	state = cloudSyncFresh
	if age >= cloudSyncStaleAfter {
		state = cloudSyncStale
	}
	switch {
	case age < time.Minute:
		text = httpx.T(locale, "status.cloud_sync.just_now")
	case age < cloudSyncRelativeFor:
		text = httpx.TM(locale, "status.cloud_sync.minutes_ago_plural", map[string]any{"count": int(age / time.Minute)})
	default:
		text = fmt.Sprintf(httpx.T(locale, "status.cloud_sync.at"), httpx.FormatShortDateTime(last.Local(), now.Local(), locale))
	}
	return state, text
}

// cloudSyncChipShown: the chip appears only on a main till with its own cloud
// credential whose cloud sync runs (paid, ADR-0148 §1), and never beside the
// refused-credential chip (cloud_auth_chip.go). Local reads only.
func cloudSyncChipShown(r *http.Request, d *common.Deps) bool {
	if d == nil || d.Settings == nil || d.Cfg == nil {
		return false
	}
	ctx := r.Context()
	if d.SyncPrimaryURL(ctx) != "" || !enroll.HasCredentials(d.Cfg) {
		return false
	}
	if !entitlement.SyncAllowed(ctx, d.Settings) {
		return false
	}
	if _, replaced := tillReplaced(r, d); replaced {
		return false
	}
	// A refused credential is the heal chip's (or the lock-out chip's) to say,
	// never "check the internet connection" (ADR-0217, AC12).
	return !cloudAuthChipStatus().Show && !healChipStatus().ShowsChip()
}

// renderCloudSyncChip writes the chip (or an empty 200). result is an
// optional one-line outcome under a Sync now press (already translated).
func renderCloudSyncChip(w http.ResponseWriter, r *http.Request, d *common.Deps, result string) {
	if !cloudSyncChipShown(r, d) {
		w.WriteHeader(http.StatusOK)
		return
	}
	locale := httpx.ResolveLocale(w, r)
	cs := enroll.CurrentCardStatus(r.Context(), d.Settings)
	state, text := cloudSyncText(locale, cs.LastCheckin, cloudSyncChipNow())
	title := httpx.T(locale, "status.cloud_sync.hint")
	if state == cloudSyncStale {
		title = httpx.T(locale, "status.cloud_sync.stale_hint")
	}
	httpx.RenderPartial("ui/partials/cloud_sync_chip.html", map[string]any{
		"state":   state,
		"text":    text,
		"title":   title,
		"result":  result,
		"canSync": canPerform(d, r, "settings"),
	})(w, r)
}

func registerCloudSyncChip(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/cloud-sync-chip", func(w http.ResponseWriter, r *http.Request) {
		renderCloudSyncChip(w, r, d, "")
	})
	mux.HandleFunc("POST /api/cloud/sync-now", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = r.ParseForm()
		// Gate first: a till with no chip has nothing to sync, so it never
		// asks for a PIN it would then ignore (no kick, empty chip).
		if !cloudSyncChipShown(r, d) {
			w.WriteHeader(http.StatusOK)
			return
		}
		elev := checkOrElevate(d, r, "settings", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/cloud/sync-now", "#sb-cloud-sync-mount",
				httpx.T(locale, "elevation.summary.cloud_sync_now"), nil, elev)
			return
		}
		res, err := operatorCheckinAndWait(r.Context(), d, enroll.KeyLastCheckinAt, refreshEntitlementWait)
		if err != nil && !errors.Is(err, context.Canceled) { // a closed tab is not a fault
			logging.L().Warnf("cloud sync-now: %v", err)
		}
		result := ""
		switch res {
		case checkinAnswered:
			w.Header().Set("HX-Trigger", "ut-cloud-synced")
		case checkinTimedOut:
			result = httpx.T(locale, "status.cloud_sync.still_trying")
		} // checkinThrottled: show the chip as it is, no line
		renderCloudSyncChip(w, r, d, result)
	})
}
