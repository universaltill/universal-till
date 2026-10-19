package pages

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
)

// ADR-0121 amendment "plugin settings form v2" S2 (ut-docs#4157): each
// declared field kind draws its own control and stores a fixed value_json
// shape; every visible field is validated server-side on every save; a
// failed save answers 422 with the re-rendered form and writes nothing.

const v2Manifest = `{"id":"p1","name":"Plugin","version":"%s","runtime":"none",
	"settings":[
	  {"key":"enabled","type":"bool","label":"plugins.settings.save"},
	  {"key":"max_items","type":"number","min":1,"max":50,"required":true},
	  {"key":"discount","type":"percent","min":0,"max":5000},
	  {"key":"fee","type":"money","max":10000},
	  {"key":"shop_note","type":"text","max_len":10,"placeholder":"plugins.settings.per_till"},
	  {"key":"mode","type":"choice","options":[{"value":"a","label":"plugins.settings.permissions.granted"},{"value":"b","label":"plugins.settings.permissions.not_granted"}]},
	  {"key":"extra","type":"number","required":true,"show_when":{"key":"mode","in":["b"]}},
	  {"key":"token","type":"secret"},
	  {"key":"erp_url","type":"endpoint"}
	]}`

type v2Fixture struct {
	mux *http.ServeMux
	dp  *common.Deps
}

func seedRawPluginSetting(t *testing.T, dp *common.Deps, key, raw string) {
	t.Helper()
	if err := data.NewPluginRepo(dp.Db).UpsertPluginSettingScoped(context.Background(), "p1", key, raw, "global", key == "token"); err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
}

// setupV2Plugin installs v2Manifest for p1 and seeds every key with the
// given raw value_json (defaults below, overridden by seeds).
func setupV2Plugin(t *testing.T, seeds map[string]string) v2Fixture {
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
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(fmt.Sprintf(v2Manifest, version)), 0o644); err != nil {
		t.Fatal(err)
	}
	vals := map[string]string{
		"enabled": `true`, "max_items": `5`, "discount": `750`, "fee": `1250`,
		"shop_note": `"hello"`, "mode": `"a"`, "extra": `""`, "token": `"sk-stored-secret"`, "erp_url": `"https://erp.lan/api"`,
	}
	for k, v := range seeds {
		vals[k] = v
	}
	for k, v := range vals {
		seedRawPluginSetting(t, dp, k, v)
	}
	return v2Fixture{mux: mux, dp: dp}
}

func (f v2Fixture) get(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/plugins/p1/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: code %d body %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func (f v2Fixture) post(t *testing.T, form url.Values, htmx bool) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if htmx {
		req.Header.Set("HX-Request", "true")
	}
	rec := httptest.NewRecorder()
	f.mux.ServeHTTP(rec, req)
	return rec
}

// raw returns a key's stored value_json (opened if sealed).
func (f v2Fixture) raw(t *testing.T, key string) string {
	t.Helper()
	got, found, err := data.NewPluginRepo(f.dp.Db).GetPluginSetting(context.Background(), "p1", key)
	if err != nil || !found {
		t.Fatalf("GetPluginSetting(%s) = %q %v %v", key, got, found, err)
	}
	return got
}

// atRest returns every p1 row's raw value_json and updated_at, so a
// refused save can be proven to have written nothing at all.
func (f v2Fixture) atRest(t *testing.T) string {
	t.Helper()
	rows, err := f.dp.Db.Query(`SELECT key, scope, value_json, updated_at FROM plugin_settings WHERE plugin_id = 'p1' ORDER BY key, scope`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var k, s, v, u string
		if err := rows.Scan(&k, &s, &v, &u); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s|%s|%s|%s\n", k, s, v, u)
	}
	return b.String()
}

