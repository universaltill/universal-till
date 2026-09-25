package pages

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerSyncCloudDevice mounts the main-till side of a replica's own cloud
// device identity (ut-docs#2730): POST /api/sync/cloud-device. The replica
// authenticates with its sync bearer (syncTill, like every other /api/sync
// pull; middleware-exempt for that reason) and sends only its own device id +
// version. The main till registers that device under the store with the
// store token only it holds (enroll.VouchForReplica) and answers with the
// store id and device id — never a credential (enroll.Vouch has no token
// field), so no cloud token ever crosses the LAN. The till id and name sent
// to the cloud come from this till's own tills table — never from the
// replica's body.
//
// Errors are machine-to-machine codes (the replica logs and retries); no
// operator ever reads them.
// cloudDeviceVouchMax caps accepted vouch requests per authenticated till
// per cloudDeviceVouchWindow. Each one is a cloud call the main till makes
// with its own token, so this bounds what a replica can make it do
// (reviewer finding, ut-docs#2730). The replica's own loop asks once per
// boot, then every 6 h, with a 30 s..30 min backoff on failure — a handful
// per window even with a few "Register now" presses on top.
const (
	cloudDeviceVouchMax    = 10
	cloudDeviceVouchWindow = 10 * time.Minute
)

func registerSyncCloudDevice(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	// Keyed by the authenticated till id, not the source address: one
	// noisy replica never blocks its siblings, and an unknown bearer is
	// refused before it can spend anyone's budget.
	limiter := newPairRateLimiter(cloudDeviceVouchWindow, cloudDeviceVouchMax)
	fail := func(w http.ResponseWriter, status int, code string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nil, "error": code})
	}
	mux.HandleFunc("POST /api/sync/cloud-device", func(w http.ResponseWriter, r *http.Request) {
		till, ok := syncTill(r, tills)
		if !ok {
			fail(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if !limiter.allow(till.ID) {
			fail(w, http.StatusTooManyRequests, "too_many_requests")
			return
		}
		// Only the main till holds the store identity; a replica that was
		// promoted/demoted mid-flight must not answer for it.
		if d.SyncPrimaryURL(r.Context()) != "" {
			fail(w, http.StatusConflict, "not_main_till")
			return
		}
		var in struct {
			DeviceID string `json:"device_id"`
			Version  string `json:"version"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, "invalid_request")
			return
		}
		vouch, err := enroll.VouchForReplica(r.Context(), d.Cfg, enroll.ReplicaRequest{
			TillID: till.ID, DeviceID: in.DeviceID, DeviceName: till.Name, Version: in.Version,
		})
		switch {
		case err == nil:
		case errors.Is(err, enroll.ErrBadDeviceRequest):
			fail(w, http.StatusBadRequest, "invalid_device")
			return
		case errors.Is(err, enroll.ErrNotRegistered):
			fail(w, http.StatusConflict, "main_till_not_registered")
			return
		default:
			logging.L().Warnf("cloud device registration for replica till %s: %v", till.ID, err)
			fail(w, http.StatusBadGateway, "cloud_unavailable")
			return
		}
		// ut-docs#2792: entitlement.* is per-till in the admin sync, and
		// a replica with no store token can't ask the cloud — relay ours.
		if c, ok := entitlement.ReadCached(r.Context(), d.Settings); ok {
			vouch.Entitlement = &c
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": vouch, "error": nil})
	})
}
