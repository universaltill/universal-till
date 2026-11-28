package settingsnav

import (
	"testing"

	"github.com/universaltill/universal-till/internal/uislot"
)

// classMap builds the effective-class func a template would supply: rows
// absent from m keep their base class.
func classMap(m map[string]uislot.Class) ClassFunc {
	return func(key string, base uislot.Class) uislot.Class {
		if c, ok := m[key]; ok {
			return c
		}
		return base
	}
}

func rowByKey(t *testing.T, rows []Row, key string) Row {
	t.Helper()
	for _, r := range rows {
		if r.Key == key {
			return r
		}
	}
	t.Fatalf("row %q missing from %d rows", key, len(rows))
	return Row{}
}

// A nil ClassFunc (no template, unset type, legacy mode) must reproduce the
// plain Resolve output exactly: same rows, same order, no tags.
func TestResolveClassed_NilClassFuncEqualsResolve(t *testing.T) {
	initRealI18n(t)
	plain := Resolve("en", nil)
	got := ResolveClassed("en", nil, nil)
	if len(got) != len(plain) {
		t.Fatalf("len %d != %d", len(got), len(plain))
	}
	for i := range plain {
		if got[i] != plain[i] {
			t.Fatalf("row %d: %+v != %+v", i, got[i], plain[i])
		}
		if got[i].Tag != "" || got[i].HomeCat != "" {
			t.Fatalf("row %d carries template state with no template: %+v", i, got[i])
		}
	}
}

// Demoted row: Cat becomes advanced, Group (the heading in the More settings
// view) and its position among rows stay those of its normal category, and
// the tag says it was tucked away by the template.
func TestResolveClassed_DemotedRowMovesToAdvancedKeepsGroupAndOrder(t *testing.T) {
	initRealI18n(t)
	plain := Resolve("en", nil)
	got := ResolveClassed("en", nil, classMap(map[string]uislot.Class{
		"settings-order-type-prompt": uislot.ClassAdvanced,
	}))
	r := rowByKey(t, got, "settings-order-type-prompt")
	if r.Cat != uislot.SettingsAdvancedCategory {
		t.Errorf("Cat = %q, want %q", r.Cat, uislot.SettingsAdvancedCategory)
	}
	if r.Group != "Selling" {
		t.Errorf("Group = %q, want Selling (normal category heading)", r.Group)
	}
	if r.HomeCat != "selling" {
		t.Errorf("HomeCat = %q, want selling", r.HomeCat)
	}
	if r.Tag != TagTucked {
		t.Errorf("Tag = %q, want %q", r.Tag, TagTucked)
	}
	if len(got) != len(plain) {
		t.Fatalf("every row must stay exactly once: %d != %d", len(got), len(plain))
	}
	for i := range plain {
		if got[i].Key != plain[i].Key {
			t.Fatalf("order changed at %d: %s != %s", i, got[i].Key, plain[i].Key)
		}
	}
}

// A row that is advanced by default is not "tucked away by the template".
func TestResolveClassed_BaseAdvancedRowIsNotTagged(t *testing.T) {
	initRealI18n(t)
	got := ResolveClassed("en", nil, classMap(nil))
	r := rowByKey(t, got, "settings-diagnostics")
	if r.Tag != "" || r.HomeCat != "" {
		t.Errorf("base-advanced row tagged: %+v", r)
	}
}

// Fixed-class rows never move even if a (buggy) class func says advanced.
func TestResolveClassed_FixedRowsNeverDemoted(t *testing.T) {
	initRealI18n(t)
	m := map[string]uislot.Class{}
	for _, e := range uislot.CoreSettings {
		m[e.Key] = uislot.ClassAdvanced
	}
	got := ResolveClassed("en", nil, classMap(m))
	for _, e := range uislot.CoreSettings {
		if !uislot.FixedClassFor(uislot.KindSettings, e.Key) {
			continue
		}
		r := rowByKey(t, got, e.Key)
		if r.Tag == TagTucked || (e.Class != uislot.ClassAdvanced && r.Cat == uislot.SettingsAdvancedCategory) {
			t.Errorf("fixed row %s was demoted: %+v", e.Key, r)
		}
	}
}

// Essential rows sort first inside their category and carry the Recommended
// tag; categories keep their order.
func TestResolveClassed_EssentialSortsFirstAndIsTagged(t *testing.T) {
	initRealI18n(t)
	got := ResolveClassed("en", nil, classMap(map[string]uislot.Class{
		"settings-stock-tracking": uislot.ClassEssential,
	}))
	var selling []string
	for _, r := range got {
		if r.Cat == "selling" {
			selling = append(selling, r.Key)
		}
	}
	if len(selling) == 0 || selling[0] != "settings-stock-tracking" {
		t.Fatalf("selling order = %v, want stock tracking first", selling)
	}
	if r := rowByKey(t, got, "settings-stock-tracking"); r.Tag != TagRecommended {
		t.Errorf("Tag = %q, want %q", r.Tag, TagRecommended)
	}
	// Order among the rest is unchanged.
	if selling[1] != "settings-order-no" {
		t.Errorf("second selling row = %s, want settings-order-no", selling[1])
	}
}

// Plugin layout wins: a row a layout plugin moved into its own group is not
// demoted by the template.
func TestResolveClassed_PluginGroupWinsOverTemplate(t *testing.T) {
	initRealI18n(t)
	got := ResolveClassed("en", []uislot.Amendment{
		{PluginID: "layout.x", Slot: uislot.SettingsSlot, Key: "settings-printer", Group: "layout.x.Hardware"},
	}, classMap(map[string]uislot.Class{"settings-printer": uislot.ClassAdvanced}))
	r := rowByKey(t, got, "settings-printer")
	if r.Cat != "g-layout-x-hardware" && r.Cat[:2] != "g-" {
		t.Errorf("Cat = %q, want a plugin group", r.Cat)
	}
	if r.Tag != "" {
		t.Errorf("plugin-group row must not carry a template tag: %+v", r)
	}
}

// A category whose every row is demoted gets no tile; More settings stays.
func TestCategories_AllDemotedCategoryHasNoTile(t *testing.T) {
	initRealI18n(t)
	m := map[string]uislot.Class{}
	for _, e := range uislot.CoreSettings {
		if e.Group == "settings.cat.selling.label" {
			if uislot.FixedClassFor(uislot.KindSettings, e.Key) {
				t.Fatalf("selling row %s is fixed; pick another category", e.Key)
			}
			m[e.Key] = uislot.ClassAdvanced
		}
	}
	cats := Categories("en", ResolveClassed("en", nil, classMap(m)))
	for _, c := range cats {
		if c.ID == "selling" {
			t.Errorf("fully demoted selling category still has a tile")
		}
	}
	if last := cats[len(cats)-1]; last.ID != uislot.SettingsAdvancedCategory {
		t.Errorf("last tile = %q, want More settings (advanced)", last.ID)
	}
}
