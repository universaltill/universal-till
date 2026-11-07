package pages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// xRegisterMaxLen caps the posted register id (ut-docs#5797).
const xRegisterMaxLen = 128

// registerShiftXReport wires POST /shifts/x-report (ut-docs#5797): the
// mid-shift X report of one register's open shift, as an HTML fragment for
// the /shifts page or JSON on Accept: application/json. Read-only: it writes
// no row at all (no audit, no archive, no setting) -- ut-docs design-5780 D2.
func registerShiftXReport(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("POST /shifts/x-report", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		r.Body = http.MaxBytesReader(w, r.Body, 4096)
		if err := r.ParseForm(); err != nil {
			writeXReportError(w, r, http.StatusBadRequest, "invalid_request", "shifts.x_report.invalid_request", nil)
			return
		}
		// PostForm only: a PIN or register in the query string is ignored.
		register := strings.TrimSpace(r.PostForm.Get("register"))
		pin := strings.TrimSpace(r.PostForm.Get("override_pin"))
		if len(register) > xRegisterMaxLen {
			writeXReportError(w, r, http.StatusBadRequest, "invalid_register", "shifts.x_report.invalid_register", nil)
			return
		}

		repo := data.NewPOSRepo(d.Db)
		open, err := repo.ListOpenShifts(ctx)
		if err != nil {
			writeXReportError(w, r, http.StatusInternalServerError, "x_report_failed", "shifts.x_report.failed", err)
			return
		}
		sel, ok := pickXShift(open, register, xTillRegister(ctx, d))
		if !ok {
			writeXReportError(w, r, http.StatusNotFound, "no_open_shift", "shifts.x_report.no_open_shift", nil)
			return
		}

		elev := checkOrElevate(d, r, "reports", pin)
		if elev.Outcome == needsElevation {
			if wantsJSON(r) {
				status := http.StatusForbidden
				if errors.Is(elev.Err, auth.ErrLockedOut) {
					status = http.StatusTooManyRequests
				}
				writeXReportError(w, r, status, "manager_pin_required", "common.error.manager_or_admin_required", nil)
				return
			}
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/shifts/x-report", "#x-report-body",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.x_report"), sel.RegisterName),
				[]elevationHiddenField{{Name: "register", Value: sel.RegisterID}}, elev)
			return
		}

		rep, err := repo.ShiftXReport(ctx, sel.ShiftID, time.Now())
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeXReportError(w, r, http.StatusNotFound, "no_open_shift", "shifts.x_report.no_open_shift", nil)
				return
			}
			writeXReportError(w, r, http.StatusInternalServerError, "x_report_failed", "shifts.x_report.failed", err)
			return
		}
		if !rep.Shift.Open { // closed between the list and the read
			writeXReportError(w, r, http.StatusNotFound, "no_open_shift", "shifts.x_report.no_open_shift", nil)
			return
		}
		if wantsJSON(r) {
			writeJSON(w, http.StatusOK, map[string]any{"data": toXReportDTO(rep), "error": nil})
			return
		}
		httpx.RenderPartial("ui/partials/shift_x_report.html", map[string]any{"R": rep})(w, r)
	})
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json")
}

// pickXShift implements design D7 over ListOpenShifts' rows: an explicit
// register must be in the list; empty picks this till's register when it has
// an open shift, else the newest open shift (largest OpenedAt, ties to the
// larger shift id, as CurrentOpenShift).
func pickXShift(open []data.OpenShift, register, tillRegister string) (data.OpenShift, bool) {
	find := func(reg string) (data.OpenShift, bool) {
		for _, o := range open {
			if o.RegisterID == reg {
				return o, true
			}
		}
		return data.OpenShift{}, false
	}
	if register != "" {
		return find(register)
	}
	if tillRegister != "" {
		if o, ok := find(tillRegister); ok {
			return o, true
		}
	}
	var best data.OpenShift
	found := false
	for _, o := range open {
		if !found || o.OpenedAt > best.OpenedAt || (o.OpenedAt == best.OpenedAt && o.ShiftID > best.ShiftID) {
			best, found = o, true
		}
	}
	return best, found
}

// xTillRegister reads this till's persisted register WITHOUT
// pos.ResolveTillRegisterID, which may write (EnsureRegister / settings
// Set): the X endpoint must write nothing. nil Settings, an error or an
// unset key all give "".
func xTillRegister(ctx context.Context, d *common.Deps) string {
	if d.Settings == nil {
		return ""
	}
	v, ok, err := d.Settings.Get(ctx, pos.SettingsKeyTillRegisterID)
	if err != nil || !ok {
		return ""
	}
	return strings.TrimSpace(v)
}

// writeXReportError writes {"data":null,"error":{"code","message"}} for JSON
// clients or an escaped <div class='error' role='alert'> fragment otherwise;
// the cause (if any) is logged for server errors.
func writeXReportError(w http.ResponseWriter, r *http.Request, status int, code, key string, cause error) {
	if cause != nil && status >= http.StatusInternalServerError {
		logging.L().Errorf("x report: %s: %v", code, cause)
	}
	msg := httpx.T(httpx.ResolveLocale(w, r), key)
	if wantsJSON(r) {
		writeJSON(w, status, map[string]any{"data": nil, "error": map[string]string{"code": code, "message": msg}})
		return
	}
	writeHTML(w, status, "<div class='error' role='alert'>"+html.EscapeString(msg)+"</div>")
}

