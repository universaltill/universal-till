package pages

import (
	"net/http"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// fakeMultiChargeAsker answers charge.policy.ask with the merchant service
// charge permitted plus two plugin-declared levies — one apportioned at the
// sale's per-line rates (TaxBasisBP 0), one at a flat basis — without a
// plugin (ADR-0062 Non-goals: test-injected, no GCC plugin exists).
type fakeMultiChargeAsker struct{}

func (fakeMultiChargeAsker) AskChargePolicy() (pos.ChargePolicy, bool) {
	return pos.ChargePolicy{
		ServiceChargePermitted: true,
		Charges: []pos.ChargeItem{
			{Key: "municipality_tax", Label: "Municipality tax", DefaultRateBP: 500, TaxBasisBP: 0, Base: pos.ChargeBaseNetLines},
			{Key: "tourism_tax", Label: "Tourism tax", DefaultRateBP: 400, TaxBasisBP: 700, Base: pos.ChargeBaseNetLines},
		},
	}, true
}

// ADR-0062 step 2 (ut-docs#985) lockstep parity: the basket preview
// (pos.Service.computeTotals), the tender handler's demanded total
// (pos_api.go) and the persisted sale (pos.computeSaleTotals via
// CompleteSale) must all see the SAME charge list — merchant service charge
// plus both plugin levies — so the on-screen total IS the demanded total IS
// the recorded total, in both pricing modes. Mixed-rate lines with odd
// amounts make every rounding path matter. Fails if any one site forgets
// the plugin charges (its total/tax diverges from the other two).
func TestMultiCharge_BasketTenderAndPersistedTotalsAgree(t *testing.T) {
	for _, tc := range []struct {
		name      string
		inclusive bool
		// Hand-computed for the exclusive case (see below); inclusive is
		// pinned by parity alone plus its own identity checks.
		wantTotal, wantTax, wantCharges int64
	}{
		// Lines: 1999 @20% (tax 400) + 333 @7% (tax 23) -> net 2332, tax 423.
		// Charges on 2332: service 12.5% = 292; municipality 5% = 117;
		// tourism 4% = 93 -> 502.
		// Charge tax: service 292 -> 41 @7% (3) + 251 @20% (50) = 53;
		// municipality 117 -> 16 @7% (1) + 101 @20% (20) = 21; tourism 93 @
		// flat 7% = 7 -> 81. Tax 423+81 = 504; total 2332+502+504 = 3338.
		{name: "exclusive", inclusive: false, wantTotal: 3338, wantTax: 504, wantCharges: 502},
		{name: "inclusive", inclusive: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, dp := newPOSTestDeps(t)
			dp.UpdateState(func(s *common.RuntimeState) {
				s.ServiceChargeRateBasisPoints = 1250
				s.TaxInclusive = tc.inclusive
			})
			engine := pos.NewServiceWithResolver(pos.Config{
				TaxInclusive:                 tc.inclusive,
				TaxRateBasisPoints:           2000,
				ServiceChargeRateBasisPoints: 1250,
			}, stubResolver{
				"HI": {SKU: "HI", Name: "Main", Qty: 1, PriceCents: 1999, ItemID: "itm1", TaxRateBP: 2000},
				"LO": {SKU: "LO", Name: "Side", Qty: 1, PriceCents: 333, ItemID: "itm-plain2", TaxRateBP: 700},
			})
			engine.SetChargePolicyAsker(fakeMultiChargeAsker{})
			dp.Engine = engine
			for _, code := range []string{"HI", "LO"} {
				if _, err := dp.Engine.Scan(code); err != nil {
					t.Fatalf("scan %s: %v", code, err)
				}
			}
			preview := dp.Engine.Basket()

			rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=0")
			if rec.Code != http.StatusOK {
				t.Fatalf("tender: expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			var saleID string
			var total, taxTotal, serviceCharge int64
			var basisBP int
			if err := dp.Db.QueryRow(`SELECT id, total, tax_total, service_charge_amount, service_charge_tax_basis_bp FROM sales`).
				Scan(&saleID, &total, &taxTotal, &serviceCharge, &basisBP); err != nil {
				t.Fatalf("query sale: %v", err)
			}
			var demanded int64
			if err := dp.Db.QueryRow(`SELECT amount FROM payments WHERE sale_id = ?`, saleID).Scan(&demanded); err != nil {
				t.Fatalf("query payment: %v", err)
			}

			if preview.Total.Minor() != demanded || demanded != total {
				t.Fatalf("total drift: basket preview %d, tender demanded %d, persisted %d", preview.Total.Minor(), demanded, total)
			}
			if preview.Tax.Minor() != taxTotal {
				t.Fatalf("tax drift: basket preview %d, persisted %d", preview.Tax.Minor(), taxTotal)
			}
			if preview.ServiceCharge.Minor() != serviceCharge {
				t.Fatalf("charge drift: basket preview %d, persisted service_charge_amount %d", preview.ServiceCharge.Minor(), serviceCharge)
			}
			if serviceCharge != 502 {
				t.Fatalf("service_charge_amount = %d, want the sum of all three charges 502", serviceCharge)
			}
			if basisBP != 0 {
				t.Fatalf("service_charge_tax_basis_bp = %d, want 0 for a multi-charge sale (ADR-0062 Decision 2)", basisBP)
			}
			if tc.wantTotal != 0 && (total != tc.wantTotal || taxTotal != tc.wantTax || serviceCharge != tc.wantCharges) {
				t.Fatalf("got total %d tax %d charges %d, want %d/%d/%d", total, taxTotal, serviceCharge, tc.wantTotal, tc.wantTax, tc.wantCharges)
			}
			if tc.inclusive && total != 2332+502 {
				t.Fatalf("inclusive: total = %d, want lines + charges 2834 (tax carried inside)", total)
			}

			type row struct {
				key, label string
				amount     int64
				basis      int
			}
			rows, err := dp.Db.Query(`SELECT key, label, amount_minor, tax_basis_bp FROM sale_charges WHERE sale_id = ? ORDER BY seq`, saleID)
			if err != nil {
				t.Fatalf("query sale_charges: %v", err)
			}
			defer rows.Close()
			var got []row
			for rows.Next() {
				var r row
				if err := rows.Scan(&r.key, &r.label, &r.amount, &r.basis); err != nil {
					t.Fatalf("scan sale_charges: %v", err)
				}
				got = append(got, r)
			}
			want := []row{
				{pos.ServiceChargeKey, "", 292, 0},
				{"municipality_tax", "Municipality tax", 117, 0},
				{"tourism_tax", "Tourism tax", 93, 700},
			}
			if len(got) != len(want) {
				t.Fatalf("sale_charges rows = %+v, want %+v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("sale_charges[%d] = %+v, want %+v", i, got[i], want[i])
				}
			}
		})
	}
}

// ADR-0062 Decision 3 on the tender path: the Turkey ban suppresses the
// WHOLE list — a plugin-declared levy must not reach a Turkish bill even
// when the plugin says permitted. The sale still completes.
func TestMultiCharge_TurkeyBanSuppressesPluginCharges(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) {
		s.Country = "TR"
		s.ServiceChargeRateBasisPoints = 1000
	})
	engine := pos.NewServiceWithResolver(pos.Config{
		TaxRateBasisPoints: 2000,
		ChargesForbidden:   common.ServiceChargeForbidden("TR"),
	}, stubResolver{"PLAIN": {SKU: "PLAIN", Name: "Plain Item", Qty: 1, PriceCents: 200, ItemID: "itm-plain2", TaxRateBP: 2000}})
	engine.SetChargePolicyAsker(fakeMultiChargeAsker{})
	dp.Engine = engine
	if _, err := dp.Engine.Scan("PLAIN"); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if b := dp.Engine.Basket(); b.ServiceCharge != 0 || b.Total != 240 {
		t.Fatalf("basket preview: charge %d total %d, want 0/240 (no levy on a TR bill)", b.ServiceCharge, b.Total)
	}
	rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var total, serviceCharge, rows int64
	if err := dp.Db.QueryRow(`SELECT total, service_charge_amount FROM sales`).Scan(&total, &serviceCharge); err != nil {
		t.Fatalf("query sale: %v", err)
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sale_charges`).Scan(&rows); err != nil {
		t.Fatalf("count sale_charges: %v", err)
	}
	if total != 240 || serviceCharge != 0 || rows != 0 {
		t.Fatalf("got total %d service_charge_amount %d sale_charges rows %d, want 240/0/0", total, serviceCharge, rows)
	}
}
