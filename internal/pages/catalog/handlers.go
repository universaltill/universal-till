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
	"github.com/universaltill/universal-till/internal/barcode"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	productlookup "github.com/universaltill/universal-till/internal/lookup"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/pos"
)

// newLookupClient is a test seam: production resolves barcodes against the
// real Open*Facts product databases; tests swap in a client pointed at
// hermetic httptest servers so nothing in this package can ever touch the
// network under test.
var newLookupClient = func() *productlookup.Client { return productlookup.NewClient(nil, nil) }

// Register mounts catalog list/create and barcode attach endpoints.
// func Register(mux *http.ServeMux, db *sql.DB, theme string, menu []map[string]string) {
func Register(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewCatalogRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	lookupClient := newLookupClient()

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
					// Decimal-aware, same as the modifier-option price
					// handling below: a 0-decimal currency (IRR/JPY/…)
					// renders whole units, never a hardcoded /100.
					decimals := httpx.CurrencyByCode(d.CurrentState().Currency).Decimals
					costMajor = strconv.FormatFloat(float64(cost)/math.Pow(10, float64(decimals)), 'f', decimals, 64)
				}
				leadTimeDays, _ := repo.ItemLeadTimeDays(r.Context(), itemID)
				// ADR-0020: shows deactivated groups/options too (unlike the
				// sale-time ListGroupsForItem) so a manager can reactivate one.
				modGroups, _ := data.NewModifierRepo(d.Db).ListAllGroupsForItem(r.Context(), itemID)
				pdata = map[string]any{
					"ItemID":         itemID,
					"ItemName":       label.Name,
					"Variants":       variants,
					"ItemBarcodes":   itemBCs,
					"CostMajor":      costMajor,
					"LeadTimeDays":   leadTimeDays,
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
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
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
			// Upper bound is a sanity ceiling, not a real limit on shop
			// pricing (universaltill/ut-docs#276) — 1,000,000 major units
			// survives conversion to minor units at any known currency's
			// decimal count with no int64 overflow risk.
			if err != nil || f < 0 || f > 1_000_000 {
				http.Error(w, "invalid cost", http.StatusBadRequest)
				return
			}
			// Decimal-aware major→minor conversion (see the identical
			// reasoning on the modifier-option handler below): a hardcoded
			// *100 would store every cost 100x too high for a 0-decimal
			// currency shop and wreck the margin report.
			decimals := httpx.CurrencyByCode(d.CurrentState().Currency).Decimals
			minor = int64(math.Round(f * math.Pow(10, float64(decimals))))
		}
		if err := repo.SetItemCostPrice(r.Context(), itemID, minor); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		renderVariantsPanel(w, r, itemID, false)
	})

	// Lead time (days to receive a reorder) — feeds the inventory page's
	// per-item warn/reorder-suggestion thresholds (universaltill/ut-docs#85).
	// Plain integer, no currency conversion (unlike cost price above).
	mux.HandleFunc("POST /api/catalog/item-lead-time", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("panelItem"))
		raw := strings.TrimSpace(r.Form.Get("leadTimeDays"))
		if itemID == "" {
			http.Error(w, "item required", http.StatusBadRequest)
			return
		}
		var days int
		if raw != "" {
			n, err := strconv.Atoi(raw)
			// Upper bound is a sanity ceiling (universaltill/ut-docs#276):
			// without it, an absurdly large value (e.g. 999999999) makes
			// the inventory/digest "DaysLeft <= leadTimeDays" warning fire
			// permanently — a real, if self-inflicted, footgun.
			if err != nil || n < 0 || n > 365 {
				http.Error(w, "invalid lead time", http.StatusBadRequest)
				return
			}
			days = n
		}
		if err := repo.SetItemLeadTimeDays(r.Context(), itemID, days); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
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
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		cats, brands, taxCodes, err := listLookups(r.Context(), repo)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		barcodes, _ := repo.ItemBarcodes(r.Context())
		variants, _ := repo.ItemVariants(r.Context())
		data := map[string]any{
			"title":       "Catalog",
			"menuItems":   d.MenuSnapshot(),
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
			filepath.Join("web", "ui", "partials", "bugreport_panel.html"),
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
			// parseItemInput/validateLookups return clean, bounded,
			// hand-written validation errors ("name and price required",
			// "invalid categories id", …) — never raw SQL/driver text or
			// an internal ID, so these are out of ut-docs#316's scope
			// (unlike the pos./data.-layer errors below, which can wrap
			// real DB errors and go through the translated+logged path).
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateLookups(r.Context(), repo, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		itemID, err := pos.CreateItem(r.Context(), d.Db, itemInput)
		if err != nil {
			skuAwareError(w, r, http.StatusBadRequest, err)
			return
		}
		// Auto-fill flow: attach the looked-up barcode so the new item is
		// instantly scannable, and save the source image as the tile thumb.
		if code := strings.TrimSpace(r.Form.Get("barcode")); code != "" {
			in := pos.BarcodeInput{Barcode: code, ItemID: itemID, IsPrimary: true}
			// See the matching comment at /api/catalog/barcode (ut-docs#948 F1).
			in.BarcodeType = plainBarcodeTypeFor(r, code)
			if err := pos.AddBarcode(r.Context(), d.Db, in); err != nil {
				locale := httpx.ResolveLocale(w, r)
				if errors.Is(err, data.ErrInvalidEAN13) {
					http.Error(w, httpx.T(locale, "catalog.error.item_created_invalid_ean13"), http.StatusBadRequest)
					return
				}
				// Name the conflicting item/variant for the operator instead
				// of the raw internal ID that used to leak here (ut-docs#303).
				reason := common.FriendlyBarcodeConflict(r.Context(), repo, locale, err)
				http.Error(w, fmt.Sprintf(httpx.T(locale, "catalog.error.item_created_barcode_failed"), reason), http.StatusBadRequest)
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
			// See the matching comment in /api/catalog/item above.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		itemInput.ID = strings.TrimSpace(r.Form.Get("id"))
		if err := validateLookups(r.Context(), repo, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := pos.UpdateItem(r.Context(), d.Db, itemInput); err != nil {
			skuAwareError(w, r, http.StatusBadRequest, err)
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
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
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
				skuAwareError(w, r, http.StatusBadRequest, err)
				return
			}
		} else {
			if err := pos.UpdateVariant(r.Context(), d.Db, vInput); err != nil {
				skuAwareError(w, r, http.StatusBadRequest, err)
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
				common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
				return
			}
		} else {
			if err := modRepo.UpdateGroup(r.Context(), groupID, name, required, minSelect, maxSelect, sortOrder, active); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
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
				common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
				return
			}
		} else {
			if err := modRepo.UpdateOption(r.Context(), optionID, name, priceDeltaMinor, sortOrder, active); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
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
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
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
			common.LocalizedError(w, r, http.StatusBadRequest, "common.error.invalid_upload")
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
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		out, err := os.Create(filepath.Join(dir, "thumb.png"))
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		defer out.Close()
		if err := png.Encode(out, img); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		renderCatalogTable(w, r)
	})

	// Variant image upload → assets/items/<itemID>/variants/<variantID>/thumb.png
	// (docs: architecture/variant-images.md). Fallback chain: variant → item →
	// placeholder, resolved by the template's imgv versioned URLs.
	mux.HandleFunc("POST /api/catalog/variant/image", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			common.LocalizedError(w, r, http.StatusBadRequest, "common.error.invalid_upload")
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
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		out, err := os.Create(filepath.Join(dir, "thumb.png"))
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		defer out.Close()
		if err := png.Encode(out, img); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
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
		code := strings.TrimSpace(r.Form.Get("barcode"))
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		variantID := strings.TrimSpace(r.Form.Get("variantId"))
		// The form carries both (item picked from a row + optional variant). A
		// barcode attaches to exactly one — prefer the variant when chosen.
		if variantID != "" {
			itemID = ""
		}
		isPrimary := r.Form.Get("isPrimary") == "1" || strings.ToLower(r.Form.Get("isPrimary")) == "on"
		if code == "" {
			http.Error(w, "barcode required", http.StatusBadRequest)
			return
		}
		in := pos.BarcodeInput{
			Barcode:   code,
			ItemID:    itemID,
			VariantID: variantID,
			IsPrimary: isPrimary,
		}
		// ut-docs#948 F1: once a shop enables an embedded symbology
		// (EAN13_WEIGHT_PREFIX2X/EAN13_PRICE_PREFIX02, ADR-0059), the
		// untyped-inference path in AddBarcode would classify ANY
		// check-digit-valid EAN-13 in that prefix range as embedded-data
		// first — even a genuine plain retail product that happens to
		// share the prefix. Checking "plain code" here passes an explicit
		// BarcodeType, which takes AddBarcode's existing
		// explicit-type-bypasses-inference path (ADR-0059 §3) instead —
		// but only for a genuine EAN-13 (see plainBarcodeTypeFor / F-2).
		in.BarcodeType = plainBarcodeTypeFor(r, code)
		if err := pos.AddBarcode(r.Context(), d.Db, in); err != nil {
			locale := httpx.ResolveLocale(w, r)
			if errors.Is(err, data.ErrInvalidEAN13) {
				http.Error(w, httpx.T(locale, "catalog.error.invalid_ean13"), http.StatusBadRequest)
				return
			}
			// Same fix as the auto-fill flow above (ut-docs#303): name the
			// conflicting item/variant instead of leaking its raw ID.
			http.Error(w, common.FriendlyBarcodeConflict(r.Context(), repo, locale, err), http.StatusBadRequest)
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
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
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

// plainBarcodeTypeFor returns "EAN13" when the operator checked the "plain
// code" escape hatch (ut-docs#948 F1) AND code is actually a valid EAN-13,
// else "" (leave AddBarcode's untyped inference to run).
//
// The EAN-13 guard is the F-2 review fix: forcing BarcodeType:"EAN13"
// makes AddBarcode assert a valid EAN-13 check digit, so blindly forcing
// it would reject an operator who ticks the box on a perfectly valid EAN-8
// / UPC-A / GTIN-14 / CODE128 / internal-PLU code. That rejection would
// also be gratuitous: the only symbologies the escape hatch exists to
// override — EAN13_WEIGHT_PREFIX2X / EAN13_PRICE_PREFIX02 — both require a
// valid EAN-13 check digit to match at all (ADR-0059 §1), so a non-EAN-13
// code can never be mis-inferred as embedded-data in the first place and
// needs no escaping. Ticking the box on such a code is therefore a no-op:
// untyped inference already picks a plain symbology for it.
func plainBarcodeTypeFor(r *http.Request, code string) string {
	v := r.Form.Get("forcePlainBarcode")
	checked := v == "1" || strings.ToLower(v) == "on"
	if checked && barcode.ValidEAN13Checksum(code) {
		return "EAN13"
	}
	return ""
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

// skuAwareError handles a Create/UpdateItem or Create/UpdateVariant error:
// a duplicate SKU (data.ErrSKUExists) is common enough, and specific
// enough to name, that downgrading it to the generic
// "catalog.error.invalid_request" (ut-docs#316's review) throws away
// actionable feedback — same reasoning ut-docs#303 already applied to
// barcode conflicts via FriendlyBarcodeConflict. Anything else still goes
// through the generic translated+logged path.
func skuAwareError(w http.ResponseWriter, r *http.Request, status int, err error) {
	if errors.Is(err, data.ErrSKUExists) {
		common.LocalizedError(w, r, http.StatusBadRequest, "catalog.error.sku_exists")
		return
	}
	common.LogAndLocalizedError(w, r, status, "catalog.error.invalid_request", "catalog", err)
}
