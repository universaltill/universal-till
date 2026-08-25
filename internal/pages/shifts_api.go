package pages

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ShiftOpenRequest models input for opening a shift. OpeningCash is a
// pointer so "explicitly provided" (any value, including 0) is
// distinguishable from "omitted": omitted defaults to the float carried
// forward from the register's last close (ut-docs#1006), while an explicit
// value — an operator correcting the drawer — is always respected.
type ShiftOpenRequest struct {
	RegisterID  string `json:"register_id"`
	CashierID   string `json:"cashier_id"`
	OpeningCash *int64 `json:"opening_cash"` // minor units; nil = carry forward
}

// ShiftOpenResponse models response with created shift ID
type ShiftOpenResponse struct {
	ShiftID string `json:"shift_id"`
	Success bool   `json:"success"`
	Message string `json:"message,omitempty"`
}

// OpenShift handles POST /api/shifts/open
func OpenShift(dp *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var req ShiftOpenRequest

		// Handle both JSON and form-encoded data
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				respondShiftError(w, r, http.StatusBadRequest, "invalid JSON")
				return
			}
		} else {
			if err := r.ParseForm(); err != nil {
				respondShiftError(w, r, http.StatusBadRequest, "invalid form data")
				return
			}
			req.RegisterID = r.FormValue("register_id")
			req.CashierID = r.FormValue("cashier_id")
			// Only an actually-submitted value sets the pointer — an absent
			// or empty field stays nil so the carry-forward default below
			// applies, mirroring the JSON path's omitted-field behavior.
			if ocStr := r.FormValue("opening_cash"); ocStr != "" {
				if oc, err := strconv.ParseInt(ocStr, 10, 64); err == nil {
					req.OpeningCash = &oc
				}
			}
		}

		// Default cashier to session user if not provided
		if req.CashierID == "" {
			req.CashierID = getSessionUserID(r)
		}

		// Validate input
		if req.RegisterID == "" {
			respondShiftError(w, r, http.StatusBadRequest, "register_id required")
			return
		}
		if req.CashierID == "" {
			respondShiftError(w, r, http.StatusBadRequest, "cashier_id required")
			return
		}
		// Resolve the opening float: an omitted opening_cash carries the
		// last close's new float forward (ut-docs#1006) — or 0 when the
		// register has never closed a shift; a provided value (even 0) is
		// the operator's explicit count and wins unchanged.
		var openingCash int64
		if req.OpeningCash != nil {
			openingCash = *req.OpeningCash
		} else {
			carried, ok, err := pos.LastClosedShiftNewFloat(ctx, dp.Db, req.RegisterID)
			if err != nil {
				respondShiftError(w, r, http.StatusInternalServerError, err.Error())
				return
			}
			if ok {
				openingCash = carried.Minor()
			}
		}
		if openingCash < 0 {
			respondShiftError(w, r, http.StatusBadRequest, "opening_cash must be >= 0")
			return
		}

		// Open shift
		shiftID, err := pos.OpenShift(ctx, dp.Db, pos.ShiftInput{
			RegisterID:  req.RegisterID,
			CashierID:   req.CashierID,
			OpeningCash: money.FromMinor(openingCash),
		})
		if err != nil {
			respondShiftError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		respondShiftSuccess(w, r, ShiftOpenResponse{ShiftID: shiftID, Success: true})
	}
}

// ShiftCloseRequest models input for closing a shift. Skim, SkimReason and
// CountProtocol are the optional close-time cash reconciliation extras
// (ut-docs#1006) — see pos.ShiftCloseInput for their semantics.
type ShiftCloseRequest struct {
	ShiftID       string `json:"shift_id"`
	ClosingCash   int64  `json:"closing_cash"` // minor units
	Note          string `json:"note"`
	Skim          int64  `json:"skim"` // minor units, >= 0
	SkimReason    string `json:"skim_reason"`
	CountProtocol string `json:"count_protocol"` // raw JSON denomination count
	// ManagerPIN authorizes a skim (ut-docs#1006 review finding 1) — a skim
	// moves real cash out of the drawer, the same class of action
	// RecordCashAdjustment's sign-based gate requires a manager PIN for
	// (ut-docs#266); only required/checked when Skim > 0, same as that
	// handler's own gate only fires on a negative amount.
	ManagerPIN string `json:"manager_pin"`
}

// ShiftCloseResponse models response for shift close
type ShiftCloseResponse struct {
	Success      bool   `json:"success"`
	Message      string `json:"message,omitempty"`
	ExpectedCash int64  `json:"expected_cash,omitempty"`
	ClosingCash  int64  `json:"closing_cash,omitempty"`
	Variance     int64  `json:"variance,omitempty"`
	Skim         int64  `json:"skim,omitempty"`
	NewFloat     int64  `json:"new_float,omitempty"`
}

