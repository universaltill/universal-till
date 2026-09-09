package plugins

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/uislot"
)

// Install-time half of ADR-0088 (ut-docs#1904): a `layout` entry's config
// is an amendment document over the Menu slot. Decision E (a protected key
// can never be hidden) and Decision F (two plugins restructuring the same
// key is a conflict; two plugins hiding the same key is not) are refused
// HERE, at PersistManifest, naming the key and the incumbent — never a
// silent render-time no-op or "first wins".

func layoutManifest(id string, amendments ...map[string]any) *Manifest {
	list := make([]any, 0, len(amendments))
	for _, a := range amendments {
		list = append(list, a)
	}
	return &Manifest{
		ID:            id,
		Name:          "Layout " + id,
		Version:       "1.0.0",
		Runtime:       "none",
		CanonicalType: "layout",
		Entries: []ManifestEntry{{
			Type:   "layout",
			Key:    "menu",
			Label:  "Layout " + id,
			Config: map[string]any{"slot": "menu", "amendments": list},
		}},
	}
}

func TestPersistManifest_RefusesHidingProtectedKey(t *testing.T) {
	for _, key := range uislot.ProtectedMenuKeys {
		t.Run(key, func(t *testing.T) {
			d := openRealDB(t)
			err := PersistManifest(context.Background(), d.DB, layoutManifest("com.example.hider", map[string]any{"key": key, "hide": true}), InstallOptions{})
			if err == nil {
				t.Fatalf("install hiding protected key %q must be refused", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("refusal must name the key, got: %v", err)
			}
			var n int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.example.hider'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("refused plugin left %d plugins row(s), want 0", n)
			}
		})
	}
}

// A protected key may be MOVED and GROUPED at install: that positions a
// statutory destination without disguising it.
func TestPersistManifest_AllowsReorderingAndRegroupingProtectedKey(t *testing.T) {
	d := openRealDB(t)
	err := PersistManifest(context.Background(), d.DB, layoutManifest("com.example.mover",
		map[string]any{"key": "/settings", "order": 50},
		map[string]any{"key": "/journal", "group": "x.group"},
	), InstallOptions{})
	if err != nil {
		t.Fatalf("reorder/re-group of protected keys must install: %v", err)
	}
}

// ...but re-label and re-icon are refused at INSTALL, not just at parse.
// ADR-0088 Decision E originally permitted them, on the reasoning that they
// "keep the destination visible". The independent review of ut-docs#1904
// disproved that by driving it: /report-issue re-labelled to "Catalog" with
// a tag icon and moved last is nominally visible and actually unfindable,
// and the Decision D recovery surface reported nothing because it lists
// hides. The label and icon are the identity a merchant recognises a
// statutory surface by, so they are protected like its presence.
//
// This is the install-path half of
// uislot.TestParseMenuAmendments_ProtectedKeyCannotBeRelabelledOrReiconed —
// it also proves the refusal rolls the whole transaction back, so a
// half-installed plugin never lingers.
func TestPersistManifest_RefusesRelabellingOrReiconingProtectedKey(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	for name, amendment := range map[string]map[string]any{
		"re-label": {"key": "/journal", "label_key": "x.journal"},
		"re-icon":  {"key": "/fiscal-register", "icon": "tag"},
	} {
		t.Run(name, func(t *testing.T) {
			err := PersistManifest(ctx, d.DB, layoutManifest("com.example.disguise", amendment), InstallOptions{})
			if err == nil {
				t.Fatal("re-labelling/re-iconing a protected key must be refused at install")
			}
			if !strings.Contains(err.Error(), amendment["key"].(string)) {
				t.Fatalf("refusal must name the protected key, got: %v", err)
			}
			var n int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.example.disguise'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("refused plugin left %d plugins row(s), want 0 (transaction must roll back)", n)
			}
		})
	}
}

