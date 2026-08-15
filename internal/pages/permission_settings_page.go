package pages

import (
	"fmt"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
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
			logging.L().Errorf("permission matrix: list: %v", err)
			http.Error(w, "failed to load permission matrix", http.StatusInternalServerError)
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
			row.Cells[g.Role] = gridCell{
				Granted: g.Granted,
				Locked:  g.Role == lockoutRole && g.Action == lockoutAction,
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
		role := r.FormValue("role")
		action := r.FormValue("action")
		granted := r.FormValue("granted") == "1"
		locale := httpx.ResolveLocale(w, r)

		// Reject clean 400s for bad input before ever touching a write —
		// role_permissions' FK constraints would also refuse an unknown
		// role/action, but as a raw SQLite error, not a message an operator
		// (or this page's own client-side JS) can act on.
		if role == "" || action == "" {
			http.Error(w, "role and action required", http.StatusBadRequest)
			return
		}
		if ok, err := authRepo.RoleExists(r.Context(), role); err != nil {
			logging.L().Errorf("permission matrix: role exists check: %v", err)
			http.Error(w, "failed to save", http.StatusInternalServerError)
			return
		} else if !ok {
			http.Error(w, "unknown role", http.StatusBadRequest)
			return
		}
		if ok, err := authRepo.ActionExists(r.Context(), action); err != nil {
			logging.L().Errorf("permission matrix: action exists check: %v", err)
			http.Error(w, "failed to save", http.StatusInternalServerError)
			return
		} else if !ok {
			http.Error(w, "unknown action", http.StatusBadRequest)
			return
		}

		// Self-lockout guard. Returns 200 (not 409): htmx never swaps a
		// non-2xx response by default, and this codebase's only override
		// (web/public/app.js's beforeSwap) is scoped to 400s under
		// /api/pos/ — a 409 here would render as a generic "server error"
		// banner, not the actual reason, on the one message this guard
		// exists to surface.
		if role == lockoutRole && action == lockoutAction && !granted {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<span class="login-error">%s</span>`, httpx.T(locale, "permissions.lockout_error"))
			return
		}

		tx, err := d.Db.BeginTx(r.Context(), nil)
		if err != nil {
			logging.L().Errorf("permission matrix: begin tx: %v", err)
			http.Error(w, "failed to save", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		if err := authRepo.SetRolePermission(r.Context(), tx, role, action, granted); err != nil {
			logging.L().Errorf("permission matrix: set role permission: %v", err)
			http.Error(w, "failed to save", http.StatusInternalServerError)
			return
		}
		verb := "role_permission_revoked"
		if granted {
			verb = "role_permission_granted"
		}
		if err := posRepo.InsertAudit(r.Context(), tx, getSessionUserID(r), "role_permission", role+":"+action, verb,
			map[string]any{"role": role, "action": action, "granted": granted},
			time.Now().UTC().Format(time.RFC3339), ""); err != nil {
			logging.L().Errorf("permission matrix: journal write: %v", err)
			http.Error(w, "failed to save", http.StatusInternalServerError)
			return
		}
		if err := tx.Commit(); err != nil {
			logging.L().Errorf("permission matrix: commit: %v", err)
			http.Error(w, "failed to save", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, "permissions.saved"))
	})
}
