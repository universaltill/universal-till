package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/fiscal"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/money"
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

// OverrideRequest models manager override input. ManagerPIN authorizes the
// override when the signed-in operator is a cashier (docs:
// architecture/pos-auth.md — the PIN's owner becomes the audit actor).
type OverrideRequest struct {
	Reason     string  `json:"reason"`
	ItemID     string  `json:"item_id"`
	VariantID  string  `json:"variant_id"`
	LocationID string  `json:"location_id"`
	QtyBefore  float64 `json:"qty_before"`
	ManagerPIN string  `json:"manager_pin"`
}

// OverrideResponse models manager override response
type OverrideResponse struct {
	OverrideID string `json:"override_id"`
	Success    bool   `json:"success"`
	Message    string `json:"message,omitempty"`
}

// CreateNegativeInventoryOverride handles POST /api/inventory/override
func CreateNegativeInventoryOverride(dp *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		repo := data.NewPOSRepo(dp.Db)

		var req OverrideRequest

		// Handle both JSON and form-encoded data
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"data": nil, "error": "invalid JSON"})
				return
			}
		} else {
			if err := r.ParseForm(); err != nil {
				writeHTML(w, http.StatusBadRequest, "<div class='error'>Invalid form data</div>")
				return
			}
			req.Reason = r.FormValue("reason")
			req.ItemID = r.FormValue("item_id")
			req.VariantID = r.FormValue("variant_id")
			req.LocationID = r.FormValue("location_id")
			req.ManagerPIN = r.FormValue("manager_pin")

			if qtyStr := r.FormValue("qty_before"); qtyStr != "" {
				if qty, err := strconv.ParseFloat(qtyStr, 64); err == nil {
					req.QtyBefore = qty
				}
			}
		}

		// Actor: a manager/admin session authorizes itself; a cashier needs
		// a manager's PIN, and that manager becomes the audit actor.
		// requestedBy is kept separate from actorID so a PIN-approved
		// override still records who was actually blocked and asked for
		// approval (ut-docs#780) — mirrors fiscal_api.go's
		// createTSEOverride, which captures the same distinction as
		// requestedBy/actorID.
		requestedBy := getSessionUserID(r)
		if requestedBy == "" {
			respondOverrideError(w, r, http.StatusUnauthorized, "authentication required")
			return
		}
		actorID := requestedBy
		role, ok, err := repo.LookupUserRole(ctx, actorID)
		if err != nil {
			respondOverrideError(w, r, http.StatusInternalServerError, fmt.Sprintf("auth check failed: %v", err))
			return
		}
		if !ok {
			respondOverrideError(w, r, http.StatusForbidden, "user not found")
			return
		}
		if role != "manager" && role != "admin" {
			if req.ManagerPIN == "" {
				respondOverrideError(w, r, http.StatusForbidden, "manager approval required")
				return
			}
			svc := dp.AuthSvc
			if svc == nil { // tests wiring handlers directly
				svc = auth.NewService(dp.Db)
			}
			// Shares the login lockout, so this endpoint can't be used to
			// brute-force a manager PIN.
			approver, err := svc.AuthorizeManager(ctx, req.ManagerPIN)
			switch {
			case errors.Is(err, auth.ErrLockedOut):
				respondOverrideError(w, r, http.StatusForbidden, "too many attempts")
				return
			case errors.Is(err, auth.ErrInvalidPIN):
				respondOverrideError(w, r, http.StatusForbidden, "manager pin not recognised")
				return
			case err != nil:
				respondOverrideError(w, r, http.StatusInternalServerError, "manager pin check failed")
				return
			}
			actorID = approver.ID
		}

		// Validate input
		if req.Reason == "" {
			respondOverrideError(w, r, http.StatusBadRequest, "reason required")
			return
		}
		if req.LocationID == "" {
			respondOverrideError(w, r, http.StatusBadRequest, "location_id required")
			return
		}
		if req.ItemID == "" && req.VariantID == "" {
			respondOverrideError(w, r, http.StatusBadRequest, "item_id or variant_id required")
			return
		}

		// Record override
		overrideID, err := pos.RecordNegativeInventoryOverride(ctx, dp.Db, pos.OverrideNegativeInventory{
			ActorID:     actorID,
			RequestedBy: requestedBy,
			Reason:      req.Reason,
			ItemID:      req.ItemID,
			VariantID:   req.VariantID,
			LocationID:  req.LocationID,
			QtyBefore:   req.QtyBefore,
		})
		if err != nil {
			respondOverrideError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		respondOverrideSuccess(w, r, OverrideResponse{OverrideID: overrideID, Success: true})
	}
}

