package pluginview

import (
	"strings"
	"testing"
)

var testCtx = Context{
	PluginID:  "com.demo",
	OwnKeys:   map[string]bool{"plugin.demo.title": true, "plugin.demo.hello": true},
	OwnRoutes: []string{"/plugin/demo", "/plugin/demo/other"},
}

func doc(components string) string {
	return `{"document":{"version":1,"title":{"key":"plugin.demo.title"},"components":[` + components + `]}}`
}

func TestDecodeViewAnswer_Valid(t *testing.T) {
	raw := doc(`
		{"type":"heading","text":{"key":"plugin.demo.hello"}},
		{"type":"text","text":{"literal":"<script>alert(1)</script>"}},
		{"type":"notice","level":"warn","text":{"literal":"careful"}},
		{"type":"stat_tiles","tiles":[{"label":{"literal":"Sales"},"value":{"minor":123456,"currency":"EUR"}},{"label":{"literal":"Count"},"value":42},{"label":{"literal":"Best"},"value":"Coffee"}]},
		{"type":"table","columns":[{"label":{"literal":"Item"},"kind":"text"},{"label":{"literal":"Qty"},"kind":"number"},{"label":{"literal":"Total"},"kind":"money"}],
		 "rows":[["Tea", 2, {"minor":350,"currency":"GBP"}], [null, -1.5, null]]},
		{"type":"list","items":[{"literal":"one"},{"key":"plugin.demo.hello"}]},
		{"type":"empty_state","title":{"literal":"Nothing yet"},"body":{"literal":"Add one"},"action":{"action":"create","label":{"literal":"Create"}}},
		{"type":"button","action":"refresh","label":{"literal":"Refresh"},"style":"secondary"},
		{"type":"form","action":"save","submit":{"literal":"Save"},"fields":[
			{"name":"name","label":{"literal":"Name"},"kind":"text","required":true,"value":"x"},
			{"name":"count","label":{"literal":"Count"},"kind":"number","value":"3"},
			{"name":"price","label":{"literal":"Price"},"kind":"money","value":250},
			{"name":"mode","label":{"literal":"Mode"},"kind":"select","options":[{"value":"a","label":{"literal":"A"}},{"value":"b","label":{"literal":"B"}}],"value":"b"},
			{"name":"on","label":{"literal":"On"},"kind":"toggle","value":true},
			{"name":"api_key","label":{"literal":"API key"},"kind":"secret"}
		]}`)
	d, err := DecodeViewAnswer([]byte(raw), testCtx)
	if err != nil {
		t.Fatalf("valid document refused: %v", err)
	}
	if len(d.Components) != 9 {
		t.Fatalf("components = %d, want 9", len(d.Components))
	}
}

