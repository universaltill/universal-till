package cloudsync

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pos"
)

// Till-side producer for the cloud sales-aggregate endpoint (ut-docs#2535;
// ADR-0111): one structured rollup per (business date, till),
// POSTed to {EndpointURL}/v1/stores/sales-aggregates with the store token.
// Best-effort and off the sale path like the rest of this package
// (ADR-0003): the sales rows are the queue, sales_aggregate_uploads records
// what the cloud has accepted (content hash), and a failed round simply
// leaves the ledger alone for a later tick.

const (
	// salesAggregateLookbackDays covers today and the 13 days before it — a
	// refund or void on an older day re-sends that day, and a till that was
	// offline for up to two weeks backfills on reconnect.
	salesAggregateLookbackDays = 14
	// salesAggregateStaffSetting is the per-shop by_cashier toggle
	// (ADR-0111 §4): only the exact value "true" turns it on; default off.
	salesAggregateStaffSetting = "reports.cloud_staff_breakdown"
	salesAggregatePath         = "/v1/stores/sales-aggregates"
)

var (
	// salesAggregateIntervalNS throttles pushSalesAggregates to at most one
	// round per interval (default 10 minutes) — each round reads up to 14
	// days of rollups, far more than the 2-minute sync tick needs to pay.
	// atomic for the same reason as tickIntervalNS: tests override it while
	// Start's goroutine may read it.
	salesAggregateIntervalNS atomic.Int64
	// salesAggregateLastNS is the UnixNano start of the last round (0 = never).
	salesAggregateLastNS atomic.Int64
	// salesAggregateInactiveLogged keeps a lapsed subscription (402) from
	// logging on every round: logged once, re-armed by the next 200.
	salesAggregateInactiveLogged atomic.Bool
)

func init() {
	salesAggregateIntervalNS.Store(int64(10 * time.Minute))
}

// Wire DTO — field names mirror ut-cloud's salesAggregateUpload and its
// schema bucket types (internal/repositories/ent/schema/salesaggregate.go)
// exactly. Money is int64 minor units at this boundary only (ADR-0004).
type salesAggregateUpload struct {
	StoreID         string                  `json:"store_id"`
	BusinessDate    string                  `json:"business_date"`
	TillID          string                  `json:"till_id"`
	Hourly          []salesAggHourBucket    `json:"hourly"`
	ByCashier       []salesAggCashierBucket `json:"by_cashier"`
	ByItemCategory  []salesAggItemBucket    `json:"by_item_category"`
	ByPaymentMethod []salesAggPaymentBucket `json:"by_payment_method"`
	ByVATRate       []salesAggVATBucket     `json:"by_vat_rate"`
	RefundCount     int                     `json:"refund_count"`
	VoidCount       int                     `json:"void_count"`
	NoSaleCount     int                     `json:"no_sale_count"`
	DiscountCount   int                     `json:"discount_count"`
}

type salesAggHourBucket struct {
	Hour          int   `json:"hour"`
	NetSalesMinor int64 `json:"net_sales_minor"` // Σ sales.total of completed sales (VAT-inclusive, returns NOT netted — they are refund_count), as busyBuckets/EOD Gross
	SalesCount    int   `json:"sales_count"`
}

type salesAggCashierBucket struct {
	StaffID       string  `json:"staff_id"`
	NetSalesMinor int64   `json:"net_sales_minor"`
	SalesCount    int     `json:"sales_count"`
	AvgSaleMinor  int64   `json:"avg_sale_minor"`
	ItemsPerSale  float64 `json:"items_per_sale"`
	Refunds       int     `json:"refunds"`
	Voids         int     `json:"voids"`
	NoSaleOpens   int     `json:"no_sale_opens"`
	Discounts     int     `json:"discounts"`
	HoursOnTill   float64 `json:"hours_on_till"`
}

type salesAggItemBucket struct {
	ItemOrCategoryID string `json:"item_or_category_id"`
	Qty              int    `json:"qty"`
	ShareBasisPoints int    `json:"share_basis_points"`
	TopModifierID    string `json:"top_modifier_id"`
}

type salesAggPaymentBucket struct {
	Method            string `json:"method"`
	AmountMinor       int64  `json:"amount_minor"` // EOD Methods In−Out: tendered − change, returns netted, tips INCLUDED (tips_minor is a subset, not additive)
	TipsMinor         int64  `json:"tips_minor"`
	ExpectedCashMinor int64  `json:"expected_cash_minor"`
}

