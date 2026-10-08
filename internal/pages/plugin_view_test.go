package pages

import (
	"context"
	"encoding/json"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

const viewPluginID = "com.test.views"

// viewHarness is a page entry with a view (ADR-0121 §7, ut-docs#3160), its
// plugin holding ui:page + events:receive, and a fake in-process plugin
// answering ui.view.ask / ui.action.ask on the shared bus.
type viewHarness struct {
	t   *testing.T
	d   *common.Deps
	mux *http.ServeMux

	mu      sync.Mutex
	answer  func(ev plugins.Event) (json.RawMessage, error)
	lastEv  plugins.Event
	lastPay map[string]any
}

func newViewHarness(t *testing.T) *viewHarness {
	t.Helper()
	d, _ := pluginPageTestDeps(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("load i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	seedTestPlugin(t, d.Db, viewPluginID, "Views", "1.0.0")
	seedTestPlugin(t, d.Db, "com.test.other", "Other", "1.0.0")
	if _, err := d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label,config_json) VALUES
		('v1',?,'page','home','/plugin/views','Views page','{"view":"views.home"}'),
		('v2',?,'page','next','/plugin/views/next','Next','{"view":"views.next"}'),
		('v3','com.test.other','page','other','/plugin/other','Other','{"view":"other.home"}'),
		('v4',?,'page','static','/plugin/views/static','Static','')`, viewPluginID, viewPluginID, viewPluginID); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"ui:page", "events:receive"} {
		if _, err := d.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES(?,?,?,1)`, viewPluginID+p, viewPluginID, p); err != nil {
			t.Fatal(err)
		}
	}
	// The plugin declares hooks for both asks (manifest "hooks"), as a
	// real view plugin must.
	for i, ev := range []string{"ui.view.ask", "ui.action.ask"} {
		if _, err := d.Db.Exec(`INSERT INTO plugin_hooks(id,plugin_id,event,action,is_active) VALUES(?,?,?,'view',1)`, viewPluginID+"-h"+string(rune('0'+i)), viewPluginID, ev); err != nil {
			t.Fatal(err)
		}
	}
	origKeys := pluginOwnLocaleKeys
	pluginOwnLocaleKeys = func(_ *common.Deps, id string) map[string]bool {
		if id == viewPluginID {
			return map[string]bool{"plugin.views.hello": true}
		}
		return nil
	}
	t.Cleanup(func() { pluginOwnLocaleKeys = origKeys })

	h := &viewHarness{t: t, d: d, mux: http.NewServeMux()}
	registerPluginPages(h.mux, d)

	bus := plugins.SharedBus(d.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	for _, ev := range []string{pluginViewAskEvent, pluginActionAskEvent} {
		bus.SetEventMode(ev, plugins.Blocking)
	}
	if _, err := bus.SubscribeWithHandler(t.Context(), viewPluginID, []string{pluginViewAskEvent, pluginActionAskEvent},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			h.mu.Lock()
			h.lastEv = ev
			h.lastPay = nil
			_ = json.Unmarshal(ev.Payload, &h.lastPay)
			answer := h.answer
			h.mu.Unlock()
			if answer == nil {
				return nil, nil
			}
			return answer(ev)
		}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	return h
}

func (h *viewHarness) answerWith(raw string) {
	h.mu.Lock()
	h.answer = func(plugins.Event) (json.RawMessage, error) { return json.RawMessage(raw), nil }
	h.mu.Unlock()
}

func (h *viewHarness) do(method, path string, form url.Values, htmx bool) *httptest.ResponseRecorder {
	h.t.Helper()
	var req *http.Request
	if form != nil {
		req = httptest.NewRequest(method, path, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	} else {
		req = httptest.NewRequest(method, path, nil)
	}
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	return rec
}

const viewDocument = `{"document":{"version":1,"title":{"key":"plugin.views.hello"},"components":[
	{"type":"text","text":{"literal":"<script>alert(1)</script>"}},
	{"type":"table","columns":[{"label":{"literal":"Item"},"kind":"text"},{"label":{"literal":"Total"},"kind":"money"}],"rows":[["Tea",{"minor":350,"currency":"GBP"}]]},
	{"type":"button","action":"refresh","label":{"literal":"Refresh"}},
	{"type":"form","action":"save","submit":{"literal":"Save"},"fields":[
		{"name":"note","label":{"literal":"Note"},"kind":"text"},
		{"name":"price","label":{"literal":"Price"},"kind":"money","value":250},
		{"name":"api_key","label":{"literal":"Key"},"kind":"secret"}]}]}}`

func assertPluginPolicy(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Content-Security-Policy"); got != pluginPageCSP {
		t.Errorf("CSP = %q, want %q", got, pluginPageCSP)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("nosniff missing")
	}
}

func assertUnavailable(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d", rec.Code, wantStatus)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `plugin-view-notice warn`) || !strings.Contains(body, template.HTMLEscapeString(httpx.T("en", "plugin.view.unavailable"))) {
		t.Fatalf("no unavailable notice in:\n%s", body)
	}
	if httpx.T("en", "plugin.view.unavailable") == "plugin.view.unavailable" {
		t.Fatal("plugin.view.unavailable is not in en.json")
	}
}

