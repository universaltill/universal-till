package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Plugin content slots (ADR-0121 §7, ut-docs#3872): a core-owned region
// asks every active plugin whose page entry declares the slot and which
// holds ui:slot:<slot>, in parallel, 2 s each; a slow, broken, invalid or
// permission-less plugin is skipped and never blocks the region.

const slotOtherID = "com.test.other"

// slotHarness extends the view harness with one reports.panels entry per
// plugin (com.test.other sorts before com.test.views), both plugins
// holding ui:slot:reports.panels, and com.test.other answering on the bus
// through its own handler.
type slotHarness struct {
	*viewHarness

	omu         sync.Mutex
	otherAnswer func(ev plugins.Event) (json.RawMessage, error)
	otherAsked  bool
}

func newSlotHarness(t *testing.T) *slotHarness {
	t.Helper()
	t.Setenv("UT_AUTH", "off") // the route's role gate has its own test
	vh := newViewHarness(t)
	h := &slotHarness{viewHarness: vh}
	db := vh.d.Db
	if _, err := db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label,config_json) VALUES
		('s1',?,'page','panel','/plugin/views/panel','Views panel','{"view":"views.panel","content_slot":"reports.panels"}'),
		('s2',?,'page','panel','/plugin/other/panel','Other panel','{"view":"other.panel","content_slot":"reports.panels"}')`, viewPluginID, slotOtherID); err != nil {
		t.Fatal(err)
	}
	grants := [][2]string{
		{viewPluginID, "ui:slot:reports.panels"},
		{slotOtherID, "ui:slot:reports.panels"},
		{slotOtherID, "events:receive"},
	}
	for _, g := range grants {
		if _, err := db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES(?,?,?,1)`, g[0]+g[1], g[0], g[1]); err != nil {
			t.Fatal(err)
		}
	}
	for i, ev := range []string{pluginViewAskEvent, pluginActionAskEvent} {
		if _, err := db.Exec(`INSERT INTO plugin_hooks(id,plugin_id,event,action,is_active) VALUES(?,?,?,'view',1)`, slotOtherID+"-h"+string(rune('0'+i)), slotOtherID, ev); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := plugins.SharedBus(db).SubscribeWithHandler(t.Context(), slotOtherID, []string{pluginViewAskEvent, pluginActionAskEvent},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			h.omu.Lock()
			h.otherAsked = true
			answer := h.otherAnswer
			h.omu.Unlock()
			if answer == nil {
				return nil, nil
			}
			return answer(ev)
		}); err != nil {
		t.Fatalf("subscribe other: %v", err)
	}
	return h
}

func (h *slotHarness) otherWith(raw string, delay time.Duration) {
	h.omu.Lock()
	h.otherAnswer = func(plugins.Event) (json.RawMessage, error) {
		time.Sleep(delay)
		return json.RawMessage(raw), nil
	}
	h.omu.Unlock()
}

func slotDoc(text string) string {
	return `{"document":{"version":1,"title":{"literal":"` + text + ` title"},"components":[
		{"type":"text","text":{"literal":"` + text + `"}},
		{"type":"button","action":"refresh","label":{"literal":"Refresh"}}]}}`
}

func shortenSlotTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	orig := pluginSlotTimeout
	pluginSlotTimeout = d
	t.Cleanup(func() { pluginSlotTimeout = orig })
}

