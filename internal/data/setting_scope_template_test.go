package data

import "testing"

// ADR-0190 (S6): the theme seed marker is this till's own; the ledger is
// shop-wide so it syncs main -> satellite through the admin bundle.
func TestSettingScope_TemplateKeys(t *testing.T) {
	if got := SettingScope("template.theme_seeded"); got != SettingPerTill {
		t.Fatalf("template.theme_seeded scope = %v, want per-till", got)
	}
	if got := SettingScope("shop.template_applied"); got != SettingShopWide {
		t.Fatalf("shop.template_applied scope = %v, want shop-wide", got)
	}
}
