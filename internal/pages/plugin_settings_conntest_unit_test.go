package pages

import (
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/plugins"
)

// ADR-0121 S5 (ut-docs#5961): the Test connection answer decoder and the
// helpers that decide which tests are drawn.

func TestDecodeSettingsTestAnswer(t *testing.T) {
	big := `{"ok":true,"message":"` + strings.Repeat("a", settingsTestMaxAnswerBytes) + `"}`
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		ok      bool
		message string
		field   string
	}{
		{"ok", `{"ok":true}`, false, true, "", ""},
		{"ok with message", `{"ok":true,"message":"x.ok"}`, false, true, "x.ok", ""},
		{"failed with field", `{"ok":false,"message":"x.bad","field":"api_key"}`, false, false, "x.bad", "api_key"},
		{"failed bare", `{"ok":false}`, false, false, "", ""},
		{"null message and field", `{"ok":true,"message":null,"field":null}`, false, true, "", ""},
		{"surrounding space", " {\"ok\":true}\n", false, true, "", ""},
		{"unknown field", `{"ok":true,"extra":1}`, true, false, "", ""},
		{"trailing data", `{"ok":true}{"ok":false}`, true, false, "", ""},
		{"trailing garbage", `{"ok":true} x`, true, false, "", ""},
		{"null", `null`, true, false, "", ""},
		{"array", `[]`, true, false, "", ""},
		{"string", `"ok"`, true, false, "", ""},
		{"empty", ``, true, false, "", ""},
		{"ok missing", `{"message":"x.ok"}`, true, false, "", ""},
		{"ok null", `{"ok":null}`, true, false, "", ""},
		{"ok string", `{"ok":"true"}`, true, false, "", ""},
		{"ok number", `{"ok":1}`, true, false, "", ""},
		{"message number", `{"ok":true,"message":5}`, true, false, "", ""},
		{"field object", `{"ok":false,"field":{}}`, true, false, "", ""},
		{"over 4 KiB", big, true, false, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, message, field, err := decodeSettingsTestAnswer([]byte(c.raw))
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if err != nil {
				return
			}
			if ok != c.ok || message != c.message || field != c.field {
				t.Fatalf("got (%v,%q,%q), want (%v,%q,%q)", ok, message, field, c.ok, c.message, c.field)
			}
		})
	}
}

func testsManifest() *plugins.Manifest {
	return &plugins.Manifest{SettingsTests: []plugins.ManifestSettingsTest{
		{ID: "conn", Label: "plugins.settings.save"},
		{ID: "per_rec", Label: "plugins.settings.save", Record: true},
		{ID: "two", Label: "plugins.settings.title", Tab: "main"},
	}}
}

func TestDrawnSettingsTest(t *testing.T) {
	m := testsManifest()
	if got := drawnSettingsTest(m, "conn"); got == nil || got.ID != "conn" {
		t.Errorf("conn = %+v", got)
	}
	if got := drawnSettingsTest(m, "two"); got == nil {
		t.Error("a test that names a tab is still drawn")
	}
	for _, id := range []string{"per_rec", "nope", ""} {
		if got := drawnSettingsTest(m, id); got != nil {
			t.Errorf("%q = %+v, want nil", id, got)
		}
	}
	if drawnSettingsTest(nil, "conn") != nil {
		t.Error("nil manifest must give nil")
	}
}

func TestSettingsTestViews(t *testing.T) {
	if got := settingsTestViews(nil, "p1", "en", ""); got != nil {
		t.Errorf("nil manifest: %+v", got)
	}
	if got := settingsTestViews(&plugins.Manifest{}, "p1", "en", ""); got != nil {
		t.Errorf("no tests: %+v", got)
	}
	got := settingsTestViews(testsManifest(), "p1", "en", "two")
	if len(got) != 2 {
		t.Fatalf("views = %+v, want conn and two (record skipped)", got)
	}
	if got[0].ID != "conn" || got[0].Label != v2en("plugins.settings.save") || got[0].PluginID != "p1" || got[0].Run {
		t.Errorf("conn view = %+v", got[0])
	}
	if got[1].ID != "two" || !got[1].Run || got[1].Label != v2en("plugins.settings.title") {
		t.Errorf("two view = %+v", got[1])
	}
	if got[0].Label == "plugins.settings.save" {
		t.Error("label must be translated")
	}
}
