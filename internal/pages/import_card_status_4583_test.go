package pages

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestImport_FlaggedRowStatusStaysOnPhoneCard pins ut-docs#4583: at the
// phone tier (table-cards.js) a row's fifth labelled cell waits behind
// More, so the status cell of a warned or skipped row carries
// data-card-keep and stays on the collapsed card — otherwise a tint is the
// only sign the row needs attention. A clean row's "ok" keeps waiting
// behind More.
func TestImport_FlaggedRowStatusStaysOnPhoneCard(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newImportTestDeps(t)
	initAuthTestI18n(t)
	mux := http.NewServeMux()
	registerImport(mux, dp)

	resp := postImportPreview(t, mux, "items.csv", []byte(
		"Name,SKU,Price,Tax rate\n"+
			"Oddball,T8,1.00,abc\n"+
			"Plain,P1,1.00,19\n"+
			",S1,1.00,19\n"))

	rowRe := regexp.MustCompile(`(?s)<tr[^>]*>.*?</tr>`)
	var warned, clean, skipped string
	for _, row := range rowRe.FindAllString(resp, -1) {
		switch {
		case strings.Contains(row, "Oddball"):
			warned = row
		case strings.Contains(row, "<td>Plain</td>"):
			clean = row
		case strings.HasPrefix(row, `<tr class="muted">`): // the nameless row
			skipped = row
		}
	}
	keep := `<td data-card-keep>`
	if warned == "" || !strings.Contains(warned, keep+`<span class="row-warn-icon"`) {
		t.Fatalf("a warned row's status cell must carry data-card-keep, got row: %s", warned)
	}
	if clean == "" || strings.Contains(clean, "data-card-keep") {
		t.Fatalf("a clean row's status must stay behind More (no data-card-keep), got row: %s", clean)
	}
	if skipped == "" || !strings.Contains(skipped, keep) {
		t.Fatalf("a skipped row's status cell must carry data-card-keep, got row: %s", skipped)
	}
}
