package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// TSEOverrideRequest models the owner override for a configured-but-failing
// TSE (ADR-0048 Decision 3, ut-docs#715). OwnerPIN mirrors the inventory
// override's ManagerPIN shape so a cashier can request it and an admin can
// authorize it in place, without a role switch — named owner_pin (not
// admin_pin) deliberately, to avoid colliding with ADR-0045 §3's unrelated
// "Admin PUK/PIN custody", which is the TSE device's own admin PIN.
type TSEOverrideRequest struct {
	Reason string `json:"reason"`
	// Acknowledgement must exactly match fiscal.OverrideAcknowledgement —
	// a typed confirmation phrase, deliberately not a checkbox.
	Acknowledgement string `json:"acknowledgement"`
	DurationMinutes int    `json:"duration_minutes"`
	OwnerPIN        string `json:"owner_pin"`
}

// TSEOverrideResponse is the grant confirmation.
type TSEOverrideResponse struct {
	Granted bool   `json:"granted"`
	Until   string `json:"until"`
	Actor   string `json:"actor"`
}

// registerFiscalAPI registers the fiscal endpoints (ADR-0048).
func registerFiscalAPI(mux *http.ServeMux, dp *common.Deps) {
	mux.HandleFunc("POST /api/fiscal/tse-override", createTSEOverride(dp))
}

