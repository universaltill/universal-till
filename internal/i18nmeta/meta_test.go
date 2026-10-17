package i18nmeta

import (
	"strings"
	"testing"
)

const testAreas = `{"default_area":"common","areas":{
  "sale":{"description":"d","namespaces":["pos","basket"]},
  "settings":{"description":"d","namespaces":["translations","basket"]},
  "selforder":{"description":"d","namespaces":["self_order"]}}}`

const testMeta = `{
  "pos.pay":{"description":"Pay button.","review":"standard","max_length":12},
  "translations.title":{"description":"Page title.","review":"standard","placeholders":["the shop name"]},
  "common.x":{"description":"Moved.","review":"standard","area":"selforder"},
  "common.y":{"description":"Unknown area falls back.","review":"standard","area":"nope"}}`

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := parse([]byte(testMeta), []byte(testAreas))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestAreaFor_FollowsUtTranslationsMapping(t *testing.T) {
	c := testCatalog(t)
	for key, want := range map[string]string{
		"pos.pay":              "sale",
		"basket.total":         "sale", // first area listing the namespace wins
		"translations.title":   "settings",
		"common.x":             "selforder", // explicit, known area
		"common.y":             "common",    // explicit but unknown: namespace map, then default
		"mystery.key":          "common",
		"self_order.basket.ok": "selforder",
	} {
		if got := c.AreaFor(key); got != want {
			t.Errorf("AreaFor(%q) = %q, want %q", key, got, want)
		}
	}
}

func TestLookup_ReturnsMetadata(t *testing.T) {
	c := testCatalog(t)
	k, ok := c.Lookup("pos.pay")
	if !ok || k.Description != "Pay button." || k.MaxLength != 12 {
		t.Fatalf("Lookup = %+v %v", k, ok)
	}
	k, _ = c.Lookup("translations.title")
	if len(k.Placeholders) != 1 || k.Placeholders[0] != "the shop name" {
		t.Fatalf("placeholders = %v", k.Placeholders)
	}
	if _, ok := c.Lookup("missing"); ok {
		t.Fatal("missing key reported present")
	}
}

func TestWeblateURL(t *testing.T) {
	got := WeblateURL("sale", "de", "age_check.badge")
	want := `https://translate.universaltill.com/translate/universal-till/sale/de/?q=key%3A%3D%22age_check.badge%22`
	if got != want {
		t.Fatalf("WeblateURL =\n %s\nwant\n %s", got, want)
	}
	if got := WeblateURL("sale", "pt-br", "a.b"); !strings.Contains(got, "/sale/pt_BR/") {
		t.Fatalf("region code not mapped to Weblate's form: %s", got)
	}
	if got := WeblateURL("sale", "zh-hant", "a.b"); !strings.Contains(got, "/zh_Hant/") {
		t.Fatalf("script code not mapped: %s", got)
	}
	if got := WeblateURL("sale", "en", `we"ird`); !strings.Contains(got, `we%5C%22ird`) {
		t.Fatalf("quote in key not escaped for Weblate's query: %s", got)
	}
}

func TestWeblateLanguage(t *testing.T) {
	for in, want := range map[string]string{"de": "de", "DE": "de", "pt-BR": "pt_BR", "pt_br": "pt_BR", "sr-latn": "sr_Latn", "": "en"} {
		if got := WeblateLanguage(in); got != want {
			t.Errorf("WeblateLanguage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuiltinLoads(t *testing.T) {
	c, err := Builtin()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Lookup("common.close"); !ok {
		t.Fatal("embedded en.meta.json lacks common.close")
	}
	if c.AreaFor("translations.title") != "settings" {
		t.Fatalf("translations namespace should map to settings, got %q", c.AreaFor("translations.title"))
	}
}
