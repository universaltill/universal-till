package shoptemplate

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/uislot"
)

const salonLike = `"settings":{"settings-theme":"common","settings-printer":"advanced","settings-tills":"essential"},` +
	`"settings_layout":{"settings-theme":{"order":50},"settings-printer":{"group":"g.hw"},"settings-tills":{"group":"g.hw"}},` +
	`"menu":{"/tables":"advanced","/kitchen-stations":"advanced","/items":{"label_key":"lbl.services","icon":"scissors","order":50}},` +
	`"items":{"/catalog":{"label_key":"lbl.services","order":50}},` +
	`"rail":{"/orders":{"order":250}},` +
	`"seed":[{"key":"sale.order_type_prompt","value":"off"}],` +
	`"legacy_layout":{"menu":[{"key":"/tables","hide":true},{"key":"/items","label_key":"lbl.services","icon":"scissors","order":50}],"items":[{"key":"/catalog","order":50}],"rail":[{"key":"/orders","order":250}],"settings":[{"key":"settings-printer","group":"g.hw"}]}`

func serviceSet(t *testing.T) *Resolved {
	t.Helper()
	return mustLoad(t, map[string]string{"service": tmpl("service", []string{"service"}, salonLike)}).ForType("service")
}

func TestResolvedIdentity(t *testing.T) {
	r := serviceSet(t)
	if r.ID() != "service" || r.Version() != 1 {
		t.Fatalf("ID/Version = %q/%d, want service/1", r.ID(), r.Version())
	}
	if r.LabelKey() != "lbl" || r.SummaryKey() != "sum" {
		t.Fatalf("label/summary = %q/%q, want lbl/sum", r.LabelKey(), r.SummaryKey())
	}
}

func TestClassForPerKind(t *testing.T) {
	r := serviceSet(t)
	cases := []struct {
		kind Kind
		key  string
		base uislot.Class
		want uislot.Class
	}{
		{KindSettings, "settings-printer", uislot.ClassCommon, uislot.ClassAdvanced},
		{KindSettings, "settings-tills", uislot.ClassCommon, uislot.ClassEssential},
		{KindSettings, "settings-theme", uislot.ClassAdvanced, uislot.ClassCommon},
		{KindMenu, "/tables", uislot.ClassCommon, uislot.ClassAdvanced},
		{KindMenu, "/items", uislot.ClassCommon, uislot.ClassCommon}, // object without class: base kept
		{KindSettings, "settings-unlisted", uislot.ClassEssential, uislot.ClassEssential},
		{KindMenu, "settings-printer", uislot.ClassCommon, uislot.ClassCommon}, // same key, other kind: no leak
	}
	for _, c := range cases {
		if got := r.ClassFor(c.kind, c.key, c.base); got != c.want {
			t.Errorf("ClassFor(%s,%s,base=%s) = %q, want %q", c.kind, c.key, c.base, got, c.want)
		}
	}
}

func TestNilResolvedReturnsBaseAndNothing(t *testing.T) {
	var r *Resolved
	if got := r.ClassFor(KindMenu, "/tables", uislot.ClassCommon); got != uislot.ClassCommon {
		t.Fatalf("nil receiver ClassFor = %q, want the base class common", got)
	}
	if r.Amendments("menu") != nil || r.LegacyAmendments("menu") != nil || r.Seeds() != nil || r.HasLegacy() {
		t.Fatal("nil receiver must return no amendments, no seeds, no legacy")
	}
	if r.ID() != "" || r.Version() != 0 {
		t.Fatal("nil receiver identity must be empty")
	}
}

func TestForTypeUnsetOrUnknownIsNil(t *testing.T) {
	s := mustLoad(t, nil)
	for _, ty := range []string{"", "spaceport", "RETAIL"} {
		if s.ForType(ty) != nil {
			t.Errorf("ForType(%q) != nil, want nil (no template)", ty)
		}
	}
	var nilSet *Set
	if nilSet.ForType("retail") != nil {
		t.Error("nil Set ForType must be nil")
	}
	if s.ForType("retail") == nil {
		t.Error("ForType(retail) = nil, want the retail template")
	}
}

func TestForTypeUnsetAllocatesNothing(t *testing.T) {
	s := mustLoad(t, nil)
	r := s.ForType("retail")
	n := testing.AllocsPerRun(100, func() {
		_ = s.ForType("")
		_ = s.ForType("retail")
		_ = r.ClassFor(KindMenu, "/tables", uislot.ClassCommon)
		_ = r.Amendments("menu")
		_ = r.LegacyAmendments("menu")
		var none *Resolved
		_ = none.ClassFor(KindMenu, "/tables", uislot.ClassCommon)
	})
	if n != 0 {
		t.Fatalf("allocs per run = %v, want 0", n)
	}
}

func TestMenuAmendmentsHideDemotedAndCarryObjectFields(t *testing.T) {
	r := serviceSet(t)
	am := r.Amendments("menu")
	// sorted by key: /items, /kitchen-stations, /tables
	if len(am) != 3 {
		t.Fatalf("menu amendments = %+v, want 3", am)
	}
	items, kitchen, tables := am[0], am[1], am[2]
	if items.Key != "/items" || items.LabelKey != "lbl.services" || items.Icon != "scissors" || items.Order == nil || *items.Order != 50 || items.Hide {
		t.Errorf("/items amendment = %+v, want label lbl.services icon scissors order 50, not hidden", items)
	}
	if !kitchen.Hide || kitchen.Key != "/kitchen-stations" || !tables.Hide || tables.Key != "/tables" {
		t.Errorf("kitchen/tables = %+v / %+v, want both hidden", kitchen, tables)
	}
	for _, a := range am {
		if a.PluginID != "core.template" || a.Slot != "menu" {
			t.Errorf("amendment %+v: want PluginID core.template and Slot menu", a)
		}
	}
}

