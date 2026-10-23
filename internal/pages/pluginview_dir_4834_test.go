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

// The rest of a plugin view's plugin-supplied text runs in its own
// direction too (ut-docs#4834, after #3947): form field labels, select
// options, the submit label, button and empty-state action labels, and a
// job's progress message. A <label> or <button> holds a <bdi>; an
// <option> holds text only, so it carries dir="auto". The paging
// indicator is core's own translated text and stays in the page direction.
func TestPluginView_ControlLiteralsIsolated_4834(t *testing.T) {
	raw := `{"document":{"version":1,"components":[
{"type":"button","action":"go","label":{"literal":"Lit button."}},
{"type":"empty_state","title":{"literal":"Lit empty."},"action":{"action":"add","label":{"literal":"Lit empty action."}}},
{"type":"form","action":"save","submit":{"literal":"Lit submit."},"fields":[
 {"name":"t","label":{"literal":"Lit toggle."},"kind":"toggle"},
 {"name":"s","label":{"literal":"Lit select."},"kind":"select","options":[{"value":"a","label":{"literal":"Lit option."}}]},
 {"name":"m","label":{"literal":"Lit money."},"kind":"money"},
 {"name":"n","label":{"literal":"Lit number."},"kind":"number"},
 {"name":"f","label":{"literal":"Lit file."},"kind":"file"},
 {"name":"k","label":{"literal":"Lit secret."},"kind":"secret"},
 {"name":"x","label":{"literal":"Lit text."},"kind":"text"}]},
{"type":"chat","turns":[{"role":"user","text":{"literal":"Hi"}}],"action":"ask"}]}}`
	doc, err := pluginview.DecodeViewAnswer([]byte(raw), pluginview.Context{PluginID: "com.example.dir", Uploads: true})
	if err != nil {
		t.Fatal(err)
	}
	view := doc.Prepare("fa")
	render := func(b pluginViewBody) string {
		rec := httptest.NewRecorder()
		httpx.RenderWith(pluginViewFiles, httpx.FuncsFor("fa"))("pluginview_body", b)(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec.Body.String()
	}
	body := render(pluginViewBody{Route: "/plugin/x", View: view})
	// A running job's poll, and a chat's thinking bubble, with the plugin's message.
	body += render(pluginViewBody{Route: "/plugin/x", Job: &pluginJobPoll{ID: strings.Repeat("a", 32), Message: "Lit job."}})
	var chat *pluginview.ViewChat
	for _, c := range view.Components {
		if c.Chat != nil {
			chat = c.Chat
		}
	}
	if chat == nil {
		t.Fatal("chat component not prepared")
	}
	body += render(pluginViewBody{Route: "/plugin/x", Chat: chat, Job: &pluginJobPoll{ID: strings.Repeat("b", 32), Message: "Lit thinking."}})

	for _, lit := range []string{"Lit button.", "Lit empty action.", "Lit submit.", "Lit toggle.", "Lit select.", "Lit option.",
		"Lit money.", "Lit number.", "Lit file.", "Lit secret.", "Lit text.", "Lit job.", "Lit thinking."} {
		// The literal is an element's whole text, and that element isolates it.
		re := regexp.MustCompile(`<([a-z0-9]+)\b([^<>]*)>` + regexp.QuoteMeta(lit) + `<`)
		m := re.FindStringSubmatch(body)
		if m == nil {
			t.Errorf("%q not rendered as an element's whole text:\n%s", lit, body)
			continue
		}
		if m[1] != "bdi" && !strings.Contains(m[2], ` dir="auto"`) {
			t.Errorf("element holding %q is not isolated: <%s%s>", lit, m[1], m[2])
		}
	}
	// Core's own fallbacks are untouched: no empty <bdi> for a job with no message.
	if strings.Contains(render(pluginViewBody{Route: "/plugin/x", Job: &pluginJobPoll{ID: strings.Repeat("c", 32)}}), "<bdi>") {
		t.Error("a poll with core's own message renders a <bdi>")
	}
}
