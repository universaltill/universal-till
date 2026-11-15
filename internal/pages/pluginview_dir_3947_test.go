package pages

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pluginview"
)

// Plugin-supplied text runs in its own direction (ut-docs#3947): a Latin
// literal on an fa till keeps its full stop at its own end, so every
// element that holds one carries dir="auto" — or, where its alignment or
// list marker must stay with the page (table cells, list items, tile
// labels), is wrapped in <bdi>. Number and money cells keep the page
// direction (#3891 covers them).
func TestPluginView_LiteralsCarryDirAuto_3947(t *testing.T) {
	raw := `{"document":{"version":1,"title":{"literal":"Lit title."},"components":[
{"type":"heading","text":{"literal":"Lit heading."}},
{"type":"text","text":{"literal":"Lit text."}},
{"type":"notice","level":"warn","text":{"literal":"Lit notice."}},
{"type":"list","items":[{"literal":"Lit item one."},{"literal":"Lit item two."}]},
{"type":"stat_tiles","tiles":[{"label":{"literal":"Lit tile."},"value":3}]},
{"type":"table","columns":[{"label":{"literal":"Lit col."},"kind":"text"},{"label":{"literal":"Lit amount."},"kind":"money"}],
 "rows":[["Lit cell.",{"minor":250,"currency":"GBP"}],["",null]]},
{"type":"wizard_step","steps":[{"literal":"Lit step."},{"literal":"Lit step two."}],"current":1},
{"type":"empty_state","title":{"literal":"Lit empty."},"body":{"literal":"Lit body."}}]}}`
	doc, err := pluginview.DecodeViewAnswer([]byte(raw), pluginview.Context{PluginID: "com.example.dir"})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	httpx.RenderWith(pluginViewFiles, httpx.FuncsFor("fa"))("pluginview_body", pluginViewBody{Route: "/plugin/x", View: doc.Prepare("fa")})(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	for _, lit := range []string{"Lit title.", "Lit heading.", "Lit text.", "Lit notice.", "Lit item one.", "Lit item two.",
		"Lit tile.", "Lit col.", "Lit amount.", "Lit cell.", "Lit step.", "Lit step two.", "Lit empty.", "Lit body."} {
		// The element whose text this literal is: its opening tag must carry dir="auto".
		re := regexp.MustCompile(`<([a-z0-9]+)\b([^<>]*)>` + regexp.QuoteMeta(lit) + `<`)
		m := re.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("%q not rendered as an element's whole text:\n%s", lit, body)
			continue
		}
		if m[1] != "bdi" && !strings.Contains(m[2], `dir="auto"`) {
			t.Errorf("element holding %q lacks dir=\"auto\": <%s%s>", lit, m[1], m[2])
		}
	}
	// A money cell keeps the page direction.
	if !strings.Contains(body, `<td class="num">`) || strings.Contains(body, `<td class="num" dir=`) || strings.Contains(body, `<td class="num"><bdi>`) {
		t.Error("a money cell is isolated, or missing")
	}
	// An empty text cell stays empty, so table-cards.js can still mark it blank.
	if strings.Contains(body, `<bdi></bdi>`) {
		t.Error("an empty text cell renders an empty <bdi>")
	}
}
