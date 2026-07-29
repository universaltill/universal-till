package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	productlookup "github.com/universaltill/universal-till/internal/lookup"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/pos"
)

// Register mounts catalog list/create and barcode attach endpoints.
// func Register(mux *http.ServeMux, db *sql.DB, theme string, menu []map[string]string) {
func Register(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewCatalogRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	lookupClient := productlookup.NewClient(nil, nil)

	writeJSON := func(w http.ResponseWriter, status int, data any, errMsg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		var errField any
		if errMsg != "" {
			errField = errMsg
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "error": errField})
	}

	// Barcode → open product databases; pre-fills the new-item form
	// (G15 increment 1). Back-office convenience only, fails soft offline.
	mux.HandleFunc("GET /api/catalog/lookup", func(w http.ResponseWriter, r *http.Request) {
		barcode := strings.TrimSpace(r.URL.Query().Get("barcode"))
		if !productlookup.ValidBarcode(barcode) {
			writeJSON(w, http.StatusBadRequest, nil, "barcode must be 6-14 digits")
			return
		}
		product, err := lookupClient.Lookup(r.Context(), barcode)
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, auth.UserID(r), "catalog", barcode, "barcode_lookup",
			map[string]any{"found": err == nil, "source": product.Source}, now, "")
		switch {
		case errors.Is(err, productlookup.ErrNotFound):
			writeJSON(w, http.StatusNotFound, nil, "barcode not found in product databases")
		case err != nil:
			writeJSON(w, http.StatusBadGateway, nil, "product databases unreachable")
		default:
			writeJSON(w, http.StatusOK, product, "")
		}
	})

	// Every mutation endpoint answers with the refreshed items table.
	renderCatalogTable := func(w http.ResponseWriter, r *http.Request) {
		items, _ := repo.ListItems(r.Context())
		barcodes, _ := repo.ItemBarcodes(r.Context())
		variants, _ := repo.ItemVariants(r.Context())
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_table.html"),
		), funcs)("catalog_table", map[string]any{"Items": items, "Barcodes": barcodes, "Variants": variants})(w, r)
	}

	// renderVariantsPanel answers with the per-item variants/barcodes editor.
	// Panel mutations also change what the items table shows (its variant and
	// barcode summaries), so the refreshed table rides along as an HTMX
	// out-of-band swap when withTable is set.
	renderVariantsPanel := func(w http.ResponseWriter, r *http.Request, itemID string, withTable bool) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		pdata := map[string]any{"ItemID": "", "ItemName": ""}
		if itemID != "" {
			if label, ok, err := repo.GetItemLabel(r.Context(), itemID); err == nil && ok {
				variants, _ := repo.VariantsForItem(r.Context(), itemID)
				itemBCs, _ := repo.BarcodesForItem(r.Context(), itemID)
				cost, _ := repo.ItemCostPrice(r.Context(), itemID)
				costMajor := ""
				if cost > 0 {
					costMajor = fmt.Sprintf("%.2f", float64(cost)/100)
				}
				// ADR-0020: shows deactivated groups/options too (unlike the
				// sale-time ListGroupsForItem) so a manager can reactivate one.
				modGroups, _ := data.NewModifierRepo(d.Db).ListAllGroupsForItem(r.Context(), itemID)
				pdata = map[string]any{
					"ItemID":         itemID,
					"ItemName":       label.Name,
					"Variants":       variants,
					"ItemBarcodes":   itemBCs,
					"CostMajor":      costMajor,
					"ModifierGroups": modGroups,
				}
			}
		}
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_variants.html"),
		), funcs)("catalog_variants", pdata)(w, r)
		if withTable {
			var buf bytes.Buffer
			items, _ := repo.ListItems(r.Context())
			barcodes, _ := repo.ItemBarcodes(r.Context())
			variants, _ := repo.ItemVariants(r.Context())
			bw := newBufResponseWriter(&buf)
			httpx.RenderWith(files(
				filepath.Join("web", "ui", "partials", "catalog_table.html"),
			), funcs)("catalog_table", map[string]any{"Items": items, "Barcodes": barcodes, "Variants": variants})(bw, r)
			// Inject the oob marker so htmx replaces #catalog-table in place.
			html := strings.Replace(buf.String(), `id="catalog-table"`, `id="catalog-table" hx-swap-oob="true"`, 1)
			_, _ = io.WriteString(w, html)
		}
	}

	// Variant options as JSON — the labels form's variant picker.
	mux.HandleFunc("GET /api/catalog/variant-options", func(w http.ResponseWriter, r *http.Request) {
		itemID := strings.TrimSpace(r.URL.Query().Get("item_id"))
		variants, err := repo.VariantsForItem(r.Context(), itemID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		type opt struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		opts := []opt{}
		for _, v := range variants {
			if v.IsActive {
				opts = append(opts, opt{ID: v.ID, Name: v.Name})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": opts, "error": nil})
	})

	// Cost price (what the shop pays) — feeds the margin report. Accepts a
	// decimal in major units; stored as minor units (money boundary rule).
	mux.HandleFunc("POST /api/catalog/item-cost", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("panelItem"))
		raw := strings.TrimSpace(r.Form.Get("cost"))
		if itemID == "" {
			http.Error(w, "item required", http.StatusBadRequest)
			return
		}
		var minor int64
		if raw != "" {
			f, err := strconv.ParseFloat(raw, 64)
			if err != nil || f < 0 {
				http.Error(w, "invalid cost", http.StatusBadRequest)
				return
			}
			minor = int64(math.Round(f * 100))
		}
		if err := repo.SetItemCostPrice(r.Context(), itemID, minor); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		renderVariantsPanel(w, r, itemID, false)
	})

	// The per-item editor: all of one item's variants and barcodes, editable.
	mux.HandleFunc("GET /api/catalog/item-variants", func(w http.ResponseWriter, r *http.Request) {
		renderVariantsPanel(w, r, strings.TrimSpace(r.URL.Query().Get("item_id")), false)
	})

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
		barcodes, _ := repo.ItemBarcodes(r.Context())
		variants, _ := repo.ItemVariants(r.Context())
		data := map[string]any{
			"title":       "Catalog",
			"menuItems":   d.Menu,
			"theme":       d.CurrentState().Theme,
			"Items":       items,
			"Barcodes":    barcodes,
			"Variants":    variants,
			"Categories":  cats,
			"Brands":      brands,
			"TaxCodes":    taxCodes,
			"SyncPrimary": d.SyncPrimaryURL(r.Context()),
		}
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "catalog.html"),
			filepath.Join("web", "ui", "partials", "nav.html"),
			filepath.Join("web", "ui", "partials", "catalog_lookups.html"),
			filepath.Join("web", "ui", "partials", "catalog_table.html"),
			filepath.Join("web", "ui", "partials", "catalog_variants.html"),
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
		itemID, err := pos.CreateItem(r.Context(), d.Db, itemInput)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Auto-fill flow: attach the looked-up barcode so the new item is
		// instantly scannable, and save the source image as the tile thumb.
		if barcode := strings.TrimSpace(r.Form.Get("barcode")); barcode != "" {
			if err := pos.AddBarcode(r.Context(), d.Db, pos.BarcodeInput{
				Barcode: barcode, ItemID: itemID, IsPrimary: true,
			}); err != nil {
				http.Error(w, "item created, but barcode attach failed: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		if imgURL := strings.TrimSpace(r.Form.Get("imageUrl")); imgURL != "" {
			if err := saveLookupImage(r.Context(), lookupClient, itemID, imgURL); err != nil {
				log.Printf("[catalog] lookup image for item %s skipped: %v", itemID, err)
			}
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
		// Checkbox semantics: an unchecked box submits nothing, so the panel
		// pairs it with a hidden isActive=0 — active only when a "1" arrived.
		active := r.Form.Get("isActive") != "0"
		if vals := r.Form["isActive"]; len(vals) > 1 {
			active = false
			for _, v := range vals {
				if v == "1" {
					active = true
				}
			}
		}
		vInput := pos.VariantInput{
			ID:       strings.TrimSpace(r.Form.Get("id")),
			ItemID:   itemID,
			SKU:      strings.TrimSpace(r.Form.Get("sku")),
			Name:     strings.TrimSpace(r.Form.Get("name")),
			Price:    price,
			IsActive: active,
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
		if panelItem := strings.TrimSpace(r.Form.Get("panelItem")); panelItem != "" {
			renderVariantsPanel(w, r, panelItem, true)
			return
		}
		renderCatalogTable(w, r)
	})

	// Create or update a modifier group (ADR-0020) — id present = update,
	// absent = create. Same soft-deactivate convention as items/variants
	// (isActive toggle, no hard delete) so historical sale_line_modifiers
	// snapshots are never orphaned by a group disappearing from under them.
	mux.HandleFunc("/api/catalog/modifier-group", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		name := strings.TrimSpace(r.Form.Get("name"))
		if itemID == "" || name == "" {
			http.Error(w, "itemId and name required", http.StatusBadRequest)
			return
		}
		minSelect, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get("minSelect")))
		maxSelect, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("maxSelect")))
		if err != nil || maxSelect < 1 {
			maxSelect = 1
		}
		sortOrder, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get("sortOrder")))
		required := r.Form.Get("required") == "1"
		if required && minSelect < 1 {
			minSelect = 1 // a required group must ask for at least one pick
		}
		active := formCheckboxActive(r)

		modRepo := data.NewModifierRepo(d.Db)
		groupID := strings.TrimSpace(r.Form.Get("id"))
		if groupID == "" {
			if _, err := modRepo.CreateGroup(r.Context(), uuid.NewString(), itemID, name, required, minSelect, maxSelect, sortOrder); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		} else {
			if err := modRepo.UpdateGroup(r.Context(), groupID, name, required, minSelect, maxSelect, sortOrder, active); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		renderVariantsPanel(w, r, itemID, false)
	})

	// Create or update a modifier option (ADR-0020). majorPrice is entered
	// in the shop's display currency and converted to minor units here —
	// same convention as variant price entry.
	mux.HandleFunc("/api/catalog/modifier-option", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		_ = r.ParseForm()
		groupID := strings.TrimSpace(r.Form.Get("groupId"))
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		name := strings.TrimSpace(r.Form.Get("name"))
		if groupID == "" || itemID == "" || name == "" {
			http.Error(w, "groupId, itemId and name required", http.StatusBadRequest)
			return
		}
		priceDeltaMinor := int64(0)
		if majorStr := strings.TrimSpace(r.Form.Get("priceDeltaMajor")); majorStr != "" {
			major, err := strconv.ParseFloat(majorStr, 64)
			if err != nil || major < 0 {
				http.Error(w, "invalid priceDeltaMajor", http.StatusBadRequest)
				return
			}
			// Decimal-aware: a 0-decimal currency (IRR/IRT/IQD/AFN/JPY, all
			// supported — see httpx.currencies) has no minor-unit
			// subdivision at all, so a hardcoded *100 would inflate every
			// price 100x for those shops. Matches the same
			// currency.Decimals the template already uses for this
			// field's step="" attribute.
			decimals := httpx.CurrencyByCode(d.CurrentState().Currency).Decimals
			priceDeltaMinor = int64(math.Round(major * math.Pow(10, float64(decimals))))
		}
		sortOrder, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get("sortOrder")))
		active := formCheckboxActive(r)

		modRepo := data.NewModifierRepo(d.Db)
		optionID := strings.TrimSpace(r.Form.Get("id"))
		if optionID == "" {
			if _, err := modRepo.CreateOption(r.Context(), uuid.NewString(), groupID, name, priceDeltaMinor, sortOrder); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		} else {
			if err := modRepo.UpdateOption(r.Context(), optionID, name, priceDeltaMinor, sortOrder, active); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
		}
		renderVariantsPanel(w, r, itemID, false)
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
		if panelItem := strings.TrimSpace(r.Form.Get("panelItem")); panelItem != "" {
			renderVariantsPanel(w, r, panelItem, true)
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
		dir := paths.Data("public", "assets", "items", itemID)
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

	// Variant image upload → assets/items/<itemID>/variants/<variantID>/thumb.png
	// (docs: architecture/variant-images.md). Fallback chain: variant → item →
	// placeholder, resolved by the template's imgv versioned URLs.
	mux.HandleFunc("POST /api/catalog/variant/image", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "invalid upload", http.StatusBadRequest)
			return
		}
		variantID := strings.TrimSpace(r.Form.Get("variant_id"))
		if variantID == "" || strings.ContainsAny(variantID, "/\\.") {
			http.Error(w, "valid variant_id required", http.StatusBadRequest)
			return
		}
		vl, ok, err := repo.GetVariantLabel(r.Context(), variantID)
		if err != nil || !ok {
			http.Error(w, "variant not found", http.StatusNotFound)
			return
		}
		itemID := strings.TrimSpace(r.Form.Get("panelItem"))
		if itemID == "" || strings.ContainsAny(itemID, "/\\.") {
			http.Error(w, "valid panelItem required", http.StatusBadRequest)
			return
		}
		_ = vl
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
		dir := paths.Data("public", "assets", "items", itemID, "variants", variantID)
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
		renderVariantsPanel(w, r, itemID, false)
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
		// The form carries both (item picked from a row + optional variant). A
		// barcode attaches to exactly one — prefer the variant when chosen.
		if variantID != "" {
			itemID = ""
		}
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
		if panelItem := strings.TrimSpace(r.Form.Get("panelItem")); panelItem != "" {
			renderVariantsPanel(w, r, panelItem, true)
			return
		}
		renderCatalogTable(w, r)
	})

	// Detach a barcode (mis-scans and reassignments are routine corrections).
	mux.HandleFunc("POST /api/catalog/barcode/delete", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		barcode := strings.TrimSpace(r.Form.Get("barcode"))
		if barcode == "" {
			http.Error(w, "barcode required", http.StatusBadRequest)
			return
		}
		if err := pos.RemoveBarcode(r.Context(), d.Db, barcode); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if panelItem := strings.TrimSpace(r.Form.Get("panelItem")); panelItem != "" {
			renderVariantsPanel(w, r, panelItem, true)
			return
		}
		renderCatalogTable(w, r)
	})
}

