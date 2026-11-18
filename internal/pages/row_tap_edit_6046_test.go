package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#6046: the five former pencil screens (kitchen stations, locations,
// promotions, country settings, registers) follow the row-tap pattern: the
// record's name is the real button carrying data-record-edit, there is no
// trailing pencil column, and the table's colspans match its columns.
func TestRowTapEdit_6046(t *testing.T) {
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	get := func(mux *http.ServeMux, path string) string {
		t.Helper()
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, auth.WithUser(httptest.NewRequest(http.MethodGet, path, nil), manager))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		return rec.Body.String()
	}

	type page struct {
		name, path, tableID, dialog, hintID string
		render                              func(t *testing.T) string
		// wantInButton must all appear inside the first-cell name button.
		wantInButton []string
		// buttonStarts, when set, is the text the button's content begins with.
		buttonStarts string
	}
	pages := []page{
		{name: "kitchen", hintID: "kitchen-stations-row-edit-hint", path: "/kitchen-stations", tableID: "kitchen-stations-table", dialog: "kitchen-station-dialog",
			wantInButton: []string{"Pass"},
			render: func(t *testing.T) string {
				mux, _ := newKitchenStationsTestMux(t)
				postForm(mux, "/api/kitchen-stations", url.Values{"name": {"Pass"}, "destination_type": {"both"}, "printer_address": {"tcp://10.0.0.5:9100"}}, &manager)
				body := get(mux, "/kitchen-stations")
				if !strings.Contains(body, `class="kitchen-view-display"`) {
					t.Errorf("kitchen-view-display link missing for an enabled display station")
				}
				return body
			}},
		{name: "locations", hintID: "locations-row-edit-hint", path: "/locations", tableID: "locations-table", dialog: "location-dialog",
			wantInButton: []string{"Back Room"},
			render: func(t *testing.T) string {
				mux, _ := newLocationsTestMux(t)
				postForm(mux, "/api/locations", url.Values{"name": {"Back Room"}}, &manager)
				return get(mux, "/locations")
			}},
		{name: "promotions", hintID: "promotions-row-edit-hint", path: "/promotions", tableID: "promotions-table", dialog: "promotion-dialog",
			wantInButton: []string{"NAMED"},
			render: func(t *testing.T) string {
				mux, _ := newPromotionsTestMux(t)
				postForm(mux, "/api/promotions", url.Values{"code": {"NAMED"}, "type": {"percent"}, "value_percent": {"10"}}, &manager)
				return get(mux, "/promotions")
			}},
		{name: "country", hintID: "country-settings-row-edit-hint", path: "/country-settings?all=1", tableID: "country-settings-table", dialog: "country-dialog",
			wantInButton: []string{"ZZ", "added here"},
			buttonStarts: "Germany",
			render: func(t *testing.T) string {
				mux, _, _ := newCountrySettingsTestMux(t)
				rec := postForm(mux, "/api/country-settings", url.Values{
					"code": {"ZZ"}, "original_code": {""}, "currency": {"GBP"}, "tax_rate_pct": {"20"},
					"archive_min_days": {strconv.FormatInt(data.GlobalArchiveMinDays, 10)},
				}, &manager)
				if rec.Code != http.StatusSeeOther {
					t.Fatalf("seed custom country: %d", rec.Code)
				}
				return get(mux, "/country-settings?all=1")
			}},
		{name: "registers", hintID: "registers-row-edit-hint", path: "/registers", tableID: "registers-table", dialog: "register-dialog",
			wantInButton: []string{"Active Till", "Old Till"},
			render: func(t *testing.T) string {
				mux, d := newRegistersTestMux(t)
				for _, r := range []struct {
					id, name string
					active   int
				}{{"r-a", "Active Till", 1}, {"r-o", "Old Till", 0}} {
					if _, err := d.Db.Exec(`INSERT INTO registers (id, name, is_active) VALUES (?, ?, ?)`, r.id, r.name, r.active); err != nil {
						t.Fatal(err)
					}
				}
				return get(mux, "/registers")
			}},
	}

	for _, p := range pages {
		t.Run(p.name, func(t *testing.T) {
			btnRe := `<button type="button" class="record-row-open" data-record-edit aria-describedby="` + p.hintID + `"><span class="record-row-name">`
			body := p.render(t)
			assertEditHint6056(t, body, p.tableID, p.hintID)
			if p.name == "country" {
				// The chip swap replaces #country-settings-view only; the hint
				// must sit outside it so it is neither dropped nor duplicated.
				if h, v := strings.Index(body, `id="`+p.hintID+`"`), strings.Index(body, `id="country-settings-view"`); h < 0 || h > v {
					t.Errorf("country hint must precede #country-settings-view (hint %d, view %d)", h, v)
				}
			}
			i := strings.Index(body, `id="`+p.tableID+`"`)
			if i < 0 {
				t.Fatalf("table %s missing", p.tableID)
			}
			end := strings.Index(body[i:], "</table>")
			if end < 0 {
				t.Fatalf("table %s not closed", p.tableID)
			}
			tbl := body[i : i+end]

			rowRe := regexp.MustCompile(`(?s)<tr[^>]*data-record-open="` + p.dialog + `"[^>]*>\s*<td>\s*` + regexp.QuoteMeta(btnRe) + `(.*?)</button>`)
			rows := rowRe.FindAllStringSubmatch(tbl, -1)
			if len(rows) == 0 {
				t.Fatalf("no row whose first cell is the name button (%s)", btnRe)
			}
			all := ""
			for _, m := range rows {
				all += m[1] + "\n"
			}
			for _, w := range p.wantInButton {
				if !strings.Contains(all, w) {
					t.Errorf("name button text lacks %q: %s", w, all)
				}
			}
			if p.buttonStarts != "" {
				if !regexp.MustCompile(`(?m)^\s*` + p.buttonStarts).MatchString(all) {
					t.Errorf("first name button should begin with %q: %s", p.buttonStarts, all)
				}
			}
			for _, tag := range regexp.MustCompile(`<button[^>]*data-record-edit[^>]*>`).FindAllString(tbl, -1) {
				if !strings.Contains(tag, ` aria-describedby="`+p.hintID+`"`) {
					t.Errorf("name button lacks aria-describedby=%q: %s", p.hintID, tag)
				}
				for _, bad := range []string{"aria-label", "title=", "aria-keyshortcuts"} {
					if strings.Contains(tag, bad) {
						t.Errorf("name button tag carries %s: %s", bad, tag)
					}
				}
			}
			for _, bad := range []string{`data-icon="pencil"`, `class="list-actions"`, `<th></th>`, `data-reorder-list`, `btn-icon`} {
				if strings.Contains(tbl, bad) {
					t.Errorf("list table still contains %q", bad)
				}
			}
			head := tbl[strings.Index(tbl, "<thead"):strings.Index(tbl, "</thead>")]
			cols := len(regexp.MustCompile(`<th[ >]`).FindAllString(head, -1))
			spans := regexp.MustCompile(`colspan="(\d+)"`).FindAllStringSubmatch(tbl, -1)
			if len(spans) == 0 {
				t.Errorf("expected at least one colspan row")
			}
			for _, s := range spans {
				if s[1] != strconv.Itoa(cols) {
					t.Errorf("colspan=%s, table has %d columns", s[1], cols)
				}
			}
		})
	}
}

