package pages

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ADR-0121 S5 (ut-docs#4160): the asked-options fragment, its save rule,
// cache and failure policy, against a fake plugin on the shared bus.

const optionsManifest = `{"id":"p1","name":"Plugin","version":"%s","runtime":"none",
	"settings":[
	  {"key":"provider","type":"choice","options":[{"value":"a","label":"plugins.settings.save"},{"value":"b","label":"plugins.settings.permissions.granted"}]},
	  {"key":"model","type":"choice","options_ask":true,"depends_on":["provider"],"label":"plugins.settings.title"},
	  {"key":"pick","type":"choice","options_ask":true,"required":true},
	  {"key":"mode","type":"choice","options":[{"value":"x","label":"plugins.settings.save"}]},
	  {"key":"api_key","type":"secret"},
	  {"key":"items","type":"list","title_field":"f","max_items":5,"fields":[{"key":"f","type":"text"}]}
	]}`

type optionsFixture struct {
	v2Fixture
	mu      sync.Mutex
	asks    []settingsOptionsAsk
	rawAsks []string
	answer  func(settingsOptionsAsk) (json.RawMessage, error)
}

func (f *optionsFixture) askCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.asks)
}

func (f *optionsFixture) lastAsk(t *testing.T) settingsOptionsAsk {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.asks) == 0 {
		t.Fatal("no ask was sent")
	}
	return f.asks[len(f.asks)-1]
}

func (f *optionsFixture) answerWith(raw string) {
	f.mu.Lock()
	f.answer = func(settingsOptionsAsk) (json.RawMessage, error) { return json.RawMessage(raw), nil }
	f.mu.Unlock()
}

// setupOptionsPlugin installs optionsManifest for p1 with a fake plugin
// subscribed to ui.settings.options.ask (hook row + events:receive grant).
func setupOptionsPlugin(t *testing.T, grant bool) *optionsFixture {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	ctx := context.Background()
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	version, ok, err := data.NewPluginRepo(dp.Db).GetActivePluginVersion(ctx, "p1")
	if err != nil || !ok {
		t.Fatalf("p1 active version: %q %v %v", version, ok, err)
	}
	dir := paths.Plugins("p1", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(fmt.Sprintf(optionsManifest, version)), 0o644); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"provider": `"a"`, "model": `"stored-model"`, "pick": `"old"`, "mode": `"x"`,
		"api_key": `"sk-stored-secret"`, "items": `[{"f":"x"}]`,
	} {
		if err := data.NewPluginRepo(dp.Db).UpsertPluginSettingScoped(ctx, "p1", k, v, "global", k == "api_key"); err != nil {
			t.Fatal(err)
		}
	}
	if grant {
		if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('opt-er','p1','events:receive',1)`); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks(id,plugin_id,event,action,is_active) VALUES('opt-h','p1',?,'settings',1)`, settingsOptionsAskEvent); err != nil {
		t.Fatal(err)
	}
	f := &optionsFixture{v2Fixture: v2Fixture{mux: mux, dp: dp}}
	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	if _, err := bus.SubscribeWithHandler(t.Context(), "p1", []string{settingsOptionsAskEvent},
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			var a settingsOptionsAsk
			if err := json.Unmarshal(ev.Payload, &a); err != nil {
				return nil, err
			}
			f.mu.Lock()
			f.asks = append(f.asks, a)
			f.rawAsks = append(f.rawAsks, string(ev.Payload))
			answer := f.answer
			f.mu.Unlock()
			if answer == nil {
				return nil, nil
			}
			return answer(a)
		}); err != nil {
		t.Fatal(err)
	}
	origCache := sharedSettingsOptionsCache
	sharedSettingsOptionsCache = newSettingsOptionsCache(time.Now)
	origTimeout := settingsOptionsTimeout
	settingsOptionsTimeout = 300 * time.Millisecond
	t.Cleanup(func() { sharedSettingsOptionsCache = origCache; settingsOptionsTimeout = origTimeout })
	return f
}

