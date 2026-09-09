package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/imaging"
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

	// requirePrimary gates catalog mutation on this till being the primary
	// (ut-docs#1667, extended by ut-docs#1689): item_modifier_groups/
	// item_modifier_options/items/item_variants/item_barcodes/
	// variant_barcodes are all synced shop-wide (sync_admin_repo.go's
	// adminTables) via a one-way primary-wins pull, so a write accepted on a
	// satellite would silently vanish (a new row deleted, an edit reverted)
	// on the very next admin pull — refuse it up front instead, same pattern
	// as registers_page.go's requirePrimary. Unlike that page's full-page
	// redirect, these handlers return an HTMX fragment, so the refusal is a
	// plain localized error response like this file's own validation
	// branches (e.g. catalog.error.invalid_request) rather than a redirect.
	// The message key is a parameter (not hardcoded) because
	// "manage customization options" only reads correctly for the modifier
	// group/option routes below — the item/variant/barcode routes ut-docs#1689
	// added use their own, more general "manage the catalog" key instead of
	// silently reusing wording that doesn't fit them.
	requirePrimary := func(w http.ResponseWriter, r *http.Request, key string) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			common.LocalizedError(w, r, http.StatusConflict, key)
			return false
		}
		return true
	}

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

	// Every mutation endpoint answers with row-level out-of-band fragments
	// for the ONE item it touched (ut-docs#1363) — see row_oob.go. The
	// request's primary swap is none, so these fragments ARE the UI update.
	// The mutation itself has already committed by the time this runs, so a
	// render/query failure here is logged rather than surfaced as an error
	// status the client would misread as "the save failed".
	// hadThumbColumn is a snapshot the CALLER takes before its own mutation
	// runs (ut-docs#1842 review F1/F2) — writeCatalogRowOOB compares it
	// against the fresh post-mutation answer to decide whether a plain row
	// fragment is still safe, or whether the thumbnail column's presence
	// just flipped and the whole table needs re-rendering instead. Taking
	// it here, after the mutation, would be too late — the "before"
	// answer would already be gone.
	writeRowOOB := func(w http.ResponseWriter, r *http.Request, itemID string, insert bool, hadThumbColumn bool) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		if err := writeCatalogRowOOB(w, r, repo, funcs, itemID, insert, hadThumbColumn); err != nil {
			log.Printf("[catalog] row oob for item %s: %v", itemID, err)
		}
	}
	// snapshotThumbColumn is the "before" half of the above — call it as
	// the FIRST thing a mutation handler does, before touching the DB.
	// Cheap (one indexed EXISTS query); a read failure here degrades to
	// "assume unchanged" (false either way — see the mismatched comment
	// below) rather than blocking the mutation on a diagnostic query.
	snapshotThumbColumn := func(r *http.Request) bool {
		has, err := repo.HasAnyThumbnail(r.Context())
		if err != nil {
			log.Printf("[catalog] snapshot thumb column: %v", err)
		}
		return has
	}

	// renderVariantsPanel answers with the per-item variants/barcodes editor.
	// Panel mutations also change what the items table shows (its variant and
	// barcode summaries), so the affected item's ROW rides along as an HTMX
	// out-of-band fragment when withTable is set (ut-docs#1363 — previously
	// this injected the entire re-rendered table).
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
			writeRowOOB(w, r, itemID, false, snapshotThumbColumn(r))
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
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
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
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
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
			httpx.RenderError(w, r, http.StatusInternalServerError, "catalog.error.server", err)
			return
		}
		cats, brands, err := listLookups(r.Context(), repo)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "catalog.error.server", err)
			return
		}
		taxCodes, err := repo.ListAllTaxCodes(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "catalog.error.server", err)
			return
		}
		funcs["taxCodeName"] = taxCodeNameFunc(taxCodes)
		funcs["categoryName"] = lookupNameFunc(cats)
		funcs["brandName"] = lookupNameFunc(brands)
		barcodes, _ := repo.ItemBarcodes(r.Context())
		variants, _ := repo.ItemVariants(r.Context())
		thumbnails, _ := repo.ItemThumbnails(r.Context())
		// The thumbnail column exists at all only when some listed item
		// actually has one (ut-docs#1842 AC2) — never an empty 40px cell
		// on every row of a catalog nobody has put images into.
		hasThumbnails := false
		for _, itm := range items {
			if thumbnails[itm.ID] != "" {
				hasThumbnails = true
				break
			}
		}
		data := map[string]any{
			"title":         "Catalog",
			"menuItems":     d.MenuSnapshot(),
			"theme":         d.CurrentState().Theme,
			"Rows":          buildCatalogRows(items, barcodes, variants, thumbnails, hasThumbnails),
			"Categories":    cats,
			"Brands":        brands,
			"TaxCodes":      taxCodes,
			"SyncPrimary":   d.SyncPrimaryURL(r.Context()),
			"HasThumbnails": hasThumbnails,
			"EmptyColspan":  emptyRowColspan(hasThumbnails),
			"BuiltinIcons":  catimport.BuiltinIcons(),
			"ItemColors":    catalogtypes.ItemColors(),
		}
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "catalog.html"),
			filepath.Join("web", "ui", "partials", "nav.html"),
			filepath.Join("web", "ui", "partials", "bugreport_panel.html"),
			filepath.Join("web", "ui", "partials", "catalog_table.html"),
			filepath.Join("web", "ui", "partials", "catalog_row.html"),
			filepath.Join("web", "ui", "partials", "catalog_variants.html"),
		), funcs)("base", data)(w, r)
	})

	mux.HandleFunc("/api/catalog/item", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		// Before anything: a new item can get a thumbnail below (the
		// barcode-lookup auto-fill photo), which can flip whether the
		// column exists at all — snapshot now, while "before" still means
		// something (ut-docs#1842 review F1).
		hadThumbColumn := snapshotThumbColumn(r)
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
			} else if err := repo.SetItemThumbnail(r.Context(), itemID, "/public/assets/items/"+itemID+"/thumb.png"); err != nil {
				// Same review-F2 reasoning as the manual upload handler
				// above: the photo is safely on disk regardless, this only
				// keeps item_images (POS grid/basket/self-order/
				// suggestions) in sync with what the admin table shows.
				log.Printf("[catalog] record item_images thumbnail for %s: %v", itemID, err)
			}
		}
		writeRowOOB(w, r, itemID, true, hadThumbColumn)
	})

	// Update item
	mux.HandleFunc("/api/catalog/item/update", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		// This save can also (de)activate the item (the isActive field),
		// which can flip whether the column exists — same reasoning as
		// item creation above (ut-docs#1842 review F1/F2). Snapshot before
		// the write, not after.
		hadThumbColumn := snapshotThumbColumn(r)
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
		// Previous active state decides the OOB mode: an item that was
		// INACTIVE before this update has no row in the table (inactive
		// items are never rendered), so a reactivating save must APPEND its
		// row — an in-place update would target a missing id, which htmx
		// answers with a console error and no visible row. Reachable for
		// real: deactivate a row while the edit form still holds that item,
		// then save. Read+write run atomically (ut-docs#1399) so two
		// genuinely concurrent updates on the same item can't both read the
		// pre-update state and both append a row — see
		// UpdateItemReturningWasActive's doc comment.
		wasActive, err := pos.UpdateItemReturningWasActive(r.Context(), d.Db, itemInput)
		if err != nil {
			skuAwareError(w, r, http.StatusBadRequest, err)
			return
		}
		writeRowOOB(w, r, itemInput.ID, !wasActive, hadThumbColumn)
	})

	// Deactivate item
	mux.HandleFunc("/api/catalog/item/deactivate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		// Deactivating the last active item with a thumbnail collapses the
		// column — snapshot before the write (ut-docs#1842 review F2).
		hadThumbColumn := snapshotThumbColumn(r)
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
		writeRowOOB(w, r, itemID, false, hadThumbColumn)
	})

	// Create or update variant (if id present => update)
	mux.HandleFunc("/api/catalog/variant", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
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
		writeRowOOB(w, r, itemID, false, snapshotThumbColumn(r))
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
		if !requirePrimary(w, r, "catalog.error.replica_use_primary") {
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
		if !requirePrimary(w, r, "catalog.error.replica_use_primary") {
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
			// field's pattern="" attribute (ut-docs#1284 moved it from
			// type="number" step="" to type="text" pattern="").
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
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
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
		// No panel open: the form carries only the variant id, so resolve
		// the parent item whose row summary must drop this variant
		// (soft-deactivated rows still resolve). A lookup failure only
		// costs the row refresh — the deactivation itself already landed.
		if itemID, ok, err := repo.ItemIDForVariant(r.Context(), variantID); err == nil && ok {
			writeRowOOB(w, r, itemID, false, snapshotThumbColumn(r))
		} else if err != nil {
			log.Printf("[catalog] resolve item for variant %s: %v", variantID, err)
		}
	})

	// Item image upload → web/public/assets/items/<id>/thumb.png (the same
	// convention the product tiles and designer use).
	//
	// Deliberately NOT requirePrimary-gated (ut-docs#1689): item_images is
	// explicitly excluded from sync_admin_repo.go's adminTables ("files
	// don't travel — D2 limit"), so a photo uploaded on a replica is a
	// replica-local fact, not a synced row that would silently revert on
	// the next admin pull — unlike items/item_variants/item_barcodes/
	// variant_barcodes below, which are.
	mux.HandleFunc("POST /api/catalog/item/image", func(w http.ResponseWriter, r *http.Request) {
		// The exact flow ut-docs#1842 is about: this can be the FIRST
		// thumbnail the whole catalog ever gets. Snapshot before the
		// write, not after (review F1).
		hadThumbColumn := snapshotThumbColumn(r)
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
		raw, readErr := io.ReadAll(io.LimitReader(file, 10<<20))
		if readErr != nil {
			common.LocalizedError(w, r, http.StatusBadRequest, "catalog.error.image_invalid")
			return
		}
		img, err := imaging.Decode(raw)
		if err != nil {
			if errors.Is(err, imaging.ErrTooManyPixels) {
				common.LocalizedError(w, r, http.StatusBadRequest, "catalog.error.image_too_large")
				return
			}
			common.LocalizedError(w, r, http.StatusBadRequest, "catalog.error.image_invalid")
			return
		}
		img = imaging.DownscaleMaxEdge(img, imaging.MaxThumbEdge)
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
		// Review finding F2 (ut-docs#1189): this handler used to write ONLY
		// the disk file — the admin Catalog table (which checks the file)
		// showed the new photo, but the POS sale-screen grid/basket/self-
		// order/suggestions (which resolve via item_images/ImageURL) never
		// saw it, so a placeholder icon (or nothing) kept showing there
		// forever with no in-app way to clear it. Best-effort: the photo
		// is already saved and correct on disk either way, so a DB hiccup
		// here logs rather than fails the upload.
		if err := repo.SetItemThumbnail(r.Context(), itemID, "/public/assets/items/"+itemID+"/thumb.png"); err != nil {
			log.Printf("[catalog] record item_images thumbnail for %s: %v", itemID, err)
		}
		writeRowOOB(w, r, itemID, false, hadThumbColumn)
	})

	// Built-in icon picker (ut-docs#1844): a bundled category icon
	// (catimport.BuiltinIcons — the same 5 assets PlaceholderIcon already
	// picks from for imageless imports) is a valid alternative to an
	// uploaded photo, stored through the identical item_images/thumbnail
	// row so every existing reader (POS grid, basket, self-order,
	// suggestions) needs no change to pick it up.
	//
	// icon-state tells the picker what to show when it opens for a given
	// item: which built-in key (if any) is currently selected, whether the
	// current thumbnail is instead a custom upload (a built-in choice and a
	// custom photo are mutually exclusive — picking one always replaces
	// the other, same as the existing upload-over-placeholder behaviour),
	// and which key PlaceholderIcon's own keyword match would suggest for
	// this item's actual name/category — so a still-imageless item shows a
	// sensible preselected tile instead of nothing.
	mux.HandleFunc("GET /api/catalog/item/icon-state", func(w http.ResponseWriter, r *http.Request) {
		itemID := strings.TrimSpace(r.URL.Query().Get("item_id"))
		if itemID == "" {
			writeJSON(w, http.StatusBadRequest, nil, "item_id required")
			return
		}
		itm, ok, err := repo.GetItem(r.Context(), itemID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, nil, "item not found")
			return
		}
		var categoryName string
		if itm.CategoryID != nil {
			if cat, err := repo.GetLookup(r.Context(), "categories", *itm.CategoryID); err == nil {
				categoryName = cat.Name
			}
		}
		suggestedKey := catimport.PlaceholderIcon(itm.Name, categoryName)
		selectedKey := ""
		isCustom := false
		if path, hasPath, err := repo.ItemThumbnailPath(r.Context(), itemID); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		} else if hasPath {
			isCustom = true
			for _, ic := range catimport.BuiltinIcons() {
				if ic.Path == path {
					selectedKey = ic.Key
					isCustom = false
					break
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"selected_key":  selectedKey,
			"is_custom":     isCustom,
			"suggested_key": suggestedKey,
		}, "")
	})

	// Choosing (or clearing) a built-in icon. icon="" (or "none") clears
	// back to no thumbnail at all — distinct from picking "generic", which
	// is a real, visible choice. Deliberately NOT requirePrimary-gated,
	// same reasoning as item/image above: this only ever touches
	// item_images, which sync_admin_repo.go's adminTables explicitly
	// excludes (files/icon choices don't travel over the sync bundle).
	mux.HandleFunc("POST /api/catalog/item/icon", func(w http.ResponseWriter, r *http.Request) {
		// Choosing or clearing a built-in icon writes/removes an
		// item_images/thumbnail row exactly like the upload handler below
		// — it can just as easily be the catalog's first-ever thumbnail,
		// or clear its last one, so it needs the same before-the-mutation
		// snapshot for the OOB response to stay consistent with the
		// <thead> (ut-docs#1842 review F1/F2).
		hadThumbColumn := snapshotThumbColumn(r)
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("item_id"))
		// Review finding F1 (ut-docs#1844): this handler now also removes
		// an item's uploaded thumbnail file (see removeUploadedThumbnail
		// below), which makes item_id reach a filesystem path for the
		// first time on this route — so it needs the same path-traversal
		// guard the sibling upload handler already applies to itemID.
		if itemID == "" || strings.ContainsAny(itemID, "/\\.") {
			http.Error(w, "valid item_id required", http.StatusBadRequest)
			return
		}
		if ok, err := repo.ItemExists(r.Context(), itemID); err != nil || !ok {
			http.Error(w, "item not found", http.StatusNotFound)
			return
		}
		icon := strings.TrimSpace(r.Form.Get("icon"))
		if icon == "" || icon == "none" {
			if err := repo.ClearItemThumbnail(r.Context(), itemID); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
				return
			}
			removeUploadedThumbnail(itemID)
			writeRowOOB(w, r, itemID, false, hadThumbColumn)
			return
		}
		path, ok := catimport.IconPath(icon)
		if !ok {
			http.Error(w, "unknown icon", http.StatusBadRequest)
			return
		}
		if err := repo.SetItemThumbnail(r.Context(), itemID, path); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		removeUploadedThumbnail(itemID)
		writeRowOOB(w, r, itemID, false, hadThumbColumn)
	})

	// Variant image upload → assets/items/<itemID>/variants/<variantID>/thumb.png
	// (docs: architecture/variant-images.md). Fallback chain: variant → item →
	// placeholder, resolved by the template's imgv versioned URLs.
	//
	// Deliberately NOT requirePrimary-gated, same reasoning as item/image
	// above: this writes only image files (item_images), never
	// item_variants itself, so there is no synced row for a replica write
	// to lose on the next admin pull.
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
		raw, readErr := io.ReadAll(io.LimitReader(file, 10<<20))
		if readErr != nil {
			common.LocalizedError(w, r, http.StatusBadRequest, "catalog.error.image_invalid")
			return
		}
		img, err := imaging.Decode(raw)
		if err != nil {
			if errors.Is(err, imaging.ErrTooManyPixels) {
				common.LocalizedError(w, r, http.StatusBadRequest, "catalog.error.image_too_large")
				return
			}
			common.LocalizedError(w, r, http.StatusBadRequest, "catalog.error.image_invalid")
			return
		}
		img = imaging.DownscaleMaxEdge(img, imaging.MaxThumbEdge)
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
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		code := strings.TrimSpace(r.Form.Get("barcode"))
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		variantID := strings.TrimSpace(r.Form.Get("variantId"))
		// The row whose summary must refresh afterwards is the item's even
		// when the barcode attaches to a variant — remember it before the
		// variant-wins clearing below.
		rowItemID := itemID
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
		// No panel open (the keypad-mapper path): answer with the affected
		// item's row. A variant-only submission resolves its parent item.
		if rowItemID == "" && variantID != "" {
			if id, ok, err := repo.ItemIDForVariant(r.Context(), variantID); err == nil && ok {
				rowItemID = id
			} else if err != nil {
				log.Printf("[catalog] resolve item for variant %s: %v", variantID, err)
			}
		}
		if rowItemID != "" {
			writeRowOOB(w, r, rowItemID, false, snapshotThumbColumn(r))
		}
	})

	// Detach a barcode (mis-scans and reassignments are routine corrections).
	mux.HandleFunc("POST /api/catalog/barcode/delete", func(w http.ResponseWriter, r *http.Request) {
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		barcode := strings.TrimSpace(r.Form.Get("barcode"))
		if barcode == "" {
			http.Error(w, "barcode required", http.StatusBadRequest)
			return
		}
		// Resolve the owning item BEFORE the delete — afterwards the code
		// resolves to nothing. Best-effort: the delete must not be blocked
		// by this read failing.
		ownerID, ownerOK, err := repo.ItemIDForBarcode(r.Context(), barcode)
		if err != nil {
			log.Printf("[catalog] resolve item for barcode %s: %v", barcode, err)
		}
		if err := pos.RemoveBarcode(r.Context(), d.Db, barcode); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
			return
		}
		if panelItem := strings.TrimSpace(r.Form.Get("panelItem")); panelItem != "" {
			renderVariantsPanel(w, r, panelItem, true)
			return
		}
		if ownerOK {
			writeRowOOB(w, r, ownerID, false, snapshotThumbColumn(r))
		}
	})

	// ut-docs#1356: bulk "backfill barcodes from SKU" — preview (dry run,
	// GET, no writes) + commit (POST). Both share computeBarcodeBackfillPlan
	// below, which reuses ut-docs#1224's exported derivation
	// (catimport.DeriveNumberBarcode, ADR-0059 §3 "call the same function,
	// don't reimplement it") and the dry-run-safe BarcodeOwner lookup —
	// never ensureBarcodeAvailable/AddBarcode's own transactional check,
	// which stays load-bearing for the real write path (see AddBarcode
	// below and BarcodeOwner's own doc comment).
	mux.HandleFunc("GET /api/catalog/barcode-backfill", func(w http.ResponseWriter, r *http.Request) {
		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		T := funcs["T"].(func(string) string)
		settingsRepo := data.NewSettingsRepo(d.Db)
		enabledIDs, symErr := settingsRepo.EnabledBarcodeSymbologies(r.Context())
		if symErr != nil {
			log.Printf("[catalog] enabled symbologies unavailable for barcode backfill, using defaults: %v", symErr)
		}
		assign, noSymbology, alreadyUsed, err := computeBarcodeBackfillPlan(r.Context(), repo, enabledIDs, locale, T)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_barcode_backfill.html"),
		), funcs)("catalog_barcode_backfill", barcodeBackfillPreviewView(assign, noSymbology, alreadyUsed))(w, r)
	})

	mux.HandleFunc("POST /api/catalog/barcode-backfill", func(w http.ResponseWriter, r *http.Request) {
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		settingsRepo := data.NewSettingsRepo(d.Db)
		enabledIDs, symErr := settingsRepo.EnabledBarcodeSymbologies(r.Context())
		if symErr != nil {
			log.Printf("[catalog] enabled symbologies unavailable for barcode backfill, using defaults: %v", symErr)
		}
		// Re-derive eligibility fresh against CURRENT data — never trust the
		// preview (ut-docs#1356): a concurrent edit between preview and
		// commit (another operator adding a barcode, an item going
		// inactive, …) must not misfire.
		T := funcs["T"].(func(string) string)
		assign, noSymbology, alreadyUsed, err := computeBarcodeBackfillPlan(r.Context(), repo, enabledIDs, locale, T)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		var assigned int
		// Pre-seeded with the plan's own two issue buckets (items never
		// attempted at all — not eligible even before this commit ran) so
		// the result names EVERY item that didn't get a barcode and why,
		// not only the rarer AddBarcode-time race case the loop below adds
		// to it.
		skipped := make([]backfillSkip, 0, len(noSymbology)+len(alreadyUsed))
		skipped = append(skipped, noSymbology...)
		skipped = append(skipped, alreadyUsed...)
		for _, row := range assign {
			if aerr := pos.AddBarcode(r.Context(), d.Db, pos.BarcodeInput{
				Barcode: row.Barcode, BarcodeType: row.BarcodeType, ItemID: row.ItemID, IsPrimary: true,
			}); aerr != nil {
				// AddBarcode re-checks availability inside its own IMMEDIATE
				// transaction (ut-docs#304) — a race since the plan above
				// was computed (another request/import claiming the same
				// derived code first) is an ordinary "skipped: already in
				// use" outcome here, not a batch-aborting error.
				skipped = append(skipped, backfillSkip{
					Name: row.Name, SKU: row.SKU,
					Reason: common.FriendlyBarcodeConflict(r.Context(), repo, locale, aerr),
				})
				continue
			}
			assigned++
		}
		// Deliberately NOT HX-Refresh (unlike pairing_api.go/backup_api.go's
		// own bulk endpoints): htmx processes that header before the swap,
		// so it would reload the page and discard this very result fragment
		// before the operator ever saw it — a partial backfill (some SKUs
		// skipped) would then look identical to a full one. The result
		// fragment's own "Close" button (catalog_barcode_backfill.html)
		// reloads instead, once the operator has actually read the report —
		// same close-then-reload shape as plugin_install_modal.html.
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_barcode_backfill.html"),
		), funcs)("catalog_barcode_backfill_result", barcodeBackfillResultView(assigned, skipped))(w, r)
	})
}

