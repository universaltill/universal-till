package ui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// ut-docs#3089: the all_filter_chips chip used to live only in Alpine
// x-data, so a buttons-changed/modifiers-changed whole-document refresh
// (the .products root's own hx-get /ui/buttons) always swapped the grid
// back to All mid-sale, dropping whatever category the cashier had tapped.
// GET /ui/buttons now accepts the same ?category= the chip's own hx-get
// already used (ButtonsHTTP.AllMore's semantics -- see its own doc comment):
// absent/"all" = whole catalog, ""=uncategorized, otherwise a category id.
// These tests drive that through the real handlers against
// newBrowsingModeTestHTTP's fixture (buttons_browsing_mode_test.go):
//
//	Food (top-level)      Bread   — quick button
//	  └ Dairy (nested)    Butter  — active, NO quick button
//	Drinks (top-level)    Cola    — quick button
//	Household (top-level) Soap    — active, NO quick button anywhere
//	(uncategorized)       Loose Sweet — active, no category
//	Household             Old Mop — INACTIVE

// renderListQuery is renderList (buttons_browsing_mode_test.go) with a
// caller-chosen request target, so a ?category= query can be exercised.
func renderListQuery(t *testing.T, h *ButtonsHTTP, target string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// tagFor returns the opening tag (up to and including its own ">") starting
// at idx, so an assertion can check attributes on ONE element without a
// substring match bleeding into a sibling that happens to share a prefix.
func tagFor(t *testing.T, body string, idx int) string {
	t.Helper()
	if idx < 0 {
		t.Fatalf("tagFor: negative index into: %s", body)
	}
	end := strings.Index(body[idx:], ">")
	if end < 0 {
		t.Fatalf("tagFor: no closing '>' from index %d in: %s", idx, body)
	}
	return body[idx : idx+end+1]
}

// A category chip on the request renders that chip pressed, All not
// pressed, the grid filtered to the category's subtree (nested Dairy folds
// into Food's own chip), and the all-more-fragment's marker present.
func TestButtonsHTTPList_ChipSurvivesRefresh_CategoryFilter(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	h.BrowsingMode = "all_filter_chips"
	body := renderListQuery(t, h, "/ui/buttons?category=cat_food")

	allTag := tagFor(t, body, strings.Index(body, `data-cat-all`))
	if !strings.Contains(allTag, `aria-pressed="false"`) {
		t.Fatalf("All chip must not render pressed when a category is requested, tag: %s", allTag)
	}
	foodTag := tagFor(t, body, strings.Index(body, `data-cat-id="cat_food"`))
	if !strings.Contains(foodTag, `aria-pressed="true"`) {
		t.Fatalf("Food chip must render pressed, tag: %s", foodTag)
	}

	grid := allGridSlice(t, body)
	mustContainAll(t, grid, `data-name="Bread"`, `data-name="Butter"`, `data-all-filter="cat_food"`)
	mustContainNone(t, grid, `data-name="Cola"`, `data-name="Soap"`, `data-name="Loose Sweet"`)

	// The chip that hx-include carries on the next refresh mirrors the
	// server's own selection (no flash of All before Alpine hydrates).
	mustContainAll(t, body, `id="browsing-chip-input"`, `name="category"`, `value="cat_food"`)
}

// A category id that no longer resolves (a deleted category, or garbage)
// falls back to All quietly -- no error, full unfiltered grid.
func TestButtonsHTTPList_ChipSurvivesRefresh_UnknownCategoryFallsBackToAll(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	h.BrowsingMode = "all_filter_chips"
	body := renderListQuery(t, h, "/ui/buttons?category=cat_deleted")

	allTag := tagFor(t, body, strings.Index(body, `data-cat-all`))
	if !strings.Contains(allTag, `aria-pressed="true"`) {
		t.Fatalf("an unknown category must fall back to All, pressed; tag: %s", allTag)
	}
	grid := allGridSlice(t, body)
	mustContainAll(t, grid, `data-name="Bread"`, `data-name="Cola"`, `data-name="Soap"`, `data-name="Loose Sweet"`)
	mustContainNone(t, grid, `data-all-filter`)
	mustContainAll(t, body, `id="browsing-chip-input"`, `value="all"`)
}

// category="" (present but empty) is the uncategorized bucket -- distinct
// from All, which the historical Alpine chip value (” for both) could not
// tell apart.
func TestButtonsHTTPList_ChipSurvivesRefresh_UncategorizedDistinctFromAll(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	h.BrowsingMode = "all_filter_chips"
	body := renderListQuery(t, h, "/ui/buttons?category=")

	allTag := tagFor(t, body, strings.Index(body, `data-cat-all`))
	if !strings.Contains(allTag, `aria-pressed="false"`) {
		t.Fatalf("All must not render pressed when uncategorized is selected, tag: %s", allTag)
	}
	uncatIdx := strings.Index(body, `data-cat-id=""`)
	if uncatIdx < 0 {
		t.Fatalf("expected the uncategorized chip in the row, got: %s", body)
	}
	uncatTag := tagFor(t, body, uncatIdx)
	if !strings.Contains(uncatTag, `aria-pressed="true"`) {
		t.Fatalf("the uncategorized chip must render pressed, tag: %s", uncatTag)
	}

	grid := allGridSlice(t, body)
	mustContainAll(t, grid, `data-name="Loose Sweet"`, `data-all-filter=""`)
	mustContainNone(t, grid, `data-name="Bread"`, `data-name="Cola"`, `data-name="Soap"`)
}

// Every other browsing mode ignores the param outright -- a stray
// ?category= on the strip or category_tabs render must change nothing.
func TestButtonsHTTPList_ChipParamIgnoredOutsideAllFilterChips(t *testing.T) {
	for _, mode := range []string{"category_tabs", "strip_overflow"} {
		_, _, h := newBrowsingModeTestHTTP(t)
		h.BrowsingMode = mode
		without := renderListQuery(t, h, "/ui/buttons")
		with := renderListQuery(t, h, "/ui/buttons?category=cat_food")
		if with != without {
			t.Fatalf("%s: a category query param changed the render; other modes must ignore it entirely\nwithout:\n%s\nwith:\n%s", mode, without, with)
		}
	}
}

// EditMode (the Designer's live replica) always renders the strip
// (ButtonsHTTP.List's own long-standing rule) and must ignore a category
// query param the same way.
func TestButtonsHTTPList_ChipParamIgnoredInEditMode(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	h.BrowsingMode = "all_filter_chips"
	h.EditMode = true
	without := renderListQuery(t, h, "/ui/buttons")
	with := renderListQuery(t, h, "/ui/buttons?category=cat_food")
	if with != without {
		t.Fatalf("EditMode must ignore a category query param\nwithout:\n%s\nwith:\n%s", without, with)
	}
}

// renderList's own filtered grid must be byte-for-byte what AllMore would
// have swapped in for the same chip -- both now go through the shared
// allFilterPage helper.
func TestButtonsHTTPList_ChipGridMatchesAllMoreFragment(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	h.BrowsingMode = "all_filter_chips"

	listBody := renderListQuery(t, h, "/ui/buttons?category=cat_food")
	listGrid := allGridSlice(t, listBody)

	rec := httptest.NewRecorder()
	h.AllMore(rec, httptest.NewRequest("GET", "/ui/buttons/all/more?offset=0&category=cat_food", nil))
	allMoreBody := rec.Body.String()

	if !strings.Contains(listGrid, allMoreBody) {
		t.Fatalf("renderList's filtered grid must contain AllMore's exact fragment bytes\nlist grid:\n%s\n\nallMore body:\n%s", listGrid, allMoreBody)
	}
}

// ListFragment (GET /'s first paint) always renders All, whatever query
// param a caller's *http.Request happens to carry -- the chip is passed
// into renderList explicitly by ButtonsHTTP.List/ListFragment, never read
// back off r, so ListFragment structurally cannot pick one up.
func TestButtonsHTTPListFragment_IgnoresCategoryQueryParam(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	h.BrowsingMode = "all_filter_chips"

	without, ok := h.ListFragment(httptest.NewRequest("GET", "/", nil))
	if !ok {
		t.Fatal("ListFragment(no query) ok=false")
	}
	with, ok := h.ListFragment(httptest.NewRequest("GET", "/?category=cat_food", nil))
	if !ok {
		t.Fatal("ListFragment(?category=cat_food) ok=false")
	}
	if string(with) != string(without) {
		t.Fatalf("ListFragment must ignore a category query param entirely\nwithout:\n%s\nwith:\n%s", without, with)
	}
	allTag := tagFor(t, string(without), strings.Index(string(without), `data-cat-all`))
	if !strings.Contains(allTag, `aria-pressed="true"`) {
		t.Fatalf("expected the All chip pressed by default, tag: %s", allTag)
	}
}

// The #2501 sell-screen cache must never cross-serve a chip render for
// plain /ui/buttons, or vice versa -- the chip has to be part of the key.
func TestSellScreenCache_ChipAndPlainListDontCrossServe(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, false, false)
	h.BrowsingMode = "all_filter_chips"

	plain, plainQueries := f.list(t, h)
	if plainQueries < 5 {
		t.Fatalf("plain All render ran only %d SELECTs -- the harness isn't counting", plainQueries)
	}
	if !strings.Contains(plain, "Cola Can") || !strings.Contains(plain, "Sticky Bun") {
		t.Fatalf("plain All render missing an item: %s", plain)
	}

	chip, chipQueries := f.listQuery(t, h, "/ui/buttons?category=cat-drinks")
	if chipQueries < 5 {
		t.Fatalf("chip render was served from the plain All cache entry (only %d SELECTs)", chipQueries)
	}
	if strings.Contains(chip, "Sticky Bun") {
		t.Fatalf("cat-drinks chip render must not include Sticky Bun (Food): %s", chip)
	}
	if !strings.Contains(chip, "Cola Can") {
		t.Fatalf("cat-drinks chip render must include Cola Can: %s", chip)
	}

	// Re-requesting plain All must still be ITS OWN cache entry, not the
	// chip's -- a hit (1 SELECT, the version read) with Sticky Bun back.
	plainAgain, plainAgainQueries := f.list(t, h)
	if !strings.Contains(plainAgain, "Sticky Bun") {
		t.Fatalf("plain All was served the chip-filtered entry instead: %s", plainAgain)
	}
	if plainAgainQueries != 1 {
		t.Fatalf("plain All's own cache entry missed: %d SELECTs, want 1 (cache hit)", plainAgainQueries)
	}

	// And re-requesting the chip is itself a cache hit, not the plain entry.
	chipAgain, chipAgainQueries := f.listQuery(t, h, "/ui/buttons?category=cat-drinks")
	if chipAgainQueries != 1 {
		t.Fatalf("chip's own cache entry missed: %d SELECTs, want 1 (cache hit)", chipAgainQueries)
	}
	if chipAgain != chip {
		t.Fatal("chip's cached response differs from the render it was stored from")
	}
}

