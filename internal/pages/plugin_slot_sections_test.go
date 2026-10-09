package pages

import (
	"database/sql"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#3946: settings.sections and admin.pages are drawn as entries of
// their host page's own switcher, one per plugin entry -- never as panels
// under every core section (Settings) or destination (Admin).

// seedSlotEntry adds a /plugin/ view entry that fills slot, its plugin
// holding ui:slot:<slot>.
func seedSlotEntry(t *testing.T, db *sql.DB, pluginID, key, route, label, slot string) {
	t.Helper()
	if _, err := db.Exec(`INSERT OR IGNORE INTO plugin_catalog(id,version,name,description,runtime,entrypoint,package_url,sha256,author,website,tags_json,min_pos_version,api_version,published_at) VALUES(?,'1.0.0',?,'desc','go','entry','url','sha','auth','site','[]','0.0.0','1',datetime('now'))`, pluginID, pluginID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO plugins(id,name,version,entrypoint) VALUES(?,?,'1.0.0','entry')`, pluginID, pluginID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label,config_json) VALUES(?,?,'page',?,?,?,?)`,
		pluginID+"/"+key, pluginID, key, route, label, `{"view":"`+key+`.view","content_slot":"`+slot+`"}`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO plugin_permissions(id,plugin_id,permission,granted) VALUES(?,?,?,1)`, pluginID+slot, pluginID, "ui:slot:"+slot); err != nil {
		t.Fatal(err)
	}
}

// moveSlotHarnessTo re-points the slot harness's two reports.panels
// entries at slot.
func moveSlotHarnessTo(t *testing.T, h *slotHarness, slot string) {
	t.Helper()
	if _, err := h.d.Db.Exec(`UPDATE plugin_entries SET config_json = replace(config_json, 'reports.panels', ?) WHERE id IN ('s1','s2')`, slot); err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.Db.Exec(`UPDATE plugin_permissions SET permission = ? WHERE permission = 'ui:slot:reports.panels'`, "ui:slot:"+slot); err != nil {
		t.Fatal(err)
	}
}

// GET /ui/slot/{slot}/{plugin}/{entry} draws only that entry's panel, not
// a card (its section is the card), with the panel id its index in the
// whole slot gives it -- so two sections on one page never share an id.
func TestPluginSlot_EntryRouteDrawsOnePanel_3946(t *testing.T) {
	h := newSlotHarness(t)
	moveSlotHarnessTo(t, h, settingsSectionsSlot)
	h.answerWith(slotDoc("VIEWS"))
	h.otherWith(slotDoc("OTHER"), 0)

	rec := h.do(http.MethodGet, "/ui/slot/settings.sections/"+viewPluginID+"/panel?days=7", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "VIEWS title") || strings.Contains(body, "OTHER") {
		t.Fatalf("want only the views entry's panel:\n%s", body)
	}
	if strings.Count(body, "<section") != 1 || strings.Contains(body, `class="card`) {
		t.Fatalf("want one panel that is not a card of its own:\n%s", body)
	}
	// com.test.other sorts first, so the views entry is index 1.
	if !strings.Contains(body, `<div id="plugin-slot-settings-sections-1" class="plugin-view"`) ||
		!strings.Contains(body, `hx-target="#plugin-slot-settings-sections-1"`) {
		t.Fatalf("panel id/target is not the entry's slot-wide index:\n%s", body)
	}
	params, _ := h.lastPay["params"].(map[string]any)
	if params["slot"] != settingsSectionsSlot || params["days"] != "7" {
		t.Fatalf("params = %v", params)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("panel must not be cached")
	}

	for _, path := range []string{
		"/ui/slot/settings.sections/" + viewPluginID + "/nope",   // no such entry
		"/ui/slot/reports.panels/" + viewPluginID + "/panel",     // entry fills another slot
		"/ui/slot/setup.wizard.steps/" + viewPluginID + "/panel", // no route for the wizard
		"/ui/slot/no.such.slot/" + viewPluginID + "/panel",       // unknown slot
		"/ui/slot/settings.sections/com.test.unknown/panel",      // unknown plugin
	} {
		if rec := h.do(http.MethodGet, path, nil, true); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}

// A plugin that does not answer still gets its section drawn: its label
// and the unavailable notice, never a blank card.
func TestPluginSlot_EntryRouteSilentPluginShowsNotice_3946(t *testing.T) {
	h := newSlotHarness(t)
	moveSlotHarnessTo(t, h, settingsSectionsSlot)
	shortenSlotTimeout(t, 50*time.Millisecond)
	h.otherWith(slotDoc("LATE"), 500*time.Millisecond)

	rec := h.do(http.MethodGet, "/ui/slot/settings.sections/"+slotOtherID+"/panel", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<h2 class="plugin-view-title">Other panel</h2>`) || !strings.Contains(body, "plugin-view-notice warn") {
		t.Fatalf("want the entry label and the unavailable notice:\n%s", body)
	}
	if strings.Contains(body, "LATE") {
		t.Fatalf("a late answer must not be drawn:\n%s", body)
	}
}

// The entry route keeps the slot's host gate: a cashier (no settings
// permission) is refused before any plugin is asked.
func TestPluginSlot_EntryRouteNeedsHostGate_3946(t *testing.T) {
	h := newSlotHarness(t)
	moveSlotHarnessTo(t, h, settingsSectionsSlot)
	h.otherWith(slotDoc("OTHER"), 0)
	h.d.AuthSvc = auth.NewService(h.d.Db)
	t.Setenv("UT_AUTH", "on")
	path := "/ui/slot/settings.sections/" + slotOtherID + "/panel"
	hx := map[string]string{"HX-Request": "true"}
	if rec := h.slotRouteReq(auth.User{ID: "c1", Role: "cashier"}, http.MethodGet, path, nil, hx); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "OTHER") {
		t.Fatalf("cashier = %d, want 403 with no panel: %s", rec.Code, rec.Body.String())
	}
	h.omu.Lock()
	asked := h.otherAsked
	h.omu.Unlock()
	if asked {
		t.Error("the plugin was asked for a refused viewer")
	}
	if rec := h.slotRouteReq(auth.User{ID: "a1", Role: "admin"}, http.MethodGet, path, nil, hx); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "OTHER") {
		t.Fatalf("admin = %d, want the panel: %s", rec.Code, rec.Body.String())
	}
}

// Settings: each settings.sections entry is a card of its own in
// #settings-grid, listed in the sidebar index under Plugins, loading only
// its own panel -- and nothing loads the whole slot under every section.
func TestSettingsPage_PluginSectionIsItsOwnSection_3946(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, d := newFullAuthDeps(t)
	seedSlotEntry(t, d.Db, "com.test.loyalty", "points", "/plugin/loyalty/points", "Loyalty points", settingsSectionsSlot)
	seedSlotEntry(t, d.Db, "com.test.other", "x", "/plugin/other/x", "Other report", "reports.panels")

	body := renderSettingsAs(t, mux, mgrUser, false)
	if strings.Contains(body, `hx-get="/ui/slot/settings.sections"`) {
		t.Error("settings still loads the whole slot under every section")
	}
	grid := strings.Index(body, `<div class="settings-grid" id="settings-grid">`)
	gridEnd := closingDivEnd(body, grid)
	card := strings.Index(body, `<div class="card plugin-settings-section" id="plugin-section-com-test-loyalty-points">`)
	if grid < 0 || gridEnd < 0 || card < grid || card > gridEnd {
		t.Fatalf("plugin section card not inside #settings-grid (grid %d, card %d, end %d)", grid, card, gridEnd)
	}
	if !strings.Contains(body[card:gridEnd], `hx-get="/ui/slot/settings.sections/com.test.loyalty/points"`) {
		t.Errorf("the card does not load its own entry:\n%s", body[card:gridEnd])
	}
	if !strings.Contains(body, `<li data-key="plugin-section-com-test-loyalty-points" data-group="Plugins" data-cat="g-plugins">Loyalty points</li>`) {
		t.Error("the sidebar index has no Plugins row for the section")
	}
	if !strings.Contains(body, `href="#cat-g-plugins"`) {
		t.Error("the landing grid has no Plugins tile")
	}
	if strings.Contains(body, "Other report") {
		t.Error("an entry of another slot became a settings section")
	}
}

// Plugin "a-b" entry "c" and plugin "a" entry "b-c" fold to the same
// section id; the later one is suffixed so each keeps its own card and row.
func TestSettingsPage_PluginSectionIDsNeverCollide_3946(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, d := newFullAuthDeps(t)
	seedSlotEntry(t, d.Db, "a-b", "c", "/plugin/ab/c", "First", settingsSectionsSlot)
	seedSlotEntry(t, d.Db, "a", "b-c", "/plugin/a/bc", "Second", settingsSectionsSlot)

	body := renderSettingsAs(t, mux, mgrUser, false)
	for _, id := range []string{"plugin-section-a-b-c", "plugin-section-a-b-c-2"} {
		if n := strings.Count(body, `id="`+id+`"`); n != 1 {
			t.Errorf("section id %q drawn %d times, want 1", id, n)
		}
		if n := strings.Count(body, `data-key="`+id+`"`); n != 1 {
			t.Errorf("switcher row %q drawn %d times, want 1", id, n)
		}
	}
}

// Without the settings permission there are no plugin sections at all.
func TestSettingsPage_NoPluginSectionsWithoutGrant_3946(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _, d := newFullAuthDeps(t)
	seedSlotEntry(t, d.Db, "com.test.loyalty", "points", "/plugin/loyalty/points", "Loyalty points", settingsSectionsSlot)
	if _, err := d.Db.Exec(`UPDATE plugin_permissions SET granted = 0 WHERE plugin_id = 'com.test.loyalty'`); err != nil {
		t.Fatal(err)
	}
	body := renderSettingsAs(t, mux, mgrUser, false)
	if strings.Contains(body, "plugin-section-") || strings.Contains(body, "Loyalty points") {
		t.Error("a plugin without ui:slot:settings.sections got a section")
	}
}

// Admin: an admin.pages entry is a row of its own in a Plugins tree group,
// a plain link to its own page; no destination loads the slot below it.
func TestAdminPage_PluginEntryIsATreeRow_3946(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "off")
	seedSlotEntry(t, dp.Db, "com.test.loyalty", "admin", "/plugin/loyalty/admin", "Loyalty admin", adminPagesSlot)

	rec := getAdmin(t, mux, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin = %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "/ui/slot/admin.pages") {
		t.Error("/admin still loads the admin.pages slot under the destination")
	}
	idx := strings.Index(body, `href="/plugin/loyalty/admin"`)
	if idx < 0 {
		t.Fatalf("no tree row for the plugin entry:\n%s", body)
	}
	if h := strings.LastIndex(body[:idx], "<h2>"); h < 0 || !strings.HasPrefix(body[h:], "<h2>Plugins</h2>") {
		t.Errorf("the plugin row is not under a Plugins heading")
	}
	tagStart := strings.LastIndex(body[:idx], "<a ")
	row := body[tagStart : strings.Index(body[tagStart:], ">")+tagStart]
	if strings.Contains(row, "hx-get") {
		t.Errorf("the plugin row must be a plain link (its route answers with a whole page): %s", row)
	}
	if !strings.Contains(body[idx:], "Loyalty admin") {
		t.Error("the row does not carry the entry's label")
	}
}

// The row follows the slot's own gate: reports AND an /admin destination.
// A settings-only role reaches /admin but not admin.pages.
func TestAdminPage_PluginRowFollowsSlotGate_3946(t *testing.T) {
	mux, dp := newAdminPageTestDeps(t)
	t.Setenv("UT_AUTH", "on")
	seedSlotEntry(t, dp.Db, "com.test.loyalty", "admin", "/plugin/loyalty/admin", "Loyalty admin", adminPagesSlot)
	const settingsOnly = "c_01j9z3k4m5n6p7q8r9s0t1v2x9"
	if _, err := dp.Db.Exec(`INSERT INTO roles (role, label, origin) VALUES (?, ?, 'cloud')`, settingsOnly, settingsOnly); err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`INSERT INTO role_permissions (role, action, granted) VALUES (?, 'settings', 1)`, settingsOnly); err != nil {
		t.Fatal(err)
	}
	rec := getAdmin(t, mux, &auth.User{ID: "s1", Role: settingsOnly})
	if rec.Code != http.StatusOK {
		t.Fatalf("settings-only /admin = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "/plugin/loyalty/admin") {
		t.Error("a viewer without core reports sees the admin.pages row")
	}
	if rec := getAdmin(t, mux, &auth.User{ID: "a1", Role: "admin"}); !strings.Contains(rec.Body.String(), `href="/plugin/loyalty/admin"`) {
		t.Error("admin does not see the admin.pages row")
	}
}

// The host templates no longer load either slot as a whole.
func TestPluginSlot_SwitcherSlotsNotLoadedWhole_3946(t *testing.T) {
	chdirRoot(t)
	for slot, file := range map[string]string{
		settingsSectionsSlot: "web/ui/pages/settings.html",
		adminPagesSlot:       "web/ui/pages/admin.html",
	} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), `"/ui/slot/`+slot+`"`) {
			t.Errorf("%s loads /ui/slot/%s whole", file, slot)
		}
	}
}

// closingDivEnd is the offset just past the </div> that closes the <div
// starting at start, or -1.
func closingDivEnd(body string, start int) int {
	if start < 0 {
		return -1
	}
	depth := 0
	for i := start; i < len(body); {
		switch {
		case strings.HasPrefix(body[i:], "<div"):
			depth++
			i += 4
		case strings.HasPrefix(body[i:], "</div>"):
			depth--
			i += 6
			if depth == 0 {
				return i
			}
		default:
			i++
		}
	}
	return -1
}