// respondOverrideError writes override error response. JSON uses the
// { "data": null, "error": … } envelope (ut-docs#378).
func respondOverrideError(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, status, map[string]any{"data": nil, "error": message})
	} else {
		writeHTML(w, status, errorHTML(message))
	}
}

// respondOverrideSuccess writes override success response. JSON uses the
// { "data": …, "error": null } envelope (ut-docs#378).
func respondOverrideSuccess(w http.ResponseWriter, r *http.Request, data OverrideResponse) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "error": nil})
	} else {
		writeHTMLStockChanged(w, http.StatusOK, fmt.Sprintf("<div class='success'>Override recorded: %s</div>", data.OverrideID))
	}
}

// ReturnRequest models input for processing a return
type ReturnRequest struct {
	OriginalSaleID string              `json:"original_sale_id"`
	ReceiptNo      string              `json:"receipt_no"`
	Lines          []ReturnLineRequest `json:"lines"`
	Reason         string              `json:"reason"`
	// Offline (ut-docs#1493): the till's declared offline state, same
	// signal completeTender (pos_api.go) already threads into
	// SaleInput.Offline — mirrored here for the JSON request path.
	Offline bool `json:"offline,omitempty"`
}

type ReturnLineRequest struct {
	LineID   string  `json:"line_id"`
	Quantity float64 `json:"quantity"`
}

// ReturnResponse models response with created return sale ID
type ReturnResponse struct {
	ReturnSaleID string `json:"return_sale_id"`
	ReceiptNo    string `json:"receipt_no"`
	Success      bool   `json:"success"`
	Message      string `json:"message,omitempty"`
}

