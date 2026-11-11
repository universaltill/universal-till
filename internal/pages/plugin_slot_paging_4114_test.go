package pages

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ut-docs#4114: table paging inside a content slot panel. The paging links
// of a panel ask the entry's route as an htmx GET whose HX-Target is the
// panel, under ui:slot:<slot> and the slot's 2 s budget.

const panel0 = "plugin-slot-reports-panels-0"

func pagedDoc(page, pages int, param string) string {
	p := ""
	if param != "" {
		p = `,"param":"` + param + `"`
	}
	return fmt.Sprintf(`{"document":{"version":1,"title":{"literal":"PAGED title"},"components":[
		{"type":"table","columns":[{"label":{"literal":"Item"},"kind":"text"}],"rows":[["Tea"]],"paging":{"page":%d,"pages":%d%s}}]}}`, page, pages, p)
}

// panelGet is the request htmx sends for a paging link in a panel.
func (h *slotHarness) panelGet(path, target string) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.doAs(http.MethodGet, path, nil, nil, map[string]string{"HX-Request": "true", "HX-Target": target})
}

func (h *slotHarness) setOtherAsked(v bool) {
	h.omu.Lock()
	h.otherAsked = v
	h.omu.Unlock()
}

func (h *slotHarness) askedOther() bool {
	h.omu.Lock()
	defer h.omu.Unlock()
	return h.otherAsked
}

func TestPluginSlotPanelOf_4114(t *testing.T) {
	yes := []string{"plugin-slot-reports-panels-0", "plugin-slot-reports-panels-7", "plugin-slot-reports-panels-0123456789abcdef"}
	for _, id := range yes {
		if !pluginSlotPanelOf("reports.panels", id) {
			t.Errorf("%q should be a reports.panels panel", id)
		}
	}
	no := []struct{ slot, target string }{
		{"reports.panels", "plugin-slot-reports-panels-01"},
		{"reports.panels", "plugin-slot-reports-panels-"},
		{"reports.panels", "plugin-slot-reports-panels-0-alert"},
		{"reports.panels", "plugin-slot-reports-panels-0-pos"},
		{"reports.panels", "plugin-slot-eod-footer-0"},
		{"eod", "plugin-slot-eod-footer-0"},
		{"", "plugin-slot-reports-panels-0"},
		{"reports.panels", "plugin-slot-reports-panels-0123456789ABCDEF"},
		{"reports.panels", "plugin-slot-reports-panels-0123456789abcdef0"},
		{"reports.panels", "plugin-slot-x<script>"},
		{"reports.panels", "plugin-view"},
		{"reports.panels", "plugin-overlay-view"},
	}
	for _, c := range no {
		if pluginSlotPanelOf(c.slot, c.target) {
			t.Errorf("pluginSlotPanelOf(%q, %q) = true", c.slot, c.target)
		}
	}
	if pluginSlotMaxEntries > 10 {
		t.Errorf("pluginSlotMaxEntries = %d: the panel index is validated as one digit", pluginSlotMaxEntries)
	}
	for slot := range pluginSlotGates {
		for i := 0; i < pluginSlotMaxEntries; i++ {
			if id := pluginSlotPanelID(slot, i); !pluginSlotPanelOf(slot, id) {
				t.Errorf("generated id %q is not a panel of %q", id, slot)
			}
		}
	}
}

