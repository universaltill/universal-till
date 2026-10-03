package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/catalogsync"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3606: ut-docs#2817's optimistic conflict check only worked for
// the item editor, the one dialog that sent the updated_at it LOADED. Every
// other conflict-checked catalogue form sent nothing, so the additional
// till fell back to its own copy — which the admin pull refreshes within a
// second of a main-till change — and a main-till change landing more than a
// second before the save was silently overwritten. Every form that edits an
// existing record now carries the record's updated_at as it was rendered,
// in base_updated_at.

var baseStampRE = regexp.MustCompile(`name="base_updated_at" value="([^"]*)"`)

// formBaseStamp returns the base_updated_at value inside the element that
// opens with marker (up to its closing </form>); found=false when the form
// carries no such field.
func formBaseStamp(t *testing.T, body, marker string) (string, bool) {
	t.Helper()
	seg := rowSegment(t, body, marker, `</form>`)
	m := baseStampRE.FindStringSubmatch(seg)
	if m == nil {
		return "", false
	}
	return m[1], true
}

func execStamps(t *testing.T, dp *common.Deps, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := dp.Db.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

// --- Render: every conflict-checked form carries its record's stamp. ---

func TestBaseStamp_VariantPanelFormsCarryTheirRecordsStamp(t *testing.T) {
	m := newCatalogTill(t)
	execStamps(t, m.dp,
		`UPDATE items SET updated_at = '2026-02-02 10:00:00' WHERE id = 'itm1'`,
		`UPDATE item_variants SET updated_at = '2026-02-02 11:00:00' WHERE id = 'var1'`,
	)
	rec := getWithUser(m.mux, "/api/catalog/item-variants?item_id=itm1", &catalogMgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET item-variants = %d %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if v, ok := formBaseStamp(t, body, `<form id="vf-var1"`); !ok || v != "2026-02-02 11:00:00" {
		t.Errorf("variant edit form base_updated_at = %q (present %v), want the variant's stamp", v, ok)
	}
	if v, ok := formBaseStamp(t, body, `<form id="vf-new"`); ok {
		t.Errorf("the variant CREATE form carries base_updated_at %q — a create has no record to conflict on", v)
	}
	for _, action := range []string{"/api/catalog/item-cost", "/api/catalog/item-lead-time", "/api/catalog/item-reorder-level"} {
		if v, ok := formBaseStamp(t, body, `hx-post="`+action+`"`); !ok || v != "2026-02-02 10:00:00" {
			t.Errorf("%s form base_updated_at = %q (present %v), want the item's stamp", action, v, ok)
		}
	}
	// The panel names the item stamp it rendered, so catalog.html can keep
	// the item editor's own base in step after a panel save.
	if !strings.Contains(body, `data-item-updated-at="2026-02-02 10:00:00"`) {
		t.Errorf("panel root does not carry data-item-updated-at")
	}
}

func TestBaseStamp_ModifierGroupEditFormCarriesItsStamp(t *testing.T) {
	m := newCatalogTill(t)
	execStamps(t, m.dp, `INSERT INTO item_modifier_groups (id, name, updated_at) VALUES ('grp1', 'Milk', '2026-02-02 12:00:00')`)
	rec := getWithUser(m.mux, "/modifiers", &catalogMgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /modifiers = %d", rec.Code)
	}
	body := rec.Body.String()
	card := rowSegment(t, body, `data-group-id="grp1"`, `</form>`)
	if m := baseStampRE.FindStringSubmatch(card); m == nil || m[1] != "2026-02-02 12:00:00" {
		t.Errorf("group edit form base_updated_at = %v, want the group's stamp; form:\n%s", m, card)
	}
	if v, ok := formBaseStamp(t, body, `modifier-admin-group-new"`); ok {
		t.Errorf("the group CREATE form carries base_updated_at %q", v)
	}
}

func TestBaseStamp_CategoryDialogCarriesTheRowsStamp(t *testing.T) {
	m := newCatalogTill(t)
	execStamps(t, m.dp, `UPDATE categories SET updated_at = '2026-02-02 13:00:00' WHERE id = 'cat1'`)
	rec := getWithUser(m.mux, "/categories", &catalogMgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /categories = %d", rec.Code)
	}
	body := rec.Body.String()
	row := rowSegment(t, body, `<tr class="category-row" data-id="cat1"`, `>`)
	// record-dialog.js prefills the form from the row's data-field-*
	// attributes (and restores the blank default for create).
	if !strings.Contains(row, `data-field-base_updated_at="2026-02-02 13:00:00"`) {
		t.Errorf("category row does not hand the dialog its stamp:\n%s", row)
	}
	form := rowSegment(t, body, `<form id="category-form"`, `</form>`)
	if !strings.Contains(form, `<input type="hidden" name="base_updated_at"`) {
		t.Errorf("category dialog form has no base_updated_at field")
	}
}

func TestBaseStamp_DesignerCategoryFormCarriesItsStamp(t *testing.T) {
	mux, d := newButtonsMuxRealSession(t)
	seedOneButton(t, d)
	execStamps(t, d, `INSERT INTO categories (id, name, sort_order, is_active, updated_at) VALUES ('c-st', 'Stamped', 0, 1, '2026-02-02 14:00:00')`)
	rec := getWithUser(mux, "/ui/buttons?mode=edit", &auth.User{ID: "m1", Role: "manager"})
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/buttons?mode=edit = %d", rec.Code)
	}
	body := rec.Body.String()
	if v, ok := formBaseStamp(t, body, `id="designer-cat-form-c-st"`); !ok || v != "2026-02-02 14:00:00" {
		t.Errorf("Designer category form base_updated_at = %q (present %v), want the category's stamp", v, ok)
	}
	if v, ok := formBaseStamp(t, body, `id="designer-cat-form-new"`); ok {
		t.Errorf("the Designer CREATE form carries base_updated_at %q", v)
	}
}

// --- The bug itself: a stale LOADED stamp is refused even after the
// additional till's own copy has caught up. ---

// The variant panel was rendered on the additional till; then the variant
// changed on the main till AND the pull brought it here. Saving the panel's
// form must still be a conflict — before ut-docs#3606 the form carried no
// stamp, the fallback read this till's already-refreshed copy and the
// main till's change was overwritten.
func TestCatalogWriteThrough_VariantStaleLoadedStampRefusedAfterPull(t *testing.T) {
	p := newCatalogWriteThroughPair(t)
	rec := getWithUser(p.replica.mux, "/api/catalog/item-variants?item_id=itm1", &catalogMgr)
	loaded, _ := formBaseStamp(t, rec.Body.String(), `<form id="vf-var1"`)
	for _, dp := range []*common.Deps{p.main.dp, p.replica.dp} {
		execStamps(t, dp, `UPDATE item_variants SET name = 'Grande', updated_at = '2026-01-01 09:00:00' WHERE id = 'var1'`)
	}
	form := url.Values{"panelItem": {"itm1"}, "id": {"var1"}, "itemId": {"itm1"}, "isActive": {"1"}, "price": {"400"}, "name": {"Large"}, "sku": {"SKU1-L"}}
	if loaded != "" {
		form.Set("base_updated_at", loaded)
	}
	rec = postFormHtmx(p.replica.mux, "/api/catalog/variant", form, &catalogMgr)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), catalogConflictEN) {
		t.Fatalf("stale variant save = %d %q (loaded stamp %q), want 409 with the reload message", rec.Code, rec.Body.String(), loaded)
	}
	var name string
	if err := p.main.dp.Db.QueryRow(`SELECT name FROM item_variants WHERE id = 'var1'`).Scan(&name); err != nil || name != "Grande" {
		t.Fatalf("main till variant name = %q (%v), want the main till's change kept", name, err)
	}
}

