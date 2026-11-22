package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ADR-0121 S5 (ut-docs#5961): the Test connection flow against a fake
// plugin on the shared bus: save first, ask {test} only, strict answer,
// ADR-0041 skip failure policy.

const connManifest = `{"id":"p1","name":"Plugin","version":"%s","runtime":"none",
	"settings_tabs":[{"id":"main","label":"plugins.settings.title"},{"id":"adv","label":"plugins.settings.save"}],
	"settings":[
	  {"key":"shop_note","type":"text","required":true,"tab":"main"},
	  {"key":"mode","type":"choice","tab":"main","options":[{"value":"a","label":"plugins.settings.save"},{"value":"b","label":"plugins.settings.permissions.granted"}]},
	  {"key":"api_key","type":"secret","tab":"main"},
	  {"key":"token","type":"text","tab":"main"},
	  {"key":"adv_field","type":"text","tab":"adv"},
	  {"key":"hid","type":"text","tab":"main","show_when":{"key":"mode","in":["b"]}},
	  {"key":"items","type":"list","title_field":"f","max_items":5,"fields":[{"key":"f","type":"text"}]}
	],
	"settings_tests":[
	  {"id":"conn","label":"plugins.settings.save"},
	  {"id":"rec","label":"plugins.settings.save","record":true}
	]}`

type connFixture struct {
	v2Fixture
	mu      sync.Mutex
	raws    []string
	answer  func() (json.RawMessage, error)
	panicky bool
	block   time.Duration
}

func (f *connFixture) askCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.raws)
}

func (f *connFixture) answerWith(raw string) {
	f.mu.Lock()
	f.answer = func() (json.RawMessage, error) { return json.RawMessage(raw), nil }
	f.mu.Unlock()
}

// setupConnPlugin installs connManifest for p1. hook / grant control whether
// the plugin is subscribed to the ask (hook row) and holds events:receive.
func setupConnPlugin(t *testing.T, hook, grant bool) *connFixture {
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
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(fmt.Sprintf(connManifest, version)), 0o644); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{
		"shop_note": `"hello"`, "mode": `"a"`, "api_key": `"SENTINEL-2"`, "token": `"tok-stored"`,
		"adv_field": `"adv"`, "hid": `"hidden-val"`, "items": `[{"f":"x"}]`,
	} {
		if err := data.NewPluginRepo(dp.Db).UpsertPluginSettingScoped(ctx, "p1", k, v, "global", k == "api_key"); err != nil {
			t.Fatal(err)
		}
	}
	if grant {
		if _, err := dp.Db.Exec(`INSERT INTO plugin_permissions(id,plugin_id,permission,granted) VALUES('ct-er','p1','events:receive',1)`); err != nil {
			t.Fatal(err)
		}
	}
	if hook {
		if _, err := dp.Db.Exec(`INSERT INTO plugin_hooks(id,plugin_id,event,action,is_active) VALUES('ct-h','p1',?,'settings',1)`, settingsTestAskEvent); err != nil {
			t.Fatal(err)
		}
	}
	f := &connFixture{v2Fixture: v2Fixture{mux: mux, dp: dp}}
	bus := plugins.SharedBus(dp.Db)
	bus.ResetSubscribers()
	t.Cleanup(bus.ResetSubscribers)
	subscribe := func(h plugins.EventHandler) error {
		if !hook {
			return nil // no hook row: the bus refuses a subscription, nobody listens
		}
		_, err := bus.SubscribeWithHandler(t.Context(), "p1", []string{settingsTestAskEvent}, h)
		return err
	}
	if err := subscribe(
		func(ctx context.Context, ev plugins.Event) (json.RawMessage, error) {
			f.mu.Lock()
			f.raws = append(f.raws, string(ev.Payload))
			answer, panicky, block := f.answer, f.panicky, f.block
			f.mu.Unlock()
			if panicky {
				panic("plugin crashed")
			}
			if block > 0 {
				time.Sleep(block)
			}
			if answer == nil {
				return nil, nil
			}
			return answer()
		}); err != nil {
		t.Fatal(err)
	}
	origKeys := pluginOwnLocaleKeys
	pluginOwnLocaleKeys = func(_ *common.Deps, id string) map[string]bool {
		if id == "p1" {
			return map[string]bool{"x.ok": true, "x.bad": true}
		}
		return nil
	}
	origTimeout := settingsTestTimeout
	settingsTestTimeout = 300 * time.Millisecond
	t.Cleanup(func() { pluginOwnLocaleKeys = origKeys; settingsTestTimeout = origTimeout })
	return f
}