// CloseShift handles POST /api/shifts/close
func CloseShift(dp *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		repo := data.NewPOSRepo(dp.Db)

		var req ShiftCloseRequest

		// Handle both JSON and form-encoded data
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				respondCloseError(w, r, http.StatusBadRequest, "invalid JSON")
				return
			}
		} else {
			if err := r.ParseForm(); err != nil {
				respondCloseError(w, r, http.StatusBadRequest, "invalid form data")
				return
			}
			req.ShiftID = r.FormValue("shift_id")
			req.Note = r.FormValue("note")
			req.SkimReason = r.FormValue("skim_reason")
			req.CountProtocol = r.FormValue("count_protocol")
			req.ManagerPIN = r.FormValue("manager_pin")
			if ccStr := r.FormValue("closing_cash"); ccStr != "" {
				if cc, err := strconv.ParseInt(ccStr, 10, 64); err == nil {
					req.ClosingCash = cc
				}
			}
			if skStr := r.FormValue("skim"); skStr != "" {
				if sk, err := strconv.ParseInt(skStr, 10, 64); err == nil {
					req.Skim = sk
				}
			}
		}

		// Validate input
		if req.ShiftID == "" {
			respondCloseError(w, r, http.StatusBadRequest, "shift_id required")
			return
		}
		if req.ClosingCash < 0 {
			respondCloseError(w, r, http.StatusBadRequest, "closing_cash must be >= 0")
			return
		}
		if req.Skim < 0 {
			respondCloseError(w, r, http.StatusBadRequest, "skim must be >= 0")
			return
		}
		// ut-docs#1006 review finding 6: these are user-input errors, not
		// internal ones — checked here (400) rather than left to surface
		// as pos.CloseShift's generic error (which the handler mapped to
		// 500 across the board).
		if req.Skim > req.ClosingCash {
			respondCloseError(w, r, http.StatusBadRequest, "skim cannot exceed the counted closing cash")
			return
		}
		if req.CountProtocol != "" && !pos.ValidCountProtocol(req.CountProtocol) {
			respondCloseError(w, r, http.StatusBadRequest, "count_protocol must be a flat JSON object of denomination:count")
			return
		}

		// Get actor from session (the skim audit row's actor when no PIN
		// gate applies — mirrors RecordCashAdjustment's actorID pattern)
		actorID := getSessionUserID(r)

		// Manager approval whenever a skim actually moves cash out of the
		// drawer (ut-docs#1006 review finding 1) — a skim is exactly the
		// class of action RecordCashAdjustment's sign-based gate exists for
		// (ut-docs#266), and closing a shift must not become a way to move
		// cash out without that same authorization just because the skim
		// audit row is written here rather than through
		// RecordCashAdjustment (which requires the shift to still be open).
		// A plain close (no skim) stays ungated, same as a positive
		// adjustment does today.
		skimApproverID := ""
		if req.Skim > 0 {
			authOff := auth.Disabled(os.Getenv("UT_AUTH"))
			if !authOff {
				if strings.TrimSpace(req.ManagerPIN) == "" {
					respondCloseError(w, r, http.StatusForbidden, "manager PIN required")
					return
				}
				approver, err := dp.AuthSvc.AuthorizeManager(ctx, strings.TrimSpace(req.ManagerPIN))
				if err != nil {
					status := http.StatusForbidden
					if errors.Is(err, auth.ErrLockedOut) {
						status = http.StatusTooManyRequests
					}
					respondCloseError(w, r, status, "manager PIN required")
					return
				}
				skimApproverID = approver.ID
			} else {
				// Auth disabled (test/dev mode, UT_AUTH) — same convention
				// RecordCashAdjustment follows: the gate itself is skipped,
				// but CloseShift still requires a non-empty
				// SkimApproverID, so fall back to the session actor.
				skimApproverID = actorID
			}
		}

		// Get expected cash before closing
		openingCash, ok, err := repo.GetShiftOpeningCash(ctx, req.ShiftID)
		if err != nil {
			respondCloseError(w, r, http.StatusInternalServerError, fmt.Sprintf("query shift: %v", err))
			return
		}
		if !ok {
			respondCloseError(w, r, http.StatusNotFound, "shift not found or already closed")
			return
		}

		expectedCash, err := pos.ComputeExpectedCash(ctx, dp.Db, req.ShiftID, openingCash)
		if err != nil {
			respondCloseError(w, r, http.StatusInternalServerError, fmt.Sprintf("compute expected cash: %v", err))
			return
		}

		// Close shift
		err = pos.CloseShift(ctx, dp.Db, pos.ShiftCloseInput{
			ShiftID:        req.ShiftID,
			ClosingCash:    money.FromMinor(req.ClosingCash),
			Note:           req.Note,
			Skim:           money.FromMinor(req.Skim),
			SkimReason:     req.SkimReason,
			CountProtocol:  req.CountProtocol,
			SkimApproverID: skimApproverID,
		})
		if err != nil {
			respondCloseError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		variance := req.ClosingCash - expectedCash
		respondCloseSuccess(w, r, ShiftCloseResponse{
			Success:      true,
			ExpectedCash: expectedCash,
			ClosingCash:  req.ClosingCash,
			Variance:     variance,
			Skim:         req.Skim,
			NewFloat:     req.ClosingCash - req.Skim,
		})
	}
}

