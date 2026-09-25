package pages

import (
	"context"

	"github.com/universaltill/universal-till/internal/data"
)

// User-admin invariants shared by the local users page (users_page.go) and
// the main till's write-through endpoint (sync_users.go, ADR-0115 §1), so
// the two can never drift apart: an additional till checks them against its
// mirror, and the main till re-checks them against the shop's real state.

// isAssignableUserRole is the role allow-list a user can be created with or
// moved to.
func isAssignableUserRole(role string) bool {
	switch role {
	case "cashier", "manager", "admin", "super_admin":
		return true
	}
	return false
}

// canManageUser: admins and super_admins manage everyone but 'system';
// managers (and anyone else holding user_management) only cashiers. The
// users page evaluates it against its resolved acting user, the main till
// against the actor row it holds (authorizeUserChange).
func canManageUser(actorRole string, target data.UserRow) bool {
	if target.ID == "system" {
		return false
	}
	if actorRole == "admin" || actorRole == "super_admin" {
		return true
	}
	return target.Role == "cashier"
}

// authorizeUserChange is the main till's re-check of the users page's own
// permission rules (users_page.go, auth_page.go) against the ACTOR ROW THE
// MAIN TILL HOLDS -- an additional till's actor_id is only as strong as the
// role the main till has for it (ut-docs#2755 review blocker). A bearer
// alone never authorizes a privileged change. Returns false to refuse:
//
//   - the actor must be active;
//   - set_pin on the actor's own row is the staffer's own PIN change
//     (auth_page.go's /api/pin/change) and needs no further permission;
//   - everything else needs user_management (the users page's
//     checkOrElevate gate -- an elevated change names the approver, who
//     holds it);
//   - granting super_admin (create, set_role/promote) needs
//     permission_management, the users page's canPerform gate, read from
//     role_permissions for the actor's role. promote-super-admin checks
//     only that on the users page, so it is the whole rule here too;
//   - removing super_admin needs permission_management plus
//     canManageUser, as POST /api/users/{id}/role does;
//   - creating, or moving a user to or from, manager/admin needs an
//     admin or super_admin actor;
//   - set_pin / set_active / set_role need canManageUser(actor, target).
func authorizeUserChange(ctx context.Context, repo *data.AuthRepo, actor data.UserRow, op string, target data.UserRow, newRole string) (bool, error) {
	if !actor.IsActive {
		return false, nil
	}
	if op == "set_pin" && target.ID == actor.ID {
		return true, nil
	}
	if ok, err := repo.HasPermission(ctx, actor.Role, "user_management"); err != nil || !ok {
		return false, err
	}
	isAdmin := actor.Role == "admin" || actor.Role == "super_admin"
	switch op {
	case "create":
		if newRole == "super_admin" {
			return repo.HasPermission(ctx, actor.Role, "permission_management")
		}
		return isAdmin || newRole == "cashier", nil
	case "set_pin", "set_active":
		return canManageUser(actor.Role, target), nil
	case "set_role":
		if newRole == "super_admin" {
			return repo.HasPermission(ctx, actor.Role, "permission_management")
		}
		if !canManageUser(actor.Role, target) {
			return false, nil
		}
		if target.Role == "super_admin" {
			return repo.HasPermission(ctx, actor.Role, "permission_management")
		}
		return isAdmin, nil
	}
	return false, nil
}

// lastPrivilegedUserGuard returns the error key refusing a change that
// would leave the till with no active admin, or no active super_admin, who
// can sign in -- "" when the change is allowed. newRole/active describe the
// target AFTER the change: deactivation passes (target.Role, false), a role
// change passes (newRole, true). Fails closed: a count error refuses, same
// as the users page always has. The users page's local pre-check; the main
// till's write-through endpoint runs the same rule inside the write's own
// transaction (data.AuthRepo.SetUserActiveGuarded / SetUserRoleGuarded).
func lastPrivilegedUserGuard(ctx context.Context, repo *data.AuthRepo, target data.UserRow, newRole string, active bool) string {
	leaves := func(role string) bool { return target.Role == role && (!active || newRole != role) }
	if leaves("admin") {
		others, err := repo.CountOtherActiveAdminsWithPIN(ctx, target.ID)
		if err != nil || others == 0 {
			return "users.error.last_admin"
		}
	}
	// Same guard, super_admin side (ut-docs#761 review finding 4): losing
	// the only super_admin would strand the till with nobody able to reach
	// the permission matrix, audit page or backoffice.
	if leaves("super_admin") {
		others, err := repo.CountOtherActiveSuperAdminsWithPIN(ctx, target.ID)
		if err != nil || others == 0 {
			return "users.error.last_super_admin"
		}
	}
	return ""
}