// AC1: the whole-slot draw links a paged table into its panel.
func TestPluginSlot_PanelPagingLinks_4114(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(pagedDoc(1, 3, ""))
	rec := h.do(http.MethodGet, "/ui/slot/reports.panels?days=7", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	const u = "/plugin/views/panel?days=7&amp;page=2&amp;slot=reports.panels"
	for _, want := range []string{
		`href="` + u + `" rel="next"`,
		`hx-get="` + u + `"`,
		`hx-target="#` + panel0 + `"`,
		`hx-swap="innerHTML"`,
		`hx-push-url="false"`,
		`hx-sync="#` + panel0 + `:drop"`,
		`hx-indicator="#` + panel0 + `"`,
		`aria-describedby="` + panel0 + `-pos"`,
		`id="` + panel0 + `-pos"`,
		"Page 1 of 3",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, `rel="prev"`) {
		t.Error("page 1 has a Previous link")
	}
}

// AC2: a per-entry panel (Settings section, item dialog) links with its own
// panel id and keeps the host's params.
func TestPluginSlot_EntryPanelPagingLinks_4114(t *testing.T) {
	h := newSlotHarness(t)
	for slot, host := range map[string]string{"settings.sections": "settings", "item.edit.actions": "item"} {
		if _, err := h.d.Db.Exec(`UPDATE plugin_entries SET config_json = ? WHERE id = 's1'`, `{"view":"views.panel","content_slot":"`+slot+`"}`); err != nil {
			t.Fatal(err)
		}
		if _, err := h.d.Db.Exec(`INSERT OR REPLACE INTO plugin_permissions(id,plugin_id,permission,granted) VALUES(?,?,?,1)`, "g"+slot, viewPluginID, "ui:slot:"+slot); err != nil {
			t.Fatal(err)
		}
		h.answerWith(pagedDoc(2, 3, ""))
		path := "/ui/slot/" + slot + "/com.test.views/panel?item_id=42"
		body := h.do(http.MethodGet, path, nil, true).Body.String()
		if !strings.Contains(body, `hx-get="/plugin/views/panel?item_id=42&amp;page=3&amp;slot=`+slot+`"`) ||
			!strings.Contains(body, `hx-get="/plugin/views/panel?item_id=42&amp;page=1&amp;slot=`+slot+`"`) {
			t.Errorf("%s (%s): links missing item_id/slot:\n%s", slot, host, body)
		}
		prefix := "plugin-slot-" + strings.ReplaceAll(slot, ".", "-") + "-"
		if !strings.Contains(body, `hx-target="#`+prefix) {
			t.Errorf("%s: no panel target:\n%s", slot, body)
		}
	}
}

// AC3, AC4, AC12: the panel GET is asked as a panel of the entry's slot.
func TestPluginSlot_PanelGetAsksAsSlot_4114(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(pagedDoc(2, 3, ""))
	h.setOtherAsked(false)
	rec := h.panelGet("/plugin/views/panel?days=7&page=2&slot=reports.panels", panel0)
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("panel GET = %d: %s", rec.Code, body)
	}
	if h.lastEv.Type != pluginViewAskEvent || h.lastPay["view"] != "views.panel" {
		t.Fatalf("ask = %s %v", h.lastEv.Type, h.lastPay)
	}
	params, _ := h.lastPay["params"].(map[string]any)
	if params["page"] != "2" || params["days"] != "7" || params["slot"] != "reports.panels" {
		t.Errorf("params = %v", params)
	}
	for _, want := range []string{
		`hx-get="/plugin/views/panel?days=7&amp;page=3&amp;slot=reports.panels"`,
		`hx-get="/plugin/views/panel?days=7&amp;page=1&amp;slot=reports.panels"`,
		`hx-target="#` + panel0 + `"`,
		`id="` + panel0 + `-alert" class="plugin-view-alert" aria-live="polite" hx-swap-oob="innerHTML"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	for _, bad := range []string{`id="plugin-view-title"`, "<html", "plugin-slot-reports-panels-1", `id="plugin-view-alert"`} {
		if strings.Contains(body, bad) {
			t.Errorf("fragment contains %q:\n%s", bad, body)
		}
	}
	if h.askedOther() {
		t.Error("a panel GET asked the other plugin")
	}
	if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "HX-Target") || rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Vary %q Cache-Control %q", rec.Header().Get("Vary"), rec.Header().Get("Cache-Control"))
	}
	assertPluginPolicy(t, rec)
}

// AC5: a slot-only plugin pages; ui:page alone does not.
func TestPluginSlot_PanelGetSlotOnlyGrant_4114(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(pagedDoc(2, 3, ""))
	h.revokePerm("ui:page")
	if rec := h.panelGet("/plugin/views/panel?page=2", panel0); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Page 2 of 3") {
		t.Fatalf("slot-only panel GET = %d:\n%s", rec.Code, rec.Body.String())
	}
}

func TestPluginSlot_PanelGetNeedsOwnSlotGrant_4114(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(pagedDoc(2, 3, ""))
	h.revokePerm("ui:slot:reports.panels")
	for _, g := range [][2]string{{"ui:page", "p"}, {"ui:slot:eod.footer", "y"}} {
		if _, err := h.d.Db.Exec(`INSERT OR REPLACE INTO plugin_permissions(id,plugin_id,permission,granted) VALUES(?,?,?,1)`, g[1], viewPluginID, g[0]); err != nil {
			t.Fatal(err)
		}
	}
	h.lastEv = plugins.Event{}
	assertPanelPageFailed(t, h.panelGet("/plugin/views/panel?page=2", panel0), panel0)
	if h.lastEv.Type != "" {
		t.Fatalf("a panel GET without ui:slot:reports.panels asked the plugin (%s)", h.lastEv.Type)
	}
}

// assertPanelPageFailed: a failed panel page swaps nothing and fills only
// the panel's own alert slot with plugin.view.page_failed.
func assertPanelPageFailed(t *testing.T, rec *httptest.ResponseRecorder, panel string) {
	t.Helper()
	body := rec.Body.String()
	if rec.Code != http.StatusBadGateway || rec.Header().Get("HX-Reswap") != "none" {
		t.Fatalf("failed panel GET = %d (HX-Reswap %q), want 502 and none:\n%s", rec.Code, rec.Header().Get("HX-Reswap"), body)
	}
	if !strings.Contains(body, `<div id="`+panel+`-alert" class="plugin-view-alert" aria-live="polite" hx-swap-oob="innerHTML">`) || strings.Contains(body, `id="plugin-view-alert"`) {
		t.Fatalf("must fill only the panel's alert slot:\n%s", body)
	}
	assertNotice(t, rec, "plugin.view.page_failed")
	for _, bad := range []string{"plugin-view-title", "plugin-view-table", "<table"} {
		if strings.Contains(body, bad) {
			t.Errorf("failure answer contains %q:\n%s", bad, body)
		}
	}
}

// AC6: the host gate runs first, before the plugin is asked.
func TestPluginSlot_PanelGetNeedsHostGate_4114(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(slotDoc("SAVED"))
	h.d.AuthSvc = auth.NewService(h.d.Db)
	t.Setenv("UT_AUTH", "on")
	hdr := map[string]string{"HX-Request": "true", "HX-Target": panel0}
	const path = "/plugin/views/panel?days=7"
	h.lastEv = plugins.Event{}
	rec := h.slotRouteReq(auth.User{ID: "c1", Role: "cashier"}, http.MethodGet, path, nil, hdr)
	if rec.Code != http.StatusForbidden || strings.Contains(rec.Body.String(), "SAVED") || h.lastEv.Type != "" {
		t.Errorf("cashier panel GET = %d (asked %q)", rec.Code, h.lastEv.Type)
	}
	rec = h.slotRouteReq(auth.User{ID: "a1", Role: "admin"}, http.MethodGet, path, nil, hdr)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SAVED") || h.lastEv.Type != pluginViewAskEvent {
		t.Errorf("admin panel GET = %d (asked %q)", rec.Code, h.lastEv.Type)
	}
}

// AC7: a request that contradicts the entry is refused 400 before any ask.
func TestPluginSlot_PanelGetRefusals_4114(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(slotDoc("SAVED"))
	refused := []struct{ name, path, target string }{
		{"another slot's panel", "/plugin/views/panel?days=7", "plugin-slot-eod-footer-0"},
		{"slot panel on an entry without a slot", "/plugin/views?days=7", panel0},
		{"query slot of another slot with a panel target", "/plugin/views/panel?slot=eod.footer", panel0},
		{"query slot of another slot, page GET", "/plugin/views/panel?slot=eod.footer", ""},
		{"alert id as target", "/plugin/views/panel", panel0 + "-alert"},
	}
	for _, c := range refused {
		h.lastEv = plugins.Event{}
		rec := h.panelGet(c.path, c.target)
		if c.target == "" {
			rec = h.do(http.MethodGet, c.path, nil, false)
		}
		if rec.Code != http.StatusBadRequest || strings.Contains(rec.Body.String(), "SAVED") || h.lastEv.Type != "" {
			t.Errorf("%s = %d (asked %q):\n%s", c.name, rec.Code, h.lastEv.Type, rec.Body.String())
		}
	}
	// Not panel-shaped: stays today's page fragment.
	for _, target := range []string{"main", pluginOverlayTargetID, "plugin-slot-x<script>", "plugin-view"} {
		rec := h.panelGet("/plugin/views/panel?days=7", target)
		body := rec.Body.String()
		if rec.Code != http.StatusOK || !strings.Contains(body, "SAVED") || strings.Contains(body, `id="`+panel0+`-alert"`) {
			t.Errorf("target %q = %d, want the page fragment:\n%s", target, rec.Code, body)
		}
	}
	// HEAD with a panel target stays a page HEAD.
	if rec := h.doAs(http.MethodHead, "/plugin/views/panel", nil, nil, map[string]string{"HX-Request": "true", "HX-Target": panel0}); rec.Code != http.StatusOK {
		t.Errorf("HEAD = %d, want 200", rec.Code)
	}
}

// AC8: a slow, failing, silent, invalid or redirecting plugin leaves the
// page shown and fills the panel's alert; the call returns within budget.
func TestPluginSlot_PanelGetFailureKeepsPage_4114(t *testing.T) {
	cases := map[string]func(h *slotHarness){
		"slow": func(h *slotHarness) {
			h.mu.Lock()
			h.answer = func(plugins.Event) (json.RawMessage, error) {
				time.Sleep(time.Second)
				return json.RawMessage(pagedDoc(2, 3, "")), nil
			}
			h.mu.Unlock()
		},
		"invalid": func(h *slotHarness) { h.answerWith(`{"document":{"version":99}}`) },
		"silent": func(h *slotHarness) {
			h.mu.Lock()
			h.answer = nil
			h.mu.Unlock()
		},
		"redirect": func(h *slotHarness) { h.answerWith(`{"redirect":"/plugin/views"}`) },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			h := newSlotHarness(t)
			shortenSlotTimeout(t, 100*time.Millisecond)
			setup(h)
			start := time.Now()
			rec := h.panelGet("/plugin/views/panel?page=2", panel0)
			if el := time.Since(start); el > 900*time.Millisecond {
				t.Errorf("answered after %s, want within the slot budget", el)
			}
			assertPanelPageFailed(t, rec, panel0)
		})
	}
}

// AC9: params.slot is core's, whatever the request says.
func TestPluginSlot_PanelGetSlotFromCore_4114(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(pagedDoc(1, 2, ""))
	if rec := h.panelGet("/plugin/views/panel?days=7", panel0); rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	params, _ := h.lastPay["params"].(map[string]any)
	if params["slot"] != "reports.panels" {
		t.Errorf("params = %v, want slot=reports.panels set by core", params)
	}
}

// AC10: links keep param and slot within the 20-key cap; a table paging
// with "slot" draws the indicator only.
func TestPluginSlot_PanelLinkCap_4114(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(pagedDoc(2, 5, ""))
	var q []string
	for i := 0; i < 20; i++ {
		q = append(q, fmt.Sprintf("k%02d=v", i))
	}
	for _, rec := range []*httptest.ResponseRecorder{
		h.do(http.MethodGet, "/ui/slot/reports.panels?"+strings.Join(q, "&"), nil, true),
		h.panelGet("/plugin/views/panel?"+strings.Join(q, "&"), panel0),
	} {
		body := rec.Body.String()
		n := 0
		for _, part := range strings.Split(body, `hx-get="`)[1:] {
			link := part[:strings.Index(part, `"`)]
			n++
			u, err := url.Parse(strings.ReplaceAll(link, "&amp;", "&"))
			if err != nil {
				t.Fatal(err)
			}
			if got := len(u.Query()); got > 20 || u.Query().Get("page") == "" || u.Query().Get("slot") != "reports.panels" {
				t.Errorf("link %q has %d keys, want <= 20 incl. page and slot", link, got)
			}
		}
		if n != 2 {
			t.Errorf("%d links, want 2:\n%s", n, body)
		}
	}
	h.answerWith(pagedDoc(2, 5, "slot"))
	body := h.do(http.MethodGet, "/ui/slot/reports.panels", nil, true).Body.String()
	if strings.Contains(body, `rel="next"`) || strings.Contains(body, `rel="prev"`) || !strings.Contains(body, "Page 2 of 5") {
		t.Errorf("param=slot table must show the indicator only:\n%s", body)
	}
}

