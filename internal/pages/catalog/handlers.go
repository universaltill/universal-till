package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Register mounts catalog list/create and barcode attach endpoints.
// func Register(mux *http.ServeMux, db *sql.DB, theme string, menu []map[string]string) {
func Register(mux *http.ServeMux, d *common.Deps) {

	mux.HandleFunc("/catalog", func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		items, err := listItems(r.Context(), d.Db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cats, brands, taxCodes, err := listLookups(r.Context(), d.Db)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data := map[string]any{
			"title":      "Catalog",
			"menuItems":  d.Menu,
			"theme":      d.State.Theme,
			"Items":      items,
			"Categories": cats,
			"Brands":     brands,
			"TaxCodes":   taxCodes,
		}
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "catalog.html"),
			filepath.Join("web", "ui", "partials", "nav.html"),
			filepath.Join("web", "ui", "partials", "catalog_lookups.html"),
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
		if err := validateLookups(r.Context(), d.Db, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := pos.CreateItem(r.Context(), d.Db, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, _ := listItems(r.Context(), d.Db)
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
		if err := validateLookups(r.Context(), d.Db, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := pos.UpdateItem(r.Context(), d.Db, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, _ := listItems(r.Context(), d.Db)
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
		if err := pos.DeactivateItem(r.Context(), d.Db, itemID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, _ := listItems(r.Context(), d.Db)
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
			if _, err := pos.CreateVariant(r.Context(), d.Db, vInput); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		} else {
			if err := pos.UpdateVariant(r.Context(), d.Db, vInput); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		items, _ := listItems(r.Context(), d.Db)
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
		if err := pos.DeactivateVariant(r.Context(), d.Db, variantID); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, _ := listItems(r.Context(), d.Db)
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
		if err := pos.AddBarcode(r.Context(), d.Db, pos.BarcodeInput{
			Barcode:   barcode,
			ItemID:    itemID,
			VariantID: variantID,
			IsPrimary: isPrimary,
		}); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		items, _ := listItems(r.Context(), d.Db)
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

type lookup struct {
	ID   string
	Name string
}

func listLookups(ctx context.Context, db *sql.DB) ([]lookup, []lookup, []lookup, error) {
	cats, err := readLookup(ctx, db, "categories")
	if err != nil {
		return nil, nil, nil, err
	}
	brands, err := readLookup(ctx, db, "brands")
	if err != nil {
		return nil, nil, nil, err
	}
	taxCodes, err := readLookup(ctx, db, "tax_codes")
	if err != nil {
		return nil, nil, nil, err
	}
	return cats, brands, taxCodes, nil
}

func readLookup(ctx context.Context, db *sql.DB, table string) ([]lookup, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, name FROM `+table+` WHERE (is_active IS NULL OR is_active = 1) ORDER BY name`)
	if err != nil {
		// Fallback for tables without is_active column
		if strings.Contains(err.Error(), "no such column: is_active") {
			rows, err = db.QueryContext(ctx, `SELECT id, name FROM `+table+` ORDER BY name`)
		}
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []lookup
	for rows.Next() {
		var l lookup
		if err := rows.Scan(&l.ID, &l.Name); err != nil {
			return nil, err
		}
		res = append(res, l)
	}
	return res, rows.Err()
}

func validateLookups(ctx context.Context, db *sql.DB, in pos.ItemInput) error {
	check := func(table, id string) error {
		var exists int
		if err := db.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ?`, id).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%s not found: %s", table, id)
			}
			return err
		}
		return nil
	}
	if in.CategoryID != nil {
		if err := check("categories", *in.CategoryID); err != nil {
			return err
		}
	}
	if in.BrandID != nil {
		if err := check("brands", *in.BrandID); err != nil {
			return err
		}
	}
	if in.TaxCodeID != nil {
		if err := check("tax_codes", *in.TaxCodeID); err != nil {
			return err
		}
	}
	return nil
}
func files(paths ...string) []string { return paths }
