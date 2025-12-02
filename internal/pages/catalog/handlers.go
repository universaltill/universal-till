package catalog

import (
	"context"
	"database/sql"
	"errors"
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
		itemInput, err := parseItemInput(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := pos.CreateItem(r.Context(), db, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, _ := listItems(r.Context(), db)
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_table.html"),
		), funcs)("catalog_table", map[string]any{"Items": items})(w, r)
	})

	// Update item
	mux.HandleFunc("/api/catalog/item/update", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		itemInput, err := parseItemInput(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		itemInput.ID = strings.TrimSpace(r.Form.Get("id"))
		if err := pos.UpdateItem(r.Context(), db, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, _ := listItems(r.Context(), db)
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_table.html"),
		), funcs)("catalog_table", map[string]any{"Items": items})(w, r)
	})

	// Deactivate item
	mux.HandleFunc("/api/catalog/item/deactivate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("id"))
		if itemID == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := pos.DeactivateItem(r.Context(), db, itemID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, _ := listItems(r.Context(), db)
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_table.html"),
		), funcs)("catalog_table", map[string]any{"Items": items})(w, r)
	})

	// Create or update variant (if id present => update)
	mux.HandleFunc("/api/catalog/variant", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		if itemID == "" {
			http.Error(w, "itemId required", http.StatusBadRequest)
			return
		}
		priceStr := strings.TrimSpace(r.Form.Get("price"))
		if priceStr == "" {
			http.Error(w, "price required", http.StatusBadRequest)
			return
		}
		price, err := strconv.ParseInt(priceStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid price", http.StatusBadRequest)
			return
		}
		vInput := pos.VariantInput{
			ID:       strings.TrimSpace(r.Form.Get("id")),
			ItemID:   itemID,
			SKU:      strings.TrimSpace(r.Form.Get("sku")),
			Name:     strings.TrimSpace(r.Form.Get("name")),
			Price:    price,
			IsActive: r.Form.Get("isActive") != "0",
		}
		if costStr := strings.TrimSpace(r.Form.Get("costPrice")); costStr != "" {
			if c, err := strconv.ParseInt(costStr, 10, 64); err == nil {
				vInput.CostPrice = &c
			} else {
				http.Error(w, "invalid costPrice", http.StatusBadRequest)
				return
			}
		}
		if vInput.ID == "" {
			if _, err := pos.CreateVariant(r.Context(), db, vInput); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		} else {
			if err := pos.UpdateVariant(r.Context(), db, vInput); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		items, _ := listItems(r.Context(), db)
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_table.html"),
		), funcs)("catalog_table", map[string]any{"Items": items})(w, r)
	})

	// Deactivate variant
	mux.HandleFunc("/api/catalog/variant/deactivate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		variantID := strings.TrimSpace(r.Form.Get("id"))
		if variantID == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := pos.DeactivateVariant(r.Context(), db, variantID); err != nil {
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
	rows, err := db.QueryContext(ctx, `SELECT id, sku, name, description, category_id, brand_id, unit, base_price, tax_code_id, is_active, is_weighed FROM items WHERE is_active = 1 ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pos.ItemInput
	for rows.Next() {
		var itm pos.ItemInput
		var tax, cat, brand, desc sql.NullString
		if err := rows.Scan(&itm.ID, &itm.SKU, &itm.Name, &desc, &cat, &brand, &itm.Unit, &itm.BasePrice, &tax, &itm.IsActive, &itm.IsWeighed); err != nil {
			return nil, err
		}
		if desc.Valid {
			itm.Description = desc.String
		}
		if tax.Valid {
			itm.TaxCodeID = &tax.String
		}
		if cat.Valid {
			itm.CategoryID = &cat.String
		}
		if brand.Valid {
			itm.BrandID = &brand.String
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

func parseItemInput(r *http.Request) (pos.ItemInput, error) {
	name := strings.TrimSpace(r.Form.Get("name"))
	priceStr := strings.TrimSpace(r.Form.Get("price"))
	if name == "" || priceStr == "" {
		return pos.ItemInput{}, errors.New("name and price required")
	}
	price, err := strconv.ParseInt(priceStr, 10, 64)
	if err != nil {
		return pos.ItemInput{}, errors.New("invalid price")
	}
	cat := strPtr(r.Form.Get("categoryId"))
	brand := strPtr(r.Form.Get("brandId"))
	taxCode := strPtr(r.Form.Get("taxCode"))
	cost := strPtr(r.Form.Get("costPrice")) // cost not stored on item; kept for future?
	_ = cost                                // placeholder to avoid unused; cost handling can be added if schema supports
	return pos.ItemInput{
		Name:        name,
		SKU:         strings.TrimSpace(r.Form.Get("sku")),
		BasePrice:   price,
		TaxCodeID:   taxCode,
		CategoryID:  cat,
		BrandID:     brand,
		Description: strings.TrimSpace(r.Form.Get("description")),
		Unit:        strings.TrimSpace(r.Form.Get("unit")),
		IsWeighed:   r.Form.Get("isWeighed") == "1" || strings.ToLower(r.Form.Get("isWeighed")) == "on",
		IsActive:    r.Form.Get("isActive") != "0",
	}, nil
}
func files(paths ...string) []string { return paths }
