package plugins

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// TelemetryEvent represents a single telemetry event
type TelemetryEvent struct {
	EventType string                 `json:"event_type"` // status_update|install|update|browse
	PluginID  string                 `json:"plugin_id,omitempty"`
	Version   string                 `json:"version,omitempty"`
	Data      map[string]interface{} `json:"data,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
	DeviceID  string                 `json:"device_id"`
}

// TelemetryBatch contains a batch of events to send
type TelemetryBatch struct {
	Events    []TelemetryEvent `json:"events"`
	BatchID   string           `json:"batch_id"`
	Timestamp time.Time        `json:"timestamp"`
}

// TelemetryClient sends plugin telemetry to marketplace
type TelemetryClient struct {
	db              *sql.DB
	marketplaceURL  string
	deviceID        string
	httpClient      *http.Client
	optInEnabled    bool
	batchSize       int
	batchInterval   time.Duration
	mu              sync.Mutex
	pendingEvents   []TelemetryEvent
	ticker          *time.Ticker
	stopChan        chan struct{}
}

// NewTelemetryClient creates a new telemetry client
func NewTelemetryClient(db *sql.DB, marketplaceURL, deviceID string) *TelemetryClient {
	return &TelemetryClient{
		db:             db,
		marketplaceURL: marketplaceURL,
		deviceID:       deviceID,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		batchSize:     50,
		batchInterval: 5 * time.Minute,
		pendingEvents: []TelemetryEvent{},
		stopChan:      make(chan struct{}),
	}
}

// Start begins the telemetry batching and sending loop
func (tc *TelemetryClient) Start(ctx context.Context) {
	// Check opt-in status from settings
	if err := tc.loadOptInStatus(ctx); err != nil {
		logging.L().Warnf("[Telemetry] Failed to load opt-in status: %v", err)
		tc.optInEnabled = false
	}

	if !tc.optInEnabled {
		logging.L().Infof("[Telemetry] Telemetry disabled (opt-in required)")
		return
	}

	tc.ticker = time.NewTicker(tc.batchInterval)
	
	go func() {
		for {
			select {
			case <-tc.ticker.C:
				if err := tc.flush(ctx); err != nil {
					logging.L().Warnf("[Telemetry] Failed to flush events: %v", err)
				}
			case <-tc.stopChan:
				tc.ticker.Stop()
				// Final flush
				tc.flush(ctx)
				return
			}
		}
	}()

	logging.L().Infof("[Telemetry] Client started")
}

// Stop stops the telemetry client
func (tc *TelemetryClient) Stop() {
	close(tc.stopChan)
}

// TrackStatusUpdate records a plugin status change (T024a)
func (tc *TelemetryClient) TrackStatusUpdate(ctx context.Context, pluginID, version string, enabled bool) error {
	if !tc.shouldTrack(ctx) {
		return nil
	}

	event := TelemetryEvent{
		EventType: "status_update",
		PluginID:  pluginID,
		Version:   version,
		Data: map[string]interface{}{
			"enabled": enabled,
		},
		Timestamp: time.Now().UTC(),
		DeviceID:  tc.deviceID,
	}

	return tc.enqueue(event)
}

// TrackInstall records a plugin installation (T024b)
func (tc *TelemetryClient) TrackInstall(ctx context.Context, pluginID, version string) error {
	if !tc.shouldTrack(ctx) {
		return nil
	}

	event := TelemetryEvent{
		EventType: "install",
		PluginID:  pluginID,
		Version:   version,
		Timestamp: time.Now().UTC(),
		DeviceID:  tc.deviceID,
	}

	return tc.enqueue(event)
}

// TrackUpdate records a plugin update (T024b)
func (tc *TelemetryClient) TrackUpdate(ctx context.Context, pluginID, fromVersion, toVersion string) error {
	if !tc.shouldTrack(ctx) {
		return nil
	}

	event := TelemetryEvent{
		EventType: "update",
		PluginID:  pluginID,
		Version:   toVersion,
		Data: map[string]interface{}{
			"from_version": fromVersion,
			"to_version":   toVersion,
		},
		Timestamp: time.Now().UTC(),
		DeviceID:  tc.deviceID,
	}

	return tc.enqueue(event)
}

// TrackBrowse records a catalog browse event (T024b)
func (tc *TelemetryClient) TrackBrowse(ctx context.Context, catalogSize int) error {
	if !tc.shouldTrack(ctx) {
		return nil
	}

	event := TelemetryEvent{
		EventType: "browse",
		Data: map[string]interface{}{
			"catalog_size": catalogSize,
		},
		Timestamp: time.Now().UTC(),
		DeviceID:  tc.deviceID,
	}

	return tc.enqueue(event)
}

// shouldTrack checks if telemetry should be tracked based on opt-in status
func (tc *TelemetryClient) shouldTrack(ctx context.Context) bool {
	// Reload opt-in status periodically
	if err := tc.loadOptInStatus(ctx); err != nil {
		return false
	}
	return tc.optInEnabled
}

// loadOptInStatus reads telemetry opt-in from settings
func (tc *TelemetryClient) loadOptInStatus(ctx context.Context) error {
	var value string
	err := tc.db.QueryRowContext(ctx,
		`SELECT value FROM settings WHERE key = 'marketplace.telemetry_opt_in' LIMIT 1`,
	).Scan(&value)
	
	if err == sql.ErrNoRows {
		// Default to opt-out if not set
		tc.optInEnabled = false
		return nil
	}
	
	if err != nil {
		return err
	}

	tc.optInEnabled = (value == "true" || value == "1")
	return nil
}

// enqueue adds an event to the pending queue
func (tc *TelemetryClient) enqueue(event TelemetryEvent) error {
	tc.mu.Lock()
	defer tc.mu.Unlock()

	tc.pendingEvents = append(tc.pendingEvents, event)

	// Auto-flush if batch size reached
	if len(tc.pendingEvents) >= tc.batchSize {
		// Flush in background to avoid blocking
		go func() {
			ctx := context.Background()
			if err := tc.flush(ctx); err != nil {
				logging.L().Warnf("[Telemetry] Failed to flush full batch: %v", err)
			}
		}()
	}

	return nil
}

// flush sends pending events to marketplace
func (tc *TelemetryClient) flush(ctx context.Context) error {
	tc.mu.Lock()
	if len(tc.pendingEvents) == 0 {
		tc.mu.Unlock()
		return nil
	}

	// Take all pending events
	events := make([]TelemetryEvent, len(tc.pendingEvents))
	copy(events, tc.pendingEvents)
	tc.pendingEvents = []TelemetryEvent{}
	tc.mu.Unlock()

	// Create batch
	batch := TelemetryBatch{
		Events:    events,
		BatchID:   fmt.Sprintf("%s-%d", tc.deviceID, time.Now().Unix()),
		Timestamp: time.Now().UTC(),
	}

	// Send to marketplace with retry
	return tc.sendBatch(ctx, batch)
}

// sendBatch sends a batch with exponential backoff retry
func (tc *TelemetryClient) sendBatch(ctx context.Context, batch TelemetryBatch) error {
	log := logging.L()

	data, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("failed to marshal batch: %w", err)
	}

	endpoint := fmt.Sprintf("%s/v1/telemetry", tc.marketplaceURL)
	
	maxRetries := 3
	backoff := 2 * time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")

		resp, err := tc.httpClient.Do(req)
		if err != nil {
			log.Warnf("[Telemetry] Attempt %d failed: %v", attempt+1, err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			log.Infof("[Telemetry] Sent batch with %d events", len(batch.Events))
			return nil
		}

		log.Warnf("[Telemetry] Server returned %d", resp.StatusCode)
	}

	return fmt.Errorf("failed to send batch after %d retries", maxRetries+1)
}
