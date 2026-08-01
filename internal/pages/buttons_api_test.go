package pages

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/ui"
)

// newButtonsMux wires registerButtonsAPI over a DB seeded with the pages
// fixture plus the shortcut_buttons table (created by migrations in prod;
// the shared fixture doesn't include it).
func newButtonsMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	chdirRoot(t)
	initPagesI18n(t)
	db := openPagesTestDB(t)
	t.Cleanup(func() { db.Close() })
	seedForPages(t, db)
	if _, err := db.Exec(`CREATE TABLE shortcut_buttons (barcode TEXT PRIMARY KEY, label TEXT, item_id TEXT, image_path TEXT, sort_order INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatalf("create shortcut_buttons: %v", err)
	}
	// LoadButtons/SearchItemsForShortcuts join item_images for tile thumbnails.
	if _, err := db.Exec(`CREATE TABLE item_images (id TEXT PRIMARY KEY, item_id TEXT NOT NULL, path TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'thumbnail')`); err != nil {
		t.Fatalf("create item_images: %v", err)
	}
	d := &common.Deps{Db: db, BtnStore: ui.NewButtonStore(db)}
	mux := http.NewServeMux()
	registerButtonsAPI(mux, d)
	return mux, d
}

func TestButtonsUIFragmentRendersSeededButtons(t *testing.T) {
	mux, d := newButtonsMux(t)
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id) VALUES ('ABC','Apple Tile','itm1')`); err != nil {
		t.Fatalf("seed button: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/buttons = %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Apple Tile") {
		t.Fatalf("buttons fragment missing seeded label: %s", rec.Body.String())
	}
}

