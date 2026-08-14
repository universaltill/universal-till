package pages

// ADR-0048 (ut-docs#715): the German TSE hard-gate's operator surface —
// owner-only fiscal settings toggles, the owner override grant for a
// configured-but-failing TSE, and the persistent override banner fragment.
// The gate itself lives in internal/fiscal and is enforced inside
// completeTender (pos_api.go); nothing here touches Engine/KioskEngine and
// no route here is under /self-order (ADR-0020 unaffected).

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// isAdminOrAuthOff gates owner-only fiscal settings: admin or super_admin,
// deliberately NOT the manager-or-above isManagerOrAuthOff gate the rest of
// the settings surface uses (ADR-0048: "owner" maps to admin, and must not
// silently become manager-or-above). With UT_AUTH=off there is no session
// to check, so dev/CI tooling passes — same escape hatch as
// isManagerOrAuthOff (settings_page.go).
func isAdminOrAuthOff(r *http.Request) bool {
	if auth.Disabled(os.Getenv("UT_AUTH")) {
		return true
	}
	u, ok := auth.FromContext(r.Context())
	return ok && fiscal.IsOwnerRole(u.Role)
}

// TSEOverrideRequest models the owner override grant input (ADR-0048
// Decision 3). OwnerPIN — deliberately not "AdminPIN", to avoid colliding
// with ADR-0045 §3's unrelated TSE-device Admin PUK/PIN — lets an admin
// authorize in place on a cashier's session, same shape as the negative-
// inventory override's ManagerPIN.
type TSEOverrideRequest struct {
	Reason          string `json:"reason"`
	Acknowledgement string `json:"acknowledgement"`
	DurationMinutes int    `json:"duration_minutes"`
	OwnerPIN        string `json:"owner_pin"`
}

// TSEOverrideResponse is the grant result envelope payload.
type TSEOverrideResponse struct {
	Until   string `json:"until"`
	Reason  string `json:"reason"`
	Success bool   `json:"success"`
}

// respondFiscalError writes the { data: null, error } envelope (JSON) or an
// error div (HTML), matching the inventory override's dual-format shape.
func respondFiscalError(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, status, map[string]any{"data": nil, "error": message})
	} else {
		writeHTML(w, status, fmt.Sprintf("<div class='error'>%s</div>", message))
	}
}

