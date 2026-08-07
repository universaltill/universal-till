package pages

import (
	"encoding/json"
	"errors"
	"fmt"
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

// ShiftOpenRequest models input for opening a shift
type ShiftOpenRequest struct {
	RegisterID  string `json:"register_id"`
	CashierID   string `json:"cashier_id"`
	OpeningCash int64  `json:"opening_cash"` // minor units
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
			if ocStr := r.FormValue("opening_cash"); ocStr != "" {
				if oc, err := strconv.ParseInt(ocStr, 10, 64); err == nil {
					req.OpeningCash = oc
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
		if req.OpeningCash < 0 {
			respondShiftError(w, r, http.StatusBadRequest, "opening_cash must be >= 0")
			return
		}

		// Open shift
		shiftID, err := pos.OpenShift(ctx, dp.Db, pos.ShiftInput{
			RegisterID:  req.RegisterID,
			CashierID:   req.CashierID,
			OpeningCash: money.FromMinor(req.OpeningCash),
		})
		if err != nil {
			respondShiftError(w, r, http.StatusInternalServerError, err.Error())
			return
		}

		respondShiftSuccess(w, r, ShiftOpenResponse{ShiftID: shiftID, Success: true})
	}
}

// ShiftCloseRequest models input for closing a shift
type ShiftCloseRequest struct {
	ShiftID     string `json:"shift_id"`
	ClosingCash int64  `json:"closing_cash"` // minor units
	Note        string `json:"note"`
}

// ShiftCloseResponse models response for shift close
type ShiftCloseResponse struct {
	Success      bool   `json:"success"`
	Message      string `json:"message,omitempty"`
	ExpectedCash int64  `json:"expected_cash,omitempty"`
	ClosingCash  int64  `json:"closing_cash,omitempty"`
	Variance     int64  `json:"variance,omitempty"`
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
			if ccStr := r.FormValue("closing_cash"); ccStr != "" {
				if cc, err := strconv.ParseInt(ccStr, 10, 64); err == nil {
					req.ClosingCash = cc
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
			ShiftID:     req.ShiftID,
			ClosingCash: money.FromMinor(req.ClosingCash),
			Note:        req.Note,
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
		})
	}
}

// CashAdjustmentRequest models input for cash adjustments/payouts
type CashAdjustmentRequest struct {
	ShiftID string `json:"shift_id"`
	Type    string `json:"type"`   // payout|adjustment
	Amount  int64  `json:"amount"` // minor units, negative for payout
	Reason  string `json:"reason"`
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
		if req.Type != "payout" && req.Type != "adjustment" {
			respondAdjustmentError(w, r, http.StatusBadRequest, "type must be 'payout' or 'adjustment'")
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

		current, hasOpen, err := data.NewPOSRepo(dp.Db).CurrentOpenShift(ctx)
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
		writeHTML(w, status, fmt.Sprintf("<div class='error'>%s</div>", message))
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
		writeHTML(w, status, fmt.Sprintf("<div class='error'>%s</div>", message))
	}
}

func respondCloseSuccess(w http.ResponseWriter, r *http.Request, data ShiftCloseResponse) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]any{"data": data, "error": nil})
	} else {
		msg := fmt.Sprintf("<div class='success'>Shift closed. Expected: £%.2f, Actual: £%.2f, Variance: £%.2f</div>",
			float64(data.ExpectedCash)/100, float64(data.ClosingCash)/100, float64(data.Variance)/100)
		writeHTML(w, http.StatusOK, msg)
	}
}

func respondAdjustmentError(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, status, map[string]any{"data": nil, "error": message})
	} else {
		writeHTML(w, status, fmt.Sprintf("<div class='error'>%s</div>", message))
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