// CashAdjustmentRequest models input for cash adjustments/payouts
type CashAdjustmentRequest struct {
	ShiftID    string `json:"shift_id"`
	Type       string `json:"type"`   // payout|adjustment
	Amount     int64  `json:"amount"` // minor units, negative for payout
	Reason     string `json:"reason"`
	ManagerPIN string `json:"manager_pin"`
}

// CashAdjustmentResponse models response for cash adjustment
type CashAdjustmentResponse struct {
	AdjustmentID string `json:"adjustment_id"`
	Success      bool   `json:"success"`
	Message      string `json:"message,omitempty"`
}

// RecordCashAdjustment handles POST /api/shifts/adjustment
func RecordCashAdjustment(dp *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var req CashAdjustmentRequest

		// Handle both JSON and form-encoded data
		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				respondAdjustmentError(w, r, http.StatusBadRequest, "invalid JSON")
				return
			}
		} else {
			if err := r.ParseForm(); err != nil {
				respondAdjustmentError(w, r, http.StatusBadRequest, "invalid form data")
				return
			}
			req.ShiftID = r.FormValue("shift_id")
			req.Type = r.FormValue("type")
			req.Reason = r.FormValue("reason")
			req.ManagerPIN = r.FormValue("manager_pin")
			if amtStr := r.FormValue("amount"); amtStr != "" {
				if amt, err := strconv.ParseInt(amtStr, 10, 64); err == nil {
					req.Amount = amt
				}
			}
		}

		// Get actor from session
		actorID := getSessionUserID(r)
		if actorID == "" {
			respondAdjustmentError(w, r, http.StatusUnauthorized, "authentication required")
			return
		}

		// Validate input
		if req.ShiftID == "" {
			respondAdjustmentError(w, r, http.StatusBadRequest, "shift_id required")
			return
		}
		if req.Type != "payout" && req.Type != "adjustment" && req.Type != "skim" {
			respondAdjustmentError(w, r, http.StatusBadRequest, "type must be 'payout', 'adjustment' or 'skim'")
			return
		}
		if req.Amount == 0 {
			respondAdjustmentError(w, r, http.StatusBadRequest, "amount must be non-zero")
			return
		}
		if req.Reason == "" {
			respondAdjustmentError(w, r, http.StatusBadRequest, "reason required")
			return
		}
		// type=payout is, by definition, cash LEAVING the till — a positive
		// amount there would record a payout that actually adds cash, an
		// audit row lying about its own direction (the same class of gap
		// this change closes for the type/sign mismatch below). Doesn't
		// affect expected-cash math (that's sign-only, see
		// pos.RecordCashAdjustment/SumShiftAdjustments) but the audit trail
		// itself must stay honest about what happened.
		if req.Type == "payout" && req.Amount > 0 {
			respondAdjustmentError(w, r, http.StatusBadRequest, "a payout amount must be negative")
			return
		}
		// Same direction-honesty rule for a skim (ut-docs#1006): skimming is,
		// by definition, cash leaving the drawer for the safe — a positive
		// "skim" would be an audit row lying about its own direction.
		if req.Type == "skim" && req.Amount > 0 {
			respondAdjustmentError(w, r, http.StatusBadRequest, "a skim amount must be negative")
			return
		}

		// Manager approval whenever cash actually LEAVES the till (a
		// negative amount) — gated on the sign, not the declared "type",
		// because "type" is a client-supplied label with no sign
		// enforcement: a cashier could otherwise pick "adjustment" instead
		// of "payout" for the same negative amount and bypass a
		// type-only gate entirely (ut-docs#266). Positive adjustments
		// (cash going in, e.g. a float top-up correction) are unaffected,
		// same as the existing refund/PfandRueckgabe gates only ever
		// covering cash leaving the till. The PIN owner becomes the audit
		// actor, mirroring PfandRueckgabe/refund.
		if req.Amount < 0 {
			authOff := auth.Disabled(os.Getenv("UT_AUTH"))
			if !authOff {
				// An empty PIN can never authorize anything — reject it
				// before AuthorizeManager, which would otherwise burn a
				// failed-attempt count shared with keypad login
				// (internal/auth.Service: 5 failures device-wide locks out
				// for 30s). A blank manager_pin is the natural first
				// mistake here since, unlike refund.html's field, this one
				// can't be HTML-`required` — positive adjustments must be
				// allowed to submit it blank.
				if strings.TrimSpace(req.ManagerPIN) == "" {
					respondAdjustmentError(w, r, http.StatusForbidden, "manager PIN required")
					return
				}
				approver, err := dp.AuthSvc.AuthorizeManager(ctx, strings.TrimSpace(req.ManagerPIN))
				if err != nil {
					status := http.StatusForbidden
					if errors.Is(err, auth.ErrLockedOut) {
						status = http.StatusTooManyRequests
					}
					respondAdjustmentError(w, r, status, "manager PIN required")
					return
				}
				actorID = approver.ID
			}
		}

		// Record adjustment
		adjustmentID, err := pos.RecordCashAdjustment(ctx, dp.Db, pos.CashAdjustmentInput{
			ShiftID: req.ShiftID,
			Type:    req.Type,
			Amount:  money.FromMinor(req.Amount),
			Reason:  req.Reason,
			ActorID: actorID,
		})
		if err != nil {
			respondAdjustmentError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		respondAdjustmentSuccess(w, r, CashAdjustmentResponse{AdjustmentID: adjustmentID, Success: true})
	}
}