type salesAggVATBucket struct {
	RateBasisPoints int   `json:"rate_basis_points"`
	NetMinor        int64 `json:"net_minor"`
	VATMinor        int64 `json:"vat_minor"`
}

// salesAggregateSelfTill is the till key for a sale carrying neither
// till_id nor register_id: this till's marketplace device id (the id the
// cloud already knows it by from /v1/stores/sync's device record; explicit
// config wins, else the enrolled one), falling back to the persistent LAN
// discovery id on a till that somehow has no device id yet — never empty,
// never a guess that could change between rounds.
func salesAggregateSelfTill(ctx context.Context, cfg *config.Config, settings *data.SettingsRepo) (string, error) {
	if id := strings.TrimSpace(enroll.Effective(cfg).Marketplace.DeviceID); id != "" {
		return id, nil
	}
	return discovery.TillID(ctx, settings)
}

// pushSalesAggregates uploads every (business date, till) rollup in the
// lookback window whose content changed since the cloud last accepted it.
// Called from Tick's primary-only block; never returns an error — every
// failure is logged and the next eligible round retries.
func pushSalesAggregates(ctx context.Context, cfg *config.Config, db *sql.DB) {
	now := time.Now()
	last := salesAggregateLastNS.Load()
	if last != 0 && now.UnixNano()-last < salesAggregateIntervalNS.Load() {
		return
	}
	// CAS, not Store: a second concurrent caller (a future "sync now") loses
	// the race and skips instead of running a duplicate round.
	if !salesAggregateLastNS.CompareAndSwap(last, now.UnixNano()) {
		return
	}

	storeID := enroll.Effective(cfg).Marketplace.StoreID
	settings := data.NewSettingsRepo(db)
	repo := data.NewPOSRepo(db)
	selfTill, err := salesAggregateSelfTill(ctx, cfg, settings)
	if err != nil {
		logging.L().Warnf("cloudsync: sales aggregates: till identity: %v", err)
		return
	}
	v, _, _ := settings.Get(ctx, salesAggregateStaffSetting)
	withCashiers := strings.TrimSpace(v) == "true"

	to := now.Format("2006-01-02")
	from := now.AddDate(0, 0, -(salesAggregateLookbackDays - 1)).Format("2006-01-02")
	if _, err := repo.PruneSalesAggregateUploads(ctx, from); err != nil {
		logging.L().Warnf("cloudsync: sales aggregates: prune: %v", err)
	}
	keys, err := repo.SalesAggregateKeys(ctx, from, to, selfTill)
	if err != nil {
		logging.L().Warnf("cloudsync: sales aggregates: list days: %v", err)
		return
	}
	sent := 0
	for _, k := range keys {
		payload, err := buildSalesAggregate(ctx, repo, storeID, k, selfTill, withCashiers)
		if err != nil {
			logging.L().Warnf("cloudsync: sales aggregates: build %s/%s: %v", k.BusinessDate, k.TillID, err)
			return
		}
		sum := sha256.Sum256(payload)
		hash := hex.EncodeToString(sum[:])
		if prev, ok, err := repo.SalesAggregateUploadHash(ctx, k.BusinessDate, k.TillID); err != nil {
			logging.L().Warnf("cloudsync: sales aggregates: read ledger: %v", err)
			return
		} else if ok && prev == hash {
			continue // the cloud already holds exactly this rollup
		}
		if _, err := post(ctx, cfg, salesAggregatePath, payload); err != nil {
			var se *statusError
			if errors.As(err, &se) && se.StatusCode == http.StatusPaymentRequired {
				// ADR-0111 §3: an explicit subscription_inactive refusal,
				// not a fault — every other day would get the same answer.
				if salesAggregateInactiveLogged.CompareAndSwap(false, true) {
					logging.L().Infof("cloudsync: sales aggregates not uploaded: cloud subscription inactive (402)")
				}
				return
			}
			if errors.As(err, &se) && rejectedRollup(se.StatusCode) {
				// The cloud refused THIS rollup's content (400/413/422…):
				// re-sending the same bytes gets the same answer, so skip
				// it rather than wedge every newer day behind it. Not
				// recorded — a changed rollup for that day still retries.
				logging.L().Warnf("cloudsync: sales aggregate %s/%s rejected (%d), skipping: %v", k.BusinessDate, k.TillID, se.StatusCode, err)
				continue
			}
			logging.L().Warnf("cloudsync: sales aggregate %s/%s upload failed (will retry): %v", k.BusinessDate, k.TillID, err)
			return
		}
		salesAggregateInactiveLogged.Store(false)
		if err := repo.RecordSalesAggregateUpload(ctx, k.BusinessDate, k.TillID, hash, now); err != nil {
			logging.L().Warnf("cloudsync: sales aggregates: record upload: %v", err)
			return
		}
		sent++
	}
	if sent > 0 {
		logging.L().Infof("cloudsync: sales aggregates pushed (%d day/till rollups)", sent)
	}
}