func TestDecodeViewAnswer_Refusals(t *testing.T) {
	long := strings.Repeat("a", MaxStringBytes+1)
	many := strings.TrimSuffix(strings.Repeat(`{"type":"text","text":{"literal":"x"}},`, MaxComponents+1), ",")
	rows := strings.TrimSuffix(strings.Repeat(`["x"],`, MaxTableRows+1), ",")
	cols := strings.TrimSuffix(strings.Repeat(`{"label":{"literal":"c"},"kind":"text"},`, MaxTableCols+1), ",")
	fields := ""
	for i := 0; i <= MaxFormFields; i++ {
		fields += `{"name":"f` + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + itoa(i) + `","label":{"literal":"F"},"kind":"text"},`
	}
	fields = strings.TrimSuffix(fields, ",")
	big := `{"document":{"version":1,"components":[{"type":"text","text":{"literal":"` + strings.Repeat("a", MaxDocumentBytes) + `"}}]}}`

	cases := []struct{ name, raw, want string }{
		{"not json", `{"document":`, "json"},
		{"no document", `{}`, "document"},
		{"unknown top-level field", `{"document":{"version":1,"components":[]},"html":"<b>"}`, "unknown field"},
		{"bad version", `{"document":{"version":2,"components":[]}}`, "version"},
		{"unknown component", doc(`{"type":"iframe","src":"x"}`), "unknown component"},
		{"unknown field", doc(`{"type":"text","text":{"literal":"x"},"html":"<b>x</b>"}`), "unknown field"},
		{"unknown text field", doc(`{"type":"text","text":{"literal":"x","html":"y"}}`), "unknown field"},
		{"text both key and literal", doc(`{"type":"text","text":{"key":"plugin.demo.hello","literal":"x"}}`), "exactly one"},
		{"text neither", doc(`{"type":"text","text":{}}`), "exactly one"},
		{"core key", doc(`{"type":"text","text":{"key":"nav.home"}}`), "own locale bundle"},
		{"foreign plugin key", doc(`{"type":"text","text":{"key":"plugin.other.title"}}`), "own locale bundle"},
		{"title foreign key", `{"document":{"version":1,"title":{"key":"plugin.other.title"},"components":[]}}`, "own locale bundle"},
		{"oversized document", big, "exceeds"},
		{"too many components", doc(many), "components"},
		{"long literal", doc(`{"type":"text","text":{"literal":"` + long + `"}}`), "exceeds"},
		{"bad notice level", doc(`{"type":"notice","level":"fatal","text":{"literal":"x"}}`), "level"},
		{"bad currency", doc(`{"type":"stat_tiles","tiles":[{"label":{"literal":"x"},"value":{"minor":1,"currency":"eur"}}]}`), "currency"},
		{"currency not in the till's registry (KWD has 3 decimals; would render 10x off)", doc(`{"type":"stat_tiles","tiles":[{"label":{"literal":"x"},"value":{"minor":1234,"currency":"KWD"}}]}`), "currency"},
		{"unknown currency in a table", doc(`{"type":"table","columns":[{"label":{"literal":"c"},"kind":"money"}],"rows":[[{"minor":1,"currency":"XYZ"}]]}`), "currency"},
		{"negative money field value (the input cannot submit it)", doc(`{"type":"form","action":"save","submit":{"literal":"s"},"fields":[{"name":"a","label":{"literal":"x"},"kind":"money","value":-250}]}`), "value"},
		{"money float", doc(`{"type":"stat_tiles","tiles":[{"label":{"literal":"x"},"value":{"minor":1.5,"currency":"EUR"}}]}`), "minor"},
		{"number with exponent", doc(`{"type":"stat_tiles","tiles":[{"label":{"literal":"x"},"value":1e9}]}`), "number"},
		{"money cell in text column", doc(`{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[[{"minor":1,"currency":"EUR"}]]}`), "kind"},
		{"row width mismatch", doc(`{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[["a","b"]]}`), "cells"},
		{"too many rows", doc(`{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[` + rows + `]}`), "rows"},
		{"too many cols", doc(`{"type":"table","columns":[` + cols + `],"rows":[]}`), "columns"},
		{"bad column kind", doc(`{"type":"table","columns":[{"label":{"literal":"c"},"kind":"html"}],"rows":[]}`), "kind"},
		{"bad action name", doc(`{"type":"button","action":"../../admin","label":{"literal":"x"}}`), "action"},
		{"uppercase action", doc(`{"type":"button","action":"Save","label":{"literal":"x"}}`), "action"},
		{"bad button style", doc(`{"type":"button","action":"go","label":{"literal":"x"},"style":"link"}`), "style"},
		{"button url", doc(`{"type":"button","action":"go","label":{"literal":"x"},"href":"https://evil"}`), "unknown field"},
		{"bad field name", doc(`{"type":"form","action":"save","submit":{"literal":"s"},"fields":[{"name":"_action","label":{"literal":"x"},"kind":"text"}]}`), "name"},
		{"duplicate field name", doc(`{"type":"form","action":"save","submit":{"literal":"s"},"fields":[{"name":"a","label":{"literal":"x"},"kind":"text"},{"name":"a","label":{"literal":"x"},"kind":"text"}]}`), "twice"},
		{"bad field kind", doc(`{"type":"form","action":"save","submit":{"literal":"s"},"fields":[{"name":"a","label":{"literal":"x"},"kind":"file"}]}`), "kind"},
		{"secret value echoed", doc(`{"type":"form","action":"save","submit":{"literal":"s"},"fields":[{"name":"a","label":{"literal":"x"},"kind":"secret","value":"hunter2"}]}`), "secret"},
		{"select without options", doc(`{"type":"form","action":"save","submit":{"literal":"s"},"fields":[{"name":"a","label":{"literal":"x"},"kind":"select"}]}`), "options"},
		{"select value not an option", doc(`{"type":"form","action":"save","submit":{"literal":"s"},"fields":[{"name":"a","label":{"literal":"x"},"kind":"select","options":[{"value":"a","label":{"literal":"A"}}],"value":"z"}]}`), "option"},
		{"toggle string value", doc(`{"type":"form","action":"save","submit":{"literal":"s"},"fields":[{"name":"a","label":{"literal":"x"},"kind":"toggle","value":"yes"}]}`), "value"},
		{"money field float value", doc(`{"type":"form","action":"save","submit":{"literal":"s"},"fields":[{"name":"a","label":{"literal":"x"},"kind":"money","value":2.5}]}`), "value"},
		{"too many form fields", doc(`{"type":"form","action":"save","submit":{"literal":"s"},"fields":[` + fields + `]}`), "fields"},
		{"redirect in view answer", `{"redirect":"/plugin/demo"}`, "document"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DecodeViewAnswer([]byte(c.raw), testCtx)
			if err == nil {
				t.Fatalf("accepted, want refusal containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
}

func TestDecodeActionAnswer(t *testing.T) {
	if a, err := DecodeActionAnswer([]byte(`{"redirect":"/plugin/demo/other"}`), testCtx); err != nil || a.Redirect != "/plugin/demo/other" {
		t.Fatalf("own route redirect = %q, %v", a.Redirect, err)
	}
	for _, bad := range []string{"/plugin/other", "/settings", "https://evil.example/plugin/demo", "//evil.example", "/plugin/demo/other?x=1", ""} {
		raw := `{"redirect":"` + bad + `"}`
		if _, err := DecodeActionAnswer([]byte(raw), testCtx); err == nil {
			t.Errorf("redirect to %q accepted", bad)
		}
	}
	if _, err := DecodeActionAnswer([]byte(`{"redirect":"/plugin/demo","document":{"version":1,"components":[]}}`), testCtx); err == nil {
		t.Error("redirect AND document accepted")
	}
	a, err := DecodeActionAnswer([]byte(doc(`{"type":"text","text":{"literal":"done"}}`)), testCtx)
	if err != nil || a.Redirect != "" || a.Document == nil || a.Job != "" {
		t.Fatalf("document answer = %+v %v", a, err)
	}
}

// ADR-0121 §8 (ut-docs#3908): {"job":{"event":"<plugin-id>.<name>"}} runs
// the plugin's own event as a job; exactly one answer shape, strictly.
func TestDecodeActionAnswer_Job_3908(t *testing.T) {
	a, err := DecodeActionAnswer([]byte(`{"job":{"event":"com.demo.identify"}}`), testCtx)
	if err != nil || a.Job != "com.demo.identify" || a.Document != nil || a.Redirect != "" {
		t.Fatalf("job answer = %+v, %v", a, err)
	}
	for name, raw := range map[string]string{
		"foreign namespace":  `{"job":{"event":"com.other.identify"}}`,
		"prefix lookalike":   `{"job":{"event":"com.demox.identify"}}`,
		"bare plugin id":     `{"job":{"event":"com.demo"}}`,
		"core event":         `{"job":{"event":"sale.completed"}}`,
		"ui ask":             `{"job":{"event":"ui.action.ask"}}`,
		"upper case":         `{"job":{"event":"com.demo.Identify"}}`,
		"space":              `{"job":{"event":"com.demo.a b"}}`,
		"empty event":        `{"job":{"event":""}}`,
		"no event":           `{"job":{}}`,
		"null job":           `{"job":null}`,
		"too long":           `{"job":{"event":"com.demo.` + strings.Repeat("a", 300) + `"}}`,
		"unknown job field":  `{"job":{"event":"com.demo.identify","deadline_s":900}}`,
		"unknown top field":  `{"job":{"event":"com.demo.identify"},"poll_ms":10}`,
		"job and document":   `{"job":{"event":"com.demo.identify"},"document":{"version":1,"components":[]}}`,
		"job and redirect":   `{"job":{"event":"com.demo.identify"},"redirect":"/plugin/demo"}`,
		"event not a string": `{"job":{"event":1}}`,
		"job is a string":    `{"job":"com.demo.identify"}`,
		"trailing data":      `{"job":{"event":"com.demo.identify"}} {}`,
	} {
		if a, err := DecodeActionAnswer([]byte(raw), testCtx); err == nil {
			t.Errorf("%s: accepted as %+v", name, a)
		}
	}
	// Without a plugin id to check the namespace against, refuse.
	if _, err := DecodeActionAnswer([]byte(`{"job":{"event":"com.demo.identify"}}`), Context{OwnKeys: testCtx.OwnKeys}); err == nil {
		t.Error("job accepted with no plugin id in the context")
	}
	// A view answer is never a job.
	if _, err := DecodeViewAnswer([]byte(`{"job":{"event":"com.demo.identify"}}`), testCtx); err == nil {
		t.Error("ui.view.ask answer accepted a job")
	}
}

// A job's own answer is a normal action answer: a document or a redirect,
// never another job.
func TestDecodeJobResult_3908(t *testing.T) {
	d, to, err := DecodeJobResult([]byte(doc(`{"type":"text","text":{"literal":"done"}}`)), testCtx)
	if err != nil || d == nil || to != "" {
		t.Fatalf("document result = %v %q %v", d, to, err)
	}
	if _, to, err := DecodeJobResult([]byte(`{"redirect":"/plugin/demo/other"}`), testCtx); err != nil || to != "/plugin/demo/other" {
		t.Fatalf("redirect result = %q %v", to, err)
	}
	if _, _, err := DecodeJobResult([]byte(`{"redirect":"/plugin/other"}`), testCtx); err == nil {
		t.Error("job result redirect to another plugin's route accepted")
	}
	if _, _, err := DecodeJobResult([]byte(`{"job":{"event":"com.demo.identify"}}`), testCtx); err == nil {
		t.Error("job result chaining another job accepted")
	}
}

func itoa(i int) string {
	return strings.TrimLeft(string([]byte{byte('0' + i/100%10), byte('0' + i/10%10), byte('0' + i%10)}), "0")
}

func TestDecodeForm(t *testing.T) {
	vals := map[string][]string{
		"_action":         {"save"},
		"name":            {"Tea"},
		"price":           {"2,50"},
		"_kind.price":     {"money"},
		"bad_price":       {"2.5.0"},
		"_kind.bad_price": {"money"},
		"qty":             {"1,5"},
		"_kind.qty":       {"number"},
		"_kind.on":        {"toggle"},
		"_kind.off":       {"toggle"},
		"on":              {"true"},
		"api_key":         {"s3cret"},
	}
	f, err := DecodeForm(vals, 2, "EUR")
	if err != nil {
		t.Fatalf("DecodeForm: %v", err)
	}
	if f.Action != "save" || f.Values["name"] != "Tea" || f.Values["api_key"] != "s3cret" {
		t.Fatalf("values = %+v", f)
	}
	if m, ok := f.Values["price"].(Money); !ok || m.Minor != 250 || m.Currency != "EUR" {
		t.Fatalf("money field = %#v, want 250 EUR minor units", f.Values["price"])
	}
	if f.Values["qty"] != "1.5" {
		t.Fatalf("number field = %#v, want normalised \"1.5\"", f.Values["qty"])
	}
	if f.Values["on"] != true || f.Values["off"] != false {
		t.Fatalf("toggles = %#v / %#v", f.Values["on"], f.Values["off"])
	}
	if _, has := f.Values["bad_price"]; has || len(f.Invalid) != 1 || f.Invalid[0] != "bad_price" {
		t.Fatalf("invalid = %v, values %v", f.Invalid, f.Values)
	}

	for name, bad := range map[string]map[string][]string{
		"no action":      {"name": {"x"}},
		"bad action":     {"_action": {"../x"}},
		"bad field name": {"_action": {"go"}, "Name": {"x"}},
		"long value":     {"_action": {"go"}, "x": {strings.Repeat("a", MaxStringBytes+1)}},
		"unknown core":   {"_action": {"go"}, "_other": {"x"}},
	} {
		if _, err := DecodeForm(bad, 2, "EUR"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestPrepare_ResolvesAndFormats(t *testing.T) {
	raw := doc(`{"type":"table","columns":[{"label":{"literal":"Total"},"kind":"money"},{"label":{"literal":"N"},"kind":"number"}],"rows":[[{"minor":123456,"currency":"JPY"},1234.5]]},
		{"type":"form","action":"save","submit":{"literal":"Go"},"fields":[{"name":"p","label":{"literal":"P"},"kind":"money","value":250},{"name":"s","label":{"literal":"S"},"kind":"select","options":[{"value":"a","label":{"literal":"A"}},{"value":"b","label":{"literal":"B"}}],"value":"b"}]}`)
	d, err := DecodeViewAnswer([]byte(raw), testCtx)
	if err != nil {
		t.Fatal(err)
	}
	v := d.Prepare("en")
	if got := v.Components[0].Rows[0][0].Value; got != "¥123,456" {
		t.Errorf("money cell = %q, want ¥123,456 (minor units, JPY has 0 decimals)", got)
	}
	if got := v.Components[0].Rows[0][1].Value; got != "1,234.5" {
		t.Errorf("number cell = %q", got)
	}
	f := v.Components[1].Fields
	if f[0].Value != "2.50" {
		t.Errorf("money field value = %q, want 2.50", f[0].Value)
	}
	if f[1].Options[0].Selected || !f[1].Options[1].Selected {
		t.Errorf("select selection = %+v", f[1].Options)
	}
	if fa := d.Prepare("fa"); fa.Components[0].Rows[0][1].Value != "۱٬۲۳۴٫۵" {
		t.Errorf("fa number cell = %q", fa.Components[0].Rows[0][1].Value)
	}
}
