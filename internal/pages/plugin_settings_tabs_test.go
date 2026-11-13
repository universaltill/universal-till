package pages

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/paths"
)

// ADR-0121 amendment "plugin settings form v2" S1 (ut-docs#4666): settings
// grouped into tabs on one form, a tab hidden by show_when is neither shown
// nor validated, a tab holding errors is flagged and opened, and a
// controlling setting that is itself hidden counts as its stored value.

const tabsManifest = `{"id":"p1","name":"Plugin","version":"%s","runtime":"none",
	"settings_tabs":[
	  {"id":"conn","label":"plugins.settings.permissions.title","help":"plugins.settings.permissions.help"},
	  {"id":"adv","label":"plugins.settings.title"},
	  {"id":"empty","label":"plugins.settings.none"},
	  {"id":"later","label":"plugins.settings.saved","show_when":{"key":"mode","in":["b"]}}
	],
	"settings":[
	  {"key":"name","type":"text"},
	  {"key":"mode","type":"choice","options":[{"value":"a","label":"plugins.settings.permissions.granted"},{"value":"b","label":"plugins.settings.permissions.not_granted"}]},
	  {"key":"extra","type":"text","show_when":{"key":"mode","in":["b"]}},
	  {"key":"host","type":"text","required":true,"tab":"conn"},
	  {"key":"port","type":"number","min":1,"max":99,"required":true,"tab":"conn"},
	  {"key":"gate","type":"text","tab":"adv"},
	  {"key":"deep","type":"text","required":true,"tab":"adv","show_when":{"key":"extra","in":["9"]}},
	  {"key":"later_note","type":"text","required":true,"tab":"later"}
	]}`

func setupManifestPlugin(t *testing.T, manifest string, seeds map[string]string) v2Fixture {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	version, ok, err := data.NewPluginRepo(dp.Db).GetActivePluginVersion(context.Background(), "p1")
	if err != nil || !ok {
		t.Fatalf("p1 active version: %q %v %v", version, ok, err)
	}
	dir := paths.Plugins("p1", version)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(fmt.Sprintf(manifest, version)), 0o644); err != nil {
		t.Fatal(err)
	}
	for k, v := range seeds {
		seedRawPluginSetting(t, dp, k, v)
	}
	return v2Fixture{mux: mux, dp: dp}
}

func setupTabsPlugin(t *testing.T, seeds map[string]string) v2Fixture {
	t.Helper()
	vals := map[string]string{
		"name": `"n"`, "mode": `"a"`, "extra": `"9"`, "host": `"h"`, "port": `5`,
		"gate": `"g"`, "deep": `"d"`, "later_note": `"x"`,
	}
	for k, v := range seeds {
		vals[k] = v
	}
	return setupManifestPlugin(t, tabsManifest, vals)
}

// tabSection returns one tab's whole <details> element ("" when absent).
func tabSection(body, id string) string {
	i := strings.Index(body, `id="ps-tab-`+id+`"`)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(body[:i], "<details")
	end := strings.Index(body[i:], "</details>")
	if start < 0 || end < 0 {
		return ""
	}
	return body[start : i+end+len("</details>")]
}

// tabOpenTag returns the <details …> start tag of a tab.
func tabOpenTag(body, id string) string {
	s := tabSection(body, id)
	if s == "" {
		return ""
	}
	return s[:strings.Index(s, ">")+1]
}

func formFragment(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `<form id="ps-form"`)
	j := strings.Index(body, "</form>")
	if i < 0 || j < i {
		t.Fatalf("no ps-form in %s", body)
	}
	return body[i : j+len("</form>")]
}