func TestPersistManifest_TwoPluginsRestructuringSameKeyConflict(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, layoutManifest("com.first.layout", map[string]any{"key": "/items", "label_key": "first.services"}), InstallOptions{}); err != nil {
		t.Fatalf("first plugin must install: %v", err)
	}
	for name, amendment := range map[string]map[string]any{
		"re-label": {"key": "/items", "label_key": "second.services"},
		"reorder":  {"key": "/items", "order": 10},
		"re-group": {"key": "/items", "group": "second.group"},
		"re-icon":  {"key": "/items", "icon": "tag"},
	} {
		t.Run(name, func(t *testing.T) {
			err := PersistManifest(ctx, d.DB, layoutManifest("com.second.layout", amendment), InstallOptions{})
			if err == nil {
				t.Fatal("second plugin restructuring the same key must be refused")
			}
			if !strings.Contains(err.Error(), "/items") || !strings.Contains(err.Error(), "com.first.layout") {
				t.Fatalf("refusal must name the contested key and the incumbent plugin, got: %v", err)
			}
			var n int
			if err := d.DB.QueryRow(`SELECT COUNT(*) FROM plugins WHERE id = 'com.second.layout'`).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if n != 0 {
				t.Fatalf("refused plugin left %d plugins row(s), want 0 (transaction must roll back)", n)
			}
		})
	}
	// A DIFFERENT key never collides.
	if err := PersistManifest(ctx, d.DB, layoutManifest("com.second.layout", map[string]any{"key": "/orders", "label_key": "second.orders"}), InstallOptions{}); err != nil {
		t.Fatalf("restructuring a different key must install: %v", err)
	}
	// The incumbent's own reinstall / upgrade never self-conflicts.
	if err := PersistManifest(ctx, d.DB, layoutManifest("com.first.layout", map[string]any{"key": "/items", "label_key": "first.services.v2"}), InstallOptions{}); err != nil {
		t.Fatalf("reinstalling the incumbent must not conflict with its own rows: %v", err)
	}
}