func (f *optionsFixture) ask(t *testing.T, key string, form url.Values, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings/"+key+"/options", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

const twoModels = `{"options":[{"value":"m1","label":"Model One"},{"value":"m2","label":"<b>Big</b>"}]}`

// AC 6: the page response never waits for or sends an ask.
func TestSettingsOptions_GETDrawsWrapperAndAsksNothing(t *testing.T) {
	f := setupOptionsPlugin(t, true)
	f.answerWith(twoModels)
	body := f.get(t)
	for _, want := range []string{
		`data-ps-ask="model"`, `data-ps-ask-on="provider"`,
		`hx-post="/api/plugins/p1/settings/model/options"`, `hx-trigger="ps-reask"`,
		`id="ps-opts-msg-model"`, `role="status"`, `id="ps-f-model"`,
		`<option value="stored-model" selected dir="auto">stored-model</option>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET lacks %s", want)
		}
	}
	if strings.Contains(body, `data-ps-ask="mode"`) || strings.Contains(body, `data-ps-ask="provider"`) {
		t.Error("a static choice drew the ask wrapper")
	}
	if n := f.askCount(); n != 0 {
		t.Errorf("GET sent %d asks", n)
	}
}

// AC 1, 3, 4: one ask, payload {key, values} with the UNSAVED provider and
// no secret / list, even when typed; labels escaped.
func TestSettingsOptions_FragmentAsksWithUnsavedValuesAndEscapes(t *testing.T) {
	f := setupOptionsPlugin(t, true)
	f.answerWith(twoModels)
	rec := f.ask(t, "model", url.Values{
		"setting_provider": {"b"}, "setting_model": {"stored-model"},
		"setting_api_key": {"typed-secret-123"}, "setting_items": {`[{"f":"typed-list"}]`},
	}, nil)
	if rec.Code != 200 {
		t.Fatalf("code %d %s", rec.Code, rec.Body.String())
	}
	if f.askCount() != 1 {
		t.Fatalf("asks = %d", f.askCount())
	}
	a := f.lastAsk(t)
	if a.Key != "model" || a.Values["provider"] != "b" {
		t.Errorf("payload = %+v", a)
	}
	raw := f.rawAsks[0]
	for _, banned := range []string{"typed-secret-123", "sk-stored-secret", "typed-list", "api_key", `"items"`} {
		if strings.Contains(raw, banned) {
			t.Errorf("payload leaks %q: %s", banned, raw)
		}
	}
	body := rec.Body.String()
	if strings.Contains(body, "<b>Big</b>") || !strings.Contains(body, "&lt;b&gt;Big&lt;/b&gt;") {
		t.Errorf("label not escaped: %s", body)
	}
	if !strings.Contains(body, `value="m1"`) || !strings.Contains(body, `dir="auto"`) {
		t.Errorf("asked options missing: %s", body)
	}
	if !strings.Contains(body, `<option value="stored-model" selected`) {
		t.Errorf("stored value not kept selected: %s", body)
	}
	if i, j := strings.Index(body, `value="m1"`), strings.Index(body, `value="m2"`); i < 0 || j < i {
		t.Error("asked options not in answer order")
	}
	if !strings.Contains(body, `hx-swap-oob="innerHTML"`) || !strings.Contains(body, `id="ps-opts-msg-model"`) {
		t.Errorf("message line is not swapped out of band: %s", body)
	}
	if !strings.Contains(body, "isn&#39;t in the plugin&#39;s list") {
		t.Errorf("saved-missing hint absent: %s", body)
	}
}

// AC 8: cache hit / miss / save drops / failures not cached.
func TestSettingsOptions_Cache(t *testing.T) {
	f := setupOptionsPlugin(t, true)
	f.answerWith(twoModels)
	form := url.Values{"setting_provider": {"a"}}
	f.ask(t, "model", form, nil)
	f.ask(t, "model", form, nil)
	if f.askCount() != 1 {
		t.Fatalf("same values asked %d times", f.askCount())
	}
	f.ask(t, "model", url.Values{"setting_provider": {"b"}}, nil)
	if f.askCount() != 2 {
		t.Fatalf("different values: asks = %d", f.askCount())
	}
	// A save that changes a setting drops the plugin's entries.
	rec := f.post(t, url.Values{
		"setting_provider": {"a"}, "setting_model": {"stored-model"}, "setting_pick": {"old"},
		"setting_mode": {"x"}, "setting_items": {`[{"f":"x"}]`}, "setting_api_key": {"new-key"},
	}, true)
	if rec.Code != 200 {
		t.Fatalf("save: %d %s", rec.Code, rec.Body.String())
	}
	f.ask(t, "model", form, nil)
	if f.askCount() != 3 {
		t.Fatalf("after a save the list was not asked again: asks = %d", f.askCount())
	}
}

// AC 5, 6: timeout, error, panic, malformed: notice, stored kept, not cached.
func TestSettingsOptions_FailuresShowNoticeAndAreNotCached(t *testing.T) {
	cases := map[string]func(settingsOptionsAsk) (json.RawMessage, error){
		"timeout": func(settingsOptionsAsk) (json.RawMessage, error) {
			time.Sleep(2 * time.Second)
			return json.RawMessage(twoModels), nil
		},
		"error": func(settingsOptionsAsk) (json.RawMessage, error) { return nil, fmt.Errorf("offline") },
		"panic": func(settingsOptionsAsk) (json.RawMessage, error) { panic("boom") },
		"malformed": func(settingsOptionsAsk) (json.RawMessage, error) {
			return json.RawMessage(`{"options":[{"value":"a,b","label":"x"}]}`), nil
		},
	}
	for name, ans := range cases {
		t.Run(name, func(t *testing.T) {
			f := setupOptionsPlugin(t, true)
			f.mu.Lock()
			f.answer = ans
			f.mu.Unlock()
			start := time.Now()
			rec := f.ask(t, "model", url.Values{"setting_provider": {"a"}}, nil)
			if d := time.Since(start); d > 1500*time.Millisecond {
				t.Errorf("took %v, want about the 300ms timeout", d)
			}
			if rec.Code != 200 {
				t.Fatalf("code %d", rec.Code)
			}
			body := rec.Body.String()
			if !strings.Contains(body, "load the choices from the plugin") || !strings.Contains(body, "data-ps-ask-retry=\"model\"") {
				t.Errorf("no failure notice / retry: %s", body)
			}
			if !strings.Contains(body, `<option value="stored-model" selected`) {
				t.Errorf("stored value not selected: %s", body)
			}
			f.ask(t, "model", url.Values{"setting_provider": {"a"}}, nil)
			if f.askCount() != 2 {
				t.Errorf("a failure was cached: asks = %d", f.askCount())
			}
		})
	}
}

// AC 2, 7: declined, or no events:receive: empty hint, no failure notice,
// not cached; the refusal is audited.
func TestSettingsOptions_DeclinedAndDeniedAreEmptyNotFailures(t *testing.T) {
	t.Run("declined", func(t *testing.T) {
		f := setupOptionsPlugin(t, true) // answer nil = declines
		rec := f.ask(t, "model", url.Values{"setting_provider": {"a"}}, nil)
		body := rec.Body.String()
		if rec.Code != 200 || strings.Contains(body, "load the choices") || strings.Contains(body, "data-ps-ask-retry") {
			t.Errorf("declined showed a failure: %d %s", rec.Code, body)
		}
		if !strings.Contains(body, "no choices to offer yet") {
			t.Errorf("empty hint absent: %s", body)
		}
		f.ask(t, "model", url.Values{"setting_provider": {"a"}}, nil)
		if f.askCount() != 2 {
			t.Errorf("a declined ask was cached: asks = %d", f.askCount())
		}
	})
	t.Run("no events:receive", func(t *testing.T) {
		f := setupOptionsPlugin(t, false)
		f.answerWith(twoModels)
		rec := f.ask(t, "model", url.Values{"setting_provider": {"a"}}, nil)
		body := rec.Body.String()
		if rec.Code != 200 || strings.Contains(body, `value="m1"`) || strings.Contains(body, "load the choices") {
			t.Errorf("denied plugin: %d %s", rec.Code, body)
		}
		if f.askCount() != 0 {
			t.Error("the handler ran without events:receive")
		}
		var n int
		if err := f.dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'event_dispatch' AND data_json LIKE '%status=denied%' AND data_json LIKE '%plugin_id=p1%'`).Scan(&n); err != nil || n == 0 {
			t.Errorf("no denied dispatch audit row (n=%d, err=%v)", n, err)
		}
	})
}

