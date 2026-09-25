package pages

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Users and PINs write-through, main-till side (ADR-0115 §1, ut-docs#2755).
// users travels main -> additional tills in the admin bundle
// (sync_admin_repo.go, main-till-wins: deleteMissing + ON CONFLICT DO
// UPDATE), so a user created or changed only on an additional till was
// silently reverted by its next pull. An additional till now sends every
// such change here first (user_sync_proxy.go) and mirrors the answer
// locally; the main till is the one place the change is decided.
//
//   - POST /api/sync/users/apply -- {op, user_id, username, display_name,
//     role, pin_hash, active, actor_id}; op is create | set_pin |
//     set_active | set_role. Every invariant the main till can check
//     without the plaintext PIN is re-checked here against its own state:
//     the actor's permission for the change (authorizeUserChange in
//     user_rules.go: the users page's rules, evaluated against the actor
//     row THIS till holds, never trusted from the additional till; 403
//     "forbidden"), role allow-list, username uniqueness, the
//     last-active-admin / last-super_admin guards (counted in the same
//     transaction as the write -- AuthRepo.SetUser*Guarded -- so two
//     concurrent changes cannot both pass), and pin_hash must be a
//     well-formed HashPIN string
//     (auth.ValidatePINHash) -- it is stored exactly as received. PIN
//     uniqueness across users cannot be re-checked from a salted hash; the
//     additional till checks it against its mirror (the ADR's accepted
//     residual).
//   - The answer is { "data": {user row WITHOUT pin_hash}, "error": null }
//     on success, { "data": null, "error": {code, message} } with a 4xx on
//     a refusal. For create, the main till assigns the id.
//
// Bearer-authed via syncTill and on internal/auth/middleware.go's exempt
// list (TestSyncPullPathsAreExempt pins it), like every /api/sync/*
// endpoint. The PIN never travels (only its hash) and neither is ever
// logged or echoed back.

// syncUserApplyRequest is the POST body. Active is a pointer so set_active
// can tell "false" from "missing".
type syncUserApplyRequest struct {
	Op          string `json:"op"`
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	PinHash     string `json:"pin_hash"`
	Active      *bool  `json:"active"`
	ActorID     string `json:"actor_id"`
}

// syncUserRow is the answered user row -- deliberately without pin_hash;
// has_pin says whether one is set.
type syncUserRow struct {
	ID          string `json:"id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Active      bool   `json:"active"`
	HasPIN      bool   `json:"has_pin"`
}

func userToSyncRow(u data.UserRow) syncUserRow {
	return syncUserRow{ID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role, Active: u.IsActive, HasPIN: u.PinHash != ""}
}

// syncUserError is the error object of a refused apply.
type syncUserError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeSyncUserError(w http.ResponseWriter, status int, code, message string) {
	writeSyncOrdersJSON(w, status, nil, syncUserError{Code: code, Message: message})
}

// writeSyncUserLastPrivileged answers a change the in-transaction
// last-admin / last-super_admin guard refused (refusedRole is "admin" or
// "super_admin", from AuthRepo.SetUser*Guarded).
func writeSyncUserLastPrivileged(w http.ResponseWriter, refusedRole string) {
	writeSyncUserError(w, http.StatusConflict, "last_"+refusedRole, "would leave no active "+refusedRole)
}

// writeSyncUserForbidden answers a change the actor may not make
// (authorizeUserChange).
func writeSyncUserForbidden(w http.ResponseWriter) {
	writeSyncUserError(w, http.StatusForbidden, "forbidden", "actor may not make this change")
}

// maxSyncUserBody bounds the request body: the largest legitimate one is a
// few hundred bytes.
const maxSyncUserBody = 16 << 10

// registerSyncUsers mounts the main-till user write-through endpoint on the
// bearer-authed /api/sync/* surface.
func registerSyncUsers(mux *http.ServeMux, d *common.Deps) {
	tills := data.NewTillsRepo(d.Db)
	repo := data.NewAuthRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)

	mux.HandleFunc("POST /api/sync/users/apply", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		till, ok := syncTill(r, tills)
		if !ok {
			writeSyncUserError(w, http.StatusUnauthorized, "unauthorized", "unauthorized")
			return
		}
		// A till that itself follows a main till would have this change
		// reverted by its own next pull -- never accept it here.
		if d.SyncPrimaryURL(ctx) != "" {
			writeSyncUserError(w, http.StatusConflict, "replica", "this till follows a main till")
			return
		}
		var in syncUserApplyRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSyncUserBody)).Decode(&in); err != nil {
			writeSyncUserError(w, http.StatusBadRequest, "invalid_body", "invalid body")
			return
		}
		in.Op = strings.TrimSpace(in.Op)
		in.UserID = strings.TrimSpace(in.UserID)
		in.Username = strings.TrimSpace(in.Username)
		in.DisplayName = strings.TrimSpace(in.DisplayName)
		in.Role = strings.TrimSpace(in.Role)
		in.PinHash = strings.TrimSpace(in.PinHash)
		in.ActorID = strings.TrimSpace(in.ActorID)

		switch in.Op {
		case "create", "set_pin", "set_active", "set_role":
		default:
			writeSyncUserError(w, http.StatusBadRequest, "invalid_op", "unknown op")
			return
		}
		// audit_log.actor_id FKs onto users: the actor must be a real user
		// here, which it is for any operator the admin bundle delivered. Its
		// row -- the MAIN till's, never the additional till's mirror -- is
		// what authorizeUserChange decides with below.
		if in.ActorID == "" {
			writeSyncUserError(w, http.StatusBadRequest, "unknown_actor", "actor_id required")
			return
		}
		actor, found, err := repo.GetUser(ctx, in.ActorID)
		if err != nil {
			logging.L().Errorf("sync users %s from %s: actor lookup: %v", in.Op, till.Name, err)
			writeSyncUserError(w, http.StatusInternalServerError, "server_error", "server error")
			return
		}
		if !found {
			writeSyncUserError(w, http.StatusBadRequest, "unknown_actor", "unknown actor")
			return
		}
		if in.PinHash != "" && auth.ValidatePINHash(in.PinHash) != nil {
			writeSyncUserError(w, http.StatusBadRequest, "invalid_pin_hash", "pin_hash is not a valid PIN hash")
			return
		}

		now := time.Now().UTC().Format(time.RFC3339)
		provenance := func(extra map[string]any) map[string]any {
			p := map[string]any{"via": "till-sync", "till": till.Name}
			for k, v := range extra {
				p[k] = v
			}
			return p
		}
		// authorize refuses (and answers) a change the actor may not make.
		authorize := func(target data.UserRow, newRole string) bool {
			ok, err := authorizeUserChange(ctx, repo, actor, in.Op, target, newRole)
			if err != nil {
				logging.L().Errorf("sync users %s from %s: permission check: %v", in.Op, till.Name, err)
				writeSyncUserError(w, http.StatusInternalServerError, "server_error", "server error")
				return false
			}
			if !ok {
				logging.L().Infof("sync users: %s by %s (%s) refused as forbidden, from %s", in.Op, actor.ID, actor.Role, till.Name)
				writeSyncUserForbidden(w)
				return false
			}
			return true
		}
		serverError := func(step string, err error) {
			logging.L().Errorf("sync users %s for %s from %s: %s: %v", in.Op, in.UserID, till.Name, step, err)
			writeSyncUserError(w, http.StatusInternalServerError, "server_error", "server error")
		}
		answer := func(id string) {
			u, found, err := repo.GetUser(ctx, id)
			if err != nil || !found {
				serverError("read back", err)
				return
			}
			writeSyncOrdersJSON(w, http.StatusOK, userToSyncRow(u), nil)
		}

		if in.Op == "create" {
			if !isAssignableUserRole(in.Role) {
				writeSyncUserError(w, http.StatusBadRequest, "invalid_role", "invalid role")
				return
			}
			if in.Username == "" || in.DisplayName == "" {
				writeSyncUserError(w, http.StatusBadRequest, "required", "username and display_name required")
				return
			}
			if !authorize(data.UserRow{}, in.Role) {
				return
			}
			if taken, err := repo.UsernameTaken(ctx, in.Username); err != nil {
				serverError("username check", err)
				return
			} else if taken {
				writeSyncUserError(w, http.StatusConflict, "username_taken", "username taken")
				return
			}
			id, err := repo.CreateUser(ctx, in.Username, in.DisplayName, in.Role)
			if err != nil {
				serverError("create", err)
				return
			}
			if in.PinHash != "" {
				if err := repo.SetUserPIN(ctx, id, in.PinHash); err != nil {
					serverError("set pin", err)
					return
				}
			}
			_ = posRepo.InsertAudit(ctx, nil, in.ActorID, "user", id, "user_create", provenance(nil), now, "")
			logging.L().Infof("sync users: created user %s (%s) from %s (ADR-0115)", id, in.Role, till.Name)
			answer(id)
			return
		}

		// set_pin / set_active / set_role act on an existing user.
		if in.UserID == "" {
			writeSyncUserError(w, http.StatusBadRequest, "required", "user_id required")
			return
		}
		target, found, err := repo.GetUser(ctx, in.UserID)
		if err != nil {
			serverError("target lookup", err)
			return
		}
		if !found {
			writeSyncUserError(w, http.StatusNotFound, "not_found", "user not found")
			return
		}
		// 'system' is never an operator (canManage); 'kiosk' is the PIN-less
		// self-order identity whose role must never change (users page).
		if target.ID == "system" || (target.ID == "kiosk" && in.Op == "set_role") {
			writeSyncUserError(w, http.StatusForbidden, "protected_user", "this user cannot be changed")
			return
		}
		if !authorize(target, in.Role) {
			return
		}

		switch in.Op {
		case "set_pin":
			if in.PinHash == "" {
				writeSyncUserError(w, http.StatusBadRequest, "invalid_pin_hash", "pin_hash required")
				return
			}
			if err := repo.SetUserPIN(ctx, target.ID, in.PinHash); err != nil {
				serverError("set pin", err)
				return
			}
			// A changed credential invalidates existing sessions.
			_ = repo.RevokeUserSessions(ctx, target.ID)
			_ = posRepo.InsertAudit(ctx, nil, in.ActorID, "user", target.ID, "user_pin_set", provenance(nil), now, "")

		case "set_active":
			if in.Active == nil {
				writeSyncUserError(w, http.StatusBadRequest, "required", "active required")
				return
			}
			activate := *in.Active
			action := "user_activate"
			if !activate {
				action = "user_deactivate"
			}
			// Guard, write and audit in one transaction: the last-admin
			// count must see every change committed before it.
			tx, err := d.Db.BeginTx(ctx, nil)
			if err != nil {
				serverError("begin tx", err)
				return
			}
			defer tx.Rollback()
			if refused, err := repo.SetUserActiveGuarded(ctx, tx, target.ID, activate); err != nil {
				serverError("set active", err)
				return
			} else if refused != "" {
				writeSyncUserLastPrivileged(w, refused)
				return
			}
			if err := posRepo.InsertAudit(ctx, tx, in.ActorID, "user", target.ID, action, provenance(nil), now, ""); err != nil {
				serverError("audit", err)
				return
			}
			if err := tx.Commit(); err != nil {
				serverError("commit", err)
				return
			}
			if !activate {
				_ = repo.RevokeUserSessions(ctx, target.ID)
			}

		case "set_role":
			if !isAssignableUserRole(in.Role) {
				writeSyncUserError(w, http.StatusBadRequest, "invalid_role", "invalid role")
				return
			}
			if in.Role == target.Role {
				answer(target.ID) // already there -- a no-op, not an error
				return
			}
			// Guard, write and audit in one transaction (see set_active).
			tx, err := d.Db.BeginTx(ctx, nil)
			if err != nil {
				serverError("begin tx", err)
				return
			}
			defer tx.Rollback()
			if refused, err := repo.SetUserRoleGuarded(ctx, tx, target.ID, in.Role); err != nil {
				serverError("set role", err)
				return
			} else if refused != "" {
				writeSyncUserLastPrivileged(w, refused)
				return
			}
			if err := posRepo.InsertAudit(ctx, tx, in.ActorID, "user", target.ID, "user_role_changed",
				provenance(map[string]any{"from": target.Role, "to": in.Role}), now, ""); err != nil {
				serverError("audit", err)
				return
			}
			if err := tx.Commit(); err != nil {
				serverError("commit", err)
				return
			}
			// A changed privilege boundary invalidates sessions.
			_ = repo.RevokeUserSessions(ctx, target.ID)
		}
		logging.L().Infof("sync users: %s applied to user %s from %s (ADR-0115)", in.Op, target.ID, till.Name)
		answer(target.ID)
	})
}
