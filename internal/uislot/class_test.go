package uislot

import "testing"

func allRows() map[string][]Entry {
	return map[string][]Entry{
		KindSettings: CoreSettings, KindMenu: CoreMenu, KindItems: CoreItems, KindRail: CoreRail,
	}
}

func TestClassValid(t *testing.T) {
	for _, c := range []Class{ClassEssential, ClassCommon, ClassAdvanced} {
		if !c.Valid() {
			t.Errorf("%q must be valid", c)
		}
	}
	for _, c := range []Class{"", "Essential", "rare", "hidden"} {
		if c.Valid() {
			t.Errorf("%q must be invalid (zero value and unknown names fail)", c)
		}
	}
}

func TestEveryCoreRowDeclaresValidClass(t *testing.T) {
	want := map[string]int{KindSettings: 39, KindMenu: 24, KindItems: 5, KindRail: 4}
	for kind, rows := range allRows() {
		if len(rows) != want[kind] {
			t.Errorf("%s has %d rows, want %d: update the class tables with the new row", kind, len(rows), want[kind])
		}
		for _, e := range rows {
			if !e.Class.Valid() {
				t.Errorf("%s row %q has class %q: every core row declares essential|common|advanced", kind, e.Key, e.Class)
			}
		}
	}
}

func TestRailAndItemsRowsAreCommonAndFixed(t *testing.T) {
	for kind, rows := range map[string][]Entry{KindRail: CoreRail, KindItems: CoreItems} {
		for _, e := range rows {
			if e.Class != ClassCommon {
				t.Errorf("%s %q class = %q, want common", kind, e.Key, e.Class)
			}
			if !FixedClassFor(kind, e.Key) {
				t.Errorf("%s %q must be fixed (not demotable in v1)", kind, e.Key)
			}
		}
	}
}

func TestFixedClassTablesCoverProtectedKeys(t *testing.T) {
	for _, k := range ProtectedMenuKeys {
		if !FixedClassFor(KindMenu, k) {
			t.Errorf("protected menu key %q missing from FixedMenu", k)
		}
	}
	for _, k := range ProtectedRailKeys {
		if !FixedClassFor(KindRail, k) {
			t.Errorf("protected rail key %q missing from FixedRail", k)
		}
	}
	for _, k := range ProtectedItemsKeys {
		if !FixedClassFor(KindItems, k) {
			t.Errorf("protected items key %q missing from FixedItems", k)
		}
	}
	for _, k := range ProtectedSettingsKeys {
		if !FixedClassFor(KindSettings, k) {
			t.Errorf("protected settings key %q missing from FixedSettings", k)
		}
	}
	for _, k := range []string{"settings-issuereport", "settings-menulayout", "settings-payments", "settings-backup",
		"settings-store-name", "settings-currency", "settings-language", "settings-shop-type"} {
		if !FixedClassFor(KindSettings, k) {
			t.Errorf("%q must be a fixed-class settings card", k)
		}
	}
	if FixedClassFor("bogus", "/admin") {
		t.Error("unknown kind is never fixed")
	}
}

// Every fixed key names a real row, so a rename cannot leave a stale entry
// that silently protects nothing.
func TestFixedClassTablesNameRealRows(t *testing.T) {
	tables := map[string][]string{KindSettings: FixedSettings, KindMenu: FixedMenu, KindItems: FixedItems, KindRail: FixedRail}
	for kind, keys := range tables {
		have := map[string]bool{}
		for _, e := range allRows()[kind] {
			have[e.Key] = true
		}
		for _, k := range keys {
			if !have[k] {
				t.Errorf("fixed %s key %q is not a core row", kind, k)
			}
		}
	}
}

// Named expected values: rows already under Advanced stay advanced, the
// universal essentials are essential, and everything else keeps today's
// behaviour (common) until a template says otherwise.
func TestCoreSettingsBaseClasses(t *testing.T) {
	want := map[string]Class{
		"settings-tills-this": ClassCommon, "settings-tills-shop": ClassCommon, "settings-tills-others": ClassCommon,
		"settings-tills-old": ClassCommon, "settings-tills-add": ClassCommon, "settings-tills-readers": ClassCommon,
		"settings-privacy":     ClassCommon,
		"settings-issuereport": ClassAdvanced, "settings-diagnostics": ClassAdvanced,
		"settings-menulayout": ClassCommon, "settings-update": ClassCommon, "settings-about": ClassCommon,
		"settings-theme": ClassEssential, "settings-display": ClassCommon,
		"settings-payments": ClassEssential, "settings-order-no": ClassCommon, "settings-order-type-prompt": ClassCommon,
		"settings-barcode": ClassAdvanced, "settings-catalog-import-barcode-default": ClassAdvanced,
		"settings-catalog-pre-pack-unit-price": ClassAdvanced, "settings-sell-screen": ClassCommon,
		"settings-stock-tracking": ClassCommon, "settings-backup": ClassEssential,
		"settings-data": ClassAdvanced, "settings-retention": ClassAdvanced, "settings-cloud-staff": ClassAdvanced,
		"settings-printer": ClassCommon, "settings-invoice": ClassCommon,
		"settings-idle-lock": ClassCommon, "settings-staff-links": ClassCommon,
		"settings-kiosk":     ClassCommon,
		"settings-telemetry": ClassAdvanced, "settings-notices": ClassCommon,
		"settings-store-name": ClassEssential, "settings-currency": ClassEssential,
		"settings-language": ClassEssential, "settings-staff-languages": ClassCommon,
		"settings-shop-type": ClassEssential, "settings-all": ClassAdvanced,
	}
	for _, e := range CoreSettings {
		w, ok := want[e.Key]
		if !ok {
			t.Errorf("no expected class named for settings row %q", e.Key)
			continue
		}
		if e.Class != w {
			t.Errorf("settings %q class = %q, want %q", e.Key, e.Class, w)
		}
		delete(want, e.Key)
	}
	for k := range want {
		t.Errorf("expected row %q is not in CoreSettings", k)
	}
}

func TestCoreMenuBaseClassIsCommon(t *testing.T) {
	for _, e := range CoreMenu {
		if e.Class != ClassCommon {
			t.Errorf("menu %q class = %q, want common (templates demote, the base never does)", e.Key, e.Class)
		}
	}
}
