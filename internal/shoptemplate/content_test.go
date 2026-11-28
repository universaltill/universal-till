package shoptemplate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/universaltill/universal-till/internal/uislot"
)

// realCatalog is the injected Catalog over the shipped slots, settings
// registry and en.json, so the shipped templates are validated exactly as
// the till validates them at boot.
type realCatalog struct {
	tier map[string]string
	en   map[string]bool
}

func newRealCatalog(t *testing.T) *realCatalog {
	t.Helper()
	root := filepath.Join("..", "..")
	var reg struct {
		Entries []struct {
			Key  string `json:"key"`
			Tier string `json:"tier"`
		} `json:"entries"`
	}
	readJSON(t, filepath.Join(root, "docs", "till-settings-registry.json"), &reg)
	c := &realCatalog{tier: map[string]string{}, en: map[string]bool{}}
	for _, e := range reg.Entries {
		c.tier[e.Key] = e.Tier
	}
	var en map[string]string
	readJSON(t, filepath.Join(root, "web", "locales", "en.json"), &en)
	for k := range en {
		c.en[k] = true
	}
	return c
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func (c *realCatalog) HasKey(k Kind, key string) bool {
	var ok bool
	switch k {
	case KindSettings:
		_, ok = uislot.CoreSettingsEntry(key)
	case KindMenu:
		_, ok = uislot.CoreMenuEntry(key)
	case KindItems:
		_, ok = uislot.CoreItemsEntry(key)
	case KindRail:
		_, ok = uislot.CoreRailEntry(key)
	}
	return ok
}
func (c *realCatalog) FixedClass(k Kind, key string) bool {
	return uislot.FixedClassFor(string(k), key)
}
func (c *realCatalog) RegistryTier(key string) (string, bool) {
	tier, ok := c.tier[key]
	return tier, ok
}

// No shipped template suggests a capability in v1.
func (c *realCatalog) CapabilityKnown(string) bool { return false }
func (c *realCatalog) EnKey(key string) bool       { return c.en[key] }

func loadReal(t *testing.T) *Set {
	t.Helper()
	s, err := Load(newRealCatalog(t))
	if err != nil {
		t.Fatalf("Load against the real catalog: %v", err)
	}
	return s
}

func TestShippedTemplatesLoadAgainstRealCatalog(t *testing.T) {
	s := loadReal(t)
	for _, id := range []string{"retail", "cafe", "hospitality", "service", "market_stall", "other"} {
		r := s.ForType(id)
		if r == nil {
			t.Fatalf("no template for shop type %q", id)
		}
		if r.ID() != id || r.Version() != 1 {
			t.Errorf("%s: ID/Version = %q/%d, want %s/1", id, r.ID(), r.Version(), id)
		}
		if want := "setup.shop_type." + id; r.LabelKey() != want {
			t.Errorf("%s: LabelKey = %q, want %q", id, r.LabelKey(), want)
		}
		if want := "shoptemplate." + id + ".summary"; r.SummaryKey() != want {
			t.Errorf("%s: SummaryKey = %q, want %q", id, r.SummaryKey(), want)
		}
		if len(r.Suggest()) != 0 {
			t.Errorf("%s: v1 templates suggest nothing, got %v", id, r.Suggest())
		}
	}
}

const (
	adv = uislot.ClassAdvanced
	ess = uislot.ClassEssential
)

var kioskAdv = map[string]uislot.Class{"settings-kiosk": adv}

func merge(ms ...map[string]uislot.Class) map[string]uislot.Class {
	out := map[string]uislot.Class{}
	for _, m := range ms {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// Expected content per shop type: Settings class for every card the template
// names (every other card keeps its base class), the Menu tiles it hides, the
// Menu tiles it promotes and the apply-once seeds. Named values, not derived.
var contentCases = []struct {
	id       string
	settings map[string]uislot.Class
	hidden   []string
	menuEss  []string
	seeds    []Seed
}{
	{
		id: "retail",
		settings: merge(kioskAdv, map[string]uislot.Class{
			"settings-order-no": adv, "settings-order-type-prompt": adv, "settings-about": adv,
			"settings-notices": adv, "settings-stock-tracking": ess, "settings-printer": ess,
		}),
		hidden: []string{"/bluetooth-devices", "/kitchen-stations", "/open-orders", "/tables"},
		seeds:  []Seed{{Key: "sale.order_type_prompt", Value: "off"}},
	},
	{
		id: "cafe",
		settings: merge(kioskAdv, map[string]uislot.Class{
			"settings-order-no": ess, "settings-order-type-prompt": ess, "settings-printer": ess,
			"settings-invoice": adv, "settings-about": adv, "settings-notices": adv,
		}),
		hidden: []string{"/bluetooth-devices", "/kitchen-stations", "/tables", "/vouchers"},
	},
	{
		// extends cafe: same settings, Tables and Kitchen stations promoted.
		id: "hospitality",
		settings: merge(kioskAdv, map[string]uislot.Class{
			"settings-order-no": ess, "settings-order-type-prompt": ess, "settings-printer": ess,
			"settings-invoice": adv, "settings-about": adv, "settings-notices": adv,
		}),
		hidden:  []string{"/bluetooth-devices", "/vouchers"},
		menuEss: []string{"/kitchen-stations", "/tables"},
	},
	{
		id: "service",
		settings: merge(kioskAdv, map[string]uislot.Class{
			"settings-order-no": adv, "settings-order-type-prompt": adv, "settings-sell-screen": adv,
			"settings-stock-tracking": adv, "settings-about": adv, "settings-notices": adv,
		}),
		hidden: []string{"/bluetooth-devices", "/kitchen-stations", "/tables"},
		seeds:  []Seed{{Key: "sale.order_type_prompt", Value: "off"}},
	},
	{
		id: "market_stall",
		settings: merge(kioskAdv, map[string]uislot.Class{
			"settings-order-no": adv, "settings-order-type-prompt": adv, "settings-sell-screen": adv,
			"settings-stock-tracking": ess, "settings-printer": adv, "settings-tills-this": adv, "settings-tills-others": adv,
			"settings-tills-old": adv, "settings-tills-add": adv, "settings-tills-readers": adv,
			"settings-invoice": adv, "settings-idle-lock": adv, "settings-staff-links": adv,
			"settings-staff-languages": adv, "settings-about": adv, "settings-notices": adv,
		}),
		hidden: []string{"/bluetooth-devices", "/kitchen-stations", "/locations", "/open-orders", "/registers", "/tables", "/vouchers"},
	},
	{
		id:     "other",
		hidden: []string{"/kitchen-stations", "/tables"},
	},
}

func TestTemplateContentPerShopType(t *testing.T) {
	s := loadReal(t)
	for _, tc := range contentCases {
		t.Run(tc.id, func(t *testing.T) {
			r := s.ForType(tc.id)
			// Every Settings card: the expected class, else the base class.
			for _, row := range uislot.CoreSettings {
				want := row.Class
				if c, ok := tc.settings[row.Key]; ok {
					want = c
				}
				if got := r.ClassFor(KindSettings, row.Key, row.Class); got != want {
					t.Errorf("settings %s: class = %q, want %q", row.Key, got, want)
				}
			}
			// Every Menu tile: advanced iff expected hidden, else base.
			hidden := map[string]bool{}
			for _, k := range tc.hidden {
				hidden[k] = true
			}
			ess := map[string]bool{}
			for _, k := range tc.menuEss {
				ess[k] = true
			}
			for _, row := range uislot.CoreMenu {
				want := row.Class
				switch {
				case hidden[row.Key]:
					want = adv
				case ess[row.Key]:
					want = uislot.ClassEssential
				}
				if got := r.ClassFor(KindMenu, row.Key, row.Class); got != want {
					t.Errorf("menu %s: class = %q, want %q", row.Key, got, want)
				}
			}
			// The menu amendments hide exactly the expected tiles.
			var gotHidden []string
			for _, a := range r.Amendments(uislot.MenuSlot) {
				if a.PluginID != PluginID {
					t.Errorf("menu amendment %s: PluginID = %q, want %q", a.Key, a.PluginID, PluginID)
				}
				if a.Hide {
					gotHidden = append(gotHidden, a.Key)
				}
			}
			sort.Strings(gotHidden)
			if !equalStrings(gotHidden, tc.hidden) {
				t.Errorf("hidden menu tiles = %v, want %v", gotHidden, tc.hidden)
			}
			// Seeds.
			gotSeeds := r.Seeds()
			if len(gotSeeds) != len(tc.seeds) {
				t.Fatalf("seeds = %v, want %v", gotSeeds, tc.seeds)
			}
			for i := range tc.seeds {
				if gotSeeds[i] != tc.seeds[i] {
					t.Errorf("seed %d = %v, want %v", i, gotSeeds[i], tc.seeds[i])
				}
			}
			// Only service keeps a legacy layout.
			if got, want := r.HasLegacy(), tc.id == "service"; got != want {
				t.Errorf("HasLegacy = %v, want %v", got, want)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestNoTemplateTouchesAFixedClassRow walks every shipped template against
// the fixed-class tables: statutory/protected rows keep their base class and
// are never hidden, whatever a template says.
func TestNoTemplateTouchesAFixedClassRow(t *testing.T) {
	s := loadReal(t)
	for _, id := range []string{"retail", "cafe", "hospitality", "service", "market_stall", "other"} {
		r := s.ForType(id)
		for _, key := range uislot.FixedMenu {
			base, _ := uislot.CoreMenuEntry(key)
			if got := r.ClassFor(KindMenu, key, base.Class); got != base.Class {
				t.Errorf("%s: fixed menu %s class = %q, want base %q", id, key, got, base.Class)
			}
		}
		for _, key := range uislot.FixedSettings {
			base, _ := uislot.CoreSettingsEntry(key)
			if got := r.ClassFor(KindSettings, key, base.Class); got != base.Class {
				t.Errorf("%s: fixed settings %s class = %q, want base %q", id, key, got, base.Class)
			}
		}
		for _, a := range r.Amendments(uislot.MenuSlot) {
			if a.Hide && uislot.IsProtectedMenuKey(a.Key) {
				t.Errorf("%s: hides protected menu tile %s", id, a.Key)
			}
		}
	}
}

// salon* are frozen copies of the amendments the retired plugins/layout-salon/plugin.json carried
// (v0.4.1), taken before the plugin is deleted: the service template's
// legacy_layout must replay them exactly (E3).
func ip(n int) *int { return &n }

var salonLegacy = map[string][]LegacyEntryWant{
	uislot.MenuSlot: {
		{Key: "/tables", Hide: true},
		{Key: "/kitchen-stations", Hide: true},
		{Key: "/items", LabelKey: "layout.salon.services", Icon: "scissors", Order: ip(50)},
	},
	uislot.ItemsSlot: {
		{Key: "/catalog", LabelKey: "layout.salon.services", Order: ip(50)},
	},
	uislot.RailSlot: {
		{Key: "/orders", Order: ip(250)},
	},
	uislot.SettingsSlot: {
		{Key: "settings-theme", Order: ip(50)},
		{Key: "settings-printer", Group: "layout.salon.settings_group"},
		{Key: "settings-tills-this", Group: "layout.salon.settings_group"},
	},
}

// LegacyEntryWant mirrors one plugin amendment.
type LegacyEntryWant struct {
	Key      string
	Hide     bool
	Order    *int
	LabelKey string
	Group    string
	Icon     string
}

func TestServiceLegacyLayoutIsTheFrozenSalonLayout(t *testing.T) {
	r := loadReal(t).ForType("service")
	for slot, want := range salonLegacy {
		got := r.LegacyAmendments(slot)
		if len(got) != len(want) {
			t.Fatalf("legacy %s: %d amendments, want %d (%v)", slot, len(got), len(want), got)
		}
		for i, w := range want {
			g := got[i]
			if g.Key != w.Key || g.Hide != w.Hide || g.LabelKey != w.LabelKey || g.Group != w.Group || g.Icon != w.Icon || !sameInt(g.Order, w.Order) {
				t.Errorf("legacy %s[%d] = %+v, want %+v", slot, i, g, w)
			}
		}
	}
}

// The accepted (non-legacy) service template renders the same salon layout:
// every restructure the legacy block has is also in the template amendments.
func TestServiceTemplateRestructuresMatchSalonLayout(t *testing.T) {
	r := loadReal(t).ForType("service")
	for slot, want := range salonLegacy {
		got := map[string]uislot.Amendment{}
		for _, a := range r.Amendments(slot) {
			got[a.Key] = a
		}
		for _, w := range want {
			a, ok := got[w.Key]
			if !ok {
				t.Errorf("template %s: no amendment for %s", slot, w.Key)
				continue
			}
			if a.Hide != w.Hide || a.LabelKey != w.LabelKey || a.Group != w.Group || a.Icon != w.Icon || !sameInt(a.Order, w.Order) {
				t.Errorf("template %s %s = %+v, want %+v", slot, w.Key, a, w)
			}
		}
	}
}

func sameInt(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