// Same for the /categories dialog, with the stamp taken from the rendered
// row exactly as record-dialog.js copies it.
func TestCatalogWriteThrough_CategoryStaleLoadedStampRefusedAfterPull(t *testing.T) {
	p := newCatalogWriteThroughPair(t)
	rec := getWithUser(p.replica.mux, "/categories", &catalogMgr)
	loaded := ""
	if m := regexp.MustCompile(`data-field-base_updated_at="([^"]*)"`).FindStringSubmatch(rec.Body.String()); m != nil {
		loaded = m[1]
	}
	for _, dp := range []*common.Deps{p.main.dp, p.replica.dp} {
		execStamps(t, dp, `UPDATE categories SET name = 'Coffee', updated_at = '2026-01-01 09:00:00' WHERE id = 'cat1'`)
	}
	rec = postFormHtmx(p.replica.mux, "/api/categories/cat1", url.Values{"name": {"Hot drinks"}, "base_updated_at": {loaded}}, &catalogMgr)
	// The /categories dialog answers a refusal as its own 400 fragment.
	if rec.Code < 400 || !strings.Contains(rec.Body.String(), "changed on another till") || catalogCategoryName(t, p.main.dp, "cat1") != "Coffee" {
		t.Fatalf("stale category save = %d %q (loaded stamp %q), want a conflict and the main till's name kept (got %q)",
			rec.Code, rec.Body.String(), loaded, catalogCategoryName(t, p.main.dp, "cat1"))
	}
}

// --- Self-conflict: a surface re-rendered from this till's own (not yet
// pulled) copy keeps showing the pre-save stamp. Saving again from it must
// not read as a conflict with this till's own previous save. ---

