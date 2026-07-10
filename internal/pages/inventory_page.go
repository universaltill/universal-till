package pages

import (
	"encoding/json"
	"html/template"
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

func registerInventoryPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/inventory", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		posRepo := data.NewPOSRepo(d.Db)
		catRepo := data.NewCatalogRepo(d.Db)

		rawLevels, _ := posRepo.ListStockLevels(ctx)
		locations, _ := posRepo.ListStockLocations(ctx)
		items, _ := catRepo.ListItems(ctx)

		type stockRow struct {
			data.LowStockItem
			Low bool
		}
		levels := make([]stockRow, 0, len(rawLevels))
		for _, l := range rawLevels {
			levels = append(levels, stockRow{
				LowStockItem: l,
				Low:          l.ReorderLevel > 0 && l.CurrentQty < float64(l.ReorderLevel),
			})
		}

		// Compact id/name/sku list for the item picker.
		type pickerItem struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			SKU  string `json:"sku"`
		}
		picker := make([]pickerItem, 0, len(items))
		for _, it := range items {
			picker = append(picker, pickerItem{ID: it.ID, Name: it.Name, SKU: it.SKU})
		}
		pickerJSON, _ := json.Marshal(picker)

		data := map[string]any{
			"title":       "Inventory",
			"theme":       d.CurrentState().Theme,
			"menuItems":   d.Menu,
			"StockLevels": levels,
			"Locations":   locations,
			"ItemsJSON":   template.JS(pickerJSON),
		}
		httpx.Render("ui/pages/inventory.html", data)(w, r)
	})
}
