// Package cloudsync is the till side of ADR-0018: the shop's till talks to
// Universal Till Cloud on a periodic loop — pushes fleet state (heartbeat +
// health) up, pulls remote-management directives down, applies them through
// the SAME code paths a local operator action would take, and reports each
// result. Best-effort and entirely off the sale path (ADR-0003): a dead
// network just means the next tick retries; checkout never waits on it.
package cloudsync

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

var (
	httpClient = &http.Client{Timeout: 30 * time.Second}
	started    = time.Now()
	// Overridable in tests.
	tickInterval = 5 * time.Minute
	firstDelay   = 90 * time.Second
)

// Hooks are the till-local actions a directive may trigger. Each returns a
// short human message for the cloud's result column. A nil hook marks the
// directive type unsupported on this till.
type Hooks struct {
	SetSetting    func(ctx context.Context, key, value string) (string, error)
	InstallPlugin func(ctx context.Context, listingID string) (string, error)
	RemovePlugin  func(ctx context.Context, pluginID string) (string, error)
}

type directive struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload"`
}

// Tick runs one full sync round: heartbeat up, directives down, apply,
// report. Exported for tests; Start drives it on the loop.
func Tick(ctx context.Context, cfg *config.Config, db *sql.DB, hooks Hooks) error {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		return nil // not registered — nothing to sync
	}

	dirs, err := pushSync(ctx, cfg, db)
	if err != nil {
		return err
	}
	for _, d := range dirs {
		status, msg := apply(ctx, d, hooks)
		if err := postResult(ctx, cfg, d.ID, status, msg); err != nil {
			// Leave it pending on the cloud; the next tick re-applies (the
			// hooks are idempotent for the supported types) and re-reports.
			logging.L().Warnf("cloudsync: result for %s not delivered: %v", d.ID, err)
		} else {
			logging.L().Infof("cloudsync: directive %s (%s) %s: %s", d.ID, d.Type, status, msg)
		}
	}
	return nil
}

// apply routes one directive to its hook. Unknown types and nil hooks fail
// cleanly so the cloud shows WHY nothing happened.
func apply(ctx context.Context, d directive, hooks Hooks) (status, msg string) {
	str := func(k string) string { v, _ := d.Payload[k].(string); return strings.TrimSpace(v) }
	var err error
	switch d.Type {
	case "set_setting":
		if hooks.SetSetting == nil {
			return "failed", "set_setting is not supported on this till"
		}
		key := str("key")
		if key == "" {
			return "failed", "missing setting key"
		}
		msg, err = hooks.SetSetting(ctx, key, str("value"))
	case "install_plugin":
		if hooks.InstallPlugin == nil {
			return "failed", "install_plugin is not supported on this till"
		}
		id := str("listing_id")
		if id == "" {
			return "failed", "missing listing_id"
		}
		msg, err = hooks.InstallPlugin(ctx, id)
	case "remove_plugin":
		if hooks.RemovePlugin == nil {
			return "failed", "remove_plugin is not supported on this till"
		}
		id := str("plugin_id")
		if id == "" {
			return "failed", "missing plugin_id"
		}
		msg, err = hooks.RemovePlugin(ctx, id)
	default:
		return "failed", "unknown directive type " + d.Type
	}
	if err != nil {
		return "failed", err.Error()
	}
	if msg == "" {
		msg = "done"
	}
	return "applied", msg
}

// pushSync reports this device's state and returns the store's pending
// directives.
func pushSync(ctx context.Context, cfg *config.Config, db *sql.DB) ([]directive, error) {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace

	settings := data.NewSettingsRepo(db)
	get := func(k string) string {
		v, _, _ := settings.Get(ctx, k)
		return v
	}
	name := strings.TrimSpace(get("sync.till_name"))
	if name == "" {
		name = "Till"
	}
	role := "primary"
	if strings.TrimSpace(get("sync.primary_url")) != "" {
		role = "replica"
	}
	if strings.TrimSpace(get("display.mode")) == "backoffice" {
		role = "backoffice"
	}
	health := map[string]any{
		"uptime_min": int(time.Since(started).Minutes()),
	}
	if fi, err := os.Stat(cfg.DBPath); err == nil {
		health["db_mb"] = fi.Size() / (1 << 20)
	}

	payload, _ := json.Marshal(map[string]any{
		"store_id": m.StoreID,
		"devices": []map[string]any{{
			"device_id": enroll.CurrentStatus().DeviceID,
			"name":      name,
			"version":   buildinfo.Version,
			"platform":  runtime.GOOS + "/" + runtime.GOARCH,
			"role":      role,
			"health":    health,
		}},
	})
	body, err := post(ctx, cfg, "/v1/stores/sync", payload)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Data struct {
			Directives []directive `json:"directives"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("cloudsync: decode sync response: %w", err)
	}
	return resp.Data.Directives, nil
}

// postResult reports one directive outcome.
func postResult(ctx context.Context, cfg *config.Config, directiveID, status, msg string) error {
	eff := enroll.Effective(cfg)
	payload, _ := json.Marshal(map[string]string{
		"store_id":     eff.Marketplace.StoreID,
		"directive_id": directiveID,
		"status":       status,
		"message":      msg,
	})
	_, err := post(ctx, cfg, "/v1/stores/directives/result", payload)
	return err
}

func post(ctx context.Context, cfg *config.Config, path string, payload []byte) ([]byte, error) {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace
	url := strings.TrimRight(m.EndpointURL, "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	buf := new(bytes.Buffer)
	_, _ = buf.ReadFrom(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cloudsync: %s returned %d", path, resp.StatusCode)
	}
	return buf.Bytes(), nil
}

// Start runs the sync loop: first tick shortly after boot (give enrolment a
// moment), then every few minutes. Ctx-cancelled with the server.
func Start(ctx context.Context, cfg *config.Config, db *sql.DB, hooks Hooks) {
	go func() {
		first := time.NewTimer(firstDelay)
		defer first.Stop()
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		}
		for {
			if err := Tick(ctx, cfg, db, hooks); err != nil {
				logging.L().Warnf("cloudsync: tick failed (will retry): %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(tickInterval):
			}
		}
	}()
}
