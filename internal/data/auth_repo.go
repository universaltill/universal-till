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