// CreateReturn handles POST /api/inventory/return
func CreateReturn(dp *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		repo := data.NewPOSRepo(dp.Db)

		var req ReturnRequest

		// Handle both JSON and form-encoded data
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"data": nil, "error": "invalid JSON"})
				return
			}
		} else {
			if err := r.ParseForm(); err != nil {
				writeHTML(w, http.StatusBadRequest, "<div class='error'>Invalid form data</div>")
				return
			}
			req.OriginalSaleID = r.FormValue("original_sale_id")
			req.ReceiptNo = r.FormValue("receipt_no")
			req.Reason = r.FormValue("reason")
			// Note: Form handling for lines array would need custom parsing
			// ut-docs#1493: same "offline" form field as the refund/sale
			// paths (#offline-flag, formFlagTruthy) — form-encoded submits
			// go through r.Form here since ParseForm already ran above.
			req.Offline = formFlagTruthy(r.Form.Get("offline"))
		}

		// Extract actor from session
		actorID := getSessionUserID(r)
		if actorID == "" {
			respondReturnError(w, r, http.StatusUnauthorized, "authentication required")
			return
		}

		// Lookup original sale by receipt_no if provided instead of ID
		// Lookup original sale by receipt_no if provided instead of ID
		originalSaleID := req.OriginalSaleID
		if originalSaleID == "" && req.ReceiptNo != "" {
			foundID, ok, err := repo.FindSaleIDByReceipt(ctx, req.ReceiptNo)
			if err != nil {
				respondReturnError(w, r, http.StatusInternalServerError, fmt.Sprintf("lookup failed: %v", err))
				return
			}
			if !ok {
				respondReturnError(w, r, http.StatusNotFound, "original sale not found")
				return
			}
			originalSaleID = foundID
		}

		// Validate input
		if originalSaleID == "" {
			respondReturnError(w, r, http.StatusBadRequest, "original_sale_id or receipt_no required")
			return
		}
		if len(req.Lines) == 0 {
			respondReturnError(w, r, http.StatusBadRequest, "at least one line required")
			return
		}

		// Fetch the original sale's own currency/pricing-mode (ut-docs#1494):
		// a return must be signed and persisted in the ORIGINAL sale's
		// currency and inclusive/exclusive mode, not a hardcoded English-
		// market default — same source refund_page.go's sibling flow
		// already reads (saleIsTaxInclusive(detail)/detail.Currency).
		originalDetail, found, err := repo.GetSaleDetailByID(ctx, originalSaleID)
		if err != nil {
			respondReturnError(w, r, http.StatusInternalServerError, fmt.Sprintf("fetch original sale: %v", err))
			return
		}
		if !found {
			respondReturnError(w, r, http.StatusNotFound, "original sale not found")
			return
		}
		inclusive := saleIsTaxInclusive(originalDetail)

		// Fetch original sale lines
		snapshots, err := repo.ListSaleLineSnapshots(ctx, originalSaleID)
		if err != nil {
			respondReturnError(w, r, http.StatusInternalServerError, fmt.Sprintf("fetch lines: %v", err))
			return
		}
		originalLines := make(map[string]pos.SaleLineInput)
		for _, s := range snapshots {
			originalLines[s.ID] = pos.SaleLineInput{
				ItemID:             s.ItemID,
				VariantID:          s.VariantID,
				Name:               s.Name,
				SKU:                s.SKU,
				Barcode:            s.Barcode,
				Qty:                s.Qty,
				UnitPrice:          money.FromMinor(s.UnitPrice),
				TaxRateBasisPoints: s.TaxRateBP,
				LocationID:         defaultLocation(s.LocationID),
				OrderType:          s.OrderType, // ADR-0073 D6: return keeps the original line's mode
			}
		}

		// Build return sale lines
		returnLines := []pos.SaleLineInput{}
		for _, reqLine := range req.Lines {
			origLine, ok := originalLines[reqLine.LineID]
			if !ok {
				respondReturnError(w, r, http.StatusBadRequest, fmt.Sprintf("line_id %s not found in original sale", reqLine.LineID))
				return
			}
			if reqLine.Quantity <= 0 || reqLine.Quantity > origLine.Qty {
				respondReturnError(w, r, http.StatusBadRequest, fmt.Sprintf("invalid return quantity %.2f for line %s (max %.2f)", reqLine.Quantity, reqLine.LineID, origLine.Qty))
				return
			}
			returnLine := origLine
			returnLine.Qty = reqLine.Quantity
			returnLines = append(returnLines, returnLine)
		}

		// Calculate return total (sum of line totals) using the SAME
		// per-line math pos.CompleteSale's own computeSaleTotals uses
		// (pos.AmountForQuantity's half-away-from-zero rounding,
		// pos.ComputeTaxBasisPoints's half-up tax) — not a hand-rolled
		// truncating int64 conversion + integer tax division, which could
		// disagree with CompleteSale by a minor unit on an entirely
		// ordinary price (e.g. 99p @ 20% VAT) and reject with "payments do
		// not cover total". That mismatch predates this file's fiscal.sign.ask
		// dispatch (ut-docs#1405) but is newly load-bearing now that dispatch
		// fires BEFORE CompleteSale: a rejected return would otherwise still
		// have been signed — an irreversible TSE record for a return that
		// never persisted, exactly the harm ADR-0044 D1's ordering exists to
		// prevent. This return carries no SaleDiscount/ServiceCharge/vouchers
		// (returnInput below sets none), so summing per-line
		// pos.ComputeTaxBasisPoints results is algebraically identical to
		// computeSaleTotals's own VATBandsForSale apportionment in this case
		// (apportionment only redistributes when discountTotal > 0).
		// `inclusive` (ut-docs#1494) must match the pricing mode this same
		// return's returnInput.TaxInclusive carries below — this used to be
		// hardcoded `false`, which happened to agree with TaxInclusive's own
		// former hardcoded zero value, but would disagree (and reject a
		// perfectly good return with "payments do not cover total") for any
		// tax-inclusive-priced shop now that TaxInclusive reflects the
		// original sale's real pricing mode.
		var returnTotal money.Money
		for _, line := range returnLines {
			lineBase := pos.AmountForQuantity(line.UnitPrice, line.Qty)
			_, lineGross := pos.ComputeTaxBasisPoints(lineBase, line.TaxRateBasisPoints, inclusive)
			returnTotal = returnTotal.Add(lineGross)
		}

		// For returns, payment represents the refund amount
		if !returnTotal.IsPositive() {
			respondReturnError(w, r, http.StatusBadRequest, "return total must be positive")
			return
		}

		// German TSE hard gate (ADR-0048, ut-docs#731): a return moves real
		// money and is aufzeichnungspflichtig under KassenSichV the same as
		// a sale, so it's blocked the same way completeTender blocks a
		// sale — checked before CompleteSale runs, same as the /refund
		// page's own return flow (refund_page.go). The refusal lands in
		// the inventory page's own #return-result slot, inside an
		// otherwise fully-translated screen, and this gate exists FOR the
		// German market — so it reuses the refund flow's already-shipped
		// copy rather than raw Go error text (ut-docs#316/#893: a
		// sentinel's Error() string is still raw English, and a settings
		// read failure here would put SQL text on the operator's screen).
		gate, err := enforceFiscalGate(ctx, dp)
		if err != nil {
			locale := httpx.ResolveLocale(w, r)
			var fiscalNC *fiscalNeverConfiguredError
			var fiscalTF *fiscalTSEFailingError
			switch {
			case errors.As(err, &fiscalTF):
				log.Printf("inventory return rejected: %v (ADR-0048 fiscal hard gate)", err)
				respondReturnError(w, r, http.StatusConflict, httpx.T(locale, "refund.error.fiscal_tse_failing"))
			case errors.As(err, &fiscalNC):
				log.Printf("inventory return rejected: %v (ADR-0048 fiscal hard gate)", err)
				respondReturnError(w, r, http.StatusConflict, httpx.T(locale, "refund.error.fiscal_never_configured"))
			default:
				// Settings-store read failure inside EvaluateGate: an
				// internal fault, not a fiscal posture. Fails closed all
				// the same, but as the 500 it is.
				log.Printf("inventory return: fiscal gate evaluation failed: %v", err)
				respondReturnError(w, r, http.StatusInternalServerError, httpx.T(locale, "refund.error.server"))
			}
			return
		}

		// Create return sale
		returnInput := pos.SaleInput{
			SaleType:               "return",
			OriginalSaleID:         originalSaleID,
			Lines:                  returnLines,
			Payments:               []pos.PaymentInput{{MethodID: "cash", Amount: returnTotal}},
			ActorID:                actorID,
			Note:                   req.Reason,
			Currency:               originalDetail.Currency,
			TaxInclusive:           inclusive,
			AllowNegativeInventory: true, // Returns add inventory
			// ut-docs#1493: same known-offline short-circuit (ADR-0044 D1)
			// as the refund/sale paths — see refund_page.go's identical
			// comment.
			Offline: req.Offline,
		}

		// fiscal.sign.ask (ADR-0044 Decision 1, ut-docs#999/#1405): a return
		// moves real money and is aufzeichnungspflichtig under KassenSichV
		// exactly like a sale, same reasoning as the ADR-0048 gate just
		// above — so it gets a real signing attempt too, mirroring
		// completeTender's ordering (pos_api.go): after any payment-provider
		// interaction (none on this path — the return always pays via the
		// fixed "cash" method above, so there is no provider webhook to wait
		// on), before CompleteSale persists. Never blocks or refuses the
		// return — any failure lands on the proceed-and-declare surface
		// below. returnInput.SaleType is already "return" (set above), so
		// buildFiscalSignPayload's SaleType field (contract 1.6.0,
		// ut-docs#1203) lets a signer tell this apart from a sale of the
		// same amount.
		signRes := dispatchFiscalSignAsk(ctx, dp, &returnInput)

		returnSaleID, err := pos.CompleteSale(ctx, dp.Db, returnInput)
		if err != nil {
			respondReturnError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		// Same per-completion audit marker completeTender writes for a sale
		// taken during an active TSE-override window (ADR-0048 Decision
		// 3) — the journal must show exactly which money-moving
		// completions (sale AND return alike) were taken unsigned.
		// Best-effort after the fact, same as completeTender's own: the
		// return is already committed, so a failed marker write is
		// logged, never unwinds it.
		if gate.Decision == fiscal.AllowedWithOverride {
			if auditErr := repo.InsertAudit(ctx, nil, actorID, "sale", returnSaleID, "unsigned_override", map[string]any{
				"override_actor":  gate.OverrideActor,
				"override_reason": gate.OverrideReason,
				"override_until":  gate.OverrideUntil.UTC().Format(time.RFC3339),
			}, time.Now().UTC().Format(time.RFC3339), ""); auditErr != nil {
				log.Printf("fiscal gate: unsigned_override audit marker for return %s failed: %v", returnSaleID, auditErr)
			}
		}
		// fiscal.sign.ask proceed-and-declare (ADR-0044/ADR-0041 Decision E,
		// ut-docs#999/#1405): the return is already committed — a failed (or
		// known-offline-skipped) signing dispatch is now DECLARED, never
		// unwound, exactly the way completeTender declares a sale's own
		// signing gap. Never re-attempted for a completed return (ADR-0056,
		// ut-docs#839). Best-effort, log-only on failure, same as the
		// unsigned_override block above.
		if signRes.Outcome.isFailure() || signRes.Outcome == fiscalSignSkippedOffline {
			declareUnsignedFiscalSale(ctx, repo, returnSaleID, actorID, signRes)
		}
		// ut-docs#585 (contract v1.1.0): an approved answer that carried the
		// §6 KassenSichV evidence gets it persisted against the return's own
		// sale row, same as completeTender does for a sale.
		if signRes.Outcome == fiscalSignApproved {
			recordFiscalTSEEvidence(ctx, repo, returnSaleID, actorID, signRes.Evidence)
		}
		// Mirror the restock to inventory connectors (best-effort, non-blocking).
		publishStockAdjustedForSale(ctx, dp, returnInput)
		// A replica's return is a journaled sale like any other (ADR-0011
		// D3) — nudge the push loop the same way a tender does (ut-docs#404,
		// ADR-0036). No-op on a primary/single till.
		if dp.SyncPrimaryURL(ctx) != "" {
			dp.RequestSyncPush()
		}
		// Fetch receipt_no
		var receiptNo string
		receiptNo, ok, err := repo.GetReceiptNo(ctx, returnSaleID)
		if err != nil || !ok {
			receiptNo = ""
		}

		respondReturnSuccess(w, r, ReturnResponse{ReturnSaleID: returnSaleID, ReceiptNo: receiptNo, Success: true})
	}
}

