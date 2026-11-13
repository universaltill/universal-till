package pages

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pluginview"
)

// catalog.item.update.v1 through the real confirm route: a manager's
// confirm changes the item and audits; a cashier's is refused with the
// database untouched (sale-only rule, ut-docs#3079).
func TestAgentConfirm_UpdateAsManagerAndCashier_4128(t *testing.T) {
	h := newAgentHarness(t)
	tea := h.item("Tea")
	args := `{"changes":[{"item_id":"` + tea + `","name":"Green tea","price_minor":275}]}`
	id := h.propose("mgr-1", "manager", "sess", "catalog.item.update.v1", args)
	if rec := h.post(id, "confirm", "mgr-1", "manager", "sess"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Changes applied: 2") {
		t.Fatalf("confirm = %d %s", rec.Code, rec.Body.String())
	}
	if n := h.count(`SELECT COUNT(*) FROM items WHERE id = ? AND name = 'Green tea' AND base_price = 275`, tea); n != 1 {
		t.Fatal("item not updated")
	}
	if n := h.count(`SELECT COUNT(*) FROM audit_log WHERE action = 'agent.action.confirmed' AND actor_id = 'mgr-1'`); n != 1 {
		t.Fatalf("confirmed audit rows = %d", n)
	}
	cup := h.item("Cup")
	id = h.propose("csh-1", "cashier", "sess", "catalog.item.update.v1", `{"changes":[{"item_id":"`+cup+`","name":"Hacked"}]}`)
	if rec := h.post(id, "confirm", "csh-1", "cashier", "sess"); rec.Code == http.StatusOK {
		t.Fatalf("cashier confirm = %d", rec.Code)
	}
	if n := h.count(`SELECT COUNT(*) FROM items WHERE id = ? AND name = 'Cup'`, cup); n != 1 {
		t.Fatal("cashier changed an item")
	}
}

// item_list: core resolves names, thumbnails and links from its own
// database for an operator who may manage the catalogue; a cashier or an
// unknown id gets nothing.
func TestAgentItemList_CoreDrawsNamesThumbsLinks_4128(t *testing.T) {
	h := newAgentHarness(t)
	tea := h.item("Tea")
	if _, err := h.d.Db.Exec(`INSERT INTO item_images(id, item_id, role, path) VALUES ('im1', ?, 'thumbnail', ?)`, tea, "/public/assets/items/"+tea+"/thumb.png"); err != nil {
		t.Fatal(err)
	}
	cup := h.item("Cup <b>x</b>")
	raw := `{"document":{"version":1,"components":[{"type":"item_list","items":["` + tea + `","` + cup + `","nope"],"missing":1}]}}`
	doc, err := pluginview.DecodeViewAnswer([]byte(raw), pluginview.Context{PluginID: agentPluginID})
	if err != nil {
		t.Fatal(err)
	}
	render := func(user, role string) string {
		v := doc.Prepare("en")
		ctx := auth.WithSessionKey(auth.WithUser(httptest.NewRequest(http.MethodGet, "/", nil), auth.User{ID: user, Role: role}).Context(), "sess")
		resolveAgentConfirms(ctx, h.d, &v, agentPluginID, "en")
		rec := httptest.NewRecorder()
		httpx.RenderWith(pluginViewFiles, httpx.FuncsFor("en"))("pluginview_body", pluginViewBody{Route: "/plugin/x", View: v})(rec, httptest.NewRequest(http.MethodGet, "/", nil))
		return rec.Body.String()
	}
	body := render("mgr-1", "manager")
	for _, want := range []string{"Tea", `href="/catalog?item=` + tea + `"`, `src="/public/assets/items/` + tea + `/thumb.png"`,
		`href="/catalog?missing=1"`, "Cup &lt;b&gt;x&lt;/b&gt;"} {
		if !strings.Contains(body, want) {
			t.Errorf("manager list lacks %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, "nope") || strings.Contains(body, "<b>x</b>") {
		t.Error("unknown id or unescaped name shown")
	}
	if got := render("csh-1", "cashier"); strings.Contains(got, "Tea") || strings.Contains(got, "/catalog") {
		t.Errorf("cashier sees the list: %s", got)
	}
}
