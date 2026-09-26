package data

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// AuthRepo owns the SQL for operator authentication: users' PIN credentials
// and the sessions table (docs: architecture/pos-auth.md).
type AuthRepo struct {
	db *sql.DB
}

func NewAuthRepo(db *sql.DB) *AuthRepo {
	return &AuthRepo{db: db}
}

// UserRow is an operator as auth and the users admin page need it.
type UserRow struct {
	ID          string
	Username    string
	DisplayName string
	Role        string
	PinHash     string
	IsActive    bool
}

// SessionRow is a resolved session joined with its user.
type SessionRow struct {
	SessionID   string
	UserID      string
	Username    string
	DisplayName string
	Role        string
	ExpiresAt   time.Time
	LastSeenAt  time.Time
}

const userCols = `id, username, display_name, role, COALESCE(pin_hash, ''), is_active`

func scanUser(scan func(dest ...any) error) (UserRow, error) {
	var u UserRow
	var active int
	if err := scan(&u.ID, &u.Username, &u.DisplayName, &u.Role, &u.PinHash, &active); err != nil {
		return UserRow{}, err
	}
	u.IsActive = active != 0
	// ut-docs#1610: single scan choke point for ListUsers (the admin page
	// shows retired accounts too), GetUser and ListActiveUsersWithPIN — undo
	// admin-sync's "<username>~<id>" retire mangle before it can be displayed.
	u.Username = stripRetireMangle(u.ID, u.Username)
	return u, nil
}

