package ui

import (
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
)

// allGridSlice returns just the #buttons-grid-all portion of a /ui/buttons
// body, so a tile count taken from it can only ever see the All tab's own
// tiles.
//
// Corrected in independent review (ut-docs#2319 review): this used to slice
// to the END of the body, on the stated grounds that "no sibling grid
// [comes] after it to bleed into the count". That is not true —
// buttons.html renders `{{ range $g := .Groups }}`, one #cat-panel-* per
// category, each one full of "product-tile"s, immediately AFTER the
// #buttons-grid-all block. The assertions only happened to hold because
// both fixtures here seed `items` but no `shortcut_buttons`, so .Groups is
// empty; the first quick button anyone adds to a fixture would make the
// count over-read and the test fail for a reason that has nothing to do
// with AllTabPageSize. Bound the slice at the first category panel instead
// (end-of-body when there are none), which is correct either way.
func allGridSlice(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `id="buttons-grid-all"`)
	if start < 0 {
		t.Fatalf("expected #buttons-grid-all in the response, got: %s", body)
	}
	rest := body[start:]
	if end := strings.Index(rest, `id="cat-panel-`); end >= 0 {
		return rest[:end]
	}
	return rest
}

// TestPageButtons_Bounds exercises pageButtons directly (ut-docs#2319): the
// slicing helper behind both ButtonsHTTP.List's first page and AllMore's
// subsequent ones. Table-driven over the boundary cases a hand-typed or
// stale "offset" query param can actually produce.
func TestPageButtons_Bounds(t *testing.T) {
	all := make([]Button, 250)
	for i := range all {
		all[i] = Button{ItemID: itemIDFor(i)}
	}

	cases := []struct {
		name           string
		offset         int
		wantLen        int
		wantHasMore    bool
		wantFirstIndex int // index into `all` the page's first item should carry, when wantLen > 0
	}{
		{"first page, more remain", 0, AllTabPageSize, true, 0},
		{"second (final) page, exact remainder under page size", AllTabPageSize, 50, false, AllTabPageSize},
		{"offset exactly at the end", 250, 0, false, -1},
		{"offset past the end", 9999, 0, false, -1},
		{"negative offset treated as out of range", -1, 0, false, -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, hasMore := pageButtons(all, tc.offset)
			if len(page) != tc.wantLen {
				t.Fatalf("pageButtons(offset=%d): len = %d, want %d", tc.offset, len(page), tc.wantLen)
			}
			if hasMore != tc.wantHasMore {
				t.Fatalf("pageButtons(offset=%d): hasMore = %v, want %v", tc.offset, hasMore, tc.wantHasMore)
			}
			if tc.wantLen > 0 && page[0].ItemID != itemIDFor(tc.wantFirstIndex) {
				t.Fatalf("pageButtons(offset=%d): first item = %s, want %s", tc.offset, page[0].ItemID, itemIDFor(tc.wantFirstIndex))
			}
		})
	}

	// A page never exceeds AllTabPageSize even when the caller asks for
	// more than that in one call — pageButtons has no "limit" parameter of
	// its own to misuse, but this pins the constant's actual effect.
	page, hasMore := pageButtons(all, 0)
	if len(page) != AllTabPageSize || !hasMore {
		t.Fatalf("first page = %d items, hasMore=%v; want %d items, hasMore=true", len(page), hasMore, AllTabPageSize)
	}
}

