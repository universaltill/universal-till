package pages

import (
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerUsers wires the operator admin page (docs: architecture/pos-auth.md).
// Manager/admin only; managers can only manage cashiers, 'system' is never
// editable, and the last active admin with a PIN cannot be deactivated.
func registerUsers(mux *http.ServeMux, d *common.Deps, svc *auth.Service) {
	repo := svc.Repo()
	posRepo := data.NewPOSRepo(d.Db)

	// requireManager gates on the user_management action (039's catalog),
	// not the old IsManager() bit — granted to manager/admin/super_admin,
	// same set IsManager() plus super_admin recognized (ut-docs#556: without
	// this, super_admin can never reach /users at all, so the Permissions
	// link this page also carries would be dead — a super_admin session
	// 403'd here before ever seeing it).
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "user_management") {
			http.Error(w, "manager or admin role required", http.StatusForbidden)
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// canManage: admins and super_admins manage everyone but 'system';
	// managers only cashiers.
	canManage := func(actor auth.User, target data.UserRow) bool {
		if target.ID == "system" {
			return false
		}
		if actor.Role == "admin" || actor.Role == "super_admin" {
			return true
		}
		return target.Role == "cashier"
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "user", targetID, action, nil, now, "")
	}

	renderUsers := func(w http.ResponseWriter, r *http.Request, actor auth.User, errKey string) {
		users, err := repo.ListUsers(r.Context())
		if err != nil {
			http.Error(w, "failed to load users", http.StatusInternalServerError)
			return
		}
		type row struct {
			data.UserRow
			HasPIN  bool
			CanEdit bool
		}
		rows := make([]row, 0, len(users))
		for _, u := range users {
			if u.ID == "system" {
				continue // service identity, not an operator
			}
			rows = append(rows, row{UserRow: u, HasPIN: u.PinHash != "", CanEdit: canManage(actor, u)})
		}
		httpx.Render("ui/pages/users.html", map[string]any{
			"title":              "Users",
			"theme":              d.CurrentState().Theme,
			"menuItems":          d.MenuSnapshot(),
			"users":              rows,
			// super_admin is the top of the role hierarchy (canManage
			// above already treats it that way) — it must see at least
			// what a plain admin sees, including the manager/admin role
			// options (ut-docs#761 review finding 3).
			"isAdmin":            actor.Role == "admin" || actor.Role == "super_admin",
			"canEditPermissions": canPerform(d, r, lockoutAction), // super_admin only (ut-docs#556)
			"errKey":             errKey,
		})(w, r)
	}

	mux.HandleFunc("GET /users", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		renderUsers(w, r, actor, r.URL.Query().Get("err"))
	})

	mux.HandleFunc("POST /api/users", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		_ = r.ParseForm()
		username, display, role := r.PostFormValue("username"), r.PostFormValue("display_name"), r.PostFormValue("role")
		if role != "cashier" && role != "manager" && role != "admin" && role != "super_admin" {
			http.Redirect(w, r, "/users?err=users.error.role", http.StatusSeeOther)
			return
		}
		if role == "super_admin" {
			// Creating a super_admin is at least as sensitive as anything
			// permission_management gates (ut-docs#761, mirrors 047's own
			// migration comment) — deliberately its own branch rather than
			// falling into the "admins create managers/admins" rule below,
			// which an admin actor would otherwise satisfy.
			if !canPerform(d, r, "permission_management") {
				http.Error(w, "super_admin required", http.StatusForbidden)
				return
			}
		} else if actor.Role != "admin" && actor.Role != "super_admin" && role != "cashier" {
			// super_admin included (ut-docs#761 review finding 3) — it's
			// the top of the role hierarchy and must be able to do at
			// least what a plain admin can.
			http.Error(w, "only admins create managers or admins", http.StatusForbidden)
			return
		}
		if username == "" || display == "" {
			http.Redirect(w, r, "/users?err=users.error.required", http.StatusSeeOther)
			return
		}
		id, err := repo.CreateUser(r.Context(), username, display, role)
		if err != nil {
			http.Redirect(w, r, "/users?err=users.error.create", http.StatusSeeOther)
			return
		}
		audit(r, actor.ID, id, "user_create")
		http.Redirect(w, r, "/users", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/users/{id}/pin", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		target, found, err := repo.GetUser(r.Context(), r.PathValue("id"))
		if err != nil || !found || !canManage(actor, target) {
			http.Error(w, "cannot manage this user", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		pin := r.PostFormValue("pin")
		if auth.ValidatePINFormat(pin) != nil {
			http.Redirect(w, r, "/users?err=auth.error.pin_format", http.StatusSeeOther)
			return
		}
		// PIN-only login: the PIN must identify exactly one operator.
		if owner, taken, err := svc.FindUserByPIN(r.Context(), pin); err == nil && taken && owner.ID != target.ID {
			http.Redirect(w, r, "/users?err=users.error.pin_taken", http.StatusSeeOther)
			return
		} else if err != nil {
			http.Error(w, "pin check failed", http.StatusInternalServerError)
			return
		}
		hash, err := auth.HashPIN(pin)
		if err != nil {
			http.Redirect(w, r, "/users?err=auth.error.pin_format", http.StatusSeeOther)
			return
		}
		if err := repo.SetUserPIN(r.Context(), target.ID, hash); err != nil {
			http.Error(w, "failed to set pin", http.StatusInternalServerError)
			return
		}
		// A changed credential invalidates existing sessions.
		_ = repo.RevokeUserSessions(r.Context(), target.ID)
		audit(r, actor.ID, target.ID, "user_pin_set")
		http.Redirect(w, r, "/users", http.StatusSeeOther)
	})

	mux.HandleFunc("POST /api/users/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		target, found, err := repo.GetUser(r.Context(), r.PathValue("id"))
		if err != nil || !found || !canManage(actor, target) {
			http.Error(w, "cannot manage this user", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		activate := r.PostFormValue("active") == "1"
		if !activate && target.Role == "admin" {
			others, err := repo.CountOtherActiveAdminsWithPIN(r.Context(), target.ID)
			if err != nil || others == 0 {
				http.Redirect(w, r, "/users?err=users.error.last_admin", http.StatusSeeOther)
				return
			}
		}
		// Same guard, super_admin side (ut-docs#761 review finding 4):
		// deactivating the only super_admin would strand the till with
		// nobody able to reach the permission matrix, audit page or
		// backoffice.
		if !activate && target.Role == "super_admin" {
			others, err := repo.CountOtherActiveSuperAdminsWithPIN(r.Context(), target.ID)
			if err != nil || others == 0 {
				http.Redirect(w, r, "/users?err=users.error.last_super_admin", http.StatusSeeOther)
				return
			}
		}
		if err := repo.SetUserActive(r.Context(), target.ID, activate); err != nil {
			http.Error(w, "failed to update user", http.StatusInternalServerError)
			return
		}
		if !activate {
			_ = repo.RevokeUserSessions(r.Context(), target.ID)
		}
		action := "user_deactivate"
		if activate {
			action = "user_activate"
		}
		audit(r, actor.ID, target.ID, action)
		http.Redirect(w, r, "/users", http.StatusSeeOther)
	})

	// POST /api/users/{id}/promote-super-admin closes the ut-docs#761 gap:
	// nothing before this could ever create or promote a super_admin user,
	// so every super_admin-gated surface built so far (audit_page.go,
	// backoffice_page.go, permission_settings_page.go) was unreachable in
	// production. Deliberately gated on permission_management, not merely
	// "admin" — a promotion is at least as sensitive as anything that
	// action already gates (047's own migration comment), and a plain
	// admin promoting itself would defeat that gate's whole point. A
	// single-purpose action rather than a general role editor, to keep
	// this change scoped to the one gap the card asks to close.
	mux.HandleFunc("POST /api/users/{id}/promote-super-admin", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "permission_management") {
			http.Error(w, "super_admin required", http.StatusForbidden)
			return
		}
		actorID := getSessionUserID(r)
		target, found, err := repo.GetUser(r.Context(), r.PathValue("id"))
		if err != nil || !found {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		// 'system' (audit actor for till-initiated writes) and 'kiosk'
		// (migration 018 — the PIN-less service identity self-order sales
		// attribute to, reachable by any anonymous LAN client via the
		// auth-exempt /self-order surface) are never real operators —
		// promoting either would be a real privilege-escalation path, not
		// a theoretical one.
		if target.ID == "system" || target.ID == "kiosk" {
			http.Error(w, "cannot promote this user", http.StatusForbidden)
			return
		}
		if target.Role == "super_admin" {
			// Already there — a no-op, not an error.
			http.Redirect(w, r, "/users", http.StatusSeeOther)
			return
		}

		tx, err := d.Db.BeginTx(r.Context(), nil)
		if err != nil {
			http.Redirect(w, r, "/users?err=users.error.promote", http.StatusSeeOther)
			return
		}
		defer tx.Rollback()

		if err := repo.SetUserRole(r.Context(), tx, target.ID, "super_admin"); err != nil {
			http.Redirect(w, r, "/users?err=users.error.promote", http.StatusSeeOther)
			return
		}
		if err := posRepo.InsertAudit(r.Context(), tx, actorID, "user", target.ID, "user_role_changed",
			// "via" mirrors the bootstrap CLI's own audit payload
			// (scripts/promote-super-admin) so an auditor reading
			// user_role_changed entries can tell the two provenances
			// apart without cross-referencing actor ids.
			map[string]any{"from": target.Role, "to": "super_admin", "via": "in-app"}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
			http.Redirect(w, r, "/users?err=users.error.promote", http.StatusSeeOther)
			return
		}
		if err := tx.Commit(); err != nil {
			http.Redirect(w, r, "/users?err=users.error.promote", http.StatusSeeOther)
			return
		}
		// A changed role is a changed privilege boundary — same
		// "invalidate existing sessions" convention SetUserPIN/deactivate
		// already follow, so the new role takes effect on next login
		// rather than silently applying mid-session.
		_ = repo.RevokeUserSessions(r.Context(), target.ID)
		http.Redirect(w, r, "/users", http.StatusSeeOther)
	})
}
