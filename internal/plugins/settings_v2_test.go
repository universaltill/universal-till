package plugins

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ut-docs#4156 (ADR-0121 amendment "plugin settings form v2", S1–S8): the
// manifest schema and its validation at both install paths.

const settingsV2FixturePath = "testdata/settings_v2_full_manifest.json"

// v2Manifest builds a manifest that passes both ParseManifest and
// VerifyManifest's structural checks, with settingsJSON as its settings
// array and extra appended as further top-level fields (leading comma).
func v2Manifest(settingsJSON, extra string) string {
	return `{"id":"t.p","name":"T","version":"1.0.0","runtime":"none","canonical_type":"integration","device_arch":"any","settings":[` + settingsJSON + `]` + extra + `}`
}

// bothInstallPaths runs src through ParseManifest and VerifyManifest and
// returns each path's error.
func bothInstallPaths(t *testing.T, src string) (parseErr, verifyErr error) {
	t.Helper()
	_, parseErr = ParseManifest(strings.NewReader(src))
	p := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	_, verifyErr = structuralOnlyVerifier().VerifyManifest(p)
	return parseErr, verifyErr
}

func repeatSettings(n int, f func(i int) string) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = f(i)
	}
	return strings.Join(parts, ",")
}

const (
	v2Tab        = `,"settings_tabs":[{"id":"main","label":"t.main"}]`
	v2ListFields = `"fields":[{"key":"name","label":"t.n"}],"title_field":"name","max_items":5`
	v2List       = `{"key":"conns","type":"list",` + v2ListFields + `}`
)

