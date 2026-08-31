package catalog

import (
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/testsupport"
)

func TestItemCreate_DuplicateSKUIs400(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "DUP", Name: "First", BasePrice: 100, IsActive: true})
	rec := postForm(t, mux, "/api/catalog/item", "name=Second&price=200&sku=DUP&isActive=1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for duplicate sku, got %d: %s", rec.Code, rec.Body.String())
	}
	// ut-docs#316's review: a duplicate SKU must stay a SPECIFIC, actionable
	// message, not fall back to the generic "invalid request" every other
	// repo-layer error now gets.
	if !strings.Contains(rec.Body.String(), "already in use") {
		t.Fatalf("want the specific duplicate-SKU message, got %s", rec.Body.String())
	}
}

func TestItemUpdate_DuplicateSKUIs400(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "A", Name: "One", BasePrice: 100, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm2", SKU: "B", Name: "Two", BasePrice: 100, IsActive: true})
	rec := postForm(t, mux, "/api/catalog/item/update", "id=itm2&name=Two&price=100&sku=A&isActive=1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for duplicate sku on update, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already in use") {
		t.Fatalf("want the specific duplicate-SKU message, got %s", rec.Body.String())
	}
}

func TestItemDeactivate_RepoErrorIs400(t *testing.T) {
	mux, db := newCatalogMux(t)
	if _, err := db.Exec(`DROP TABLE items`); err != nil {
		t.Fatal(err)
	}
	if rec := postForm(t, mux, "/api/catalog/item/deactivate", "id=itm1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

// The variant endpoint's own checkbox-pair handling: a checked box submits
// the hidden 0 AND the checkbox 1 (DOM order) and must read as active.
func TestVariantUpdate_CheckedCheckboxPairReadsActive(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: false})

	rec := postForm(t, mux, "/api/catalog/variant", "id=v1&itemId=itm1&name=Large&sku=S1-L&price=350&isActive=0&isActive=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var active int
	if err := db.QueryRow(`SELECT is_active FROM item_variants WHERE id = 'v1'`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatal("hidden-0-plus-checkbox-1 submission must reactivate the variant")
	}
}

func TestVariantUpdate_DuplicateSKUIs400(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v2", ItemID: "itm1", SKU: "S1-M", Name: "Medium", Price: 300, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/variant", "id=v2&itemId=itm1&name=Medium&sku=S1-L&price=300&isActive=1")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400 for duplicate variant sku, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already in use") {
		t.Fatalf("want the specific duplicate-SKU message, got %s", rec.Body.String())
	}
}

func TestModifierGroupAndOption_RepoErrorsAre400(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	if _, err := db.Exec(`DROP TABLE item_modifier_options`); err != nil {
		t.Fatal(err)
	}
	if rec := postForm(t, mux, "/api/catalog/modifier-option", "groupId=g1&itemId=itm1&name=Shot&isActive=1"); rec.Code != http.StatusBadRequest {
		t.Errorf("create option repo error: want 400, got %d", rec.Code)
	}
	if rec := postForm(t, mux, "/api/catalog/modifier-option", "id=o1&groupId=g1&itemId=itm1&name=Shot&isActive=1"); rec.Code != http.StatusBadRequest {
		t.Errorf("update option repo error: want 400, got %d", rec.Code)
	}
	if _, err := db.Exec(`DROP TABLE item_modifier_groups`); err != nil {
		t.Fatal(err)
	}
	if rec := postForm(t, mux, "/api/catalog/modifier-group", "itemId=itm1&name=Extras&isActive=1"); rec.Code != http.StatusBadRequest {
		t.Errorf("create group repo error: want 400, got %d", rec.Code)
	}
	if rec := postForm(t, mux, "/api/catalog/modifier-group", "id=g1&itemId=itm1&name=Extras&isActive=1"); rec.Code != http.StatusBadRequest {
		t.Errorf("update group repo error: want 400, got %d", rec.Code)
	}
}