func (f v2Fixture) auditRows(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'plugin_settings_saved'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// validV2Form is a complete, valid submission matching the default seeds.
func validV2Form() url.Values {
	return url.Values{
		"setting_enabled": {"1"}, "present_setting_enabled": {"1"},
		"setting_max_items": {"5"}, "setting_discount": {"7.50"}, "setting_fee": {"12.50"},
		"setting_shop_note": {"hello"}, "setting_mode": {"a"}, "setting_extra": {""},
		"setting_token": {""}, "setting_erp_url": {"https://erp.lan/api"},
	}
}

// inputTag returns the whole <input …> tag carrying name="<name>".
func inputTag(t *testing.T, body, name string) string {
	t.Helper()
	i := strings.Index(body, `name="`+name+`"`)
	if i < 0 {
		t.Fatalf("no control named %s in %s", name, body)
	}
	start := strings.LastIndex(body[:i], "<")
	end := i + strings.Index(body[i:], ">")
	return body[start : end+1]
}

func v2en(k string) string { return httpx.T("en", k) }

func TestPluginSettingsV2_GET_RendersEachKind(t *testing.T) {
	f := setupV2Plugin(t, nil)
	body := f.get(t)

	// bool: a real checkbox styled as a switch, plus the presence marker
	// that makes "unchecked" mean false.
	if tag := inputTag(t, body, "setting_enabled"); !strings.Contains(tag, `type="checkbox"`) || !strings.Contains(tag, `value="1"`) || !strings.Contains(tag, " checked") {
		t.Fatalf("bool must be a checked checkbox, got %s", tag)
	}
	if !strings.Contains(body, `<input type="hidden" name="present_setting_enabled" value="1">`) {
		t.Fatal("bool needs its presence marker")
	}
	if !strings.Contains(body, `class="ps-switch"`) {
		t.Fatal("bool must be drawn as a switch")
	}
	// number
	if tag := inputTag(t, body, "setting_max_items"); !strings.Contains(tag, `inputmode="numeric"`) || !strings.Contains(tag, `value="5"`) || !strings.Contains(tag, " required") {
		t.Fatalf("number input wrong: %s", tag)
	}
	// percent: bps shown as a localized percent with a % suffix.
	if tag := inputTag(t, body, "setting_discount"); !strings.Contains(tag, `inputmode="decimal"`) || !strings.Contains(tag, `value="7.50"`) || !strings.Contains(tag, `data-money-local="percent"`) {
		t.Fatalf("percent input wrong: %s", tag)
	}
	if !strings.Contains(body, `<span class="ps-suffix" aria-hidden="true">%</span>`) {
		t.Fatal("percent needs its % suffix")
	}
	// money: minor units shown as major, with the shop currency.
	if tag := inputTag(t, body, "setting_fee"); !strings.Contains(tag, `inputmode="decimal"`) || !strings.Contains(tag, `value="12.50"`) || !strings.Contains(tag, `data-money-local`) {
		t.Fatalf("money input wrong: %s", tag)
	}
	if !strings.Contains(body, `<span class="ps-suffix">GBP</span>`) {
		t.Fatal("money must show the shop currency code")
	}
	// text: maxlength + translated placeholder.
	if tag := inputTag(t, body, "setting_shop_note"); !strings.Contains(tag, `maxlength="10"`) || !strings.Contains(tag, `placeholder="`+v2en("plugins.settings.per_till")+`"`) || !strings.Contains(tag, `value="hello"`) {
		t.Fatalf("text input wrong: %s", tag)
	}
	// The form posts without JS too, and targets itself under htmx.
	if !strings.Contains(body, `<form id="ps-form" novalidate action="/api/plugins/p1/settings" method="post" hx-post="/api/plugins/p1/settings" hx-target="this" hx-swap="outerHTML"`) {
		t.Fatalf("form must post without JS and swap itself under htmx, got %s", body)
	}
	if strings.Contains(body, "sk-stored-secret") {
		t.Fatal("a secret's value must never reach the page")
	}
}

func TestPluginSettingsV2_POST_StoresTypedShapes(t *testing.T) {
	f := setupV2Plugin(t, nil)
	form := validV2Form()
	form.Del("setting_enabled") // unchecked switch
	form.Set("setting_max_items", "12")
	form.Set("setting_discount", "7,5")
	form.Set("setting_fee", "3.5")
	form.Set("setting_shop_note", "hi there")
	form.Set("setting_mode", "b")
	form.Set("setting_extra", "3")
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("POST: code %d body %s", rec.Code, rec.Body.String())
	}
	for key, want := range map[string]string{
		"enabled": `false`, "max_items": `12`, "discount": `750`, "fee": `350`,
		"shop_note": `"hi there"`, "mode": `"b"`, "extra": `3`, "token": `"sk-stored-secret"`,
	} {
		if got := f.raw(t, key); got != want {
			t.Errorf("%s stored %s, want %s", key, got, want)
		}
	}
	form.Set("setting_enabled", "1")
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("POST: code %d body %s", rec.Code, rec.Body.String())
	}
	if got := f.raw(t, "enabled"); got != `true` {
		t.Fatalf("checked switch stored %s, want true", got)
	}
	// Without the presence marker (an older form) an absent checkbox is
	// left alone, never read as "off".
	form.Del("setting_enabled")
	form.Del("present_setting_enabled")
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("POST: code %d body %s", rec.Code, rec.Body.String())
	}
	if got := f.raw(t, "enabled"); got != `true` {
		t.Fatalf("absent checkbox without its marker stored %s, want true kept", got)
	}
}

