package catalog

import (
	"context"
	"database/sql"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pos"
)

// Register mounts catalog list/create and barcode attach endpoints.
func Register(mux *http.ServeMux, db *sql.DB, theme string, menu []map[string]string) {
	mux.HandleFunc("/catalog", func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		items, err := listItems(r.Context(), db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data := map[string]any{
			"title":     "Catalog",
			"menuItems": menu,
			"theme":     theme,
			"Items":     items,
		}
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "catalog.html"),
			filepath.Join("web", "ui", "partials", "nav.html"),
			filepath.Join("web", "ui", "partials", "catalog_table.html"),
		), funcs)("base", data)(w, r)
	})

	mux.HandleFunc("/api/catalog/item", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		name := strings.TrimSpace(r.Form.Get("name"))
		sku := strings.TrimSpace(r.Form.Get("sku"))
		priceStr := strings.TrimSpace(r.Form.Get("price"))
		taxCode := strings.TrimSpace(r.Form.Get("taxCode"))
		if name == "" || priceStr == "" {
			http.Error(w, "name and price required", http.StatusBadRequest)
			return
		}
		price, err := strconv.ParseInt(priceStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid price", http.StatusBadRequest)
			return
		}
		if _, err := pos.CreateItem(r.Context(), db, pos.ItemInput{
			Name:      name,
			SKU:       sku,
			BasePrice: price,
			TaxCodeID: strPtr(taxCode),
			IsActive:  true,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, _ := listItems(r.Context(), db)
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_table.html"),
		), funcs)("catalog_table", map[string]any{"Items": items})(w, r)
	})

	mux.HandleFunc("/api/catalog/barcode", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		barcode := strings.TrimSpace(r.Form.Get("barcode"))
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		variantID := strings.TrimSpace(r.Form.Get("variantId"))
		isPrimary := r.Form.Get("isPrimary") == "1" || strings.ToLower(r.Form.Get("isPrimary")) == "on"
		if barcode == "" {
			http.Error(w, "barcode required", http.StatusBadRequest)
			return
		}
		if err := pos.AddBarcode(r.Context(), db, pos.BarcodeInput{
			Barcode:   barcode,
			ItemID:    itemID,
			VariantID: variantID,
			IsPrimary: isPrimary,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, _ := listItems(r.Context(), db)
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_table.html"),
		), funcs)("catalog_table", map[string]any{"Items": items})(w, r)
	})
}

func listItems(ctx context.Context, db *sql.DB) ([]pos.ItemInput, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, sku, name, base_price, tax_code_id, is_active FROM items WHERE is_active = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pos.ItemInput
	for rows.Next() {
		var itm pos.ItemInput
		var tax sql.NullString
		if err := rows.Scan(&itm.ID, &itm.SKU, &itm.Name, &itm.BasePrice, &tax, &itm.IsActive); err != nil {
			return nil, err
		}
		if tax.Valid {
			itm.TaxCodeID = &tax.String
		}
		out = append(out, itm)
	}
	return out, rows.Err()
}

func strPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}
func files(paths ...string) []string { return paths }