func TestVariantDeactivate_TableResponseAndRepoError(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	// Without panelItem the response is the affected item's row as an
	// out-of-band fragment (ut-docs#1363), resolved from the variant.
	rec := postForm(t, mux, "/api/catalog/variant/deactivate", "id=v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `id="catalog-row-itm1"`) {
		t.Fatal("table-scoped deactivate must answer with the affected item's row fragment")
	}

	if _, err := db.Exec(`DROP TABLE item_variants`); err != nil {
		t.Fatal(err)
	}
	if rec := postForm(t, mux, "/api/catalog/variant/deactivate", "id=v1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("repo error: want 400, got %d", rec.Code)
	}
}

func TestImageUploads_RejectNonMultipartBody(t *testing.T) {
	mux, _ := newCatalogMux(t)
	for _, path := range []string{"/api/catalog/item/image", "/api/catalog/variant/image"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("item_id=x"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: want 400 for a non-multipart body, got %d", path, rec.Code)
		}
	}
}

func TestVariantImageUpload_Validation(t *testing.T) {
	mux, db := imageUploadTestDeps(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	send := func(fields map[string]string, filename string, fileBytes []byte) *httptest.ResponseRecorder {
		body, ct := multipartUpload(t, fields, filename, fileBytes)
		req := httptest.NewRequest(http.MethodPost, "/api/catalog/variant/image", body)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}

	if rec := send(map[string]string{"variant_id": "v1"}, "p.png", validPNG(t)); rec.Code != http.StatusBadRequest {
		t.Errorf("missing panelItem: want 400, got %d", rec.Code)
	}
	if rec := send(map[string]string{"variant_id": "v1", "panelItem": "../evil"}, "p.png", validPNG(t)); rec.Code != http.StatusBadRequest {
		t.Errorf("traversal panelItem: want 400, got %d", rec.Code)
	}
	if rec := send(map[string]string{"variant_id": "v1", "panelItem": "itm1"}, "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("missing file: want 400, got %d", rec.Code)
	}
	if rec := send(map[string]string{"variant_id": "v1", "panelItem": "itm1"}, "n.txt", []byte("not an image")); rec.Code != http.StatusBadRequest {
		t.Errorf("non-image: want 400, got %d", rec.Code)
	}
}

// A plain file squatting where the item's asset DIRECTORY must go makes
// os.MkdirAll fail — the handler must answer 500, not crash or pretend
// success while the photo silently vanished.
func TestItemImageUpload_MkdirFailureIs500(t *testing.T) {
	mux, db := imageUploadTestDeps(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	blocker := paths.Data("public", "assets", "items", "itm1")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("i am a file, not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	body, ct := multipartUpload(t, map[string]string{"item_id": "itm1"}, "p.png", validPNG(t))
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/item/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 when the asset dir cannot be created, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestBarcodeAttach_PanelResponse(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	rec := postForm(t, mux, "/api/catalog/barcode", "barcode=5000001&itemId=itm1&panelItem=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `id="catalog-row-itm1"`) || !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Fatal("panel-scoped attach must carry the out-of-band item row (ut-docs#1363)")
	}
	if !strings.Contains(body, "5000001") {
		t.Fatal("panel must show the newly attached barcode")
	}
}

// This attach endpoint doesn't enforce productlookup.ValidBarcode's
// digits-only format (that check only gates the external-lookup flow), so a
// barcode containing quote/apostrophe characters can genuinely reach the
// render path. catalog_variants.html previously interpolated it directly
// into a hand-written hx-vals JSON literal -- the same bug class
// buttons_admin.html's AddVals fixed (ut-docs#19) -- producing invalid JSON
// for any such value. Regression guard for the httpx.jsonVals fix.
func TestBarcodeAttach_PanelHxValsSurvivesQuotedBarcode(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})

	weird := `we"ird'code`
	rec := postForm(t, mux, "/api/catalog/barcode", "barcode="+url.QueryEscape(weird)+"&itemId=itm1&panelItem=itm1")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	idx := strings.Index(body, `hx-post="/api/catalog/barcode/delete"`)
	if idx == -1 {
		t.Fatalf("expected a barcode-delete button, got %.500s", body)
	}
	valsIdx := strings.Index(body[idx:], "hx-vals='")
	if valsIdx == -1 {
		t.Fatalf("expected an hx-vals attribute on the delete button, got %.500s", body[idx:])
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
	if vals["barcode"] != weird {
		t.Errorf("barcode round-trip = %q, want %q", vals["barcode"], weird)
	}
}

func TestBarcodeDelete_TableResponse(t *testing.T) {
	mux, db := newCatalogMux(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedBarcode(t, db, "5000001", "itm1", true)

	// Without panelItem the response is the owning item's row fragment
	// (ut-docs#1363), resolved from the barcode before it is detached.
	rec := postForm(t, mux, "/api/catalog/barcode/delete", "barcode=5000001")
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `id="catalog-row-itm1"`) {
		t.Fatal("table-scoped delete must answer with the owning item's row fragment")
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM item_barcodes WHERE barcode = '5000001'`).Scan(&n)
	if n != 0 {
		t.Fatal("barcode not deleted")
	}
}

func TestVariantImageUpload_MkdirFailureIs500(t *testing.T) {
	mux, db := imageUploadTestDeps(t)
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "itm1", SKU: "S1", Name: "Coffee", BasePrice: 300, IsActive: true})
	testsupport.SeedVariant(t, db, testsupport.VariantSeed{ID: "v1", ItemID: "itm1", SKU: "S1-L", Name: "Large", Price: 350, IsActive: true})

	blocker := paths.Data("public", "assets", "items", "itm1", "variants")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("file, not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	body, ct := multipartUpload(t, map[string]string{"variant_id": "v1", "panelItem": "itm1"}, "p.png", validPNG(t))
	req := httptest.NewRequest(http.MethodPost, "/api/catalog/variant/image", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("want 500 when the variant asset dir cannot be created, got %d: %s", rec.Code, rec.Body.String())
	}
}

// saveLookupImage's MkdirAll failure: the created item's id is a fresh
// uuid, so the blocker sits one level up — the items/ tree itself is a
// plain file. The create must still succeed (image save is best-effort).
func TestCatalogCreate_LookupImageMkdirFailureIsNonFatal(t *testing.T) {
	chdirToRepoRoot(t)
	paths.Init(t.TempDir())
	t.Cleanup(func() { paths.Init("") })
	db := setupCatalogPageDB(t)
	defer db.Close()
	stubLookupClient(t, func(w http.ResponseWriter, r *http.Request) {}, imageTransport(t, "http://never-used", func() *http.Response {
		return pngResponse(t)
	}))

	blocker := paths.Data("public", "assets", "items")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocker, []byte("file, not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}})
	form := "name=Cola&price=150&isActive=1&imageUrl=https%3A%2F%2Fimages.openfoodfacts.org%2Ff.png"
	rec := postForm(t, mux, "/api/catalog/item", form)
	if rec.Code != http.StatusOK {
		t.Fatalf("item create must survive an unsavable image: got %d: %s", rec.Code, rec.Body.String())
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM items`).Scan(&n)
	if n != 1 {
		t.Fatalf("item should exist, found %d", n)
	}
}

func TestCatalogPage_BrandAndTaxLookupErrorsAre500(t *testing.T) {
	t.Run("brands table missing", func(t *testing.T) {
		mux, db := newCatalogMux(t)
		if _, err := db.Exec(`DROP TABLE brands`); err != nil {
			t.Fatal(err)
		}
		if rec := get(t, mux, "/catalog"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("want 500, got %d", rec.Code)
		}
	})
	t.Run("tax_codes table missing", func(t *testing.T) {
		mux, db := newCatalogMux(t)
		if _, err := db.Exec(`DROP TABLE tax_codes`); err != nil {
			t.Fatal(err)
		}
		if rec := get(t, mux, "/catalog"); rec.Code != http.StatusInternalServerError {
			t.Fatalf("want 500, got %d", rec.Code)
		}
	})
}
