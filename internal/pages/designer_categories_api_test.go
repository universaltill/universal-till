package pages

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#2174: the Designer's own thin, catalog_management-gated category
// routes (rename/recolour, create, activate/deactivate, reorder) behind
// the live sale-screen replica. They call the exact CatalogRepo methods
// /categories already uses (no new SQL) and answer the way the replica's
// htmx popovers need: 204 + HX-Trigger: buttons-changed on success (the
// replica root refetches itself), a translated text/html fragment on a
// refusal (swapped into the popover's own message region), never a
// redirect -- there is no page to navigate to.
func newDesignerCategoriesMux(t *testing.T) (*http.ServeMux, *data.CatalogRepo, *auth.User) {
	t.Helper()
	dp := newDesignerTestDeps(t)
	t.Setenv("UT_AUTH", "on")
	mux := http.NewServeMux()
	registerDesigner(mux, dp)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	return mux, data.NewCatalogRepo(dp.Db), &manager
}

func TestDesignerCategories_GatedOnCatalogManagement(t *testing.T) {
	mux, _, _ := newDesignerCategoriesMux(t)
	cashier := auth.User{ID: "c1", Role: "cashier"}

	for _, path := range []string{
		"/api/designer/categories",
		"/api/designer/categories/x",
		"/api/designer/categories/x/active",
		"/api/designer/categories/reorder",
	} {
		if rec := postForm(mux, path, url.Values{"name": {"Drinks"}, "ids": {"x"}}, &cashier); rec.Code != http.StatusForbidden {
			t.Errorf("cashier POST %s = %d, want 403: %s", path, rec.Code, rec.Body.String())
		}
		if rec := postForm(mux, path, url.Values{"name": {"Drinks"}, "ids": {"x"}}, nil); rec.Code != http.StatusForbidden {
			t.Errorf("no-session POST %s = %d, want 403: %s", path, rec.Code, rec.Body.String())
		}
	}
	// The replica fragment itself is gated the same way as /designer.
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/designer/buttons", nil), cashier)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cashier GET /ui/designer/buttons = %d, want 403", rec.Code)
	}
}

func TestDesignerCategories_CreateRenameRecolour(t *testing.T) {
	mux, repo, manager := newDesignerCategoriesMux(t)

	rec := postForm(mux, "/api/designer/categories", url.Values{"name": {"Drinks"}, "color": {"#0f766e"}}, manager)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("create = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "buttons-changed" {
		t.Fatalf("create HX-Trigger = %q, want buttons-changed", got)
	}
	cats, err := repo.ListCategories(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, c := range cats {
		if c.Name == "Drinks" {
			id = c.ID
			if c.Color != "#0f766e" {
				t.Fatalf("created colour = %q, want #0f766e", c.Color)
			}
		}
	}
	if id == "" {
		t.Fatalf("created category not found in %+v", cats)
	}

	// Whitespace-only name refused with the categories page's own key.
	rec = postForm(mux, "/api/designer/categories", url.Values{"name": {"   "}}, manager)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank create = %d, want 400", rec.Code)
	}
	if want := httpx.T("en", "categories.error.name_required"); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("blank create body %q lacks %q", rec.Body.String(), want)
	}

	// Rename + recolour in one POST, same shape as /api/categories/{id}.
	rec = postForm(mux, "/api/designer/categories/"+id, url.Values{"name": {"Cold Drinks"}, "color": {"#4338ca"}}, manager)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("update = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "buttons-changed" {
		t.Fatalf("update HX-Trigger = %q, want buttons-changed", got)
	}
	cats, _ = repo.ListCategories(t.Context())
	for _, c := range cats {
		if c.ID == id && (c.Name != "Cold Drinks" || c.Color != "#4338ca") {
			t.Fatalf("after update: %+v", c)
		}
	}

	// A colour outside the fixed palette is refused (it flows into a CSS
	// custom property -- catalogtypes.ItemColors' allowlist is a security
	// control, same as on /categories).
	rec = postForm(mux, "/api/designer/categories/"+id, url.Values{"name": {"Cold Drinks"}, "color": {"#ff0000"}}, manager)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("off-palette colour = %d, want 400", rec.Code)
	}
	if want := httpx.T("en", "categories.error.color_invalid"); !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("off-palette body %q lacks %q", rec.Body.String(), want)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("refusal Content-Type = %q, want text/html (htmx force-swaps only html fragments)", ct)
	}

	rec = postForm(mux, "/api/designer/categories/nope", url.Values{"name": {"X"}}, manager)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), httpx.T("en", "categories.error.not_found")) {
		t.Fatalf("unknown id = %d %q", rec.Code, rec.Body.String())
	}
}