func TestAmendmentsPerSlot(t *testing.T) {
	r := serviceSet(t)
	if am := r.Amendments("items"); len(am) != 1 || am[0].Key != "/catalog" || am[0].LabelKey != "lbl.services" || *am[0].Order != 50 || am[0].Slot != "items" {
		t.Errorf("items amendments = %+v", am)
	}
	if am := r.Amendments("rail"); len(am) != 1 || am[0].Key != "/orders" || *am[0].Order != 250 || am[0].Slot != "rail" {
		t.Errorf("rail amendments = %+v", am)
	}
	set := r.Amendments("settings")
	if len(set) != 3 || set[0].Key != "settings-printer" || set[0].Group != "g.hw" || set[1].Key != "settings-theme" || *set[1].Order != 50 || set[2].Key != "settings-tills" || set[2].Slot != "settings" {
		t.Errorf("settings amendments (sorted by key) = %+v", set)
	}
	if r.Amendments("nonsense") != nil {
		t.Error("unknown slot must have no amendments")
	}
}

func TestSettingsClassDoesNotBecomeAnAmendment(t *testing.T) {
	// demotion is a class, read via ClassFor by the Settings nav; it must not
	// hide a Settings row (nothing is ever removed, ADR-0190)
	s := mustLoad(t, map[string]string{"retail": tmpl("retail", []string{"retail"}, `"settings":{"settings-printer":"advanced"}`)})
	if am := s.ForType("retail").Amendments("settings"); len(am) != 0 {
		t.Fatalf("settings amendments = %+v, want none", am)
	}
}

func TestLegacyAmendments(t *testing.T) {
	r := serviceSet(t)
	if !r.HasLegacy() {
		t.Fatal("service fixture has legacy_layout, HasLegacy = false")
	}
	m := r.LegacyAmendments("menu")
	if len(m) != 2 || m[0].Key != "/tables" || !m[0].Hide || m[1].Key != "/items" || m[1].Icon != "scissors" || m[1].PluginID != "core.template" || m[1].Slot != "menu" {
		t.Errorf("legacy menu = %+v, want declared order: hide /tables then relabel /items", m)
	}
	if st := r.LegacyAmendments("settings"); len(st) != 1 || st[0].Group != "g.hw" {
		t.Errorf("legacy settings = %+v", st)
	}
	plain := mustLoad(t, nil).ForType("retail")
	if plain.HasLegacy() || plain.LegacyAmendments("menu") != nil {
		t.Error("a template without legacy_layout has no legacy amendments")
	}
}

func TestSeedsReturnedAreACopy(t *testing.T) {
	r := serviceSet(t)
	got := r.Seeds()
	if len(got) != 1 || got[0] != (Seed{Key: "sale.order_type_prompt", Value: "off"}) {
		t.Fatalf("seeds = %+v, want sale.order_type_prompt=off", got)
	}
	got[0].Value = "mutated"
	if r.Seeds()[0].Value != "off" {
		t.Fatal("mutating the returned slice changed the resolved template")
	}
}

func TestEmbeddedTemplatesLoadAgainstPermissiveCatalog(t *testing.T) {
	s, err := Load(permissiveCatalog{})
	if err != nil {
		t.Fatalf("Load(embedded): %v", err)
	}
	for _, ty := range []string{"retail", "cafe", "hospitality", "service", "market_stall", "other"} {
		if s.ForType(ty) == nil {
			t.Errorf("embedded set has no template for %q", ty)
		}
	}
}

// permissiveCatalog accepts every name: it checks the embedded files parse
// and cover every type, not that G8's content names real rows (that is the
// real-catalog test in internal/pages, G5).
type permissiveCatalog struct{}

func (permissiveCatalog) HasKey(Kind, string) bool           { return true }
func (permissiveCatalog) FixedClass(Kind, string) bool       { return false }
func (permissiveCatalog) RegistryTier(string) (string, bool) { return "normal", true }
func (permissiveCatalog) CapabilityKnown(string) bool        { return true }
func (permissiveCatalog) EnKey(string) bool                  { return true }

func TestPackageNeverImportsPages(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, dep := range strings.Fields(string(out)) {
		if strings.Contains(dep, "universal-till/internal/pages") || strings.Contains(dep, "universal-till/internal/data") {
			t.Fatalf("shoptemplate depends on %s; it may import only shoptype and uislot (ADR-0190, design-A 2.1)", dep)
		}
	}
}

func TestTestSetBuildsASetForOtherPackages(t *testing.T) {
	s := testSet(t, &Template{
		Schema: 1, ID: "retail", Version: 1, ShopTypes: []string{"retail"},
		Settings: map[string]Value{"settings-printer": {Class: uislot.ClassAdvanced}},
	})
	if got := s.ForType("retail").ClassFor(KindSettings, "settings-printer", uislot.ClassCommon); got != uislot.ClassAdvanced {
		t.Fatalf("TestSet class = %q, want advanced", got)
	}
	if s.ForType("cafe") != nil {
		t.Fatal("TestSet must not invent templates for types it was not given")
	}
}