func TestPluginSlot_RendersPanelsInStableOrder_3872(t *testing.T) {
	h := newSlotHarness(t)
	// The later plugin (by id) answers first; the order must not follow
	// the answer timing.
	h.answerWith(slotDoc("VIEWS"))
	h.otherWith(slotDoc("OTHER"), 80*time.Millisecond)
	rec := h.do(http.MethodGet, "/ui/slot/reports.panels?days=7", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	o, v := strings.Index(body, "OTHER title"), strings.Index(body, "VIEWS title")
	if o < 0 || v < 0 || o > v {
		t.Fatalf("panels missing or out of (plugin id, entry key) order:\n%s", body)
	}
	if strings.Count(body, "<section") != 2 || strings.Contains(body, "<html") {
		t.Fatalf("want a fragment of two <section> panels:\n%s", body)
	}
	// The ask payload: the entry's view, params + slot, locale.
	if h.lastEv.Type != pluginViewAskEvent || h.lastPay["view"] != "views.panel" || h.lastPay["locale"] != "en" {
		t.Fatalf("ask = %s %v", h.lastEv.Type, h.lastPay)
	}
	params, _ := h.lastPay["params"].(map[string]any)
	if params["slot"] != "reports.panels" || params["days"] != "7" {
		t.Fatalf("params = %v, want slot=reports.panels days=7", params)
	}
	// Each panel's actions post to its own entry route and target its own
	// container; nothing targets a core URL.
	ids := regexp.MustCompile(`<div id="(plugin-slot-[a-z0-9-]+)"`).FindAllStringSubmatch(body, -1)
	if len(ids) != 2 || ids[0][1] == ids[1][1] {
		t.Fatalf("panel containers = %v, want two distinct ids", ids)
	}
	for _, m := range regexp.MustCompile(`(?:hx-post|hx-get| action)="([^"]*)"`).FindAllStringSubmatch(body, -1) {
		if m[1] != "/plugin/other/panel" && m[1] != "/plugin/views/panel" {
			t.Errorf("%s targets %q, want a plugin entry route", m[0], m[1])
		}
	}
	for _, id := range ids {
		if !strings.Contains(body, `hx-target="#`+id[1]+`"`) {
			t.Errorf("no action targets panel %s", id[1])
		}
	}
	if strings.Contains(body, `hx-target="#plugin-view"`) {
		t.Error("a slot panel action targets the page view container")
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("slot fragment must not be cached")
	}
}

func TestPluginSlot_SlowPluginSkipped_3872(t *testing.T) {
	h := newSlotHarness(t)
	shortenSlotTimeout(t, 100*time.Millisecond)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h.mu.Lock()
	// Ignores its context on purpose: the region must not wait for it.
	h.answer = func(plugins.Event) (json.RawMessage, error) {
		<-release
		return json.RawMessage(slotDoc("VIEWS")), nil
	}
	h.mu.Unlock()
	h.otherWith(slotDoc("OTHER"), 0)
	start := time.Now()
	rec := h.do(http.MethodGet, "/ui/slot/reports.panels", nil, true)
	if time.Since(start) > 2*time.Second {
		t.Fatal("the slot waited for a slow plugin")
	}
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "OTHER") || strings.Contains(body, "VIEWS") {
		t.Fatalf("want only the fast plugin's panel, got %d:\n%s", rec.Code, body)
	}
	if strings.Contains(body, "plugin-view-notice warn") {
		t.Fatal("a skipped plugin must not show a notice")
	}
}

func TestPluginSlot_InvalidAndBrokenSkipped_3872(t *testing.T) {
	h := newSlotHarness(t)
	h.otherWith(slotDoc("OTHER"), 0)
	for name, set := range map[string]func(){
		"invalid document": func() { h.answerWith(`{"document":{"version":1,"components":[{"type":"iframe"}]}}`) },
		"core key": func() {
			h.answerWith(`{"document":{"version":1,"components":[{"type":"text","text":{"key":"nav.home"}}]}}`)
		},
		"no answer": func() {
			h.mu.Lock()
			h.answer = nil
			h.mu.Unlock()
		},
		"panics": func() {
			h.mu.Lock()
			h.answer = func(plugins.Event) (json.RawMessage, error) { panic("boom") }
			h.mu.Unlock()
		},
		"redirect answer": func() { h.answerWith(`{"redirect":"/plugin/views"}`) },
	} {
		t.Run(name, func(t *testing.T) {
			set()
			rec := h.do(http.MethodGet, "/ui/slot/reports.panels", nil, true)
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, "OTHER") || strings.Count(body, "<section") != 1 {
				t.Fatalf("want only the valid panel, got %d:\n%s", rec.Code, body)
			}
		})
	}
}

