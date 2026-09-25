package pages

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#2699: both category lists (/categories and the Designer's
// category-management section) drop the per-row pencil and up/down
// chevrons. A row is tapped to edit and long-pressed to drag
// (web/public/list-reorder.js); its leading visual is the category's own
// picture — image, else icon (only ever through iconid.AssetPath), else
// colour swatch, else a neutral placeholder.

// seed2699 inserts one category per leading-visual case, in sort order.
func seed2699(t *testing.T, exec func(string, ...any) error) {
	t.Helper()
	stmts := []string{
		// An image that exists (embedded asset) wins over icon and colour.
		`INSERT INTO categories(id,name,sort_order,is_active,color,image_path,icon) VALUES ('c-img','WithImage',0,1,'#0f766e','/public/assets/category-icons/drink.svg','lucide:coffee')`,
		// No image: a known icon id draws its registered asset.
		`INSERT INTO categories(id,name,sort_order,is_active,color,icon) VALUES ('c-icon','WithIcon',1,1,'#0f766e','lucide:coffee')`,
		// An image path whose file is missing falls through; an unknown /
		// hostile icon value never reaches the page — the fallback glyph does.
		`INSERT INTO categories(id,name,sort_order,is_active,image_path,icon) VALUES ('c-evil','WithEvilIcon',2,1,'/public/uploads/missing.png','javascript:alert(1)')`,
		// Colour only.
		`INSERT INTO categories(id,name,sort_order,is_active,color) VALUES ('c-color','WithColour',3,1,'#b91c1c')`,
		// Nothing at all.
		`INSERT INTO categories(id,name,sort_order,is_active) VALUES ('c-none','Plain',4,1)`,
	}
	for _, s := range stmts {
		if err := exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
}

// rowSegment returns the markup from the element that opens `marker` up to
// the next occurrence of `end` (exclusive).
func rowSegment(t *testing.T, body, marker, end string) string {
	t.Helper()
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no %q in body:\n%.4000s", marker, body)
	}
	rest := body[i:]
	j := strings.Index(rest[len(marker):], end)
	if j < 0 {
		return rest
	}
	return rest[:len(marker)+j]
}

func assertThumbs2699(t *testing.T, surface string, seg func(id string) string) {
	t.Helper()
	cases := []struct {
		id, want string
		not      []string
	}{
		// ut-docs#2717: a set icon beats a library tile left in image_path.
		{"c-img", `src="/public/assets/category-icons/coffee.svg`, []string{`src="/public/assets/category-icons/drink.svg`, `cat-thumb-swatch`}},
		{"c-icon", `src="/public/assets/category-icons/coffee.svg`, []string{`cat-thumb-swatch`}},
		{"c-evil", `src="/public/assets/category-icons/tag.svg`, []string{`javascript`, `src="/public/uploads/missing.png`, `alert(1)`}},
		{"c-color", `class="cat-thumb-swatch" style="--swatch: #b91c1c"`, []string{`<img`}},
		{"c-none", `class="cat-thumb-placeholder"`, []string{`<img`, `cat-thumb-swatch`}},
	}
	for _, c := range cases {
		s := seg(c.id)
		if !strings.Contains(s, `class="cat-thumb"`) {
			t.Errorf("%s row %s has no leading .cat-thumb:\n%s", surface, c.id, s)
		}
		if !strings.Contains(s, c.want) {
			t.Errorf("%s row %s: want %q in:\n%s", surface, c.id, c.want, s)
		}
		for _, n := range c.not {
			if strings.Contains(s, n) {
				t.Errorf("%s row %s must not contain %q:\n%s", surface, c.id, n, s)
			}
		}
		// Decorative: the name next to it is the label.
		if strings.Contains(s, `<img`) && !regexp.MustCompile(`<img[^>]*alt=""`).MatchString(s) {
			t.Errorf("%s row %s thumb image must carry alt=\"\"", surface, c.id)
		}
	}
}