// The sale-screen tile itself (not just the Designer admin panel) previously
// interpolated a shortcut button's barcode/code directly into a hand-written
// hx-vals JSON literal -- same bug class as buttons_admin.html's AddVals fix,
// but higher-impact: this is the tile a cashier actually taps at checkout
// (ut-docs#19, flagged by independent review as the highest-impact miss in
// the original sweep). shortcut_buttons.barcode has no format restriction
// (TestBarcodeAttach_PanelHxValsSurvivesQuotedBarcode already proves a
// quote-containing barcode can be attached), so a button seeded with one
// must still round-trip as valid JSON via httpx.jsonVals.
func TestButtonsUIFragment_HxValsSurvivesQuotedCode(t *testing.T) {
	mux, d := newButtonsMux(t)
	weird := `we"ird'code`
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id) VALUES (?,'Weird Tile','itm1')`, weird); err != nil {
		t.Fatalf("seed button: %v", err)
	}

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/buttons", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/ui/buttons = %d (%s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	idx := strings.Index(body, `hx-post="/api/pos/scan"`)
	if idx == -1 {
		t.Fatalf("expected a sale-screen tile posting to /api/pos/scan, got %.500s", body)
	}
	valsIdx := strings.Index(body[idx:], "hx-vals='")
	if valsIdx == -1 {
		t.Fatalf("expected an hx-vals attribute on the tile, got %.500s", body[idx:])
	}
	start := idx + valsIdx + len("hx-vals='")
	end := strings.Index(body[start:], "'")
	if end == -1 {
		t.Fatalf("unterminated hx-vals attribute")
	}
	raw := body[start : start+end]

	var vals map[string]string
	if err := json.Unmarshal([]byte(html.UnescapeString(raw)), &vals); err != nil {
		t.Fatalf("hx-vals is not valid JSON after HTML-unescape: %v (raw attribute: %q)", err, raw)
	}
	if vals["code"] != weird {
		t.Errorf("code round-trip = %q, want %q", vals["code"], weird)
	}
}

func TestButtonsAddValidatesPersistsAndNormalizesImage(t *testing.T) {
	mux, d := newButtonsMux(t)

	// Missing required fields -> 400 from the store's validation.
	if rec := postForm(mux, "/api/buttons/add", url.Values{"label": {"No Code"}}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("add without code/itemId = %d, want 400", rec.Code)
	}

	// A bare image filename is normalized into /public/images/.
	rec := postForm(mux, "/api/buttons/add", url.Values{
		"label":    {"Apple"},
		"code":     {"ABC"},
		"itemId":   {"itm1"},
		"imageUrl": {"apple.png"},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("add = %d (%s)", rec.Code, rec.Body.String())
	}
	var image string
	if err := d.Db.QueryRow(`SELECT image_path FROM shortcut_buttons WHERE barcode='ABC'`).Scan(&image); err != nil {
		t.Fatalf("added button missing: %v", err)
	}
	if image != "/public/images/apple.png" {
		t.Fatalf("image_path = %q, want /public/images/apple.png", image)
	}
	// The response is the re-rendered admin grid containing the new tile.
	if !strings.Contains(rec.Body.String(), "Apple") {
		t.Fatalf("admin grid response missing added button: %s", rec.Body.String())
	}

	// An absolute URL is stored untouched.
	rec = postForm(mux, "/api/buttons/add", url.Values{
		"label":    {"Pear"},
		"code":     {"DEF"},
		"itemId":   {"itm1"},
		"imageUrl": {"https://cdn.example.com/pear.png"},
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("add absolute-url = %d (%s)", rec.Code, rec.Body.String())
	}
	if err := d.Db.QueryRow(`SELECT image_path FROM shortcut_buttons WHERE barcode='DEF'`).Scan(&image); err != nil {
		t.Fatalf("added button missing: %v", err)
	}
	if image != "https://cdn.example.com/pear.png" {
		t.Fatalf("image_path = %q, want the absolute URL untouched", image)
	}
}

func TestButtonsRemoveDeletesRow(t *testing.T) {
	mux, d := newButtonsMux(t)
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id) VALUES ('ABC','Apple','itm1')`); err != nil {
		t.Fatalf("seed button: %v", err)
	}

	rec := postForm(mux, "/api/buttons/remove", url.Values{"code": {"ABC"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove = %d (%s)", rec.Code, rec.Body.String())
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM shortcut_buttons WHERE barcode='ABC'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatalf("button still present after remove")
	}
}

func TestButtonsSearchShortQueryHintAndResults(t *testing.T) {
	mux, _ := newButtonsMux(t)

	// Under 3 characters: a hint, not a query.
	rec := postForm(mux, "/api/buttons/search", url.Values{"q": {"Ap"}}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Type 3+ characters") {
		t.Fatalf("short query: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// A real query finds the seeded item (name "Apple", barcode ABC) via
	// either the q or the legacy search field name.
	for _, field := range []string{"q", "search"} {
		rec = postForm(mux, "/api/buttons/search", url.Values{field: {"Appl"}}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("search via %s = %d (%s)", field, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "Apple") {
			t.Fatalf("search via %s missing seeded item: %s", field, rec.Body.String())
		}
	}

	// A large offset pages past the single result.
	rec = postForm(mux, "/api/buttons/search?offset=50", url.Values{"q": {"Appl"}}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("offset search = %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Apple") {
		t.Fatalf("offset=50 should page past the only result: %s", rec.Body.String())
	}

	// A non-numeric offset is ignored, not an error.
	rec = postForm(mux, "/api/buttons/search?offset=bogus", url.Values{"q": {"Appl"}}, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Apple") {
		t.Fatalf("bogus offset: code=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestButtonsReorderEdgeCases(t *testing.T) {
	mux, d := newButtonsMux(t)
	if _, err := d.Db.Exec(`INSERT INTO shortcut_buttons(barcode,label) VALUES ('b1','A'),('b2','B'),('b3','C')`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// No codes at all -> 400.
	if rec := postForm(mux, "/api/buttons/reorder", url.Values{}, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty reorder = %d, want 400", rec.Code)
	}

	// A single comma-joined field is split and applied (urlencoded path).
	rec := postForm(mux, "/api/buttons/reorder", url.Values{"codes": {"b2, b3, b1"}}, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("comma-joined reorder = %d (%s)", rec.Code, rec.Body.String())
	}
	var first string
	if err := d.Db.QueryRow(`SELECT barcode FROM shortcut_buttons ORDER BY sort_order LIMIT 1`).Scan(&first); err != nil {
		t.Fatalf("read order: %v", err)
	}
	if first != "b2" {
		t.Fatalf("first after reorder = %q, want b2 (comma-joined codes must split + trim)", first)
	}
}

func TestButtonsStoreErrorsSurfaceAs500(t *testing.T) {
	mux, d := newButtonsMux(t)
	// Break the storage underneath the handlers.
	if _, err := d.Db.Exec(`DROP TABLE shortcut_buttons`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := d.Db.Exec(`DROP TABLE items`); err != nil {
		t.Fatalf("drop items: %v", err)
	}

	if rec := postForm(mux, "/api/buttons/reorder", url.Values{"codes": {"b1"}}, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("reorder with broken store = %d, want 500", rec.Code)
	}
	if rec := postForm(mux, "/api/buttons/search", url.Values{"q": {"Appl"}}, nil); rec.Code != http.StatusInternalServerError {
		t.Fatalf("search with broken store = %d, want 500", rec.Code)
	}
}
