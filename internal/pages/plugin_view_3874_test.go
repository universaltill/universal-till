package pages

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#3874 (ADR-0121 build 8d): qr, wizard_step and table paging,
// drawn by core's own partials on a plugin page.
const view3874 = `{"document":{"version":1,"components":[
	{"type":"wizard_step","steps":[{"literal":"Connect"},{"literal":"Check <b>"},{"literal":"Done"}],"current":2},
	{"type":"qr","data":"https://example.com/pair?c=1\"><script>","label":{"literal":"Scan <me>"}},
	{"type":"table","columns":[{"label":{"literal":"Item"},"kind":"text"}],"rows":[["Tea"]],"paging":{"page":2,"pages":3}}]}}`

func TestPluginView_QRWizardPagingRender_3874(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(view3874)
	rec := h.do(http.MethodGet, "/plugin/views?q=tea+%26+milk&page=2", nil, false)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	params, _ := h.lastPay["params"].(map[string]any)
	if params["page"] != "2" {
		t.Errorf("params = %v, want page=2 forwarded to the plugin", params)
	}
	for _, want := range []string{
		// wizard: position in words, aria-current, check mark, escaped label
		`<p class="plugin-view-wizard-pos">` + httpx.T("en", "plugin.view.wizard_step_of")[:4],
		`aria-label="` + httpx.T("en", "plugin.view.wizard_label") + `"`,
		`<li class="done"><span aria-hidden="true">✓ </span>Connect<span class="visually-hidden"> ` + httpx.T("en", "plugin.view.step_done") + `</span></li>`,
		`<li class="current" aria-current="step">Check &lt;b&gt;</li>`,
		`<li class="upcoming">Done</li>`,
		// qr: a core-made PNG data URI, core alt, escaped caption
		`<img src="data:image/png;base64,`,
		`alt="` + httpx.T("en", "plugin.view.qr_alt") + `"`,
		`<figcaption>Scan &lt;me&gt;</figcaption>`,
		// paging: both links on the entry's route, the query kept
		`href="/plugin/views?page=1&amp;q=tea&#43;%26&#43;milk" rel="prev"`,
		`href="/plugin/views?page=3&amp;q=tea&#43;%26&#43;milk" rel="next"`,
		`Page 2 of 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered view missing %q", want)
		}
	}
	if strings.Contains(body, "<script>\"") || strings.Contains(body, "pair?c=1") {
		t.Error("the qr data reached the page as text or a URL")
	}
	for _, k := range []string{"plugin.view.qr_alt", "plugin.view.wizard_step_of", "plugin.view.page_n_of", "plugin.view.page_prev", "plugin.view.page_next", "plugin.view.paging_label", "plugin.view.wizard_label", "plugin.view.page_n", "plugin.view.step_done"} {
		if httpx.T("en", k) == k {
			t.Errorf("%s is not in en.json", k)
		}
	}
}

// After an htmx action the new document's paging links carry only the
// table's own param (a POST has no query).
func TestPluginView_PagingAfterAction_3874(t *testing.T) {
	h := newViewHarness(t)
	h.answerWith(`{"document":{"version":1,"components":[
		{"type":"table","columns":[{"label":{"literal":"Item"},"kind":"text"}],"rows":[["Tea"]],"paging":{"page":1,"has_next":true,"param":"log_page"}}]}}`)
	rec := h.do(http.MethodPost, "/plugin/views", url.Values{"_action": {"refresh"}}, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `href="/plugin/views?log_page=2" rel="next"`) {
		t.Errorf("next link missing:\n%s", body)
	}
	if strings.Contains(body, `rel="prev"`) {
		t.Error("page 1 has a Previous link")
	}
	if !strings.Contains(body, ">Page 1<") {
		t.Error("indicator without a total missing")
	}
}

// A content slot panel has no GET swap of its own, so a paged table there
// shows its page indicator without links (ut-docs#3874 non-goal, follow-up
// card), and the panel's other output is unchanged.
func TestPluginSlot_PagedTableHasNoLinks_3874(t *testing.T) {
	h := newSlotHarness(t)
	h.answerWith(`{"document":{"version":1,"components":[
		{"type":"table","columns":[{"label":{"literal":"Item"},"kind":"text"}],"rows":[["Tea"]],"paging":{"page":2,"pages":3}}]}}`)
	rec := h.do(http.MethodGet, "/ui/slot/reports.panels?days=7&page=2", nil, true)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Page 2 of 3") {
		t.Errorf("indicator missing:\n%s", body)
	}
	if strings.Contains(body, "rel=\"prev\"") || strings.Contains(body, "rel=\"next\"") {
		t.Errorf("a slot panel drew paging links:\n%s", body)
	}
}
