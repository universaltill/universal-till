package entitlement_test

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/entitlement"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pos"
)

// deadNetwork fails every outbound HTTP request and counts the attempts —
// "no cloud reachable".
type deadNetwork struct{ calls atomic.Int32 }

func (d *deadNetwork) RoundTrip(*http.Request) (*http.Response, error) {
	d.calls.Add(1)
	return nil, errors.New("network unreachable (test)")
}

// ADR-0060 §5/§7, ADR-0027 §1, ADR-0003: a till whose entitlement cache says
// lapsed, last confirmed 30 days ago, with no cloud reachable, still
// completes a sale (basket + tender through pos.CompleteSale) — a regression
// tripwire for the sale step itself. Fiscal signing, receipts and EOD are
// covered structurally by salepath_imports_test.go, not driven here.
// Setup follows internal/pos's voucher_sale_test.go (real migrated schema,
// real pos.CompleteSale) with the four entitlement keys seeded in the same DB.
func TestFullSaleCompletesOfflineWithLapsedStaleEntitlement(t *testing.T) {
	ctx := context.Background()
	net := &deadNetwork{}
	prev := http.DefaultTransport
	http.DefaultTransport = net
	t.Cleanup(func() { http.DefaultTransport = prev })

	d, err := db.Open(filepath.Join(t.TempDir(), "lapsed.db"))
	if err != nil {
		t.Fatalf("open migrated db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	for _, s := range []string{
		`INSERT INTO stock_locations (id, name) VALUES ('loc1', 'Main')`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm1', 'SKU1', 'Coffee Beans', 1000, 1)`,
		`INSERT INTO inventory (id, item_id, variant_id, location_id, quantity, updated_at) VALUES ('inv1', 'itm1', NULL, 'loc1', 50, datetime('now'))`,
		`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('cash', 'Cash', 'cash', 1)`,
	} {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}

	now := time.Now().UTC()
	settings := data.NewSettingsRepo(d.DB)
	if err := settings.SetMany(ctx, map[string]string{
		entitlement.KeyPlan:               "pro",
		entitlement.KeySubscriptionStatus: "lapsed",
		entitlement.KeyExpiresAt:          now.Add(-40 * 24 * time.Hour).Format(time.RFC3339),
		entitlement.KeyLastConfirmedAt:    now.Add(-30 * 24 * time.Hour).Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("seed entitlement cache: %v", err)
	}
	if p := entitlement.EffectivePlan(ctx, settings, now); p != entitlement.PlanLocal {
		t.Fatalf("precondition: EffectivePlan = %q, want local (lapsed + 30 days stale)", p)
	}

	saleID, err := pos.CompleteSale(ctx, d.DB, pos.SaleInput{
		SaleType:     "sale",
		Currency:     "EUR",
		TaxInclusive: true,
		Lines: []pos.SaleLineInput{{
			ItemID:             "itm1",
			Name:               "Coffee Beans",
			Qty:                2,
			UnitPrice:          money.FromMinor(1000),
			TaxRateBasisPoints: 1900,
			LocationID:         "loc1",
		}},
		Payments: []pos.PaymentInput{{MethodID: "cash", Amount: money.FromMinor(2000)}},
	})
	if err != nil {
		t.Fatalf("CompleteSale with a lapsed, stale entitlement and no network: %v", err)
	}

	var status string
	var total int64
	if err := d.DB.QueryRow(`SELECT status, total FROM sales WHERE id = ?`, saleID).Scan(&status, &total); err != nil {
		t.Fatalf("read sale: %v", err)
	}
	if status != "completed" || total != 2000 {
		t.Fatalf("sale = status %q total %d, want completed / 2000", status, total)
	}
	var qty float64
	if err := d.DB.QueryRow(`SELECT quantity FROM inventory WHERE id = 'inv1'`).Scan(&qty); err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	if qty != 48 {
		t.Fatalf("inventory = %v, want 48", qty)
	}
	if n := net.calls.Load(); n != 0 {
		t.Fatalf("sale attempted %d network call(s); checkout must be fully offline", n)
	}
}