// TestButtonsHTTPList_AllTabCapsInitialPageAndOffersLoadMore reproduces the
// actual defect (ut-docs#2319): before this fix, GET /ui/buttons inlined
// EVERY active item into #buttons-grid-all, so a 2000-item catalog shipped
// ~1MB on every render, including the whole-document refetch that
// "modifiers-changed from:body" triggers on every unrelated config edit.
// 220 active items (comfortably past AllTabPageSize=200) is enough to prove
// the cap without this test itself paying #2318's large-catalog runtime.
func TestButtonsHTTPList_AllTabCapsInitialPageAndOffersLoadMore(t *testing.T) {
	const n = 220
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })

	var sb strings.Builder
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, "('%s','SKU%05d','Item %05d',100,1)", itemIDFor(i), i, i)
	}
	mustExec(t, db, "INSERT INTO items(id, sku, name, base_price, is_active) VALUES "+sb.String())

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

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	if rec.Code != 200 {
		t.Fatalf("List = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	allGridHTML := allGridSlice(t, body)

	gotTiles := strings.Count(allGridHTML, `class="btn-tile`)
	if gotTiles != AllTabPageSize {
		t.Fatalf("initial All grid rendered %d tiles, want exactly AllTabPageSize (%d) — the whole %d-item catalog is leaking into one response again", gotTiles, AllTabPageSize, n)
	}
	// Only the first page's items, in ListItems' name-sorted order — not an
	// arbitrary/random subset.
	if !strings.Contains(allGridHTML, "Item 00000") || strings.Contains(allGridHTML, fmt.Sprintf("Item %05d", AllTabPageSize)) {
		t.Fatalf("expected items 0..%d (name-sorted) on the first page, got: %s", AllTabPageSize-1, allGridHTML)
	}

	if !strings.Contains(allGridHTML, `data-testid="all-more-btn"`) {
		t.Fatalf("expected a load-more button since %d items exceed AllTabPageSize=%d, got: %s", n, AllTabPageSize, allGridHTML)
	}
	wantOffsetParam := fmt.Sprintf("offset=%d", AllTabPageSize)
	if !strings.Contains(allGridHTML, wantOffsetParam) {
		t.Fatalf("expected the load-more button's hx-get to carry %q, got: %s", wantOffsetParam, allGridHTML)
	}

	// AllMore serves the remainder (n - AllTabPageSize = 20 items here),
	// with no further load-more button once exhausted.
	rec2 := httptest.NewRecorder()
	h.AllMore(rec2, httptest.NewRequest("GET", "/ui/buttons/all/more?offset="+strconv.Itoa(AllTabPageSize), nil))
	if rec2.Code != 200 {
		t.Fatalf("AllMore = %d: %s", rec2.Code, rec2.Body.String())
	}
	moreBody := rec2.Body.String()
	wantRemaining := n - AllTabPageSize
	if got := strings.Count(moreBody, `class="btn-tile`); got != wantRemaining {
		t.Fatalf("AllMore(offset=%d) rendered %d tiles, want the exact remainder %d", AllTabPageSize, got, wantRemaining)
	}
	if strings.Contains(moreBody, `data-testid="all-more-btn"`) {
		t.Fatalf("AllMore's last page should carry no further load-more button, got: %s", moreBody)
	}
	if !strings.Contains(moreBody, fmt.Sprintf("Item %05d", n-1)) {
		t.Fatalf("expected the very last item on the final page, got: %s", moreBody)
	}
}

// TestButtonsHTTPList_AllTabUnderPageSizeIsUnchanged pins the other half of
// the fix's own scope: a catalog at or under AllTabPageSize must render
// EXACTLY as it did before this card — one response, every item, no
// load-more button — since that's the exact "usable with a 200+ item
// catalog" bar ut-docs#2294 already committed to.
func TestButtonsHTTPList_AllTabUnderPageSizeIsUnchanged(t *testing.T) {
	db := setupFullTestDB(t)
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, `INSERT INTO items(id, sku, name, base_price, is_active) VALUES
		('i1', 'S1', 'Bread', 140, 1),
		('i2', 'S2', 'Cola', 120, 1)`)

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

	rec := httptest.NewRecorder()
	h.List(rec, httptest.NewRequest("GET", "/ui/buttons", nil))
	body := rec.Body.String()

	if strings.Contains(body, `data-testid="all-more-btn"`) {
		t.Fatalf("a 2-item catalog must never render a load-more button, got: %s", body)
	}
	if got := strings.Count(allGridSlice(t, body), `class="btn-tile`); got != 2 {
		t.Fatalf("expected both items to render (unpaginated below the cap), got %d tiles", got)
	}
}
