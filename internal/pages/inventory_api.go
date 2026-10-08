package pages

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/fleetlink"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
	"github.com/universaltill/universal-till/internal/pos"
)

// StockReceiptRequest models input for stock receipt/adjustment
type StockReceiptRequest struct {
	ItemID     string  `json:"item_id"`
	VariantID  string  `json:"variant_id"`
	LocationID string  `json:"location_id"`
	Type       string  `json:"type"`       // receive|adjust
	Quantity   float64 `json:"quantity"`   // positive for receive, +/- for adjust
	CostPrice  int64   `json:"cost_price"` // optional, minor units
	Reason     string  `json:"reason"`
}

// StockReceiptResponse models response with created movement ID
type StockReceiptResponse struct {
	MovementID string `json:"movement_id"`
	Success    bool   `json:"success"`
	Message    string `json:"message,omitempty"`
}

// CreateStockReceipt handles POST /api/inventory/receipt
func CreateStockReceipt(dp *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ut-docs#3079: goods-in had no permission check at all.
		if !canPerform(dp, r, "stock_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		ctx := r.Context()

		var req StockReceiptRequest

		// Handle both JSON and form-encoded data
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"data": nil, "error": "invalid JSON"})
				return
			}
		} else {
			// Parse form data
			if err := r.ParseForm(); err != nil {
				writeHTML(w, http.StatusBadRequest, "<div class='error'>Invalid form data</div>")
				return
			}
			req.Type = r.FormValue("type")
			req.ItemID = r.FormValue("item_id")
			req.VariantID = r.FormValue("variant_id")
			req.LocationID = r.FormValue("location_id")
			req.Reason = r.FormValue("reason")

			if qtyStr := r.FormValue("quantity"); qtyStr != "" {
				if qty, err := strconv.ParseFloat(qtyStr, 64); err == nil {
					req.Quantity = qty
				}
			}
			if cpStr := r.FormValue("cost_price"); cpStr != "" {
				if cp, err := strconv.ParseInt(cpStr, 10, 64); err == nil {
					req.CostPrice = cp
				}
			}
		}

		// Extract actor from session
		actorID := getSessionUserID(r)
		if actorID == "" {
			respondError(w, r, http.StatusUnauthorized, "authentication required")
			return
		}

		// Validate input
		if req.Type != "receive" && req.Type != "adjust" {
			respondError(w, r, http.StatusBadRequest, "type must be 'receive' or 'adjust'")
			return
		}
		if req.LocationID == "" {
			respondError(w, r, http.StatusBadRequest, "location_id required")
			return
		}
		if req.ItemID == "" && req.VariantID == "" {
			respondError(w, r, http.StatusBadRequest, "item_id or variant_id required")
			return
		}
		if req.Quantity == 0 {
			respondError(w, r, http.StatusBadRequest, "quantity must be non-zero")
			return
		}

		// Record stock movement
		movementID, err := pos.RecordStockMovement(ctx, dp.Db, pos.StockMovementInput{
			ItemID:     req.ItemID,
			VariantID:  req.VariantID,
			LocationID: req.LocationID,
			Type:       req.Type,
			Quantity:   req.Quantity,
			CostPrice:  req.CostPrice,
			Reason:     req.Reason,
			ActorID:    actorID,
		})
		if err != nil {
			respondError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		// Mirror the manual adjustment/receipt to inventory connectors
		// (best-effort, non-blocking).
		publishStockAdjusted(ctx, dp, plugins.StockAdjustedEvent{
			ItemID:    req.ItemID,
			VariantID: req.VariantID,
			DeltaQty:  req.Quantity,
			Reason:    stockMovementReason(req.Type),
			Location:  req.LocationID,
		})

		// A receipt or adjustment moves the levels linked tills pull
		// (ADR-0114 §2); nil-safe, a no-op with no link.
		dp.NudgeLink(fleetlink.ScopeStock)
		respondSuccess(w, r, StockReceiptResponse{MovementID: movementID, Success: true})
	}
}

// getSessionUserID returns the logged-in operator's id ('system' only when
// auth is disabled via UT_AUTH=off).
func getSessionUserID(r *http.Request) string {
	return auth.UserID(r)
}

// writeJSON writes JSON response
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// writeHTML writes HTML response
func writeHTML(w http.ResponseWriter, status int, html string) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(status)
	fmt.Fprint(w, html)
}

// writeHTMLStockChanged is writeHTML plus HX-Trigger: stock-updated — htmx
// fires that as a DOM event on <body> once the response lands, and the
// /inventory page's stock-levels table listens for it (hx-trigger="load,
// stock-updated from:body") to refetch itself. Without this, a successful
// receive/adjust/override/return updated the database correctly but the
// on-screen quantity table just sat there showing the old number until a
// full page reload — confirmed live 2026-07-29 as "inventory count is not
// updating" (it was; nothing told the table to look again).
func writeHTMLStockChanged(w http.ResponseWriter, status int, html string) {
	w.Header().Set("HX-Trigger", "stock-updated")
	writeHTML(w, status, html)
}

// errorHTML renders an error message into the `<div class='error'>…</div>`
// fragment the respond*Error helpers' HTML branch writes, HTML-escaping the
// message so caller-controlled input echoed back in a validation error
// (e.g. an unrecognized line_id) can't inject markup into an authenticated
// operator's browser via htmx's error-response DOM swap (ut-docs#1000).
func errorHTML(message string) string {
	return fmt.Sprintf("<div class='error'>%s</div>", html.EscapeString(message))
}

