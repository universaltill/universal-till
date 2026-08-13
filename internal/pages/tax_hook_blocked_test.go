package pages

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#368: a replica inheriting a plugin's DB rows without its binary
// boots believing the plugin is installed, but the plugin never subscribes —
// and pre-#368 that was indistinguishable from "no tax plugin was ever
// installed", silently falling back to the line's base rate (two tills in
// one shop printing different tax for the same item). These tests pin the
// fail-closed verdict: a REGISTERED tax plugin whose install_state is
// 'broken' blocks the lines it may own, scoped per payload, and heals
// visibly once restored.

// markPluginBroken flips the seeded tax plugin's install_state the same way
// WasmRuntime.Sync does on a load failure, and bumps the bus generation the
// same way Sync's ResetSubscribers does — a state transition always arrives
// with a generation bump in production.
func markPluginBroken(t *testing.T, db *sql.DB, pluginID, state string) {
	t.Helper()
	if _, err := db.Exec(`UPDATE plugins SET install_state = ? WHERE id = ?`, state, pluginID); err != nil {
		t.Fatalf("set install_state=%s: %v", state, err)
	}
}

func TestAskTaxRateBP_BrokenRegisteredPluginBlocks(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedTaxPlugin(t, db)

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers() // drop any subscriber leaked by another test

	// The plugin's binary failed to load: state broken, never subscribed.
	markPluginBroken(t, db, "com.universaltill.tax-uk", "broken")
	bus.ResetSubscribers() // the generation bump Sync's flip always brings

	asker := &pluginTaxRateAsker{db: db}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}

	for i := 0; i < 3; i++ {
		rate, ok, blocked := asker.AskTaxRateBP(line, "eat_in")
		if ok || rate != 0 {
			t.Fatalf("ask %d: broken plugin must never yield an override, got (%d,%v)", i, rate, ok)
		}
		if !blocked {
			t.Fatalf("ask %d: a registered-but-broken tax plugin must block, not silently fall back", i)
		}
	}

	// The verdict is memoized per bus generation: a DB flip alone (no
	// generation bump) keeps the cached verdict — in production the flip and
	// a FOLLOWING generation bump always travel together for a broken plugin
	// (WasmRuntime.Sync's own bus.BumpGeneration() call after the flip loop,
	// review finding 2026-08-13 — see
	// TestAskTaxRateBP_RealSyncNeverLeavesAStaleUnblockedGeneration below for
	// the real-Sync regression pin of that ordering), pinned next.
	markPluginBroken(t, db, "com.universaltill.tax-uk", "installed")
	if _, _, blocked := asker.AskTaxRateBP(line, "eat_in"); !blocked {
		t.Fatalf("verdict must be generation-cached (state flip without a bump keeps the old verdict)")
	}

	// Self-healing must be visible: state restored + generation bumped (what
	// a successful Sync after reinstall does) clears the block.
	bus.ResetSubscribers()
	if _, _, blocked := asker.AskTaxRateBP(line, "eat_in"); blocked {
		t.Fatalf("restored plugin must unblock on the next generation")
	}
}

