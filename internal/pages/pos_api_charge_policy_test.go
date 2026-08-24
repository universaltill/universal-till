package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ADR-0061 Decision 2 end-to-end on the cashier tender path: with NO plugin
// installed the service charge is still taxed at the sale's blended
// per-line rates — the quick-tender's server-filled amount, the persisted
// total and tax_total all carry the charge's tax, proving the fail-closed
// default drives real behavior, not just the pure function.
func TestTenderHandler_ServiceChargeTaxedAtBlendedRates(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) { s.ServiceChargeRateBasisPoints = 1000 })
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// ABC: price 100 @20% -> line tax 20; 10% service charge on the 100
	// subtotal = 10, taxed at the sale's only rate (20%) = 2 -> total 132.
	var total, taxTotal, serviceCharge int64
	if err := dp.Db.QueryRow(`SELECT total, tax_total, service_charge_amount FROM sales`).Scan(&total, &taxTotal, &serviceCharge); err != nil {
		t.Fatalf("query sale: %v", err)
	}
	if serviceCharge != 10 {
		t.Fatalf("expected service_charge_amount 10, got %d", serviceCharge)
	}
	if taxTotal != 22 {
		t.Fatalf("expected tax_total 22 (20 line + 2 on the charge), got %d", taxTotal)
	}
	if total != 132 {
		t.Fatalf("expected total 132 (100 + 10 charge + 22 tax), got %d", total)
	}
	var paymentAmount int64
	if err := dp.Db.QueryRow(`SELECT amount FROM payments`).Scan(&paymentAmount); err != nil {
		t.Fatalf("query payment: %v", err)
	}
	if paymentAmount != 132 {
		t.Fatalf("expected the zero-amount payment filled in as the charge-taxed 132, got %d", paymentAmount)
	}
}

// ADR-0061 Decision 3 on the tender path: a payment's tip recipient
// defaults from charge.policy.ask's tip_default_recipient when a country
// plugin answers, and to "employee" when nothing does — decided in
// completeTender (shared by cashier and kiosk), right where the
// plugin-reported tip already lands.
func TestTenderHandler_TipRecipientDefaultsFromChargePolicy(t *testing.T) {
	tender := func(t *testing.T, mux *http.ServeMux) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
			strings.NewReader(`{"payments":[{"method":"cash","amount":200,"tip":50}],"offline":true}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("tender: expected 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Data struct {
				SaleID string `json:"saleId"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("parse tender response: %v", err)
		}
		return out.Data.SaleID
	}

	t.Run("no plugin answer defaults to employee", func(t *testing.T) {
		mux, dp := newPOSTestDeps(t)
		plugins.SharedBus(dp.Db).ResetSubscribers()
		if _, err := dp.Engine.Scan("ABC"); err != nil {
			t.Fatalf("seed scan: %v", err)
		}
		saleID := tender(t, mux)
		var recipient string
		if err := dp.Db.QueryRow(`SELECT tip_recipient FROM payments WHERE sale_id = ?`, saleID).Scan(&recipient); err != nil {
			t.Fatalf("query payment: %v", err)
		}
		if recipient != "employee" {
			t.Fatalf("want tip_recipient 'employee' with no plugin, got %q", recipient)
		}
	})

	t.Run("plugin's business default is applied", func(t *testing.T) {
		mux, dp := newPOSTestDeps(t)
		seedChargePolicyPlugin(t, dp.Db)
		bus := plugins.SharedBus(dp.Db)
		bus.ResetSubscribers()
		t.Cleanup(bus.ResetSubscribers)
		bus.SetEventMode("charge.policy.ask", plugins.Blocking)
		if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
			[]string{"charge.policy.ask"},
			func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
				return json.RawMessage(`{"service_charge_permitted":true,"tip_default_recipient":"business"}`), nil
			}); err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		if _, err := dp.Engine.Scan("ABC"); err != nil {
			t.Fatalf("seed scan: %v", err)
		}
		saleID := tender(t, mux)
		var recipient string
		if err := dp.Db.QueryRow(`SELECT tip_recipient FROM payments WHERE sale_id = ?`, saleID).Scan(&recipient); err != nil {
			t.Fatalf("query payment: %v", err)
		}
		if recipient != "business" {
			t.Fatalf("want tip_recipient 'business' from the plugin's tip_default_recipient, got %q", recipient)
		}
	})
}

// A plugin answering service_charge_permitted=false suppresses the
// till-configured charge on the tender path — the sale still completes
// (never blocked), just without the charge line.
func TestTenderHandler_ChargePolicyNotPermittedSuppressesCharge(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) { s.ServiceChargeRateBasisPoints = 1000 })
	seedChargePolicyPlugin(t, dp.Db)
	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("charge.policy.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"charge.policy.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"service_charge_permitted":false}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (never blocked), got %d: %s", rec.Code, rec.Body.String())
	}
	var total, serviceCharge int64
	if err := dp.Db.QueryRow(`SELECT total, service_charge_amount FROM sales`).Scan(&total, &serviceCharge); err != nil {
		t.Fatalf("query sale: %v", err)
	}
	if serviceCharge != 0 {
		t.Fatalf("expected service_charge_amount 0 when the plugin forbids it, got %d", serviceCharge)
	}
	if total != 120 {
		t.Fatalf("expected total 120 (100 + 20 tax, no charge), got %d", total)
	}
}

// The ut-docs#962 Turkey backstop and ADR-0061's charge policy are two
// INDEPENDENT mechanisms that meet on the same tender path, and neither
// knows about the other: EffectiveServiceChargeRateBP zeroes the charge for
// a TR shop BEFORE the policy consult, and the policy consult then hands a
// flat tax basis to ServiceChargeTax. This pins that they compose — a plugin
// answering "permitted, taxed flat at 19%" must NOT resurrect a charge on a
// Turkish till, and taxing a zero charge at a flat basis must be a clean
// no-op (no phantom tax band, no inflated total), with the sale still
// completing.
func TestTenderHandler_TurkeyBackstopComposesWithChargePolicy(t *testing.T) {
	mux, dp := newPOSTestDeps(t)
	dp.UpdateState(func(s *common.RuntimeState) {
		s.Country = "TR"
		s.ServiceChargeRateBasisPoints = 1250
	})
	seedChargePolicyPlugin(t, dp.Db)
	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	bus.SetEventMode("charge.policy.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"charge.policy.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"service_charge_permitted":true,"service_charge_default_rate_bp":1250,"service_charge_tax_basis_bp":1900}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}

	rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (a TR sale must still complete, never blocked), got %d: %s", rec.Code, rec.Body.String())
	}
	// ABC: price 100 @20% -> line tax 20. No charge (TR), so no charge tax
	// at ANY basis -> tax_total 20, total 120.
	var total, taxTotal, serviceCharge int64
	if err := dp.Db.QueryRow(`SELECT total, tax_total, service_charge_amount FROM sales`).
		Scan(&total, &taxTotal, &serviceCharge); err != nil {
		t.Fatalf("query sale: %v", err)
	}
	if serviceCharge != 0 {
		t.Fatalf("a permitting plugin must not resurrect a charge the TR backstop zeroed, got service_charge_amount %d", serviceCharge)
	}
	if taxTotal != 20 {
		t.Fatalf("taxing a zero charge at a flat 19%% basis must be a no-op: want tax_total 20 (line tax only), got %d", taxTotal)
	}
	if total != 120 {
		t.Fatalf("expected total 120 (100 + 20 tax, no charge, no charge tax), got %d", total)
	}
}
