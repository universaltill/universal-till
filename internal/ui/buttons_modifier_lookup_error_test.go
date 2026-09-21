package ui

// ut-docs#2454: ButtonStore.Load and SearchSellable both discarded
// ItemIDsWithModifiers' error via `_` — the exact silent-failure pattern
// ut-docs#2318 fixed in LoadAllActive and ut-docs#2451 fixed in the kiosk's
// loadShopItems. Neither of these two call sites needs chunking (their id
// sets are already bounded — admin-curated quick buttons, and
// SearchSellable's own limit=30), so the fix here is only the logging half
// of that pattern: don't let a real lookup failure (a transient DB error, a
// locked table) pass in total silence.
//
// Both tests force a REAL query failure by dropping a table the modifiers
// UNION query references unconditionally (ut-docs#2454's migration-031
// note in setupFullTestDB) — not a mock, an actual SQL error from the same
// driver production hits.

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/logging"
)

func TestButtonStoreLoad_ModifierLookupFailureIsLoggedNotSilent(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-mod','S1','Coffee', 300, 1)`)
	mustExec(t, db, `INSERT INTO shortcut_buttons(barcode,label,item_id,sort_order) VALUES('C1','Coffee Tile','itm-mod',0)`)
	// A real, active modifier group linked to the item — if the lookup
	// silently failed, this tile would wrongly resolve HasModifiers=false.
	mustExec(t, db, `INSERT INTO item_modifier_groups(id, name, is_active) VALUES ('grp1', 'Size', 1)`)
	mustExec(t, db, `INSERT INTO item_modifier_group_links(item_id, group_id) VALUES ('itm-mod', 'grp1')`)

	// Force ItemIDsWithModifiers to fail for real: its UNION query
	// references item_modifier_group_opt_outs unconditionally (ADR-0094),
	// so dropping it breaks only this lookup — items/shortcut_buttons and
	// every other repo call stay intact.
	mustExec(t, db, `DROP TABLE item_modifier_group_opt_outs`)

	logging.ResetRecent()
	store := NewButtonStore(db)
	btns, err := store.Load()
	if err != nil {
		t.Fatalf("Load must stay non-fatal on a modifier-lookup failure, got err: %v", err)
	}
	if len(btns) != 1 {
		t.Fatalf("len(btns) = %d, want 1", len(btns))
	}
	if btns[0].HasModifiers {
		t.Fatalf("expected the tile to fall back to HasModifiers=false when the lookup itself failed, got %+v", btns[0])
	}

	if !anyRecentWarnContains("items-with-modifiers") {
		t.Fatalf("expected a WARN in logging.Recent() naming the modifier-lookup failure, got: %+v", logging.Recent())
	}
}

func TestButtonStoreSearchSellable_ModifierLookupFailureIsLoggedNotSilent(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })

	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES('itm-mod','S1','Coffee', 300, 1)`)
	mustExec(t, db, `INSERT INTO item_modifier_groups(id, name, is_active) VALUES ('grp1', 'Size', 1)`)
	mustExec(t, db, `INSERT INTO item_modifier_group_links(item_id, group_id) VALUES ('itm-mod', 'grp1')`)
	mustExec(t, db, `DROP TABLE item_modifier_group_opt_outs`)

	logging.ResetRecent()
	store := NewButtonStore(db)
	btns, err := store.SearchSellable(context.Background(), "Coffee", 10)
	if err != nil {
		t.Fatalf("SearchSellable must stay non-fatal on a modifier-lookup failure, got err: %v", err)
	}
	if len(btns) != 1 {
		t.Fatalf("len(btns) = %d, want 1", len(btns))
	}
	if btns[0].HasModifiers {
		t.Fatalf("expected the result to fall back to HasModifiers=false when the lookup itself failed, got %+v", btns[0])
	}

	if !anyRecentWarnContains("items-with-modifiers") {
		t.Fatalf("expected a WARN in logging.Recent() naming the modifier-lookup failure, got: %+v", logging.Recent())
	}
}

func anyRecentWarnContains(substr string) bool {
	for _, p := range logging.Recent() {
		if p.Level == "WARN" && strings.Contains(p.Msg, substr) {
			return true
		}
	}
	return false
}
