// Package alerts pushes owner notifications to the marketplace (docs repo:
// architecture/notifications-and-email.md). Best-effort and off the sale
// path: a failed push is logged and retried next tick, never surfaced to
// the kiosk.
package alerts

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

// runningOutCount mirrors the inventory page's model: items whose on-hand
// stock covers ≤7 days at the 28-day selling rate.
func runningOutCount(ctx context.Context, db *sql.DB) (int, error) {
	repo := data.NewPOSRepo(db)
	rates, err := repo.ItemDailySellRates(ctx, 28)
	if err != nil || len(rates) == 0 {
		return 0, err
	}
	levels, err := repo.ListStockLevels(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, l := range levels {
		if rate := rates[l.ItemID]; rate > 0 && l.CurrentQty/rate <= 7 {
			n++
		}
	}
	return n, nil
}

// pushDigest sends today's low-stock digest to the marketplace when the till
// is registered and something is actually running out.
func pushDigest(ctx context.Context, cfg *config.Config, db *sql.DB) error {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace
	storeID, token := m.StoreID, m.MerchantToken
	if m.EndpointURL == "" || storeID == "" || token == "" {
		return nil // not registered — nothing to push
	}
	n, err := runningOutCount(ctx, db)
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{
		"store_id": storeID,
		"type":     "low_stock_digest",
		"payload":  map[string]any{"running_out": n},
	})
	url := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/notify"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("notify returned %d", resp.StatusCode)
	}
	logging.L().Infof("alerts: low-stock digest pushed (%d items)", n)
	return nil
}

// Start runs the daily digest loop: first push shortly after boot (give
// enrolment a moment), then every 24h. Ctx-cancelled with the server.
func Start(ctx context.Context, cfg *config.Config, db *sql.DB) {
	go func() {
		first := time.NewTimer(2 * time.Minute)
		defer first.Stop()
		select {
		case <-ctx.Done():
			return
		case <-first.C:
		}
		for {
			if err := pushDigest(ctx, cfg, db); err != nil {
				logging.L().Warnf("alerts: digest push failed (will retry tomorrow): %v", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(24 * time.Hour):
			}
		}
	}()
}