// An empty answer is a real answer: cached.
func TestSettingsOptions_EmptyAnswerIsCached(t *testing.T) {
	f := setupOptionsPlugin(t, true)
	f.answerWith(`{"options":[]}`)
	for i := 0; i < 2; i++ {
		rec := f.ask(t, "model", url.Values{"setting_provider": {"a"}}, nil)
		if strings.Contains(rec.Body.String(), "load the choices") {
			t.Fatal("empty answer shown as failure")
		}
	}
	if f.askCount() != 1 {
		t.Errorf("asks = %d, want 1", f.askCount())
	}
}

// AC 18 (access): gate, same origin, not installed, unknown, static, corrupt.
func TestSettingsOptions_Refusals(t *testing.T) {
	t.Run("non-manager 403", func(t *testing.T) {
		mux, _ := newPluginSettingsTestDeps(t)
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings/model/options", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("code %d", rec.Code)
		}
	})
	f := setupOptionsPlugin(t, true)
	f.answerWith(twoModels)
	if rec := f.ask(t, "model", nil, map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: %d", rec.Code)
	}
	if rec := f.ask(t, "model", nil, map[string]string{"Origin": "https://evil.example"}); rec.Code != http.StatusForbidden {
		t.Errorf("foreign origin: %d", rec.Code)
	}
	for _, key := range []string{"mode", "provider", "nope", "api_key"} {
		if rec := f.ask(t, key, nil, nil); rec.Code != http.StatusNotFound {
			t.Errorf("key %s: %d, want 404", key, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/ghost/settings/model/options", nil)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("not installed: %d", rec.Code)
	}
	if n := f.askCount(); n != 0 {
		t.Errorf("a refused request still asked the plugin %d times", n)
	}
	// corrupt manifest: 500
	version, _, _ := data.NewPluginRepo(f.dp.Db).GetActivePluginVersion(context.Background(), "p1")
	if err := os.WriteFile(filepath.Join(paths.Plugins("p1", version), "manifest.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := f.ask(t, "model", nil, nil); rec.Code != http.StatusInternalServerError {
		t.Errorf("corrupt manifest: %d, want 500", rec.Code)
	}
	if f.askCount() != 0 {
		t.Error("asked with an unreadable manifest")
	}
}

// AC 9, 10: the save rule.
func TestSettingsOptions_SaveRule(t *testing.T) {
	f := setupOptionsPlugin(t, true)
	form := func(model string) url.Values {
		return url.Values{
			"setting_provider": {"a"}, "setting_model": {model}, "setting_pick": {"old"},
			"setting_mode": {"x"}, "setting_items": {`[{"f":"x"}]`},
		}
	}
	long := strings.Repeat("m", 64)
	if rec := f.post(t, form(long), true); rec.Code != 200 {
		t.Fatalf("64 bytes: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.raw(t, "model"); got != `"`+long+`"` {
		t.Errorf("stored %s", got)
	}
	for name, bad := range map[string]string{"65 bytes": strings.Repeat("m", 65), "comma": "a,b"} {
		before := f.atRest(t)
		rec := f.post(t, form(bad), true)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not one of the options") && !strings.Contains(rec.Body.String(), `aria-invalid="true"`) {
			t.Errorf("%s: %d", name, rec.Code)
		}
		if f.atRest(t) != before {
			t.Errorf("%s: a refused save wrote something", name)
		}
	}
	// required and empty
	bad := form(long)
	bad.Set("setting_pick", "")
	if rec := f.post(t, bad, true); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("required empty: %d", rec.Code)
	}
	// static choice still refuses outside values
	bad = form(long)
	bad.Set("setting_mode", "zzz")
	if rec := f.post(t, bad, true); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("static choice: %d", rec.Code)
	}
	// AC 10: a stored value no answer lists is kept and shown selected after a save
	f.answerWith(`{"options":[{"value":"other","label":"Other"}]}`)
	rec := f.ask(t, "model", url.Values{"setting_provider": {"a"}}, nil)
	if !strings.Contains(rec.Body.String(), `<option value="`+long+`" selected`) {
		t.Errorf("stored value dropped from the dropdown: %s", rec.Body.String())
	}
	if f.raw(t, "model") != `"`+long+`"` {
		t.Error("a later answer rewrote the stored value")
	}
}