func TestDesignerCategories_DeactivateAndReactivateEmptyCategory(t *testing.T) {
	mux, repo, manager := newDesignerCategoriesMux(t)
	id, err := repo.CreateCategoryWithColor(t.Context(), "Snacks", "")
	if err != nil {
		t.Fatal(err)
	}
	if rec := postForm(mux, "/api/designer/categories/"+id+"/active", url.Values{"active": {"0"}}, manager); rec.Code != http.StatusNoContent {
		t.Fatalf("deactivate empty category = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if rec := postForm(mux, "/api/designer/categories/"+id+"/active", url.Values{"active": {"1"}}, manager); rec.Code != http.StatusNoContent {
		t.Fatalf("reactivate = %d, want 204: %s", rec.Code, rec.Body.String())
	}
}

func TestDesignerCategories_DeactivateBlockedWhenItemsRemain(t *testing.T) {
	dp := newDesignerTestDeps(t)
	t.Setenv("UT_AUTH", "on")
	mux := http.NewServeMux()
	registerDesigner(mux, dp)
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	repo := data.NewCatalogRepo(dp.Db)

	id, err := repo.CreateCategoryWithColor(t.Context(), "Snacks", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range []string{"a", "b"} {
		if _, err := dp.Db.Exec(`INSERT INTO items (id, sku, name, base_price, is_active, category_id) VALUES (?, ?, ?, 100, 1, ?)`, "it-"+it, "SKU-"+it, "Item "+it, id); err != nil {
			t.Fatalf("seed item: %v", err)
		}
	}
	rec := postForm(mux, "/api/designer/categories/"+id+"/active", url.Values{"active": {"0"}}, &manager)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blocked deactivate = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	// The SAME translated copy /categories shows, count interpolated -- no
	// duplicate key minted for the Designer.
	want := strings.Replace(httpx.T("en", "categories.error.deactivate_blocked"), "%d", "2", 1)
	if !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("blocked body %q lacks %q", rec.Body.String(), want)
	}
	if got := rec.Header().Get("HX-Trigger"); got != "" {
		t.Fatalf("a refused deactivate must not fire a refresh trigger, got %q", got)
	}
	var active int
	if err := dp.Db.QueryRow(`SELECT is_active FROM categories WHERE id = ?`, id).Scan(&active); err != nil || active != 1 {
		t.Fatalf("category must remain active: active=%d err=%v", active, err)
	}
}

func TestDesignerCategories_ReorderMirrorsCategoriesShape(t *testing.T) {
	mux, repo, manager := newDesignerCategoriesMux(t)
	a, _ := repo.CreateCategoryWithColor(t.Context(), "A", "")
	b, _ := repo.CreateCategoryWithColor(t.Context(), "B", "")
	c, _ := repo.CreateCategoryWithColor(t.Context(), "C", "")

	// Repeated "ids" form values in display order (urlencoded).
	rec := postForm(mux, "/api/designer/categories/reorder", url.Values{"ids": {c, a, b}}, manager)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("reorder = %d, want 204: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Trigger"); got != "buttons-changed" {
		t.Fatalf("reorder HX-Trigger = %q, want buttons-changed", got)
	}
	orderOf := func() []string {
		cats, err := repo.ListActiveCategories(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, x := range cats {
			if x.ID == a || x.ID == b || x.ID == c {
				ids = append(ids, x.ID)
			}
		}
		return ids
	}
	if got := orderOf(); strings.Join(got, ",") != strings.Join([]string{c, a, b}, ",") {
		t.Fatalf("order after urlencoded reorder = %v, want [c a b]", got)
	}

	// multipart/form-data (what a browser FormData POST sends) must work
	// too -- ut-docs#2018's own trap.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for _, id := range []string{b, c, a} {
		fw, _ := mw.CreateFormField("ids")
		_, _ = io.WriteString(fw, id)
	}
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/designer/categories/reorder", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req = auth.WithUser(req, *manager)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("multipart reorder = %d: %s", rec.Code, rec.Body.String())
	}
	if got := orderOf(); strings.Join(got, ",") != strings.Join([]string{b, c, a}, ",") {
		t.Fatalf("order after multipart reorder = %v, want [b c a]", got)
	}

	if rec := postForm(mux, "/api/designer/categories/reorder", url.Values{}, manager); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty reorder = %d, want 400", rec.Code)
	}
}

// The replica fragment renders through the SAME buttons.html partial the
// sale screen uses, in Designer mode: an empty category (no quick buttons
// yet) still gets a tab -- ui.BuildCategoryGroups prunes those for the
// sale screen, but an editor that hid a just-created category would be
// useless -- plus the edit affordances (pencil per tab, + tab, popovers).
func TestDesignerReplica_RendersEmptyCategoryAndEditAffordances(t *testing.T) {
	mux, repo, manager := newDesignerCategoriesMux(t)
	id, err := repo.CreateCategoryWithColor(t.Context(), "Brand New", "#0369a1")
	if err != nil {
		t.Fatal(err)
	}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/designer/buttons", nil), *manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/designer/buttons = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="cat-tab-` + id + `"`,                // the empty category's own tab
		`data-cat-id="` + id + `"`,               // its reorderable wrapper
		`data-cat-popover="cat-edit-` + id + `"`, // its pencil
		`id="cat-edit-` + id + `"`,               // its popover
		`hx-post="/api/designer/categories/` + id + `"`,
		`hx-post="/api/designer/categories/` + id + `/active"`,
		`hx-confirm="` + httpx.T("en", "categories.deactivate_confirm") + `"`,
		`data-cat-popover="cat-add"`,
		`hx-post="/api/designer/categories"`,
		`data-testid="designer-edit-toggle"`,
		`hx-get="/ui/designer/buttons"`, // the root refetches ITSELF, not /ui/buttons
		`class="products-finder cat-editable"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("replica fragment lacks %q", want)
		}
	}
	if strings.Contains(body, `href="/designer"`) {
		t.Errorf("replica must not link to /designer from /designer")
	}
}

func TestDesigner_PageHostsReplicaNotFlatGrid(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newDesignerTestDeps(t)
	mux := http.NewServeMux()
	registerDesigner(mux, dp)
	req := httptest.NewRequest(http.MethodGet, "/designer", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /designer = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-get="/ui/designer/buttons"`) {
		t.Fatalf("designer page has no live-replica placeholder")
	}
	if strings.Contains(body, "buttons-grid-admin") || strings.Contains(body, "reorderable-tile") {
		t.Fatalf("designer page still renders the retired flat admin grid")
	}
	if !strings.Contains(body, `id="search"`) {
		t.Fatalf("the add-a-button search panel must stay on the page")
	}
}
