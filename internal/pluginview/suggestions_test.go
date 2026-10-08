package pluginview

import (
	"strings"
	"testing"
)

// suggestions (ADR-0121 §7, ut-docs#3873): the one component that reaches a
// core screen, only inside a core seam, with one core-defined effect.

func seamCtx(s Seam) Context {
	c := testCtx
	c.Seam = s
	return c
}

func sugg(items string) string {
	return doc(`{"type":"suggestions","items":[` + items + `]}`)
}

const basketItem = `{"label":{"literal":"Tea"},"detail":{"key":"plugin.demo.hello"},"effect":{"add_to_basket":{"sku":"4006381333931","qty":2}}}`
const fieldsItem = `{"label":{"literal":"Oat latte"},"effect":{"apply_fields":{"name":"Oat latte","sku":"OL-1","barcode":"123","category":"Coffee","price":350}}}`

func TestSuggestions_ValidInSeam_3873(t *testing.T) {
	d, err := DecodeViewAnswer([]byte(sugg(basketItem+`,{"label":{"literal":"Default qty"},"effect":{"add_to_basket":{"sku":"X-1"}}}`)), seamCtx(SeamSellIdentify))
	if err != nil {
		t.Fatalf("valid add_to_basket suggestions refused: %v", err)
	}
	v := d.Prepare("en")
	s := v.Components[0]
	if s.Type != "suggestions" || len(s.Suggestions) != 2 {
		t.Fatalf("prepared = %+v", s)
	}
	if got := s.Suggestions[0]; got.Label != "Tea" || got.Detail != "plugin.demo.hello" || got.SKU != "4006381333931" || got.Qty != 2 {
		t.Errorf("first suggestion = %+v", got)
	}
	if got := s.Suggestions[1]; got.SKU != "X-1" || got.Qty != 1 || got.Detail != "" {
		t.Errorf("default qty suggestion = %+v, want qty 1", got)
	}

	d, err = DecodeViewAnswer([]byte(sugg(fieldsItem)), seamCtx(SeamItemForm))
	if err != nil {
		t.Fatalf("valid apply_fields suggestions refused: %v", err)
	}
	f := d.Prepare("en").Components[0].Suggestions[0]
	if f.SKU != "" || f.Fields["name"] != "Oat latte" || f.Fields["category"] != "Coffee" || f.Fields["price"] != int64(350) {
		t.Errorf("apply_fields suggestion = %+v", f)
	}
}

// A candidate may name a thumbnail: a blob in the answering plugin's own
// store (#3957). Validation checks only the name; core resolves it against
// that plugin's store when it renders.
func TestSuggestions_Thumbnail_3957(t *testing.T) {
	raw := sugg(`{"label":{"literal":"Oat"},"thumbnail":"oat-latte_1.png","effect":{"add_to_basket":{"sku":"OL-1"}}},` + basketItem)
	d, err := DecodeViewAnswer([]byte(raw), seamCtx(SeamSellIdentify))
	if err != nil {
		t.Fatalf("thumbnail refused: %v", err)
	}
	s := d.Prepare("en").Components[0].Suggestions
	if s[0].Thumbnail != "oat-latte_1.png" || s[1].Thumbnail != "" {
		t.Fatalf("thumbnails = %q, %q; want oat-latte_1.png and none", s[0].Thumbnail, s[1].Thumbnail)
	}
	// The item form's apply_fields candidates may carry one too.
	if _, err := DecodeViewAnswer([]byte(sugg(`{"label":{"literal":"Oat"},"thumbnail":"a.webp","effect":{"apply_fields":{"name":"Oat"}}}`)), seamCtx(SeamItemForm)); err != nil {
		t.Fatalf("thumbnail on apply_fields refused: %v", err)
	}
	// 128 bytes is the longest name.
	if _, err := DecodeViewAnswer([]byte(sugg(`{"label":{"literal":"x"},"thumbnail":"`+strings.Repeat("a", 128)+`","effect":{"add_to_basket":{"sku":"A"}}}`)), seamCtx(SeamSellIdentify)); err != nil {
		t.Fatalf("128-byte thumbnail name refused: %v", err)
	}
}