// AC11: an action's answer in a panel, and the setup wizard, show the
// indicator only.
func TestPluginSlot_PanelActionAnswerIndicatorOnly_4114(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(pagedDoc(1, 3, ""))
	rec := h.postSlotAction(panel0)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Page 1 of 3") {
		t.Fatalf("POST = %d:\n%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `rel="next"`) {
		t.Errorf("an action answer drew paging links:\n%s", rec.Body.String())
	}
}

func TestPluginSlot_SetupWizardPagingIndicatorOnly_4114(t *testing.T) {
	h := newSlotHarness(t)
	if _, err := h.d.Db.Exec(`UPDATE plugin_entries SET config_json = '{"view":"views.panel","content_slot":"setup.wizard.steps"}' WHERE id = 's1'`); err != nil {
		t.Fatal(err)
	}
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('sw',?,'ui:slot:setup.wizard.steps',1)`, viewPluginID); err != nil {
		t.Fatal(err)
	}
	h.answerWith(pagedDoc(1, 3, ""))
	s := string(renderSetupSlot(httptest.NewRequest(http.MethodGet, "/setup", nil), h.d, "en"))
	if !strings.Contains(s, "Page 1 of 3") || strings.Contains(s, `rel="next"`) || strings.Contains(s, "hx-get") {
		t.Errorf("setup wizard paging must be indicator only:\n%s", s)
	}
}

func TestPluginOverlay_PagingIndicatorOnly_4114(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(pagedDoc(1, 3, ""))
	rec := h.doAs(http.MethodGet, "/plugin/views/next", nil, nil, map[string]string{"HX-Request": "true", "HX-Target": pluginOverlayTargetID})
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "Page 1 of 3") || strings.Contains(body, `rel="next"`) {
		t.Errorf("overlay paging must be indicator only (%d):\n%s", rec.Code, body)
	}
}

// The failure notice's text is its own key, not the page's "unavailable".
func TestPluginSlot_PageFailedKey_4114(t *testing.T) {
	newSlotHarness(t)
	if httpx.T("en", "plugin.view.page_failed") == "plugin.view.page_failed" {
		t.Error("plugin.view.page_failed is not in en.json")
	}
}
