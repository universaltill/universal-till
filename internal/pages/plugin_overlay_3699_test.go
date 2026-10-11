package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

func readFileForTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ADR-0149 §6 (ut-docs#3699): ui.view.ask / ui.action.ask carry the
// signed-in operator's role and permissions; a sell-screen plugin button
// with opens_view opens that view in core's overlay (#plugin-overlay).

func (h *viewHarness) doAs(method, path string, form url.Values, u *auth.User, hdr map[string]string) *httptest.ResponseRecorder {
	h.t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

func (h *viewHarness) operator() (role string, perms []string, userID string, present bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	op, ok := h.lastPay["operator"].(map[string]any)
	if !ok {
		return "", nil, "", false
	}
	role, _ = op["role"].(string)
	userID, _ = op["user_id"].(string)
	raw, isList := op["permissions"].([]any)
	if isList {
		perms = []string{}
	}
	for _, p := range raw {
		s, _ := p.(string)
		perms = append(perms, s)
	}
	return role, perms, userID, true
}

func TestPluginView_OperatorInPayload_3699(t *testing.T) {
	h := newViewHarness(t)
	h.d.AuthSvc = auth.NewService(h.d.Db)
	h.answerWith(viewDocument)
	manager := &auth.User{ID: "m1", Role: "manager"}
	cashier := &auth.User{ID: "c1", Role: "cashier"}

	// The page's GET (ui.view.ask).
	if rec := h.doAs(http.MethodGet, "/plugin/views", nil, manager, nil); rec.Code != http.StatusOK {
		t.Fatalf("manager GET = %d", rec.Code)
	}
	role, perms, uid, ok := h.operator()
	if !ok || role != "manager" || uid != "m1" || !slices.Contains(perms, "reports") {
		t.Fatalf("manager ask operator = %v %q %q %v, want role manager, user m1, reports granted", ok, role, uid, perms)
	}
	if !slices.IsSorted(perms) {
		t.Errorf("permissions not sorted: %v", perms)
	}

	if rec := h.doAs(http.MethodGet, "/plugin/views", nil, cashier, nil); rec.Code != http.StatusOK {
		t.Fatalf("cashier GET = %d", rec.Code)
	}
	role, perms, _, ok = h.operator()
	if !ok || role != "cashier" || slices.Contains(perms, "reports") {
		t.Fatalf("cashier ask operator = %v %q %v, want role cashier without reports", ok, role, perms)
	}
	if perms == nil {
		t.Fatal("cashier permissions missing, want a list (possibly empty), never absent")
	}

	// An action (ui.action.ask) carries it too.
	h.answerWith(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"ok"}}]}}`)
	form := url.Values{"_action": {"refresh"}}
	if rec := h.doAs(http.MethodPost, "/plugin/views", form, manager, map[string]string{"HX-Request": "true"}); rec.Code != http.StatusOK {
		t.Fatalf("manager POST = %d:\n%s", rec.Code, rec.Body.String())
	}
	if role, _, _, ok = h.operator(); !ok || role != "manager" {
		t.Fatalf("action operator = %v %q, want manager", ok, role)
	}

	// No signed-in operator (sign-in off): no operator at all.
	h.answerWith(viewDocument)
	h.doAs(http.MethodGet, "/plugin/views", nil, nil, nil)
	if _, _, _, ok = h.operator(); ok {
		t.Fatal("operator sent with nobody signed in")
	}
}

// A content slot's ask is role-aware too (askSlotEntries shares the path).
func TestPluginSlot_OperatorInPayload_3699(t *testing.T) {
	h := newViewHarness(t)
	h.d.AuthSvc = auth.NewService(h.d.Db)
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('slotgrant',?,'ui:slot:reports.panels',1)`, viewPluginID); err != nil {
		t.Fatal(err)
	}
	h.answerWith(viewDocument)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = auth.WithUser(req, auth.User{ID: "m1", Role: "manager"})
	entries := contentSlotEntriesForTest(t, h)
	askSlotEntries(req.Context(), h.d, "reports.panels", "en", map[string]string{}, entries)
	if role, _, _, ok := h.operator(); !ok || role != "manager" {
		t.Fatalf("slot ask operator = %v %q, want manager", ok, role)
	}
}

