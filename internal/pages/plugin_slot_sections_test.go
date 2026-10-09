package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/settings"
)

// Settings and /admin give each plugin entry of their slot its own place
// in the screen's switcher, instead of drawing every panel under whichever
// core section is open (ut-docs#3946).

// moveSlotEntries puts the harness's two entries (com.test.other "panel",
// com.test.views "panel") in slot and grants both plugins ui:slot:<slot>.
func (h *slotHarness) moveSlotEntries(slot string) {
	h.t.Helper()
	if _, err := h.d.Db.Exec(`UPDATE plugin_entries SET config_json = REPLACE(config_json, '"reports.panels"', ?) WHERE id IN ('s1','s2')`, `"`+slot+`"`); err != nil {
		h.t.Fatal(err)
	}
	for _, id := range []string{viewPluginID, slotOtherID} {
		if _, err := h.d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES(?,?,?,1)`, id+slot, id, "ui:slot:"+slot); err != nil {
			h.t.Fatal(err)
		}
	}
}

// pageDeps fills in what the Settings and /admin handlers read.
func (h *slotHarness) pageDeps() {
	cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
	h.d.Cfg = cfg
	h.d.Settings = settings.NewStore(h.d.Db)
	h.d.State = common.LoadState(h.t.Context(), h.d.Settings, cfg)
	h.d.Menu = []common.MenuItem{}
}

// One entry's panel: only that plugin is asked, its container id is the
// entry's own (distinct per entry), and its actions target it.
func TestPluginSlot_EntryRoute_3946(t *testing.T) {
	h := newSlotHarness(t)
	h.moveSlotEntries("settings.sections")
	h.answerWith(slotDoc("VIEWS"))
	h.otherWith(slotDoc("OTHER"), 0)

	rec := h.do(http.MethodGet, "/ui/slot/settings.sections/com.test.views/panel", nil, true)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "VIEWS title") || strings.Contains(body, "OTHER") {
		t.Fatalf("GET views entry = %d, want only its panel:\n%s", rec.Code, body)
	}
	h.omu.Lock()
	otherAsked := h.otherAsked
	h.omu.Unlock()
	if otherAsked {
		t.Error("loading one entry asked another plugin")
	}
	id := pluginSlotEntryPanelID("settings.sections", data.PageEntryRow{PluginID: viewPluginID, EntryKey: "panel"})
	if !pluginSlotPanelIDRe.MatchString(id) {
		t.Fatalf("panel id %q does not match pluginSlotPanelIDRe", id)
	}
	if !strings.Contains(body, `<div id="`+id+`" class="plugin-view">`) ||
		!strings.Contains(body, `hx-target="#`+id+`"`) ||
		!strings.Contains(body, `<div id="`+id+`-alert" class="plugin-view-alert" aria-live="polite"></div>`) {
		t.Fatalf("entry panel must own its container, actions and alert slot:\n%s", body)
	}
	if strings.Contains(body, "<section") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("want a bare, uncached panel fragment (the page owns the card):\n%s", body)
	}
	if p, _ := h.lastPay["params"].(map[string]any); p["slot"] != "settings.sections" {
		t.Fatalf("ask params = %v, want slot=settings.sections", h.lastPay["params"])
	}

	// A plugin that fails keeps its heading and says so.
	h.answerWith(`{"not":"a document"}`)
	body = h.do(http.MethodGet, "/ui/slot/settings.sections/com.test.views/panel", nil, true).Body.String()
	if !strings.Contains(body, `<h2 class="plugin-view-title">Views panel</h2>`) || !strings.Contains(body, "plugin-view-notice warn") {
		t.Fatalf("failed entry must show its label and the unavailable notice:\n%s", body)
	}

	// Not an entry of this slot, or an unknown slot: 404, nobody asked.
	h.lastEv.Type = ""
	for _, path := range []string{
		"/ui/slot/settings.sections/com.test.views/nope",
		"/ui/slot/settings.sections/com.test.nope/panel",
		"/ui/slot/reports.panels/com.test.views/panel",
		"/ui/slot/setup.wizard.steps/com.test.views/panel",
	} {
		if rec := h.do(http.MethodGet, path, nil, true); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
	if h.lastEv.Type != "" {
		t.Error("a 404 entry asked a plugin")
	}
}

// The entry route has the slot's own host gate.
func TestPluginSlot_EntryRouteGate_3946(t *testing.T) {
	h := newSlotHarness(t)
	h.moveSlotEntries("settings.sections")
	h.answerWith(slotDoc("VIEWS"))
	h.d.AuthSvc = auth.NewService(h.d.Db)
	t.Setenv("UT_AUTH", "on")
	const path = "/ui/slot/settings.sections/com.test.views/panel"
	hx := map[string]string{"HX-Request": "true"}
	if rec := h.slotRouteReq(auth.User{ID: "c1", Role: "cashier"}, http.MethodGet, path, nil, hx); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "VIEWS") {
		t.Fatalf("cashier = %d, want 403 and no panel", rec.Code)
	}
	if h.lastEv.Type != "" {
		t.Fatal("a refused viewer asked the plugin")
	}
	if rec := h.slotRouteReq(auth.User{ID: "a1", Role: "admin"}, http.MethodGet, path, nil, hx); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "VIEWS") {
		t.Fatalf("admin = %d, want 200 and the panel", rec.Code)
	}
	// A viewer the gate refuses gets no sections listed either.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), auth.User{ID: "c1", Role: "cashier"})
	if secs := pluginSlotSections(h.d, req, "settings.sections", "settings-plugin-"); len(secs) != 0 {
		t.Fatalf("cashier sections = %v, want none", secs)
	}
}

// Settings: each entry is a card of its own inside #settings-grid, in the
// switcher's Plugins group (and its landing tile), loading only its own
// panel; nothing is drawn outside the grid under every section.
func TestSettingsPage_PluginEntriesAreOwnSections_3946(t *testing.T) {
	h := newSlotHarness(t)
	h.moveSlotEntries("settings.sections")
	h.pageDeps()
	registerSettings(h.mux, h.d)

	rec := h.do(http.MethodGet, "/settings", nil, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	// The switcher script runs right after the grid's closing tag
	// (html/template drops the comments that would mark it).
	gridStart := strings.Index(body, `id="settings-grid"`)
	gridEnd := strings.Index(body, `var shell = document.getElementById('settings-shell');`)
	for _, want := range []struct{ plugin, entry, label string }{{slotOtherID, "com.test.other/panel", "Other panel"}, {viewPluginID, "com.test.views/panel", "Views panel"}} {
		key := "settings-plugin-" + pluginSlotEntryID(data.PageEntryRow{PluginID: want.plugin, EntryKey: "panel"})
		card := strings.Index(body, `<div class="card" id="`+key+`">`)
		if card < gridStart || gridStart < 0 || card > gridEnd {
			t.Fatalf("no %s card inside #settings-grid:\n%s", key, body)
		}
		if !strings.Contains(body[card:], `hx-get="/ui/slot/settings.sections/`+want.entry+`" hx-trigger="intersect once"`) {
			t.Errorf("%s does not lazily load only its own entry", key)
		}
		if !strings.Contains(body, `<li data-key="`+key+`" data-group="Plugins" data-cat="plugins">`+want.label+`</li>`) {
			t.Errorf("%s has no switcher row in the Plugins group", key)
		}
	}
	if !strings.Contains(body, `href="#cat-plugins"`) {
		t.Error("no Plugins tile on the landing grid")
	}
	if strings.Contains(body, `hx-get="/ui/slot/settings.sections"`) {
		t.Error("the whole slot is still drawn under every section")
	}
	// Plugins comes after every core category and before Advanced.
	if p, a := strings.Index(body, `href="#cat-plugins"`), strings.Index(body, `href="#cat-advanced"`); p < 0 || a < 0 || p > a {
		t.Errorf("Plugins tile at %d, Advanced at %d: want Plugins just before Advanced", p, a)
	}

	// No entries: no Plugins group at all.
	if _, err := h.d.Db.Exec(`DELETE FROM plugin_entries WHERE id IN ('s1','s2')`); err != nil {
		t.Fatal(err)
	}
	body = h.do(http.MethodGet, "/settings", nil, false).Body.String()
	if strings.Contains(body, "settings-plugin-") || strings.Contains(body, "#cat-plugins") {
		t.Error("a till with no plugin sections shows a Plugins group")
	}
}

// /admin: each admin.pages entry is a plain row of the tree's Plugins
// group, linking its own page; the destinations' OOB tree keeps it.
func TestAdminPage_PluginPagesAreTreeRows_3946(t *testing.T) {
	h := newSlotHarness(t)
	h.moveSlotEntries("admin.pages")
	h.pageDeps()
	registerAdmin(h.mux, h.d)

	rec := h.do(http.MethodGet, "/admin", nil, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /admin = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "/ui/slot/admin.pages") {
		t.Error("admin.pages panels are still drawn under every destination")
	}
	heading := strings.Index(body, "<h2>Plugins</h2>")
	if heading < 0 {
		t.Fatalf("no Plugins group in the tree:\n%s", body)
	}
	for _, href := range []string{"/plugin/other/panel", "/plugin/views/panel"} {
		i := strings.Index(body, `href="`+href+`"`)
		if i < heading {
			t.Fatalf("no %s row under Plugins", href)
		}
		row := body[i:]
		row = row[:strings.Index(row, "</a>")]
		if strings.Contains(row, "hx-get") {
			t.Errorf("%s row must be a plain link (no fragment handler): %s", href, row)
		}
	}
	groups := adminTreeGroups(h.d, httptest.NewRequest(http.MethodGet, "/locations", nil))
	if last := groups[len(groups)-1]; last.HeadingKey != "nav.plugins" || len(last.Entries) != 2 {
		t.Fatalf("OOB tree groups end with %+v, want the Plugins group", last)
	}
}

// An entry keeps its section key and panel id when another entry of the
// slot goes away, so a loaded panel's actions never answer into another
// plugin's card (review of ut-docs#3946).
func TestPluginSlot_EntryIDsStable_3946(t *testing.T) {
	h := newSlotHarness(t)
	h.moveSlotEntries("settings.sections")
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	before := pluginSlotSections(h.d, req, "settings.sections", "settings-plugin-")
	if len(before) != 2 || before[0].Key == before[1].Key {
		t.Fatalf("sections = %+v, want two distinct keys", before)
	}
	if _, err := h.d.Db.Exec(`DELETE FROM plugin_entries WHERE id = 's2'`); err != nil { // com.test.other, sorted first
		t.Fatal(err)
	}
	after := pluginSlotSections(h.d, req, "settings.sections", "settings-plugin-")
	if len(after) != 1 || after[0].Key != before[1].Key {
		t.Fatalf("views section key moved: before %+v, after %+v", before, after)
	}
	h.answerWith(slotDoc("VIEWS"))
	id := pluginSlotEntryPanelID("settings.sections", data.PageEntryRow{PluginID: viewPluginID, EntryKey: "panel"})
	if body := h.do(http.MethodGet, "/ui/slot/settings.sections/com.test.views/panel", nil, true).Body.String(); !strings.Contains(body, `id="`+id+`"`) {
		t.Fatalf("panel id changed after another entry went away:\n%s", body)
	}
}

// Every /admin destination refreshes the tree out of band with
// adminTreeGroups, so its Plugins group never drops out after a row click
// (review of ut-docs#3946).
func TestAdminPage_EveryOOBTreeKeepsPluginGroup_3946(t *testing.T) {
	chdirRoot(t)
	files, err := filepath.Glob("internal/pages/*.go")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`writeAdminTreeOOB\([^\n]*\)`)
	n := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || strings.HasSuffix(f, "admin_page.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range call.FindAllString(string(raw), -1) {
			n++
			if !strings.HasSuffix(m, "adminTreeGroups(d, r))") {
				t.Errorf("%s: %s does not pass adminTreeGroups(d, r)", f, m)
			}
		}
	}
	if n != len(adminFragmentCapableHrefs) {
		t.Errorf("found %d writeAdminTreeOOB calls, want one per destination (%d)", n, len(adminFragmentCapableHrefs))
	}
}