func TestSettingsV2_RefusedShapes(t *testing.T) {
	long := strings.Repeat("x", 129)
	cases := []struct {
		name, settings, extra string
	}{
		// type / bounds
		{"unknown type", `{"key":"p","type":"color"}`, ""},
		{"too many settings", repeatSettings(65, func(i int) string { return `{"key":"k` + strings.Repeat("x", i) + `"}` }), ""},
		{"overlong placeholder", `{"key":"p","type":"text","placeholder":"` + long + `"}`, ""},
		{"overlong guide", `{"key":"p","guide":"` + long + `"}`, ""},
		{"overlong notice", `{"key":"p","notice":"` + long + `"}`, ""},
		// applicability
		{"min on text", `{"key":"p","type":"text","min":1}`, ""},
		{"max on bool", `{"key":"p","type":"bool","max":1}`, ""},
		{"min greater than max", `{"key":"p","type":"number","min":5,"max":1}`, ""},
		{"max_len on number", `{"key":"p","type":"number","max_len":4}`, ""},
		{"max_len zero-or-negative", `{"key":"p","type":"text","max_len":-1}`, ""},
		{"max_len over 4096", `{"key":"p","type":"text","max_len":4097}`, ""},
		{"placeholder on bool", `{"key":"p","type":"bool","placeholder":"t.x"}`, ""},
		{"placeholder on choice", `{"key":"p","type":"choice","options":[{"value":"a","label":"x"}],"placeholder":"t.x"}`, ""},
		{"required on bool", `{"key":"p","type":"bool","required":true}`, ""},
		{"required on list", `{"key":"conns","type":"list",` + v2ListFields + `,"required":true}`, ""},
		{"required on map", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"text"},"required":true}`, ""},
		{"paste on number", `{"key":"p","type":"number","paste":true}`, ""},
		{"paste on endpoint", `{"key":"p","type":"endpoint","paste":true}`, ""},
		{"fields on text", `{"key":"p","type":"text","fields":[{"key":"a"}]}`, ""},
		{"title_field on text", `{"key":"p","title_field":"a"}`, ""},
		{"min_items on text", `{"key":"p","min_items":1}`, ""},
		{"max_items on text", `{"key":"p","max_items":1}`, ""},
		{"map_over on text", `{"key":"p","map_over":"tax_codes"}`, ""},
		{"value on text", `{"key":"p","value":{"type":"text"}}`, ""},
		{"options_ask on text", `{"key":"p","options_ask":true}`, ""},
		{"depends_on on text", `{"key":"a"},{"key":"p","depends_on":["a"]}`, ""},
		{"depends_on without options_ask", `{"key":"a"},{"key":"p","type":"choice","options":[{"value":"a","label":"x"}],"depends_on":["a"]}`, ""},
		// credential-named keys
		{"credential-named bool", `{"key":"api_key","type":"bool"}`, ""},
		{"credential-named number", `{"key":"admin_password","type":"number"}`, ""},
		{"credential-named list", `{"key":"api_tokens","type":"list",` + v2ListFields + `}`, ""},
		// default_value shapes
		{"text default not a string", `{"key":"p","type":"text","default_value":3}`, ""},
		{"text default over max_len", `{"key":"p","type":"text","max_len":2,"default_value":"abc"}`, ""},
		{"bool default not a bool", `{"key":"p","type":"bool","default_value":"true"}`, ""},
		{"number default a string", `{"key":"p","type":"number","default_value":"3"}`, ""},
		{"number default with fraction", `{"key":"p","type":"number","default_value":1.5}`, ""},
		{"percent default beyond 2^53", `{"key":"p","type":"percent","default_value":9007199254740993000}`, ""},
		{"money default below min", `{"key":"p","type":"money","min":10,"default_value":5}`, ""},
		{"number default above max", `{"key":"p","type":"number","max":10,"default_value":11}`, ""},
		{"list with default", `{"key":"conns","type":"list",` + v2ListFields + `,"default_value":[]}`, ""},
		{"map default not an object", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"percent"},"default_value":[1]}`, ""},
		{"map default value wrong shape", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"percent"},"default_value":{"a":"7"}}`, ""},
		{"map default value out of range", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"percent","max":100},"default_value":{"a":101}}`, ""},
		{"map default choice not an option", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"choice","options":[{"value":"a","label":"x"}]},"default_value":{"t":"z"}}`, ""},
		{"endpoint default not a URL", `{"key":"p","type":"endpoint","default_value":"ftp://x"}`, ""},
		// choice + options_ask
		{"options_ask default with comma", `{"key":"p","type":"choice","options_ask":true,"default_value":"a,b"}`, ""},
		{"options_ask default too long", `{"key":"p","type":"choice","options_ask":true,"default_value":"` + strings.Repeat("v", 65) + `"}`, ""},
		{"options_ask default not a string", `{"key":"p","type":"choice","options_ask":true,"default_value":1}`, ""},
		{"options_ask too many options", `{"key":"p","type":"choice","options_ask":true,"options":[` + manyOptions(33) + `]}`, ""},
		{"depends_on unknown key", `{"key":"p","type":"choice","options_ask":true,"depends_on":["nope"]}`, ""},
		{"depends_on self", `{"key":"p","type":"choice","options_ask":true,"depends_on":["p"]}`, ""},
		{"depends_on a secret", `{"key":"k","type":"secret"},{"key":"p","type":"choice","options_ask":true,"depends_on":["k"]}`, ""},
		{"depends_on a heuristic secret", `{"key":"api_key"},{"key":"p","type":"choice","options_ask":true,"depends_on":["api_key"]}`, ""},
		{"depends_on too many", repeatSettings(9, func(i int) string { return `{"key":"d` + strings.Repeat("x", i) + `"}` }) +
			`,{"key":"p","type":"choice","options_ask":true,"depends_on":[` + repeatSettings(9, func(i int) string { return `"d` + strings.Repeat("x", i) + `"` }) + `]}`, ""},
		// guide_url
		{"guide_url http", `{"key":"p","guide_url":"http://example.com/k"}`, ""},
		{"guide_url no host", `{"key":"p","guide_url":"https:///k"}`, ""},
		{"guide_url userinfo", `{"key":"p","guide_url":"https://u:p@example.com/k"}`, ""},
		{"guide_url too long", `{"key":"p","guide_url":"https://example.com/` + strings.Repeat("a", 500) + `"}`, ""},
		{"guide_url javascript", `{"key":"p","guide_url":"javascript:alert(1)"}`, ""},
		// show_when on a list/map controller (its value is not a scalar)
		{"show_when on a list", v2List + `,{"key":"q","show_when":{"key":"conns","in":["a"]}}`, ""},
		// tabs
		{"tab not declared", `{"key":"p","tab":"main"}`, ""},
		{"tab unknown id", `{"key":"p","tab":"other"}`, v2Tab},
		{"too many tabs", `{"key":"p"}`, `,"settings_tabs":[` + repeatSettings(9, func(i int) string { return `{"id":"t` + strings.Repeat("x", i) + `","label":"l"}` }) + `]`},
		{"tab bad id", `{"key":"p"}`, `,"settings_tabs":[{"id":"Main","label":"l"}]`},
		{"tab id too long", `{"key":"p"}`, `,"settings_tabs":[{"id":"t` + strings.Repeat("x", 32) + `","label":"l"}]`},
		{"tab duplicate id", `{"key":"p"}`, `,"settings_tabs":[{"id":"a","label":"l"},{"id":"a","label":"m"}]`},
		{"tab empty label", `{"key":"p"}`, `,"settings_tabs":[{"id":"a","label":""}]`},
		{"tab overlong help", `{"key":"p"}`, `,"settings_tabs":[{"id":"a","label":"l","help":"` + long + `"}]`},
		{"tab show_when unknown key", `{"key":"p"}`, `,"settings_tabs":[{"id":"a","label":"l","show_when":{"key":"nope","in":["x"]}}]`},
		{"tab show_when on a secret", `{"key":"k","type":"secret"}`, `,"settings_tabs":[{"id":"a","label":"l","show_when":{"key":"k","in":["x"]}}]`},
		{"tab show_when empty in", `{"key":"p"}`, `,"settings_tabs":[{"id":"a","label":"l","show_when":{"key":"p","in":[]}}]`},
		{"tab show_when comma", `{"key":"p"}`, `,"settings_tabs":[{"id":"a","label":"l","show_when":{"key":"p","in":["a,b"]}}]`},
		// list
		{"list without fields", `{"key":"conns","type":"list","title_field":"name","max_items":5}`, ""},
		{"list too many fields", `{"key":"conns","type":"list","title_field":"f","max_items":5,"fields":[` + repeatSettings(13, func(i int) string { return `{"key":"f` + strings.Repeat("x", i) + `"}` }) + `]}`, ""},
		{"list without title_field", `{"key":"conns","type":"list","fields":[{"key":"name"}],"max_items":5}`, ""},
		{"list title_field unknown", `{"key":"conns","type":"list","fields":[{"key":"name"}],"title_field":"nope","max_items":5}`, ""},
		{"list title_field a bool", `{"key":"conns","type":"list","fields":[{"key":"on","type":"bool"}],"title_field":"on","max_items":5}`, ""},
		{"list title_field credential-named text", `{"key":"conns","type":"list","fields":[{"key":"token_name"}],"title_field":"token_name","max_items":5}`, ""},
		{"list title_field a secret", `{"key":"conns","type":"list","fields":[{"key":"pw","type":"secret"}],"title_field":"pw","max_items":5}`, ""},
		{"list without max_items", `{"key":"conns","type":"list","fields":[{"key":"name"}],"title_field":"name"}`, ""},
		{"list max_items over 20", `{"key":"conns","type":"list","fields":[{"key":"name"}],"title_field":"name","max_items":21}`, ""},
		{"list min_items over max_items", `{"key":"conns","type":"list","fields":[{"key":"name"}],"title_field":"name","min_items":3,"max_items":2}`, ""},
		{"list negative min_items", `{"key":"conns","type":"list","fields":[{"key":"name"}],"title_field":"name","min_items":-1,"max_items":2}`, ""},
		{"field empty key", `{"key":"conns","type":"list","fields":[{"key":"name"},{"key":""}],"title_field":"name","max_items":5}`, ""},
		{"field duplicate key", `{"key":"conns","type":"list","fields":[{"key":"name"},{"key":"name"}],"title_field":"name","max_items":5}`, ""},
		{"field unknown type", `{"key":"conns","type":"list","fields":[{"key":"name"},{"key":"c","type":"color"}],"title_field":"name","max_items":5}`, ""},
		{"field nested list", `{"key":"conns","type":"list","fields":[{"key":"name"},{"key":"sub","type":"list","fields":[{"key":"a"}],"title_field":"a","max_items":2}],"title_field":"name","max_items":5}`, ""},
		{"field map", `{"key":"conns","type":"list","fields":[{"key":"name"},{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"text"}}],"title_field":"name","max_items":5}`, ""},
		{"field with tab", `{"key":"conns","type":"list","fields":[{"key":"name","tab":"main"}],"title_field":"name","max_items":5}`, v2Tab},
		{"field show_when top-level key", `{"key":"top"},{"key":"conns","type":"list","fields":[{"key":"name","show_when":{"key":"top","in":["a"]}}],"title_field":"name","max_items":5}`, ""},
		{"field show_when itself", `{"key":"conns","type":"list","fields":[{"key":"name","show_when":{"key":"name","in":["a"]}}],"title_field":"name","max_items":5}`, ""},
		{"field show_when secret sibling", `{"key":"conns","type":"list","fields":[{"key":"name"},{"key":"pw","type":"secret"},{"key":"x","show_when":{"key":"pw","in":["a"]}}],"title_field":"name","max_items":5}`, ""},
		{"field depends_on top-level key", `{"key":"top"},{"key":"conns","type":"list","fields":[{"key":"name"},{"key":"m","type":"choice","options_ask":true,"depends_on":["top"]}],"title_field":"name","max_items":5}`, ""},
		{"field credential-named choice", `{"key":"conns","type":"list","fields":[{"key":"name"},{"key":"api_key","type":"choice","options":[{"value":"a","label":"x"}]}],"title_field":"name","max_items":5}`, ""},
		{"field secret default not a string", `{"key":"conns","type":"list","fields":[{"key":"name"},{"key":"pw","type":"secret","default_value":1}],"title_field":"name","max_items":5}`, ""},
		{"field untyped default not a string", `{"key":"conns","type":"list","fields":[{"key":"name","default_value":{"a":1}}],"title_field":"name","max_items":5}`, ""},
		{"field bad default", `{"key":"conns","type":"list","fields":[{"key":"name"},{"key":"n","type":"number","default_value":"1"}],"title_field":"name","max_items":5}`, ""},
		{"field choice without options", `{"key":"conns","type":"list","fields":[{"key":"name"},{"key":"c","type":"choice"}],"title_field":"name","max_items":5}`, ""},
		{"field min on text", `{"key":"conns","type":"list","fields":[{"key":"name","min":1}],"title_field":"name","max_items":5}`, ""},
		{"field overlong label", `{"key":"conns","type":"list","fields":[{"key":"name","label":"` + long + `"}],"title_field":"name","max_items":5}`, ""},
		// map
		{"map without map_over", `{"key":"m","type":"map","value":{"type":"text"}}`, ""},
		{"map without value", `{"key":"m","type":"map","map_over":"tax_codes"}`, ""},
		{"map unknown map_over", `{"key":"m","type":"map","map_over":"customers","value":{"type":"text"}}`, ""},
		{"map value type list", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"list"}}`, ""},
		{"map value type secret", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"secret"}}`, ""},
		{"map value type empty", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":""}}`, ""},
		{"map value min on text", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"text","min":1}}`, ""},
		{"map value min greater than max", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"percent","min":5,"max":1}}`, ""},
		{"map value max_len on percent", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"percent","max_len":3}}`, ""},
		{"map value options on text", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"text","options":[{"value":"a","label":"x"}]}}`, ""},
		{"map value choice without options", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"choice"}}`, ""},
		{"map value choice duplicate option", `{"key":"m","type":"map","map_over":"tax_codes","value":{"type":"choice","options":[{"value":"a","label":"x"},{"value":"a","label":"y"}]}}`, ""},
		// settings_tests
		{"too many tests", `{"key":"p"}`, `,"settings_tests":[` + repeatSettings(5, func(i int) string { return `{"id":"t` + strings.Repeat("x", i) + `","label":"l"}` }) + `]`},
		{"test bad id", `{"key":"p"}`, `,"settings_tests":[{"id":"9x","label":"l"}]`},
		{"test duplicate id", `{"key":"p"}`, `,"settings_tests":[{"id":"a","label":"l"},{"id":"a","label":"m"}]`},
		{"test empty label", `{"key":"p"}`, `,"settings_tests":[{"id":"a","label":""}]`},
		{"test overlong label", `{"key":"p"}`, `,"settings_tests":[{"id":"a","label":"` + long + `"}]`},
		{"test unknown tab", `{"key":"p"}`, v2Tab + `,"settings_tests":[{"id":"a","label":"l","tab":"other"}]`},
		{"record test without a list", `{"key":"p"}`, `,"settings_tests":[{"id":"a","label":"l","record":true}]`},
		{"record test with a tab", v2List, v2Tab + `,"settings_tests":[{"id":"a","label":"l","record":true,"tab":"main"}]`},
		// settings_setup
		{"setup without steps", `{"key":"p"}`, `,"settings_setup":{"steps":[]}`},
		{"setup too many steps", `{"key":"p"}`, `,"settings_setup":{"steps":[` + repeatSettings(9, func(int) string { return `{"title":"t","fields":["p"]}` }) + `]}`},
		{"step empty title", `{"key":"p"}`, `,"settings_setup":{"steps":[{"title":"","fields":["p"]}]}`},
		{"step overlong help", `{"key":"p"}`, `,"settings_setup":{"steps":[{"title":"t","help":"` + long + `","fields":["p"]}]}`},
		{"step without fields", `{"key":"p"}`, `,"settings_setup":{"steps":[{"title":"t","fields":[]}]}`},
		{"step unknown field", `{"key":"p"}`, `,"settings_setup":{"steps":[{"title":"t","fields":["nope"]}]}`},
		{"step record field", v2List, `,"settings_setup":{"steps":[{"title":"t","fields":["name"]}]}`},
		{"step duplicate field", `{"key":"p"}`, `,"settings_setup":{"steps":[{"title":"t","fields":["p","p"]}]}`},
		{"step too many fields", `{"key":"p"}`, `,"settings_setup":{"steps":[{"title":"t","fields":[` + repeatSettings(65, func(int) string { return `"p"` }) + `]}]}`},
		{"step unknown test", `{"key":"p"}`, `,"settings_setup":{"steps":[{"title":"t","fields":["p"],"test":"nope"}]}`},
		{"step record test", v2List, `,"settings_tests":[{"id":"rec","label":"l","record":true}],"settings_setup":{"steps":[{"title":"t","fields":["conns"],"test":"rec"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			src := v2Manifest(c.settings, c.extra)
			if !json.Valid([]byte(src)) {
				t.Fatalf("test case is not valid JSON: %s", src)
			}
			parseErr, verifyErr := bothInstallPaths(t, src)
			if parseErr == nil {
				t.Errorf("ParseManifest accepted %s", src)
			}
			if verifyErr == nil {
				t.Errorf("VerifyManifest accepted %s", src)
			}
		})
	}
}

