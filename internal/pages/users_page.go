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

	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		u, ok := auth.FromContext(r.Context())
		if !ok || !u.IsManager() {
			http.Error(w, "manager or admin role required", http.StatusForbidden)
			return auth.User{}, false
		}
		return u, true
	}

	// canManage: admins manage everyone but 'system'; managers only cashiers.
	canManage := func(actor auth.User, target data.UserRow) bool {
		if target.ID == "system" {
			return false
		}
		if actor.Role == "admin" {
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
			"title":     "Users",
			"theme":     d.CurrentState().Theme,
			"menuItems": d.Menu,
			"users":     rows,
			"isAdmin":   actor.Role == "admin",
			"errKey":    errKey,
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
		if role != "cashier" && role != "manager" && role != "admin" {
			http.Redirect(w, r, "/users?err=users.error.role", http.StatusSeeOther)
			return
		}
		if actor.Role != "admin" && role != "cashier" {
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
}