// PfandRueckgabeRequest models input for a bottle-deposit cash payout.
type PfandRueckgabeRequest struct {
	Amount     int64  `json:"amount"` // minor units, must be > 0 (the amount paid out)
	ManagerPIN string `json:"manager_pin"`
}

// PfandRueckgabe handles POST /api/shifts/pfandrueckgabe — a first-class,
// manager-gated bottle-deposit cash payout. Recorded via the same
// cash-adjustment machinery as any other payout (internal/pos.
// RecordCashAdjustment), with a fixed reason so it's reportable distinctly
// from free-text adjustments (pos.CashAdjustmentReasonPfandrueckgabe).
func PfandRueckgabe(dp *common.Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		var req PfandRueckgabeRequest

		contentType := r.Header.Get("Content-Type")
		if strings.Contains(contentType, "application/json") {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				respondAdjustmentError(w, r, http.StatusBadRequest, "invalid JSON")
				return
			}
		} else {
			if err := r.ParseForm(); err != nil {
				respondAdjustmentError(w, r, http.StatusBadRequest, "invalid form data")
				return
			}
			req.ManagerPIN = r.FormValue("manager_pin")
			if amtStr := r.FormValue("amount"); amtStr != "" {
				if amt, err := strconv.ParseInt(amtStr, 10, 64); err == nil {
					req.Amount = amt
				}
			}
		}

		if req.Amount <= 0 {
			respondAdjustmentError(w, r, http.StatusBadRequest, "amount must be greater than zero")
			return
		}

		actorID := getSessionUserID(r)
		if actorID == "" {
			respondAdjustmentError(w, r, http.StatusUnauthorized, "authentication required")
			return
		}

		// Manager approval; the PIN owner is the audit actor (pos-auth),
		// mirroring refund_page.go's gate — paying cash out is at least as
		// sensitive as a refund.
		authOff := auth.Disabled(os.Getenv("UT_AUTH"))
		if !authOff {
			// Same adjacent fix as RecordCashAdjustment's own gate
			// (ut-docs#266 review finding): this field also isn't
			// HTML-`required` (index.html), so a blank submission is a
			// real, easy first mistake — reject it before it burns a
			// failed-attempt count shared device-wide with keypad login.
			if strings.TrimSpace(req.ManagerPIN) == "" {
				respondAdjustmentError(w, r, http.StatusForbidden, "manager PIN required")
				return
			}
			approver, err := dp.AuthSvc.AuthorizeManager(ctx, strings.TrimSpace(req.ManagerPIN))
			if err != nil {
				status := http.StatusForbidden
				if errors.Is(err, auth.ErrLockedOut) {
					status = http.StatusTooManyRequests
				}
				respondAdjustmentError(w, r, status, "manager PIN required")
				return
			}
			actorID = approver.ID
		}

		// A payout is a WRITE against a shift, so it resolves against THIS
		// till's own register identity — never "whichever shift was opened
		// most recently anywhere", the heuristic that paid deposits out of
		// another register's drawer on a two-register shop (ut-docs#268).
		registerID, err := pos.ResolveTillRegisterID(ctx, dp.Db, dp.Settings)
		if err != nil {
			if errors.Is(err, pos.ErrRegisterIdentityAmbiguous) {
				respondAdjustmentError(w, r, http.StatusConflict, "this till's register is not set — choose it in Settings > Tills before recording a payout")
				return
			}
			respondAdjustmentError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		current, hasOpen, err := data.NewPOSRepo(dp.Db).CurrentOpenShiftForRegister(ctx, registerID)
		if err != nil {
			respondAdjustmentError(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		if !hasOpen {
			respondAdjustmentError(w, r, http.StatusNotFound, "shift not found or already closed")
			return
		}

		adjustmentID, err := pos.RecordCashAdjustment(ctx, dp.Db, pos.CashAdjustmentInput{
			ShiftID: current.ID,
			Type:    "payout",
			Amount:  -money.FromMinor(req.Amount),
			Reason:  pos.CashAdjustmentReasonPfandrueckgabe,
			ActorID: actorID,
		})
		if err != nil {
			respondAdjustmentError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		respondAdjustmentSuccess(w, r, CashAdjustmentResponse{AdjustmentID: adjustmentID, Success: true})
	}
}

// Helper response functions for shifts. JSON responses use the
// { "data": …, "error": … } envelope universal-till/CLAUDE.md mandates for
// every JSON API response (ut-docs#378).
func respondShiftError(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, status, map[string]any{"data": nil, "error": message})
	} else {
		writeHTML(w, status, fmt.Sprintf("<div class='error'>%s</div>", html.EscapeString(message)))
	}
}