// buildSalesAggregate reads one (date, till) rollup and marshals the wire
// body. Every breakdown is a non-nil slice so it encodes as [] (never null).
func buildSalesAggregate(ctx context.Context, repo *data.POSRepo, storeID string, k data.SalesAggregateKey, selfTill string, withCashiers bool) ([]byte, error) {
	agg, err := repo.SalesAggregateForTill(ctx, k.BusinessDate, k.TillID, selfTill, withCashiers)
	if err != nil {
		return nil, err
	}
	bandSales, err := repo.SalesForTaxBandsForTill(ctx, k.BusinessDate, k.TillID, selfTill)
	if err != nil {
		return nil, err
	}
	up := salesAggregateUpload{
		StoreID: storeID, BusinessDate: k.BusinessDate, TillID: k.TillID,
		Hourly:          make([]salesAggHourBucket, 0, len(agg.Hourly)),
		ByCashier:       make([]salesAggCashierBucket, 0, len(agg.Cashiers)),
		ByItemCategory:  make([]salesAggItemBucket, 0, len(agg.Items)),
		ByPaymentMethod: make([]salesAggPaymentBucket, 0, len(agg.Payments)),
		ByVATRate:       []salesAggVATBucket{},
		RefundCount:     agg.RefundCount,
		VoidCount:       agg.VoidCount,
		// No-sale drawer opens are not recorded by the till yet (follow-up).
		NoSaleCount:   0,
		DiscountCount: agg.DiscountCount,
	}
	for _, h := range agg.Hourly {
		up.Hourly = append(up.Hourly, salesAggHourBucket{Hour: h.Hour, NetSalesMinor: h.Net.Minor(), SalesCount: h.Count})
	}
	for _, c := range agg.Cashiers {
		b := salesAggCashierBucket{
			StaffID: c.StaffID, NetSalesMinor: c.Net.Minor(), SalesCount: c.Count,
			Refunds: c.Refunds, Voids: c.Voids, Discounts: c.Discounts,
			// no_sale_opens / hours_on_till: not derivable yet (see card).
		}
		if c.Count > 0 {
			b.AvgSaleMinor = c.Net.MulDiv(1, int64(c.Count)).Minor()
			b.ItemsPerSale = math.Round(c.ItemQty/float64(c.Count)*100) / 100
		}
		up.ByCashier = append(up.ByCashier, b)
	}
	var totalQty float64
	for _, it := range agg.Items {
		totalQty += it.Qty
	}
	for _, it := range agg.Items {
		share := 0
		if totalQty > 0 {
			share = int(math.Round(it.Qty / totalQty * 10000))
		}
		up.ByItemCategory = append(up.ByItemCategory, salesAggItemBucket{
			ItemOrCategoryID: it.ItemID, Qty: int(math.Round(it.Qty)),
			ShareBasisPoints: share, TopModifierID: it.TopModifierID,
		})
	}
	for _, p := range agg.Payments {
		up.ByPaymentMethod = append(up.ByPaymentMethod, salesAggPaymentBucket{
			Method: p.Method, AmountMinor: p.Amount.Minor(), TipsMinor: p.Tips.Minor(),
			ExpectedCashMinor: p.ExpectedCash.Minor(),
		})
	}
	// VAT: the Z-report's own per-sale banding over this till's sales.
	for _, b := range pos.EODTaxBandsFromSales(bandSales) {
		up.ByVATRate = append(up.ByVATRate, salesAggVATBucket{
			RateBasisPoints: b.RateBP,
			NetMinor:        b.Net,
			VATMinor:        b.Tax,
		})
	}
	return json.Marshal(up)
}

// rejectedRollup reports whether a status means the cloud refused one
// rollup's content, as opposed to the store, the token or the service being
// unavailable — those (401/403/408/429 and every 5xx) hold for every day, so
// the round stops and the next tick retries.
func rejectedRollup(code int) bool {
	if code < 400 || code >= 500 {
		return false
	}
	switch code {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusForbidden,
		http.StatusNotFound, http.StatusRequestTimeout, http.StatusTooManyRequests:
		return false
	}
	return true
}