func TestPluginView_GetRendersDocument_3160(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(viewDocument)
	rec := h.do(http.MethodGet, "/plugin/views?q=tea&x="+strings.Repeat("a", 300), nil, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	assertPluginPolicy(t, rec)
	body := rec.Body.String()

	// The ask payload: view, bounded params, locale.
	if h.lastEv.Type != pluginViewAskEvent || h.lastPay["view"] != "views.home" || h.lastPay["locale"] != "en" {
		t.Fatalf("ask = %s %v", h.lastEv.Type, h.lastPay)
	}
	params, _ := h.lastPay["params"].(map[string]any)
	if params["q"] != "tea" {
		t.Errorf("params = %v, want q=tea", params)
	}
	if _, has := params["x"]; has {
		t.Error("an oversize param value was forwarded")
	}

	// Page chrome + the document, plugin literal escaped.
	// The document title (a key from the plugin's own bundle; this test
	// bundle has no translation, so T shows the key) is the page heading.
	if !strings.Contains(body, `<h1 id="plugin-view-title">plugin.views.hello</h1>`) {
		t.Error("document title is not the page heading")
	}
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("plugin literal rendered unescaped")
	}
	if !strings.Contains(body, "&lt;script&gt;alert(1)&lt;/script&gt;") {
		t.Error("escaped literal missing")
	}
	if !strings.Contains(body, "£3.50") {
		t.Error("money cell not rendered from minor units")
	}
	if !strings.Contains(body, `value="2.50"`) {
		t.Error("money field value not rendered")
	}
	if !regexp.MustCompile(`type="password" name="api_key" value=""`).MatchString(body) {
		t.Error("secret field must be an empty password input")
	}
	// Every form posts to the entry's own route; hx-target is the view.
	// (Scoped to the view container: the page chrome has its own chips.)
	start := strings.Index(body, `id="plugin-view"`)
	end := strings.LastIndex(body, "</form>")
	if start < 0 || end < start {
		t.Fatal("view container / forms missing")
	}
	view := body[start:end]
	for _, m := range regexp.MustCompile(`(?:hx-post|hx-get|hx-put|hx-delete| action)="([^"]*)"`).FindAllStringSubmatch(view, -1) {
		if m[1] != "/plugin/views" {
			t.Errorf("%s targets %q, want the entry route", m[0], m[1])
		}
	}
	if n := strings.Count(body, `hx-post="/plugin/views"`); n != 2 {
		t.Errorf("hx-post count = %d, want 2 (button + form)", n)
	}
	if strings.Count(body, `hx-target="#plugin-view"`) != 2 {
		t.Error("hx-target must be the view container")
	}
	if !strings.Contains(body, `id="plugin-view"`) {
		t.Error("view container missing")
	}
}

