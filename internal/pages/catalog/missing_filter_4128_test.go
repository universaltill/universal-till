package catalog

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#4128: /catalog?missing=<mask> lists only the items with a gap
// (1 barcode, 2 image, 4 price, 8 category) -- the link the AI assistant's
// answers carry -- and says so with a way back to the full list.
func TestCatalogPage_MissingFilter_4128(t *testing.T) {
	chdirToRepoRoot(t)
	db := setupCatalogPageDB(t)
	defer db.Close()
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "withcode", SKU: "S1", Name: "Has barcode item", BasePrice: 350, IsActive: true})
	testsupport.SeedItem(t, db, testsupport.ItemSeed{ID: "nocode", SKU: "S2", Name: "Lacks barcode item", BasePrice: 350, IsActive: true})
	if _, err := db.Exec(`INSERT INTO item_barcodes(item_id, barcode, is_primary) VALUES('withcode', '4006381333931', 1)`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, &common.Deps{Db: db, State: common.RuntimeState{Theme: "default"}, Menu: []common.MenuItem{}})
	get := func(q string) string {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/catalog"+q, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /catalog%s = %d", q, rec.Code)
		}
		return rec.Body.String()
	}
	body := get("?missing=1")
	if !strings.Contains(body, "Lacks barcode item") || strings.Contains(body, "Has barcode item") {
		t.Errorf("missing=1 did not filter to the item without a barcode")
	}
	if !strings.Contains(body, "Showing only items without a barcode") || !strings.Contains(body, `href="/catalog"`) {
		t.Errorf("filter banner or way back missing")
	}
	// Unfiltered and nonsense masks show everything, with no banner.
	for _, q := range []string{"", "?missing=0", "?missing=99", "?missing=x"} {
		b := get(q)
		if !strings.Contains(b, "Has barcode item") || strings.Contains(b, "Showing only items") {
			t.Errorf("%q: should list everything without a banner", q)
		}
	}
}