// A manifest without settings_tabs renders exactly the markup it did before
// tabs existed: no tab element, and the form is byte-identical to the golden.
func TestPluginSettingsTabs_NoTabsManifestUnchanged(t *testing.T) {
	_, thisFile, _, _ := runtime.Caller(0) // the tests chdir, so locate the golden by this file
	golden := filepath.Join(filepath.Dir(thisFile), "testdata", "plugin_settings_notab_form.golden.html")
	f := setupV2Plugin(t, nil)
	form := formFragment(t, f.get(t))
	for _, bad := range []string{"ps-tabs", "ps-tab", `role="tab`, "data-ps-"} {
		if strings.Contains(form, bad) {
			t.Fatalf("a manifest without settings_tabs drew tab markup %q", bad)
		}
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(golden, []byte(form), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if form != string(want) {
		t.Fatalf("no-tab form markup changed:\n--- got\n%s\n--- want\n%s", form, want)
	}
}

func TestPluginSettingsTabs_GroupedGeneralFirstEmptyNotDrawn(t *testing.T) {
	f := setupTabsPlugin(t, nil)
	body := f.get(t)
	form := formFragment(t, body)
	if n := strings.Count(form, `<form id="ps-form"`); n != 1 {
		t.Fatalf("%d forms", n)
	}
	if n := strings.Count(form, `type="submit"`); n != 1 {
		t.Fatalf("%d Save buttons, want 1 below all tabs", n)
	}
	if !strings.Contains(form, `data-ps-tabs`) {
		t.Fatal("no tab container")
	}
	gen, conn, adv, later := strings.Index(form, `id="ps-tab-_general"`), strings.Index(form, `id="ps-tab-conn"`), strings.Index(form, `id="ps-tab-adv"`), strings.Index(form, `id="ps-tab-later"`)
	if gen < 0 || !(gen < conn && conn < adv && adv < later) {
		t.Fatalf("tab order wrong: general %d conn %d adv %d later %d", gen, conn, adv, later)
	}
	if strings.Contains(form, `id="ps-tab-empty"`) {
		t.Fatal("a tab without settings must not be drawn")
	}
	if strings.Count(form, "<details") != 4 {
		t.Fatalf("want 4 tabs (general, conn, adv, later), got %d", strings.Count(form, "<details"))
	}
	genSec := tabSection(form, "_general")
	for _, k := range []string{"setting_name", "setting_mode", "setting_extra"} {
		if !strings.Contains(genSec, `name="`+k+`"`) {
			t.Fatalf("General lacks %s", k)
		}
	}
	if !(strings.Index(genSec, `name="setting_name"`) < strings.Index(genSec, `name="setting_mode"`) && strings.Index(genSec, `name="setting_mode"`) < strings.Index(genSec, `name="setting_extra"`)) {
		t.Fatal("fields must keep manifest order inside a tab")
	}
	if !strings.Contains(genSec, html.EscapeString(httpx.T("en", "plugins.settings.tab.general"))) || httpx.T("en", "plugins.settings.tab.general") == "plugins.settings.tab.general" {
		t.Fatal("General tab title missing")
	}
	connSec := tabSection(form, "conn")
	if !(strings.Index(connSec, `name="setting_host"`) > 0 && strings.Index(connSec, `name="setting_host"`) < strings.Index(connSec, `name="setting_port"`)) {
		t.Fatalf("conn fields wrong: %s", connSec)
	}
	if strings.Contains(connSec, `name="setting_gate"`) {
		t.Fatal("gate belongs to adv")
	}
	if !strings.Contains(connSec, "<summary") || !strings.Contains(connSec, html.EscapeString(httpx.T("en", "plugins.settings.permissions.title"))) {
		t.Fatal("tab title (summary) missing")
	}
	if !strings.Contains(connSec, html.EscapeString(httpx.T("en", "plugins.settings.permissions.help"))) {
		t.Fatal("tab help line missing in the panel")
	}
	if !strings.Contains(tabOpenTag(form, "conn"), " open") {
		t.Fatal("every tab renders expanded for no-JS")
	}
	if !strings.Contains(tabOpenTag(form, "_general"), "data-ps-active") || strings.Contains(tabOpenTag(form, "conn"), "data-ps-active") {
		t.Fatal("without errors the first visible tab is the active one")
	}
}

func TestPluginSettingsTabs_TabHiddenByShowWhen(t *testing.T) {
	f := setupTabsPlugin(t, nil) // mode = a
	tag := tabOpenTag(f.get(t), "later")
	if !strings.Contains(tag, `data-show-when-key="mode"`) || !strings.Contains(tag, `data-show-when-in="b"`) || !strings.Contains(tag, " hidden") {
		t.Fatalf("tab must render hidden with its condition while mode = a: %s", tag)
	}
	f = setupTabsPlugin(t, map[string]string{"mode": `"b"`})
	if tag := tabOpenTag(f.get(t), "later"); strings.Contains(tag, " hidden") || !strings.Contains(tag, "data-show-when-key") {
		t.Fatalf("tab must show while mode = b: %s", tag)
	}
}

func TestPluginSettingsTabs_ErrorTabCountAndOpenedAfter422(t *testing.T) {
	f := setupTabsPlugin(t, nil)
	form := url422Form()
	form.Set("setting_host", "")
	form.Set("setting_port", "0")
	rec := f.post(t, form, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code %d", rec.Code)
	}
	body := rec.Body.String()
	conn := tabSection(body, "conn")
	if !strings.Contains(conn, "ps-tab-errs") || !strings.Contains(conn, "⚠ 2") {
		t.Fatalf("conn title must carry an icon + count of 2: %s", conn)
	}
	if want := fmt.Sprintf(httpx.T("en", "plugins.settings.tab.errors"), 2); !strings.Contains(conn, `aria-label="`+html.EscapeString(want)+`"`) {
		t.Fatalf("the count needs an accessible label %q", want)
	}
	if strings.Contains(tabSection(body, "adv"), "ps-tab-errs") || strings.Contains(tabSection(body, "_general"), "ps-tab-errs") {
		t.Fatal("only the tab with errors is flagged")
	}
	if !strings.Contains(tabOpenTag(body, "conn"), "data-ps-active") || strings.Contains(tabOpenTag(body, "_general"), "data-ps-active") {
		t.Fatal("the first tab with errors is the one opened")
	}
	if !strings.Contains(body, "<details") || strings.Contains(body, "<html") {
		t.Fatal("an htmx 422 answers the tabbed form fragment")
	}
}

// url422Form is a complete valid submission of the tabs manifest.
func url422Form() url.Values {
	return url.Values{
		"setting_name": {"n"}, "setting_mode": {"a"}, "setting_extra": {"9"}, "setting_host": {"h"},
		"setting_port": {"5"}, "setting_gate": {"g"}, "setting_deep": {"d"}, "setting_later_note": {"x"},
	}
}

// A field in a hidden tab is neither validated nor written, like a field
// hidden by its own show_when.
func TestPluginSettingsTabs_HiddenTabNotValidatedNorWritten(t *testing.T) {
	f := setupTabsPlugin(t, map[string]string{"later_note": `""`})
	form := url422Form() // mode = a → tab "later" hidden; later_note required + blank
	form.Set("setting_later_note", "")
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("a required field in a hidden tab blocked the save: %d %s", rec.Code, rec.Body.String())
	}
	form.Set("setting_later_note", "typed")
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if got := f.raw(t, "later_note"); got != `""` {
		t.Fatalf("hidden-tab field written as %s", got)
	}
	// Switching the controller in the same submission shows the tab: validated, then written.
	form.Set("setting_mode", "b")
	form.Set("setting_later_note", "")
	if rec := f.post(t, form, true); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("visible tab must be validated, code %d", rec.Code)
	}
	form.Set("setting_later_note", "typed")
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("code %d", rec.Code)
	}
	if got := f.raw(t, "later_note"); got != `"typed"` {
		t.Fatalf("visible-tab field stored %s", got)
	}
}