// backfillRow is one item computeBarcodeBackfillPlan found eligible for a
// SKU-derived barcode (ut-docs#1356).
type backfillRow struct {
	ItemID      string
	Name        string
	SKU         string
	Barcode     string
	BarcodeType string
}

// backfillSkip is one item computeBarcodeBackfillPlan (or the commit loop)
// could NOT assign a barcode to, with an operator-facing, already-
// translated reason.
type backfillSkip struct {
	Name   string
	SKU    string
	Reason string
}

// backfillPreviewRowCap caps the preview's eligible-list display — same
// "don't render an unbounded table" convention as import_page.go's own
// 200-row cap, sized smaller here since this renders inside a dialog, not a
// full page (ut-docs#1356 brief: "first ~50").
const backfillPreviewRowCap = 50

// computeBarcodeBackfillPlan re-derives, from CURRENT data, which of
// CatalogRepo.ItemsWithoutBarcode's candidates are actually eligible for a
// SKU-derived barcode right now — the shared logic both the preview (dry
// run) and the commit call, so a concurrent edit between them can never
// leave the commit trusting stale eligibility. For each candidate:
//   - DeriveNumberBarcode reports an issue (no enabled symbology matches the
//     SKU's shape) → the "can't derive" bucket.
//   - Otherwise BarcodeOwner finds the derived code already claimed by a
//     different item/variant → the "already in use" bucket, named via
//     FriendlyBarcodeConflict (ut-docs#303) — reused by constructing the
//     exact *data.BarcodeConflictError AddBarcode itself would return,
//     rather than duplicating its item/variant label resolution.
//   - Otherwise → eligible to assign.
func computeBarcodeBackfillPlan(ctx context.Context, repo *data.CatalogRepo, enabledIDs []string, locale string, T func(string) string) (assign []backfillRow, noSymbology, alreadyUsed []backfillSkip, err error) {
	items, ierr := repo.ItemsWithoutBarcode(ctx)
	if ierr != nil {
		return nil, nil, nil, fmt.Errorf("barcode backfill: %w", ierr)
	}
	// Two DIFFERENT SKUs in this same candidate set can derive the SAME
	// code — DeriveNumberBarcode's underlying LookupKey collapses some
	// distinct-looking numbers onto one key (e.g. EAN13 weight/price-prefix
	// variants), and normalizeBarcode strips a trailing ".0". items.sku is
	// UNIQUE, so both rows are legitimate candidates, but only the first can
	// actually get the code — BarcodeOwner only sees data already committed
	// before this plan ran, so without tracking this batch's own claims a
	// later duplicate would show as "eligible" in the preview and then
	// silently fail to assign at commit time. Track them here so the
	// preview's count and the commit's actual result always agree.
	claimedInBatch := make(map[string]string) // code -> name of the item that claimed it first
	for _, it := range items {
		code, codeType, issue, _ := catimport.DeriveNumberBarcode(it.SKU, enabledIDs)
		if issue != "" || code == "" {
			noSymbology = append(noSymbology, backfillSkip{Name: it.Name, SKU: it.SKU, Reason: T("catalog.barcode_backfill.issue.no_symbology")})
			continue
		}
		if claimant, dup := claimedInBatch[code]; dup {
			reason := fmt.Sprintf(T("catalog.error.barcode_conflict"), claimant)
			alreadyUsed = append(alreadyUsed, backfillSkip{Name: it.Name, SKU: it.SKU, Reason: reason})
			continue
		}
		targetType, targetID, found, oerr := repo.BarcodeOwner(ctx, code)
		if oerr != nil {
			return nil, nil, nil, fmt.Errorf("barcode backfill: %w", oerr)
		}
		if found {
			// The candidate item itself can never be this owner:
			// ItemsWithoutBarcode already excludes any item that has a row
			// in item_barcodes, so a found owner here is always a
			// DIFFERENT item or variant.
			reason := common.FriendlyBarcodeConflict(ctx, repo, locale, &data.BarcodeConflictError{TargetType: targetType, TargetID: targetID})
			alreadyUsed = append(alreadyUsed, backfillSkip{Name: it.Name, SKU: it.SKU, Reason: reason})
			continue
		}
		claimedInBatch[code] = it.Name
		assign = append(assign, backfillRow{ItemID: it.ID, Name: it.Name, SKU: it.SKU, Barcode: code, BarcodeType: codeType})
	}
	return assign, noSymbology, alreadyUsed, nil
}

