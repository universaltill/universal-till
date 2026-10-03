package data

import (
	"context"
	"testing"
)

// ut-docs#3211: HasActiveEntryTypeForMarket is the setup wizard's neutral,
// offline "is a tax plugin for this country already here?" check — entry
// type plus ADR-0129 §3 markets, no plugin ids or country list in core.
func TestPluginRepo_HasActiveEntryTypeForMarket(t *testing.T) {
	ctx := context.Background()
	db := newPluginRepoTestDB(t)
	if _, err := db.Exec(`CREATE TABLE plugin_markets (plugin_id TEXT NOT NULL, market TEXT NOT NULL, PRIMARY KEY (plugin_id, market));`); err != nil {
		t.Fatalf("create plugin_markets: %v", err)
	}
	repo := NewPluginRepo(db)
	seed := func(id string, active, entryActive int, entryType string, markets ...string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO plugins(id,name,version,is_active) VALUES(?,?,'1.0',?)`, id, id, active); err != nil {
			t.Fatalf("seed plugin %s: %v", id, err)
		}
		if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,label,is_active) VALUES(?,?,?,'k','L',?)`, id+"-e", id, entryType, entryActive); err != nil {
			t.Fatalf("seed entry %s: %v", id, err)
		}
		for _, m := range markets {
			if _, err := db.Exec(`INSERT INTO plugin_markets(plugin_id,market) VALUES(?,?)`, id, m); err != nil {
				t.Fatalf("seed market %s: %v", id, err)
			}
		}
	}
	check := func(entryType, market string, want bool) {
		t.Helper()
		got, err := repo.HasActiveEntryTypeForMarket(ctx, entryType, market)
		if err != nil {
			t.Fatalf("HasActiveEntryTypeForMarket(%q,%q): %v", entryType, market, err)
		}
		if got != want {
			t.Fatalf("HasActiveEntryTypeForMarket(%q,%q) = %v, want %v", entryType, market, got, want)
		}
	}

	check("tax", "DE", false) // empty DB

	seed("disabled", 0, 1, "tax")
	seed("entry-off", 1, 0, "tax")
	seed("other-type", 1, 1, "export")
	seed("fr-only", 1, 1, "tax", "FR")
	check("tax", "DE", false) // disabled plugin, inactive entry, wrong type, other market

	check("tax", "FR", true) // markets lists the country
	check("tax", "fr", true) // the caller's market is upper-cased before matching

	seed("everywhere", 1, 1, "tax") // no markets rows = every market (ADR-0129 §3)
	check("tax", "DE", true)
}