// assertEditHint6056 pins the page-level description target (ut-docs#6056):
// exactly one, hidden, outside the table, carrying the rendered common.edit.
func assertEditHint6056(t *testing.T, body, tableID, hintID string) {
	t.Helper()
	if n := strings.Count(body, `id="`+hintID+`"`); n != 1 {
		t.Fatalf("id=%q appears %d times, want 1", hintID, n)
	}
	const tag = `<span id="%s" class="visually-hidden" hidden>Edit</span>`
	want := strings.Replace(tag, "%s", hintID, 1)
	h := strings.Index(body, want)
	if h < 0 {
		t.Fatalf("hint %s missing", want)
	}
	i := strings.Index(body, `id="`+tableID+`"`)
	if i < 0 {
		t.Fatalf("table %s missing", tableID)
	}
	end := strings.Index(body[i:], "</table>")
	if end < 0 {
		t.Fatalf("table %s not closed", tableID)
	}
	if h >= i && h < i+end {
		t.Errorf("hint %s must sit outside the table", hintID)
	}
}

// ut-docs#6056: categories uses the same page-level hint; the table keeps its
// reorder hint, and the buttons keep their aria-keyshortcuts.
func TestRowTapEdit_Categories_6056(t *testing.T) {
	mux, d := newCategoriesTestMux(t)
	seed2699(t, func(s string, a ...any) error { _, err := d.Db.Exec(s, a...); return err })
	manager := auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(httptest.NewRequest(http.MethodGet, "/categories", nil), manager))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /categories: %d", rec.Code)
	}
	body := rec.Body.String()
	assertEditHint6056(t, body, "categories-table", "categories-row-edit-hint")
	if !strings.Contains(body, `id="categories-table" aria-describedby="categories-reorder-hint"`) {
		t.Errorf("table lost its reorder hint")
	}
	btns := regexp.MustCompile(`<button[^>]*data-record-edit[^>]*>`).FindAllString(body, -1)
	if len(btns) == 0 {
		t.Fatalf("no category name buttons")
	}
	for _, b := range btns {
		if !strings.Contains(b, `aria-describedby="categories-row-edit-hint"`) || !strings.Contains(b, "aria-keyshortcuts=") {
			t.Errorf("category button: %s", b)
		}
		if strings.Contains(b, "categories-reorder-hint") {
			t.Errorf("button must not carry the table-level reorder hint: %s", b)
		}
	}
}