// AC 14: a refused save keeps the operator's asked pick in the 422 form.
func TestSettingsOptions_422KeepsAskedPick(t *testing.T) {
	f := setupOptionsPlugin(t, true)
	rec := f.post(t, url.Values{
		"setting_provider": {"a"}, "setting_model": {"picked-model"}, "setting_pick": {""},
		"setting_mode": {"x"}, "setting_items": {`[{"f":"x"}]`},
	}, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `<option value="picked-model" selected`) {
		t.Errorf("pick lost on 422: %s", rec.Body.String())
	}
}

// The unsaved choice (not just the stored one) feeds show_when and the ask.
func TestSettingsOptions_CurrentPickPostedIsKept(t *testing.T) {
	f := setupOptionsPlugin(t, true)
	f.answerWith(twoModels)
	rec := f.ask(t, "model", url.Values{"setting_provider": {"a"}, "setting_model": {"m2"}}, nil)
	if !strings.Contains(rec.Body.String(), `<option value="m2" selected`) {
		t.Errorf("posted pick not selected: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "isn&#39;t in the plugin") {
		t.Error("saved_missing shown although the pick is listed")
	}
	// A tampered, over-long pick falls back to the stored value
	rec = f.ask(t, "model", url.Values{"setting_provider": {"a"}, "setting_model": {strings.Repeat("z", 80)}}, nil)
	if !strings.Contains(rec.Body.String(), `<option value="stored-model" selected`) {
		t.Errorf("stored fallback missing: %s", rec.Body.String())
	}
}
