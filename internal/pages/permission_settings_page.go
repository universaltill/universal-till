package pages

import (
	"fmt"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// lockoutAction/lockoutRole are the one grant this page can never let go to
// zero: the (super_admin, permission_management) cell gates this page
// itself. Revoking it would permanently lock every super_admin — including
// the one making the change — out of the only surface that can grant it
// back (ut-docs#556's own acceptance criteria).
const (
	lockoutRole   = "super_admin"
	lockoutAction = "permission_management"
)

// registerPermissionSettings wires the super_admin-only role→action
// permission-matrix editor (ut-docs#556, split (c) of #520). Dogfoods the
// #554 Can() mechanism it edits: the page is itself gated on the
// `permission_management` action, seeded super_admin-only (migration 047).
func registerPermissionSettings(mux *http.ServeMux, d *common.Deps) {
	authRepo := data.NewAuthRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	type gridCell struct {
		Granted bool
		Locked  bool // this exact cell can't be unchecked (self-lockout guard)
		Toggled int  // 1 or 0 — the value a click sends (opposite of Granted); no ternary in html/template
	}
	type actionRow struct {
		Action string
		Cells  map[string]gridCell // by role
	}

	mux.HandleFunc("GET /users/permissions", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, lockoutAction) {
			http.Error(w, "super_admin required", http.StatusForbidden)
			return
		}
		grants, err := authRepo.ListRolePermissionMatrix(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var roles []string
		seenRole := map[string]bool{}
		rowByAction := map[string]*actionRow{}
		var rows []*actionRow
		for _, g := range grants {
			if !seenRole[g.Role] {
				seenRole[g.Role] = true
				roles = append(roles, g.Role)
			}
			row, ok := rowByAction[g.Action]
			if !ok {
				row = &actionRow{Action: g.Action, Cells: map[string]gridCell{}}
				rowByAction[g.Action] = row
				rows = append(rows, row)
			}
			toggled := 1
			if g.Granted {
				toggled = 0
			}
			row.Cells[g.Role] = gridCell{
				Granted: g.Granted,
				Locked:  g.Role == lockoutRole && g.Action == lockoutAction,
				Toggled: toggled,
			}
		}

		httpx.Render("ui/pages/permissions.html", map[string]any{
			"title":     "Permissions",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.MenuSnapshot(),
			"Roles":     roles,
			"Rows":      rows,
		})(w, r)
	})

	mux.HandleFunc("POST /api/users/permissions", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, lockoutAction) {
			http.Error(w, "super_admin required", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		role := r.FormValue("role")
		action := r.FormValue("action")
		granted := r.FormValue("granted") == "1"

		locale := httpx.ResolveLocale(w, r)
		if role == lockoutRole && action == lockoutAction && !granted {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusConflict)
			fmt.Fprintf(w, `<span class="error">%s</span>`, httpx.T(locale, "permissions.lockout_error"))
			return
		}
		if role == "" || action == "" {
			http.Error(w, "role and action required", http.StatusBadRequest)
			return
		}

		tx, err := d.Db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		if err := authRepo.SetRolePermission(r.Context(), tx, role, action, granted); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		verb := "role_permission_revoked"
		if granted {
			verb = "role_permission_granted"
		}
		if err := posRepo.InsertAudit(r.Context(), tx, getSessionUserID(r), "role_permission", role+":"+action, verb,
			map[string]any{"role": role, "action": action, "granted": granted},
			time.Now().UTC().Format(time.RFC3339), ""); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "permissions.saved"))
	})
}
