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
	"sync"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

// firstDelayNS/tickIntervalNS back Start's two interval knobs (nanoseconds,
// atomic.Int64 rather than plain vars for the same reason as cloudsync's
// twin: the loop goroutine reads them independently of a test's write).
var (
	firstDelayNS   atomic.Int64
	tickIntervalNS atomic.Int64
)

func init() {
	firstDelayNS.Store(int64(2 * time.Minute))
	tickIntervalNS.Store(int64(24 * time.Hour))
}

func firstDelay() time.Duration   { return time.Duration(firstDelayNS.Load()) }
func tickInterval() time.Duration { return time.Duration(tickIntervalNS.Load()) }

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
	if err := pushNotify(ctx, cfg, "low_stock_digest", map[string]any{"running_out": n}); err != nil {
		return err
	}
	logging.L().Infof("alerts: low-stock digest pushed (%d items)", n)
	return nil
}

// pushNotify posts one owner notification to the marketplace with the store
// token; no-op when the till isn't registered.
func pushNotify(ctx context.Context, cfg *config.Config, typ string, body map[string]any) error {
	eff := enroll.Effective(cfg)
	m := eff.Marketplace
	if m.EndpointURL == "" || m.StoreID == "" || m.MerchantToken == "" {
		return nil
	}
	payload, _ := json.Marshal(map[string]any{
		"store_id": m.StoreID,
		"type":     typ,
		"payload":  body,
	})
	url := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/notify"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("notify returned %d", resp.StatusCode)
	}
	return nil
}

// unusualSales compares YESTERDAY's revenue with the average of the same
// weekday over the previous 4 weeks. Returns (ratio, yesterdayTotal, true)
// only when the baseline is meaningful (≥3 of 4 weeks had sales) and the
// deviation is big (>1.8× or <0.4×).
func unusualSales(ctx context.Context, db *sql.DB) (float64, int64, bool) {
	repo := data.NewPOSRepo(db)
	yTotal, yCount, err := repo.DayTotal(ctx, 1)
	if err != nil || yCount == 0 && yTotal == 0 {
		// A zero day is only unusual if the baseline says the day normally
		// sells — fall through with zero and let the ratio check decide.
		_ = err
	}
	var sum int64
	weeks := 0
	for _, back := range []int{8, 15, 22, 29} { // same weekday, 1-4 weeks earlier
		t, c, err := repo.DayTotal(ctx, back)
		if err == nil && (c > 0 || t > 0) {
			sum += t
			weeks++
		}
	}
	if weeks < 3 || sum == 0 {
		return 0, yTotal, false
	}
	avg := float64(sum) / float64(weeks)
	ratio := float64(yTotal) / avg
	if ratio > 1.8 || ratio < 0.4 {
		return ratio, yTotal, true
	}
	return ratio, yTotal, false
}

// Start runs the daily digest loop: first push shortly after boot (give
// enrolment a moment), then every 24h. Ctx-cancelled with the server. wg is
// marked Done once the loop goroutine has fully exited, so callers can wait
// out shutdown instead of returning while it still runs.
func Start(ctx context.Context, cfg *config.Config, db *sql.DB, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		first := time.NewTimer(firstDelay())
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
			if ratio, total, unusual := unusualSales(ctx, db); unusual {
				if err := pushNotify(ctx, cfg, "unusual_sales", map[string]any{
					"ratio_pct": int(ratio * 100),
					"total":     total,
				}); err != nil {
					logging.L().Warnf("alerts: unusual-sales push failed: %v", err)
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(tickInterval()):
			}
		}
	}()
}
