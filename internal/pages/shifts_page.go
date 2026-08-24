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
		data := map[string]any{
			"title":          "Shifts",
			"theme":          d.CurrentState().Theme,
			"menuItems":      d.MenuSnapshot(),
			"Current":        current,
			"HasOpen":        hasOpen,
			"History":        history,
			"Registers":      registers,
			"TillRegisterID": tillRegisterID,
		}
		httpx.Render("ui/pages/shifts.html", data)(w, r)
	})
}