func TestPluginView_PostActionReturnsDocument_3160(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(`{"document":{"version":1,"components":[{"type":"notice","level":"info","text":{"literal":"Saved"}}]}}`)
	form := url.Values{"_action": {"save"}, "note": {"hi"}, "price": {"2.50"}, "_kind.price": {"money"}, "api_key": {"s3"}}
	rec := h.do(http.MethodPost, "/plugin/views", form, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	assertPluginPolicy(t, rec)
	if h.lastEv.Type != pluginActionAskEvent || h.lastPay["action"] != "save" || h.lastPay["view"] != "views.home" {
		t.Fatalf("ask = %s %v", h.lastEv.Type, h.lastPay)
	}
	f, _ := h.lastPay["form"].(map[string]any)
	if f["note"] != "hi" || f["api_key"] != "s3" {
		t.Errorf("form = %v", f)
	}
	if m, _ := f["price"].(map[string]any); m["minor"] != float64(250) || m["currency"] != "GBP" {
		t.Errorf("money field = %v, want 250 GBP minor units", f["price"])
	}
	if uh, ok := h.lastPay["upload_handles"].([]any); !ok || len(uh) != 0 {
		t.Errorf("upload_handles = %v, want []", h.lastPay["upload_handles"])
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Saved") || strings.Contains(body, "<html") {
		t.Fatalf("htmx action must answer the view fragment only:\n%s", body)
	}

	// Without htmx the same post renders the whole page.
	rec = h.do(http.MethodPost, "/plugin/views", form, false)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "<html") || !strings.Contains(rec.Body.String(), "Saved") {
		t.Fatalf("no-JS POST = %d, want full page with the new document", rec.Code)
	}
}

func TestPluginView_Redirect_3160(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(`{"redirect":"/plugin/views/next"}`)
	form := url.Values{"_action": {"go"}}
	rec := h.do(http.MethodPost, "/plugin/views", form, true)
	if rec.Code != http.StatusOK || rec.Header().Get("HX-Redirect") != "/plugin/views/next" {
		t.Fatalf("htmx redirect = %d %q", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	rec = h.do(http.MethodPost, "/plugin/views", form, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/plugin/views/next" {
		t.Fatalf("redirect = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// Another plugin's route is refused -> notice, no redirect.
	h.answerWith(`{"redirect":"/plugin/other"}`)
	rec = h.do(http.MethodPost, "/plugin/views", form, true)
	if rec.Header().Get("HX-Redirect") != "" {
		t.Fatal("redirected to another plugin's route")
	}
	assertActionFailed(t, rec, http.StatusBadGateway, "plugin.view.action_failed")
}

func TestPluginView_TimeoutRendersNotice_3160(t *testing.T) {
	h := newViewHarness(t)
	orig := pluginViewTimeout
	pluginViewTimeout = 50 * time.Millisecond
	t.Cleanup(func() { pluginViewTimeout = orig })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h.mu.Lock()
	// Ignores its context on purpose: the page must not wait for it.
	h.answer = func(plugins.Event) (json.RawMessage, error) {
		<-release
		return json.RawMessage(viewDocument), nil
	}
	h.mu.Unlock()
	start := time.Now()
	rec := h.do(http.MethodGet, "/plugin/views", nil, false)
	if time.Since(start) > 2*time.Second {
		t.Fatal("the page waited for a slow plugin")
	}
	assertUnavailable(t, rec, http.StatusOK)
	assertPluginPolicy(t, rec)
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Fatal("a failed view still renders the page chrome")
	}
}

func TestPluginView_BrokenAnswersRenderNotice_3160(t *testing.T) {
	h := newViewHarness(t)
	for name, raw := range map[string]string{
		"invalid json":      `{"document":`,
		"unknown component": `{"document":{"version":1,"components":[{"type":"iframe"}]}}`,
		"core key":          `{"document":{"version":1,"components":[{"type":"text","text":{"key":"nav.home"}}]}}`,
		"raw html":          `<p>hello</p>`,
	} {
		t.Run(name, func(t *testing.T) {
			h.answerWith(raw)
			assertUnavailable(t, h.do(http.MethodGet, "/plugin/views", nil, false), http.StatusOK)
			assertActionFailed(t, h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"x"}}, true), http.StatusBadGateway, "plugin.view.action_failed")
		})
	}
	t.Run("plugin error", func(t *testing.T) {
		h.mu.Lock()
		h.answer = func(plugins.Event) (json.RawMessage, error) { return nil, context.DeadlineExceeded }
		h.mu.Unlock()
		assertUnavailable(t, h.do(http.MethodGet, "/plugin/views", nil, false), http.StatusOK)
	})
	t.Run("handler panics", func(t *testing.T) {
		h.mu.Lock()
		h.answer = func(plugins.Event) (json.RawMessage, error) { panic("boom") }
		h.mu.Unlock()
		start := time.Now()
		assertUnavailable(t, h.do(http.MethodGet, "/plugin/views", nil, false), http.StatusOK)
		if time.Since(start) > 2*time.Second {
			t.Fatal("a panicking plugin handler made the page wait for the deadline")
		}
	})
	t.Run("no answer", func(t *testing.T) {
		h.mu.Lock()
		h.answer = nil
		h.mu.Unlock()
		assertUnavailable(t, h.do(http.MethodGet, "/plugin/views", nil, false), http.StatusOK)
	})
	t.Run("bad form", func(t *testing.T) {
		h.answerWith(viewDocument)
		assertActionFailed(t, h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"../admin"}}, true), http.StatusBadRequest, "plugin.view.action_failed")
	})
}