func TestPluginSettingsV2_POST_ValidationFailsWith422AndWritesNothing(t *testing.T) {
	cases := []struct {
		field, value, msg string
	}{
		{"max_items", "", v2en("plugins.settings.error.required")},
		{"max_items", "abc", v2en("plugins.settings.error.number")},
		{"max_items", "1.5", v2en("plugins.settings.error.number")},
		{"max_items", "0", fmt.Sprintf(v2en("plugins.settings.error.min"), "1")},
		{"max_items", "51", fmt.Sprintf(v2en("plugins.settings.error.max"), "50")},
		{"discount", "50.01", fmt.Sprintf(v2en("plugins.settings.error.max"), httpx.FormatPercentBP(5000, "en"))},
		{"discount", "1e3", v2en("plugins.settings.error.number")},
		{"fee", "100.01", fmt.Sprintf(v2en("plugins.settings.error.max"), httpx.FormatMoney(10000, "en"))},
		// Above 2^53 minor units: refused at parse, the bound the read side
		// applies, so a saved amount never reloads as invalid (review).
		{"fee", "99999999999999.99", v2en("plugins.settings.error.number")},
		{"shop_note", "12345678901", fmt.Sprintf(v2en("plugins.settings.error.too_long"), "10")},
		{"mode", "c", v2en("plugins.settings.error.choice")},
		{"erp_url", "ftp://x", v2en("plugins.settings.error.url")},
	}
	for _, c := range cases {
		t.Run(c.field+"="+c.value, func(t *testing.T) {
			if strings.HasPrefix(c.msg, "plugins.settings.") {
				t.Fatalf("missing en.json key for %q", c.msg)
			}
			f := setupV2Plugin(t, nil)
			before := f.atRest(t)
			form := validV2Form()
			form.Set("setting_shop_note", "changed") // a valid sibling edit
			form.Set("setting_token", "sk-typed-new-secret")
			form.Set("setting_"+c.field, c.value)
			rec := f.post(t, form, true)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("code %d, want 422; body %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			if !strings.Contains(body, html.EscapeString(c.msg)) {
				t.Fatalf("body lacks the message %q: %s", c.msg, body)
			}
			tag := inputTag(t, body, "setting_"+c.field)
			if !strings.Contains(tag, `aria-invalid="true"`) || !strings.Contains(tag, `aria-describedby="ps-err-`+c.field+`"`) {
				t.Fatalf("bad field must be marked invalid and point at its message: %s", tag)
			}
			if !strings.Contains(body, `id="ps-err-`+c.field+`"`) {
				t.Fatal("the message element must carry the described-by id")
			}
			if c.field != "mode" && !strings.Contains(tag, `value="`+c.value+`"`) && c.value != "" {
				t.Fatalf("the entered value must be kept: %s", tag)
			}
			if c.field != "shop_note" && !strings.Contains(inputTag(t, body, "setting_shop_note"), `value="changed"`) {
				t.Fatal("a sibling's entered value must be kept")
			}
			if strings.Contains(body, "sk-typed-new-secret") || strings.Contains(body, "sk-stored-secret") {
				t.Fatal("a secret must never be echoed back")
			}
			if strings.Contains(body, "<html") {
				t.Fatal("an htmx 422 answers the form fragment, not a whole page")
			}
			if after := f.atRest(t); after != before {
				t.Fatalf("a refused save wrote something:\nbefore %s\nafter  %s", before, after)
			}
			if n := f.auditRows(t); n != 0 {
				t.Fatalf("a refused save wrote %d audit rows", n)
			}
		})
	}
}