// A controlling setting that is itself hidden counts as holding its STORED
// value, not the submitted one (deferred from #4157's review).
func TestPluginSettingsTabs_ChainedShowWhenUsesHiddenControllersStoredValue(t *testing.T) {
	// extra is hidden (mode = a). Stored extra = 9, so deep (shows when extra = 9) is visible.
	f := setupTabsPlugin(t, map[string]string{"deep": `""`})
	if tag := fieldDivTag(f.get(t), "deep"); strings.Contains(tag, " hidden") || !strings.Contains(tag, `data-show-when-stored="9"`) {
		t.Fatalf("deep must render visible, carrying the controller's stored value: %s", tag)
	}
	form := url422Form()
	form.Set("setting_extra", "3") // a hidden controller's submitted value must not count
	form.Set("setting_deep", "")
	rec := f.post(t, form, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), html.EscapeString(httpx.T("en", "plugins.settings.error.required"))) {
		t.Fatalf("deep is visible by the stored extra=9 and required: code %d", rec.Code)
	}
	// Stored extra = 1: deep hidden even though the (ignored) submission says 9.
	f = setupTabsPlugin(t, map[string]string{"extra": `"1"`, "deep": `""`})
	if tag := fieldDivTag(f.get(t), "deep"); !strings.Contains(tag, " hidden") {
		t.Fatalf("deep must render hidden by the stored extra=1: %s", tag)
	}
	form = url422Form()
	form.Set("setting_extra", "9")
	form.Set("setting_deep", "")
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("deep is hidden by the stored extra=1: code %d %s", rec.Code, rec.Body.String())
	}
}