// A broken tax plugin must not block a line a HEALTHY plugin answers for:
// the block is scoped to lines whose answer would have to come from the
// broken plugin (no healthy override), never the whole till.
func TestAskTaxRateBP_HealthyAnswerWinsOverBrokenSibling(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedTaxPlugin(t, db) // com.universaltill.tax-uk — will subscribe, healthy

	// A second registered tax plugin, broken (its binary is gone).
	for _, q := range []string{
		`INSERT INTO plugins (id, name, version, is_active, install_state) VALUES ('com.test.tax-broken', 'Broken Tax', '1.0.0', 1, 'broken')`,
		`INSERT INTO plugin_hooks (id, plugin_id, event, action, is_active)
		   VALUES ('hook-tax-broken', 'com.test.tax-broken', 'tax.rate.ask', 'tax.rate', 1)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("seed broken tax plugin: %v", err)
		}
	}

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers()

	bus.SetEventMode("tax.rate.ask", plugins.Blocking)
	if _, err := bus.SubscribeWithHandler(context.Background(), "com.universaltill.tax-uk",
		[]string{"tax.rate.ask"},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			var p taxRateAskPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				return nil, err
			}
			if p.TaxCodeID == "tax_answered" {
				return json.RawMessage(`{"rate_bp":500}`), nil
			}
			return nil, nil // no opinion on anything else
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	asker := &pluginTaxRateAsker{db: db}

	// The healthy plugin's real answer stands: not blocked.
	answered := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_answered", TaxRateBP: 2000}
	if rate, ok, blocked := asker.AskTaxRateBP(answered, "eat_in"); !ok || rate != 500 || blocked {
		t.Fatalf("healthy override must stand un-blocked, got (%d,%v,blocked=%v)", rate, ok, blocked)
	}

	// A line no healthy plugin answers for might be the broken plugin's:
	// fail closed on exactly these.
	unanswered := pos.BasketLine{ItemID: "itm2", TaxCodeID: "tax_other", TaxRateBP: 2000}
	if _, ok, blocked := asker.AskTaxRateBP(unanswered, "eat_in"); ok || !blocked {
		t.Fatalf("unanswered line with a broken registered tax plugin must be blocked, got (ok=%v, blocked=%v)", ok, blocked)
	}
}

// TestAskTaxRateBP_ConcurrentAskDuringSyncNeverLeavesAStaleUnblockedCache is
// the review-finding regression pin (2026-08-13). WasmRuntime.Sync's real
// order is: bus.ResetSubscribers() (bumps generation to N) → attempt each
// plugin's load → on failure, flip install_state='broken' via
// PluginRepo.SetPluginState (no bump of its own) → a successful load's
// Subscribe bumps further. When the broken plugin is the LAST one Sync
// processes — guaranteed whenever it's the only wasm plugin, since a broken
// plugin never reaches Subscribe — nothing bumps generation again after its
// flip lands, UNLESS Sync's own post-loop bump (added as this finding's fix)
// runs.
//
// The exposure: a concurrent asker.AskTaxRateBP call that lands in the
// window between the generation-N bump and the flip landing caches a
// deterministic "not broken" verdict AT generation N (bus.HasSubscribers is
// already false — the plugin never got to Subscribe — so the asker's
// no-subscriber branch runs and finds nothing broken yet). Without a bump
// strictly after the flip, generation N never invalidates, so every LATER
// ask — including ones long after Sync returns — keeps reading that stale
// cache entry: a registered, now-broken tax plugin's lines silently fall
// back to their base rate, exactly the original ut-docs#368 bug.
//
// This test drives the asker's real cache through that exact interleaving
// (ask at generation N while still healthy, THEN the DB flip, mirroring
// Sync's true order) rather than a full Sync() call, because Sync is
// single-threaded and synchronous — a black-box call can't itself inject a
// concurrent ask mid-way. What's under test is the INVARIANT Sync's fix
// establishes (a bump always follows every flip a sync pass makes, so no
// generation an asker can observe ever predates that pass's flips) — pinned
// here by calling the same bus.BumpGeneration() Sync's fix calls.
func TestAskTaxRateBP_ConcurrentAskDuringSyncNeverLeavesAStaleUnblockedCache(t *testing.T) {
	db := openPagesTestDB(t)
	defer db.Close()
	seedForPages(t, db)
	seedTaxPlugin(t, db) // com.universaltill.tax-uk, registered for tax.rate.ask, starts 'installed'

	bus := plugins.SharedBus(db)
	t.Cleanup(bus.ResetSubscribers)
	bus.ResetSubscribers() // simulates Sync's initial ResetSubscribers() bump, generation N

	asker := &pluginTaxRateAsker{db: db}
	line := pos.BasketLine{ItemID: "itm1", TaxCodeID: "tax_std", TaxRateBP: 2000}

	// The race window: a concurrent recompute asks HERE — after the bump,
	// before the flip — while the plugin still shows healthy (no subscriber
	// yet, but not broken either). Caches ok=false, blocked=false at gen N.
	if rate, ok, blocked := asker.AskTaxRateBP(line, "eat_in"); ok || blocked {
		t.Fatalf("precondition: mid-window ask should see no opinion, not yet blocked, got (%d,%v,blocked=%v)", rate, ok, blocked)
	}

	// The flip: Sync's load fails and marks the plugin broken — same
	// generation N, no bump (this is Sync's real order pre-fix).
	markPluginBroken(t, db, "com.universaltill.tax-uk", "broken")

	// Still generation N: the poisoned cache entry, if nothing invalidates
	// it, answers every further ask with the stale "not blocked" verdict —
	// this IS the bug, reproduced without needing real goroutines.
	if _, ok, blocked := asker.AskTaxRateBP(line, "eat_in"); ok || blocked {
		t.Fatalf("precondition failed: expected the cache to still be poisoned pre-fix (ok=%v, blocked=%v) — "+
			"if this fails, the cache invalidated on its own and the regression below proves nothing", ok, blocked)
	}

	// Sync's fix: bump once more, strictly after every flip this pass made
	// (internal/plugins/wasm_runtime.go's post-loop bus.BumpGeneration()
	// call, inside `if failedCount > 0`). This is the exact statement that
	// closes the window; the assertion below is what regresses without it.
	bus.BumpGeneration()

	if rate, ok, blocked := asker.AskTaxRateBP(line, "eat_in"); ok || !blocked {
		t.Fatalf("FAIL-OPEN: a registered tax plugin flipped broken mid-generation, but the asker still returns "+
			"(ok=%v, blocked=%v, rate=%d) after the fix's bump — the line would silently use its base rate", ok, blocked, rate)
	}
}