// --- JSON DTO (money = integer minor units, snake_case) ---

type xCountTotalDTO struct {
	Count      int   `json:"count"`
	TotalMinor int64 `json:"total_minor"`
}

type xTenderDTO struct {
	MethodID      string `json:"method_id"`
	Name          string `json:"name"`
	Type          string `json:"type"`
	InMinor       int64  `json:"in_minor"`
	RefundedMinor int64  `json:"refunded_minor"`
	NetMinor      int64  `json:"net_minor"`
	TipsMinor     int64  `json:"tips_minor"`
}

type xAdjustmentDTO struct {
	Type        string `json:"type"`
	Reason      string `json:"reason"`
	AmountMinor int64  `json:"amount_minor"`
	At          string `json:"at"`
}

type xCashDTO struct {
	OpeningMinor          int64            `json:"opening_minor"`
	CashSalesMinor        int64            `json:"cash_sales_minor"`
	TipsHeldOutMinor      int64            `json:"tips_held_out_minor"`
	AdjustmentsTotalMinor int64            `json:"adjustments_total_minor"`
	ExpectedMinor         int64            `json:"expected_minor"`
	Adjustments           []xAdjustmentDTO `json:"adjustments"`
}

type xNoSaleDTO struct {
	Count     int                   `json:"count"`
	Operators []data.NoSaleOperator `json:"operators"`
}

type xReportDTO struct {
	ShiftID      string `json:"shift_id"`
	RegisterID   string `json:"register_id"`
	RegisterName string `json:"register_name"`
	CashierID    string `json:"cashier_id"`
	CashierName  string `json:"cashier_name"`
	OpenedAt     string `json:"opened_at"`
	GeneratedAt  string `json:"generated_at"`
	Currency     string `json:"currency"`
	Sales        struct {
		Count      int   `json:"count"`
		GrossMinor int64 `json:"gross_minor"`
	} `json:"sales"`
	Returns        xCountTotalDTO `json:"returns"`
	NetMinor       int64          `json:"net_minor"`
	TaxMinor       int64          `json:"tax_minor"`
	Voids          xCountTotalDTO `json:"voids"`
	ClearedBaskets xCountTotalDTO `json:"cleared_baskets"`
	Tenders        []xTenderDTO   `json:"tenders"`
	Cash           xCashDTO       `json:"cash"`
	NoSale         xNoSaleDTO     `json:"no_sale"`
	FirstReceipt   string         `json:"first_receipt"`
	LastReceipt    string         `json:"last_receipt"`
}

// xISOTime renders a stored timestamp as RFC 3339 UTC; unparseable input is
// returned as stored (never blanked).
func xISOTime(s string) string {
	if t, ok := httpx.ParseStoredTime(s); ok {
		return t.UTC().Format(time.RFC3339)
	}
	return s
}

func toXReportDTO(rep data.XReport) xReportDTO {
	o := xReportDTO{
		ShiftID: rep.Shift.ID, RegisterID: rep.Shift.RegisterID, RegisterName: rep.RegisterName,
		CashierID: rep.Shift.CashierID, CashierName: rep.CashierName,
		OpenedAt: xISOTime(rep.Shift.OpenedAt), GeneratedAt: xISOTime(rep.GeneratedAt),
		Currency: httpx.ActiveCurrency().Code,
		NetMinor: rep.Net.Minor(), TaxMinor: rep.Tax.Minor(),
		Returns:        xCountTotalDTO{rep.ReturnsCount, rep.ReturnsTotal.Minor()},
		Voids:          xCountTotalDTO{rep.VoidsCount, rep.VoidsTotal.Minor()},
		ClearedBaskets: xCountTotalDTO{rep.ClearedBasketsCount, rep.ClearedBasketsTotal.Minor()},
		Tenders:        []xTenderDTO{},
		FirstReceipt:   rep.FirstReceipt, LastReceipt: rep.LastReceipt,
	}
	o.Sales.Count, o.Sales.GrossMinor = rep.SalesCount, rep.SalesGross.Minor()
	for _, m := range rep.Methods {
		o.Tenders = append(o.Tenders, xTenderDTO{m.MethodID, m.Name, m.Type, m.In.Minor(), m.Refunded.Minor(), m.Net.Minor(), m.Tips.Minor()})
	}
	o.Cash = xCashDTO{
		OpeningMinor: rep.Cash.Opening.Minor(), CashSalesMinor: rep.Cash.CashSales.Minor(),
		TipsHeldOutMinor: rep.Cash.TipsHeldOut.Minor(), AdjustmentsTotalMinor: rep.Cash.AdjustmentsTotal.Minor(),
		ExpectedMinor: rep.Cash.Expected.Minor(), Adjustments: []xAdjustmentDTO{},
	}
	for _, a := range rep.Cash.Adjustments {
		o.Cash.Adjustments = append(o.Cash.Adjustments, xAdjustmentDTO{a.Type, a.Reason, a.Amount.Minor(), xISOTime(a.At)})
	}
	o.NoSale = xNoSaleDTO{Count: rep.NoSale, Operators: []data.NoSaleOperator{}}
	if rep.NoSaleOperators != nil {
		o.NoSale.Operators = rep.NoSaleOperators
	}
	return o
}
