package pages

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/tills"
)

// Removing a ghost on the till (ut-docs#7098). Three pieces live here so the
// layout run (the Tills & devices tree) never has to touch them:
//   - authorizeTillRemoval: the manager gate, a signed-in manager or a manager
//     PIN typed on the spot (a cashier never removes anything unaided),
//   - the "Remove from the list" route for a live entry only the cloud list
//     holds, behind a default-off setting (the cloud has its own flag),
//   - small helpers the Others tab reads.

// settingCloudRemove switches "Remove from the list" on for this main till
// ("1" = on, absent = off, the shipped default).
const settingCloudRemove = "tills.cloud_remove_enabled"

func cloudRemoveEnabled(ctx context.Context, d *common.Deps) bool {
	if d.Settings == nil {
		return false
	}
	v, ok, err := d.Settings.Get(ctx, settingCloudRemove)
	return err == nil && ok && v == "1"
}

// registerSupersedes maps a live device id to the device id its register took
// over from (register_devices.superseded_by), for tills.MarkSameDevice.
func registerSupersedes(ctx context.Context, d *common.Deps) map[string]string {
	links, err := data.NewPOSRepo(d.Db).ListRegisterDevices(ctx)
	if err != nil {
		return nil
	}
	out := map[string]string{}
	for _, l := range links {
		if l.DeviceID != "" && l.SupersededBy != "" {
			out[l.DeviceID] = l.SupersededBy
		}
	}
	return out
}

// authorizeTillRemoval is the ONE PIN rule of every removal route (revoke,
// revoke-stale, cloud-remove, verify-pin; ut-docs#7098 + #7173, owner
// 2026-11-27): a fresh manager PIN is required whoever is signed in, so a
// person at an unlocked till cannot remove an entry without it (ADR-0087). The
// PIN comes from the form as override_pin (the inline panel) or manager_pin
// (older callers). A signed-in manager types their own PIN; a cashier session
// can remove too when a manager types theirs. The refusal is answered here and
// the acting user is the PIN's manager, so the audit names who removed it.
func authorizeTillRemoval(w http.ResponseWriter, r *http.Request, d *common.Deps) (actor string, ok bool) {
	_ = r.ParseForm()
	pin := strings.TrimSpace(r.Form.Get("override_pin"))
	if pin == "" {
		pin = strings.TrimSpace(r.Form.Get("manager_pin"))
	}
	if pin == "" {
		common.LocalizedError(w, r, http.StatusForbidden, "tills.list.error.pin_missing")
		return "", false
	}
	if d.AuthSvc == nil {
		common.LocalizedError(w, r, http.StatusForbidden, "tills.list.error.pin_wrong")
		return "", false
	}
	approver, err := d.AuthSvc.AuthorizeManager(r.Context(), pin)
	if err != nil {
		status := http.StatusForbidden
		if errors.Is(err, auth.ErrLockedOut) {
			status = http.StatusTooManyRequests
		}
		common.LocalizedError(w, r, status, "tills.list.error.pin_wrong")
		return "", false
	}
	return approver.ID, true
}

// tillsCloudRemoveCall is a var so tests can stand in for the cloud.
var tillsCloudRemoveCall = enroll.RemoveFromAccount

// registerTillsCloudRemove wires POST /api/sync/tills/cloud-remove (form
// device_id, optional manager_pin). Main till only. The id must be an entry of
// the account list read right now that has no local row, so a forged id never
// reaches the cloud. Offline or any failure changes nothing, queues nothing.
func registerTillsCloudRemove(mux *http.ServeMux, d *common.Deps) {
	posRepo := data.NewPOSRepo(d.Db)
	repo := data.NewTillsRepo(d.Db)
	mux.HandleFunc("POST /api/sync/tills/cloud-remove", func(w http.ResponseWriter, r *http.Request) {
		// Cheap checks first, so a replica or a flag-off till never burns
		// manager PIN attempts (lockout counter) on a call that cannot work.
		if d.SyncPrimaryURL(r.Context()) != "" {
			http.Error(w, "remove must be done on the primary till", http.StatusConflict)
			return
		}
		if !cloudRemoveEnabled(r.Context(), d) {
			common.LocalizedError(w, r, http.StatusNotFound, "tills.list.cloud_only.wait")
			return
		}
		actor, ok := authorizeTillRemoval(w, r, d)
		if !ok {
			return
		}
		_ = r.ParseForm()
		deviceID := strings.TrimSpace(r.Form.Get("device_id"))
		devs, fresh := accountFleet(r.Context(), d, true)
		if !fresh {
			common.LocalizedError(w, r, http.StatusConflict, "tills.list.error.account_unavailable")
			return
		}
		list, err := repo.ListTills(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "sync.error.server", "tills_cloud_remove", err)
			return
		}
		listed := false
		for _, row := range tills.Merge(tills.Source{Cloud: devs, CloudOK: true, Local: list,
			Self: tills.SelfInfo{Role: tills.RoleMain}, CloudRemoveOK: true}, time.Now(), tillsStaleAfter) {
			if row.State == tills.StateCloudOnly && row.CloudDeviceID == deviceID && deviceID != "" {
				listed = true
			}
		}
		if !listed {
			common.LocalizedError(w, r, http.StatusConflict, "tills.list.error.not_listed")
			return
		}
		if err := tillsCloudRemoveCall(r.Context(), d.Cfg, deviceID); err != nil {
			key := "tills.list.error.cloud_remove_failed"
			if errors.Is(err, enroll.ErrRemoveNotEnabled) {
				key = "tills.list.error.cloud_remove_not_enabled"
			}
			// RefuseText marks the body as the operator's own reason, so the
			// page shows it instead of the generic save-failed line.
			httpx.RefuseText(w, httpx.T(httpx.ResolveLocale(w, r), key), http.StatusBadGateway)
			return
		}
		_ = posRepo.InsertAudit(r.Context(), nil, actor, "till", deviceID, "till_cloud_removed",
			nil, time.Now().UTC().Format(time.RFC3339), "")
		// Next read of the list must not show it: drop the cache.
		fleetCache.mu.Lock()
		fleetCache.at = time.Time{}
		fleetCache.mu.Unlock()
		w.Header().Set("HX-Trigger", "tills-changed")
		w.WriteHeader(http.StatusNoContent)
	})
}