func TestPluginSlot_NeedsSlotPermission_3872(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(slotDoc("VIEWS"))
	h.otherWith(slotDoc("OTHER"), 0)
	// ui:page is not enough: the slot needs ui:slot:<slot>.
	if _, err := h.d.Db.Exec(`UPDATE plugin_permissions SET granted = 0 WHERE plugin_id = ? AND permission = 'ui:slot:reports.panels'`, viewPluginID); err != nil {
		t.Fatal(err)
	}
	rec := h.do(http.MethodGet, "/ui/slot/reports.panels", nil, true)
	body := rec.Body.String()
	if !strings.Contains(body, "OTHER") || strings.Contains(body, "VIEWS") {
		t.Fatalf("want only the permitted plugin's panel:\n%s", body)
	}
	if h.lastEv.Type != "" {
		t.Fatal("a plugin without ui:slot:reports.panels was asked")
	}
	// Holding another slot's permission does not help either.
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('x',?,'ui:slot:eod.footer',1)`, viewPluginID); err != nil {
		t.Fatal(err)
	}
	h.do(http.MethodGet, "/ui/slot/reports.panels", nil, true)
	if h.lastEv.Type != "" {
		t.Fatal("ui:slot:eod.footer let a plugin into reports.panels")
	}
}

func TestPluginSlot_NoPanelsIsEmpty_3872(t *testing.T) {
	h := newSlotHarness(t)
	// eod.footer has no entries: empty 200, nothing asked.
	rec := h.do(http.MethodGet, "/ui/slot/eod.footer", nil, true)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "" {
		t.Fatalf("empty slot = %d %q, want 200 and no body", rec.Code, rec.Body.String())
	}
	if h.lastEv.Type != "" || h.otherAsked {
		t.Fatal("a plugin was asked for a slot it does not fill")
	}
}

func TestPluginSlot_UnknownAndSetupSlot404_3872(t *testing.T) {
	h := newSlotHarness(t)
	for _, slot := range []string{"nope", "setup.wizard.steps", "sale.screen", "reports.panels.x"} {
		if rec := h.do(http.MethodGet, "/ui/slot/"+slot, nil, true); rec.Code != http.StatusNotFound {
			t.Errorf("/ui/slot/%s = %d, want 404", slot, rec.Code)
		}
	}
	if rec := h.do(http.MethodPost, "/ui/slot/reports.panels", url.Values{}, true); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /ui/slot/reports.panels = %d, want 405", rec.Code)
	}
}

// Every slot plugins may declare (plugins.ContentSlots) is drawn: five by
// the lazy route, setup.wizard.steps inline by the wizard.
func TestPluginSlot_EverySlotHasAHost_3872(t *testing.T) {
	var got []string
	for s := range pluginSlotGates {
		got = append(got, s)
	}
	got = append(got, setupWizardSlot)
	slices.Sort(got)
	want := plugins.ContentSlots()
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("slot hosts %v != plugins.ContentSlots %v", got, want)
	}
}

// An action posted from a slot panel answers into that panel (htmx sends
// its id as HX-Target): the document's title stays in the body and no
// out-of-band page heading is sent. A page post is unchanged.
func TestPluginSlot_ActionAnswersIntoPanel_3872(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(slotDoc("SAVED"))
	post := func(target string) string {
		req := httptest.NewRequest(http.MethodPost, "/plugin/views/panel", strings.NewReader(url.Values{"_action": {"refresh"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		if target != "" {
			req.Header.Set("HX-Target", target)
		}
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST (target %q) = %d: %s", target, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	body := post("plugin-slot-reports-panels-1")
	if !strings.Contains(body, `hx-target="#plugin-slot-reports-panels-1"`) || strings.Contains(body, `#plugin-view"`) {
		t.Fatalf("slot action must keep targeting its panel:\n%s", body)
	}
	if !strings.Contains(body, `<h2 class="plugin-view-title">SAVED title</h2>`) || strings.Contains(body, "hx-swap-oob") {
		t.Fatalf("slot action keeps its title in the panel, no OOB heading:\n%s", body)
	}
	// A forged target is ignored: the page's own container.
	for _, forged := range []string{"", "pos-alert", `plugin-slot-x" onclick="y`, "plugin-view"} {
		body = post(forged)
		if !strings.Contains(body, `hx-target="#plugin-view"`) || strings.Contains(body, "onclick") {
			t.Fatalf("target %q: want the page container:\n%s", forged, body)
		}
	}
}