// respondError writes error response (JSON or HTML based on request). JSON
// uses the { "data": null, "error": … } envelope universal-till/CLAUDE.md
// mandates for every JSON API response (ut-docs#378).
func respondError(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, status, map[string]any{"data": nil, "error": message})
	} else {
		writeHTML(w, status, errorHTML(message))
	}
}

// respondSuccess writes success response (JSON or HTML based on request).
// JSON uses the { "data": …, "error": null } envelope (ut-docs#378).
func respondSuccess(w http.ResponseWriter, r *http.Request, data StockReceiptResponse) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "error": nil})
	} else {
		writeHTMLStockChanged(w, http.StatusOK, fmt.Sprintf("<div class='success'>Stock movement created: %s</div>", data.MovementID))
	}
}

// registerInventoryAPI registers inventory API routes
func registerInventoryAPI(mux *http.ServeMux, dp *common.Deps) {
	mux.HandleFunc("POST /api/inventory/receipt", CreateStockReceipt(dp))
	mux.HandleFunc("GET /api/inventory/low-stock", GetLowStock(dp))
}

// GetLowStock handles GET /api/inventory/low-stock
func GetLowStock(dp *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// ut-docs#3079 review: the same stock levels /ui/inventory/stock-table
		// refuses a cashier.
		if !canPerform(dp, r, "stock_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		ctx := r.Context()
		locationID := r.URL.Query().Get("location_id")

		// ut-docs#27: a shop that sells without tracking stock has nothing
		// to reorder at a location that simply doesn't stock an item.
		items, err := pos.LowStockItemsFor(ctx, dp.Db, locationID, dp.CurrentState().AllowNegativeInventory)
		if err != nil {
			if strings.Contains(r.Header.Get("Accept"), "application/json") {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"data": nil, "error": err.Error()})
			} else {
				writeHTML(w, http.StatusInternalServerError, errorHTML(err.Error()))
			}
			return
		}

		// Check if JSON response requested. Wrapped as { "data": …, "error":
		// null } -- the envelope universal-till/CLAUDE.md mandates for every
		// JSON API response, matching every other JSON handler in this
		// package (ut-docs#323: this endpoint used to respond bare).
		if strings.Contains(r.Header.Get("Accept"), "application/json") {
			writeJSON(w, http.StatusOK, map[string]any{
				"data":  map[string]any{"items": items, "count": len(items)},
				"error": nil,
			})
			return
		}

		// Return HTML for HTMX
		if len(items) == 0 {
			writeHTML(w, http.StatusOK, "<p>No low stock items</p>")
			return
		}

		notStockedHere := html.EscapeString(httpx.T(httpx.ResolveLocale(w, r), "inventory.not_stocked_here"))
		tableHTML := "<table class='table'><thead><tr><th>Item</th><th>SKU</th><th>Location</th><th>Current</th><th>Reorder Level</th></tr></thead><tbody>"
		for _, item := range items {
			// item.Name/SKU/LocationName come from persisted catalog/location
			// data (set via catalog admin or an import), not an immediate
			// request-echo -- stored-XSS-shaped, so they must be escaped here
			// same as every other interpolated value in this file (errorHTML,
			// ut-docs#1000). ut-docs#1019.
			//
			// ut-docs#2011: class='stock-row' + the data-* attributes mirror
			// the main stock table's own row markup (web/ui/pages/inventory.html,
			// web/ui/partials/stock_table.html) so the SAME page-local click
			// listener that opens the receive/adjust dialog covers a low-stock
			// row too, with no separate JS. ItemID/LocationID are internal
			// identifiers, not user text, but escaped anyway for the same
			// defense-in-depth reason as Name/SKU/LocationName above.
			// ut-docs#2082: a variant-scoped row carries the parent item's own
			// Name (so two rows of the same item are otherwise
			// indistinguishable in this list) — suffix the visible cell with
			// the variant's own name, same convention as the main stock table.
			displayName := item.Name
			if item.VariantName != "" {
				displayName = item.Name + " — " + item.VariantName
			}
			// ut-docs#27: a row for an item stocked only at other locations.
			locationCell := html.EscapeString(item.LocationName)
			if item.NotStockedHere {
				locationCell += " <span class='muted'>(" + notStockedHere + ")</span>"
			}
			tableHTML += fmt.Sprintf("<tr class='stock-row' data-item='%s' data-name='%s' data-sku='%s' data-location='%s' data-location-name='%s' data-variant='%s'><td>%s</td><td>%s</td><td>%s</td><td class='low-stock'>%.2f</td><td>%d</td></tr>",
				html.EscapeString(item.ItemID), html.EscapeString(displayName), html.EscapeString(item.SKU), html.EscapeString(item.LocationID), html.EscapeString(item.LocationName), html.EscapeString(item.VariantID),
				html.EscapeString(displayName), html.EscapeString(item.SKU), locationCell, item.CurrentQty, item.ReorderLevel)
		}
		tableHTML += "</tbody></table>"
		tableHTML += fmt.Sprintf("<script>document.getElementById('low-stock-badge').textContent = '%d';</script>", len(items))
		writeHTML(w, http.StatusOK, tableHTML)
	}
}