// The edges of each bound are accepted, so the refusals above are the
// bounds and not something stricter.
func TestSettingsV2_AcceptedEdges(t *testing.T) {
	cases := []struct {
		name, settings, extra string
	}{
		{"64 settings", repeatSettings(64, func(i int) string { return `{"key":"k` + strings.Repeat("x", i) + `"}` }), ""},
		{"credential-named text", `{"key":"api_key","type":"text"}`, ""},
		{"credential-named endpoint", `{"key":"api_token_url","type":"endpoint"}`, ""},
		{"options_ask with no options", `{"key":"p","type":"choice","options_ask":true}`, ""},
		{"options_ask free default", `{"key":"p","type":"choice","options_ask":true,"default_value":"gpt-x"}`, ""},
		{"max_len 4096", `{"key":"p","type":"text","max_len":4096}`, ""},
		{"max_len on untyped", `{"key":"p","max_len":10,"default_value":"abc"}`, ""},
		{"min equals max", `{"key":"p","type":"number","min":3,"max":3,"default_value":3}`, ""},
		{"negative number default", `{"key":"p","type":"number","min":-10,"default_value":-5}`, ""},
		{"untyped object default (legacy)", `{"key":"p","default_value":{"a":1}}`, ""},
		{"untyped number default (legacy)", `{"key":"p","default_value":4711}`, ""},
		{"secret number default (legacy)", `{"key":"p","type":"secret","default_value":8080}`, ""},
		{"20 max_items", `{"key":"conns","type":"list","fields":[{"key":"name","type":"choice","options":[{"value":"a","label":"x"}]}],"title_field":"name","max_items":20}`, ""},
		{"8 tabs", `{"key":"p"}`, `,"settings_tabs":[` + repeatSettings(8, func(i int) string { return `{"id":"t` + strings.Repeat("x", i) + `","label":"l"}` }) + `]`},
		{"32-char tab id", `{"key":"p"}`, `,"settings_tabs":[{"id":"t` + strings.Repeat("x", 31) + `","label":"l"}]`},
		{"4 tests", `{"key":"p"}`, `,"settings_tests":[` + repeatSettings(4, func(i int) string { return `{"id":"t` + strings.Repeat("x", i) + `","label":"l"}` }) + `]`},
		{"8 steps", `{"key":"p"}`, `,"settings_setup":{"steps":[` + repeatSettings(8, func(int) string { return `{"title":"t","fields":["p"]}` }) + `]}`},
		{"guide_url at 512 bytes", `{"key":"p","guide_url":"https://example.com/` + strings.Repeat("a", 512-len("https://example.com/")) + `"}`, ""},
		{"map with no default", `{"key":"m","type":"map","map_over":"payment_methods","value":{"type":"bool"}}`, ""},
		{"map bool default", `{"key":"m","type":"map","map_over":"payment_methods","value":{"type":"bool"},"default_value":{"cash":true}}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parseErr, verifyErr := bothInstallPaths(t, v2Manifest(c.settings, c.extra))
			if parseErr != nil {
				t.Errorf("ParseManifest refused: %v", parseErr)
			}
			if verifyErr != nil {
				t.Errorf("VerifyManifest refused: %v", verifyErr)
			}
		})
	}
}

// Errors keep the existing "manifest setting %q: ..." style and name the
// record a bad field lives in.
func TestSettingsV2_ErrorNamesTheSetting(t *testing.T) {
	cases := []struct{ settings, extra, want string }{
		{`{"key":"p","type":"text","min":1}`, "", `manifest setting "p": `},
		{`{"key":"conns","type":"list","fields":[{"key":"name"},{"key":"n","type":"number","default_value":"1"}],"title_field":"name","max_items":5}`, "", `manifest setting "conns" field "n": `},
		{`{"key":"p"}`, `,"settings_tabs":[{"id":"Main","label":"l"}]`, `manifest settings_tabs[0]: `},
		{`{"key":"p"}`, `,"settings_tests":[{"id":"a","label":""}]`, `manifest settings_tests[0]: `},
		{`{"key":"p"}`, `,"settings_setup":{"steps":[{"title":"t","fields":["nope"]}]}`, `manifest settings_setup.steps[0]: `},
	}
	for _, c := range cases {
		parseErr, verifyErr := bothInstallPaths(t, v2Manifest(c.settings, c.extra))
		for _, err := range []error{parseErr, verifyErr} {
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want error containing %q, got %v", c.want, err)
			}
		}
	}
}

// The fixture ut-cloud reuses (by path) uses every new field and installs
// through both paths; marshal → re-parse → marshal is stable.
func TestSettingsV2_FullFixture(t *testing.T) {
	src, err := os.ReadFile(settingsV2FixturePath)
	if err != nil {
		t.Fatal(err)
	}
	parseErr, verifyErr := bothInstallPaths(t, string(src))
	if parseErr != nil || verifyErr != nil {
		t.Fatalf("fixture refused: parse=%v verify=%v", parseErr, verifyErr)
	}
	m, err := ParseManifest(bytes.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	first, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := ParseManifest(bytes.NewReader(first))
	if err != nil {
		t.Fatalf("re-parse of marshalled fixture refused: %v", err)
	}
	second, err := json.Marshal(m2)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("marshal is not stable:\n%s\n%s", first, second)
	}
	// Every new JSON tag appears in the marshalled fixture, so the fixture
	// really exercises the whole schema (and a dropped tag fails here).
	for _, tag := range []string{
		`"settings_tabs"`, `"settings_tests"`, `"settings_setup"`, `"steps"`, `"record":true`, `"test":"connection"`,
		`"tab"`, `"required"`, `"min"`, `"max"`, `"max_len"`, `"placeholder"`, `"fields"`, `"title_field"`,
		`"max_items"`, `"map_over"`, `"value"`, `"options_ask"`, `"depends_on"`, `"guide"`, `"guide_url"`,
		`"paste"`, `"notice"`,
		`"type":"text"`, `"type":"bool"`, `"type":"number"`, `"type":"percent"`, `"type":"money"`, `"type":"list"`, `"type":"map"`,
	} {
		if !bytes.Contains(first, []byte(tag)) {
			t.Errorf("fixture does not exercise %s", tag)
		}
	}
	// Integer defaults survive as integers (the ut-cloud signer's float64
	// round-trip must not turn 1250 into 1250.0 or 1.25e+03).
	for _, want := range []string{`"default_value":1250`, `"default_value":30`, `"default_value":{"std":700}`} {
		if !bytes.Contains(first, []byte(want)) {
			t.Errorf("marshalled fixture lacks %s: %s", want, first)
		}
	}
	conns := m.SettingDecl("connections")
	if conns == nil || len(conns.Fields) != 6 || conns.TitleField != "name" || conns.MaxItems != 10 {
		t.Fatalf("connections decl = %+v", conns)
	}
	tk := m.SettingDecl("takeaway_rate_overrides")
	if tk == nil || tk.Value == nil || tk.Value.Type != SettingTypePercent || tk.Value.Min == nil || *tk.Value.Min != 1 {
		t.Fatalf("takeaway decl = %+v", tk)
	}
	if m.SettingsSetup == nil || len(m.SettingsSetup.Steps) != 2 || m.SettingsSetup.Steps[1].Test != "connection" {
		t.Fatalf("settings_setup = %+v", m.SettingsSetup)
	}
}

// A manifest using none of the v2 fields marshals to exactly the bytes it
// did before them (pinned from the pre-#4156 code), or every signature
// already issued stops verifying (S8).
func TestSettingsV2_LegacyManifestBytesUnchanged(t *testing.T) {
	const src = `{"id":"t.legacy","name":"Legacy","version":"1.2.3","description":"d","author":"a","website":"https://example.test","entrypoint":"main.wasm","runtime":"wasm","canonical_type":"integration","device_arch":"any","min_pos_version":"0.9.0","signature":"","artifact_hash":"abc",
"entries":[{"type":"page","key":"home","label":"Home","route":"/plugin/t.legacy/home"}],
"settings":[
 {"key":"mode","type":"choice","default_value":"a","label":"t.mode","help":"t.mode.help","options":[{"value":"a","label":"t.a"},{"value":"b","label":"t.b"}]},
 {"key":"api_key","type":"secret","default_value":"","label":"t.key","show_when":{"key":"mode","in":["b"]}},
 {"key":"server","type":"endpoint","default_value":"http://127.0.0.1:8080"},
 {"key":"overrides","default_value":{"7":1900},"scope":"global"},
 {"key":"port","default_value":4711}
],
"hooks":[{"event":"sale.completed","action":"x.y","priority":2}],
"permissions":["events:receive"]}`
	const want = `{"id":"t.legacy","name":"Legacy","version":"1.2.3","description":"d","author":"a","website":"https://example.test","entrypoint":"main.wasm","executable":"","runtime":"wasm","canonical_type":"integration","device_arch":"any","min_pos_version":"0.9.0","signature":"","artifact_hash":"abc","entries":[{"type":"page","key":"home","label":"Home","route":"/plugin/t.legacy/home"}],"settings":[{"key":"mode","default_value":"a","type":"choice","label":"t.mode","help":"t.mode.help","options":[{"value":"a","label":"t.a"},{"value":"b","label":"t.b"}]},{"key":"api_key","default_value":"","type":"secret","label":"t.key","show_when":{"key":"mode","in":["b"]}},{"key":"server","default_value":"http://127.0.0.1:8080","type":"endpoint"},{"key":"overrides","default_value":{"7":1900},"scope":"global"},{"key":"port","default_value":4711}],"hooks":[{"event":"sale.completed","action":"x.y","priority":2}],"permissions":["events:receive"]}`
	m, err := ParseManifest(strings.NewReader(src))
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("legacy manifest bytes changed:\n got %s\nwant %s", got, want)
	}
}

// ut-docs#4885: a visibility cycle (show_when chains, including through a
// tab's show_when) is refused at both install paths; acyclic chains stay.
func TestSettingsV2_ShowWhenCycles(t *testing.T) {
	two := `,"settings_tabs":[{"id":"t1","label":"l"},{"id":"t2","label":"l","show_when":{"key":"a","in":["x"]}}]`
	cases := []struct {
		name, settings, extra, want string // want "" = accepted
	}{
		{"two-cycle", `{"key":"a","show_when":{"key":"b","in":["x"]}},{"key":"b","show_when":{"key":"a","in":["x"]}}`, "", "show_when cycle a → b → a"},
		{"three-cycle", `{"key":"a","show_when":{"key":"b","in":["x"]}},{"key":"b","show_when":{"key":"c","in":["x"]}},{"key":"c","show_when":{"key":"a","in":["x"]}}`, "", "show_when cycle a → b → c → a"},
		{"tab controlled by its own setting", `{"key":"a","tab":"main"}`, `,"settings_tabs":[{"id":"main","label":"l","show_when":{"key":"a","in":["x"]}}]`, "show_when cycle a → a"},
		{"cycle through a tab", `{"key":"a","tab":"main"},{"key":"b","show_when":{"key":"a","in":["x"]}},{"key":"c"}`, `,"settings_tabs":[{"id":"main","label":"l","show_when":{"key":"b","in":["x"]}}]`, "show_when cycle a → b → a"},
		{"record field cycle", `{"key":"conns","type":"list","fields":[{"key":"name","show_when":{"key":"b","in":["x"]}},{"key":"b","show_when":{"key":"name","in":["x"]}}],"title_field":"name","max_items":5}`, "", "show_when cycle name → b → name"},
		{"cycle through a key repeated in another scope", `{"key":"a","show_when":{"key":"b","in":["x"]}},{"key":"a","scope":"register"},{"key":"b","show_when":{"key":"a","in":["x"]}}`, "", "show_when cycle a → b → a"},
		{"chain a<-b<-c", `{"key":"a","show_when":{"key":"b","in":["x"]}},{"key":"b","show_when":{"key":"c","in":["x"]}},{"key":"c"}`, "", ""},
		{"tab controlled from another tab", `{"key":"a","tab":"t1"},{"key":"b","tab":"t2"}`, two, ""},
		{"tab controlled from no tab", `{"key":"a"},{"key":"b","tab":"main"}`, `,"settings_tabs":[{"id":"main","label":"l","show_when":{"key":"a","in":["x"]}}]`, ""},
		{"record field chain", `{"key":"conns","type":"list","fields":[{"key":"name","show_when":{"key":"b","in":["x"]}},{"key":"b"}],"title_field":"name","max_items":5}`, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parseErr, verifyErr := bothInstallPaths(t, v2Manifest(c.settings, c.extra))
			for _, err := range []error{parseErr, verifyErr} {
				if c.want == "" && err != nil {
					t.Errorf("refused: %v", err)
				}
				if c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
					t.Errorf("want error containing %q, got %v", c.want, err)
				}
			}
		})
	}
}