// ListUsers returns every user (the admin page shows inactive ones too).
func (r *AuthRepo) ListUsers(ctx context.Context) ([]UserRow, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()
	var out []UserRow
	for rows.Next() {
		u, err := scanUser(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListActiveUsersWithPIN returns active users that can log in. PIN-only login
// verifies the entered PIN against each candidate's hash.
func (r *AuthRepo) ListActiveUsersWithPIN(ctx context.Context) ([]UserRow, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+userCols+` FROM users WHERE is_active = 1 AND pin_hash IS NOT NULL AND pin_hash != ''`)
	if err != nil {
		return nil, fmt.Errorf("list login users: %w", err)
	}
	defer rows.Close()
	var out []UserRow
	for rows.Next() {
		u, err := scanUser(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// GetUser returns one user by id.
func (r *AuthRepo) GetUser(ctx context.Context, id string) (UserRow, bool, error) {
	u, err := scanUser(r.db.QueryRowContext(ctx,
		`SELECT `+userCols+` FROM users WHERE id = ?`, id).Scan)
	if err == sql.ErrNoRows {
		return UserRow{}, false, nil
	}
	if err != nil {
		return UserRow{}, false, fmt.Errorf("get user: %w", err)
	}
	return u, true, nil
}

// CreateUser inserts an operator (no PIN yet — set it separately).
func (r *AuthRepo) CreateUser(ctx context.Context, username, displayName, role string) (string, error) {
	id := uuid.NewString()
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO users (id, username, display_name, role, is_active) VALUES (?, ?, ?, ?, 1)`,
		id, username, displayName, role)
	if err != nil {
		return "", fmt.Errorf("create user: %w", err)
	}
	return id, nil
}

// UsernameTaken reports whether a user row already holds username (the
// users.username UNIQUE constraint). The main till checks it before a
// written-through create (ADR-0115 §1) so a duplicate is answered as a
// business refusal rather than a raw constraint error.
func (r *AuthRepo) UsernameTaken(ctx context.Context, username string) (bool, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ?`, username).Scan(&n); err != nil {
		return false, fmt.Errorf("username taken: %w", err)
	}
	return n > 0, nil
}

// MirrorUser lands a row the main till just applied into this till's own
// users table (ADR-0115 §1), so an additional till shows the change at once
// instead of after its next admin-bundle pull -- which would write exactly
// the same values. Insert on a new id (the main till assigned it), update
// on a known one. An empty PinHash means "no hash in this change": NULL on
// insert (a new user has no PIN yet, same as CreateUser), the stored hash
// left alone on update.
func (r *AuthRepo) MirrorUser(ctx context.Context, u UserRow) error {
	active := 0
	if u.IsActive {
		active = 1
	}
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO users (id, username, display_name, role, pin_hash, is_active)
		 VALUES (?, ?, ?, ?, NULLIF(?, ''), ?)
		 ON CONFLICT(id) DO UPDATE SET
		   username = excluded.username,
		   display_name = excluded.display_name,
		   role = excluded.role,
		   pin_hash = COALESCE(excluded.pin_hash, users.pin_hash),
		   is_active = excluded.is_active`,
		u.ID, u.Username, u.DisplayName, u.Role, u.PinHash, active)
	if err != nil {
		return fmt.Errorf("mirror user %s: %w", u.ID, err)
	}
	return nil
}

// SetUserPIN stores a new PIN hash for the user.
func (r *AuthRepo) SetUserPIN(ctx context.Context, userID, pinHash string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE users SET pin_hash = ? WHERE id = ?`, pinHash, userID)
	if err != nil {
		return fmt.Errorf("set pin: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set pin: user %s not found", userID)
	}
	return nil
}

// SetUserActive toggles an operator. Deactivating must also revoke sessions —
// callers use RevokeUserSessions in the same flow.
func (r *AuthRepo) SetUserActive(ctx context.Context, userID string, active bool) error {
	v := 0
	if active {
		v = 1
	}
	res, err := r.db.ExecContext(ctx, `UPDATE users SET is_active = ? WHERE id = ?`, v, userID)
	if err != nil {
		return fmt.Errorf("set active: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set active: user %s not found", userID)
	}
	return nil
}

// SetUserRole updates a user's role. Runs inside the given tx when
// non-nil (the caller journals the change in the same transaction, same
// convention as SetRolePermission), or directly against the DB otherwise.
// Callers are responsible for authorizing the change and writing the audit
// entry — see users_page.go's super_admin-promotion handler and
// scripts/promote-super-admin's bootstrap CLI (ut-docs#761), the two paths
// that share this method.
func (r *AuthRepo) SetUserRole(ctx context.Context, tx *sql.Tx, userID, role string) error {
	q := `UPDATE users SET role = ? WHERE id = ?`
	var res sql.Result
	var err error
	if tx != nil {
		res, err = tx.ExecContext(ctx, q, role, userID)
	} else {
		res, err = r.db.ExecContext(ctx, q, role, userID)
	}
	if err != nil {
		return fmt.Errorf("set user role: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set user role: user %s not found", userID)
	}
	return nil
}

// CountUsersByRole reports how many users currently hold role. Used by the
// super_admin bootstrap CLI (scripts/promote-super-admin, ut-docs#761) to
// refuse re-bootstrapping over an existing super_admin without an explicit
// --force, so the CLI can't quietly become a standing backdoor next to the
// in-app promotion path.
func (r *AuthRepo) CountUsersByRole(ctx context.Context, role string) (int, error) {
	var n int
	if err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = ?`, role).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users by role: %w", err)
	}
	return n, nil
}

// CountOtherActiveAdminsWithPIN counts active admins with a PIN besides the
// given user — guards "cannot deactivate the last admin".
func (r *AuthRepo) CountOtherActiveAdminsWithPIN(ctx context.Context, excludeUserID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users
		 WHERE is_active = 1 AND role = 'admin' AND pin_hash IS NOT NULL AND pin_hash != '' AND id != ?`,
		excludeUserID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count admins: %w", err)
	}
	return n, nil
}

// CountOtherActiveSuperAdminsWithPIN counts active super_admins with a PIN
// besides the given user — guards "cannot deactivate the last super_admin"
// (ut-docs#761 review finding 4), the same shape as
// CountOtherActiveAdminsWithPIN above but for the role that surface never
// had to consider before super_admin promotion existed. Deactivating the
// last one would strand the till with nobody able to reach the permission
// matrix, audit page or backoffice.
func (r *AuthRepo) CountOtherActiveSuperAdminsWithPIN(ctx context.Context, excludeUserID string) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users
		 WHERE is_active = 1 AND role = 'super_admin' AND pin_hash IS NOT NULL AND pin_hash != '' AND id != ?`,
		excludeUserID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count super_admins: %w", err)
	}
	return n, nil
}

// lastPrivilegedRoleLeft is the last-active-admin / last-super_admin rule
// evaluated inside tx: it returns the role ("admin" or "super_admin") the
// change would leave with no other active holder that has a PIN, or "".
// curRole is the target's role now; newRole/active describe it AFTER the
// change.
func lastPrivilegedRoleLeft(ctx context.Context, tx *sql.Tx, userID, curRole, newRole string, active bool) (string, error) {
	for _, role := range []string{"admin", "super_admin"} {
		if curRole != role || (active && newRole == role) {
			continue
		}
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM users
			 WHERE is_active = 1 AND role = ? AND pin_hash IS NOT NULL AND pin_hash != '' AND id != ?`,
			role, userID).Scan(&n); err != nil {
			return "", fmt.Errorf("count other active %s: %w", role, err)
		}
		if n == 0 {
			return role, nil
		}
	}
	return "", nil
}