func defaultLocation(loc string) string {
	if strings.TrimSpace(loc) == "" {
		return "loc_main"
	}
	return loc
}

// respondReturnError writes return error response. JSON uses the
// { "data": null, "error": … } envelope (ut-docs#378).
func respondReturnError(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, status, map[string]any{"data": nil, "error": message})
	} else {
		writeHTML(w, status, errorHTML(message))
	}
}

// respondReturnSuccess writes return success response. JSON uses the
// { "data": …, "error": null } envelope (ut-docs#378).
func respondReturnSuccess(w http.ResponseWriter, r *http.Request, data ReturnResponse) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "error": nil})
	} else {
		writeHTMLStockChanged(w, http.StatusOK, fmt.Sprintf("<div class='success'>Return created: %s (Receipt: %s)</div>", data.ReturnSaleID, data.ReceiptNo))
	}
}

// registerInventoryAPI registers inventory API routes
func registerInventoryAPI(mux *http.ServeMux, dp *common.Deps) {
	mux.HandleFunc("POST /api/inventory/receipt", CreateStockReceipt(dp))
	mux.HandleFunc("POST /api/inventory/override", CreateNegativeInventoryOverride(dp))
	mux.HandleFunc("POST /api/inventory/return", CreateReturn(dp))
	mux.HandleFunc("GET /api/inventory/low-stock", GetLowStock(dp))
}

