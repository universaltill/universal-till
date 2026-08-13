package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#368: a registered tax plugin whose binary is broken cannot
// subscribe to the bus, so "no subscribers" used to be indistinguishable
// from "no tax plugin was ever installed" — and checkout silently fell back
// to the item's base rate. AskTaxRateBP must now signal "blocked" distinctly
// from "no opinion" whenever an ACTIVE plugin registered (in its manifest,
// via plugin_hooks) for tax.rate.ask sits in install_state='broken'.

func markPluginBroken(t *testing.T, db *sql.DB, pluginID string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE plugins SET install_state = 'broken' WHERE id = ?`, pluginID); err != nil {
		t.Fatalf("mark %s broken: %v", pluginID, err)
	}
}

func TestAskTaxRateBP_BrokenRegisteredPluginBlocks(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedTaxPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers() // broken plugin: never got to subscribe

	markPluginBroken(t, db, "com.universaltill.tax-uk")

	asker := &pluginTaxRateAsker{db: db}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}

	rate, ok, blocked := asker.AskTaxRateBP(line, "eat_in")
	if ok || rate != 0 {
		t.Fatalf("broken plugin cannot have answered: got (%d,%v)", rate, ok)
	}
	if !blocked {
		t.Fatal("a broken, registered tax plugin must block (fail closed), not read as 'no tax plugin installed'")
	}

	// Self-heal: the plugin recovers (state flips back, bus generation
	// moves — Manager.Reload/WasmRuntime.Sync do both) and re-subscribes.
	if _, err := db.Exec(`UPDATE plugins SET install_state = 'installed' WHERE id = ?`, "com.universaltill.tax-uk"); err != nil {
		t.Fatal(err)
	}
	bus.SetEventMode("tax.rate.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"tax.rate.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			return json.RawMessage(`{"rate_bp":500}`), nil
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	rate, ok, blocked = asker.AskTaxRateBP(line, "eat_in")
	if !ok || rate != 500 || blocked {
		t.Fatalf("recovered plugin: got (%d,%v,blocked=%v), want (500,true,false)", rate, ok, blocked)
	}
}

// With no tax plugin registered at all, the decline must stay a plain "no
// opinion" — never blocked (a shop with no tax plugin sells normally).
func TestAskTaxRateBP_NoPluginNeverBlocks(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)

	plugins.SharedBus(db).ResetSubscribers()

	asker := &pluginTaxRateAsker{db: db}
	_, ok, blocked := asker.AskTaxRateBP(pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}, "eat_in")
	if ok {
		t.Fatal("expected no override")
	}
	if blocked {
		t.Fatal("no registered tax plugin: must never block")
	}
}

// A DISABLED broken plugin was turned off on purpose — it must not block.
func TestAskTaxRateBP_DisabledBrokenPluginDoesNotBlock(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedTaxPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	markPluginBroken(t, db, "com.universaltill.tax-uk")
	if _, err := db.Exec(`UPDATE plugins SET is_active = 0 WHERE id = ?`, "com.universaltill.tax-uk"); err != nil {
		t.Fatal(err)
	}
	// State changed outside a reload — move the generation the way the
	// disable handler (Manager.Reload → ResetSubscribers) does.
	bus.BumpGeneration()

	asker := &pluginTaxRateAsker{db: db}
	_, _, blocked := asker.AskTaxRateBP(pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}, "eat_in")
	if blocked {
		t.Fatal("a deliberately disabled plugin must not block checkout")
	}
}

// MINOR (ut-docs#368 round-2 review): a DB error in taxAuthorityBroken fails
// OPEN (a DB that can't answer a COUNT can't record a sale either, so this
// doesn't reopen the silent-wrong-tax hole) — but never SILENTLY: a
// persistent read failure disables the whole protection, so it must leave an
// error-level signal in the log.
func TestTaxAuthorityBroken_DBErrorFailsOpenButLogs(t *testing.T) {
	db := openPagesTestDB(t)
	seedForPages(t, db)
	seedTaxPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()
	gen := bus.Generation()
	db.Close() // every read from here on fails

	asker := &pluginTaxRateAsker{db: db}
	start := time.Now()
	if asker.taxAuthorityBroken(gen) {
		t.Fatal("a DB error must fail open (not blocked)")
	}
	found := false
	for _, p := range logging.Recent() {
		if p.At.After(start.Add(-time.Second)) && strings.Contains(p.Msg, "tax fail-closed") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("failing open silently disables the protection with zero signal — expected an error log; recent: %+v", logging.Recent())
	}
}

// The broken check is cached per bus generation (it runs per line per
// recompute on the checkout path) and must re-evaluate when the generation
// moves — the same invalidation contract the answer cache already follows.
func TestAskTaxRateBP_BrokenCheckFollowsGeneration(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedTaxPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	asker := &pluginTaxRateAsker{db: db}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}

	// Healthy: not blocked (and that answer is now cached for this gen).
	if _, _, blocked := asker.AskTaxRateBP(line, "eat_in"); blocked {
		t.Fatal("healthy plugin must not block")
	}

	// The plugin breaks; Sync flips the row AND bumps the generation.
	markPluginBroken(t, db, "com.universaltill.tax-uk")
	bus.BumpGeneration()

	if _, _, blocked := asker.AskTaxRateBP(line, "eat_in"); !blocked {
		t.Fatal("after the generation moved, the broken state must be re-read (stale not-broken cache?)")
	}
}
