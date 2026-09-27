package pages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/directivekey"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// The main till's apply of the cloud user directives save_user,
// set_user_pin and deactivate_user (ut-docs
// reference/till-user-directives.md §4; ADR-0115 §2 and its 2026-09-25
// amendment). Each apply runs in two phases:
//
//  1. Outside any write transaction: the protected-target / role refusals,
//     opening pin_sealed with the till's directive key (AAD = store external
//     id, directive id, type, user id), auth.ValidatePINFormat, PIN
//     uniqueness by the till's own login rule (VerifyPIN against every
//     ACTIVE user with a PIN except the target — auth.Service.FindUserByPIN)
//     and auth.HashPIN. PBKDF2 runs here so SQLite's write lock is never held
//     across it (a sale is never held up by a cloud PIN change); a local PIN
//     set racing in between is ADR-0115 §1's accepted uniqueness residual.
//  2. One BEGIN IMMEDIATE transaction (the DSN's _txlock=immediate) through
//     data.AuthRepo: username uniqueness, the in-transaction last-active-admin
//     guard (SetUserActiveGuarded / SetUserRoleGuarded), the writes, session
//     revocation on a PIN change, reactivation or deactivation, and one audit
//     row (actor "system", provenance {"via":"cloud","actor":<created_by>}).
//
// The PIN, its hash and pin_sealed never reach a log line, a result or error
// message, or an audit row. Failure texts are the contract's owner-readable
// sentences, shown verbatim by the cloud.

// Contract §4 failure sentences.
const (
	msgUserPINInUse          = "This PIN is already in use. Choose a different PIN."
	msgUserReactivateNeedPIN = "Reactivating a user needs a new PIN."
	msgUserUsernameTaken     = "The username %s is already taken."
	msgUserLastAdmin         = "The last active admin can't be deactivated or demoted."
	msgUserProtected         = "Super admins and built-in till users can only be changed at a till."
	msgUserMissing           = "User %s does not exist on the main till."
	msgUserBadRole           = "The role %s can't be set from the cloud."
	msgUserNewNeedsPIN       = "A new user needs a PIN."
	msgUserBadPINFormat      = "The PIN must be 4 to 8 digits."
	msgUserBadUsername       = "The username must be 1 to 64 characters."
	msgUserBadDisplayName    = "The display name can be at most 80 characters."
)

// userDirectivePINHash / userDirectivePINVerify are the PBKDF2 calls of
// phase 1 — seams so a test can probe that no write transaction is open
// while they run.
var (
	userDirectivePINHash   = auth.HashPIN
	userDirectivePINVerify = auth.VerifyPIN
)

// cloudAssignableUserRole is the role allow-list a cloud directive may set:
// the users page's list without super_admin.
func cloudAssignableUserRole(role string) bool {
	return role != "super_admin" && isAssignableUserRole(role)
}

// isBuiltInUser: 'system' is never an operator; 'kiosk' is the PIN-less
// self-order identity. Both stay at a till.
func isBuiltInUser(id string) bool { return id == "system" || id == "kiosk" }

func cloudSaveUser(ctx context.Context, d *common.Deps, keys *directivekey.Store, u cloudsync.UserDirective) (string, error) {
	return applyCloudUserDirective(ctx, d, keys, u)
}

func cloudSetUserPIN(ctx context.Context, d *common.Deps, keys *directivekey.Store, u cloudsync.UserDirective) (string, error) {
	if u.PINSealed == nil {
		return "", errors.New("missing pin_sealed")
	}
	return applyCloudUserDirective(ctx, d, keys, u)
}

func cloudDeactivateUser(ctx context.Context, d *common.Deps, keys *directivekey.Store, u cloudsync.UserDirective) (string, error) {
	return applyCloudUserDirective(ctx, d, keys, u)
}

