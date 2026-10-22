package pages

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/settings"
	"github.com/universaltill/universal-till/internal/ui"
)

// ut-docs#4838 (independent review): a photo set in the item editor
// (CatalogRepo.SetItemThumbnail — what the upload handler and my.'s
// set_catalog_image directive call) must show on the BASKET row whatever
// code the sale screen sends for the item: its barcode (scan), its SKU, a
// shortcut_buttons row's code (an explicit quick tile, even one whose
// image_path froze a placeholder), and the item:<id> code of an implicit
// All-tab tile. Unlike newPOSTestDeps this wires the REAL production
// resolver (ui.PriceResolverAdapter over a ButtonStore), so the image comes
// from the same repo queries the till runs.
func TestBasketRow_ShowsItemEditorPhoto_4838(t *testing.T) {
	const photo = "/public/assets/items/itm-photo/thumb.png"
	cases := []struct{ name, code string }{
		{"barcode scan", "5012345678900"},
		{"sku", "PHOTO-SKU"},
		{"explicit quick tile with frozen placeholder", "TILE-PHOTO"},
		{"implicit tile item: code", "item:itm-photo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chdirRoot(t)
			db := openPagesTestDB(t)
			t.Cleanup(func() { db.Close() })
			seedForPages(t, db)
			for _, q := range []string{
				`INSERT INTO items(id,sku,name,base_price,tax_code_id,is_active) VALUES('itm-photo','PHOTO-SKU','Photo Lager',450,'tax_std',1)`,
				`INSERT INTO item_barcodes(barcode,item_id,is_primary) VALUES('5012345678900','itm-photo',1)`,
				`INSERT INTO shortcut_buttons(barcode,item_id,label,image_path,sort_order) VALUES('TILE-PHOTO','itm-photo','Photo Lager','/public/assets/category-icons/generic.svg',0)`,
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatalf("seed %q: %v", q, err)
				}
			}
			if err := data.NewCatalogRepo(db).SetItemThumbnail(context.Background(), "itm-photo", photo); err != nil {
				t.Fatal(err)
			}

			engine := pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, ui.PriceResolverAdapter{Store: ui.NewButtonStore(db)})
			cfg := &config.Config{Theme: "default", Locales: config.Locales{Currency: "GBP", TaxRateBP: 2000}}
			pm, err := plugins.Init(t.Context(), cfg, db)
			if err != nil {
				t.Fatalf("init plugins: %v", err)
			}
			setStore := settings.NewStore(db)
			dp := &common.Deps{
				Cfg: cfg, Db: db, State: common.LoadState(t.Context(), setStore, cfg),
				Menu: []common.MenuItem{{Href: "/", Label: "Home"}}, Engine: engine, Pm: pm, Settings: setStore,
			}
			t.Cleanup(dp.WaitForAsyncWork)
			mux := http.NewServeMux()
			registerPOSAPI(mux, dp)

			rec := posPostForm(mux, "/api/pos/scan", "code="+tc.code+"&src=tile")
			body := rec.Body.String()
			if rec.Code != http.StatusOK || !strings.Contains(body, "Photo Lager") {
				t.Fatalf("scan %s: status %d, line not in basket: %s", tc.code, rec.Code, body)
			}
			if !strings.Contains(body, `class="line-thumb" src="`+photo) {
				t.Fatalf("basket row for %s does not show the item photo %s:\n%s", tc.code, photo, basketLinesOf(body))
			}
		})
	}
}

func basketLinesOf(body string) string {
	if i := strings.Index(body, `id="basket-lines"`); i >= 0 {
		end := i + 1500
		if end > len(body) {
			end = len(body)
		}
		return body[i:end]
	}
	return body
}
