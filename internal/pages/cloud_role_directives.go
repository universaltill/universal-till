package pages

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// The main till's apply of the cloud custom-role directives save_role and
// delete_role (ADR-0128 §2/§3, ut-docs#3165). Custom roles are created and
// edited in my.universaltill.com; the till enforces them offline but never
// creates them itself. Each apply is one BEGIN IMMEDIATE transaction (the
// DSN's _txlock=immediate) through data.AuthRepo, audited under actor
// "system" with provenance {"via":"cloud","actor":<created_by>}. The admin
// bundle carries the result to every other till; ApplyAdmin prunes a
// deleted one there (ADR-0128 §6).
//
// Failure texts are owner-readable sentences, shown verbatim by the cloud.

const (
	msgRoleBuiltIn          = "Built-in roles are changed on the till."
	msgRoleBadKey           = "The role key %s is not a custom role key."
	msgRoleBadLabel         = "The role name must be 1 to 40 characters."
	msgRoleLabelControl     = "The role name can't contain control characters."
	msgRoleLabelTaken       = "A role called %s already exists."
	msgRoleUnknownAction    = "Action %s isn't on this till. Reload the roles page and save again."
	msgRolePermissionMgmt   = "Permission management can't be given to a custom role."
	msgRoleMissing          = "Role %s does not exist on the main till."
	msgRoleStillAssigned    = "%s is still assigned to %d people. Give them another role first."
	roleLabelMaxRunes       = 40
	permissionManagementAct = "permission_management"
)

// customRoleKey is a cloud-minted key: c_ + a lowercase Crockford base32
// ULID (ADR-0128 §2). The prefix cannot collide with a built-in role or
// identity name.
var customRoleKey = regexp.MustCompile(`^c_[0-9a-hjkmnp-tv-z]{26}$`)

// builtInRoleKeys are the seeded roles. Any origin='builtin' row counts as
// built-in too (a newer replica's migration-seeded role).
var builtInRoleKeys = []string{"cashier", "manager", "admin", "super_admin"}

func cloudSaveRole(ctx context.Context, d *common.Deps, r cloudsync.RoleDirective) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	if err := checkCustomRoleKey(ctx, d, r.Role); err != nil {
		return "", err
	}
	label := strings.TrimSpace(r.Label)
	if n := utf8.RuneCountInString(label); n < 1 || n > roleLabelMaxRunes {
		return "", errors.New(msgRoleBadLabel)
	}
	// Cf too: a zero-width space, BOM or bidi override would let "Admin\u200b"
	// render as "Admin" while slipping past the built-in label check
	// (ADR-0128 §2; #3165 review).
	if strings.IndexFunc(label, func(r rune) bool { return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) }) >= 0 {
		return "", errors.New(msgRoleLabelControl)
	}
	grants := make([]string, 0, len(r.Grants))
	for _, g := range r.Grants {
		if g = strings.TrimSpace(g); g != "" && !slices.Contains(grants, g) {
			grants = append(grants, g)
		}
	}
	if slices.Contains(grants, permissionManagementAct) {
		return "", errors.New(msgRolePermissionMgmt)
	}

	repo := data.NewAuthRepo(d.Db)
	tx, err := d.Db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	cur, found, err := repo.GetRoleTx(ctx, tx, r.Role)
	if err != nil {
		return "", err
	}
	if found && cur.Origin != data.RoleOriginCloud {
		return "", errors.New(msgRoleBuiltIn)
	}
	// create:true on an existing cloud role is a replace: the key is a
	// cloud-minted ULID, so the row can only be this directive's own
	// earlier apply, which Tick repeats after a lost result post.
	if !found && !r.Create {
		return "", fmt.Errorf(msgRoleMissing, r.Role)
	}
	roles, err := repo.ListRolesTx(ctx, tx)
	if err != nil {
		return "", err
	}
	if roleLabelTaken(label, r.Role, roles) {
		return "", fmt.Errorf(msgRoleLabelTaken, label)
	}
	actions, err := repo.ListActionsTx(ctx, tx)
	if err != nil {
		return "", err
	}
	for _, g := range grants {
		if !slices.Contains(actions, g) {
			// Never a partial set (manage-shop-catalog-api.md §0 rule 10).
			return "", fmt.Errorf(msgRoleUnknownAction, g)
		}
	}
	before, err := repo.RoleGrantsTx(ctx, tx, r.Role)
	if err != nil {
		return "", err
	}
	after := slices.Clone(grants)
	slices.Sort(after)
	if found && cur.Label == label && slices.Equal(before, after) && roleHasEveryActionRow(ctx, repo, tx, r.Role, len(actions)) {
		return "role " + label + " already up to date", nil
	}
	if err := repo.UpsertCloudRoleTx(ctx, tx, r.Role, label); err != nil {
		return "", err
	}
	if err := repo.ReplaceRoleGrantsTx(ctx, tx, r.Role, grants); err != nil {
		return "", err
	}
	action, result := "cloud_role_created", "created role "+label
	if found {
		action, result = "cloud_role_saved", "updated role "+label
	}
	if err := auditCloudRole(ctx, d, tx, r, action, label, before, after); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	logging.L().Infof("cloudsync: save_role applied to role %s (%d grants)", r.Role, len(after))
	return result, nil
}