func applyCloudUserDirective(ctx context.Context, d *common.Deps, keys *directivekey.Store, u cloudsync.UserDirective) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	repo := data.NewAuthRepo(d.Db)

	// ---- Phase 1: no write transaction open. ----
	if isBuiltInUser(u.UserID) {
		return "", errors.New(msgUserProtected)
	}
	target, found, err := repo.GetUser(ctx, u.UserID)
	if err != nil {
		return "", err
	}
	if found && target.Role == "super_admin" {
		return "", errors.New(msgUserProtected)
	}
	if u.Role != nil {
		if *u.Role == "super_admin" {
			return "", errors.New(msgUserProtected)
		}
		if !cloudAssignableUserRole(*u.Role) {
			return "", fmt.Errorf(msgUserBadRole, *u.Role)
		}
	}
	creating := u.Type == "save_user" && u.Create && !found
	if !found && !creating {
		return "", fmt.Errorf(msgUserMissing, u.UserID)
	}
	if u.Username != nil && (*u.Username == "" || utf8.RuneCountInString(*u.Username) > 64) {
		return "", errors.New(msgUserBadUsername)
	}
	if u.DisplayName != nil && utf8.RuneCountInString(*u.DisplayName) > 80 {
		return "", errors.New(msgUserBadDisplayName)
	}
	if creating {
		if u.Username == nil {
			return "", errors.New(msgUserBadUsername)
		}
		if u.Role == nil {
			return "", errors.New("missing role")
		}
		if u.PINSealed == nil {
			return "", errors.New(msgUserNewNeedsPIN)
		}
	}
	if u.Active != nil && *u.Active && u.PINSealed == nil {
		return "", errors.New(msgUserReactivateNeedPIN)
	}
	pinHash := ""
	if u.PINSealed != nil {
		if pinHash, err = sealedPINToHash(ctx, d, repo, keys, u); err != nil {
			return "", err
		}
	}

	// ---- Phase 2: one BEGIN IMMEDIATE transaction. ----
	tx, err := d.Db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	// Re-read under the write lock: phase 1's read may be stale.
	cur, found, err := repo.GetUserTx(ctx, tx, u.UserID)
	if err != nil {
		return "", err
	}
	if found && cur.Role == "super_admin" {
		return "", errors.New(msgUserProtected)
	}
	if !found && !(u.Type == "save_user" && u.Create) {
		return "", fmt.Errorf(msgUserMissing, u.UserID)
	}

	var (
		action  string
		changed []string
		result  string
		revoke  bool
	)
	switch {
	case !found: // create
		// Phase 1 saw the user and skipped the create checks, and it has
		// gone since: nothing safe to create from.
		if u.Username == nil || u.Role == nil || pinHash == "" {
			return "", fmt.Errorf(msgUserMissing, u.UserID)
		}
		if taken, err := repo.UsernameTakenByOther(ctx, tx, *u.Username, u.UserID); err != nil {
			return "", err
		} else if taken {
			return "", fmt.Errorf(msgUserUsernameTaken, *u.Username)
		}
		display := *u.Username
		if u.DisplayName != nil && *u.DisplayName != "" {
			display = *u.DisplayName
		}
		if err := repo.CreateUserWithID(ctx, tx, u.UserID, *u.Username, display, *u.Role); err != nil {
			return "", err
		}
		if err := repo.SetUserPINTx(ctx, tx, u.UserID, pinHash); err != nil {
			return "", err
		}
		action, result = "cloud_user_created", "created user "+*u.Username
		changed = []string{"username", "display_name", "role", "pin"}

	case u.Type == "deactivate_user":
		if !cur.IsActive {
			return "user " + cur.Username + " is already deactivated", nil
		}
		if refused, err := repo.SetUserActiveGuarded(ctx, tx, u.UserID, false); err != nil {
			return "", err
		} else if refused != "" {
			return "", errors.New(msgUserLastAdmin)
		}
		action, result, revoke = "cloud_user_deactivated", "deactivated user "+cur.Username, true
		changed = []string{"active"}

	case u.Type == "set_user_pin":
		if err := repo.SetUserPINTx(ctx, tx, u.UserID, pinHash); err != nil {
			return "", err
		}
		action, result, revoke = "cloud_user_pin_set", "set the PIN of user "+cur.Username, true
		changed = []string{"pin"}

	default: // save_user on an existing user (a create re-applied lands here too)
		name := cur.Username
		var newName, newDisplay *string
		if u.Username != nil && *u.Username != cur.Username {
			if taken, err := repo.UsernameTakenByOther(ctx, tx, *u.Username, u.UserID); err != nil {
				return "", err
			} else if taken {
				return "", fmt.Errorf(msgUserUsernameTaken, *u.Username)
			}
			newName, name = u.Username, *u.Username
			changed = append(changed, "username")
		}
		if u.DisplayName != nil && *u.DisplayName != "" && *u.DisplayName != cur.DisplayName {
			newDisplay = u.DisplayName
			changed = append(changed, "display_name")
		}
		if newName != nil || newDisplay != nil {
			if err := repo.UpdateUserProfile(ctx, tx, u.UserID, newName, newDisplay); err != nil {
				return "", err
			}
		}
		if u.Role != nil && *u.Role != cur.Role {
			if refused, err := repo.SetUserRoleGuarded(ctx, tx, u.UserID, *u.Role); err != nil {
				return "", err
			} else if refused != "" {
				return "", errors.New(msgUserLastAdmin)
			}
			changed = append(changed, "role")
		}
		if u.Active != nil && *u.Active && !cur.IsActive {
			if _, err := repo.SetUserActiveGuarded(ctx, tx, u.UserID, true); err != nil {
				return "", err
			}
			changed = append(changed, "active")
			revoke = true
		}
		if pinHash != "" {
			if err := repo.SetUserPINTx(ctx, tx, u.UserID, pinHash); err != nil {
				return "", err
			}
			changed = append(changed, "pin")
			revoke = true
		}
		if len(changed) == 0 {
			return "user " + name + " already up to date", nil
		}
		action, result = "cloud_user_saved", "updated user "+name
	}
	if revoke {
		if err := repo.RevokeUserSessionsTx(ctx, tx, u.UserID); err != nil {
			return "", err
		}
	}
	// Provenance only — never the PIN, its hash or pin_sealed.
	payload := map[string]any{"via": "cloud", "actor": u.CreatedBy, "directive_id": u.DirectiveID, "changed": changed}
	if u.Role != nil && found && *u.Role != cur.Role {
		payload["from"], payload["to"] = cur.Role, *u.Role
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := data.NewPOSRepo(d.Db).InsertAudit(ctx, tx, "system", "user", u.UserID, action, payload, now, ""); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	logging.L().Infof("cloudsync: %s applied to user %s (%s)", u.Type, u.UserID, strings.Join(changed, ","))
	return result, nil
}