// userRoleTx reads a user's current role inside tx.
func userRoleTx(ctx context.Context, tx *sql.Tx, userID string) (string, error) {
	var role string
	err := tx.QueryRowContext(ctx, `SELECT role FROM users WHERE id = ?`, userID).Scan(&role)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("user %s not found", userID)
	}
	if err != nil {
		return "", fmt.Errorf("read user role: %w", err)
	}
	return role, nil
}

// SetUserActiveGuarded is SetUserActive with the last-active-admin /
// last-super_admin guard evaluated in the SAME transaction as the write
// (ut-docs#2755 review): the target's role is re-read and the other
// holders counted inside tx, so two concurrent changes can never both pass
// a count taken before either wrote. A non-empty refusedRole means the
// change was refused and nothing was written; the caller commits tx (with
// its audit row) otherwise.
func (r *AuthRepo) SetUserActiveGuarded(ctx context.Context, tx *sql.Tx, userID string, active bool) (refusedRole string, err error) {
	role, err := userRoleTx(ctx, tx, userID)
	if err != nil {
		return "", fmt.Errorf("set active: %w", err)
	}
	if refused, err := lastPrivilegedRoleLeft(ctx, tx, userID, role, role, active); err != nil || refused != "" {
		return refused, err
	}
	v := 0
	if active {
		v = 1
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET is_active = ? WHERE id = ?`, v, userID); err != nil {
		return "", fmt.Errorf("set active: %w", err)
	}
	return "", nil
}

// SetUserRoleGuarded is SetUserRole with the same in-transaction
// last-admin / last-super_admin guard as SetUserActiveGuarded.
func (r *AuthRepo) SetUserRoleGuarded(ctx context.Context, tx *sql.Tx, userID, role string) (refusedRole string, err error) {
	cur, err := userRoleTx(ctx, tx, userID)
	if err != nil {
		return "", fmt.Errorf("set user role: %w", err)
	}
	if refused, err := lastPrivilegedRoleLeft(ctx, tx, userID, cur, role, true); err != nil || refused != "" {
		return refused, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE users SET role = ? WHERE id = ?`, role, userID); err != nil {
		return "", fmt.Errorf("set user role: %w", err)
	}
	return "", nil
}

// The transaction-taking writes behind the cloud user directives (ut-docs
// reference/till-user-directives.md §4, ADR-0115 amendment 2026-09-25):
// the main till applies one directive in ONE write transaction, so every
// read and write it makes goes through tx. A nil tx runs against the DB
// directly (SetUserPINTx / RevokeUserSessionsTx only).

// authExec returns tx when non-nil, the DB otherwise.
func (r *AuthRepo) authExec(tx *sql.Tx) execer {
	if tx != nil {
		return tx
	}
	return r.db
}

// GetUserTx is GetUser read inside tx.
func (r *AuthRepo) GetUserTx(ctx context.Context, tx *sql.Tx, id string) (UserRow, bool, error) {
	u, err := scanUser(tx.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id).Scan)
	if err == sql.ErrNoRows {
		return UserRow{}, false, nil
	}
	if err != nil {
		return UserRow{}, false, fmt.Errorf("get user: %w", err)
	}
	return u, true, nil
}

