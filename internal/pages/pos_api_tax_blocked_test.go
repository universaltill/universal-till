package pages

import (
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#368 (fail-closed decision 2026-08-13): a basket line whose tax is
// owned by a registered-but-broken tax plugin must be BLOCKED at checkout
// with a clear operator-facing message — never sold with a silently
// substituted base rate — and must tender normally again once the plugin is
// restored. This drives the real /api/pos/tender HTTP path with the real
// plugin-backed asker (the exact wiring init.go installs in production).
func TestTenderHandler_RejectsTaxBlockedLineFailClosed(t *testing.T) {
	mux, dp := newPOSTestDeps(t)

	// A registered tax plugin whose binary failed to load: install_state
	// 'broken' (what WasmRuntime.Sync writes), hook registered, never
	// subscribed on the bus.
	for _, q := range []string{
		`INSERT INTO plugins (id, name, version, is_active, install_state)
		   VALUES ('com.test.tax-broken', 'Broken Tax Plugin', '1.0.0', 1, 'broken')`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
		   VALUES ('hook-tax-368', 'com.test.tax-broken', 'tax.rate.ask', 'tax.rate', 1)`,
	} {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatalf("seed broken tax plugin: %v", err)
		}
	}
	bus := plugins.SharedBus(dp.Db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	// The production wiring (init.go): both engines share one plugin-backed
	// asker; here the cashier engine is enough.
	dp.Engine.SetTaxRateAsker(&pluginTaxRateAsker{db: dp.Db})

	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatalf("seed scan: %v", err)
	}
	// The live basket already surfaces the block per line.
	if b := dp.Engine.Basket(); len(b.Lines) != 1 || !b.Lines[0].TaxBlocked {
		t.Fatalf("expected the scanned line marked TaxBlocked in the live basket, got %+v", b.Lines)
	}

	rec := posPostForm(mux, "/api/pos/tender", "method=cash&amount=0")
	// Same surface convention as the insufficient-stock rejection: 200 with
	// the basket re-rendered carrying a persistent error toast.
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with an error toast, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// The persistent error toast is rendered (pos.toast.tax_blocked —
	// asserted structurally rather than by key/prose, since these tests run
	// with the real translator wired by other tests in this package)...
	if !strings.Contains(body, `class="pos-notice error"`) || !strings.Contains(body, `id="toast-message"`) {
		t.Fatalf("expected a persistent error toast on the basket, got: %s", body)
	}
	// ...and it NAMES the broken plugin so the operator knows what to fix.
	if !strings.Contains(body, "Broken Tax Plugin") {
		t.Fatalf("expected the message to name the broken plugin, got: %s", body)
	}
	// Fail closed means no sale happened: no sales row, basket intact.
	var sales int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&sales); err != nil {
		t.Fatalf("count sales: %v", err)
	}
	if sales != 0 {
		t.Fatalf("a tax-blocked tender must not record a sale, found %d", sales)
	}
	if len(dp.Engine.Basket().Lines) != 1 {
		t.Fatalf("a rejected tender must leave the basket intact")
	}

	// Recovery: the plugin is restored (reinstalled) — state flips back and
	// the bus generation bumps, exactly what a successful WasmRuntime.Sync
	// does. The very same tender now completes; no leftover block.
	if _, err := dp.Db.Exec(`UPDATE plugins SET install_state = 'installed' WHERE id = 'com.test.tax-broken'`); err != nil {
		t.Fatalf("heal plugin: %v", err)
	}
	bus.ResetSubscribers()

	rec = posPostForm(mux, "/api/pos/tender", "method=cash&amount=0")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected the healed till to tender normally, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "Broken Tax Plugin") {
		t.Fatalf("healed tender must not carry the blocked toast: %s", rec.Body.String())
	}
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&sales); err != nil {
		t.Fatalf("count sales after heal: %v", err)
	}
	if sales != 1 {
		t.Fatalf("expected exactly one sale after the healed tender, got %d", sales)
	}
}
