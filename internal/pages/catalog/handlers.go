package catalog

import (
	"context"
	"errors"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// Register mounts catalog list/create and barcode attach endpoints.
// func Register(mux *http.ServeMux, db *sql.DB, theme string, menu []map[string]string) {
func Register(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewCatalogRepo(d.Db)

	// Every mutation endpoint answers with the refreshed items table.
	renderCatalogTable := func(w http.ResponseWriter, r *http.Request) {
		items, _ := repo.ListItems(r.Context())
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_table.html"),
		), funcs)("catalog_table", map[string]any{"Items": items})(w, r)
	}

	mux.HandleFunc("/catalog", func(w http.ResponseWriter, r *http.Request) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		items, err := repo.ListItems(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		cats, brands, taxCodes, err := listLookups(r.Context(), repo)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		data := map[string]any{
			"title":      "Catalog",
			"menuItems":  d.Menu,
			"theme":      d.CurrentState().Theme,
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
		if err := validateLookups(r.Context(), repo, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := pos.CreateItem(r.Context(), d.Db, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		renderCatalogTable(w, r)
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
		if err := validateLookups(r.Context(), repo, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := pos.UpdateItem(r.Context(), d.Db, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		renderCatalogTable(w, r)
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
		renderCatalogTable(w, r)
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
		renderCatalogTable(w, r)
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
		renderCatalogTable(w, r)
	})

	// Item image upload → web/public/assets/items/<id>/thumb.png (the same
	// convention the product tiles and designer use).
	mux.HandleFunc("POST /api/catalog/item/image", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "invalid upload", http.StatusBadRequest)
			return
		}
		itemID := strings.TrimSpace(r.Form.Get("item_id"))
		if itemID == "" || strings.ContainsAny(itemID, "/\\.") {
			http.Error(w, "valid item_id required", http.StatusBadRequest)
			return
		}
		if ok, err := repo.ItemExists(r.Context(), itemID); err != nil || !ok {
			http.Error(w, "item not found", http.StatusNotFound)
			return
		}
		file, _, err := r.FormFile("image")
		if err != nil {
			http.Error(w, "image file required", http.StatusBadRequest)
			return
		}
		defer file.Close()
		img, _, err := image.Decode(io.LimitReader(file, 10<<20))
		if err != nil {
			http.Error(w, "not a valid PNG/JPEG image", http.StatusBadRequest)
			return
		}
		dir := filepath.Join("web", "public", "assets", "items", itemID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out, err := os.Create(filepath.Join(dir, "thumb.png"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer out.Close()
		if err := png.Encode(out, img); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		renderCatalogTable(w, r)
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
		renderCatalogTable(w, r)
	})
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

func listLookups(ctx context.Context, repo *data.CatalogRepo) ([]lookup, []lookup, []lookup, error) {
	catsRaw, err := repo.ReadLookup(ctx, "categories")
	if err != nil {
		return nil, nil, nil, err
	}
	brandsRaw, err := repo.ReadLookup(ctx, "brands")
	if err != nil {
		return nil, nil, nil, err
	}
	taxCodesRaw, err := repo.ReadLookup(ctx, "tax_codes")
	if err != nil {
		return nil, nil, nil, err
	}
	return convertLookups(catsRaw), convertLookups(brandsRaw), convertLookups(taxCodesRaw), nil
}

func convertLookups(in []data.Lookup) []lookup {
	var out []lookup
	for _, l := range in {
		out = append(out, lookup{ID: l.ID, Name: l.Name})
	}
	return out
}

func validateLookups(ctx context.Context, repo *data.CatalogRepo, in pos.ItemInput) error {
	if in.CategoryID != nil {
		if err := repo.ValidateLookup(ctx, "categories", *in.CategoryID, false); err != nil {
			return err
		}
	}
	if in.BrandID != nil {
		if err := repo.ValidateLookup(ctx, "brands", *in.BrandID, false); err != nil {
			return err
		}
	}
	if in.TaxCodeID != nil {
		if err := repo.ValidateLookup(ctx, "tax_codes", *in.TaxCodeID, false); err != nil {
			return err
		}
	}
	return nil
}
func files(paths ...string) []string { return paths }