// createTSEOverride handles POST /api/fiscal/tse-override — modeled on
// CreateNegativeInventoryOverride (inventory_api.go), adapted to owner
// (admin/super_admin) only.
func createTSEOverride(dp *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		repo := data.NewPOSRepo(dp.Db)

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
			if v := r.FormValue("duration_minutes"); v != "" {
				if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
					req.DurationMinutes = n
				}
			}
		}

		// FIRST, before roles, PINs or any validation: the override must be
		// unreachable from the never-configured state, in code — a direct
		// API call with a perfectly valid body and an admin PIN is refused
		// here all the same (ADR-0048 Decision 3; same hard-block error the
		// tender gate itself raises).
		configured, _, err := fiscalSettingsReader(dp).Get(ctx, fiscal.KeyTSEConfigured)
		if err != nil {
			respondFiscalError(w, r, http.StatusInternalServerError, fmt.Sprintf("settings read failed: %v", err))
			return
		}
		if v := strings.ToLower(strings.TrimSpace(configured)); v != "true" && v != "1" && v != "on" {
			respondFiscalError(w, r, http.StatusConflict, (&fiscalNeverConfiguredError{}).Error())
			return
		}

		// Actor: an owner (admin/super_admin) session authorizes itself; any
		// other role needs an owner's PIN, and that owner becomes the audit
		// actor. canPerform consults the fiscal_tse_override permission,
		// seeded to admin/super_admin ONLY (migration 046) — deliberately
		// NOT the manager-inclusive pattern the other action grants follow.
		requestedBy := getSessionUserID(r)
		if requestedBy == "" {
			respondFiscalError(w, r, http.StatusUnauthorized, "authentication required")
			return
		}
		actorID := requestedBy
		if !canPerform(dp, r, "fiscal_tse_override") {
			if req.OwnerPIN == "" {
				respondFiscalError(w, r, http.StatusForbidden, "owner (admin) approval required")
				return
			}
			svc := dp.AuthSvc
			if svc == nil { // tests wiring handlers directly
				svc = auth.NewService(dp.Db)
			}
			// AuthorizeManager provides the PIN-lookup mechanics (shared
			// login lockout — this endpoint is no brute-force oracle), but
			// "PIN authenticated" alone is NOT "is the owner": it accepts a
			// manager's PIN, so the authenticated user's actual role is
			// checked to be admin/super_admin afterwards (ADR-0048: this
			// must not silently become manager-or-above).
			approver, err := svc.AuthorizeManager(ctx, req.OwnerPIN)
			switch {
			case errors.Is(err, auth.ErrLockedOut):
				respondFiscalError(w, r, http.StatusForbidden, "too many attempts")
				return
			case errors.Is(err, auth.ErrInvalidPIN):
				respondFiscalError(w, r, http.StatusForbidden, "owner pin not recognised")
				return
			case err != nil:
				respondFiscalError(w, r, http.StatusInternalServerError, "owner pin check failed")
				return
			}
			if approver.Role != "admin" && approver.Role != "super_admin" {
				respondFiscalError(w, r, http.StatusForbidden, "owner (admin) approval required")
				return
			}
			actorID = approver.ID
		}

		// Validate input.
		if strings.TrimSpace(req.Reason) == "" {
			respondFiscalError(w, r, http.StatusBadRequest, "reason required")
			return
		}
		if req.Acknowledgement != fiscal.OverrideAcknowledgement {
			respondFiscalError(w, r, http.StatusBadRequest,
				fmt.Sprintf("acknowledgement must read exactly: %q", fiscal.OverrideAcknowledgement))
			return
		}
		if req.DurationMinutes < 1 {
			respondFiscalError(w, r, http.StatusBadRequest, "duration_minutes must be at least 1")
			return
		}
		if req.DurationMinutes > fiscal.MaxOverrideMinutes {
			// Rejected, never silently clamped.
			respondFiscalError(w, r, http.StatusBadRequest,
				fmt.Sprintf("duration_minutes must be at most %d (8 hours)", fiscal.MaxOverrideMinutes))
			return
		}

		now := time.Now().UTC()
		until := now.Add(time.Duration(req.DurationMinutes) * time.Minute)
		store := dp.Settings
		if store == nil {
			respondFiscalError(w, r, http.StatusInternalServerError, "settings store unavailable")
			return
		}
		if err := store.SetMany(ctx, map[string]string{
			fiscal.KeyOverrideUntil:  until.Format(time.RFC3339),
			fiscal.KeyOverrideReason: strings.TrimSpace(req.Reason),
			fiscal.KeyOverrideActor:  actorID,
		}); err != nil {
			respondFiscalError(w, r, http.StatusInternalServerError, fmt.Sprintf("could not store override: %v", err))
			return
		}

		// One-time grant audit entry — granted_at/duration recorded
		// explicitly in the payload (not just derivable from the row's own
		// timestamp), so actor/reason/timestamp/window read directly off it.
		payload := map[string]any{
			"actor":            actorID,
			"reason":           strings.TrimSpace(req.Reason),
			"granted_at":       now.Format(time.RFC3339),
			"duration_minutes": req.DurationMinutes,
			"until":            until.Format(time.RFC3339),
		}
		if requestedBy != actorID {
			payload["requested_by"] = requestedBy
		}
		if err := repo.InsertAudit(ctx, nil, actorID, "fiscal_override", "tse", "grant",
			payload, now.Format(time.RFC3339), ""); err != nil {
			respondFiscalError(w, r, http.StatusInternalServerError, fmt.Sprintf("could not audit override: %v", err))
			return
		}

		respondFiscalSuccess(w, r, TSEOverrideResponse{
			Granted: true,
			Until:   until.Format(time.RFC3339),
			Actor:   actorID,
		})
	}
}

// respondFiscalError writes the { "data": null, "error": … } envelope for
// JSON callers, an HTML fragment otherwise (same convention as
// respondOverrideError in inventory_api.go).
func respondFiscalError(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, status, map[string]any{"data": nil, "error": message})
	} else {
		writeHTML(w, status, fmt.Sprintf("<div class='error'>%s</div>", message))
	}
}

// respondFiscalSuccess writes the { "data": …, "error": null } envelope.
func respondFiscalSuccess(w http.ResponseWriter, r *http.Request, data TSEOverrideResponse) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "error": nil})
	} else {
		writeHTML(w, http.StatusOK, fmt.Sprintf("<div class='success'>Override active until %s</div>", data.Until))
	}
}
