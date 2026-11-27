package common

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/shoptemplate"
	"github.com/universaltill/universal-till/internal/shoptemplate/shoptemplatetest"
	"github.com/universaltill/universal-till/internal/uislot"
)

func ip(n int) *int { return &n }

// twoTemplates: retail hides Tables and relabels Items; service also has a
// legacy_layout (Tables hidden, Items relabelled differently).
func twoTemplates(t *testing.T) *shoptemplate.Set {
	t.Helper()
	return shoptemplatetest.TestSet(t,
		&shoptemplate.Template{
			Schema: 1, ID: "retail", Version: 1, ShopTypes: []string{"retail"},
			Menu: map[string]shoptemplate.Value{
				"/tables": {Class: uislot.ClassAdvanced},
				"/items":  {LabelKey: "tmpl.items", Order: ip(50)},
			},
		},
		&shoptemplate.Template{
			Schema: 1, ID: "service", Version: 1, ShopTypes: []string{"service"},
			LegacyLayout: &shoptemplate.Legacy{Menu: []shoptemplate.LegacyEntry{{Key: "/kitchen-stations", Hide: true}}},
		},
	)
}

func depsWithTemplates(t *testing.T, shopType, ledger string) *Deps {
	t.Helper()
	ctx := context.Background()
	st := newTestStore(t)
	d := &Deps{Settings: st, ShopTemplates: twoTemplates(t)}
	if shopType != "" {
		if err := st.Set(ctx, KeyShopType, shopType); err != nil {
			t.Fatal(err)
		}
	}
	if ledger != "" {
		if err := st.Set(ctx, shoptemplate.LedgerKey, ledger); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestTemplateState_Modes(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name, shopType, ledger string
		want                   TemplateMode
		effective              bool
	}{
		{"no shop type", "", "", TemplateNone, false},
		{"unknown shop type", "spaceship", "", TemplateNone, false},
		{"type set, no ledger = legacy", "retail", "", TemplateLegacy, false},
		{"type set, ledger = active", "retail", `{"id":"retail","version":1}`, TemplateActive, true},
	} {
		d := depsWithTemplates(t, tc.shopType, tc.ledger)
		got := d.TemplateState(ctx)
		if got.Mode != tc.want {
			t.Errorf("%s: mode = %v, want %v", tc.name, got.Mode, tc.want)
		}
		if (got.Effective() != nil) != tc.effective {
			t.Errorf("%s: Effective() set = %v, want %v", tc.name, got.Effective() != nil, tc.effective)
		}
	}
	// No template set loaded (failed to load) or a bare Deps: none, never a panic.
	var bare Deps
	if got := bare.TemplateState(ctx); got.Mode != TemplateNone {
		t.Errorf("bare Deps mode = %v, want none", got.Mode)
	}
	var nilDeps *Deps
	if got := nilDeps.TemplateState(ctx); got.Mode != TemplateNone {
		t.Errorf("nil Deps mode = %v, want none", got.Mode)
	}
}

func hides(list []uislot.Amendment, key string) bool {
	for _, a := range list {
		if a.Key == key && a.Hide {
			return true
		}
	}
	return false
}

// Active mode applies the template's own menu; legacy mode applies only the
// legacy_layout block (a retail shop that predates templates is unchanged).
func TestRebuildLayout_ActiveVersusLegacy(t *testing.T) {
	ctx := context.Background()

	active := depsWithTemplates(t, "retail", `{"id":"retail"}`)
	active.ReloadTemplate(ctx)
	if !hides(active.MenuAmendmentsSnapshot(), "/tables") {
		t.Errorf("active retail must hide /tables, got %+v", active.MenuAmendmentsSnapshot())
	}

	legacyRetail := depsWithTemplates(t, "retail", "")
	legacyRetail.ReloadTemplate(ctx)
	if n := len(legacyRetail.MenuAmendmentsSnapshot()); n != 0 {
		t.Errorf("legacy retail (no legacy_layout) must change nothing, got %d amendments", n)
	}

	legacyService := depsWithTemplates(t, "service", "")
	legacyService.ReloadTemplate(ctx)
	if !hides(legacyService.MenuAmendmentsSnapshot(), "/kitchen-stations") {
		t.Errorf("legacy service keeps its old layout (hide /kitchen-stations), got %+v", legacyService.MenuAmendmentsSnapshot())
	}
}

// An owner's restore of a template-hidden tile beats the template, and the
// restore is by key, so it survives a change of shop type.
func TestRebuildLayout_RestoredKeySurvivesTypeChange(t *testing.T) {
	ctx := context.Background()
	d := depsWithTemplates(t, "retail", `{"id":"retail"}`)
	raw, err := EncodeRestoredMenuKeys(map[string]bool{"/tables": true})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Settings.Set(ctx, MenuRestoredKeysSetting, raw); err != nil {
		t.Fatal(err)
	}
	d.ReloadTemplate(ctx)
	if hides(d.MenuAmendmentsSnapshot(), "/tables") {
		t.Fatalf("a restored /tables must not be hidden by the template, got %+v", d.MenuAmendmentsSnapshot())
	}
	// The relabel half of the template still applies.
	if len(d.MenuAmendmentsSnapshot()) != 1 {
		t.Errorf("only the Tables hide is cancelled, got %+v", d.MenuAmendmentsSnapshot())
	}
	// Type change (still active): the restore is keyed by tile, not by type.
	if err := d.Settings.Set(ctx, KeyShopType, "service"); err != nil {
		t.Fatal(err)
	}
	d.ReloadTemplate(ctx)
	if got := d.MenuAmendmentsSnapshot(); hides(got, "/tables") {
		t.Errorf("after a type change the restored tile stays restored, got %+v", got)
	}
	restored := RestoredMenuKeys(ctx, d.Settings)
	if !restored["/tables"] {
		t.Errorf("the restored key must still be stored after a type change, got %v", restored)
	}
}

// A plugin amendment on the same key wins over the template's: template
// amendments are first in the pool and uislot.Resolve applies later ones last.
func TestPluginAmendmentBeatsTemplate(t *testing.T) {
	tmpl := []uislot.Amendment{{PluginID: shoptemplate.PluginID, Slot: uislot.MenuSlot, Key: "/items", LabelKey: "tmpl.items", Order: ip(50)}}
	plug := []uislot.Amendment{{PluginID: "com.example.layout", Slot: uislot.MenuSlot, Key: "/items", LabelKey: "plugin.items"}}
	got := withTemplate(tmpl, plug, nil)
	if len(got) != 2 || got[0].PluginID != shoptemplate.PluginID || got[1].PluginID != "com.example.layout" {
		t.Fatalf("template must come first, plugin second, got %+v", got)
	}
	items, ok := uislot.CoreMenuEntry("/items")
	if !ok {
		t.Fatal("no /items core row")
	}
	resolved := uislot.Resolve([]uislot.Entry{items}, got)
	if resolved[0].LabelKey != "plugin.items" {
		t.Errorf("label = %q, want the plugin's plugin.items", resolved[0].LabelKey)
	}
	if resolved[0].Order != 50 {
		t.Errorf("order = %d, want the template's 50 (the plugin said nothing about it)", resolved[0].Order)
	}
}

// The zero-amendment path (no template, no plugin) allocates nothing.
func TestWithTemplate_NoTemplateAllocatesNothing(t *testing.T) {
	plug := []uislot.Amendment{{Key: "/tables", Hide: true}}
	var out []uislot.Amendment
	if n := testing.AllocsPerRun(100, func() {
		out = withTemplate(nil, plug, nil)
		_ = withTemplate(nil, nil, nil)
	}); n != 0 {
		t.Errorf("withTemplate with no template allocated %v times, want 0", n)
	}
	if len(out) != 1 {
		t.Errorf("plugin amendments must pass through untouched, got %+v", out)
	}
}

// A shop type or ledger that arrives by admin pull is noticed by the
// reconcile tick; an unchanged state costs no rebuild.
func TestReloadTemplateIfChanged(t *testing.T) {
	ctx := context.Background()
	d := depsWithTemplates(t, "retail", "")
	d.ReloadTemplate(ctx)
	if d.ReloadTemplateIfChanged(ctx) {
		t.Error("nothing changed: must not rebuild")
	}
	if err := d.Settings.Set(ctx, shoptemplate.LedgerKey, `{"id":"retail"}`); err != nil { // ledger arrives by bundle
		t.Fatal(err)
	}
	if !d.ReloadTemplateIfChanged(ctx) {
		t.Fatal("a ledger that appeared must rebuild")
	}
	if !hides(d.MenuAmendmentsSnapshot(), "/tables") {
		t.Errorf("after the rebuild the active retail layout hides /tables, got %+v", d.MenuAmendmentsSnapshot())
	}
	if err := d.Settings.Set(ctx, KeyShopType, "service"); err != nil { // type pulled from the main till
		t.Fatal(err)
	}
	if !d.ReloadTemplateIfChanged(ctx) {
		t.Fatal("a changed shop type must rebuild")
	}
	if hides(d.MenuAmendmentsSnapshot(), "/tables") {
		t.Errorf("service (active, no menu block) must not hide /tables, got %+v", d.MenuAmendmentsSnapshot())
	}
	if d.ReloadTemplateIfChanged(ctx) {
		t.Error("state unchanged again: must not rebuild")
	}
}

// A service shop whose manager had switched the old salon layout off keeps
// the full menu after upgrade: the legacy-off marker silences legacy_layout
// until a template is accepted (ADR-0190 Q1, never forced).
func TestLegacyLayoutOff_SilencesLegacyUntilAccepted(t *testing.T) {
	ctx := context.Background()
	d := depsWithTemplates(t, "service", "")
	if err := d.Settings.Set(ctx, shoptemplate.LegacyLayoutOffKey, "1"); err != nil {
		t.Fatal(err)
	}
	d.ReloadTemplate(ctx)
	if n := len(d.MenuAmendmentsSnapshot()); n != 0 {
		t.Fatalf("legacy service with the opt-out marker must change nothing, got %+v", d.MenuAmendmentsSnapshot())
	}
	if got := d.TemplateState(ctx); got.Mode != TemplateLegacy || !got.LegacyOff {
		t.Errorf("state = %+v, want legacy with LegacyOff", got)
	}
	// Accepting the template ends the opt-out: the active template applies.
	if err := d.Settings.Set(ctx, shoptemplate.LedgerKey, `{"id":"service"}`); err != nil {
		t.Fatal(err)
	}
	d.ReloadTemplate(ctx)
	if got := d.TemplateState(ctx); got.Mode != TemplateActive || got.Effective() == nil {
		t.Errorf("accepted template must be active, got %+v", got)
	}
}