func contentSlotEntriesForTest(t *testing.T, h *viewHarness) []data.PageEntryRow {
	t.Helper()
	entries, err := data.NewPluginRepo(h.d.Db).ListPageEntries(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.PluginID == viewPluginID && e.Route == "/plugin/views" {
			return []data.PageEntryRow{e}
		}
	}
	t.Fatal("no /plugin/views entry")
	return nil
}

// seedOverlayButtons adds button entries to the view harness: one opening
// views.next, one plain event button, one naming a view no page has, one
// whose page fills a slot, and one from a plugin without ui:page.
func seedOverlayButtons(t *testing.T, h *viewHarness) {
	t.Helper()
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label,config_json) VALUES
		('s1',?,'page','slotted','/plugin/views/slotted','Slotted','{"view":"views.slotted","content_slot":"reports.panels"}'),
		('b1',?,'button','open','','Open log','{"opens_view":"views.next"}'),
		('b2',?,'button','ping','','Ping',''),
		('b3',?,'button','lost','','Lost','{"opens_view":"views.nope"}'),
		('b4',?,'button','slotbtn','','Slot btn','{"opens_view":"views.slotted"}'),
		('b5','com.test.other','button','theirs','','Theirs','{"opens_view":"other.home"}')`,
		viewPluginID, viewPluginID, viewPluginID, viewPluginID, viewPluginID); err != nil {
		t.Fatal(err)
	}
}

func TestPluginButtons_OpensViewOpensOverlay_3699(t *testing.T) {
	h := newViewHarness(t)
	seedOverlayButtons(t, h)
	rec := h.do(http.MethodGet, "/ui/plugin-buttons", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/plugin-buttons = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`hx-get="/plugin/views/next"`,
		`hx-target="#plugin-overlay-view"`,
		`data-ut-plugin-overlay`,
		`aria-controls="plugin-overlay"`,
		`Open log`,
		// The event button is unchanged.
		`hx-post="/api/plugins/entries/` + viewPluginID + `/ping/action"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("plugin buttons missing %q in:\n%s", want, body)
		}
	}
	// A button whose view no overlay-able page serves, or whose plugin
	// lacks ui:page, is not drawn (and never falls back to an event).
	for _, gone := range []string{"Lost", "Slot btn", "Theirs", "/lost/action", "/slotbtn/action", "/theirs/action"} {
		if strings.Contains(body, gone) {
			t.Errorf("plugin buttons drew %q:\n%s", gone, body)
		}
	}
}

// An overlay button pressed through the event endpoint publishes nothing.
func TestPluginButtons_OverlayButtonHasNoEventAction_3699(t *testing.T) {
	h := newViewHarness(t)
	seedOverlayButtons(t, h)
	rec := h.do(http.MethodPost, "/api/plugins/entries/"+viewPluginID+"/open/action", nil, true)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST overlay button action = %d, want 404", rec.Code)
	}
}

