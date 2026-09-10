package pages

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ADR-0088 Decision D: findability is mandatory. Settings lists every
// destination a layout plugin currently hides, names the plugin, and
// restores per entry — a merchant can always recover a destination without
// uninstalling the plugin.

func newMenuLayoutSettingsDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newMenuPageTestDeps(t, baseMenu)
	registerMenuLayoutSettings(mux, dp)
	return mux, dp
}

func installSalonLayout(t *testing.T, dp *common.Deps) {
	t.Helper()
	m := &plugins.Manifest{
		ID: "com.example.salon", Name: "Salon layout", Version: "1.0.0", Runtime: "none", CanonicalType: "layout",
		Entries: []plugins.ManifestEntry{{Type: "layout", Key: "menu", Label: "Salon", Config: map[string]any{
			"slot": "menu", "amendments": []any{
				map[string]any{"key": "/tables", "hide": true},
				map[string]any{"key": "/kitchen-stations", "hide": true},
			},
		}}},
	}
	if err := plugins.PersistManifest(t.Context(), dp.Db, m, plugins.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := dp.ReloadPlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// installRestructureLayout installs a `layout` plugin whose Menu-slot
// amendments never hide anything — only relabel/reicon/reorder/regroup — so
// tests can drive the amendedMenuRows path (ut-docs#1921) independently of
// installSalonLayout's hide-only amendments above.
func installRestructureLayout(t *testing.T, dp *common.Deps, pluginID, name string, amendments []any) {
	t.Helper()
	m := &plugins.Manifest{
		ID: pluginID, Name: name, Version: "1.0.0", Runtime: "none", CanonicalType: "layout",
		Entries: []plugins.ManifestEntry{{Type: "layout", Key: "menu", Label: name, Config: map[string]any{
			"slot": "menu", "amendments": amendments,
		}}},
	}
	if err := plugins.PersistManifest(t.Context(), dp.Db, m, plugins.InstallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := dp.ReloadPlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func getPage(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func postMenuLayoutForm(t *testing.T, mux *http.ServeMux, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestMenuLayoutSettings_ListsHiddenDestinationsNamingThePlugin(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	installSalonLayout(t, dp)

	rec := getPage(t, mux, "/settings/menu")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings/menu = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		// the core label (as html/template escapes it), not the key
		template.HTMLEscapeString(httpx.T("en", "tables.title")), template.HTMLEscapeString(httpx.T("en", "kitchenstations.title")),
		"Salon layout",                               // the plugin that hid it, by name
		`href="/tables"`, `href="/kitchen-stations"`, // still reachable: a direct link to each
		`name="key" value="/tables"`, `action="/api/settings/menu/restore"`, // per-entry restore
	} {
		if !strings.Contains(body, want) {
			t.Errorf("hidden-destinations page missing %q in: %s", want, body)
		}
	}
	if strings.Contains(body, "menulayout.none") || strings.Contains(body, httpx.T("en", "menulayout.none")) {
		t.Errorf("empty-state text must not show while tiles are hidden: %s", body)
	}
}

func TestMenuLayoutSettings_EmptyStateWhenNothingIsHidden(t *testing.T) {
	mux, _ := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	rec := getPage(t, mux, "/settings/menu")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), httpx.T("en", "menulayout.none")) {
		t.Fatalf("expected the empty state, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMenuLayoutSettings_RestoreBringsTheTileBackAndSurvivesReload(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	installSalonLayout(t, dp)
	if body := getMenu(t, mux); strings.Contains(body, `href="/tables"`) {
		t.Fatalf("precondition: /tables hidden, got: %s", body)
	}

	rec := postMenuLayoutForm(t, mux, "/api/settings/menu/restore", url.Values{"key": {"/tables"}})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/settings/menu") {
		t.Fatalf("restore = %d Location=%q: %s", rec.Code, rec.Header().Get("Location"), rec.Body.String())
	}
	body := getMenu(t, mux)
	if !strings.Contains(body, `href="/tables"`) {
		t.Fatalf("restored /tables must render again, got: %s", body)
	}
	if strings.Contains(body, `href="/kitchen-stations"`) {
		t.Fatalf("restore is per entry — /kitchen-stations must stay hidden, got: %s", body)
	}

	// Persisted, not in-memory: a plugin reload (sync-pull fires one every
	// 30s) must not silently re-hide what the merchant restored.
	if err := dp.ReloadPlugins(t.Context()); err != nil {
		t.Fatal(err)
	}
	if body := getMenu(t, mux); !strings.Contains(body, `href="/tables"`) {
		t.Fatalf("restore must survive ReloadPlugins, got: %s", body)
	}

	// The page now shows the entry as restored, with the way back.
	page := getPage(t, mux, "/settings/menu").Body.String()
	if !strings.Contains(page, httpx.T("en", "menulayout.state.restored")) || !strings.Contains(page, `action="/api/settings/menu/rehide"`) {
		t.Fatalf("expected restored state + re-hide action, got: %s", page)
	}
	rec = postMenuLayoutForm(t, mux, "/api/settings/menu/rehide", url.Values{"key": {"/tables"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("rehide = %d: %s", rec.Code, rec.Body.String())
	}
	if body := getMenu(t, mux); strings.Contains(body, `href="/tables"`) {
		t.Fatalf("re-hidden /tables must be gone again, got: %s", body)
	}
}

func TestMenuLayoutSettings_RestoreRejectsAKeyNothingHides(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	installSalonLayout(t, dp)
	for _, key := range []string{"/nope", "/items", ""} {
		rec := postMenuLayoutForm(t, mux, "/api/settings/menu/restore", url.Values{"key": {key}})
		if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "err=menulayout.error.unknown_key") {
			t.Fatalf("restore %q = %d Location=%q, want a redirect carrying the error key", key, rec.Code, rec.Header().Get("Location"))
		}
	}
	// The error renders as text on the page, through T.
	page := getPage(t, mux, "/settings/menu?err=menulayout.error.unknown_key").Body.String()
	if !strings.Contains(page, httpx.T("en", "menulayout.error.unknown_key")) || strings.Contains(page, ">menulayout.error.unknown_key<") {
		t.Fatalf("expected the translated error, got: %s", page)
	}
}

// Same gate as every other settings surface: canPerform(d, r, "settings").
func TestMenuLayoutSettings_RequiresManager(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	installSalonLayout(t, dp)
	// UT_AUTH unset, no session user on the request.
	if rec := getPage(t, mux, "/settings/menu"); rec.Code != http.StatusForbidden {
		t.Fatalf("GET /settings/menu without a manager = %d, want 403", rec.Code)
	}
	for _, path := range []string{"/api/settings/menu/restore", "/api/settings/menu/rehide"} {
		if rec := postMenuLayoutForm(t, mux, path, url.Values{"key": {"/tables"}}); rec.Code != http.StatusForbidden {
			t.Fatalf("POST %s without a manager = %d, want 403", path, rec.Code)
		}
	}
	if body := getMenu(t, mux); strings.Contains(body, `href="/tables"`) {
		t.Fatalf("a refused restore must change nothing, got: %s", body)
	}
}

// ut-docs#1921 (independent review of ut-docs#1904, finding F1): a layout
// plugin that re-labels/re-icons a tile without hiding it left NO trace on
// this page — only hides were listed. This pins the fix: a relabel+reicon
// amendment names the plugin and shows the CORE default each field
// replaced (Entry.LabelFallback/IconFallback, produced by uislot.Resolve).
func TestMenuLayoutSettings_ListsRelabelAndReiconAmendmentNamingCoreDefault(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	installRestructureLayout(t, dp, "com.example.cafe", "Cafe layout", []any{
		map[string]any{"key": "/reports", "label_key": "layout.cafe.sales_label", "icon": "coffee"},
	})

	body := getPage(t, mux, "/settings/menu").Body.String()
	for _, want := range []string{
		"Cafe layout", // the plugin that made the change, by name
		template.HTMLEscapeString(httpx.T("en", "nav.reports")), // the core default the relabel replaced
		"layout.cafe.sales_label",                               // the plugin's own key, unresolved (no locale entry — passes through T unchanged)
		httpx.T("en", "menulayout.change.reicon"),
		`href="/reports"`, // still reachable, same as a hidden row's link
	} {
		if !strings.Contains(body, want) {
			t.Errorf("amended-tiles section missing %q in: %s", want, body)
		}
	}
}

// A plugin that only reorders/regroups (no relabel/reicon) still needs
// findability: the acceptance criteria calls out reorder and regroup as
// their own change kinds, not just relabel/reicon.
func TestMenuLayoutSettings_ListsReorderAndRegroupAmendment(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	installRestructureLayout(t, dp, "com.example.cafe", "Cafe layout", []any{
		map[string]any{"key": "/users", "order": 150, "group": "layout.cafe.group"},
	})

	body := getPage(t, mux, "/settings/menu").Body.String()
	for _, want := range []string{
		"Cafe layout",
		httpx.T("en", "menulayout.change.reorder"),
		httpx.T("en", "menulayout.change.regroup"),
		httpx.T("en", "menulayout.change.ungrouped"), // /users has no core Group — the "from" side
		"layout.cafe.group",                          // the "to" side, unresolved plugin key
	} {
		if !strings.Contains(body, want) {
			t.Errorf("amended-tiles section missing %q in: %s", want, body)
		}
	}
}

// The empty-state text claims NOTHING changed — it must not show once a
// layout plugin has changed something, even if that something is not a
// hide (ut-docs#1921's acceptance criteria: distinguish "no layout plugin
// installed" from "installed and changed things, none of them hides").
func TestMenuLayoutSettings_NonHideAmendmentsSuppressEmptyState(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	installRestructureLayout(t, dp, "com.example.cafe", "Cafe layout", []any{
		map[string]any{"key": "/reports", "label_key": "layout.cafe.sales_label"},
	})

	body := getPage(t, mux, "/settings/menu").Body.String()
	if strings.Contains(body, httpx.T("en", "menulayout.none")) {
		t.Errorf("empty state must not show once a layout plugin has relabelled a tile, got: %s", body)
	}
	if !strings.Contains(body, httpx.T("en", "menulayout.amended.title")) {
		t.Errorf("expected the amended-tiles section heading, got: %s", body)
	}
}

// Two DIFFERENT plugins may amend the same key without a conflict at
// install time as long as at most one of them restructures it (that
// validation only checks restructure-vs-restructure) — so a hide by plugin
// A and a restructure by plugin B on the SAME key is a real, reachable
// state, not a hypothetical. uislot.Resolve makes the hide win regardless
// of amendment order, so the key must show as hidden ONLY — never also in
// the amended section, which would contradict hiddenMenuRows and tell the
// merchant (via this section's own "these stay on the Menu" copy) that a
// hidden tile is still visible. Independent review finding, ut-docs#1921.
func TestMenuLayoutSettings_KeyHiddenByOnePluginAndRestructuredByAnotherShowsHiddenOnly(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	installRestructureLayout(t, dp, "com.example.hider", "Hider plugin", []any{
		map[string]any{"key": "/tables", "hide": true},
	})
	installRestructureLayout(t, dp, "com.example.relabeler", "Relabeler plugin", []any{
		map[string]any{"key": "/tables", "label_key": "layout.relabeler.tables"},
	})

	body := getPage(t, mux, "/settings/menu").Body.String()
	if !strings.Contains(body, "Hider plugin") {
		t.Errorf("expected /tables listed as hidden by Hider plugin, got: %s", body)
	}
	if strings.Contains(body, "Relabeler plugin") {
		t.Errorf("a key another plugin hides must not also appear in the amended section (hide wins at render), got: %s", body)
	}
}

// Restore stays a hide-only action (acceptance criteria) — a relabel/
// reicon/reorder/regroup amendment must not offer the restore/rehide forms
// hiddenMenuRows' table offers; there is nothing here to "restore" to.
func TestMenuLayoutSettings_AmendedRowsOfferNoRestoreAction(t *testing.T) {
	mux, dp := newMenuLayoutSettingsDeps(t)
	t.Setenv("UT_AUTH", "off")
	installRestructureLayout(t, dp, "com.example.cafe", "Cafe layout", []any{
		map[string]any{"key": "/reports", "label_key": "layout.cafe.sales_label"},
	})

	body := getPage(t, mux, "/settings/menu").Body.String()
	if strings.Contains(body, `action="/api/settings/menu/restore"`) || strings.Contains(body, `action="/api/settings/menu/rehide"`) {
		t.Errorf("a non-hide amendment must not offer a restore/rehide action, got: %s", body)
	}
}
