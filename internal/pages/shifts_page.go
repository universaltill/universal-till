package pages

import (
	"errors"
	"net/http"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// shiftHistoryRow is one History row; SlipSfx is empty for an open shift.
type shiftHistoryRow struct {
	data.ShiftSummary
	SlipSfx string
}

func registerShiftsPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/shifts", func(w http.ResponseWriter, r *http.Request) {
		repo := data.NewPOSRepo(d.Db)
		current, hasOpen, _ := repo.CurrentOpenShift(r.Context())
		// ut-docs#5755: the open shift's no-sale drawer opens, per cashier.
		// Best-effort -- a failed query omits the row, never fails the page
		// (and never shows a false 0). Declared before the local `data` map
		// below shadows the data package.
		noSaleCount, noSaleOK := 0, false
		var noSaleOps []data.NoSaleOperator
		if hasOpen {
			if n, ops, nsErr := repo.NoSaleForShift(r.Context(), current.ID); nsErr == nil {
				noSaleCount, noSaleOps, noSaleOK = n, ops, true
			} else {
				logging.L().Errorf("shift no-sale opens: %v", nsErr)
			}
		}
		recent, _ := repo.ListRecentShifts(r.Context(), 20)
		// ut-docs#5877: closed rows get a per-row slip result id (server-derived).
		history := make([]shiftHistoryRow, 0, len(recent))
		for _, s := range recent {
			row := shiftHistoryRow{ShiftSummary: s}
			if !s.Open {
				row.SlipSfx = shiftSlipRowSuffix(s.ID)
			}
			history = append(history, row)
		}
		// This till's own register identity (ut-docs#268), used only to
		// preselect the shift-open picker (ut-docs#940) -- ambiguous
		// (ErrRegisterIdentityAmbiguous) or any other resolution error just
		// leaves the picker unselected, same best-effort pattern as the
		// Settings page's till-register picker.
		tillRegisterID := ""
		if resolved, resolveErr := pos.ResolveTillRegisterID(r.Context(), d.Db, d.Settings); resolveErr == nil {
			tillRegisterID = resolved
		} else if !errors.Is(resolveErr, pos.ErrRegisterIdentityAmbiguous) {
			logging.L().Errorf("resolve till register: %v", resolveErr)
		}
		// Listed AFTER resolving (same reason as the Settings page's own
		// till-register picker): on an empty shop's very first shift-open,
		// ResolveTillRegisterID self-creates a register via EnsureRegister --
		// listing first would miss it as a real option and fall through to
		// the template's hardcoded "reg-default" fallback, which only
		// happens to line up with EnsureRegister's own default id today.
		registers, _ := repo.ListRegisters(r.Context())
		// Carried-forward opening float (ut-docs#1006): prefill the open
		// form with what this till's register's last close left in the
		// drawer (new float after any skim), so the operator confirms
		// rather than re-types it — still editable, an explicit value
		// always wins. Best-effort like the register resolution above; no
		// prior close (or no resolved register) leaves the currency's zero
		// default ("0.00", or "0" on a 0-decimal currency).
		carryMinor := int64(0)
		if tillRegisterID != "" {
			if carried, ok, cfErr := pos.LastClosedShiftNewFloat(r.Context(), d.Db, tillRegisterID); cfErr == nil && ok {
				carryMinor = carried.Minor()
			}
		}
		// ut-docs#5797: open shifts per register -- names for the Current card
		// and the X report picker. Best-effort: on error the X section is
		// hidden and the Current card shows raw ids. No figure is read here.
		openShifts, osErr := repo.ListOpenShifts(r.Context())
		if osErr != nil {
			logging.L().Errorf("list open shifts: %v", osErr)
		}
		xDefault, hasX := pickXShift(openShifts, "", xTillRegister(r.Context(), d))
		curRegName, curCashierName := current.RegisterID, current.CashierID
		for _, o := range openShifts {
			if o.ShiftID == current.ID {
				curRegName, curCashierName = o.RegisterName, o.CashierName
			}
		}
		data := map[string]any{
			"CurrentRegisterName": curRegName,
			"CurrentCashierName":  curCashierName,
			"HasX":                hasOpen && osErr == nil && hasX,
			"XShifts":             openShifts,
			"XShowPicker":         len(openShifts) > 1,
			"XDefaultRegister":    xDefault.RegisterID,
			"title":               httpx.T(httpx.RequestLocale(r), "page.title.shifts"),
			"theme":               d.ResolvedTheme(r),
			"menuItems":           d.MenuSnapshot(),
			"Current":             current,
			"HasOpen":             hasOpen,
			"NoSaleOK":            noSaleOK,
			"NoSaleCount":         noSaleCount,
			"NoSaleOperators":     noSaleOps,
			"History":             history,
			"Registers":           registers,
			"TillRegisterID":      tillRegisterID,
			// Minor units plus a pre-formatted decimal for the number input
			// (templates shouldn't do float math on money).
			"CarryForwardMinor": carryMinor,
			// ut-docs#1274: was hardcoded %d.%02d against /100, silently
			// wrong on a 0-decimal currency (IRR/IRT/IQD/AFN/JPY) — 500
			// minor units rendered as "5.00" instead of "500".
			"CarryForwardDisplay": httpx.FormatMajorPlain(carryMinor, httpx.ActiveCurrency().Decimals),
			"HasCarryForward":     carryMinor > 0,
			// NeedsManagerPIN (ut-docs#3134): the manager-PIN fields and the
			// skim-approval hint render only for a session whose role isn't
			// granted cash_adjustment (canPerform is true when auth is off).
			"NeedsManagerPIN": !canPerform(d, r, "cash_adjustment"),
		}
		httpx.Render("ui/pages/shifts.html", data)(w, r)
	})
	registerShiftXReport(mux, d)
}