// barcodeBackfillPreviewView shapes computeBarcodeBackfillPlan's result for
// catalog_barcode_backfill.html, capping the eligible list at
// backfillPreviewRowCap.
func barcodeBackfillPreviewView(assign []backfillRow, noSymbology, alreadyUsed []backfillSkip) map[string]any {
	shown := assign
	moreCount := 0
	if len(assign) > backfillPreviewRowCap {
		shown = assign[:backfillPreviewRowCap]
		moreCount = len(assign) - backfillPreviewRowCap
	}
	return map[string]any{
		"Eligible":      shown,
		"EligibleCount": len(assign),
		"MoreCount":     moreCount,
		"NoSymbology":   noSymbology,
		"AlreadyUsed":   alreadyUsed,
		"Empty":         len(assign) == 0 && len(noSymbology) == 0 && len(alreadyUsed) == 0,
	}
}

// barcodeBackfillResultView shapes the commit loop's outcome for
// catalog_barcode_backfill.html's result fragment.
func barcodeBackfillResultView(assigned int, skipped []backfillSkip) map[string]any {
	return map[string]any{
		"Assigned": assigned,
		"Skipped":  skipped,
	}
}

// saveLookupImage downloads an allowlisted product-database image and stores
// it as the item's thumb.png (same convention as the manual upload path).
// Best-effort: the item is fine without it.
func saveLookupImage(ctx context.Context, c *productlookup.Client, itemID, imgURL string) error {
	raw, err := c.FetchImage(ctx, imgURL)
	if err != nil {
		return err
	}
	img, err := imaging.Decode(raw)
	if err != nil {
		return err
	}
	img = imaging.DownscaleMaxEdge(img, imaging.MaxThumbEdge)
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

// removeUploadedThumbnail deletes an item's uploaded thumbnail file, if one
// exists, at the same "public/assets/items/<id>/thumb.png" path the upload
// handler above writes to. This is the built-in icon picker's (ut-docs#1844)
// fix for review finding F1: three surfaces resolve an item's photo by this
// path CONVENTION rather than through item_images — catalog_row.html and
// catalog_variants.html's `imgExists` check, and self_order_shop.go's
// hardcoded ImageURL (that file's own comment already documents this same
// convention-vs-item_images split, ut-docs#1189). Without this, choosing a
// built-in icon (or clearing back to none) updates item_images but leaves
// the old uploaded photo file in place, so those three surfaces keep
// showing the superseded photo while every item_images-driven surface (POS
// grid, basket, search, suggestions) correctly shows the new choice —
// visibly inconsistent. Best-effort: the DB write (the record of what the
// operator actually chose) already succeeded by the time this runs, so a
// stray leftover file on disk is a cosmetic follow-up, not a reason to fail
// the request the operator is waiting on. itemID is validated by the caller
// (no "/", "\" or "." — see the path-traversal guard on the /icon route)
// before it ever reaches this function.
func removeUploadedThumbnail(itemID string) {
	path := filepath.Join(paths.Data("public", "assets", "items", itemID), "thumb.png")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("[catalog] remove superseded upload for %s: %v", itemID, err)
	}
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
		// ut-docs#1901: "" is a real, valid value here (the "no color"
		// swatch tile) — validated against the fixed palette allowlist by
		// validateLookups below, same convention as category/brand/tax.
		Color:     strings.TrimSpace(r.Form.Get("color")),
		IsWeighed: r.Form.Get("isWeighed") == "1" || strings.ToLower(r.Form.Get("isWeighed")) == "on",
		// ut-docs#1850: unchecked (missing from the form) correctly reads
		// as false/tracked — no hidden-fallback trick needed, unlike
		// isActive below (that one defaults CHECKED, this one doesn't).
		StockUntracked: r.Form.Get("stockUntracked") == "1" || strings.ToLower(r.Form.Get("stockUntracked")) == "on",
		// formCheckboxActive, not a bare Form.Get: the item form now pairs
		// its Active checkbox with a hidden isActive=0 fallback
		// (ut-docs#1367), same convention as the variant/modifier-group
		// forms — see that helper's own comment for why Form.Get alone
		// would get this backwards.
		IsActive: formCheckboxActive(r),
	}, nil
}