func connForm() url.Values {
	return url.Values{
		"setting_shop_note": {"hello"}, "setting_mode": {"a"}, "setting_api_key": {""},
		"setting_token": {"tok-stored"}, "setting_adv_field": {"adv"}, "setting_hid": {"hidden-val"},
	}
}

func (f *connFixture) run(t *testing.T, test string, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings/tests/"+test+"/run", nil)
	req.Header.Set("HX-Request", "true")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

func (f *connFixture) lastRaw(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.raws) == 0 {
		t.Fatal("no ask was sent")
	}
	return f.raws[len(f.raws)-1]
}

const couldntRun = "Couldn't run the test"

func hasUnavailable(body string) bool {
	return strings.Contains(body, "Couldn&#39;t run the test") || strings.Contains(body, couldntRun)
}

// AC1: buttons drawn from the manifest, not for record tests; a manifest
// without settings_tests renders no test markup at all.
func TestSettingsTest_FormDrawsNonRecordButtons(t *testing.T) {
	f := setupConnPlugin(t, true, true)
	body := f.get(t)
	if !strings.Contains(body, `name="ps_test" value="conn"`) {
		t.Errorf("no Test button for conn:\n%s", body)
	}
	if strings.Contains(body, `value="rec"`) {
		t.Error("a record test must not be drawn on the page form")
	}
	if !strings.Contains(body, `id="ps-test-conn"`) || !strings.Contains(body, `role="status"`) {
		t.Error("no status line for conn")
	}
	if strings.Contains(body, `class="ps-test-run"`) {
		t.Error("a plain GET must not auto-run a test")
	}
	if f.askCount() != 0 {
		t.Error("a GET asked the plugin")
	}
	if strings.Index(body, `class="btn primary" type="submit"`) > strings.Index(body, `name="ps_test"`) {
		t.Error("Save must come before the Test button in the DOM (Enter in a field means Save)")
	}
}

func TestSettingsTest_NoTestsRendersNothingNew(t *testing.T) {
	f := setupV2Plugin(t, nil)
	body := f.get(t)
	for _, frag := range []string{"ps_test", "ps-test", "ps-tests"} {
		// the page-level script may mention the class; only markup attributes count
		if strings.Contains(body, `class="`+frag) || strings.Contains(body, `name="`+frag) || strings.Contains(body, `id="`+frag) {
			t.Errorf("manifest without settings_tests draws %q", frag)
		}
	}
	if strings.Contains(body, `hx-sync="this:drop"`) {
		t.Error("form gained hx-sync without tests")
	}
}