func TestCategoriesPage_Rows_NoPencilNoChevrons_DragWired_2699(t *testing.T) {
	mux, d := newCategoriesTestMux(t)
	seed2699(t, func(s string, a ...any) error { _, err := d.Db.Exec(s, a...); return err })
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/categories", nil), manager)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /categories: %d", rec.Code)
	}
	body := rec.Body.String()
	table := rowSegment(t, body, `<table class="table" id="categories-table"`, `</table>`)

	for _, gone := range []string{`data-icon="pencil"`, `data-icon="chevron-up"`, `data-icon="chevron-down"`, `data-move=`, `move-up`, `move-down`} {
		if strings.Contains(table, gone) {
			t.Errorf("category list still renders %q", gone)
		}
	}
	for _, want := range []string{
		// The shared drag/keyboard module is wired to this list and its
		// existing endpoint, with translated messages.
		`data-reorder-list`,
		`data-reorder-url="/api/categories/reorder"`,
		`data-reorder-live="categories-reorder-live"`,
		`data-reorder-msg-moved="`,
		`data-reorder-msg-failed="`,
		`data-reorder-id="c-img"`,
		`data-reorder-name="WithImage"`,
		// The keyboard path to edit (and the Alt+Arrow focus target) is a
		// real button wrapping the name — not an icon.
		`class="category-row-open" data-record-edit`,
		`aria-keyshortcuts="Alt+ArrowUp Alt+ArrowDown"`,
	} {
		if !strings.Contains(table, want) {
			t.Errorf("category list missing %q", want)
		}
	}
	for _, want := range []string{
		`id="categories-reorder-live"`,
		`id="categories-reorder-hint"`,
		// The single-pointer alternative lives in the edit dialog.
		`id="category-move-up"`, `id="category-move-down"`,
		`data-reorder-step="-1"`, `data-reorder-step="1"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/categories missing %q", want)
		}
	}
	// The two-sentence reorder hint describes the TABLE once; a row's
	// button must not repeat it (a screen reader would read ~25 words per
	// row) — aria-keyshortcuts already names Alt+ArrowUp/Down per row.
	if !strings.Contains(body, `<table class="table" id="categories-table" aria-describedby="categories-reorder-hint"`) {
		t.Errorf("categories table is not described by the reorder hint once")
	}
	if n := strings.Count(body, `aria-describedby="categories-reorder-hint"`); n != 1 {
		t.Errorf("reorder hint referenced %d times, want exactly 1 (the table)", n)
	}
	assertThumbs2699(t, "categories", func(id string) string {
		return rowSegment(t, table, `data-id="`+id+`"`, `</tr>`)
	})
}

func TestDesignerCategories_Rows_NoPencilNoChevrons_DragWired_2699(t *testing.T) {
	mux, d := newButtonsMuxRealSession(t)
	seedOneButton(t, d)
	seed2699(t, func(s string, a ...any) error { _, err := d.Db.Exec(s, a...); return err })
	mgr := auth.User{ID: "m1", Role: "manager"}
	rec := getWithUser(mux, "/ui/buttons?mode=edit", &mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /ui/buttons?mode=edit: %d", rec.Code)
	}
	body := rec.Body.String()
	section := rowSegment(t, body, `<section class="designer-categories"`, `</section>`)

	for _, want := range []string{
		`data-reorder-list`,
		`data-reorder-url="/api/designer/categories/reorder"`,
		`data-reorder-refresh="buttons-changed"`,
		`data-reorder-live="designer-categories-live"`,
		`id="designer-categories-live"`,
		`data-reorder-id="c-icon"`,
		// The row body is a real button toggling the inline form.
		`id="designer-cat-open-c-icon" data-cat-edit`,
		`aria-controls="designer-cat-form-c-icon"`,
		`aria-keyshortcuts="Alt+ArrowUp Alt+ArrowDown"`,
		// Deactivate stays on the row.
		`id="designer-cat-active-c-icon"`,
	} {
		if !strings.Contains(section, want) {
			t.Errorf("designer categories missing %q", want)
		}
	}
	// The help text describes the LIST once, never each row's button.
	if !strings.Contains(section, `<ol class="designer-cat-list" aria-describedby="designer-categories-help"`) {
		t.Errorf("designer category list is not described by its help once")
	}
	if n := strings.Count(section, `aria-describedby="designer-categories-help"`); n != 1 {
		t.Errorf("designer help referenced %d times, want exactly 1 (the list)", n)
	}
	// Per row, everything before the inline form (the visible row) carries
	// no pencil and no chevrons; the Move earlier/later pair lives inside
	// the form.
	for _, id := range []string{"c-img", "c-icon", "c-evil", "c-color", "c-none"} {
		li := rowSegment(t, section, `data-cat-id="`+id+`"`, `</li>`)
		head := li
		if k := strings.Index(li, `<form class="designer-cat-form"`); k >= 0 {
			head = li[:k]
			form := li[k:]
			for _, want := range []string{`id="designer-cat-up-` + id + `"`, `id="designer-cat-down-` + id + `"`} {
				if !strings.Contains(form, want) {
					t.Errorf("designer row %s inline form missing %q", id, want)
				}
			}
		} else {
			t.Errorf("designer row %s has no inline form", id)
		}
		for _, gone := range []string{`data-icon="pencil"`, `data-icon="chevron-up"`, `data-icon="chevron-down"`, `data-cat-move`} {
			if strings.Contains(head, gone) {
				t.Errorf("designer row %s still renders %q outside its form", id, gone)
			}
		}
	}
	assertThumbs2699(t, "designer", func(id string) string {
		li := rowSegment(t, section, `data-cat-id="`+id+`"`, `</li>`)
		if k := strings.Index(li, `<form`); k >= 0 {
			return li[:k]
		}
		return li
	})
}