// GetLowStock handles GET /api/inventory/low-stock
func GetLowStock(dp *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		locationID := r.URL.Query().Get("location_id")

		items, err := pos.GetLowStockItems(ctx, dp.Db, locationID)
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

		tableHTML := "<table class='table'><thead><tr><th>Item</th><th>SKU</th><th>Location</th><th>Current</th><th>Reorder Level</th></tr></thead><tbody>"
		for _, item := range items {
			// item.Name/SKU/LocationName come from persisted catalog/location
			// data (set via catalog admin or an import), not an immediate
			// request-echo -- stored-XSS-shaped, so they must be escaped here
			// same as every other interpolated value in this file (errorHTML,
			// ut-docs#1000). ut-docs#1019.
			tableHTML += fmt.Sprintf("<tr><td>%s</td><td>%s</td><td>%s</td><td class='low-stock'>%.2f</td><td>%d</td></tr>",
				html.EscapeString(item.Name), html.EscapeString(item.SKU), html.EscapeString(item.LocationName), item.CurrentQty, item.ReorderLevel)
		}
		tableHTML += "</tbody></table>"
		tableHTML += fmt.Sprintf("<script>document.getElementById('low-stock-badge').textContent = '%d';</script>", len(items))
		writeHTML(w, http.StatusOK, tableHTML)
	}
}