// fieldDivTag returns the <div class="ps-field" …> start tag of a setting.
func fieldDivTag(body, key string) string {
	i := strings.Index(body, `name="setting_`+key+`"`)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(body[:i], `<div class="ps-field"`)
	if start < 0 {
		return ""
	}
	return body[start : start+strings.Index(body[start:], ">")+1]
}

// A refused save re-renders a chained field the way the save judged it: the
// hidden controller's submitted value must not reveal it (review M2).
func TestPluginSettingsTabs_ChainedShowWhenRerenderMatchesSave(t *testing.T) {
	f := setupTabsPlugin(t, map[string]string{"extra": `"1"`})
	form := url422Form()
	form.Set("setting_extra", "9") // extra is hidden (mode = a): ignored
	form.Set("setting_host", "")   // a real error, so the form re-renders
	rec := f.post(t, form, true)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("host is required: code %d", rec.Code)
	}
	if tag := fieldDivTag(rec.Body.String(), "deep"); !strings.Contains(tag, " hidden") {
		t.Fatalf("deep must re-render hidden by the stored extra=1: %s", tag)
	}
}

const tabControllerManifest = `{"id":"p1","name":"Plugin","version":"%s","runtime":"none",
	"settings_tabs":[{"id":"later","label":"plugins.settings.saved","show_when":{"key":"mode","in":["b"]}}],
	"settings":[
	  {"key":"mode","type":"choice","options":[{"value":"a","label":"plugins.settings.permissions.granted"},{"value":"b","label":"plugins.settings.permissions.not_granted"}]},
	  {"key":"ctl","type":"text","tab":"later"},
	  {"key":"dep","type":"text","required":true,"show_when":{"key":"ctl","in":["on"]}}
	]}`

// A controller hidden by its TAB (not its own show_when) also counts as its
// stored value, on render and on save (review M3).
func TestPluginSettingsTabs_ControllerHiddenByItsTabUsesStoredValue(t *testing.T) {
	f := setupManifestPlugin(t, tabControllerManifest, map[string]string{"mode": `"a"`, "ctl": `"off"`, "dep": `""`})
	if tag := fieldDivTag(f.get(t), "dep"); !strings.Contains(tag, " hidden") || !strings.Contains(tag, `data-show-when-stored="off"`) {
		t.Fatalf("dep must render hidden, carrying ctl's stored value: %s", tag)
	}
	form := url.Values{"setting_mode": {"a"}, "setting_ctl": {"on"}, "setting_dep": {""}}
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("ctl sits in a hidden tab, so its submitted 'on' is ignored and dep stays hidden: code %d %s", rec.Code, rec.Body.String())
	}
	f = setupManifestPlugin(t, tabControllerManifest, map[string]string{"mode": `"a"`, "ctl": `"on"`, "dep": `""`})
	form.Set("setting_ctl", "off")
	if rec := f.post(t, form, true); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ctl's stored 'on' shows dep, which is required: code %d", rec.Code)
	}
}