// A successful save with ps_test renders the auto-run line; the ask is a
// separate request, so the save response itself never asks.
func TestSettingsTest_SaveSuccessRendersAutoRunLine(t *testing.T) {
	f := setupConnPlugin(t, true, true)
	form := connForm()
	form.Set("setting_shop_note", "changed")
	form.Set("ps_test", "conn")
	rec := f.post(t, form, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("code %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-post="/api/plugins/p1/settings/tests/conn/run"`) || !strings.Contains(body, `hx-trigger="load"`) || !strings.Contains(body, `hx-params="none"`) {
		t.Errorf("no auto-run line with hx-params=none:\n%s", body)
	}
	if got := f.raw(t, "shop_note"); got != `"changed"` {
		t.Errorf("save did not happen first: %s", got)
	}
	if n := f.auditRows(t); n != 1 {
		t.Errorf("audit rows = %d, want 1", n)
	}
	if f.askCount() != 0 {
		t.Error("the save response must not ask; the run request does")
	}
}

func TestSettingsTest_ZeroChangedKeysStillRuns(t *testing.T) {
	f := setupConnPlugin(t, true, true)
	form := connForm()
	form.Set("ps_test", "conn")
	rec := f.post(t, form, true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `hx-trigger="load"`) {
		t.Fatalf("a save with nothing changed must still test: %d", rec.Code)
	}
}

// AC3: a refused save sends no test and writes nothing.
func TestSettingsTest_RefusedSaveDoesNotRun(t *testing.T) {
	f := setupConnPlugin(t, true, true)
	before := f.atRest(t)
	form := connForm()
	form.Set("setting_shop_note", "") // required
	form.Set("ps_test", "conn")
	rec := f.post(t, form, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `hx-trigger="load"`) {
		t.Errorf("a refused save must not carry a run trigger:\n%s", body)
	}
	if !strings.Contains(body, v2en("plugins.settings.test.not_run")) {
		t.Errorf("422 must say the test was not run:\n%s", body)
	}
	if f.askCount() != 0 {
		t.Error("asked the plugin after a refused save")
	}
	if f.atRest(t) != before {
		t.Error("a refused save changed the stored settings")
	}
}

// ps_test is not a setting; unknown or record ids are a plain save.
func TestSettingsTest_UnknownOrRecordPsTestIsPlainSave(t *testing.T) {
	for _, id := range []string{"nope", "rec", ""} {
		f := setupConnPlugin(t, true, true)
		form := connForm()
		form.Set("ps_test", id)
		rec := f.post(t, form, true)
		if rec.Code != http.StatusOK {
			t.Fatalf("%q: code %d", id, rec.Code)
		}
		if strings.Contains(rec.Body.String(), `hx-trigger="load"`) {
			t.Errorf("%q: ran a test", id)
		}
		if f.auditRows(t) != 1 {
			t.Errorf("%q: not a normal save", id)
		}
	}
}

// No-JS save: redirect as today, no test.
func TestSettingsTest_NoJSSaveRedirectsWithoutTest(t *testing.T) {
	f := setupConnPlugin(t, true, true)
	form := connForm()
	form.Set("ps_test", "conn")
	rec := f.post(t, form, false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code %d, want 303", rec.Code)
	}
	if f.askCount() != 0 {
		t.Error("no-JS path asked")
	}
}

var settingKeyRe = regexp.MustCompile(`"(shop_note|mode|api_key|token|adv_field|hid|items)"`)

// AC4: the ask carries only {test}: no setting key, no value, no secret.
func TestSettingsTest_PayloadIsOnlyTheTestID(t *testing.T) {
	f := setupConnPlugin(t, true, true)
	f.answerWith(`{"ok":true}`)
	form := connForm()
	form.Set("setting_api_key", "SENTINEL-1")
	form.Set("ps_test", "conn")
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("save: %d", rec.Code)
	}
	// The run request carries no form (hx-params=none) — but even when a
	// client sends the whole form, nothing of it may reach the plugin.
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings/tests/conn/run", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("run: %d %s", rec.Code, rec.Body.String())
	}
	raw := f.lastRaw(t)
	if raw != `{"test":"conn"}` {
		t.Errorf("payload = %s, want exactly {\"test\":\"conn\"}", raw)
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var p struct {
		Test string `json:"test"`
	}
	if err := dec.Decode(&p); err != nil || p.Test != "conn" {
		t.Errorf("decode strict: %v %+v", err, p)
	}
	for _, bad := range []string{"SENTINEL-1", "SENTINEL-2", "tok-stored", "hello", "hidden-val"} {
		if strings.Contains(raw, bad) {
			t.Errorf("payload leaks %q", bad)
		}
	}
	if settingKeyRe.MatchString(raw) {
		t.Errorf("payload names a setting key: %s", raw)
	}
	if strings.Contains(rec.Body.String(), "SENTINEL") {
		t.Error("response echoes a secret")
	}
	if f.askCount() != 1 {
		t.Errorf("asks = %d, want exactly 1", f.askCount())
	}
	var rows int
	_ = f.dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'plugin_settings_saved'`).Scan(&rows)
	if rows != 1 {
		t.Errorf("audit rows = %d: the run request itself must not audit", rows)
	}
}

// AC5, AC6, AC9b: answers.
func TestSettingsTest_Answers(t *testing.T) {
	cases := []struct {
		name       string
		answer     string
		wantText   []string
		notText    []string
		wantField  string
		noField    bool
		wantStatus string
	}{
		{"ok", `{"ok":true}`, []string{"Connected"}, []string{couldntRun}, "", true, "ok"},
		{"ok own message", `{"ok":true,"message":"x.ok"}`, []string{"x.ok"}, []string{"Connected"}, "", true, "ok"},
		{"failed generic", `{"ok":false}`, []string{"The test failed"}, nil, "", true, "failed"},
		{"failed own message and field", `{"ok":false,"message":"x.bad","field":"api_key"}`, []string{"x.bad"}, []string{"The test failed"}, "api_key", false, "failed"},
		{"core key message dropped", `{"ok":false,"message":"plugins.settings.saved"}`, []string{"The test failed"}, []string{v2en("plugins.settings.saved")}, "", true, "failed"},
		{"unknown key message dropped", `{"ok":true,"message":"not.a.key"}`, []string{"Connected"}, []string{"not.a.key"}, "", true, "ok"},
		{"undeclared field ignored", `{"ok":false,"message":"x.bad","field":"nope"}`, []string{"x.bad"}, nil, "", true, "failed"},
		{"hidden field ignored", `{"ok":false,"field":"hid"}`, []string{"The test failed"}, nil, "", true, "failed"},
		{"list field ignored", `{"ok":false,"field":"items"}`, []string{"The test failed"}, nil, "", true, "failed"},
		{"field on ok ignored", `{"ok":true,"field":"api_key"}`, []string{"Connected"}, nil, "", true, "ok"},
		{"malformed unknown field", `{"ok":true,"x":1}`, []string{couldntRun}, nil, "", true, "unavailable"},
		{"malformed ok missing", `{"message":"x.ok"}`, []string{couldntRun}, nil, "", true, "unavailable"},
		{"malformed ok string", `{"ok":"yes"}`, []string{couldntRun}, nil, "", true, "unavailable"},
		{"malformed trailing", `{"ok":true} {}`, []string{couldntRun}, nil, "", true, "unavailable"},
		{"malformed array", `[]`, []string{couldntRun}, nil, "", true, "unavailable"},
		{"malformed over 4 KiB", `{"ok":true,"message":"` + strings.Repeat("a", 5000) + `"}`, []string{couldntRun}, nil, "", true, "unavailable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := setupConnPlugin(t, true, true)
			f.answerWith(c.answer)
			rec := f.run(t, "conn", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("code %d (a plugin failure is content, not an HTTP error)", rec.Code)
			}
			body := rec.Body.String()
			unesc := strings.ReplaceAll(body, "&#39;", "'")
			for _, w := range c.wantText {
				if !strings.Contains(unesc, w) {
					t.Errorf("missing %q in:\n%s", w, body)
				}
			}
			for _, n := range c.notText {
				if strings.Contains(unesc, n) {
					t.Errorf("unexpected %q in:\n%s", n, body)
				}
			}
			hasField := strings.Contains(body, `data-ps-test-field="`)
			if c.noField && hasField {
				t.Errorf("no field should be marked:\n%s", body)
			}
			if !c.noField && !strings.Contains(body, `data-ps-test-field="`+c.wantField+`"`) {
				t.Errorf("field %q not marked:\n%s", c.wantField, body)
			}
			if (c.wantStatus == "unavailable") != strings.Contains(body, "ps-test-retry-conn") {
				t.Errorf("Try again button presence wrong for %s:\n%s", c.wantStatus, body)
			}
			if strings.Contains(body, "hx-trigger=\"load\"") {
				t.Error("the result must not re-trigger itself")
			}
		})
	}
}