func TestSuggestions_Refusals_3873(t *testing.T) {
	many := strings.TrimSuffix(strings.Repeat(basketItem+",", MaxSuggestions+1), ",")
	long := strings.Repeat("a", MaxStringBytes+1)
	sku129 := strings.Repeat("A", 129)
	fields := func(f string) string {
		return sugg(`{"label":{"literal":"x"},"effect":{"apply_fields":{` + f + `}}}`)
	}
	basket := func(b string) string {
		return sugg(`{"label":{"literal":"x"},"effect":{"add_to_basket":` + b + `}}`)
	}
	thumb := func(v string) string {
		return sugg(`{"label":{"literal":"x"},"thumbnail":` + v + `,"effect":{"add_to_basket":{"sku":"A"}}}`)
	}
	sell, form := seamCtx(SeamSellIdentify), seamCtx(SeamItemForm)
	cases := []struct {
		name string
		raw  string
		c    Context
		want string
	}{
		// Placement.
		{"page context", sugg(basketItem), testCtx, "core seam"},
		{"unknown seam", sugg(basketItem), seamCtx("sale.footer"), "seam"},
		{"apply_fields at sell identify", sugg(fieldsItem), sell, "apply_fields"},
		{"add_to_basket at item form", sugg(basketItem), form, "add_to_basket"},
		{"form in seam", doc(`{"type":"form","action":"save","submit":{"literal":"s"},"fields":[]}`), sell, "seam"},
		{"button in seam", doc(`{"type":"button","action":"go","label":{"literal":"x"}}`), sell, "seam"},
		{"table in seam", doc(`{"type":"table","columns":[{"label":{"literal":"c"},"kind":"text"}],"rows":[]}`), sell, "seam"},
		{"heading in seam", doc(`{"type":"heading","text":{"literal":"x"}}`), sell, "seam"},
		// Items.
		{"no items", sugg(``), sell, "items"},
		{"too many items", sugg(many), sell, "items"},
		// thumbnail (#3957): one blob name in the plugin's own store.
		{"thumbnail with a scheme", thumb(`"blob:1"`), sell, "thumbnail"},
		{"thumbnail path", thumb(`"../other/x.png"`), sell, "thumbnail"},
		{"thumbnail url", thumb(`"https://evil/x.png"`), sell, "thumbnail"},
		{"thumbnail upper case", thumb(`"X.PNG"`), sell, "thumbnail"},
		{"thumbnail dot-dot", thumb(`".."`), sell, "thumbnail"},
		{"thumbnail empty", thumb(`""`), sell, "thumbnail"},
		{"thumbnail too long", thumb(`"` + strings.Repeat("a", 129) + `"`), sell, "thumbnail"},
		{"thumbnail not a string", thumb(`1`), sell, "thumbnail"},
		{"thumbnail object", thumb(`{"blob":"x.png"}`), sell, "thumbnail"},
		{"unknown item field", sugg(`{"label":{"literal":"x"},"href":"https://evil","effect":{"add_to_basket":{"sku":"A"}}}`), sell, "unknown field"},
		{"no label", sugg(`{"effect":{"add_to_basket":{"sku":"A"}}}`), sell, "exactly one"},
		{"foreign label key", sugg(`{"label":{"key":"nav.home"},"effect":{"add_to_basket":{"sku":"A"}}}`), sell, "own locale bundle"},
		{"long detail", sugg(`{"label":{"literal":"x"},"detail":{"literal":"` + long + `"},"effect":{"add_to_basket":{"sku":"A"}}}`), sell, "exceeds"},
		// Effect: exactly one.
		{"no effect", sugg(`{"label":{"literal":"x"}}`), sell, "exactly one"},
		{"empty effect", sugg(`{"label":{"literal":"x"},"effect":{}}`), sell, "exactly one"},
		{"both effects", sugg(`{"label":{"literal":"x"},"effect":{"add_to_basket":{"sku":"A"},"apply_fields":{"name":"x"}}}`), sell, "exactly one"},
		{"unknown effect", sugg(`{"label":{"literal":"x"},"effect":{"open_url":"https://evil"}}`), sell, "unknown field"},
		// add_to_basket.
		{"empty sku", basket(`{"sku":""}`), sell, "sku"},
		{"no sku", basket(`{"qty":1}`), sell, "sku"},
		{"sku too long", basket(`{"sku":"` + sku129 + `"}`), sell, "sku"},
		{"sku control char", basket(`{"sku":"A\u0007B"}`), sell, "sku"},
		{"sku leading space", basket(`{"sku":" A"}`), sell, "sku"},
		{"sku trailing space", basket(`{"sku":"A "}`), sell, "sku"},
		{"qty zero", basket(`{"sku":"A","qty":0}`), sell, "qty"},
		{"qty negative", basket(`{"sku":"A","qty":-1}`), sell, "qty"},
		{"qty over 999", basket(`{"sku":"A","qty":1000}`), sell, "qty"},
		{"qty decimal", basket(`{"sku":"A","qty":1.5}`), sell, "qty"},
		{"qty string", basket(`{"sku":"A","qty":"2"}`), sell, "qty"},
		{"unknown basket field", basket(`{"sku":"A","price":1}`), sell, "unknown field"},
		// apply_fields: the item-form v1 allow-list.
		// "allow-list", not just the field name: a non-string "cost" would
		// also fail the string check, which is not what this pins.
		{"cost not allowed", fields(`"cost":1`), form, "allow-list"},
		{"string field off the allow-list", fields(`"cost":"1"`), form, "allow-list"},
		{"allowed and disallowed together", fields(`"name":"Tea","vat_rate":"7"`), form, "allow-list"},
		{"price float", fields(`"price":1.5`), form, "price"},
		{"price negative", fields(`"price":-1`), form, "price"},
		{"price string", fields(`"price":"3.50"`), form, "price"},
		{"price exponent", fields(`"price":1e3`), form, "price"},
		{"empty apply_fields", fields(``), form, "at least one"},
		{"name not a string", fields(`"name":1`), form, "name"},
		{"name control char", fields(`"name":"a\u0000b"`), form, "name"},
		{"long category", fields(`"category":"` + long + `"`), form, "category"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := DecodeViewAnswer([]byte(c.raw), c.c)
			if err == nil {
				t.Fatalf("accepted, want refusal containing %q", c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want it to contain %q", err, c.want)
			}
		})
	}
	// Exactly MaxSuggestions is fine.
	max := strings.TrimSuffix(strings.Repeat(basketItem+",", MaxSuggestions), ",")
	if _, err := DecodeViewAnswer([]byte(sugg(max)), sell); err != nil {
		t.Fatalf("%d items refused: %v", MaxSuggestions, err)
	}
	// Qty bounds inclusive; a 128-byte sku is fine.
	for _, ok := range []string{`{"sku":"A","qty":1}`, `{"sku":"A","qty":999}`, `{"sku":"` + strings.Repeat("A", 128) + `"}`} {
		if _, err := DecodeViewAnswer([]byte(basket(ok)), sell); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	if _, err := DecodeViewAnswer([]byte(fields(`"price":0`)), form); err != nil {
		t.Errorf("price 0 refused: %v", err)
	}
}

// In a seam the document may hold only text, notice and suggestions, and
// the answer may not be a redirect or a job: the seam has no route to go
// to or post back to.
func TestSuggestions_SeamAnswerShapes_3873(t *testing.T) {
	sell := seamCtx(SeamSellIdentify)
	mixed := doc(`{"type":"text","text":{"literal":"Found 1"}},{"type":"notice","level":"info","text":{"key":"plugin.demo.hello"}},` +
		`{"type":"suggestions","items":[` + basketItem + `]}`)
	if _, err := DecodeViewAnswer([]byte(mixed), sell); err != nil {
		t.Fatalf("text + notice + suggestions refused in a seam: %v", err)
	}
	if _, _, err := DecodeJobResult([]byte(mixed), sell); err != nil {
		t.Fatalf("job result refused in a seam: %v", err)
	}
	if _, _, err := DecodeJobResult([]byte(`{"redirect":"/plugin/demo"}`), sell); err == nil || !strings.Contains(err.Error(), "seam") {
		t.Fatalf("redirect job result in a seam: err = %v, want a seam refusal", err)
	}
	if _, err := DecodeActionAnswer([]byte(`{"redirect":"/plugin/demo"}`), sell); err == nil {
		t.Fatal("redirect action answer accepted in a seam")
	}
	if _, err := DecodeActionAnswer([]byte(`{"job":{"event":"com.demo.identify"}}`), sell); err == nil || !strings.Contains(err.Error(), "seam") {
		t.Fatalf("job action answer in a seam: err = %v, want a seam refusal", err)
	}
	// The same redirect is still fine on a plugin page.
	if _, to, err := DecodeJobResult([]byte(`{"redirect":"/plugin/demo"}`), testCtx); err != nil || to != "/plugin/demo" {
		t.Fatalf("page redirect = %q %v", to, err)
	}
}
