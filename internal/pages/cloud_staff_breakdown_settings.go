package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerCloudStaffBreakdownSetting wires POST /api/settings/cloud-staff-breakdown
// (ut-docs#2557, ADR-0111 §4): the shop-wide switch for sending per-staff-ID
// sales figures (by_cashier) in the cloud sales aggregates. Off by default;
// only the exact stored value "true" enables it.
//
// Manager-only with a flat 403 and deliberately no PIN-elevation path: the
// card is never rendered for a cashier, and staff-monitoring is flipped by a
// manager in their own session. On an additional till the write goes through
// saveShopSettings to the main till, which re-checks the grant.
func registerCloudStaffBreakdownSetting(mux *http.ServeMux, d *common.Deps, posRepo *data.POSRepo) {
	mux.HandleFunc("POST /api/settings/cloud-staff-breakdown", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		elev := elevationCheck{Outcome: allowed, ActorID: getSessionUserID(r)}
		v := "false"
		switch r.Form.Get("enabled") {
		case "on", "1", "true":
			v = "true"
		}
		if err := saveShopSettings(r.Context(), d, elev, map[string]string{data.CloudStaffBreakdownKey: v}); err != nil {
			if respondSettingsSyncError(w, r, err) {
				return
			}
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "settings.error.save_failed", "settings_cloud_staff", err)
			return
		}
		settingsAudit(r, posRepo, elev, "settings", data.CloudStaffBreakdownKey, "cloud_staff_breakdown_changed", map[string]any{"enabled": v})
		settingsRespondSaved(w, r, elev)
	})
}