// listQuery is sellScreenFixture.list (sellscreen_cache_integration_test.go)
// with a caller-chosen request target.
func (f *sellScreenFixture) listQuery(t *testing.T, h *ButtonsHTTP, target string) (string, int64) {
	t.Helper()
	atomic.StoreInt64(f.counter, 0)
	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", target, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String(), atomic.LoadInt64(f.counter)
}

// ---- pure-function unit tests -----------------------------------------

func TestRequestedChipParam(t *testing.T) {
	cases := []struct{ url, want string }{
		{"/ui/buttons", "all"},
		{"/ui/buttons?category=all", "all"},
		{"/ui/buttons?category=", ""},
		{"/ui/buttons?category=cat_food", "cat_food"},
	}
	for _, tc := range cases {
		if got := requestedChipParam(httptest.NewRequest("GET", tc.url, nil)); got != tc.want {
			t.Errorf("requestedChipParam(%s) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

func TestResolveChip(t *testing.T) {
	tiles := []CategoryTileVM{{ID: "cat_food"}, {ID: ""}}
	cases := []struct{ raw, want string }{
		{"all", "all"},
		{"cat_food", "cat_food"},
		{"", ""},
		{"cat_gone", "all"},
	}
	for _, tc := range cases {
		if got := resolveChip(tc.raw, tiles); got != tc.want {
			t.Errorf("resolveChip(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestChipCacheKeyParam(t *testing.T) {
	all := chipCacheKeyParam("all")
	if all != "" {
		t.Errorf(`chipCacheKeyParam("all") = %q, want ""`, all)
	}
	uncat := chipCacheKeyParam("")
	if uncat == all {
		t.Error(`chipCacheKeyParam("") (uncategorized) must differ from chipCacheKeyParam("all")`)
	}
	food := chipCacheKeyParam("cat_food")
	if food == "" || food == uncat || food == all {
		t.Errorf("chipCacheKeyParam(cat_food) = %q, must be its own distinct, non-empty key", food)
	}
	if got := chipCacheKeyParam(strings.Repeat("a", 65)); got != all {
		t.Errorf("an over-long chip must fold into the All key, got %q", got)
	}
	if got := chipCacheKeyParam("cat/../etc"); got != all {
		t.Errorf("a chip outside the id charset must fold into the All key, got %q", got)
	}
}

// Review finding 1: a real category whose id falls outside the cache key's
// charset (ids arriving by cloud sync are not format-checked) must render
// as All, never be cached under All's key -- that entry is also GET /'s
// first paint, which would then open on a filtered grid.
func TestSellScreenCache_OutOfCharsetChipNeverPoisonsAllEntry(t *testing.T) {
	f := newSellScreenFixture(t)
	f.exec(t, `INSERT INTO categories (id, name) VALUES ('cat.snacks', 'Snacks')`)
	f.exec(t, `INSERT INTO items (id, sku, name, base_price, category_id) VALUES ('itm-crisps', 'CRISPS', 'Salt Crisps', 90, 'cat.snacks')`)
	h := f.handler(t, false, false)
	h.BrowsingMode = "all_filter_chips"

	odd, _ := f.listQuery(t, h, "/ui/buttons?category=cat.snacks")
	if !strings.Contains(odd, "Cola Can") {
		t.Fatalf("an out-of-charset chip must render as All (it shares All's cache key): %s", odd)
	}
	plain, _ := f.list(t, h)
	if !strings.Contains(plain, "Cola Can") || !strings.Contains(plain, "Salt Crisps") {
		t.Fatalf("plain All served a filtered entry: %s", plain)
	}
	first, ok := h.ListFragment(httptest.NewRequest("GET", "/", nil))
	if !ok || !strings.Contains(string(first), "Cola Can") {
		t.Fatalf("GET / first paint served a filtered entry (ok=%v): %s", ok, first)
	}
}

// Review finding 4: a well-formed but unknown chip renders as All and is not
// cached, so arbitrary ids can't crowd real entries out of the bounded cache.
func TestSellScreenCache_UnknownChipIsNotCached(t *testing.T) {
	f := newSellScreenFixture(t)
	h := f.handler(t, false, false)
	h.BrowsingMode = "all_filter_chips"

	f.listQuery(t, h, "/ui/buttons?category=cat-gone")
	body, queries := f.listQuery(t, h, "/ui/buttons?category=cat-gone")
	if queries == 1 {
		t.Fatal("an unknown chip's All fallback was cached (second request was a cache hit)")
	}
	if !strings.Contains(body, "Sticky Bun") || !strings.Contains(body, "Cola Can") {
		t.Fatalf("unknown chip must fall back to the full All grid: %s", body)
	}
}

// Review finding 3: the Load more button under the uncategorized chip must
// keep category= (empty) on its URL, or paging leaves the filter.
func TestAllMoreButton_UncategorizedKeepsEmptyCategory(t *testing.T) {
	_, _, h := newBrowsingModeTestHTTP(t)
	rec := httptest.NewRecorder()
	if err := h.View.Render(rec, "all-more-fragment", map[string]any{
		"Buttons": []ButtonVM{}, "HasMore": true, "NextOffset": 200, "Category": "", "Filtered": true,
	}); err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rec.Body.String(), `offset=200&category="`) {
		t.Fatalf("uncategorized load-more lost its empty category param: %s", rec.Body.String())
	}
}
