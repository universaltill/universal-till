package pages

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#5785: Reports still printed the archived-period dates, the Turkish
// fiscal-device counts, the Sales KPI and the "N days" options with Latin
// digits/ISO dates in ar/fa. They must follow the locale like the rest.
func TestReportsEODArchive_FiscalAndPeriodUseLocaleDigits(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	// fixed-date:ok archived-report period label, no real-clock check reads it
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO report_archive(id,kind,period,content_json) VALUES('r1','eod','2025-01-15',
'{"day":"2025-01-15","sales_count":23,"net":500,"fiscal_device":{"serial":"S1","z_nos":[17],"mali_fis":23,"iade_fisi":14,"bilgi_fisi":16,"other":18,"total":41,"till_okc_tenders":39}}')`); err != nil {
		t.Fatal(err)
	}
	// fixed-date:ok archived-report period label, no real-clock check reads it
	if _, err := dp.Db.ExecContext(ctx, `INSERT INTO report_archive(id,kind,period,content_json) VALUES('r2','eod','2025-02-03T22:00:00Z',
'{"from":"2025-02-01T08:00:00Z","to":"2025-02-03T22:00:00Z","sales_count":5,"net":100}')`); err != nil {
		t.Fatal(err)
	}

	rec := getReportsTab(t, mux, "eod", "?lang=en")
	enBody := rec.Body.String()
	for _, want := range []string{
		`<td style="text-align:end">23</td>`,
		"Mismatch: device recorded 41 receipts, the till recorded 39 tenders on it",
		httpx.FormatDate(time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC), "en"),
	} {
		if !strings.Contains(enBody, want) {
			t.Errorf("en: want %q in EOD archive", want)
		}
	}

	from := time.Date(2025, 2, 1, 8, 0, 0, 0, time.UTC).Local()
	to := time.Date(2025, 2, 3, 22, 0, 0, 0, time.UTC).Local()

	for _, locale := range []string{"ar", "fa"} {
		t.Run(locale, func(t *testing.T) {
			rec := getReportsTab(t, mux, "eod", "?lang="+locale)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			body := rec.Body.String()
			for _, n := range []int{23, 14, 16, 18, 39} {
				latin := httpx.FormatQtyLatin(float64(n), locale)
				loc := httpx.FormatQty(float64(n), locale)
				if loc == latin {
					t.Fatalf("%s: test setup: FormatQty(%d) did not change digit shape", locale, n)
				}
				cell := `<td style="text-align:end">` + loc + `</td>`
				if !strings.Contains(body, cell) {
					t.Errorf("%s: want %q in fiscal table", locale, cell)
				}
				if strings.Contains(body, `<td style="text-align:end">`+latin+`</td>`) {
					t.Errorf("%s: found Latin cell for %d", locale, n)
				}
			}
			if !strings.Contains(body, `<td style="text-align:end">17</td>`) {
				t.Errorf("%s: Z number should stay Latin", locale)
			}
			for _, n := range []int{41, 39} {
				if !strings.Contains(body, httpx.FormatQty(float64(n), locale)) {
					t.Errorf("%s: mismatch line lacks locale digits for %d", locale, n)
				}
			}
			if strings.Contains(body, "device recorded 41") {
				t.Errorf("%s: mismatch line with Latin digits still present", locale)
			}
			day := httpx.FormatDate(time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC), locale)
			if !strings.Contains(body, "<td>"+day) {
				t.Errorf("%s: want period cell %q", locale, day)
			}
			if strings.Contains(body, "<td>2025-01-15") || strings.Contains(body, "<td>2025-02-03T") {
				t.Errorf("%s: raw ISO period in cell text", locale)
			}
			for _, tm := range []time.Time{from, to} {
				if want := httpx.FormatDateTime(tm, locale); !strings.Contains(body, want) {
					t.Errorf("%s: want range bound %q", locale, want)
				}
			}
			for _, p := range []string{"2025-01-15", "2025-02-03T22:00:00Z"} {
				if !strings.Contains(body, `hx-post="/api/reports/eod/print/`+p+`"`) {
					t.Errorf("%s: print URL for %s must stay raw", locale, p)
				}
			}
		})
	}
}

func TestReportsPage_SalesCountAndDayOptionsUseLocaleDigits(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	const n = 12
	for i := 0; i < n; i++ {
		if _, err := dp.Db.ExecContext(t.Context(), fmt.Sprintf(`INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('d%d','D%03d','completed','sale','GBP',100,0,20,120,datetime('now'))`, i, i)); err != nil {
			t.Fatal(err)
		}
	}
	body := getReportsPage(t, mux, "?lang=en").Body.String()
	for _, want := range []string{`<div class="kpi-value">12</div>`, `>14 days<`, `value="14"`} {
		if !strings.Contains(body, want) {
			t.Errorf("en: want %q", want)
		}
	}
	for _, locale := range []string{"ar", "fa"} {
		t.Run(locale, func(t *testing.T) {
			body := getReportsPage(t, mux, "?lang="+locale).Body.String()
			if httpx.FormatQty(n, locale) == httpx.FormatQtyLatin(n, locale) || httpx.FormatQty(14, locale) == httpx.FormatQtyLatin(14, locale) {
				t.Fatalf("%s: test setup: digits did not change", locale)
			}
			if want := `<div class="kpi-value">` + httpx.FormatQty(n, locale) + `</div>`; !strings.Contains(body, want) {
				t.Errorf("%s: want %q", locale, want)
			}
			if !strings.Contains(body, `value="14"`) {
				t.Errorf("%s: option value must stay Latin", locale)
			}
			if strings.Contains(body, `>14 `) {
				t.Errorf("%s: Latin day-count option", locale)
			}
			if want := ">" + httpx.FormatQty(14, locale) + " "; !strings.Contains(body, want) {
				t.Errorf("%s: want option %q", locale, want)
			}
		})
	}
}
