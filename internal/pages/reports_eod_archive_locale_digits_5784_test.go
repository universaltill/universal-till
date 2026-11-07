package pages

import (
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#5784: the Reports -> End of day archive printed its integer counts
// (sales, per-operator sales, no-sale opens, article count) raw, so ar/fa
// showed Latin digits next to localized money. They must follow the locale's
// digit set, like the qty columns beside them.
func TestReportsEODArchive_CountsUseLocaleDigits(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)

	// fixed-date:ok archived-report period label, no real-clock check reads it
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO report_archive(id,kind,period,content_json) VALUES('r1','eod','2025-01-15',
'{"day":"2025-01-15","sales_count":23,"net":500,"operators":[{"cashier_id":"c1","display_name":"Cashier One","count":47,"net":300,"gross":360}],
"no_sale_count":15,"no_sale_operators":[{"cashier_id":"c1","display_name":"Cashier One","count":12}],
"articles":[{"name":"A","qty":2,"net":100,"gross":120},{"name":"B","qty":3,"net":100,"gross":120},{"name":"C","qty":4,"net":100,"gross":120}]}')`); err != nil {
		t.Fatal(err)
	}

	for _, locale := range []string{"ar", "fa"} {
		t.Run(locale, func(t *testing.T) {
			rec := getReportsTab(t, mux, "eod", "?lang="+locale)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
			}
			body := rec.Body.String()
			for _, c := range []struct {
				n    int
				want func(s string) string
			}{
				{23, func(s string) string { return "<td>" + s + "</td>" }},
				{47, func(s string) string { return `<td style="text-align:end">` + s + `</td>` }},
				{12, func(s string) string { return `<td style="text-align:end">` + s + `</td>` }},
				{15, func(s string) string { return `<strong>` + s + `</strong>` }},
				{3, func(s string) string { return "(" + s + ")" }},
			} {
				latin := httpx.FormatQtyLatin(float64(c.n), locale)
				loc := httpx.FormatQty(float64(c.n), locale)
				if loc == latin {
					t.Fatalf("%s: test setup: FormatQty(%d) did not change digit shape", locale, c.n)
				}
				if !strings.Contains(body, c.want(loc)) {
					t.Errorf("%s: want %q (locale digits) in EOD archive", locale, c.want(loc))
				}
				if strings.Contains(body, c.want(latin)) {
					t.Errorf("%s: found %q (Latin digits) in EOD archive", locale, c.want(latin))
				}
			}
		})
	}
}