func TestPluginView_OverlayTarget_3699(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(viewDocument)
	overlay := map[string]string{"HX-Request": "true", "HX-Target": pluginOverlayTargetID}

	rec := h.doAs(http.MethodGet, "/plugin/views/next", nil, nil, overlay)
	if rec.Code != http.StatusOK {
		t.Fatalf("overlay GET = %d:\n%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<html") || strings.Contains(body, `id="plugin-view-title"`) {
		t.Fatalf("overlay GET answered a page, want the body fragment:\n%s", body)
	}
	for _, want := range []string{
		`hx-target="#plugin-overlay-view"`, // the view's own actions stay in the overlay
		`class="plugin-view-title"`,        // the document's title, inside the overlay
		`id="plugin-overlay-view-alert"`,   // a stale failure notice is cleared
	} {
		if !strings.Contains(body, want) {
			t.Errorf("overlay GET missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, `hx-target="#plugin-view"`) {
		t.Errorf("overlay GET targets the page container:\n%s", body)
	}

	// An action from the overlay answers into the overlay.
	rec = h.doAs(http.MethodPost, "/plugin/views/next", url.Values{"_action": {"refresh"}}, nil, overlay)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `hx-target="#plugin-overlay-view"`) {
		t.Fatalf("overlay action = %d, want the body re-targeted at the overlay:\n%s", rec.Code, rec.Body.String())
	}
	// A failed one keeps the overlay's form and fills the overlay's alert.
	h.answerWith(`not json`)
	rec = h.doAs(http.MethodPost, "/plugin/views/next", url.Values{"_action": {"refresh"}}, nil, overlay)
	if rec.Header().Get("HX-Reswap") != "none" || !strings.Contains(rec.Body.String(), `id="plugin-overlay-view-alert"`) {
		t.Fatalf("failed overlay action: reswap %q, body:\n%s", rec.Header().Get("HX-Reswap"), rec.Body.String())
	}

	if !strings.Contains(strings.Join(rec.Header().Values("Vary"), ","), "HX-Target") {
		t.Errorf("overlay answers must Vary on HX-Target (same URL, different body): Vary = %v", rec.Header().Values("Vary"))
	}

	// A slot entry's route is never drawn into the overlay (the overlay
	// only opens slot-less pages; review finding, 2026-10-11): an
	// overlay-targeted GET gets the usual content answer, not the body.
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label,config_json) VALUES('s9',?,'page','sl','/plugin/views/sl','Sl','{"view":"views.sl","content_slot":"eod.footer"}')`, viewPluginID); err != nil {
		t.Fatal(err)
	}
	t.Setenv("UT_AUTH", "off")
	h.answerWith(viewDocument)
	rec = h.doAs(http.MethodGet, "/plugin/views/sl", nil, nil, overlay)
	if !strings.Contains(rec.Body.String(), `plugin-view`) {
		t.Fatalf("slot entry GET drew no view at all (test cannot tell):\n%s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `hx-target="#plugin-overlay-view"`) {
		t.Fatalf("slot entry drawn into the overlay:\n%s", rec.Body.String())
	}

	// A plain page GET is unchanged: the whole page, #plugin-view.
	h.answerWith(viewDocument)
	rec = h.doAs(http.MethodGet, "/plugin/views/next", nil, nil, nil)
	if !strings.Contains(rec.Body.String(), `id="plugin-view"`) || !strings.Contains(rec.Body.String(), `hx-target="#plugin-view"`) {
		t.Fatalf("plain page GET changed:\n%s", rec.Body.String())
	}
}

// The overlay lives in base.html, outside #ut-page, so it survives an
// htmx navigation; it never raises the scrim and is non-modal.
func TestPluginOverlay_InBaseLayoutOutsidePage_3699(t *testing.T) {
	chdirRoot(t)
	base := readFileForTest(t, "web/ui/layouts/base.html")
	pageEnd := strings.Index(base, `</div><!-- /ut-page -->`)
	at := strings.Index(base, `<section id="plugin-overlay"`)
	if pageEnd < 0 || at < 0 || at < pageEnd {
		t.Fatalf("#plugin-overlay must come after #ut-page closes (pageEnd %d, overlay %d)", pageEnd, at)
	}
	overlay := base[at:]
	overlay = overlay[:strings.Index(overlay, "</script>")]
	for _, want := range []string{`data-ut-no-scrim`, `aria-modal="false"`, `id="plugin-overlay-view"`, `id="plugin-overlay-view-alert"`, `{{ T "common.close" }}`, ` hidden>`, `'Escape'`} {
		if !strings.Contains(overlay, want) {
			t.Errorf("#plugin-overlay missing %q", want)
		}
	}
	if strings.Contains(overlay, "showModal") {
		t.Error("plugin overlay must be non-modal (no showModal)")
	}
	css := readFileForTest(t, "web/public/app.css")
	if !strings.Contains(css, ".plugin-overlay[hidden] { display: none; }") {
		t.Error("app.css: .plugin-overlay's display must not override [hidden]")
	}
}