// The route is gated per slot by the HOST page's permission (fail closed,
// 403 with no panel content); admin.pages needs the /admin page's own
// gate AND core reports (ADR-0149 §6).
func TestPluginSlot_RoleGates_3872(t *testing.T) {
	dp := newDesignerTestDeps(t)
	dp.Menu = baseMenu
	t.Setenv("UT_AUTH", "on")
	mux := http.NewServeMux()
	registerPluginPages(mux, dp)
	reportsOnly, settingsOnly := "c_01j9z3k4m5n6p7q8r9s0t1v2w4", "c_01j9z3k4m5n6p7q8r9s0t1v2w5"
	for role, action := range map[string]string{reportsOnly: "reports", settingsOnly: "settings"} {
		if _, err := dp.Db.Exec(`INSERT INTO roles (role, label, origin) VALUES (?, ?, 'cloud')`, role, role); err != nil {
			t.Fatal(err)
		}
		if _, err := dp.Db.Exec(`INSERT INTO role_permissions (role, action, granted) VALUES (?, ?, 1)`, role, action); err != nil {
			t.Fatal(err)
		}
	}
	get := func(slot string, u *auth.User) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/ui/slot/"+slot, nil)
		req.Header.Set("HX-Request", "true")
		if u != nil {
			req = auth.WithUser(req, *u)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	cashier := &auth.User{ID: "c1", Role: "cashier"}
	admin := &auth.User{ID: "a1", Role: "admin"}
	for slot := range pluginSlotGates {
		for name, u := range map[string]*auth.User{"cashier": cashier, "no session": nil} {
			if rec := get(slot, u); rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "<section") {
				t.Errorf("%s on %s = %d, want 403 and no panels", name, slot, rec.Code)
			}
		}
		if rec := get(slot, admin); rec.Code != http.StatusOK {
			t.Errorf("admin on %s = %d, want 200", slot, rec.Code)
		}
	}
	// admin.pages: reports alone is not enough (no /admin destination),
	// and an /admin destination (settings) without reports is not either.
	for _, role := range []string{reportsOnly, settingsOnly} {
		if rec := get("admin.pages", &auth.User{ID: "u-" + role, Role: role}); rec.Code != http.StatusForbidden {
			t.Errorf("%s on admin.pages = %d, want 403", role, rec.Code)
		}
	}
	if rec := get("reports.panels", &auth.User{ID: "u-r", Role: reportsOnly}); rec.Code != http.StatusOK {
		t.Errorf("reports-only role on reports.panels = %d, want 200", rec.Code)
	}
	if rec := get("settings.sections", &auth.User{ID: "u-s", Role: settingsOnly}); rec.Code != http.StatusOK {
		t.Errorf("settings-only role on settings.sections = %d, want 200", rec.Code)
	}
	// Unknown / setup slots stay 404 for everyone.
	if rec := get("setup.wizard.steps", admin); rec.Code != http.StatusNotFound {
		t.Errorf("setup.wizard.steps on the route = %d, want 404", rec.Code)
	}
}

// The setup wizard draws setup.wizard.steps inline (it is auth-exempt and
// /plugin/ routes are not, so the panels are read-only: no forms).
func TestPluginSlot_SetupPanelsReadOnly_3872(t *testing.T) {
	h := newSlotHarness(t)
	if _, err := h.d.Db.Exec(`UPDATE plugin_entries SET config_json = '{"view":"views.panel","content_slot":"setup.wizard.steps"}' WHERE id = 's1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('sw',?,'ui:slot:setup.wizard.steps',1)`, viewPluginID); err != nil {
		t.Fatal(err)
	}
	h.answerWith(slotDoc("SETUP"))
	html := renderSetupSlot(httptest.NewRequest(http.MethodGet, "/setup", nil), h.d, "en")
	s := string(html)
	if !strings.Contains(s, "SETUP title") || !strings.Contains(s, "<section") {
		t.Fatalf("setup slot panel missing:\n%s", s)
	}
	if strings.Contains(s, "<form") || strings.Contains(s, "hx-post") {
		t.Fatalf("setup slot panels must be read-only:\n%s", s)
	}
	if p, _ := h.lastPay["params"].(map[string]any); p["slot"] != setupWizardSlot {
		t.Fatalf("setup ask params = %v", h.lastPay["params"])
	}
	// No plugin fills it: nothing rendered.
	if _, err := h.d.Db.Exec(`DELETE FROM plugin_entries WHERE id = 's1'`); err != nil {
		t.Fatal(err)
	}
	if got := renderSetupSlot(httptest.NewRequest(http.MethodGet, "/setup", nil), h.d, "en"); strings.TrimSpace(string(got)) != "" {
		t.Fatalf("empty setup slot rendered %q", got)
	}
}

