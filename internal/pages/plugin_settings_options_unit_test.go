package pages

import (
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/plugins"
)

// ADR-0121 S5 (ut-docs#4160): units of the asked-options seam.

func TestDecodeSettingsOptionsAnswer(t *testing.T) {
	opt := func(v, l string) string { return `{"value":"` + v + `","label":"` + l + `"}` }
	many := func(n int) string {
		var parts []string
		for i := 0; i < n; i++ {
			parts = append(parts, opt(strings.Repeat("v", 3)+string(rune('a'+i%26))+strings.Repeat("x", i/26), "L"))
		}
		return `{"options":[` + strings.Join(parts, ",") + `]}`
	}
	cases := []struct {
		name    string
		raw     string
		want    []askedOption
		wantErr bool
	}{
		{"ok", `{"options":[` + opt("a", "Alpha") + `,` + opt("b", "Beta") + `]}`, []askedOption{{"a", "Alpha"}, {"b", "Beta"}}, false},
		{"empty label falls back to value", `{"options":[{"value":"a","label":""}]}`, []askedOption{{"a", "a"}}, false},
		{"missing label falls back to value", `{"options":[{"value":"a"}]}`, []askedOption{{"a", "a"}}, false},
		{"empty object", `{}`, nil, false},
		{"null", `null`, nil, false},
		{"null options", `{"options":null}`, nil, false},
		{"empty list", `{"options":[]}`, nil, false},
		{"32 entries", many(32), nil, false},
		{"33 entries", many(33), nil, true},
		{"65 byte value", `{"options":[` + opt(strings.Repeat("a", 65), "x") + `]}`, nil, true},
		{"64 byte value ok", `{"options":[` + opt(strings.Repeat("a", 64), "x") + `]}`, []askedOption{{strings.Repeat("a", 64), "x"}}, false},
		{"comma value", `{"options":[` + opt("a,b", "x") + `]}`, nil, true},
		{"leading space value", `{"options":[` + opt(" a", "x") + `]}`, nil, true},
		{"empty value", `{"options":[` + opt("", "x") + `]}`, nil, true},
		{"129 byte label", `{"options":[` + opt("a", strings.Repeat("l", 129)) + `]}`, nil, true},
		{"128 byte label ok", `{"options":[` + opt("a", strings.Repeat("l", 128)) + `]}`, []askedOption{{"a", strings.Repeat("l", 128)}}, false},
		{"newline label", `{"options":[{"value":"a","label":"x\ny"}]}`, nil, true},
		{"duplicate values", `{"options":[` + opt("a", "x") + `,` + opt("a", "y") + `]}`, nil, true},
		{"unknown top field", `{"options":[],"extra":1}`, nil, true},
		{"unknown entry field", `{"options":[{"value":"a","label":"x","id":1}]}`, nil, true},
		{"trailing data", `{"options":[]} {"options":[]}`, nil, true},
		{"not an object", `[1]`, nil, true},
		// Valid JSON padded with whitespace, so only the size bound can refuse it.
		{"one byte over the size limit", `{"options":[]}` + strings.Repeat(" ", settingsOptionsMaxAnswerBytes-len(`{"options":[]}`)+1), nil, true},
		{"at the size limit ok", `{"options":[]}` + strings.Repeat(" ", settingsOptionsMaxAnswerBytes-len(`{"options":[]}`)), nil, false},
	}
	for _, c := range cases {
		got, err := decodeSettingsOptionsAnswer([]byte(c.raw))
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", c.name, err, c.wantErr)
			continue
		}
		if c.name == "32 entries" {
			if len(got) != 32 {
				t.Errorf("%s: got %d entries", c.name, len(got))
			}
			continue
		}
		if !c.wantErr && !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

const askValuesManifest = `{"id":"p1","name":"P","version":"1.0.0","runtime":"none","settings":[
 {"key":"provider","type":"choice","options":[{"value":"a","label":"plugins.settings.save"},{"value":"b","label":"plugins.settings.save"}]},
 {"key":"model","type":"choice","options_ask":true,"depends_on":["provider"]},
 {"key":"flag","type":"bool"},
 {"key":"pct","type":"percent"},
 {"key":"cnt","type":"number"},
 {"key":"name","type":"text"},
 {"key":"api_key","type":"text"},
 {"key":"token","type":"secret"},
 {"key":"items","type":"list","title_field":"f","max_items":5,"fields":[{"key":"f","type":"text"}]}
]}`

func parseAskManifest(t *testing.T) *plugins.Manifest {
	t.Helper()
	m, err := plugins.ParseManifest(strings.NewReader(askValuesManifest))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

func askRows() []data.PluginSettingRow {
	return []data.PluginSettingRow{
		{Key: "provider", ValueJSON: `"a"`, Scope: "global"},
		{Key: "model", ValueJSON: `"m1"`, Scope: "global"},
		{Key: "flag", ValueJSON: `false`, Scope: "global"},
		{Key: "pct", ValueJSON: `500`, Scope: "global"},
		{Key: "cnt", ValueJSON: `3`, Scope: "global"},
		{Key: "name", ValueJSON: `"stored"`, Scope: "global"},
		{Key: "api_key", ValueJSON: `"sk-secret"`, Scope: "global"},
		{Key: "token", ValueJSON: `"tok"`, Scope: "global"},
		{Key: "items", ValueJSON: `[{"f":"x"}]`, Scope: "global"},
		{Key: "ghost", ValueJSON: `"undeclared"`, Scope: "global"},
	}
}

func askIsSecret(k string) bool { return k == "token" || plugins.IsSecretSettingKey(k) }

func TestSettingsAskValues(t *testing.T) {
	m := parseAskManifest(t)
	form := url.Values{
		"setting_provider": {"b"},
		"setting_api_key":  {"typed-secret"},
		"setting_token":    {"typed-token"},
		"setting_items":    {`[{"f":"typed"}]`},
		"setting_name":     {"  new name  "},
		"setting_pct":      {"7,5"},
		"setting_cnt":      {"not a number"},
		"setting_ghost":    {"zzz"},
	}
	got := settingsAskValues(m, form, askRows(), askIsSecret, 2, "en")
	want := map[string]string{
		"provider": "b",  // unsaved value wins
		"model":    "m1", // stored
		"name":     "new name",
		"pct":      "750", // canonical bp
		// cnt invalid -> omitted, flag: no marker in form -> stored
		"flag": "false",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, banned := range []string{"api_key", "token", "items", "ghost", "cnt"} {
		if _, ok := got[banned]; ok {
			t.Errorf("values carry %q", banned)
		}
	}
}

func TestSettingsAskValues_SwitchAndBlankNumber(t *testing.T) {
	m := parseAskManifest(t)
	form := url.Values{"present_setting_flag": {"1"}, "setting_flag": {"1"}, "setting_cnt": {""}}
	got := settingsAskValues(m, form, askRows(), askIsSecret, 2, "en")
	if got["flag"] != "true" {
		t.Errorf("flag = %q", got["flag"])
	}
	if _, ok := got["cnt"]; ok {
		t.Errorf("blank number should be omitted, got %q", got["cnt"])
	}
	form = url.Values{"present_setting_flag": {"1"}}
	if got := settingsAskValues(m, form, askRows(), askIsSecret, 2, "en"); got["flag"] != "false" {
		t.Errorf("unchecked switch = %q", got["flag"])
	}
}

func TestSettingsAskValues_NoRowNoForm(t *testing.T) {
	m := parseAskManifest(t)
	got := settingsAskValues(m, nil, nil, askIsSecret, 2, "en")
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestSettingsOptionsCache(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	c := newSettingsOptionsCache(clk.now)
	vals := map[string]string{"provider": "a"}
	k := settingsOptionsCacheKey("1.0.0", "model", vals)
	if _, ok := c.get("p1", k); ok {
		t.Fatal("hit on empty cache")
	}
	opts := []askedOption{{"a", "A"}}
	c.put("p1", k, opts)
	got, ok := c.get("p1", k)
	if !ok || !reflect.DeepEqual(got, opts) {
		t.Fatalf("miss/mismatch: %v %v", got, ok)
	}
	got[0].Label = "mutated"
	if again, _ := c.get("p1", k); again[0].Label != "A" {
		t.Error("cache returned its own backing slice")
	}
	// empty list is cached too
	k2 := settingsOptionsCacheKey("1.0.0", "model", map[string]string{"provider": "b"})
	c.put("p1", k2, nil)
	if got, ok := c.get("p1", k2); !ok || len(got) != 0 {
		t.Errorf("empty answer not cached: %v %v", got, ok)
	}
	// key sensitivity
	if settingsOptionsCacheKey("1.0.1", "model", vals) == k {
		t.Error("version not in key")
	}
	if settingsOptionsCacheKey("1.0.0", "model", map[string]string{"provider": "c"}) == k {
		t.Error("values not in key")
	}
	if settingsOptionsCacheKey("1.0.0", "other", vals) == k {
		t.Error("setting key not in key")
	}
	// TTL
	clk.t = clk.t.Add(settingsOptionsTTL - time.Second)
	if _, ok := c.get("p1", k); !ok {
		t.Error("expired early")
	}
	clk.t = clk.t.Add(2 * time.Second)
	if _, ok := c.get("p1", k); ok {
		t.Error("not expired after TTL")
	}
	// dropPlugin
	c.put("p1", k, opts)
	c.put("p2", k, opts)
	c.dropPlugin("p1")
	if _, ok := c.get("p1", k); ok {
		t.Error("p1 survived dropPlugin")
	}
	if _, ok := c.get("p2", k); !ok {
		t.Error("p2 dropped by p1's dropPlugin")
	}
}

func TestSettingsOptionsCache_EvictsOldestAtCap(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1_000_000, 0)}
	c := newSettingsOptionsCache(clk.now)
	keys := make([]string, settingsOptionsMaxPerPlugin+1)
	for i := range keys {
		keys[i] = settingsOptionsCacheKey("1", "m", map[string]string{"i": string(rune('A' + i))})
		clk.t = clk.t.Add(time.Second)
		c.put("p1", keys[i], []askedOption{{"v", "v"}})
	}
	if _, ok := c.get("p1", keys[0]); ok {
		t.Error("oldest entry survived the 65th put")
	}
	for _, k := range keys[1:] {
		if _, ok := c.get("p1", k); !ok {
			t.Fatal("a newer entry was evicted")
		}
	}
}

func choiceDecl(t *testing.T, ask bool, opts ...string) *plugins.ManifestSetting {
	t.Helper()
	d := &plugins.ManifestSetting{Key: "model", Type: plugins.SettingTypeChoice, OptionsAsk: ask}
	for _, o := range opts {
		d.Options = append(d.Options, plugins.ManifestSettingOption{Value: o, Label: "plugins.settings.save"})
	}
	return d
}

func values(os []choiceOptionView) []string {
	var out []string
	for _, o := range os {
		out = append(out, o.Value)
	}
	return out
}

func TestMergeChoiceOptions(t *testing.T) {
	// static only, current allowed: unchanged shape
	got := mergeChoiceOptions(choiceDecl(t, false, "a", "b"), "b", nil, "en")
	if !reflect.DeepEqual(values(got), []string{"a", "b"}) || got[0].Selected || !got[1].Selected {
		t.Errorf("static: %+v", got)
	}
	// unset: not-set entry selected first
	got = mergeChoiceOptions(choiceDecl(t, true), "", []askedOption{{"m1", "M1"}}, "en")
	if !reflect.DeepEqual(values(got), []string{"", "m1"}) || !got[0].Selected {
		t.Errorf("unset: %+v", got)
	}
	// unknown stored value on a static choice: Unknown
	got = mergeChoiceOptions(choiceDecl(t, false, "a"), "old", nil, "en")
	if !reflect.DeepEqual(values(got), []string{"old", "a"}) || !got[0].Unknown || !got[0].Selected {
		t.Errorf("legacy: %+v", got)
	}
	// asked choice: stored value missing from the answer stays first, not Unknown
	got = mergeChoiceOptions(choiceDecl(t, true), "gone", []askedOption{{"m1", "M1"}, {"m2", "M2"}}, "en")
	if !reflect.DeepEqual(values(got), []string{"gone", "m1", "m2"}) || got[0].Unknown || !got[0].Selected {
		t.Errorf("stored missing: %+v", got)
	}
	// dedupe asked vs static; asked first, answer order; label plain
	got = mergeChoiceOptions(choiceDecl(t, true, "m2", "z"), "m2", []askedOption{{"m1", "<b>x</b>"}, {"m2", "M2"}}, "en")
	if !reflect.DeepEqual(values(got), []string{"m1", "m2", "z"}) {
		t.Errorf("merge: %+v", got)
	}
	if got[0].Label != "<b>x</b>" || !got[1].Selected || got[1].Label != "M2" {
		t.Errorf("labels/selected: %+v", got)
	}
}

func TestAskedOptionsMessage(t *testing.T) {
	dStatic := choiceDecl(t, true, "s")
	dNone := choiceDecl(t, true)
	cases := []struct {
		name   string
		decl   *plugins.ManifestSetting
		cur    string
		asked  []askedOption
		failed bool
		want   string
	}{
		{"failed wins", dNone, "x", []askedOption{{"a", "a"}}, true, "failed"},
		{"empty", dNone, "", nil, false, "empty"},
		{"empty with stored", dNone, "x", nil, false, "empty"},
		{"empty but static", dStatic, "", nil, false, ""},
		{"saved missing", dNone, "x", []askedOption{{"a", "a"}}, false, "saved_missing"},
		{"saved present", dNone, "a", []askedOption{{"a", "a"}}, false, ""},
		{"saved in static", dStatic, "s", []askedOption{{"a", "a"}}, false, ""},
		{"nothing stored, list", dNone, "", []askedOption{{"a", "a"}}, false, ""},
	}
	for _, c := range cases {
		if got := askedOptionsMessage(c.decl, c.cur, c.asked, c.failed); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

func TestShowWhenHidden_AskedControllerIsLegitimate(t *testing.T) {
	m, err := plugins.ParseManifest(strings.NewReader(`{"id":"p1","name":"P","version":"1.0.0","runtime":"none","settings":[
	  {"key":"model","type":"choice","options_ask":true},
	  {"key":"x","type":"text","show_when":{"key":"model","in":["m1"]}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	w := m.SettingDecl("x").ShowWhen
	if !showWhenHidden(m, w, "other-asked-model") {
		t.Error("a value outside show_when.in must hide the field for an asked controller")
	}
	if showWhenHidden(m, w, "m1") {
		t.Error("matching value must show")
	}
}
