package pages

import (
	"context"
	"encoding/json"
	"html/template"
	"math"
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// stockRow is one rendered row of the /inventory stock-levels table.
type stockRow struct {
	data.LowStockItem
	Low      bool
	DaysLeft int  // predicted days of stock left; -1 = no sell rate
	RunsOut  bool // predicted stockout within the warning window
	OrderQty int  // suggested order to reach coverDays of stock; 0 = none
}

// stockLevelsForDisplay computes the /inventory table's rows and the
// running-out count — shared by the full page render and the
// stock-updated-triggered partial refresh (registerInventoryPage's
// /ui/inventory/stock-table), so both use identical logic.
func stockLevelsForDisplay(ctx context.Context, d *common.Deps) ([]stockRow, int) {
	posRepo := data.NewPOSRepo(d.Db)
	rawLevels, _ := posRepo.ListStockLevels(ctx)
	// Sell-rate prediction (28-day average): "this item runs out in ~N
	// days at the current rate". Best-effort — no history, no column.
	rates, _ := posRepo.ItemDailySellRates(ctx, 28)

	const warnDays = 7
	// coverDays is the stock target the suggestion refills to: two weeks
	// of sales at the current rate (a sensible default until per-item
	// lead times exist).
	const coverDays = 14
	runningOut := 0
	levels := make([]stockRow, 0, len(rawLevels))
	for _, l := range rawLevels {
		row := stockRow{
			LowStockItem: l,
			Low:          l.ReorderLevel > 0 && l.CurrentQty < float64(l.ReorderLevel),
			DaysLeft:     -1,
		}
		if rate := rates[l.ItemID]; rate > 0 && l.CurrentQty > 0 {
			row.DaysLeft = int(l.CurrentQty / rate)
			row.RunsOut = row.DaysLeft <= warnDays
		} else if rate > 0 && l.CurrentQty <= 0 {
			row.DaysLeft = 0
			row.RunsOut = true
		}
		if rate := rates[l.ItemID]; row.RunsOut && rate > 0 {
			if need := rate*coverDays - l.CurrentQty; need > 0 {
				row.OrderQty = int(math.Ceil(need))
			}
		}
		if row.RunsOut {
			runningOut++
		}
		levels = append(levels, row)
	}
	return levels, runningOut
}

func registerInventoryPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/inventory", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		catRepo := data.NewCatalogRepo(d.Db)

		levels, runningOut := stockLevelsForDisplay(ctx, d)
		locations, _ := data.NewPOSRepo(d.Db).ListStockLocations(ctx)
		items, _ := catRepo.ListItems(ctx)

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
			"RunningOut":  runningOut,
			"Locations":   locations,
			"ItemsJSON":   template.JS(pickerJSON),
			"SyncPrimary": d.SyncPrimaryURL(r.Context()),
		}
		httpx.Render("ui/pages/inventory.html", data)(w, r)
	})

	// Stock-levels table, on its own so it can be refreshed in place after a
	// receive/adjust/override/return (see writeHTMLStockChanged's
	// HX-Trigger: stock-updated, which the table in inventory.html listens
	// for) without a full page reload.
	mux.HandleFunc("/ui/inventory/stock-table", func(w http.ResponseWriter, r *http.Request) {
		levels, runningOut := stockLevelsForDisplay(r.Context(), d)
		httpx.RenderPartial("ui/partials/stock_table.html", map[string]any{
			"StockLevels": levels,
			"RunningOut":  runningOut,
		})(w, r)
	})
}
