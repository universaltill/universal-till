package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/ui"
)

// fakeKioskLevyAsker answers charge.policy.ask with the merchant service
// charge permitted plus one plugin-declared levy apportioned at the sale's
// per-line rates — no plugin needed (same shape as fakeMultiChargeAsker).
type fakeKioskLevyAsker struct{}

func (fakeKioskLevyAsker) AskChargePolicy() (pos.ChargePolicy, bool) {
	return pos.ChargePolicy{
		ServiceChargePermitted: true,
		Charges: []pos.ChargeItem{
			{Key: "municipality_tax", Label: "Municipality tax", DefaultRateBP: 500, Base: pos.ChargeBaseNetLines},
		},
	}, true
}

// ut-docs#2937: the self-order kiosk's cart (KioskEngine.computeTotals via
// pos.BuildCharges) quotes the merchant service charge and every ADR-0062
// levy, so the kiosk checkout must demand — and CompleteSale persist —
// exactly that total, charges included, the same way the cashier tender
// does. Before the fix the checkout built a lines-only total with no
// Charges: the card's own example (10% service charge, one 5% levy, a
// 10.00 @20% exclusive basket) quoted 13.80 and charged 12.00.
func TestSelfOrderCheckout_ChargesMatchCartPreview(t *testing.T) {
	for _, tc := range []struct {
		name      string
		inclusive bool
		// net 1000; service 10% = 100; levy 5% = 50 → charges 150.
		// Exclusive: line tax 200 + charge tax 30 (both @20%) → 1380.
		// Inclusive: tax carried inside → 1150.
		wantTotal, wantCharges int64
	}{
		{name: "exclusive", inclusive: false, wantTotal: 1380, wantCharges: 150},
		{name: "inclusive", inclusive: true, wantTotal: 1150, wantCharges: 150},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dp, d := setupSelfOrderShopDeps(t)
			seedShopItem(t, d, "itm-meal", "MEAL", "5000009", "Set Meal", 1000)
			seedStock(t, d, "itm-meal", 10)
			dp.UpdateState(func(s *common.RuntimeState) {
				s.ServiceChargeRateBasisPoints = 1000
				s.TaxInclusive = tc.inclusive
			})
			resolver := ui.PriceResolverAdapter{Store: ui.NewButtonStore(d.DB)}
			dp.KioskEngine = pos.NewServiceWithResolver(pos.Config{
				TaxRateBasisPoints:           2000,
				TaxInclusive:                 tc.inclusive,
				ServiceChargeRateBasisPoints: 1000,
			}, resolver)
			dp.KioskEngine.SetChargePolicyAsker(fakeKioskLevyAsker{})

			mux := http.NewServeMux()
			registerSelfOrderShop(mux, dp)
			post := func(path, body string) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)
				return rec
			}
			rec := post("/api/self-order/scan", "code=5000009")
			if rec.Code != http.StatusOK {
				t.Fatalf("scan: want 200, got %d: %s", rec.Code, rec.Body.String())
			}
			// An unattended customer must see the charge it is about to pay,
			// not only a bigger total.
			if !strings.Contains(rec.Body.String(), `class="selforder-cart-service-charge">Service Charge: `) {
				t.Fatalf("kiosk cart must list the service charge line: %s", rec.Body.String())
			}
			preview := dp.KioskEngine.Basket()
			if preview.Total.Minor() != tc.wantTotal || preview.ServiceCharge.Minor() != tc.wantCharges {
				t.Fatalf("cart preview total %d charges %d, want %d/%d", preview.Total.Minor(), preview.ServiceCharge.Minor(), tc.wantTotal, tc.wantCharges)
			}

			if rec := post("/api/self-order/checkout", "method=card"); rec.Code != http.StatusOK {
				t.Fatalf("checkout: want 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var saleID string
			var total, taxTotal, serviceCharge int64
			if err := d.DB.QueryRow(`SELECT id, total, tax_total, service_charge_amount FROM sales WHERE status = 'completed'`).
				Scan(&saleID, &total, &taxTotal, &serviceCharge); err != nil {
				t.Fatalf("query sale: %v", err)
			}
			var demanded int64
			if err := d.DB.QueryRow(`SELECT amount FROM payments WHERE sale_id = ?`, saleID).Scan(&demanded); err != nil {
				t.Fatalf("query payment: %v", err)
			}
			if preview.Total.Minor() != demanded || demanded != total {
				t.Fatalf("total drift: kiosk cart %d, checkout demanded %d, persisted %d", preview.Total.Minor(), demanded, total)
			}
			if preview.Tax.Minor() != taxTotal {
				t.Fatalf("tax drift: kiosk cart %d, persisted %d", preview.Tax.Minor(), taxTotal)
			}
			if serviceCharge != tc.wantCharges {
				t.Fatalf("service_charge_amount = %d, want %d", serviceCharge, tc.wantCharges)
			}
			var rows int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM sale_charges WHERE sale_id = ?`, saleID).Scan(&rows); err != nil {
				t.Fatalf("query sale_charges: %v", err)
			}
			if rows != 2 {
				t.Fatalf("sale_charges rows = %d, want 2 (service charge + levy)", rows)
			}
		})
	}
}

// A Turkish kiosk (ut-docs#962 ban) quotes no charge line and demands none,
// plugin levies included (ADR-0062 Decision 3) — the checkout must honour
// the kiosk engine's ChargesForbidden exactly like its cart.
func TestSelfOrderCheckout_ForbiddenChargesStayOff(t *testing.T) {
	dp, d := setupSelfOrderShopDeps(t)
	seedShopItem(t, d, "itm-meal", "MEAL", "5000009", "Set Meal", 1000)
	seedStock(t, d, "itm-meal", 10)
	dp.UpdateState(func(s *common.RuntimeState) {
		s.Country = "TR"
		s.ServiceChargeRateBasisPoints = 1000
	})
	resolver := ui.PriceResolverAdapter{Store: ui.NewButtonStore(d.DB)}
	dp.KioskEngine = pos.NewServiceWithResolver(pos.Config{
		TaxRateBasisPoints:           2000,
		ServiceChargeRateBasisPoints: common.EffectiveServiceChargeRateBP(dp.CurrentState()),
		ChargesForbidden:             common.ServiceChargeForbidden(dp.CurrentState().Country),
	}, resolver)
	dp.KioskEngine.SetChargePolicyAsker(fakeKioskLevyAsker{})

	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)
	for _, step := range []struct{ path, body string }{
		{"/api/self-order/scan", "code=5000009"},
		{"/api/self-order/checkout", "method=card"},
	} {
		req := httptest.NewRequest(http.MethodPost, step.path, strings.NewReader(step.body))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: want 200, got %d: %s", step.path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "selforder-cart-service-charge") {
			t.Fatalf("%s: a TR kiosk cart must show no service charge line", step.path)
		}
	}
	var total, serviceCharge int64
	if err := d.DB.QueryRow(`SELECT total, service_charge_amount FROM sales WHERE status = 'completed'`).Scan(&total, &serviceCharge); err != nil {
		t.Fatalf("query sale: %v", err)
	}
	if total != 1200 || serviceCharge != 0 {
		t.Fatalf("got total %d charges %d, want 1200/0 (no charge line on a TR till)", total, serviceCharge)
	}
}
