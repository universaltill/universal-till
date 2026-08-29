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

func registerShiftsPage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("/shifts", func(w http.ResponseWriter, r *http.Request) {
		repo := data.NewPOSRepo(d.Db)
		current, hasOpen, _ := repo.CurrentOpenShift(r.Context())
		history, _ := repo.ListRecentShifts(r.Context(), 20)
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
		data := map[string]any{
			"title":          "Shifts",
			"theme":          d.CurrentState().Theme,
			"menuItems":      d.MenuSnapshot(),
			"Current":        current,
			"HasOpen":        hasOpen,
			"History":        history,
			"Registers":      registers,
			"TillRegisterID": tillRegisterID,
			// Minor units plus a pre-formatted decimal for the number input
			// (templates shouldn't do float math on money).
			"CarryForwardMinor": carryMinor,
			// ut-docs#1274: was hardcoded %d.%02d against /100, silently
			// wrong on a 0-decimal currency (IRR/IRT/IQD/AFN/JPY) — 500
			// minor units rendered as "5.00" instead of "500".
			"CarryForwardDisplay": httpx.FormatMajorPlain(carryMinor, httpx.ActiveCurrency().Decimals),
			"HasCarryForward":     carryMinor > 0,
		}
		httpx.Render("ui/pages/shifts.html", data)(w, r)
	})
}