func TestPluginView_NeedsUIPagePermission_3160(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(viewDocument)
	if _, err := h.d.Db.Exec(`UPDATE plugin_permissions SET granted = 0 WHERE plugin_id = ? AND permission = 'ui:page'`, viewPluginID); err != nil {
		t.Fatal(err)
	}
	rec := h.do(http.MethodGet, "/plugin/views", nil, false)
	assertUnavailable(t, rec, http.StatusOK)
	if h.lastEv.Type != "" {
		t.Fatal("plugin was asked without ui:page")
	}
}

func TestPluginView_MethodsAndNonViewEntry_3160(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(viewDocument)
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rec := h.do(m, "/plugin/views", nil, false)
		if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD, POST" {
			t.Errorf("%s view entry = %d Allow %q", m, rec.Code, rec.Header().Get("Allow"))
		}
		assertPluginPolicy(t, rec)
	}
	// ut-docs#3789 unchanged: a non-view entry still refuses POST.
	rec := h.do(http.MethodPost, "/plugin/views/static", url.Values{"_action": {"x"}}, false)
	if rec.Code != http.StatusMethodNotAllowed || rec.Header().Get("Allow") != "GET, HEAD" {
		t.Fatalf("POST non-view entry = %d Allow %q, want 405 GET, HEAD", rec.Code, rec.Header().Get("Allow"))
	}
	assertPluginPolicy(t, rec)
}

// Every v1 component renders through core's partials, in an RTL locale
// too, with nothing a plugin wrote reaching the page unescaped.
func TestPluginView_FullVocabularyRenders_3160(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(`{"document":{"version":1,"components":[
		{"type":"heading","text":{"literal":"H <b>1</b>"}},
		{"type":"notice","level":"error","text":{"literal":"Bad"}},
		{"type":"stat_tiles","tiles":[{"label":{"literal":"Sales"},"value":{"minor":123456,"currency":"EUR"}},{"label":{"literal":"N"},"value":1234}]},
		{"type":"list","items":[{"literal":"one\" onmouseover=\"x"},{"key":"plugin.views.hello"}]},
		{"type":"empty_state","title":{"literal":"Nothing"},"body":{"literal":"Add"},"action":{"action":"create","label":{"literal":"Create"}}},
		{"type":"button","action":"wipe","label":{"literal":"Wipe"},"style":"danger"},
		{"type":"form","action":"save","submit":{"literal":"Save"},"fields":[
			{"name":"qty","label":{"literal":"Qty"},"kind":"number","value":"2"},
			{"name":"mode","label":{"literal":"Mode"},"kind":"select","options":[{"value":"a","label":{"literal":"A"}},{"value":"b","label":{"literal":"B"}}],"value":"b"},
			{"name":"on","label":{"literal":"On"},"kind":"toggle","value":true}]}]}}`)
	req := httptest.NewRequest(http.MethodGet, "/plugin/views?lang=fa", nil)
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `dir="rtl"`) || h.lastPay["locale"] != "fa" {
		t.Errorf("fa page: rtl=%v, ask locale=%v", strings.Contains(body, `dir="rtl"`), h.lastPay["locale"])
	}
	for _, want := range []string{
		`H &lt;b&gt;1&lt;/b&gt;`,
		`class="plugin-view-notice error" role="alert"`,
		`class="kpi-value">`,
		`one&#34; onmouseover=&#34;x`,
		`class="card plugin-view-empty"`,
		`value="create"`,
		`class="btn btn-touch danger"`,
		`name="_kind.qty" value="number"`,
		`inputmode="decimal"`,
		`<option value="b" selected>`,
		`name="_kind.on" value="toggle"`,
		`type="checkbox" name="on" value="true" checked`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered view missing %q", want)
		}
	}
	if strings.Contains(body, "<b>1</b>") || strings.Contains(body, `onmouseover="x`) {
		t.Fatal("plugin text reached the page unescaped")
	}
}

