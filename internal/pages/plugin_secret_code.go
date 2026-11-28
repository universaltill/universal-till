package pages

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pluginsecretapply"
)

// The till side of the one-time key delivery code (ut-docs#6960, ADR-0213
// Decision 9): a plugin's settings page whose plugin has an allow-listed
// secret (declared in the plugin manifest, pluginsecretapply.SecretKeysFor) offers "Scan code" / "Type code". The code is
// posted to the cloud through the main till's device credential; the key
// then arrives as an ordinary set_plugin_secret directive. The code is never
// stored, logged or echoed; every refusal is the same answer.

// redeemKeyCode is the cloud call (a seam for tests).
var redeemKeyCode = cloudsync.RedeemKeyDeliveryCode

// secretCodeView is the template's view of the row.
type secretCodeView struct {
	Enabled   bool // the plugin has an allow-listed secret
	Satellite bool // not the main till: no buttons, an explanation
	KeySet    bool // a key is already stored (never its value)
}

// secretKeyOf is the one declared global secret setting of a plugin the key
// delivery code can fill: from the installed manifest (display only; the
// delivery itself is applied against the VERIFIED manifest). A plugin that
// declares none, or several, offers no code entry.
func secretKeyOf(r *http.Request, d *common.Deps, pluginID string) (string, bool) {
	seam := pluginsecretapply.ManifestFunc(func(ctx context.Context, id string) (pluginsecretapply.Declared, error) {
		m, installed, err := plugins.InstalledManifest(ctx, d.Db, id)
		if err != nil || !installed {
			return pluginsecretapply.Declared{}, err
		}
		return pluginsecretapply.DeclaredFrom(m), nil
	})
	keys := pluginsecretapply.SecretKeysFor(r.Context(), seam, pluginID)
	if len(keys) != 1 {
		return "", false
	}
	return keys[0], true
}

func secretCodeRow(r *http.Request, d *common.Deps, repo *data.PluginRepo, pluginID string) secretCodeView {
	key, ok := secretKeyOf(r, d, pluginID)
	if !ok {
		return secretCodeView{}
	}
	v := secretCodeView{Enabled: true, Satellite: d.SyncPrimaryURL(r.Context()) != ""}
	v.KeySet = secretKeyIsSet(r, repo, pluginID, key)
	return v
}

func secretKeyIsSet(r *http.Request, repo *data.PluginRepo, pluginID, key string) bool {
	val, found, err := repo.GetPluginSetting(r.Context(), pluginID, key)
	if err != nil || !found {
		return false
	}
	return strings.Trim(strings.TrimSpace(val), `"`) != ""
}

func registerPluginSecretCode(mux *http.ServeMux, d *common.Deps, repo *data.PluginRepo) {
	mux.HandleFunc("POST /api/plugins/{id}/secret-code/redeem", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !canPerform(d, r, "plugin_management") {
			writeSecretCode(w, http.StatusForbidden, "forbidden")
			return
		}
		if _, ok := secretKeyOf(r, d, r.PathValue("id")); !ok {
			writeSecretCode(w, http.StatusNotFound, "not_found")
			return
		}
		if d.SyncPrimaryURL(r.Context()) != "" {
			writeSecretCode(w, http.StatusConflict, "satellite")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
		code := r.PostFormValue("code")
		if utf8.RuneCountInString(code) > 64 {
			writeSecretCode(w, http.StatusNotFound, "bad_code")
			return
		}
		norm, ok := cloudsync.NormalizeKeyDeliveryCode(code)
		if !ok {
			// A malformed code never leaves the till; same answer as a refusal.
			writeSecretCode(w, http.StatusNotFound, "bad_code")
			return
		}
		out, err := redeemKeyCode(r.Context(), d.Cfg, norm)
		if err != nil {
			// The error never carries the code.
			logging.L().Warnf("plugin secret code: redeem call failed: %v", err)
		}
		switch out {
		case cloudsync.RedeemAccepted:
			if d.CloudSyncNow != nil {
				select {
				case d.CloudSyncNow <- struct{}{}:
				default:
				}
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"state": "waiting"}, "error": nil})
		case cloudsync.RedeemRateLimited:
			writeSecretCode(w, http.StatusTooManyRequests, "rate_limited")
		case cloudsync.RedeemOffline, cloudsync.RedeemUnavailable:
			writeSecretCode(w, http.StatusServiceUnavailable, "offline")
		default:
			writeSecretCode(w, http.StatusNotFound, "bad_code")
		}
	})

	// The page polls this while "Waiting for the key": whether a key is
	// stored. Never the key, never a hash or length.
	mux.HandleFunc("GET /api/plugins/{id}/secret-code/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if !canPerform(d, r, "plugin_management") {
			writeSecretCode(w, http.StatusForbidden, "forbidden")
			return
		}
		key, ok := secretKeyOf(r, d, r.PathValue("id"))
		if !ok {
			writeSecretCode(w, http.StatusNotFound, "not_found")
			return
		}
		state := "empty"
		if secretKeyIsSet(r, repo, r.PathValue("id"), key) {
			state = "set"
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"state": state}, "error": nil})
	})
}

func writeSecretCode(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"data": nil, "error": map[string]string{"code": code}})
}