// sealedPINToHash is phase 1's PIN work: open, format check, uniqueness
// against every other active user's PIN, hash. No write transaction may be
// open while it runs.
func sealedPINToHash(ctx context.Context, d *common.Deps, repo *data.AuthRepo, keys *directivekey.Store, u cloudsync.UserDirective) (string, error) {
	storeID := enroll.Effective(d.Cfg).Marketplace.StoreID
	pin, err := keys.OpenPIN(*u.PINSealed, directivekey.AAD(storeID, u.DirectiveID, u.Type, u.UserID))
	if err != nil {
		if errors.Is(err, directivekey.ErrMalformed) {
			return "", errors.New("bad pin_sealed")
		}
		return "", directivekey.ErrKeyGone
	}
	if auth.ValidatePINFormat(pin) != nil {
		return "", errors.New(msgUserBadPINFormat)
	}
	others, err := repo.ListActiveUsersWithPIN(ctx)
	if err != nil {
		return "", err
	}
	for _, o := range others {
		if o.ID != u.UserID && userDirectivePINVerify(pin, o.PinHash) {
			return "", errors.New(msgUserPINInUse)
		}
	}
	hash, err := userDirectivePINHash(pin)
	if err != nil {
		return "", errors.New(msgUserBadPINFormat)
	}
	return hash, nil
}

// cloudUserReport is one row of the main till's `users` config report
// (contract §5) — never pin_hash, never sessions.
type cloudUserReport struct {
	UserID      string `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
	Active      bool   `json:"active"`
	HasPIN      bool   `json:"has_pin"`
}

// remoteUsersReport lists every user except the built-in system and kiosk
// identities, for the cloud's Users & PINs screen. A read error logs and
// returns nil (see configReportGate: the cloud keeps its last copy).
func remoteUsersReport(ctx context.Context, d *common.Deps) []cloudUserReport {
	users, err := data.NewAuthRepo(d.Db).ListUsers(ctx)
	if err != nil {
		logging.L().Warnf("cloudsync: users report: %v", err)
		return nil
	}
	out := make([]cloudUserReport, 0, len(users))
	for _, u := range users {
		if isBuiltInUser(u.ID) {
			continue
		}
		out = append(out, cloudUserReport{UserID: u.ID, Username: u.Username, DisplayName: u.DisplayName, Role: u.Role, Active: u.IsActive, HasPIN: u.PinHash != ""})
	}
	return out
}