// AC7: a blocked plugin answers within the bound; the saved value stays.
func TestSettingsTest_TimeoutIsUnavailable(t *testing.T) {
	f := setupConnPlugin(t, true, true)
	f.block = 2 * time.Second
	settingsTestTimeout = 50 * time.Millisecond
	form := connForm()
	form.Set("setting_shop_note", "kept")
	form.Set("ps_test", "conn")
	f.post(t, form, true)
	start := time.Now()
	rec := f.run(t, "conn", nil)
	if d := time.Since(start); d > time.Second {
		t.Errorf("took %v, want within the bound", d)
	}
	if rec.Code != http.StatusOK || !hasUnavailable(rec.Body.String()) || !strings.Contains(rec.Body.String(), "Try again") {
		t.Errorf("want couldn't-run + Try again, got %d %s", rec.Code, rec.Body.String())
	}
	if got := f.raw(t, "shop_note"); got != `"kept"` {
		t.Errorf("saved value rolled back: %s", got)
	}
}

// AC8: a panicking handler.
func TestSettingsTest_PanicIsUnavailable(t *testing.T) {
	f := setupConnPlugin(t, true, true)
	f.panicky = true
	rec := f.run(t, "conn", nil)
	if rec.Code != http.StatusOK || !hasUnavailable(rec.Body.String()) {
		t.Fatalf("panic: %d %s", rec.Code, rec.Body.String())
	}
	// The process is alive: the page still serves.
	if body := f.get(t); !strings.Contains(body, "ps-test-conn") {
		t.Error("page broken after a crashed test")
	}
}

