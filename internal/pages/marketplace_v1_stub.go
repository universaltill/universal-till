package pages

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"

	"github.com/universaltill/universal-till/internal/pages/common"
)

type installIntentPayload struct {
	PluginID   string `json:"plugin_id"`
	Version    string `json:"version"`
	MerchantID string `json:"merchant_id"`
	DeviceID   string `json:"device_id,omitempty"`
}

type bundlePayload struct {
	PluginID   string `json:"plugin_id"`
	Version    string `json:"version"`
	Checksum   string `json:"checksum"`
	MerchantID string `json:"merchant_id"`
	DeviceID   string `json:"device_id,omitempty"`
}

type telemetryPayload struct {
	PluginID   string `json:"plugin_id"`
	Version    string `json:"version"`
	MerchantID string `json:"merchant_id"`
	State      string `json:"state"`
	Error      any    `json:"error"`
	Timestamp  string `json:"timestamp"`
}

func registerMarketplaceV1Stub(mux *http.ServeMux, d *common.Deps) {
	if d == nil {
		return
	}
	if d.Cfg == nil || (!d.Cfg.DevMode && os.Getenv("UT_ENABLE_MARKETPLACE_STUB") != "true") {
		return
	}
	mux.HandleFunc("/v1/install/intents", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var payload installIntentPayload
		if err := decodeJSONBody(r, &payload); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if payload.PluginID == "" || payload.Version == "" || payload.MerchantID == "" {
			writeJSONResponse(w, http.StatusBadRequest, map[string]any{"error": "plugin_id, version, and merchant_id are required"})
			return
		}
		writeJSONResponse(w, http.StatusCreated, payload)
	})

	mux.HandleFunc("/v1/install/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		pluginID := r.URL.Query().Get("plugin_id")
		merchantID := r.URL.Query().Get("merchant_id")
		if pluginID == "" || merchantID == "" {
			writeJSONResponse(w, http.StatusBadRequest, map[string]any{"error": "plugin_id and merchant_id are required"})
			return
		}
		writeJSONResponse(w, http.StatusOK, map[string]any{
			"plugin_id":   pluginID,
			"merchant_id": merchantID,
			"status":      "pending",
		})
	})

	mux.HandleFunc("/v1/install/bundles/export", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var payload bundlePayload
		if err := decodeJSONBody(r, &payload); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if payload.PluginID == "" || payload.Version == "" || payload.Checksum == "" || payload.MerchantID == "" {
			writeJSONResponse(w, http.StatusBadRequest, map[string]any{"error": "plugin_id, version, checksum, and merchant_id are required"})
			return
		}
		writeJSONResponse(w, http.StatusOK, payload)
	})

	mux.HandleFunc("/v1/install/bundles/import", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var payload bundlePayload
		if err := decodeJSONBody(r, &payload); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if payload.PluginID == "" || payload.Version == "" || payload.Checksum == "" || payload.MerchantID == "" {
			writeJSONResponse(w, http.StatusBadRequest, map[string]any{"error": "plugin_id, version, checksum, and merchant_id are required"})
			return
		}
		writeJSONResponse(w, http.StatusAccepted, map[string]any{
			"status":  "accepted",
			"payload": payload,
		})
	})

	mux.HandleFunc("/v1/telemetry/plugins", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var payload telemetryPayload
		if err := decodeJSONBody(r, &payload); err != nil {
			writeJSONResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		if payload.PluginID == "" || payload.Version == "" || payload.MerchantID == "" || payload.State == "" || payload.Timestamp == "" {
			writeJSONResponse(w, http.StatusBadRequest, map[string]any{"error": "plugin_id, version, merchant_id, state, and timestamp are required"})
			return
		}
		writeJSONResponse(w, http.StatusAccepted, map[string]any{"status": "accepted"})
	})
}

func decodeJSONBody(r *http.Request, dst any) error {
	if r.Body == nil {
		return errors.New("request body required")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}
	return nil
}

func writeJSONResponse(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