// GrantTSEOverride handles POST /api/fiscal/tse-override (ADR-0048
// Decision 3): owner-role (admin/super_admin) only, typed acknowledgement,
// non-empty reason, duration capped at 8h (rejected above the cap, not
// clamped), audit-logged, stored as the settings-key window the gate and
// banner both read.
func GrantTSEOverride(dp *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		locale := httpx.ResolveLocale(w, r)
		repo := data.NewPOSRepo(dp.Db)

		// Never reachable from the never-configured state — checked before
		// anything else, so a direct call while never-configured cannot
		// grant regardless of role or PIN (the literal ut-docs#715
		// acceptance criterion). Same refusal the gate's own hard block
		// shows.
		configured := false
		if v, ok, err := dp.Settings.Get(ctx, fiscal.KeyTSEConfigured); err != nil {
			respondFiscalError(w, r, http.StatusInternalServerError, err.Error())
			return
		} else if ok {
			configured, _ = strconv.ParseBool(strings.TrimSpace(v))
		}
		if !configured {
			respondFiscalError(w, r, http.StatusConflict, httpx.T(locale, "fiscal.block.never_configured"))
			return
		}

		var req TSEOverrideRequest
		if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				respondFiscalError(w, r, http.StatusBadRequest, "invalid JSON")
				return
			}
		} else {
			if err := r.ParseForm(); err != nil {
				respondFiscalError(w, r, http.StatusBadRequest, "invalid form data")
				return
			}
			req.Reason = r.FormValue("reason")
			req.Acknowledgement = r.FormValue("acknowledgement")
			req.OwnerPIN = r.FormValue("owner_pin")
			if d, err := strconv.Atoi(strings.TrimSpace(r.FormValue("duration_minutes"))); err == nil {
				req.DurationMinutes = d
			}
		}

		// Actor: an admin/super_admin session authorizes itself; anyone
		// else needs an owner's PIN, and that owner becomes the audit
		// actor (the requester is recorded as requested_by). The role is
		// re-read from the DB, not trusted off the session, matching the
		// inventory override.
		sessionID := getSessionUserID(r)
		actorID := ""
		requestedBy := ""
		role, found, err := repo.LookupUserRole(ctx, sessionID)
		if err != nil {
			respondFiscalError(w, r, http.StatusInternalServerError, fmt.Sprintf("auth check failed: %v", err))
			return
		}
		if found && fiscal.IsOwnerRole(role) {
			actorID = sessionID
		} else {
			requestedBy = sessionID
			if strings.TrimSpace(req.OwnerPIN) == "" {
				respondFiscalError(w, r, http.StatusForbidden, httpx.T(locale, "fiscal.error.owner_required"))
				return
			}
			svc := dp.AuthSvc
			if svc == nil { // tests wiring handlers directly
				svc = auth.NewService(dp.Db)
			}
			// AuthorizeManager shares the login lockout (no brute-force
			// oracle) but accepts managers — the owner check below is what
			// enforces ADR-0048's admin-only rule, so a manager's PIN
			// authenticates and is then rejected.
			approver, err := svc.AuthorizeManager(ctx, req.OwnerPIN)
			switch {
			case errors.Is(err, auth.ErrLockedOut):
				respondFiscalError(w, r, http.StatusForbidden, httpx.T(locale, "auth.error.locked"))
				return
			case errors.Is(err, auth.ErrInvalidPIN):
				respondFiscalError(w, r, http.StatusForbidden, httpx.T(locale, "fiscal.error.owner_pin_invalid"))
				return
			case err != nil:
				respondFiscalError(w, r, http.StatusInternalServerError, "owner pin check failed")
				return
			}
			if !fiscal.IsOwnerRole(approver.Role) {
				respondFiscalError(w, r, http.StatusForbidden, httpx.T(locale, "fiscal.error.owner_required"))
				return
			}
			actorID = approver.ID
		}

		reason := strings.TrimSpace(req.Reason)
		if reason == "" {
			respondFiscalError(w, r, http.StatusBadRequest, httpx.T(locale, "fiscal.error.reason_required"))
			return
		}
		// Typed acknowledgement: exact, case-sensitive match after trimming
		// surrounding whitespace (ADR-0048: a typed phrase, not a checkbox).
		if strings.TrimSpace(req.Acknowledgement) != fiscal.ConfirmationPhrase {
			respondFiscalError(w, r, http.StatusBadRequest, httpx.T(locale, "fiscal.error.ack_mismatch"))
			return
		}
		maxMinutes := int(fiscal.MaxOverrideDuration / time.Minute)
		if req.DurationMinutes <= 0 || req.DurationMinutes > maxMinutes {
			respondFiscalError(w, r, http.StatusBadRequest, httpx.T(locale, "fiscal.error.duration_invalid"))
			return
		}

		grantedAt := time.Now().UTC()
		until := grantedAt.Add(time.Duration(req.DurationMinutes) * time.Minute)
		untilStr := until.Format(time.RFC3339)
		window := map[string]string{
			fiscal.KeyOverrideUntil:  untilStr,
			fiscal.KeyOverrideReason: reason,
			fiscal.KeyOverrideActor:  actorID,
		}
		if err := dp.Settings.SetMany(ctx, window); err != nil {
			respondFiscalError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		// Audit the grant (actor, reason, timestamp, window — readable off
		// the payload directly, ADR-0048 Decision 3). An unaudited override
		// must not stand: if the audit write fails, the window is cleared
		// again and the grant fails.
		if err := repo.InsertAudit(ctx, nil, actorID, "fiscal_override", uuid.NewString(), "grant", map[string]any{
			"actor":            actorID,
			"reason":           reason,
			"granted_at":       grantedAt.Format(time.RFC3339),
			"duration_minutes": req.DurationMinutes,
			"until":            untilStr,
			"requested_by":     requestedBy,
		}, grantedAt.Format(time.RFC3339), ""); err != nil {
			_ = dp.Settings.SetMany(ctx, map[string]string{
				fiscal.KeyOverrideUntil:  "",
				fiscal.KeyOverrideReason: "",
				fiscal.KeyOverrideActor:  "",
			})
			respondFiscalError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			writeJSON(w, http.StatusOK, map[string]any{
				"data":  TSEOverrideResponse{Until: untilStr, Reason: reason, Success: true},
				"error": nil,
			})
			return
		}
		writeHTML(w, http.StatusOK, fmt.Sprintf("<div class='success'>%s</div>",
			fmt.Sprintf(httpx.T(locale, "settings.fiscal.override.granted"), untilStr)))
	}
}

