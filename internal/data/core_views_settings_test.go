package data

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestSettingsDescribeView_4129(t *testing.T) {
	v, ok := LookupCoreView("settings.describe.v1")
	if !ok || v.Permission != "view:shop" || v.DataClass != DataClassShop {
		t.Fatalf("view = %+v, %v", v, ok)
	}
	prev := SetAgentSettingsText(func(key string) string { return "T:" + key })
	t.Cleanup(func() { SetAgentSettingsText(prev) })

	db := newAuditTestDB(t)
	if _, err := db.Exec(`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		t.Fatal(err)
	}
	repo := NewSettingsRepo(db)
	ctx := context.Background()
	if err := repo.Set(ctx, KeyAllowNegativeInventory, "true"); err != nil {
		t.Fatal(err)
	}
	// A secret-looking value stored under an unlisted key must never surface.
	if err := repo.Set(ctx, "marketplace.api_key", "sk-live-SECRET"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Set(ctx, "invoice.seller_name", "Hidden Ltd"); err != nil {
		t.Fatal(err)
	}
	out, err := RunCoreView(ctx, db, v, map[string]int{}, 256<<10)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "SECRET") || strings.Contains(string(out), "Hidden Ltd") {
		t.Fatalf("a secret or non-readable value leaked: %s", out)
	}
	var rows []AgentSettingViewRow
	if err := json.Unmarshal(out, &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != len(AgentSettings) {
		t.Fatalf("%d rows, want one per catalogue entry (%d)", len(rows), len(AgentSettings))
	}
	byKey := map[string]AgentSettingViewRow{}
	for _, r := range rows {
		byKey[r.Key] = r
	}
	neg := byKey[KeyAllowNegativeInventory]
	if neg.Value != "on" || !neg.Writable || neg.Link != "/settings#settings-stock-tracking" ||
		neg.Label != "T:settings.stock_tracking.label" || neg.Help != "T:settings.stock_tracking.hint" {
		t.Fatalf("stock row = %+v", neg)
	}
	inv := byKey["invoice.seller_name"]
	if inv.Value != "" || inv.Writable {
		t.Fatalf("invoice row = %+v: not readable, not writable", inv)
	}
	if byKey["store.currency"].Writable {
		t.Fatal("currency is explain-only")
	}
}

// The writable set is exactly the reviewed allow-list, and each entry is
// complete; adding a writable key is a deliberate edit of this list.
func TestAgentSettings_WritableAllowList_4129(t *testing.T) {
	want := map[string]bool{
		KeyAllowNegativeInventory:             true,
		CatalogImportBarcodeFromSKUDefaultKey: true,
		CatalogPrePackUnitPriceEnabledKey:     true,
	}
	for _, s := range AgentSettings {
		if s.Writable != want[s.Key] {
			t.Errorf("%s writable = %v, want %v", s.Key, s.Writable, want[s.Key])
		}
		if s.Writable && (s.Kind != AgentSettingBool || s.Audit == "" || s.On == "" || s.Off == "" || !s.ReadValue) {
			t.Errorf("%s: incomplete writable entry %+v", s.Key, s)
		}
	}
}

// Never writable or readable, whatever an entry says: ADR-0153 Decision 3.
func TestAgentSettings_NeverTouchesForbiddenKeys_4129(t *testing.T) {
	forbidden := []string{"fiscal.", "tax", "payments.fee", "payments.provider", "secret", "api_key", "token", "bearer",
		"pin", "user", "role", "plugin", "update.channel", "cloud", "pair", "marketplace.device", "sync.", "ai.", "password"}
	for _, s := range AgentSettings {
		for _, f := range forbidden {
			if strings.Contains(s.Key, f) && (s.Writable || s.ReadValue) {
				t.Errorf("%s matches forbidden %q but is writable/readable", s.Key, f)
			}
		}
	}
	seen := map[string]bool{}
	for _, s := range AgentSettings {
		if seen[s.Key] || seen[s.Region+"|"] {
			t.Errorf("duplicate catalogue entry %s / %s", s.Key, s.Region)
		}
		seen[s.Key] = true
	}
}