// roleHasEveryActionRow reports whether role has one role_permissions row
// per known action — ReplaceRoleGrantsTx's shape. An action installed
// since the last apply has no row yet; HasPermission treats that as deny,
// so it is only cosmetic, but a re-apply then writes it.
func roleHasEveryActionRow(ctx context.Context, repo *data.AuthRepo, tx *sql.Tx, role string, actions int) bool {
	n, err := repo.CountRoleGrantRowsTx(ctx, tx, role)
	return err == nil && n == actions
}

func cloudDeleteRole(ctx context.Context, d *common.Deps, r cloudsync.RoleDirective) (string, error) {
	if err := requirePrimaryDirective(ctx, d); err != nil {
		return "", err
	}
	if err := checkCustomRoleKey(ctx, d, r.Role); err != nil {
		return "", err
	}
	repo := data.NewAuthRepo(d.Db)
	tx, err := d.Db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()
	cur, found, err := repo.GetRoleTx(ctx, tx, r.Role)
	if err != nil {
		return "", err
	}
	if !found {
		return "role " + r.Role + " already deleted", nil
	}
	if cur.Origin != data.RoleOriginCloud {
		return "", errors.New(msgRoleBuiltIn)
	}
	label := cur.Label
	if label == "" {
		label = cur.Role
	}
	// Any user row, active or not: a deactivated holder would otherwise be
	// reactivated into a missing role. Names are never listed.
	held, err := repo.CountUsersWithRoleTx(ctx, tx, r.Role)
	if err != nil {
		return "", err
	}
	if held > 0 {
		return "", fmt.Errorf(msgRoleStillAssigned, label, held)
	}
	before, err := repo.RoleGrantsTx(ctx, tx, r.Role)
	if err != nil {
		return "", err
	}
	if err := repo.DeleteCloudRoleTx(ctx, tx, r.Role); err != nil {
		return "", err
	}
	if err := auditCloudRole(ctx, d, tx, r, "cloud_role_deleted", label, before, []string{}); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	logging.L().Infof("cloudsync: delete_role applied to role %s", r.Role)
	return "deleted role " + label, nil
}

// checkCustomRoleKey refuses a built-in role (by seeded name or by an
// origin='builtin' row) with the built-in sentence, and any other key that
// is not the cloud-minted c_<ULID> shape.
func checkCustomRoleKey(ctx context.Context, d *common.Deps, role string) error {
	if slices.Contains(builtInRoleKeys, role) {
		return errors.New(msgRoleBuiltIn)
	}
	origin, found, err := data.NewAuthRepo(d.Db).RoleOrigin(ctx, role)
	if err != nil {
		return err
	}
	if found && origin != data.RoleOriginCloud {
		return errors.New(msgRoleBuiltIn)
	}
	if !customRoleKey.MatchString(role) {
		return fmt.Errorf(msgRoleBadKey, role)
	}
	return nil
}

// roleLabelTaken applies ADR-0128 §2's uniqueness rule, case-insensitively:
// a label may not equal another custom role's label, a built-in role's key,
// or a built-in role's users.role.<key> label in any loaded locale
// (language packs and shop overrides included, via httpx.T) — so "Admin"
// can't be faked in any language the till shows. self is the role being
// saved: its own current label is no collision.
func roleLabelTaken(label, self string, roles []data.RoleInfo) bool {
	builtIn := slices.Clone(builtInRoleKeys)
	for _, ri := range roles {
		if ri.Role == self {
			continue
		}
		if ri.Origin == data.RoleOriginCloud {
			if strings.EqualFold(label, strings.TrimSpace(ri.Label)) {
				return true
			}
			continue
		}
		if !slices.Contains(builtIn, ri.Role) {
			builtIn = append(builtIn, ri.Role)
		}
	}
	locales := httpx.AvailableLocales()
	for _, key := range builtIn {
		if strings.EqualFold(label, key) || strings.EqualFold(label, strings.ReplaceAll(key, "_", " ")) {
			return true
		}
		i18nKey := "users.role." + key
		for _, loc := range locales {
			if v := strings.TrimSpace(httpx.T(loc, i18nKey)); v != i18nKey && strings.EqualFold(label, v) {
				return true
			}
		}
	}
	return false
}

func auditCloudRole(ctx context.Context, d *common.Deps, tx *sql.Tx, r cloudsync.RoleDirective, action, label string, before, after []string) error {
	payload := map[string]any{
		"via": "cloud", "actor": r.CreatedBy, "directive_id": r.DirectiveID,
		"label": label, "grants_before": before, "grants_after": after,
	}
	now := time.Now().UTC().Format(time.RFC3339)
	return data.NewPOSRepo(d.Db).InsertAudit(ctx, tx, "system", "role", r.Role, action, payload, now, "")
}