// The plugin view CSS is RTL-safe: logical properties only.
func TestPluginView_CSSIsLogical_3160(t *testing.T) {
	chdirRoot(t)
	raw, err := os.ReadFile(filepath.Join("web", "public", "app.css"))
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	start := strings.Index(css, "/* ---------- Plugin views (ADR-0121")
	if start < 0 {
		t.Fatal("plugin view CSS block missing")
	}
	block := css[start:]
	if end := strings.Index(block[10:], "/* ----------"); end >= 0 {
		block = block[:end+10]
	}
	// Comments aside, no physical left/right.
	code := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(block, "")
	if m := regexp.MustCompile(`(?i)\b(left|right)\b|-left|-right`).FindString(code); m != "" {
		t.Fatalf("plugin view CSS uses physical %q; use logical properties", m)
	}
}

// ADR-0121 §7: core-generated hx-* only ever target /plugin/…, so a view on
// a legacy root route (/faq-style) is ignored: the entry renders as a
// static page and still refuses POST.
func TestPluginView_OnlyUnderPluginPrefix_3160(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(viewDocument)
	if _, err := h.d.Db.Exec(`INSERT INTO plugin_entries(id,plugin_id,type,key,route,label,config_json) VALUES('v5',?,'page','root','/viewsroot','Root','{"view":"views.home"}')`, viewPluginID); err != nil {
		t.Fatal(err)
	}
	registerIndex(h.mux, h.d)
	rec := h.do(http.MethodGet, "/viewsroot", nil, false)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `id="plugin-view"`) || h.lastEv.Type != "" {
		t.Fatalf("root-route view entry = %d, asked %q; want the static page, no ask", rec.Code, h.lastEv.Type)
	}
	assertPluginPolicy(t, rec)
	if rec := h.do(http.MethodPost, "/viewsroot", url.Values{"_action": {"x"}}, false); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST root-route entry = %d, want 405", rec.Code)
	}
}

// An htmx action swaps only the view body, so a new document's title must
// reach the page heading out of band (review finding, ut-docs#3160).
func TestPluginView_ActionTitleUpdatesHeading_3160(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(`{"document":{"version":1,"title":{"literal":"Step <2>"},"components":[{"type":"text","text":{"literal":"x"}}]}}`)
	rec := h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"refresh"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="plugin-view-title" hx-swap-oob="true"`) || !strings.Contains(body, "Step &lt;2&gt;") {
		t.Fatalf("htmx action must update the heading out of band, escaped:\n%s", body)
	}

	// The full page carries the id the out-of-band swap targets.
	h.answerWith(viewDocument)
	rec = h.do(http.MethodGet, "/plugin/views", nil, false)
	if !strings.Contains(rec.Body.String(), `<h1 id="plugin-view-title">`) {
		t.Fatalf("page heading has no plugin-view-title id")
	}
}

// _job is core's poll parameter (ADR-0121 §8, ut-docs#3908): it never
// reaches the plugin as a view param.
func TestPluginView_JobParamReserved_3908(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/plugin/views?_job=abc&a=1", nil)
	got := pluginViewParams(r)
	if _, ok := got["_job"]; ok || got["a"] != "1" || len(got) != 1 {
		t.Fatalf("params = %v, want only a=1", got)
	}
}

// ut-docs#3879: a failed action must not cost the operator their input.

// answerByEvent answers ui.view.ask and ui.action.ask differently.
func (h *viewHarness) answerByEvent(view, action func() (json.RawMessage, error)) {
	h.mu.Lock()
	h.answer = func(ev plugins.Event) (json.RawMessage, error) {
		if ev.Type == pluginViewAskEvent {
			return view()
		}
		return action()
	}
	h.mu.Unlock()
}

func fixedAnswer(raw string) func() (json.RawMessage, error) {
	return func() (json.RawMessage, error) { return json.RawMessage(raw), nil }
}

func failingAnswer() (json.RawMessage, error) { return nil, context.DeadlineExceeded }

// oobAlertRe matches the out-of-band #plugin-view-alert slot.
var oobAlertRe = regexp.MustCompile(`<div id="plugin-view-alert"[^>]*hx-swap-oob="[^"]+"[^>]*>`)

