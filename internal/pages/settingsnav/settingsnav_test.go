package settingsnav

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/uislot"
)

func initRealI18n(t *testing.T) {
	t.Helper()
	i18n, err := config.NewI18n(filepath.Join("..", "..", "..", "web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
}

// The zero-plugin path (ADR-0088's AC "Zero-plugin Settings page is
// unchanged, pinned by a golden test", ut-docs#1913; categories added by
// ut-docs#3090): with no amendments, Resolve returns every
// uislot.CoreSettings row once, with the SAME label text (emoji included)
// settings.html's <h2> markup renders, gathered under its category heading
// in uislot.SettingsCategories order (declared order kept within a
// category).
func TestResolve_ZeroAmendmentsGroupsCoreSettingsByCategory(t *testing.T) {
	initRealI18n(t)
	got := Resolve("en", nil)
	if len(got) != len(uislot.CoreSettings) {
		t.Fatalf("want %d rows, got %d: %+v", len(uislot.CoreSettings), len(got), got)
	}
	const (
		shop     = "My shop"
		selling  = "Selling"
		payments = "Payments"
		receipts = "Receipts & printers"
		staff    = "Staff & security"
		devices  = "Tills & devices"
		look     = "Look & feel"
		backup   = "Backup & updates"
		advanced = "Advanced"
	)
	want := []Row{
		{Key: "settings-store-name", Label: "Shop name", Group: shop, Cat: "shop"},
		{Key: "settings-currency", Label: "Currency", Group: shop, Cat: "shop"},
		{Key: "settings-language", Label: "Language", Group: shop, Cat: "shop"},
		{Key: "settings-staff-languages", Label: "Languages shown to staff", Group: shop, Cat: "shop"},
		{Key: "settings-shop-type", Label: "Shop type", Group: shop, Cat: "shop"},
		{Key: "settings-order-no", Label: "Order numbers", Group: selling, Cat: "selling"},
		{Key: "settings-order-type-prompt", Label: "Dine-in/takeaway prompt", Group: selling, Cat: "selling"},
		{Key: "settings-sell-screen", Label: "Sell screen", Group: selling, Cat: "selling"},
		{Key: "settings-stock-tracking", Label: "Stock", Group: selling, Cat: "selling"},
		{Key: "settings-payments", Label: "Payments", Group: payments, Cat: "payments"},
		{Key: "settings-printer", Label: "Receipt printer", Group: receipts, Cat: "receipts"},
		{Key: "settings-invoice", Label: "🧾 Invoices", Group: receipts, Cat: "receipts"},
		{Key: "settings-idle-lock", Label: "Auto-lock", Group: staff, Cat: "staff"},
		{Key: "registration", Label: "Till registration", Group: devices, Cat: "devices"},
		{Key: "settings-tills", Label: "🔗 Tills", Group: devices, Cat: "devices"},
		{Key: "settings-kiosk-idle-reset", Label: "Kiosk idle reset", Group: devices, Cat: "devices"},
		{Key: "settings-kiosk-payment-mode", Label: "Kiosk payment mode", Group: devices, Cat: "devices"},
		{Key: "settings-menulayout", Label: "Hidden menu tiles", Group: look, Cat: "look"},
		{Key: "settings-theme", Label: "Theme", Group: look, Cat: "look"},
		{Key: "settings-display", Label: "Display", Group: look, Cat: "look"},
		{Key: "settings-update", Label: "Software update", Group: backup, Cat: "backup"},
		{Key: "settings-about", Label: "About", Group: backup, Cat: "backup"},
		{Key: "settings-backup", Label: "Backups", Group: backup, Cat: "backup"},
		{Key: "settings-issuereport", Label: "Report an issue", Group: advanced, Cat: "advanced"},
		{Key: "settings-diagnostics", Label: "Diagnostic mode", Group: advanced, Cat: "advanced"},
		{Key: "settings-barcode", Label: "Barcode types", Group: advanced, Cat: "advanced"},
		{Key: "settings-catalog-import-barcode-default", Label: "Catalog import defaults", Group: advanced, Cat: "advanced"},
		{Key: "settings-data", Label: "🧹 Data management", Group: advanced, Cat: "advanced"},
		{Key: "settings-retention", Label: "🗄️ Report retention", Group: advanced, Cat: "advanced"},
		{Key: "settings-telemetry", Label: "Plugin telemetry", Group: advanced, Cat: "advanced"},
		{Key: "settings-all", Label: "All Settings", Group: advanced, Cat: "advanced"},
	}
	if len(want) != len(uislot.CoreSettings) {
		t.Fatalf("test fixture drifted from uislot.CoreSettings — update `want` to match (%d declared entries)", len(uislot.CoreSettings))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: want %+v, got %+v", i, want[i], got[i])
		}
	}
}

// Every core section sits in a declared category, and the three
// protected sections (ADR-0088 Decision E) stay in Advanced — the one
// view that keeps every setting (ut-docs#3090 AC 3/4).
func TestCoreSettings_EveryEntryHasADeclaredCategory(t *testing.T) {
	byLabel := map[string]string{}
	for _, c := range uislot.SettingsCategories {
		byLabel[c.LabelKey] = c.Key
	}
	for _, e := range uislot.CoreSettings {
		cat, ok := byLabel[e.Group]
		if !ok {
			t.Errorf("%s: Group %q is not a uislot.SettingsCategories label key", e.Key, e.Group)
			continue
		}
		if uislot.IsProtectedSettingsKey(e.Key) && cat != uislot.SettingsAdvancedCategory {
			t.Errorf("protected section %s must stay in Advanced, got %q", e.Key, cat)
		}
	}
}

// Each category has a translated label and description, and an icon the
// built-in set really draws (an unknown icon name renders nothing).
func TestSettingsCategories_LabelsDescriptionsAndIconsResolve(t *testing.T) {
	initRealI18n(t)
	seen := map[string]bool{}
	for _, c := range uislot.SettingsCategories {
		if seen[c.Key] {
			t.Errorf("duplicate category key %q", c.Key)
		}
		seen[c.Key] = true
		for _, k := range []string{c.LabelKey, c.DescKey} {
			if httpx.T("en", k) == k {
				t.Errorf("category %s: locale key %q has no English text", c.Key, k)
			}
		}
		if httpx.Icon(c.Icon) == "" {
			t.Errorf("category %s: icon %q is not in the built-in icon set", c.Key, c.Icon)
		}
	}
	if !seen[uislot.SettingsAdvancedCategory] {
		t.Fatalf("the Advanced category must be declared")
	}
	last := uislot.SettingsCategories[len(uislot.SettingsCategories)-1]
	if last.Key != uislot.SettingsAdvancedCategory {
		t.Fatalf("Advanced must be the last category, got %q", last.Key)
	}
}

// Categories lists one tile per category that has a row in THIS render,
// in declared order, with Advanced always last and always present (it is
// the view with every setting, ut-docs#3090 AC 3) — a category whose every
// section was gated out for this session never renders an empty tile.
func TestCategories_OnlyNonEmptyCategoriesPlusAdvanced(t *testing.T) {
	initRealI18n(t)
	rows := []Row{
		{Key: "settings-currency", Cat: "shop", Group: "My shop"},
		{Key: "settings-printer", Cat: "receipts", Group: "Receipts & printers"},
	}
	got := Categories("en", rows)
	var ids []string
	for _, c := range got {
		ids = append(ids, c.ID)
	}
	if want := "shop,receipts,advanced"; strings.Join(ids, ",") != want {
		t.Fatalf("want tiles %s, got %v", want, ids)
	}
	if got[0].Label != "My shop" || got[0].Desc == "" || got[0].Icon == "" {
		t.Fatalf("tile must carry label, description and icon: %+v", got[0])
	}
}

// A `layout` plugin's Settings-slot amendment (the mechanism plugins/
// layout-salon demonstrates) reorders, re-labels and re-groups rows —
// Resolve must apply it exactly as the Menu/Items slots' own paths do.
func TestResolve_ReorderRelabelAndRegroupAmendment(t *testing.T) {
	initRealI18n(t)
	fifty := 50
	got := Resolve("en", []uislot.Amendment{
		{PluginID: "com.example.layout", Slot: uislot.SettingsSlot, Key: "settings-theme", Order: &fifty},
		{PluginID: "com.example.layout", Slot: uislot.SettingsSlot, Key: "settings-printer", Group: "layout.salon.settings_group"},
		{PluginID: "com.example.layout", Slot: uislot.SettingsSlot, Key: "settings-tills", Group: "layout.salon.settings_group"},
	})
	if len(got) != len(uislot.CoreSettings) {
		t.Fatalf("reorder/regroup must not drop or add rows, got %d", len(got))
	}
	// Categories keep their fixed order (ut-docs#3090); an Order amendment
	// reorders a row WITHIN its category.
	sawLook := false
	for _, r := range got {
		if r.Cat == "look" {
			if r.Key != "settings-theme" {
				t.Fatalf("the reordered row must now lead its category, got %+v", r)
			}
			sawLook = true
			break
		}
	}
	if !sawLook {
		t.Fatalf("no Look & feel row at all: %+v", got)
	}
	// Printer and Tills must be gathered adjacent under the same resolved
	// group heading (uislot.Resolve's groupTogether), even though they are
	// declared far apart in uislot.CoreSettings.
	var printerIdx, tillsIdx = -1, -1
	for i, r := range got {
		switch r.Key {
		case "settings-printer":
			printerIdx = i
		case "settings-tills":
			tillsIdx = i
		}
	}
	if printerIdx == -1 || tillsIdx == -1 {
		t.Fatalf("printer/tills rows missing entirely: %+v", got)
	}
	if tillsIdx != printerIdx+1 {
		t.Fatalf("regrouped rows must be adjacent, got printer=%d tills=%d", printerIdx, tillsIdx)
	}
	if got[printerIdx].Group == "" || got[tillsIdx].Group == "" {
		t.Fatalf("both regrouped rows must carry a resolved group heading, got printer=%+v tills=%+v", got[printerIdx], got[tillsIdx])
	}
	// A plugin's own group becomes its own category (ut-docs#3090 AC 8: a
	// category is a Group), placed after the core categories and before
	// Advanced, so both rows still sit outside Advanced-only.
	if got[printerIdx].Cat == "" || got[printerIdx].Cat != got[tillsIdx].Cat || got[printerIdx].Cat == uislot.SettingsAdvancedCategory {
		t.Fatalf("regrouped rows must share one plugin category, got printer=%+v tills=%+v", got[printerIdx], got[tillsIdx])
	}
	if last := got[len(got)-1]; last.Cat != uislot.SettingsAdvancedCategory {
		t.Fatalf("Advanced must stay last, got %+v", last)
	}
	cats := Categories("en", got)
	if len(cats) < 2 || cats[len(cats)-2].ID != got[printerIdx].Cat || cats[len(cats)-2].Icon == "" {
		t.Fatalf("the plugin group must render as the tile just before Advanced, with a fallback icon: %+v", cats)
	}
}

// An amendment naming a key from a different slot must never leak into the
// Settings sidebar — mirrors itemsnav's own TestResolve_UnrelatedAmendmentKeyIsANoOp.
func TestResolve_UnrelatedAmendmentKeyIsANoOp(t *testing.T) {
	initRealI18n(t)
	got := Resolve("en", []uislot.Amendment{
		{PluginID: "p", Slot: uislot.MenuSlot, Key: "/tables", Hide: true},
	})
	if len(got) != len(uislot.CoreSettings) {
		t.Fatalf("an amendment naming no Settings-slot key must change nothing, got %d rows", len(got))
	}
}

// Plugin group keys that differ only in case (or in characters the id
// sanitizer drops) stay two categories, never one merged tile — review of
// ut-docs#3090.
func TestResolve_PluginGroupIDsNeverCollide(t *testing.T) {
	initRealI18n(t)
	got := Resolve("en", []uislot.Amendment{
		{PluginID: "p", Slot: uislot.SettingsSlot, Key: "settings-printer", Group: "layout.x.GroupA"},
		{PluginID: "p", Slot: uislot.SettingsSlot, Key: "settings-tills", Group: "layout.x.groupa"},
	})
	cat := map[string]string{}
	for _, r := range got {
		cat[r.Key] = r.Cat
	}
	if cat["settings-printer"] == cat["settings-tills"] {
		t.Fatalf("distinct plugin groups must get distinct category ids, both got %q", cat["settings-printer"])
	}
	plugin := 0
	for _, c := range Categories("en", got) {
		if strings.HasPrefix(c.ID, "g-") {
			plugin++
		}
	}
	if plugin != 2 {
		t.Fatalf("want two plugin tiles, got %d", plugin)
	}
}