// CreateUserWithID inserts an active operator under a caller-chosen id (the
// cloud mints the user_id so a re-applied directive is idempotent). No PIN
// yet — SetUserPINTx in the same tx.
func (r *AuthRepo) CreateUserWithID(ctx context.Context, tx *sql.Tx, id, username, displayName, role string) error {
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, username, display_name, role, is_active) VALUES (?, ?, ?, ?, 1)`,
		id, username, displayName, role); err != nil {
		return fmt.Errorf("create user %s: %w", id, err)
	}
	return nil
}

// UpdateUserProfile sets a user's username and/or display name; a nil
// pointer keeps that field. Errors when the user does not exist.
func (r *AuthRepo) UpdateUserProfile(ctx context.Context, tx *sql.Tx, id string, username, displayName *string) error {
	res, err := tx.ExecContext(ctx,
		`UPDATE users SET username = COALESCE(?, username), display_name = COALESCE(?, display_name) WHERE id = ?`,
		nullableStr(username), nullableStr(displayName), id)
	if err != nil {
		return fmt.Errorf("update user profile %s: %w", id, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("update user profile: user %s not found", id)
	}
	return nil
}

func nullableStr(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

// UsernameTakenByOther reports, inside tx, whether a user other than
// excludeID holds username (the users.username UNIQUE constraint).
func (r *AuthRepo) UsernameTakenByOther(ctx context.Context, tx *sql.Tx, username, excludeID string) (bool, error) {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE username = ? AND id != ?`, username, excludeID).Scan(&n); err != nil {
		return false, fmt.Errorf("username taken: %w", err)
	}
	return n > 0, nil
}

// SetUserPINTx is SetUserPIN inside tx.
func (r *AuthRepo) SetUserPINTx(ctx context.Context, tx *sql.Tx, userID, pinHash string) error {
	res, err := r.authExec(tx).ExecContext(ctx, `UPDATE users SET pin_hash = ? WHERE id = ?`, pinHash, userID)
	if err != nil {
		return fmt.Errorf("set pin: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("set pin: user %s not found", userID)
	}
	return nil
}

// RevokeUserSessionsTx is RevokeUserSessions inside tx.
func (r *AuthRepo) RevokeUserSessionsTx(ctx context.Context, tx *sql.Tx, userID string) error {
	if _, err := r.authExec(tx).ExecContext(ctx,
		`UPDATE sessions SET revoked_at = datetime('now') WHERE user_id = ? AND revoked_at IS NULL`,
		userID); err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	return nil
}

// InsertSession stores a new session (token already hashed by the caller).
func (r *AuthRepo) InsertSession(ctx context.Context, tokenHash, userID string, expiresAt time.Time) (string, error) {
	id := uuid.NewString()
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO sessions (id, token_hash, user_id, expires_at) VALUES (?, ?, ?, ?)`,
		id, tokenHash, userID, expiresAt.UTC().Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("insert session: %w", err)
	}
	return id, nil
}

// LookupSession resolves a live (unexpired, unrevoked, active-user) session.
func (r *AuthRepo) LookupSession(ctx context.Context, tokenHash string) (SessionRow, bool, error) {
	var s SessionRow
	var expires, lastSeen string
	err := r.db.QueryRowContext(ctx,
		`SELECT s.id, u.id, u.username, u.display_name, u.role, s.expires_at,
		        COALESCE(s.last_seen_at, s.created_at)
		 FROM sessions s JOIN users u ON u.id = s.user_id
		 WHERE s.token_hash = ? AND s.revoked_at IS NULL AND u.is_active = 1`,
		tokenHash).Scan(&s.SessionID, &s.UserID, &s.Username, &s.DisplayName, &s.Role, &expires, &lastSeen)
	if err == sql.ErrNoRows {
		return SessionRow{}, false, nil
	}
	if err != nil {
		return SessionRow{}, false, fmt.Errorf("lookup session: %w", err)
	}
	t, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return SessionRow{}, false, fmt.Errorf("parse session expiry: %w", err)
	}
	if time.Now().After(t) {
		return SessionRow{}, false, nil
	}
	s.ExpiresAt = t
	// last_seen_at is written as RFC3339; created_at (the COALESCE fallback)
	// is SQLite datetime('now') format — accept both.
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05"} {
		if ls, perr := time.Parse(layout, lastSeen); perr == nil {
			s.LastSeenAt = ls
			break
		}
	}
	return s, true, nil
}

// TouchSession records session activity for the idle auto-lock. Callers
// throttle it (at most ~once a minute) so SQLite isn't written per request.
func (r *AuthRepo) TouchSession(ctx context.Context, tokenHash string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE token_hash = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), tokenHash)
	if err != nil {
		return fmt.Errorf("touch session: %w", err)
	}
	return nil
}

// RevokeSession revokes one session (logout / lock).
func (r *AuthRepo) RevokeSession(ctx context.Context, tokenHash string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = datetime('now') WHERE token_hash = ? AND revoked_at IS NULL`,
		tokenHash)
	if err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// PurgeDeadSessions deletes revoked or long-expired sessions so the table
// doesn't grow forever on a till that locks/unlocks all day.
func (r *AuthRepo) PurgeDeadSessions(ctx context.Context, expiredBefore time.Time) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE revoked_at IS NOT NULL OR expires_at < ?`,
		expiredBefore.UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("purge sessions: %w", err)
	}
	return nil
}

// RevokeUserSessions revokes all of a user's sessions (deactivation, PIN reset).
func (r *AuthRepo) RevokeUserSessions(ctx context.Context, userID string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE sessions SET revoked_at = datetime('now') WHERE user_id = ? AND revoked_at IS NULL`,
		userID)
	if err != nil {
		return fmt.Errorf("revoke user sessions: %w", err)
	}
	return nil
}

