package pages

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#5812: the Shrinkage & Loss tab still printed the per-reason event
// count and the tfoot total with Latin digits in ar/fa (#5785 localized the
// rest of Reports). Both cells must follow the locale; en is unchanged.
func TestReportsShrinkageTab_CountsUseLocaleDigits(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newReportsPageTestDeps(t)
	ctx := t.Context()

	const waste, void = 23, 12
	seed := func(reason string, n int) {
		for i := 0; i < n; i++ {
			if _, err := dp.Db.ExecContext(ctx, fmt.Sprintf(`INSERT INTO shrinkage_events (id, reason_category, item_name, quantity, unit_price_minor, extended_value_minor, actor_id, order_type, created_at)
VALUES ('%s%d', '%s', 'Test item', 1, 100, 100, 'mgr1', '', datetime('now'))`, reason, i, reason)); err != nil {
				t.Fatalf("seed shrinkage event: %v", err)
			}
		}
	}
	seed("waste", waste)
	seed("void", void)
	const total = waste + void

	cell := func(n int, bold bool, locale string, latin bool) string {
		s := httpx.FormatQty(float64(n), locale)
		if latin {
			s = httpx.FormatQtyLatin(float64(n), locale)
		}
		if bold {
			return `<td class="muted" style="text-align:end; font-weight:600">` + s + `</td>`
		}
		return `<td class="muted" style="text-align:end">` + s + `</td>`
	}

	for _, locale := range []string{"en", "ar", "fa"} {
		t.Run(locale, func(t *testing.T) {
			rec := getReportsTab(t, mux, "shrinkage", "?days=14&lang="+locale)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			body := rec.Body.String()
			if locale != "en" {
				for _, n := range []int{waste, void, total} {
					if httpx.FormatQty(float64(n), locale) == httpx.FormatQtyLatin(float64(n), locale) {
						t.Fatalf("%s: test setup: FormatQty(%d) did not change digit shape", locale, n)
					}
				}
			}
			for _, c := range []struct {
				n    int
				bold bool
			}{{waste, false}, {void, false}, {total, true}} {
				if want := cell(c.n, c.bold, locale, false); !strings.Contains(body, want) {
					t.Errorf("%s: want %q in shrinkage table", locale, want)
				}
				if locale != "en" {
					if bad := cell(c.n, c.bold, locale, true); strings.Contains(body, bad) {
						t.Errorf("%s: found Latin cell %q", locale, bad)
					}
				}
			}
			if locale == "en" {
				for _, want := range []string{`>23</td>`, `>12</td>`, `>35</td>`} {
					if !strings.Contains(body, want) {
						t.Errorf("en: want %q", want)
					}
				}
			}
		})
	}
}

// ut-docs#5812 (decision, guard not red/green): printed paper keeps Latin
// digits. ESC/POS text mode has no Arabic-Indic / Extended Arabic-Indic
// glyphs (RTL needs bitmap mode, ut-docs#1130), so the Z paper of an ar/fa
// store must carry no such runes -- counts included, fiscal mismatch line
// included. A future "locale digits on paper" fix must come with a bitmap
// renderer, not by dropping this pin.
func TestBuildEODDoc_ArFaPaperKeepsLatinDigits_5812(t *testing.T) {
	for _, locale := range []string{"ar", "fa"} {
		t.Run(locale, func(t *testing.T) {
			newPrintAPITestDeps(t) // loads the translator
			pinDefaultLocale(t, locale)
			rep := data.EODReport{
				Day: "2026-10-14", GeneratedAt: "not-a-timestamp",
				SalesCount: 87, Gross: 123456, RefundCount: 76, RefundTotal: 1500,
				Net: 121956, TaxNet: 20326,
				FiscalDevice: &data.FiscalDeviceWindow{
					Serial: "AV0001234", ZNos: []int64{7},
					MaliFis: 65, Total: 98, TillOKCTenders: 92,
				},
			}
			doc := buildEODDoc(rep, "Shop", "utf8", eodArticlePrintAll, 0)
			all := strings.Join(eodPrintedLines(doc), "\n") + "\n" + string(print.Render(doc))
			for _, r := range all {
				if (r >= 0x0660 && r <= 0x0669) || (r >= 0x06F0 && r <= 0x06F9) {
					t.Fatalf("%s: Z paper contains non-Latin digit %U in:\n%s", locale, r, all)
				}
			}
			// values appear nowhere else on the paper (amounts, serial, date), so a hit is the count itself
			for _, want := range []string{"87", "76", "65", "98", "92"} {
				if !strings.Contains(all, want) {
					t.Errorf("%s: Z paper lacks Latin count %q in:\n%s", locale, want, all)
				}
			}
		})
	}
}