// assertActionFailed: an htmx action failure leaves the view as it is
// (HX-Reswap: none) and only fills the alert slot out of band.
func assertActionFailed(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, key string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d", rec.Code, wantStatus)
	}
	if got := rec.Header().Get("HX-Reswap"); got != "none" {
		t.Errorf("HX-Reswap = %q, want none (the form must stay)", got)
	}
	body := rec.Body.String()
	if !oobAlertRe.MatchString(body) {
		t.Fatalf("no out-of-band plugin-view-alert in:\n%s", body)
	}
	assertNotice(t, rec, key)
	for _, bad := range []string{"<form", "plugin-view-poll", `id="plugin-view"`, "<html"} {
		if strings.Contains(body, bad) {
			t.Errorf("failure answer carries %q; only the alert may change:\n%s", bad, body)
		}
	}
}

func TestPluginView_HtmxActionFailureKeepsForm_3879(t *testing.T) {
	h := newViewHarness(t)
	h.answerByEvent(fixedAnswer(viewDocument), failingAnswer)
	form := url.Values{"_action": {"save"}, "note": {"hi"}}
	assertActionFailed(t, h.do(http.MethodPost, "/plugin/views", form, true), http.StatusBadGateway, "plugin.view.action_failed")

	// An invalid answer and an unreadable form fail the same way.
	h.answerWith(`{"document":`)
	assertActionFailed(t, h.do(http.MethodPost, "/plugin/views", form, true), http.StatusBadGateway, "plugin.view.action_failed")
	assertActionFailed(t, h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"../admin"}}, true), http.StatusBadRequest, "plugin.view.action_failed")
}

func TestPluginView_HtmxActionTimeoutKeepsForm_3879(t *testing.T) {
	h := newViewHarness(t)
	orig := pluginViewTimeout
	pluginViewTimeout = 50 * time.Millisecond
	t.Cleanup(func() { pluginViewTimeout = orig })
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h.mu.Lock()
	h.answer = func(plugins.Event) (json.RawMessage, error) {
		<-release
		return json.RawMessage(viewDocument), nil
	}
	h.mu.Unlock()
	assertActionFailed(t, h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"save"}}, true), http.StatusBadGateway, "plugin.view.action_failed")
}

func TestPluginView_HtmxJobBusyKeepsForm_3879(t *testing.T) {
	h := newJobHarness(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h.blockingJob(release)
	h.startJob(true)
	h.startJob(true)
	rec := h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"identify"}, "note": {"tea"}}, true)
	assertActionFailed(t, rec, http.StatusTooManyRequests, "plugin.job.busy")
}