// saveLookupImage downloads an allowlisted product-database image and stores
// it as the item's thumb.png (same convention as the manual upload path).
// Best-effort: the item is fine without it.
func saveLookupImage(ctx context.Context, c *productlookup.Client, itemID, imgURL string) error {
	raw, err := c.FetchImage(ctx, imgURL)
	if err != nil {
		return err
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	dir := paths.Data("public", "assets", "items", itemID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	out, err := os.Create(filepath.Join(dir, "thumb.png"))
	if err != nil {
		return err
	}
	defer out.Close()
	return png.Encode(out, img)
}

// bufResponseWriter captures a partial render into a buffer so it can be
// post-processed (the out-of-band table fragment).
type bufResponseWriter struct {
	buf    *bytes.Buffer
	header http.Header
}

func newBufResponseWriter(buf *bytes.Buffer) *bufResponseWriter {
	return &bufResponseWriter{buf: buf, header: http.Header{}}
}

func (b *bufResponseWriter) Header() http.Header         { return b.header }
func (b *bufResponseWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufResponseWriter) WriteHeader(int)             {}

func strPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// formCheckboxActive reads a checkbox paired with a hidden isActive=0
// fallback (an unchecked box submits nothing on its own). Checked ⇒ the
// browser sends BOTH the hidden "0" and the checkbox's "1", in DOM order —
// hidden-then-checkbox means Form.Get alone would always see "0" first and
// read as inactive even when checked, so this scans every submitted value
// for a "1" rather than trusting the first one.
func formCheckboxActive(r *http.Request) bool {
	vals := r.Form["isActive"]
	if len(vals) == 0 {
		return true // no isActive field at all: caller didn't use the hidden-fallback pattern, default active
	}
	for _, v := range vals {
		if v == "1" {
			return true
		}
	}
	return false
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