func respondShiftSuccess(w http.ResponseWriter, r *http.Request, data ShiftOpenResponse) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "error": nil})
	} else {
		writeHTML(w, http.StatusOK, fmt.Sprintf("<div class='success'>Shift opened: %s</div>", data.ShiftID))
	}
}

func respondCloseError(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, status, map[string]any{"data": nil, "error": message})
	} else {
		writeHTML(w, status, fmt.Sprintf("<div class='error'>%s</div>", html.EscapeString(message)))
	}
}

func respondCloseSuccess(w http.ResponseWriter, r *http.Request, data ShiftCloseResponse) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "error": nil})
	} else {
		msg := fmt.Sprintf("<div class='success'>Shift closed. Expected: £%.2f, Actual: £%.2f, Variance: £%.2f</div>",
			float64(data.ExpectedCash)/100, float64(data.ClosingCash)/100, float64(data.Variance)/100)
		if data.Skim != 0 {
			// ut-docs#1006 review finding 9: the HTML path (what the close
			// form actually renders) previously dropped the skim/new-float
			// figures the JSON path already returned — an operator who just
			// skimmed the drawer got no on-screen confirmation of the new
			// float they left it on.
			msg = fmt.Sprintf("<div class='success'>Shift closed. Expected: £%.2f, Actual: £%.2f, Variance: £%.2f, Skim: £%.2f, New float: £%.2f</div>",
				float64(data.ExpectedCash)/100, float64(data.ClosingCash)/100, float64(data.Variance)/100,
				float64(-data.Skim)/100, float64(data.NewFloat)/100)
		}
		writeHTML(w, http.StatusOK, msg)
	}
}

func respondAdjustmentError(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, status, map[string]any{"data": nil, "error": message})
	} else {
		writeHTML(w, status, fmt.Sprintf("<div class='error'>%s</div>", html.EscapeString(message)))
	}
}

func respondAdjustmentSuccess(w http.ResponseWriter, r *http.Request, data CashAdjustmentResponse) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "error": nil})
	} else {
		writeHTML(w, http.StatusOK, fmt.Sprintf("<div class='success'>Adjustment recorded: %s</div>", data.AdjustmentID))
	}
}

// registerShiftsAPI registers shift API routes
func registerShiftsAPI(mux *http.ServeMux, dp *common.Deps) {
	mux.HandleFunc("POST /api/shifts/open", OpenShift(dp))
	mux.HandleFunc("POST /api/shifts/close", CloseShift(dp))
	mux.HandleFunc("POST /api/shifts/adjustment", RecordCashAdjustment(dp))
	mux.HandleFunc("POST /api/shifts/pfandrueckgabe", PfandRueckgabe(dp))
}