// A successful htmx answer (document, job poll) and every htmx poll answer
// clear a stale failure notice out of band.
func TestPluginView_HtmxSuccessClearsAlert_3879(t *testing.T) {
	emptyAlert := regexp.MustCompile(`<div id="plugin-view-alert"[^>]*hx-swap-oob="[^"]+"[^>]*></div>`)
	h := newViewHarness(t)
	h.answerWith(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"ok"}}]}}`)
	rec := h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"save"}}, true)
	if rec.Code != http.StatusOK || !emptyAlert.MatchString(rec.Body.String()) {
		t.Fatalf("htmx success = %d, no empty out-of-band alert:\n%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("HX-Reswap") != "" {
		t.Error("a successful answer must swap the view")
	}

	jh := newJobHarness(t)
	jh.onJob(func(context.Context, plugins.Event) (json.RawMessage, error) {
		return json.RawMessage(`{"document":{"version":1,"components":[{"type":"text","text":{"literal":"done"}}]}}`), nil
	})
	id, rec := jh.startJob(true)
	if !emptyAlert.MatchString(rec.Body.String()) {
		t.Fatalf("job start answer has no empty out-of-band alert:\n%s", rec.Body.String())
	}
	if rec := jh.pollUntil(id); !emptyAlert.MatchString(rec.Body.String()) {
		t.Fatalf("poll answer has no empty out-of-band alert:\n%s", rec.Body.String())
	}
}

// The full page carries the (empty) slot the out-of-band swaps target,
// directly above the view.
func TestPluginView_PageHasAlertSlot_3879(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(viewDocument)
	body := h.do(http.MethodGet, "/plugin/views", nil, false).Body.String()
	slot := strings.Index(body, `id="plugin-view-alert"`)
	view := strings.Index(body, `id="plugin-view"`)
	if slot < 0 || view < slot {
		t.Fatalf("alert slot missing or not above the view (slot %d, view %d)", slot, view)
	}
	if !regexp.MustCompile(`<div id="plugin-view-alert" class="plugin-view-alert" aria-live="polite"></div>`).MatchString(body) {
		t.Errorf("slot must be an empty polite live region:\n%s", body[slot-5:view])
	}
}

const refillDocument = `{"document":{"version":1,"components":[
	{"type":"form","action":"save","submit":{"literal":"Save"},"fields":[
		{"name":"note","label":{"literal":"Note"},"kind":"text","value":"default note"},
		{"name":"mode","label":{"literal":"Mode"},"kind":"select","options":[{"value":"a","label":{"literal":"A"}},{"value":"b","label":{"literal":"B"}}],"value":"a"},
		{"name":"on","label":{"literal":"On"},"kind":"toggle","value":true},
		{"name":"api_key","label":{"literal":"Key"},"kind":"secret"}]}]}}`

func assertRefilledPage(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int, key string) {
	t.Helper()
	if rec.Code != wantStatus {
		t.Fatalf("status = %d, want %d", rec.Code, wantStatus)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<html") {
		t.Fatal("no-JS failure must render the whole page")
	}
	assertNotice(t, rec, key)
	slot := strings.Index(body, `id="plugin-view-alert"`)
	notice := strings.Index(body, template.HTMLEscapeString(httpx.T("en", key)))
	view := strings.Index(body, `id="plugin-view"`)
	if slot < 0 || notice < slot || view < notice {
		t.Fatalf("notice must sit in the alert slot above the view (slot %d, notice %d, view %d)", slot, notice, view)
	}
	for _, want := range []string{
		`name="note" value="typed &lt;b&gt;"`,
		`<option value="b" selected>`,
		`type="password" name="api_key" value=""`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("refilled page missing %q", want)
		}
	}
	if strings.Contains(body, "s3cret") {
		t.Fatal("a posted secret was written back into the page")
	}
	if strings.Contains(body, `name="on" value="true" checked`) {
		t.Error("an unposted toggle must come back unchecked")
	}
}

func TestPluginView_NoJSActionFailureRefillsForm_3879(t *testing.T) {
	h := newViewHarness(t)
	h.answerByEvent(fixedAnswer(refillDocument), failingAnswer)
	form := url.Values{"_action": {"save"}, "_kind.on": {"toggle"}, "note": {"typed <b>"}, "mode": {"b"}, "api_key": {"s3cret"}}
	rec := h.do(http.MethodPost, "/plugin/views", form, false)
	assertRefilledPage(t, rec, http.StatusOK, "plugin.view.action_failed")
	if h.lastEv.Type != pluginViewAskEvent {
		t.Fatalf("last ask = %s, want the view re-asked", h.lastEv.Type)
	}
	if p, _ := h.lastPay["params"].(map[string]any); len(p) != 0 {
		t.Errorf("re-ask params = %v, want none", h.lastPay["params"])
	}
}

func TestPluginView_NoJSUnreadableFormShowsViewUnfilled_3879(t *testing.T) {
	h := newViewHarness(t)
	h.answerByEvent(fixedAnswer(refillDocument), failingAnswer)
	rec := h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"../admin"}, "note": {"typed"}}, false)
	// The no-JS page answers 200, as it always has (only the htmx answers
	// carry the failure status).
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	assertNotice(t, rec, "plugin.view.action_failed")
	if !strings.Contains(rec.Body.String(), `name="note" value="default note"`) {
		t.Error("an unreadable post must show the view as the plugin drew it, refilling nothing")
	}
}

func TestPluginView_NoJSReaskFailsShowsNoticeAlone_3879(t *testing.T) {
	h := newViewHarness(t)
	h.answerByEvent(failingAnswer, failingAnswer)
	rec := h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"save"}, "note": {"x"}}, false)
	assertUnavailable(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "<form class=\"card plugin-view-form\"") {
		t.Fatal("no view to show, yet a form was rendered")
	}
}

func TestPluginView_NoJSJobBusyRefillsForm_3879(t *testing.T) {
	h := newJobHarness(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	h.blockingJob(release)
	h.startJob(true)
	h.startJob(true)
	h.answerByEvent(fixedAnswer(refillDocument), fixedAnswer(`{"job":{"event":"`+viewJobEvent+`"}}`))
	form := url.Values{"_action": {"save"}, "_kind.on": {"toggle"}, "note": {"typed <b>"}, "mode": {"b"}, "api_key": {"s3cret"}}
	assertRefilledPage(t, h.do(http.MethodPost, "/plugin/views", form, false), http.StatusOK, "plugin.job.busy")
}
