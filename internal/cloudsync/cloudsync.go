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
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
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
	// Overridable in tests. A till that was off picks up queued directives
	// (e.g. a portal-side "install to tills") right after boot, and a running
	// till within a couple of minutes — each tick is one tiny POST, so the
	// short interval is cheap (Farshid, 2026-07-19). A push channel could
	// replace the poll later; the poll stays as the NAT-safe fallback.
	tickInterval = 2 * time.Minute
	firstDelay   = 15 * time.Second
)

// Hooks are the till-local actions a directive may trigger. Each returns a
// short human message for the cloud's result column. A nil hook marks the
// directive type unsupported on this till.
type Hooks struct {
	SetSetting     func(ctx context.Context, key, value string) (string, error)
	InstallPlugin  func(ctx context.Context, listingID string) (string, error)
	RemovePlugin   func(ctx context.Context, pluginID string) (string, error)
	SetPrice       func(ctx context.Context, itemID string, priceMinor int64) (string, error)
	AdjustStock    func(ctx context.Context, itemID string, delta float64, reason string) (string, error)
	RenameItem     func(ctx context.Context, itemID, name string) (string, error)
	DeactivateItem func(ctx context.Context, itemID string) (string, error)
	// DeviceExtra contributes extra fields to the device report (e.g. the
	// current theme + the themes this till can switch to, so the cloud can
	// render a real design picker instead of a raw key/value form). Keys must
	// not collide with the fixed report fields.
	DeviceExtra func(ctx context.Context) map[string]any
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

	dirs, err := pushSync(ctx, cfg, db, hooks)
	if err != nil {
		return err
	}
	// Catalog/inventory up-sync rides the same tick, but only when the shop's
	// data actually changed (hash gate) — most ticks send nothing. Replicas
	// skip it entirely: their catalog mirrors the primary (ADR-0011), so the
	// primary's snapshot is the shop's snapshot.
	if v, _, _ := data.NewSettingsRepo(db).Get(ctx, "sync.primary_url"); strings.TrimSpace(v) == "" {
		if err := pushSnapshotIfChanged(ctx, cfg, db); err != nil {
			logging.L().Warnf("cloudsync: snapshot push failed (will retry): %v", err)
		}
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
	// JSON numbers decode as float64; tolerate string form too.
	num := func(k string) (int64, bool) {
		switch v := d.Payload[k].(type) {
		case float64:
			return int64(v), true
		case string:
			n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
			return n, err == nil
		}
		return 0, false
	}
	fnum := func(k string) (float64, bool) {
		switch v := d.Payload[k].(type) {
		case float64:
			return v, true
		case string:
			f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
			return f, err == nil
		}
		return 0, false
	}
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
	case "set_price":
		if hooks.SetPrice == nil {
			return "failed", "set_price is not supported on this till"
		}
		id := str("item_id")
		if id == "" {
			return "failed", "missing item_id"
		}
		price, ok := num("price_minor")
		if !ok || price < 0 {
			return "failed", "missing or invalid price_minor"
		}
		msg, err = hooks.SetPrice(ctx, id, price)
	case "adjust_stock":
		if hooks.AdjustStock == nil {
			return "failed", "adjust_stock is not supported on this till"
		}
		id := str("item_id")
		if id == "" {
			return "failed", "missing item_id"
		}
		delta, ok := fnum("qty_delta")
		if !ok || delta == 0 {
			return "failed", "missing or zero qty_delta"
		}
		msg, err = hooks.AdjustStock(ctx, id, delta, str("reason"))
	case "rename_item":
		if hooks.RenameItem == nil {
			return "failed", "rename_item is not supported on this till"
		}
		id := str("item_id")
		name := str("name")
		if id == "" || name == "" {
			return "failed", "missing item_id or name"
		}
		msg, err = hooks.RenameItem(ctx, id, name)
	case "deactivate_item":
		if hooks.DeactivateItem == nil {
			return "failed", "deactivate_item is not supported on this till"
		}
		id := str("item_id")
		if id == "" {
			return "failed", "missing item_id"
		}
		msg, err = hooks.DeactivateItem(ctx, id)
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
func pushSync(ctx context.Context, cfg *config.Config, db *sql.DB, hooks Hooks) ([]directive, error) {
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

	device := map[string]any{
		"device_id": enroll.CurrentStatus().DeviceID,
		"name":      name,
		"version":   buildinfo.Version,
		"platform":  runtime.GOOS + "/" + runtime.GOARCH,
		"role":      role,
		"health":    health,
	}
	if hooks.DeviceExtra != nil {
		for k, v := range hooks.DeviceExtra(ctx) {
			if _, taken := device[k]; !taken {
				device[k] = v
			}
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"store_id": m.StoreID,
		"devices":  []map[string]any{device},
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

// pushSnapshotIfChanged uploads the catalog + on-hand stock when it differs
// from what the cloud already has (tracked via a content hash in settings).
func pushSnapshotIfChanged(ctx context.Context, cfg *config.Config, db *sql.DB) error {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace

	items, err := data.NewCatalogRepo(db).ListItems(ctx)
	if err != nil {
		return err
	}
	barcodes, err := data.NewCatalogRepo(db).ItemBarcodes(ctx)
	if err != nil {
		return err
	}
	qty := map[string]float64{}
	if levels, err := data.NewPOSRepo(db).ListStockLevels(ctx); err == nil {
		for _, l := range levels {
			qty[l.ItemID] += l.CurrentQty
		}
	}
	// Variant rows ride along under their parent: own id/price/barcode, name
	// composed for the cloud table. No qty on variants — stock is tracked at
	// item level (ADR-0011), and repeating the parent qty would double-count
	// the shop's stock units.
	variants, _ := data.NewCatalogRepo(db).ItemVariants(ctx)
	rows := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if !it.IsActive {
			continue
		}
		barcode := ""
		if bs := barcodes[it.ID]; len(bs) > 0 {
			barcode = bs[0]
		}
		rows = append(rows, map[string]any{
			"id": it.ID, "name": it.Name, "price_minor": it.BasePrice,
			"barcode": barcode, "qty": qty[it.ID],
		})
		for _, v := range variants[it.ID] {
			rows = append(rows, map[string]any{
				"id": v.ID, "name": it.Name + " — " + v.Name,
				"price_minor": v.PriceMinor, "barcode": v.Barcode,
			})
		}
	}
	payload, _ := json.Marshal(map[string]any{"store_id": m.StoreID, "items": rows})

	sum := sha256.Sum256(payload)
	hash := hex.EncodeToString(sum[:])
	settings := data.NewSettingsRepo(db)
	if prev, _, _ := settings.Get(ctx, "cloudsync.snapshot_hash"); prev == hash {
		return nil // unchanged since the last successful push
	}
	if _, err := post(ctx, cfg, "/v1/stores/catalog-snapshot", payload); err != nil {
		return err
	}
	logging.L().Infof("cloudsync: catalog snapshot pushed (%d items)", len(rows))
	return settings.Set(ctx, "cloudsync.snapshot_hash", hash)
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
