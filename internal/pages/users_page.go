package pages

import (
	"fmt"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// registerUsers wires the operator admin page (docs: architecture/pos-auth.md).
// Manager/admin only; managers can only manage cashiers, 'system' is never
// editable, and the last active admin with a PIN cannot be deactivated.
//
// ut-docs#795 / ADR-0052 §2: the 4 mutating, audit-writing handlers below
// (create, pin, active, role) moved off a flat requireManager 403 onto
// checkOrElevate — a denied caller now gets the in-place manager-override
// PIN prompt instead, mirroring eod_api.go's registerEODAPI (ut-docs#794)
// exactly: dual-attribution audit via InsertAudit/InsertAuditElevated,
// Hidden fields replaying the rest of the form on retry, and a real
// response body on the elevated branch (a 204 never swaps under htmx, not
// even the dialog's own OOB retry). GET /users (read-only) is deliberately
// left on the flat requireManager 403 — checkOrElevate's own doc comment
// and ADR-0052 §2 scope elevation to mutations only.
//
// The "only admins change managers/admins" business rule (both here and in
// POST /api/users below) and canManage are evaluated against the RESOLVED
// actor (the approver once elevated, the session user otherwise) via
// resolveActingUser — see that function's own doc comment for why. The
// role=="super_admin"/target.Role=="super_admin" branches that gate on
// canPerform(d, r, "permission_management") are explicitly OUT of this
// card's scope (ut-docs#796 covers permission_management) and are left
// exactly as they were: still reading the SESSION user via canPerform,
// never re-evaluated against a resolved/elevated actor.
func registerUsers(mux *http.ServeMux, d *common.Deps, svc *auth.Service) {
	repo := svc.Repo()
	posRepo := data.NewPOSRepo(d.Db)

	// requireManager gates on the user_management action (039's catalog),
	// not the old IsManager() bit — granted to manager/admin/super_admin,
	// same set IsManager() plus super_admin recognized (ut-docs#556: without
	// this, super_admin can never reach /users at all, so the Permissions
	// link this page also carries would be dead — a super_admin session
	// 403'd here before ever seeing it). Only GET /users still uses this —
	// ut-docs#795 moved every mutating handler onto checkOrElevate instead.
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "user_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required") // page-error:allow ut-docs#1458 (pending migration to httpx.RenderError — tracked follow-up card, out of #1455's scope)
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// canManage: admins and super_admins manage everyone but 'system';
	// managers only cashiers. actor is the RESOLVED acting user (see
	// resolveActingUser below), not necessarily the session user.
	canManage := func(actor auth.User, target data.UserRow) bool {
		if target.ID == "system" {
			return false
		}
		if actor.Role == "admin" || actor.Role == "super_admin" {
			return true
		}
		return target.Role == "cashier"
	}

	// resolveActingUser resolves the user whose ROLE governs canManage and
	// the "only admins change managers/admins" rule, once checkOrElevate
	// has decided who's actually performing the write.
	//
	//   - allowed: the session user — unchanged from before this card
	//     (auth.FromContext is exactly what requireManager used to read).
	//   - elevated: the APPROVER, not the still-blocked session user.
	//     canManage encodes a role-hierarchy business rule about who is
	//     ALLOWED to manage whom — a different question from
	//     user_management's own "can this actor touch the feature at all"
	//     gate checkOrElevate already answered. Once elevated, the approver
	//     is who's actually performing the write (checkOrElevate's own
	//     svc.Can re-check already treats them that way for "can perform
	//     this action at all"), so canManage must follow suit for "which
	//     targets can they act on" — using the blocked session actor's role
	//     here instead would let a cashier's own tighter scope leak through
	//     even after a legitimate admin approved the write, defeating the
	//     point of elevating in the first place.
	resolveActingUser := func(r *http.Request, elev elevationCheck) (auth.User, error) {
		if elev.Outcome != elevated {
			u, _ := auth.FromContext(r.Context())
			return u, nil
		}
		row, found, err := repo.GetUser(r.Context(), elev.ApproverID)
		if err != nil {
			return auth.User{}, err
		}
		if !found {
			return auth.User{}, fmt.Errorf("approver %s not found", elev.ApproverID)
		}
		return auth.User{ID: row.ID, Role: row.Role}, nil
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "user", targetID, action, nil, now, "")
	}

	// auditElevated mirrors audit() above but for the elevated branch:
	// dual attribution (approver + originally-blocked session user), same
	// InsertAuditElevated shape eod_api.go's own sites use, and the SAME
	// audit action-string constants as audit() so nothing downstream
	// (audit log viewers, tests) has to distinguish the two provenances.
	auditElevated := func(r *http.Request, actorID, blockedActorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAuditElevated(r.Context(), nil, actorID, blockedActorID, "user", targetID, action, nil, now, "")
	}

	renderUsers := func(w http.ResponseWriter, r *http.Request, actor auth.User, errKey string) {
		users, err := repo.ListUsers(r.Context())
		if err != nil {
			http.Error(w, "failed to load users", http.StatusInternalServerError) // page-error:allow ut-docs#1458 (pending migration to httpx.RenderError — tracked follow-up card, out of #1455's scope)
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
			"menuItems": d.MenuSnapshot(),
			"users":     rows,
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
		_ = r.ParseForm()
		username, display, role := r.PostFormValue("username"), r.PostFormValue("display_name"), r.PostFormValue("role")
		if role != "cashier" && role != "manager" && role != "admin" && role != "super_admin" {
			usersRespondError(w, r, "users.error.role")
			return
		}
		if username == "" || display == "" {
			usersRespondError(w, r, "users.error.required")
			return
		}
		// Mutating + audit-writing (ut-docs#795): the format/required-field
		// checks above need no actor at all, so they run BEFORE
		// checkOrElevate — eod_api.go's own precedent ("don't burn a PIN
		// entry/shared-device lockout slot on a request that's refused
		// either way"). Hidden replays username/display_name/role so the
		// dialog's retry resubmits the SAME new-user form, not a blank one.
		elev := checkOrElevate(d, r, "user_management", r.PostFormValue("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, "/api/users", "#new-user-msg",
				fmt.Sprintf(httpx.T(locale, "elevation.summary.user_create"), display, userRoleLabel(locale, role)),
				[]elevationHiddenField{
					{Name: "username", Value: username},
					{Name: "display_name", Value: display},
					{Name: "role", Value: role},
				}, elev)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}
		if role == "super_admin" {
			// Creating a super_admin is at least as sensitive as anything
			// permission_management gates (ut-docs#761, mirrors 047's own
			// migration comment) — deliberately its own branch rather than
			// falling into the "admins create managers/admins" rule below.
			// OUT OF SCOPE for this card (ut-docs#796): unchanged from
			// before — still reads the SESSION user via canPerform, never
			// the resolved/elevated actor (see this file's package note).
			if !canPerform(d, r, "permission_management") {
				http.Error(w, "super_admin required", http.StatusForbidden)
				return
			}
		} else {
			actingUser, err := resolveActingUser(r, elev)
			if err != nil {
				http.Error(w, "failed to resolve acting user", http.StatusInternalServerError)
				return
			}
			// super_admin included (ut-docs#761 review finding 3) — it's
			// the top of the role hierarchy and must be able to do at
			// least what a plain admin can.
			if actingUser.Role != "admin" && actingUser.Role != "super_admin" && role != "cashier" {
				http.Error(w, "only admins create managers or admins", http.StatusForbidden)
				return
			}
		}
		id, err := repo.CreateUser(r.Context(), username, display, role)
		if err != nil {
			// Genuinely ambiguous (could be a duplicate-username UNIQUE
			// violation — a business rule — or a real DB failure), unlike
			// the role_change branches below which are unambiguous
			// infrastructure failures — logged either way so a real
			// failure here isn't invisible server-side too (ut-docs#795
			// review S-NEW: a 200-with-message response must not also be a
			// silent one for genuine errors).
			logging.L().Errorf("create user %q: %v", username, err)
			usersRespondError(w, r, "users.error.create")
			return
		}
		if elev.Outcome == elevated {
			auditElevated(r, actorID, elev.ActorID, id, "user_create")
			usersRespondOK(w, r, "elevation.approved")
			return
		}
		audit(r, actorID, id, "user_create")
		usersRespondOK(w, r, "users.saved")
	})

	mux.HandleFunc("POST /api/users/{id}/pin", func(w http.ResponseWriter, r *http.Request) {
		// Existence check needs no actor at all — validate BEFORE
		// checkOrElevate (eod_api.go's print/{period} precedent: don't burn
		// a PIN entry/shared-device lockout slot on a request that's
		// refused either way). canManage still runs below, once the
		// RESOLVED actor is known (see resolveActingUser).
		target, found, err := repo.GetUser(r.Context(), r.PathValue("id"))
		if err != nil || !found {
			http.Error(w, "cannot manage this user", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		pin := r.PostFormValue("pin")
		if auth.ValidatePINFormat(pin) != nil {
			usersRespondError(w, r, "auth.error.pin_format")
			return
		}
		// Mutating + audit-writing (ut-docs#795): the PIN-format check
		// above needs no actor either, so it too runs before
		// checkOrElevate. Hidden replays "pin" so the dialog's retry
		// resubmits the SAME new PIN, not a blank one.
		elev := checkOrElevate(d, r, "user_management", r.PostFormValue("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, r.URL.Path, "#user-msg-"+target.ID,
				fmt.Sprintf(httpx.T(locale, "elevation.summary.user_pin_set"), target.DisplayName),
				[]elevationHiddenField{{Name: "pin", Value: pin}}, elev)
			return
		}
		actingUser, err := resolveActingUser(r, elev)
		if err != nil || !canManage(actingUser, target) {
			http.Error(w, "cannot manage this user", http.StatusForbidden)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}
		// PIN-only login: the PIN must identify exactly one operator.
		if owner, taken, err := svc.FindUserByPIN(r.Context(), pin); err == nil && taken && owner.ID != target.ID {
			usersRespondError(w, r, "users.error.pin_taken")
			return
		} else if err != nil {
			http.Error(w, "pin check failed", http.StatusInternalServerError)
			return
		}
		hash, err := auth.HashPIN(pin)
		if err != nil {
			usersRespondError(w, r, "auth.error.pin_format")
			return
		}
		if err := repo.SetUserPIN(r.Context(), target.ID, hash); err != nil {
			http.Error(w, "failed to set pin", http.StatusInternalServerError)
			return
		}
		// A changed credential invalidates existing sessions.
		_ = repo.RevokeUserSessions(r.Context(), target.ID)
		if elev.Outcome == elevated {
			auditElevated(r, actorID, elev.ActorID, target.ID, "user_pin_set")
			usersRespondOK(w, r, "elevation.approved")
			return
		}
		audit(r, actorID, target.ID, "user_pin_set")
		usersRespondOK(w, r, "users.saved")
	})

	mux.HandleFunc("POST /api/users/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		target, found, err := repo.GetUser(r.Context(), r.PathValue("id"))
		if err != nil || !found {
			http.Error(w, "cannot manage this user", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		activateRaw := r.PostFormValue("active")
		activate := activateRaw == "1"
		// Mutating + audit-writing (ut-docs#795): Hidden replays "active"
		// so the dialog's retry resubmits the SAME activate/deactivate
		// request, not a default one.
		elev := checkOrElevate(d, r, "user_management", r.PostFormValue("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			summaryKey := "elevation.summary.user_deactivate"
			if activate {
				summaryKey = "elevation.summary.user_activate"
			}
			renderElevationPrompt(w, r, r.URL.Path, "#user-msg-"+target.ID,
				fmt.Sprintf(httpx.T(locale, summaryKey), target.DisplayName),
				[]elevationHiddenField{{Name: "active", Value: activateRaw}}, elev)
			return
		}
		actingUser, err := resolveActingUser(r, elev)
		if err != nil || !canManage(actingUser, target) {
			http.Error(w, "cannot manage this user", http.StatusForbidden)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}
		if !activate && target.Role == "admin" {
			others, err := repo.CountOtherActiveAdminsWithPIN(r.Context(), target.ID)
			if err != nil || others == 0 {
				usersRespondError(w, r, "users.error.last_admin")
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
				usersRespondError(w, r, "users.error.last_super_admin")
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
		if elev.Outcome == elevated {
			auditElevated(r, actorID, elev.ActorID, target.ID, action)
			usersRespondOK(w, r, "elevation.approved")
			return
		}
		audit(r, actorID, target.ID, action)
		usersRespondOK(w, r, "users.saved")
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
	//
	// OUT OF SCOPE for ut-docs#795: permission_management is a different
	// action, tracked by a different card (ut-docs#796) — this handler is
	// untouched, still a flat canPerform/403, no elevation wiring.
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

	// POST /api/users/{id}/role closes the ut-docs#766 gap left by
	// promote-super-admin above: that path can only ever move a user *to*
	// super_admin, so there was no in-app way to demote one (or change any
	// other user's role) — the only lever was deactivation, a coarser
	// action that also drops the user's login/PIN/history association.
	// General any-role-to-any-role endpoint rather than a second
	// single-purpose "demote" mirror of promote-super-admin: POST
	// /api/users above already has to validate+gate every role a user can
	// be *created* with, so reusing that same shape for role *changes*
	// covers promotion, demotion and lateral moves with one gate instead
	// of one handler per direction.
	mux.HandleFunc("POST /api/users/{id}/role", func(w http.ResponseWriter, r *http.Request) {
		target, found, err := repo.GetUser(r.Context(), r.PathValue("id"))
		if err != nil || !found {
			http.Error(w, "user not found", http.StatusNotFound)
			return
		}
		// Same service-identity exclusion as promote-super-admin: 'kiosk'
		// (migration 018) is a PIN-less identity reachable by any anonymous
		// LAN client via the auth-exempt /self-order surface, so changing
		// its role — up *or* down — is a real privilege-escalation path,
		// not a theoretical one. canManage already blocks 'system'. Needs
		// no actor, so it runs before checkOrElevate too.
		if target.ID == "kiosk" {
			http.Error(w, "cannot change this user's role", http.StatusForbidden)
			return
		}
		_ = r.ParseForm()
		newRole := r.PostFormValue("role")
		if newRole != "cashier" && newRole != "manager" && newRole != "admin" && newRole != "super_admin" {
			usersRespondError(w, r, "users.error.role")
			return
		}
		if newRole == target.Role {
			// Already there — a no-op, not an error (mirrors
			// promote-super-admin's own already-there branch) — resolved
			// before spending a PIN entry on an elevation prompt that
			// would end up doing nothing anyway.
			usersRespondOK(w, r, "users.saved")
			return
		}
		// Mutating + audit-writing (ut-docs#795): everything above
		// (existence, kiosk exclusion, role format, no-op) is structural
		// and needs no actor — eod_api.go's own precedent for validating
		// before checkOrElevate. Hidden replays "role" so the dialog's
		// retry resubmits the SAME requested role.
		elev := checkOrElevate(d, r, "user_management", r.PostFormValue("override_pin"))
		if elev.Outcome == needsElevation {
			locale := httpx.ResolveLocale(w, r)
			renderElevationPrompt(w, r, r.URL.Path, "#user-msg-"+target.ID,
				fmt.Sprintf(httpx.T(locale, "elevation.summary.user_role_change"), target.DisplayName, userRoleLabel(locale, target.Role), userRoleLabel(locale, newRole)),
				[]elevationHiddenField{{Name: "role", Value: newRole}}, elev)
			return
		}
		actingUser, err := resolveActingUser(r, elev)
		if err != nil || !canManage(actingUser, target) {
			http.Error(w, "cannot manage this user", http.StatusForbidden)
			return
		}
		actorID := elev.ActorID
		if elev.Outcome == elevated {
			actorID = elev.ApproverID
		}
		// Gating mirrors POST /api/users' create-time rule exactly, applied
		// symmetrically to both directions of a change: granting *or*
		// removing super_admin is at least as sensitive as anything
		// permission_management gates (ut-docs#761); granting or removing
		// manager/admin needs an admin-or-above actor, same as creating one.
		if newRole == "super_admin" || target.Role == "super_admin" {
			// OUT OF SCOPE for this card (ut-docs#796): unchanged from
			// before — still reads the SESSION user via canPerform, never
			// the resolved/elevated actor (see this file's package note).
			if !canPerform(d, r, "permission_management") {
				http.Error(w, "super_admin required", http.StatusForbidden)
				return
			}
		} else if actingUser.Role != "admin" && actingUser.Role != "super_admin" {
			// The newRole==target.Role return above already guarantees this
			// branch is only reached when at least one side is manager/admin
			// (both being "cashier" would mean they're equal) — so the actor
			// check alone is the whole condition; ut-docs#766 review finding 4.
			http.Error(w, "only admins change managers or admins", http.StatusForbidden)
			return
		}
		// Last-active-with-a-PIN guards: changing the last admin or the
		// last super_admin *away* from that role would strand the till the
		// same way deactivating them would (the /active handler above
		// already guards exactly this for deactivation) — reusing both
		// counters rather than inventing a third guard shape.
		if target.Role == "admin" && newRole != "admin" {
			others, err := repo.CountOtherActiveAdminsWithPIN(r.Context(), target.ID)
			if err != nil || others == 0 {
				usersRespondError(w, r, "users.error.last_admin")
				return
			}
		}
		if target.Role == "super_admin" && newRole != "super_admin" {
			others, err := repo.CountOtherActiveSuperAdminsWithPIN(r.Context(), target.ID)
			if err != nil || others == 0 {
				usersRespondError(w, r, "users.error.last_super_admin")
				return
			}
		}

		// The four branches below are unambiguous infrastructure failures
		// (tx begin/write/commit), not business-rule refusals — unlike
		// usersRespondError's other callers, these must not go silent just
		// because the response itself is a friendly 200 (ut-docs#795
		// review S-NEW: a real DB failure here used to be a 500 an
		// operator saw as a hard error; it's now the same 200 "something
		// went wrong" message as any other refusal, so the log line is
		// what keeps it from vanishing entirely).
		tx, err := d.Db.BeginTx(r.Context(), nil)
		if err != nil {
			logging.L().Errorf("role change %s->%s for user %s: begin tx: %v", target.Role, newRole, target.ID, err)
			usersRespondError(w, r, "users.error.role_change")
			return
		}
		defer tx.Rollback()

		fromRole := target.Role
		if err := repo.SetUserRole(r.Context(), tx, target.ID, newRole); err != nil {
			logging.L().Errorf("role change %s->%s for user %s: set role: %v", fromRole, newRole, target.ID, err)
			usersRespondError(w, r, "users.error.role_change")
			return
		}
		now := time.Now().UTC().Format(time.RFC3339)
		payload := map[string]any{"from": fromRole, "to": newRole, "via": "in-app"}
		if elev.Outcome == elevated {
			err = posRepo.InsertAuditElevated(r.Context(), tx, actorID, elev.ActorID, "user", target.ID, "user_role_changed", payload, now, "")
		} else {
			err = posRepo.InsertAudit(r.Context(), tx, actorID, "user", target.ID, "user_role_changed", payload, now, "")
		}
		if err != nil {
			logging.L().Errorf("role change %s->%s for user %s: audit write: %v", fromRole, newRole, target.ID, err)
			usersRespondError(w, r, "users.error.role_change")
			return
		}
		if err := tx.Commit(); err != nil {
			logging.L().Errorf("role change %s->%s for user %s: commit: %v", fromRole, newRole, target.ID, err)
			usersRespondError(w, r, "users.error.role_change")
			return
		}
		// Same "a changed privilege boundary invalidates sessions"
		// convention as promote-super-admin/deactivate.
		_ = repo.RevokeUserSessions(r.Context(), target.ID)
		if elev.Outcome == elevated {
			usersRespondOK(w, r, "elevation.approved")
			return
		}
		usersRespondOK(w, r, "users.saved")
	})
}

// userRoleLabel translates a raw role identifier ("cashier", "manager", …)
// into the approver's locale for an elevation summary (ut-docs#795 review
// S3 — these used to interpolate the raw Go identifier straight into the
// summary, unreadable in any locale but English). Same
// users.role.%s key and T()-falls-back-to-the-raw-key-if-missing shape
// permission_settings_page.go's own permissionChangeSummary already uses
// for the identical problem on the permissions matrix.
func userRoleLabel(locale, role string) string {
	return httpx.T(locale, fmt.Sprintf("users.role.%s", role))
}

// usersRespondError writes a translated inline error into the request's own
// htmx target (ut-docs#795) in place of the old "/users?err=key" redirect —
// an htmx-swapped POST can't ride a 3xx Location the way a plain full-page
// form submit could, so the message has to travel in THIS response's own
// body instead.
//
// Status is ALWAYS 200 (ut-docs#795 review Blocker 2 — this used to vary,
// 400/409/500 depending on the caller). htmx does NOT swap a non-2xx
// response by default, and this repo's app.js htmx:responseError handler
// replaces it with a generic "server error" banner instead — so a 4xx/5xx
// here would silently hide every one of this page's business-rule
// refusals (role invalid, PIN taken, last admin, …) behind a useless
// message, never the actual reason. permission_settings_page.go's own
// lockout-guard branch already documents this exact trap and the same
// fix (grep its doc comment for "htmx never swaps a non-2xx response").
// The X-UT-Response: refused header (see usersRespondOK and
// renderElevationPrompt in elevation.go) is what users.html's
// hx-on::after-request actually keys off of to skip the reload — NOT the
// status code, which is identical (200) whether this call, usersRespondOK,
// or renderElevationPrompt answered the request.
func usersRespondError(w http.ResponseWriter, r *http.Request, key string) {
	locale := httpx.ResolveLocale(w, r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-UT-Response", "refused")
	fmt.Fprintf(w, `<span class="login-error">%s</span>`, httpx.T(locale, key))
}

// usersRespondOK writes a translated inline confirmation — "elevation.
// approved" for an elevated write (same key eod_api.go's own elevated
// branches already use) or "users.saved" for a plain, already-authorized
// write. Both need a real body, not 204: the elevation dialog's own retry
// <form> targets this same span and has no reload of its own (eod_api.go's
// #794 review finding — a 204 never swaps under htmx, not even via the
// dialog's OOB retry), and the outer row/create form's own reload only
// fires from hx-on::after-request AFTER this response lands.
//
// X-UT-Response: ok (ut-docs#795 review Blocker 1) is the actual reload
// signal — see usersRespondError's doc comment for why Content-Type alone
// (settings.html's report-retention convention) can't do this job on this
// page: a real 200 success here, a 200 refusal (usersRespondError above),
// and the 200 elevation prompt (renderElevationPrompt) all render a real
// HTML body with the SAME Content-Type, unlike settings.html's success
// case (a bare 204, no body, no Content-Type at all).
func usersRespondOK(w http.ResponseWriter, r *http.Request, key string) {
	locale := httpx.ResolveLocale(w, r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-UT-Response", "ok")
	fmt.Fprintf(w, `<span>✓ %s</span>`, httpx.T(locale, key))
}