type lookup struct {
	ID   string
	Name string
}

func listLookups(ctx context.Context, repo *data.CatalogRepo) ([]lookup, []lookup, error) {
	catsRaw, err := repo.ReadLookup(ctx, "categories")
	if err != nil {
		return nil, nil, err
	}
	brandsRaw, err := repo.ReadLookup(ctx, "brands")
	if err != nil {
		return nil, nil, err
	}
	return convertLookups(catsRaw), convertLookups(brandsRaw), nil
}

func convertLookups(in []data.Lookup) []lookup {
	var out []lookup
	for _, l := range in {
		out = append(out, lookup{ID: l.ID, Name: l.Name})
	}
	return out
}

// taxCodeNameFunc returns a "taxCodeName" template func that resolves a
// stored tax_code_id to its display name (ut-docs#1178) instead of letting
// the raw id render — used by both the full /catalog page and the
// catalog_row.html fragment re-rendered after a mutation (ut-docs#1363).
//
// Takes *string, not string: Item.TaxCodeID is a *string (nil when the item
// has no tax code, the common case), and while html/template auto-derefs a
// *non-nil* *string when it flows straight into a func(string) parameter, a
// *nil* one panics the whole render with "dereference of nil pointer" — and
// map `index` rejects a *string key outright, nil or not. Handling the nil
// case here, once, is simpler than requiring every call site to guard it.
//
// Built from taxCodes' full set (active AND inactive, ut-docs#1178 review
// finding F1) so a retired tax code still resolves to its real name instead
// of falling back to "—" — see the matching note on the item-edit <select>
// in catalog.html for why inactive codes can't just be dropped here.
func taxCodeNameFunc(taxCodes []data.TaxCodeView) func(id *string) string {
	names := make(map[string]string, len(taxCodes))
	for _, tc := range taxCodes {
		names[tc.ID] = tc.Name
	}
	return func(id *string) string {
		if id == nil {
			return ""
		}
		return names[*id]
	}
}

// lookupNameFunc returns a template func resolving a stored lookup id
// (category/brand) to its display name (ut-docs#1430) instead of letting
// the raw id render -- same shape and *string-nil handling as
// taxCodeNameFunc above, generalized since categories and brands are both
// plain id/name lookup tables. Built from the already-fetched cats/brands
// list at the /catalog route, so this costs no extra query.
func lookupNameFunc(lookups []lookup) func(id *string) string {
	names := make(map[string]string, len(lookups))
	for _, l := range lookups {
		names[l.ID] = l.Name
	}
	return func(id *string) string {
		if id == nil {
			return ""
		}
		return names[*id]
	}
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
	// ut-docs#1901: color has no lookup table (it's a fixed, curated
	// palette, not an admin-editable list), so this checks it against
	// catalogtypes.ItemColors() directly rather than calling
	// repo.ValidateLookup — same "clean, bounded, hand-written" error
	// convention the caller's own comment already documents for this
	// function's other checks (never raw SQL/driver text), since a raw,
	// unvalidated value here would otherwise reach a CSS custom property.
	if !catalogtypes.ValidItemColor(in.Color) {
		return errors.New("invalid color")
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