func TestPersistManifest_TwoPluginsHidingSameKeyAccepted(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	for _, id := range []string{"com.first.layout", "com.second.layout"} {
		if err := PersistManifest(ctx, d.DB, layoutManifest(id, map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
			t.Fatalf("%s hiding /tables must install (hide is idempotent, Decision F): %v", id, err)
		}
	}
}

func TestPersistManifest_RefusesUnknownMenuKey(t *testing.T) {
	d := openRealDB(t)
	err := PersistManifest(context.Background(), d.DB, layoutManifest("com.example.typo", map[string]any{"key": "/tabels", "hide": true}), InstallOptions{})
	if err == nil || !strings.Contains(err.Error(), "/tabels") {
		t.Fatalf("an unknown key is a typo that would otherwise be a silent no-op; want a refusal naming it, got: %v", err)
	}
}

// Manager.Reload is where the render side gets its amendments from
// (ADR-0088 Decision I: no query per render — the rows are read once per
// plugin lifecycle change, like MenuPlugins). Only ACTIVE plugins count: a
// disabled layout plugin amends nothing, exactly like a disabled page
// plugin loses its tile.
func TestManagerReload_LoadsLayoutAmendmentsFromActivePluginsOnly(t *testing.T) {
	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, layoutManifest("com.active.layout", map[string]any{"key": "/tables", "hide": true}), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := PersistManifest(ctx, d.DB, layoutManifest("com.disabled.layout", map[string]any{"key": "/items", "label_key": "x.services"}), InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	repo := data.NewPluginRepo(d.DB)
	if err := repo.SetPluginActive(ctx, nil, "com.disabled.layout", false); err != nil {
		t.Fatal(err)
	}
	pm, err := Init(ctx, &config.Config{}, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if len(pm.LayoutAmendments) != 1 {
		t.Fatalf("want exactly the active plugin's amendment, got %+v", pm.LayoutAmendments)
	}
	a := pm.LayoutAmendments[0]
	if a.PluginID != "com.active.layout" || a.Key != "/tables" || !a.Hide {
		t.Fatalf("got %+v", a)
	}
}

// AC3: a real first-party layout plugin, not a fixture. plugins/layout-salon
// is a salon/barber layout: no Tables, no Kitchen stations, "Items"
// re-labelled to Services and moved first. It installs through the same
// PersistManifest every marketplace/store install uses, and ships its own
// locale files for every key it introduces, in every core locale.
func TestLayoutSalonPlugin_InstallsAndDeclaresItsAmendments(t *testing.T) {
	root := filepath.Join("..", "..", "plugins", "layout-salon")
	f, err := os.Open(filepath.Join(root, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	m, err := ParseManifest(f)
	if err != nil {
		t.Fatalf("plugins/layout-salon/plugin.json must parse: %v", err)
	}
	if m.CanonicalType != "layout" || m.Runtime != "none" {
		t.Fatalf("a layout plugin is asset-only structure: canonical_type=%q runtime=%q", m.CanonicalType, m.Runtime)
	}

	d := openRealDB(t)
	ctx := context.Background()
	if err := PersistManifest(ctx, d.DB, m, InstallOptions{}); err != nil {
		t.Fatalf("install: %v", err)
	}
	pm, err := Init(ctx, &config.Config{}, d.DB)
	if err != nil {
		t.Fatal(err)
	}
	if err := pm.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	byKey := map[string]uislot.Amendment{}
	for _, a := range pm.LayoutAmendments {
		if a.PluginID != m.ID {
			t.Fatalf("amendment from an unexpected plugin: %+v", a)
		}
		byKey[a.Key] = a
	}
	for _, hidden := range []string{"/tables", "/kitchen-stations"} {
		if a, ok := byKey[hidden]; !ok || !a.Hide {
			t.Errorf("salon layout must hide %s, got %+v", hidden, byKey[hidden])
		}
	}
	items, ok := byKey["/items"]
	if !ok || items.LabelKey == "" || items.Order == nil {
		t.Fatalf("salon layout must re-label and reorder /items, got %+v", items)
	}

	// Every locale key the plugin introduces ships in its own locale files,
	// for every core locale (Decision G: the label resolves through the
	// same overlay mechanism language packs use).
	introduced := []string{items.LabelKey}
	for _, e := range m.Entries {
		introduced = append(introduced, e.Label)
	}
	for _, a := range pm.LayoutAmendments {
		if a.Group != "" {
			introduced = append(introduced, a.Group)
		}
	}
	coreLocales, err := filepath.Glob(filepath.Join("..", "..", "web", "locales", "*.json"))
	if err != nil || len(coreLocales) == 0 {
		t.Fatalf("core locales: %v", err)
	}
	for _, core := range coreLocales {
		locale := strings.TrimSuffix(filepath.Base(core), ".json")
		raw, err := os.ReadFile(filepath.Join(root, "locales", locale+".json"))
		if err != nil {
			t.Errorf("salon layout ships no locale file for core locale %s: %v", locale, err)
			continue
		}
		var msgs map[string]string
		if err := json.Unmarshal(raw, &msgs); err != nil {
			t.Errorf("locales/%s.json: %v", locale, err)
			continue
		}
		for _, key := range introduced {
			if strings.TrimSpace(msgs[key]) == "" {
				t.Errorf("locales/%s.json is missing %q", locale, key)
			}
		}
	}
}

// shippingLocalesBeyondCore are the locales this product ships as external
// LANGUAGE PACKS (ut-plugin-language-{de,es}) rather than in web/locales.
// They are listed explicitly because no test in this repo can see those
// sibling repos, and the consequence of forgetting one is silent: a plugin
// label with no translation for the locale falls back to the core label
// (ADR-0088 Decision G, correctly), so the amendment simply does nothing
// and nothing anywhere reports it.
//
// That is not hypothetical — it is what the independent review of
// ut-docs#1904 (F4) found: the salon plugin shipped en/ar/fa/tr only, so on
// the GERMAN pilot till, the one market with a real merchant, its re-label
// of Items to "Services" was a silent no-op. The test above globs
// web/locales/*.json and by construction could never catch that.
var shippingLocalesBeyondCore = []string{"de", "es"}

func TestLayoutSalonPlugin_ShipsLocalesForTheLanguagePackMarketsToo(t *testing.T) {
	root := filepath.Join("..", "..", "plugins", "layout-salon")

	raw, err := os.ReadFile(filepath.Join(root, "locales", "en.json"))
	if err != nil {
		t.Fatalf("salon layout must ship en.json: %v", err)
	}
	var base map[string]string
	if err := json.Unmarshal(raw, &base); err != nil {
		t.Fatalf("locales/en.json: %v", err)
	}
	if len(base) == 0 {
		t.Fatal("locales/en.json declares no keys — this test would assert nothing")
	}

	for _, locale := range shippingLocalesBeyondCore {
		got, err := os.ReadFile(filepath.Join(root, "locales", locale+".json"))
		if err != nil {
			t.Errorf("salon layout ships no locale file for language-pack market %q — "+
				"its amendments would silently fall back to English there: %v", locale, err)
			continue
		}
		var msgs map[string]string
		if err := json.Unmarshal(got, &msgs); err != nil {
			t.Errorf("locales/%s.json: %v", locale, err)
			continue
		}
		for key, english := range base {
			switch v := strings.TrimSpace(msgs[key]); {
			case v == "":
				t.Errorf("locales/%s.json is missing %q", locale, key)
			case v == english:
				// The ut-docs#292 failure mode: present, non-empty, and still
				// English. A key-set-only check passes it; a merchant reads it.
				t.Errorf("locales/%s.json leaves %q untranslated (still %q)", locale, key, english)
			}
		}
	}
}
