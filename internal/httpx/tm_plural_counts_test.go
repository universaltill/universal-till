package httpx

import (
	"html/template"
	"strings"
	"testing"
)

// ut-docs#4480: the category item/button counts are ICU plural keys
// (`*_plural`, ADR-0160 §8; the old %d keys stay for language packs). This
// renders them through the real template func from the SHIPPED core locale
// files, so a wrong branch set or a mistranslated form fails here. Digits are
// shaped by the locale (ar Arabic-Indic, fa Extended Arabic-Indic).
func TestShippedCountPluralKeys_RenderEveryCategory(t *testing.T) {
	InitI18n(realI18n(t), "en")
	t.Cleanup(func() { InitI18n(nil, "en") })

	counts := []int{0, 1, 2, 5, 11, 100}
	cases := map[string]map[string][]string{
		"categories.item_count_plural": {
			"en": {"0 items", "1 item", "2 items", "5 items", "11 items", "100 items"},
			"ar": {"لا أصناف", "صنف واحد", "صنفان", "٥ أصناف", "١١ صنفًا", "١٠٠ صنف"},
			"fa": {"۰ کالا", "۱ کالا", "۲ کالا", "۵ کالا", "۱۱ کالا", "۱۰۰ کالا"},
			"tr": {"0 ürün", "1 ürün", "2 ürün", "5 ürün", "11 ürün", "100 ürün"},
		},
		"products.category_button_count_plural": {
			"en": {"0 buttons", "1 button", "2 buttons", "5 buttons", "11 buttons", "100 buttons"},
			"ar": {"لا أزرار", "زر واحد", "زران", "٥ أزرار", "١١ زرًا", "١٠٠ زر"},
			"fa": {"۰ دکمه", "۱ دکمه", "۲ دکمه", "۵ دکمه", "۱۱ دکمه", "۱۰۰ دکمه"},
			"tr": {"0 düğme", "1 düğme", "2 düğme", "5 düğme", "11 düğme", "100 düğme"},
		},
	}
	for key, byLocale := range cases {
		for loc, want := range byLocale {
			tpl := template.Must(template.New("x").Funcs(FuncsFor(loc)).Parse(`{{ TM "` + key + `" "count" .N }}`))
			for i, n := range counts {
				var b strings.Builder
				if err := tpl.Execute(&b, map[string]any{"N": n}); err != nil {
					t.Fatal(err)
				}
				if b.String() != want[i] {
					t.Errorf("%s %s count=%d: got %q, want %q", key, loc, n, b.String(), want[i])
				}
			}
		}
	}
}
