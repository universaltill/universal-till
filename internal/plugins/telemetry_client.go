package plugins

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

// pluginStatusWire mirrors cloud.proto's PluginStatus message field-for-field
// (cloud.v1.TelemetryService.ReportPluginStatus) — no per-plugin metrics
// (crash/memory/CPU) travel over the wire; the marketplace doesn't collect
// them today.
type pluginStatusWire struct {
	PluginID         string `json:"plugin_id"`
	InstalledVersion string `json:"installed_version"`
	Status           string `json:"status"` // enabled | disabled | revoked | failed
	Source           string `json:"source"` // cloud | manual
	DeviceID         string `json:"device_id"`
}

type reportPluginStatusRequest struct {
	Statuses   []pluginStatusWire `json:"statuses"`
	MerchantID string             `json:"merchant_id"`
	StoreID    string             `json:"store_id"`
}

// TelemetryClient reports the device's current installed-plugin statuses to
// the marketplace (FR-013). One report per tick is a full snapshot of what's
// installed right now — the marketplace upserts current state per
// (device, plugin), so there's no event queue/batching to manage.
type TelemetryClient struct {
	db             *sql.DB
	marketplaceURL string
	deviceID       string
	merchantID     string
	storeID        string
	httpClient     *http.Client
	optInEnabled   bool
}

// NewTelemetryClient creates a new telemetry client.
func NewTelemetryClient(db *sql.DB, marketplaceURL, deviceID, merchantID, storeID string) *TelemetryClient {
	return &TelemetryClient{
		db:             db,
		marketplaceURL: marketplaceURL,
		deviceID:       deviceID,
		merchantID:     merchantID,
		storeID:        storeID,
		httpClient:     &http.Client{Timeout: 30 * time.Second},
	}
}

// ReportNow queries every active installed plugin and sends one status
// report covering all of them. Called on the scheduler's telemetry tick.
func (tc *TelemetryClient) ReportNow(ctx context.Context) error {
	if err := tc.loadOptInStatus(ctx); err != nil {
		return fmt.Errorf("load opt-in status: %w", err)
	}
	if !tc.optInEnabled {
		return nil
	}
	if tc.marketplaceURL == "" {
		return nil
	}
	if tc.merchantID == "" {
		// Not enrolled yet (ADR-0013: store registration is lazy/background)
		// — the server requires merchant_id and will reject an empty one.
		// Skip quietly; the next tick retries once enrolment fills it in.
		return nil
	}

	installed, err := data.NewPluginRepo(tc.db).ListInstalledPlugins(ctx)
	if err != nil {
		return fmt.Errorf("list installed plugins: %w", err)
	}
	if len(installed) == 0 {
		return nil
	}

	statuses := make([]pluginStatusWire, 0, len(installed))
	for _, p := range installed {
		statuses = append(statuses, pluginStatusWire{
			PluginID:         p.ID,
			InstalledVersion: p.Version,
			Status:           "enabled", // ListInstalledPlugins already filters is_active=1
			Source:           "cloud",
			DeviceID:         tc.deviceID,
		})
	}

	return tc.send(ctx, reportPluginStatusRequest{
		Statuses:   statuses,
		MerchantID: tc.merchantID,
		StoreID:    tc.storeID,
	})
}

// loadOptInStatus reads telemetry opt-in from settings.
func (tc *TelemetryClient) loadOptInStatus(ctx context.Context) error {
	value, ok, err := data.NewSettingsRepo(tc.db).Get(ctx, "marketplace.telemetry_opt_in")
	if err != nil {
		return err
	}
	if !ok {
		tc.optInEnabled = false
		return nil
	}
	tc.optInEnabled = (value == "true" || value == "1")
	return nil
}

// send POSTs one report to the marketplace's grpc-gateway REST bridge
// (no raw gRPC — the till has no gRPC client and the cluster only exposes
// the HTTP port). Best-effort: a failed send is logged and dropped, not
// retried/queued — the next scheduler tick sends a fresh full snapshot
// anyway, so queueing would only re-send stale data.
func (tc *TelemetryClient) send(ctx context.Context, req reportPluginStatusRequest) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal telemetry report: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1/telemetry/report", tc.marketplaceURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create telemetry request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := tc.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("send telemetry report: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("telemetry report rejected: status %d", resp.StatusCode)
	}

	logging.L().Infof("[Telemetry] reported status for %d plugin(s)", len(req.Statuses))
	return nil
}
