package ui

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// TestButtonsHTTPList_AllTabIsFirstAndDefaultSelected (ut-docs#2212): the
// tabbed view ($hasTabs, >=2 top-level categories) must render a leading
// "All" tab, selected by default, so an operator who has tapped into a
// category always has an escape hatch back to the whole catalogue without
// hunting for which category everything happens to live under. Reuses the
// existing per-category panels (panelVisible, ut-docs#2181) rather than a
// separate render path — see the wiring assertions below.
func TestButtonsHTTPList_AllTabIsFirstAndDefaultSelected(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	store := NewButtonStore(db)
	renderer, err := NewRenderer(
		filepath.Join("web", "ui", "layouts", "base.html"),
		filepath.Join("web", "ui", "pages", "index.html"),
		filepath.Join("web", "ui", "partials", "buttons.html"),
		httpx.FuncsFor("en"),
	)
	if err != nil {
		t.Fatalf("NewRenderer: %v", err)
	}
	h := &ButtonsHTTP{Store: *store, View: renderer}

	// Two top-level categories, same shape as
	// TestButtonsHTTPList_TabbedPanelsCarryCrossCategorySearchWiring, so
	// $hasTabs is true and a tab bar actually renders.
	mustExec(t, db, `INSERT INTO categories(id, name, parent_id, sort_order) VALUES
		('cat_food', 'Food', NULL, 1),
		('cat_drink', 'Drinks', NULL, 2)`)
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, category_id, is_active) VALUES
		('i1', 'S1', 'Bread', 140, 'cat_food', 1),
		('i2', 'S2', 'Cola', 120, 'cat_drink', 1)`)
	for _, b := range []Button{
		{Label: "Bread", Code: "C1", ItemID: "i1"},
		{Label: "Cola", Code: "C2", ItemID: "i2"},
	} {
		if err := store.Add(b); err != nil {
			t.Fatalf("Add(%+v): %v", b, err)
		}
	}

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The default `tab` value is the All sentinel, not the first real
	// category's ID — this is what actually makes "All" the pre-selected
	// tab on first paint.
	if !strings.Contains(body, `tab: '__all__',`) {
		t.Fatalf("expected the Alpine component's default tab to be the All sentinel, got: %s", body)
	}

	// The All tab itself: a real, focusable, ARIA-correct tab, appearing
	// BEFORE either category tab (first in DOM order).
	allIdx := strings.Index(body, `id="cat-tab-all"`)
	foodIdx := strings.Index(body, `id="cat-tab-cat_food"`)
	drinkIdx := strings.Index(body, `id="cat-tab-cat_drink"`)
	if allIdx < 0 {
		t.Fatalf("expected an All tab button (id=cat-tab-all), got: %s", body)
	}
	if allIdx > foodIdx || allIdx > drinkIdx {
		t.Fatalf("expected the All tab to render before every category tab, got: %s", body)
	}
	if !strings.Contains(body, `:aria-selected="tab === '__all__'"`) {
		t.Fatalf("expected the All tab's aria-selected to be keyed off the All sentinel, got: %s", body)
	}
	// The All tab owns no single panel (selecting it shows every existing
	// per-category panel at once), so it must not claim one via
	// aria-controls — same "drop the attribute rather than assert a false
	// relationship" precedent this file already applies during search.
	allTagEnd := strings.Index(body[allIdx:], ">")
	if allTagEnd < 0 {
		t.Fatalf("could not find the end of the All tab's opening tag, got: %s", body)
	}
	allTag := body[allIdx : allIdx+allTagEnd]
	if strings.Contains(allTag, "aria-controls") {
		t.Fatalf("expected the All tab to carry no aria-controls (it owns no single panel), got tag: %s", allTag)
	}

	// panelVisible must treat the All sentinel the same as a real match:
	// every existing per-category panel becomes visible at once when "All"
	// is selected, with no query active.
	if !strings.Contains(body, `this.tab === id || this.tab === '__all__'`) {
		t.Fatalf("expected panelVisible's no-query branch to also match the All sentinel, got: %s", body)
	}

	// Still exactly the two existing per-category panels (ut-docs#2181) —
	// All must not introduce a THIRD, duplicate rendering of the catalogue.
	if strings.Count(body, `x-show="panelVisible('`) != 2 {
		t.Fatalf("expected All to reuse the existing two per-category panels, not add a new one, got: %s", body)
	}

	// No per-category panel may be x-cloak'd any more — every panel is
	// visible by default now (via the All tab), so cloaking all-but-the-
	// first (the old "first real category is default" behavior) would
	// hide real content pre-hydration. Checked per-panel-tag (not a bare
	// body-wide x-cloak substring search, which would also match unrelated
	// elements like the search strip's own back button).
	for _, marker := range []string{`id="cat-panel-cat_food"`, `id="cat-panel-cat_drink"`} {
		idx := strings.Index(body, marker)
		if idx < 0 {
			t.Fatalf("expected to find panel %s, got: %s", marker, body)
		}
		tagEnd := strings.Index(body[idx:], ">")
		if tagEnd < 0 {
			t.Fatalf("could not find the end of the %s panel's opening tag, got: %s", marker, body)
		}
		tag := body[idx : idx+tagEnd]
		if strings.Contains(tag, "x-cloak") {
			t.Fatalf("expected panel %s to carry no x-cloak, got tag: %s", marker, tag)
		}
	}

	// The ARIA role/aria-labelledby relaxation (ut-docs#2181's F3 fix for
	// the search case) must also cover the All-active case: with the All
	// tab selected, more than one panel is visible while only the All tab
	// itself is aria-selected, so a panel can no longer claim the strict
	// one-tab-one-panel `tabpanel` role.
	if !strings.Contains(body, `:role="(q || tab === '__all__') ? 'group' : 'tabpanel'"`) {
		t.Fatalf("expected each panel's role to relax under the All tab too, got: %s", body)
	}
	if !strings.Contains(body, `:aria-labelledby="(q || tab === '__all__') ? null : `) {
		t.Fatalf("expected each panel's aria-labelledby to relax under the All tab too, got: %s", body)
	}

	// Each top-level bucket's own header must show under All too (not just
	// during search), so an item stays attributable to a category once
	// "All" flattens every category together. No x-cloak: All is the
	// default tab, so this header is visible by default on first paint —
	// cloaking it would hide real category labels forever from a client
	// where Alpine never loads, the same reasoning that already dropped
	// x-cloak from the per-category panels above.
	if strings.Count(body, `<h3 class="category-header" x-show="q || tab === '__all__'">`) != 2 {
		t.Fatalf("expected both Food's and Drinks' own-bucket headers to show under the All tab too, got: %s", body)
	}
	if strings.Contains(body, `<h3 class="category-header" x-show="q || tab === '__all__'" x-cloak>`) {
		t.Fatalf("expected the top-level bucket header to carry no x-cloak (visible by default under All), got: %s", body)
	}

	// i18n: the All tab's own label goes through T, not a hardcoded
	// literal (no InitI18n call in this package's tests, so T falls back
	// to the raw key — guard-i18n.sh is what enforces the translation
	// itself exists in every locale file).
	if !strings.Contains(body, "products.all<") && !strings.Contains(body, ">products.all<") {
		t.Fatalf("expected the products.all key to render as the All tab's label, got: %s", body)
	}
}