// A field hidden by show_when (evaluated on the SUBMITTED controller value,
// falling back to the stored one) is neither validated nor written.
func TestPluginSettingsV2_POST_HiddenFieldNotValidated(t *testing.T) {
	f := setupV2Plugin(t, nil)
	body := f.get(t)
	if !strings.Contains(body, `data-show-when-key="mode" data-show-when-in="b" hidden`) {
		t.Fatal("extra must render hidden while mode = a")
	}
	form := validV2Form() // mode=a, extra="" (required, but hidden)
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("hidden required field blocked the save: %d %s", rec.Code, rec.Body.String())
	}
	form.Set("setting_extra", "9")
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("POST: %d", rec.Code)
	}
	if got := f.raw(t, "extra"); got != `""` {
		t.Fatalf("hidden field written as %s, want its stored value kept", got)
	}
	// Switching the controller in the same submission makes it visible:
	// now it is validated ...
	form.Set("setting_mode", "b")
	form.Set("setting_extra", "")
	rec := f.post(t, form, true)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), v2en("plugins.settings.error.required")) {
		t.Fatalf("visible required field: code %d, want 422 required", rec.Code)
	}
	if got := f.raw(t, "mode"); got != `"a"` {
		t.Fatalf("mode saved as %s despite the refusal", got)
	}
	// ... and written.
	form.Set("setting_extra", "4")
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("POST: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.raw(t, "extra"); got != `4` {
		t.Fatalf("extra stored %s, want 4", got)
	}
}

// A key whose type changed: an old string that parses as the new type is
// shown and rewritten in the new shape on the next save; one that doesn't
// parse is shown with "stored value is invalid" and kept untouched while
// it is left as it is.
func TestPluginSettingsV2_ChangedTypeRewriteAndInvalidStoredValue(t *testing.T) {
	f := setupV2Plugin(t, map[string]string{"max_items": `"7"`, "discount": `"abc"`, "enabled": `"true"`})
	body := f.get(t)
	if tag := inputTag(t, body, "setting_max_items"); !strings.Contains(tag, `value="7"`) || strings.Contains(tag, "aria-invalid") {
		t.Fatalf("parsable old string must display cleanly: %s", tag)
	}
	if tag := inputTag(t, body, "setting_enabled"); !strings.Contains(tag, " checked") {
		t.Fatalf("old \"true\" string must show as on: %s", tag)
	}
	tag := inputTag(t, body, "setting_discount")
	if !strings.Contains(tag, `value="abc"`) || !strings.Contains(tag, `aria-invalid="true"`) {
		t.Fatalf("an unparsable stored value must show raw and invalid: %s", tag)
	}
	if !strings.Contains(body, html.EscapeString(v2en("plugins.settings.error.stored_invalid"))) || strings.HasPrefix(v2en("plugins.settings.error.stored_invalid"), "plugins.") {
		t.Fatal("the stored-invalid message must render translated")
	}

	form := validV2Form()
	form.Set("setting_max_items", "7")
	form.Set("setting_discount", "abc") // unchanged invalid value: left as is
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("unchanged invalid value blocked the save: %d %s", rec.Code, rec.Body.String())
	}
	if got := f.raw(t, "max_items"); got != `7` {
		t.Fatalf("max_items = %s, want rewritten as the integer 7", got)
	}
	if got := f.raw(t, "enabled"); got != `true` {
		t.Fatalf("enabled = %s, want rewritten as JSON true", got)
	}
	if got := f.raw(t, "discount"); got != `"abc"` {
		t.Fatalf("invalid stored value rewritten to %s", got)
	}
	form.Set("setting_discount", "xyz")
	if rec := f.post(t, form, true); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("a changed invalid value must be refused, got %d", rec.Code)
	}
	form.Set("setting_discount", "12")
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("POST: %d", rec.Code)
	}
	if got := f.raw(t, "discount"); got != `1200` {
		t.Fatalf("discount = %s, want 1200", got)
	}
}