// setFiscalFlag handles one owner-only boolean fiscal settings toggle,
// auditing every write with {actor, from, to} (entity_type fiscal_settings).
// For fiscal.system_of_record the audit is an ADR-0048 Decision 1
// requirement (the toggle is an owner-controlled declaration of legal
// posture, and the trail must honestly show every transition);
// fiscal.tse_configured gets the same treatment so the fiscal trail is
// complete. If the audit write fails the value is restored — no unaudited
// transition can stand.
func setFiscalFlag(dp *common.Deps, key, auditAction string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isAdminOrAuthOff(r) {
			http.Error(w, "owner (admin) role required", http.StatusForbidden)
			return
		}
		ctx := r.Context()
		_ = r.ParseForm()
		enabled := r.Form.Get("enabled") == "true"

		oldRaw, _, err := dp.Settings.Get(ctx, key)
		if err != nil {
			http.Error(w, "could not read current value", http.StatusInternalServerError)
			return
		}
		oldVal, _ := strconv.ParseBool(strings.TrimSpace(oldRaw))
		if err := dp.Settings.Set(ctx, key, strconv.FormatBool(enabled)); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		actor := auth.UserID(r)
		if err := data.NewPOSRepo(dp.Db).InsertAudit(ctx, nil, actor, "fiscal_settings", key, auditAction, map[string]any{
			"actor": actor,
			"from":  oldVal,
			"to":    enabled,
		}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
			_ = dp.Settings.Set(ctx, key, oldRaw)
			http.Error(w, "could not audit", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// registerFiscalAPI registers the ADR-0048 fiscal routes. /api/fiscal/* is
// behind the auth middleware like every non-kiosk API; /ui/… is the same
// htmx-fragment namespace the sync chip uses (denylisted from help-topic
// route coverage).
func registerFiscalAPI(mux *http.ServeMux, dp *common.Deps) {
	mux.HandleFunc("POST /api/fiscal/tse-override", GrantTSEOverride(dp))
	mux.HandleFunc("POST /api/fiscal/system-of-record", setFiscalFlag(dp, fiscal.KeySystemOfRecord, "system_of_record_changed"))
	mux.HandleFunc("POST /api/fiscal/tse-configured", setFiscalFlag(dp, fiscal.KeyTSEConfigured, "tse_configured_changed"))

	// Persistent override banner (ADR-0048 Decision 3): same fragment
	// pattern as the sync chip (nav.html polls it), empty when no window
	// is active, so expiry clears it on the next poll with nobody having
	// to reset anything.
	mux.HandleFunc("GET /ui/fiscal-override-banner", func(w http.ResponseWriter, r *http.Request) {
		ov, err := fiscal.ActiveOverride(r.Context(), dp.Settings, time.Now().UTC())
		if err != nil || !ov.Active {
			w.WriteHeader(http.StatusOK)
			return
		}
		httpx.RenderPartial("ui/partials/fiscal_override_banner.html", map[string]any{
			"Reason": ov.Reason,
			"Until":  ov.Until.Format("2006-01-02 15:04"),
		})(w, r)
	})
}
