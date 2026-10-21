package pages

import (
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/print"
)

// ut-docs#1122: the printed Z report carries a "cleared before payment"
// section when at least one basket was cleared, and none when zero.
func TestEODPrint_ClearedBasketsSectionWhenNonZero(t *testing.T) {
	rep := data.EODReport{Day: "2026-10-14", ClearedBasketCount: 3, ClearedBasketTotal: 1299}
	got := buildSectionsEOD(t, "en", rep)[1:]
	want := []string{"", "CLEARED BEFORE PAYMENT", "Cleared baskets (3)  £12.99"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("footer:\n got %q\nwant %q", got, want)
	}
}

func TestEODPrint_ClearedBasketsSectionOmittedWhenZero(t *testing.T) {
	got := strings.Join(buildSectionsEOD(t, "en", data.EODReport{Day: "2026-10-14"}), "\n")
	if strings.Contains(got, "CLEARED") || strings.Contains(got, "Cleared baskets") {
		t.Fatalf("a day with no cleared baskets must print no section:\n%s", got)
	}
}

func TestEODPrint_ClearedBasketsSectionTranslated(t *testing.T) {
	rep := data.EODReport{Day: "2026-10-14", ClearedBasketCount: 2, ClearedBasketTotal: 500}
	for _, locale := range []string{"tr", "fa", "ar"} {
		joined := strings.Join(buildSectionsEOD(t, locale, rep), "\n")
		if strings.Contains(joined, "CLEARED") || strings.Contains(joined, "Cleared baskets") {
			t.Errorf("%s: section still English:\n%s", locale, joined)
		}
		if !strings.Contains(joined, "(2)") && !strings.Contains(joined, "(۲)") && !strings.Contains(joined, "(٢)") {
			t.Errorf("%s: count missing:\n%s", locale, joined)
		}
		for _, line := range buildSectionsEOD(t, locale, data.EODReport{Day: "2026-10-14", ClearedBasketCount: 12, ClearedBasketTotal: 123456})[1:] {
			if n := utf8.RuneCountInString(line); n > print.Width {
				t.Errorf("%s: line %q is %d runes, over print.Width %d", locale, line, n, print.Width)
			}
			if locale == "tr" {
				if _, err := charmap.Windows1254.NewEncoder().String(line); err != nil {
					t.Errorf("tr line %q does not encode in Windows-1254: %v", line, err)
				}
			}
		}
	}
}