// At most pluginSlotMaxEntries entries are asked per slot.
func TestPluginSlot_EntryCap_3872(t *testing.T) {
	h := newSlotHarness(t)
	for i := 0; i < pluginSlotMaxEntries+3; i++ {
		k := string(rune('a' + i))
		if _, err := h.d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label,config_json) VALUES(?,?,'page',?,?, 'L','{"view":"views.panel","content_slot":"reports.panels"}')`,
			"cap"+k, viewPluginID, "k"+k, "/plugin/views/cap/"+k); err != nil {
			t.Fatal(err)
		}
	}
	var mu sync.Mutex
	asked := 0
	h.mu.Lock()
	h.answer = func(plugins.Event) (json.RawMessage, error) {
		mu.Lock()
		asked++
		mu.Unlock()
		return json.RawMessage(slotDoc("V")), nil
	}
	h.mu.Unlock()
	h.otherWith(slotDoc("OTHER"), 0)
	panels := contentSlotPanels(context.Background(), h.d, "reports.panels", "en", nil)
	if len(panels) != pluginSlotMaxEntries {
		t.Fatalf("panels = %d, want the cap %d", len(panels), pluginSlotMaxEntries)
	}
	mu.Lock()
	defer mu.Unlock()
	if asked != pluginSlotMaxEntries-1 { // the other plugin's entry sorts first
		t.Fatalf("views plugin asked %d times, want %d", asked, pluginSlotMaxEntries-1)
	}
}

// Each lazily drawn slot has its placeholder (or loader) in its host
// template, and the wizard draws setup.wizard.steps.
func TestPluginSlot_HostTemplatesLoadTheirSlot_3872(t *testing.T) {
	chdirRoot(t)
	hosts := map[string]string{
		"item.edit.actions": "web/ui/pages/catalog.html",
		"reports.panels":    "web/ui/pages/reports.html",
		"eod.footer":        "web/ui/partials/reports_tab_eod.html",
		"settings.sections": "web/ui/pages/settings.html",
		"admin.pages":       "web/ui/pages/admin.html",
	}
	if len(hosts) != len(pluginSlotGates) {
		t.Fatalf("hosts %v do not cover every gated slot", hosts)
	}
	for slot, file := range hosts {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), "/ui/slot/"+slot) {
			t.Errorf("%s does not load /ui/slot/%s", file, slot)
		}
	}
	raw, err := os.ReadFile("web/ui/pages/setup.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "{{ .pluginSlot }}") {
		t.Error("setup.html does not draw the setup.wizard.steps panels")
	}
}

// A failed action in a slot panel keeps the panel's heading, so the
// operator still sees whose panel it is; a page keeps its heading outside
// #plugin-view, so its failed body carries none (ut-docs#3872 review).
func TestPluginSlot_FailedActionKeepsPanelHeading_3872(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(`{"not":"a document"}`)
	post := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/plugin/views/panel", strings.NewReader(url.Values{"_action": {"refresh"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		if target != "" {
			req.Header.Set("HX-Target", target)
		}
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, req)
		return rec
	}
	rec := post("plugin-slot-reports-panels-1")
	body := rec.Body.String()
	if rec.Code != http.StatusBadGateway || !strings.Contains(body, `<h2 class="plugin-view-title">Views panel</h2>`) || !strings.Contains(body, "plugin-view-notice") {
		t.Fatalf("failed slot action = %d, want 502 with the panel heading and the notice:\n%s", rec.Code, body)
	}
	if body = post("").Body.String(); strings.Contains(body, "plugin-view-title") {
		t.Fatalf("failed page action must not grow a heading inside #plugin-view:\n%s", body)
	}
}
