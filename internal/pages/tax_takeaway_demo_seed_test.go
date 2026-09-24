package pages

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#167: the demo catalogue's café items (Caffè Latte itm051, SKU-0051)
// sit on a tax code pinning a dine-in/takeaway pair (20% / 5%). The setup
// wizard installs the country's base plugins BEFORE it seeds the demo
// catalogue, so on a till whose German tax plugin activated synchronously
// the activation reconcile (ut-docs#1370) has already run — without the
// café code in it. The post-seed reconcile must add it, and a demo latte
// must then really ring at two different VAT amounts through the whole real
// chain: signed wasm tax plugin, its settings_get read, the plugin-backed
// asker, and the POS handlers — through to the completed sale's lines.
func TestDemoSeedCafeLatte_RealChain_DineInVsTakeawayTax(t *testing.T) {
	fx := newTakeawayRealChainFixture(t, false)
	ctx := t.Context()
	database := fx.dp.dp.Db
	// The fixture's engine prices tax-inclusive (the German norm); the
	// tender handler reads the same choice from the runtime state, which in
	// production comes from the same setting — align it here too.
	fx.dp.dp.UpdateState(func(st *common.RuntimeState) { st.TaxInclusive = true })

	// The wizard's own sample-data step (setup_page.go), not the pieces.
	seedDemoDataForSetup(ctx, database)
	if got := storedTakeawayOverridesDB(t, database)["tax_demo_cafe"]; got != 500 {
		t.Fatalf("takeaway_rate_overrides[tax_demo_cafe] = %d after the wizard's demo seed, want 500", got)
	}

	// Dine-in: 20% inside the €3.20 tax-inclusive gross = €0.53.
	if rec := posPostForm(fx.posMux, "/api/pos/scan", "code=SKU-0051"); rec.Code != http.StatusOK {
		t.Fatalf("scan latte: %d (%s)", rec.Code, rec.Body.String())
	}
	b := fx.engine.Basket()
	if len(b.Lines) != 1 || b.Lines[0].TaxCodeID != "tax_demo_cafe" {
		t.Fatalf("expected one latte line on tax_demo_cafe, got %+v", b.Lines)
	}
	if b.Total.Minor() != 320 || b.Tax.Minor() != 53 {
		t.Fatalf("dine-in latte: total %d tax %d, want 320 / 53 (20%%)", b.Total.Minor(), b.Tax.Minor())
	}

	// Takeaway: 5% inside the same €3.20 = €0.15.
	if rec := posPostForm(fx.posMux, "/api/pos/order-type", "order_type=takeaway"); rec.Code != http.StatusOK {
		t.Fatalf("set order type: %d (%s)", rec.Code, rec.Body.String())
	}
	b = fx.engine.Basket()
	if b.Total.Minor() != 320 || b.Tax.Minor() != 15 {
		t.Fatalf("takeaway latte: total %d tax %d, want 320 / 15 (5%%)", b.Total.Minor(), b.Tax.Minor())
	}

	// The completed sale records the takeaway rate, not the dine-in one.
	req := httptest.NewRequest(http.MethodPost, "/api/pos/tender",
		strings.NewReader(`{"payments":[{"method":"cash","amount":320}],"offline":true}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	fx.posMux.ServeHTTP(rec, req)
	fx.dp.dp.WaitForAsyncWork()
	if rec.Code != http.StatusOK {
		t.Fatalf("tender: %d (%s)", rec.Code, rec.Body.String())
	}
	var rateBP, taxAmount int
	if err := database.QueryRow(`SELECT tax_rate_bp, tax_amount FROM sale_lines WHERE item_id = 'itm051'`).Scan(&rateBP, &taxAmount); err != nil {
		t.Fatalf("read latte sale line: %v", err)
	}
	if rateBP != 500 || taxAmount != 15 {
		t.Fatalf("completed takeaway latte line = %d bp / %d tax, want 500 / 15", rateBP, taxAmount)
	}
}

// No German tax plugin installed: the post-seed reconcile is a silent no-op
// (a later install's activation reconcile picks the café code up), never an
// orphan settings row for a plugin that isn't there.
func TestReconcileTaxDeTakeawayOverridesIfActive_NoPluginIsNoop(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "noplugin.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	ctx := t.Context()
	if err := data.NewDemoSeedRepo(conn.DB).SeedDemoCatalogue(ctx); err != nil {
		t.Fatal(err)
	}
	reconcileTaxDeTakeawayOverridesIfActive(ctx, conn.DB)
	var n int
	if err := conn.DB.QueryRow(`SELECT COUNT(*) FROM plugin_settings WHERE plugin_id = ?`, taxDePluginID).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d plugin_settings rows written for an uninstalled tax plugin, want 0", n)
	}
}
