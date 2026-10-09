package pages

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// POST /api/settings/report-retention — the report retention mode
// (ADR-0040 §1 as amended by ADR-0147 §1, ut-docs#574). Kept out of
// eod_api.go on purpose: the cloud/both gate reads the cached entitlement,
// and the end-of-day file is on ADR-0060's sale path, which must never
// import internal/entitlement. Choosing a mode never touches a close.

// reportRetentionCloudAllowed is ADR-0147 §1's gate for choosing cloud or
// both: the same fail-closed EffectivePlan read every paid capability uses.
func reportRetentionCloudAllowed(ctx context.Context, d *common.Deps, now time.Time) bool {
	return entitlement.Allows(entitlement.EffectivePlan(ctx, d.Settings, now), entitlement.CapCloudBackup)
}

// registerReportRetentionModeAPI mounts the mode setting; called from
// registerReportArchiveAPI so the route wiring is unchanged.
func registerReportRetentionModeAPI(mux *http.ServeMux, d *common.Deps, repo *data.POSRepo) {
	// Mode setting (ADR-0147 §1, which amends ADR-0040 §1): the mode stays
	// till-owned. It is written here, shop-wide through the main till on an
	// additional till (#2997), and only reported read-only to the cloud
	// (#3390, cloudsync_wire.go reportedReadOnlyTillSettingKeys) — choosing
	// it decides when local legal records are deleted, so the confirmation
	// and the elevation stay at the till. "till" is always allowed;
	// "cloud"/"both" only while this till's cached entitlement allows
	// cloud_backup (fail closed: no cache, stale past grace, lapsed → 409).
	// A later lapse never resets the mode: uploads are refused, the status
	// chip shows, and nothing becomes prune-eligible (ADR-0040 §6).
	mux.HandleFunc("POST /api/settings/report-retention", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		mode := strings.TrimSpace(r.Form.Get("mode"))
		switch mode {
		case common.ReportRetentionModeTill:
		case common.ReportRetentionModeCloud, common.ReportRetentionModeBoth:
			// Re-saving the mode already in force is not choosing it: a
			// lapsed shop keeps its mode (ADR-0147 §1), and its card still
			// submits that mode, so the gate applies only to a change.
			current, _, _ := d.Settings.Get(r.Context(), common.KeyReportRetentionMode)
			if strings.TrimSpace(current) != mode && !reportRetentionCloudAllowed(r.Context(), d, time.Now()) {
				httpx.RefuseText(w, httpx.T(httpx.ResolveLocale(w, r), "settings.retention.err_needs_subscription"), http.StatusConflict)
				return
			}
		default:
			httpx.RefuseText(w, httpx.T(httpx.ResolveLocale(w, r), "settings.retention.err_invalid_mode"), http.StatusBadRequest)
			return
		}
		// Mutating + audit-writing (ut-docs#794): validated above, gated
		// below, same as every other site in this file. hx-swap="none" here
		// too (settings.html reloads the page itself on success via
		// hx-on::after-request) — HxTarget points at a dedicated
		// #retention-msg span added purely for the elevation retry.
		elev := checkOrElevate(d, r, "eod_report", r.Form.Get("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			modeLabel := httpx.T(locale, fmt.Sprintf("settings.retention.mode_%s", mode))
			renderElevationPrompt(w, r, "/api/settings/report-retention", "#retention-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.report_retention"), modeLabel),
				[]elevationHiddenField{{Name: "mode", Value: mode}}, elev)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}
		// ut-docs#2997: shop-wide -- through the main till on an additional
		// till; a refusal writes nothing and is not audited.
		if err := saveShopSettings(r.Context(), d, elev, map[string]string{common.KeyReportRetentionMode: mode}); err != nil {
			if respondSettingsSyncError(w, r, err) {
				return
			}
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "eod.err.retention_save_failed", "eod_retention_save", err)
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		payload := map[string]any{"mode": mode}
		if elev.Outcome == elevated {
			_ = repo.InsertAuditElevated(r.Context(), nil, actorID, elev.ActorID, "report", "-", "report_retention_mode_changed", payload, now, "")
			// ut-docs#794 review finding (should-fix): same reasoning as
			// /api/settings/eod above — a 204 never swaps under htmx, so the
			// dialog retry (no reload of its own, unlike the plain-session
			// form below) needs a real body to confirm anything happened.
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(httpx.ResolveLocale(w, r), "elevation.approved"))
			return
		}
		_ = repo.InsertAudit(r.Context(), nil, actorID, "report", "-", "report_retention_mode_changed", payload, now, "")
		w.WriteHeader(http.StatusNoContent)
	})
}