func TestCatalogWriteThrough_DesignerSecondSaveIsNotASelfConflict(t *testing.T) {
	p := newCatalogWriteThroughPair(t)
	catalogsync.ResetRecentSaves()
	t.Cleanup(catalogsync.ResetRecentSaves)
	// What the Designer rendered (and re-renders after buttons-changed,
	// from this till's copy, until the pull lands).
	shown, _, err := data.NewCatalogRepo(p.replica.dp.Db).CatalogUpdatedAt(t.Context(), data.CatalogKindCategory, "cat1")
	if err != nil || shown == "" {
		t.Fatalf("replica stamp = %q (%v)", shown, err)
	}
	save := func(name string) *httptest.ResponseRecorder {
		return postFormHtmx(p.replica.mux, "/api/designer/categories/cat1", url.Values{"name": {name}, "base_updated_at": {shown}}, &catalogMgr)
	}
	if rec := save("Hot drinks"); rec.Code != http.StatusNoContent {
		t.Fatalf("first save = %d %q", rec.Code, rec.Body.String())
	}
	if rec := save("Warm drinks"); rec.Code != http.StatusNoContent {
		t.Fatalf("second save from the same (re-rendered) form = %d %q — a conflict with this till's own save", rec.Code, rec.Body.String())
	}
	if got := catalogCategoryName(t, p.main.dp, "cat1"); got != "Warm drinks" {
		t.Fatalf("main till category = %q", got)
	}
	// A change made on the main till after that is still a conflict.
	execStamps(t, p.main.dp, `UPDATE categories SET name = 'Coffee', updated_at = '2999-01-01 00:00:00' WHERE id = 'cat1'`)
	rec := save("Stale")
	if rec.Code != http.StatusConflict || catalogCategoryName(t, p.main.dp, "cat1") != "Coffee" {
		t.Fatalf("save over a later main-till change = %d %q, want 409", rec.Code, rec.Body.String())
	}
}

// ut-docs#3606 review: the third consecutive save from a surface that
// re-renders from this till's own copy can show a stamp from the MIDDLE of
// this till's own chain of saves (the pull caught up with save 1 but not
// yet save 2) — still not a conflict with itself. A change made on the
// main till after that is.
func TestCatalogWriteThrough_DesignerThirdSaveIsNotASelfConflict(t *testing.T) {
	p := newCatalogWriteThroughPair(t)
	catalogsync.ResetRecentSaves()
	t.Cleanup(catalogsync.ResetRecentSaves)
	mainStamp := func() string {
		v, _, err := data.NewCatalogRepo(p.main.dp.Db).CatalogUpdatedAt(t.Context(), data.CatalogKindCategory, "cat1")
		if err != nil || v == "" {
			t.Fatalf("main stamp = %q (%v)", v, err)
		}
		return v
	}
	save := func(name, shown string) *httptest.ResponseRecorder {
		return postFormHtmx(p.replica.mux, "/api/designer/categories/cat1", url.Values{"name": {name}, "base_updated_at": {shown}}, &catalogMgr)
	}
	s0 := mainStamp()
	time.Sleep(1100 * time.Millisecond) // updated_at has one-second resolution
	if rec := save("One", s0); rec.Code != http.StatusNoContent {
		t.Fatalf("save 1 = %d %q", rec.Code, rec.Body.String())
	}
	s1 := mainStamp()
	time.Sleep(1100 * time.Millisecond)
	// Re-rendered before the pull of save 1: the form still shows S0.
	if rec := save("Two", s0); rec.Code != http.StatusNoContent {
		t.Fatalf("save 2 = %d %q", rec.Code, rec.Body.String())
	}
	if mainStamp() == s1 {
		t.Fatal("save 2 did not advance the main till's stamp")
	}
	time.Sleep(1100 * time.Millisecond)
	// Re-rendered after the pull of save 1 but before the pull of save 2.
	if rec := save("Three", s1); rec.Code != http.StatusNoContent {
		t.Fatalf("save 3 from a form showing save 1's stamp = %d %q — a conflict with this till's own save", rec.Code, rec.Body.String())
	}
	if got := catalogCategoryName(t, p.main.dp, "cat1"); got != "Three" {
		t.Fatalf("main till category = %q", got)
	}
	execStamps(t, p.main.dp, `UPDATE categories SET name = 'Coffee', updated_at = '2999-01-01 00:00:00' WHERE id = 'cat1'`)
	if rec := save("Stale", s1); rec.Code != http.StatusConflict || catalogCategoryName(t, p.main.dp, "cat1") != "Coffee" {
		t.Fatalf("save over a later main-till change = %d %q, want 409", rec.Code, rec.Body.String())
	}
}
