package settingsnav

import (
	"path/filepath"
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
// unchanged, pinned by a golden test", ut-docs#1913): with no amendments,
// Resolve returns exactly uislot.CoreSettings' rows, in its declared order,
// with the SAME label text (emoji included) settings.html's <h2> markup has
// always rendered — the sidebar must look byte-identical to the pre-#1913
// DOM-scanned version on a till with no `layout` plugin installed.
func TestResolve_ZeroAmendmentsMatchesCoreSettingsExactly(t *testing.T) {
	initRealI18n(t)
	got := Resolve("en", nil)
	if len(got) != len(uislot.CoreSettings) {
		t.Fatalf("want %d rows, got %d: %+v", len(uislot.CoreSettings), len(got), got)
	}
	want := []Row{
		{Key: "registration", Label: "Till registration"},
		{Key: "settings-issuereport", Label: "Report an issue"},
		{Key: "settings-menulayout", Label: "Hidden menu tiles"},
		{Key: "settings-update", Label: "Software update"},
		{Key: "settings-theme", Label: "Theme"},
		{Key: "settings-display", Label: "Display"},
		{Key: "settings-payments", Label: "Payments"},
		{Key: "settings-order-no", Label: "Order numbers"},
		{Key: "settings-barcode", Label: "Barcode types"},
		{Key: "settings-catalog-import-barcode-default", Label: "Catalog import defaults"},
		{Key: "settings-stock-tracking", Label: "Stock"},
		{Key: "settings-backup", Label: "Backups"},
		{Key: "settings-data", Label: "🧹 Data management"},
		{Key: "settings-retention", Label: "🗄️ Report retention"},
		{Key: "settings-printer", Label: "Receipt printer"},
		{Key: "settings-tills", Label: "🔗 Tills"},
		{Key: "settings-invoice", Label: "🧾 Invoices"},
		{Key: "settings-idle-lock", Label: "Auto-lock"},
		{Key: "settings-kiosk-idle-reset", Label: "Kiosk idle reset"},
		{Key: "settings-kiosk-payment-mode", Label: "Kiosk payment mode"},
		{Key: "settings-telemetry", Label: "Plugin telemetry"},
		{Key: "settings-currency", Label: "Currency"},
		{Key: "settings-language", Label: "Language"},
		{Key: "settings-shop-type", Label: "Shop type"},
		{Key: "settings-all", Label: "All Settings"},
	}
	if len(want) != len(uislot.CoreSettings) {
		t.Fatalf("test fixture drifted from uislot.CoreSettings — update `want` to match (%d declared entries)", len(uislot.CoreSettings))
	}
	for i, e := range uislot.CoreSettings {
		if got[i] != want[i] {
			t.Errorf("row %d: want %+v, got %+v", i, want[i], got[i])
		}
		if got[i].Key != e.Key {
			t.Fatalf("row %d: want key %q, got %q", i, e.Key, got[i].Key)
		}
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
	if got[0].Key != "settings-theme" {
		t.Fatalf("the reordered row must now be first, got %+v", got[0])
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