// Without JS (no HX-Request): success redirects back to the page (303), a
// refusal re-renders the whole page with 422.
func TestPluginSettingsV2_POST_NoJS(t *testing.T) {
	f := setupV2Plugin(t, nil)
	rec := f.post(t, validV2Form(), false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/plugins/p1/settings" {
		t.Fatalf("no-JS success: code %d location %q, want 303 to the page", rec.Code, rec.Header().Get("Location"))
	}
	form := validV2Form()
	form.Set("setting_max_items", "abc")
	rec = f.post(t, form, false)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("no-JS refusal: code %d, want 422", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "<html") || !strings.Contains(body, v2en("plugins.settings.error.number")) {
		t.Fatalf("no-JS refusal must render the whole page with the error, got %s", body)
	}
	// htmx success: the form comes back with the saved message inside it.
	rec = f.post(t, validV2Form(), true)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), v2en("plugins.settings.saved")) || !strings.Contains(rec.Body.String(), `id="ps-form"`) {
		t.Fatalf("htmx success: code %d body %s", rec.Code, rec.Body.String())
	}
	if f.auditRows(t) != 2 {
		t.Fatalf("audit rows = %d, want one per successful save", f.auditRows(t))
	}
}

// The takeaway editor's bad rate becomes a 422 field error on its row,
// with the entered value kept.
func TestPluginSettingsV2_TakeawayInvalidRateIsFieldError(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newPluginSettingsTestDeps(t)
	seedTaxCode(t, dp, "tax_19", "Standard VAT", 1900, nil)
	seedPluginSetting(t, dp, "p1", "takeaway_rate_overrides", `{}`, "global")
	form := url.Values{"setting_takeaway_typed": {"1"}, "takeaway_pct_tax_19": {"150"}}
	req := httptest.NewRequest(http.MethodPost, "/api/plugins/p1/settings", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code %d, want 422", rec.Code)
	}
	tag := inputTag(t, rec.Body.String(), "takeaway_pct_tax_19")
	if !strings.Contains(tag, `aria-invalid="true"`) || !strings.Contains(tag, `value="150"`) {
		t.Fatalf("bad takeaway rate must be marked invalid with its value kept: %s", tag)
	}
	if !strings.Contains(rec.Body.String(), v2en("plugins.settings.takeaway.invalid_rate")) {
		t.Fatal("takeaway message missing")
	}
}

// The browser must never block Save on its own constraint checks: a
// required field hidden by show_when, or an invalid stored value prefilled
// into a pattern box, would make Save do nothing with no message. The
// server validates every visible field instead (ut-docs#4157 review).
func TestPluginSettingsV2_FormIsNoValidate(t *testing.T) {
	f := setupV2Plugin(t, map[string]string{"discount": `"abc"`})
	body := f.get(t)
	i := strings.Index(body, `<form id="ps-form"`)
	if i < 0 {
		t.Fatal("no settings form")
	}
	if tag := body[i : i+strings.Index(body[i:], ">")]; !strings.Contains(tag, "novalidate") {
		t.Fatalf("form must be novalidate: %s", tag)
	}
	// The invalid stored percent is shown as is; submitted unchanged it is
	// kept and the rest of the form still saves.
	form := validV2Form()
	form.Set("setting_discount", "abc")
	form.Set("setting_max_items", "9")
	if rec := f.post(t, form, true); rec.Code != http.StatusOK {
		t.Fatalf("code %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if got := f.raw(t, "discount"); got != `"abc"` {
		t.Fatalf("invalid stored value rewritten: %s", got)
	}
	if got := f.raw(t, "max_items"); got != `9` {
		t.Fatalf("sibling not saved: %s", got)
	}
}