// AC9a: no answer — no hook, no events:receive, a declined (nil) answer.
func TestSettingsTest_NoAnswerIsUnavailable(t *testing.T) {
	for _, c := range []struct {
		name        string
		hook, grant bool
	}{{"no hook", false, true}, {"no events:receive", true, false}, {"declined", true, true}} {
		t.Run(c.name, func(t *testing.T) {
			f := setupConnPlugin(t, c.hook, c.grant)
			rec := f.run(t, "conn", nil)
			if rec.Code != http.StatusOK || !hasUnavailable(rec.Body.String()) {
				t.Fatalf("%d %s", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "Connected") {
				t.Error("no answer must never read as success")
			}
		})
	}
}

// AC11: access and refusals; none of them ask.
func TestSettingsTest_Refusals(t *testing.T) {
	t.Run("non-manager 403", func(t *testing.T) {
		mux, _ := newPluginSettingsTestDeps(t)
		req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings/tests/conn/run", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("code %d", rec.Code)
		}
	})
	f := setupConnPlugin(t, true, true)
	f.answerWith(`{"ok":true}`)
	if rec := f.run(t, "conn", map[string]string{"Sec-Fetch-Site": "cross-site"}); rec.Code != http.StatusForbidden {
		t.Errorf("cross-site: %d", rec.Code)
	}
	if rec := f.run(t, "conn", map[string]string{"Origin": "https://evil.example"}); rec.Code != http.StatusForbidden {
		t.Errorf("foreign origin: %d", rec.Code)
	}
	for _, id := range []string{"rec", "nope"} {
		if rec := f.run(t, id, nil); rec.Code != http.StatusNotFound {
			t.Errorf("test %s: %d, want 404", id, rec.Code)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/ghost/settings/tests/conn/run", nil)
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("not installed: %d", rec.Code)
	}
	if n := f.askCount(); n != 0 {
		t.Errorf("a refused request still asked the plugin %d times", n)
	}
	// Unresolved manifest: fail closed, 500, no ask.
	version, _, _ := data.NewPluginRepo(f.dp.Db).GetActivePluginVersion(context.Background(), "p1")
	if err := os.WriteFile(filepath.Join(paths.Plugins("p1", version), "manifest.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := f.run(t, "conn", nil); rec.Code != http.StatusInternalServerError {
		t.Errorf("corrupt manifest: %d, want 500", rec.Code)
	}
	if f.askCount() != 0 {
		t.Error("asked with an unreadable manifest")
	}
}

// The run request never writes: no audit row, no setting change.
func TestSettingsTest_RunWritesNothing(t *testing.T) {
	f := setupConnPlugin(t, true, true)
	f.answerWith(`{"ok":false,"message":"x.bad","field":"api_key"}`)
	before := f.atRest(t)
	f.run(t, "conn", nil)
	if f.atRest(t) != before {
		t.Error("a test outcome altered stored settings")
	}
	if f.auditRows(t) != 0 {
		t.Error("a test wrote an audit row")
	}
}