// HasPermission reports whether role_permissions grants role the given
// action. No row (unknown role, unknown action, or an ungranted pairing
// that was never seeded) means denied — this is a closed, not an open,
// permission model.
func (r *AuthRepo) HasPermission(ctx context.Context, role, action string) (bool, error) {
	var granted int
	err := r.db.QueryRowContext(ctx,
		`SELECT granted FROM role_permissions WHERE role = ? AND action = ?`, role, action).Scan(&granted)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("has permission: %w", err)
	}
	return granted != 0, nil
}

// RoleExists reports whether role is a recognized row in roles — the
// matrix editor validates against this before writing, so a typo'd or
// forged role in a POST fails with a clean 400 instead of a raw FK
// constraint error surfacing to the client.
func (r *AuthRepo) RoleExists(ctx context.Context, role string) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM roles WHERE role = ?`, role).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("role exists: %w", err)
	}
	return true, nil
}

// ActionExists reports whether action is a recognized row in
// permission_actions. Same validate-before-write purpose as RoleExists.
func (r *AuthRepo) ActionExists(ctx context.Context, action string) (bool, error) {
	var exists int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM permission_actions WHERE action = ?`, action).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("action exists: %w", err)
	}
	return true, nil
}

// PermissionGrant is one cell of the role×action permission matrix (ut-docs#556).
type PermissionGrant struct {
	Role    string
	Action  string
	Granted bool
}

// ListRolePermissionMatrix returns every (role, action) pairing in the
// catalog — including ungranted ones, so the matrix editor can render a
// full grid rather than only the rows a previous migration happened to
// seed a "granted" row for. LEFT JOIN role_permissions: an action added
// by a later migration but never explicitly granted to a role still shows
// as an unchecked cell, not a missing one.
func (r *AuthRepo) ListRolePermissionMatrix(ctx context.Context) ([]PermissionGrant, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT roles.role, permission_actions.action,
		       COALESCE(role_permissions.granted, 0)
		FROM roles
		CROSS JOIN permission_actions
		LEFT JOIN role_permissions
		  ON role_permissions.role = roles.role
		 AND role_permissions.action = permission_actions.action
		ORDER BY roles.role, permission_actions.action`)
	if err != nil {
		return nil, fmt.Errorf("list role permission matrix: %w", err)
	}
	defer rows.Close()

	var out []PermissionGrant
	for rows.Next() {
		var g PermissionGrant
		var granted int
		if err := rows.Scan(&g.Role, &g.Action, &granted); err != nil {
			return nil, fmt.Errorf("scan permission grant: %w", err)
		}
		g.Granted = granted != 0
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list role permission matrix: %w", err)
	}
	return out, nil
}

// SetRolePermission grants or revokes one (role, action) cell. Runs inside
// the given tx when non-nil (the caller journals the change in the same
// transaction), or directly against the DB otherwise. Upserts rather than
// requiring a pre-existing row, since a (role, action) pairing with no row
// at all (an action added by a later migration, never explicitly seeded
// for this role) is a legitimate starting state — HasPermission already
// treats "no row" as denied, so the first grant here has to be an insert,
// not an update.
func (r *AuthRepo) SetRolePermission(ctx context.Context, tx *sql.Tx, role, action string, granted bool) error {
	g := 0
	if granted {
		g = 1
	}
	q := `INSERT INTO role_permissions (role, action, granted) VALUES (?, ?, ?)
	      ON CONFLICT (role, action) DO UPDATE SET granted = excluded.granted`
	var err error
	if tx != nil {
		_, err = tx.ExecContext(ctx, q, role, action, g)
	} else {
		_, err = r.db.ExecContext(ctx, q, role, action, g)
	}
	if err != nil {
		return fmt.Errorf("set role permission: %w", err)
	}
	return nil
}
