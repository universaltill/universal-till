package data

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// TillsRepo manages enrolled replica tills on the primary (ADR-0011 D1).
type TillsRepo struct{ db *sql.DB }

func NewTillsRepo(db *sql.DB) *TillsRepo { return &TillsRepo{db: db} }

type TillRow struct {
	ID         string
	Name       string
	EnrolledAt string
	LastSeenAt string
	Role       string // TillRoleAdditional | TillRoleSatellite (ut-docs#2781)
}

// A joined till's manager-assigned role (ut-docs#2781, migration 067).
// Until ut-docs#1154 a satellite is ADR-0020's counter-pay kiosk: order
// capture only, never a register or back office (ADR-0086).
const (
	TillRoleAdditional = "additional"
	TillRoleSatellite  = "satellite"
)

// ValidTillRole reports whether role is one tills.role accepts (the
// migration's CHECK, enforced here first so a caller gets a plain error).
func ValidTillRole(role string) bool {
	return role == TillRoleAdditional || role == TillRoleSatellite
}

// InsertTill enrols a replica with the role a manager chose before pairing
// (ut-docs#2781; TillRoleAdditional is today's default); the caller hashes
// the bearer. An unknown role is refused.
func (r *TillsRepo) InsertTill(ctx context.Context, name, bearerHash, role string) (string, error) {
	if !ValidTillRole(role) {
		return "", fmt.Errorf("insert till: unknown role %q", role)
	}
	id := uuid.NewString()
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO tills (id, name, bearer_hash, role) VALUES (?, ?, ?, ?)`, id, name, bearerHash, role)
	if err != nil {
		return "", fmt.Errorf("insert till: %w", err)
	}
	return id, nil
}

// UpdateRole sets an enrolled till's role (ut-docs#2781). Like UpdateName it
// writes only on a real change, so a repeated save never bumps
// sync_admin_version (migration 067's role trigger); changed says whether
// it did. An unknown role is refused.
func (r *TillsRepo) UpdateRole(ctx context.Context, id, role string) (bool, error) {
	if !ValidTillRole(role) {
		return false, fmt.Errorf("update till role: unknown role %q", role)
	}
	res, err := r.db.ExecContext(ctx, `UPDATE tills SET role = ? WHERE id = ? AND role IS NOT ?`, role, id, role)
	if err != nil {
		return false, fmt.Errorf("update till role: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("update till role: %w", err)
	}
	return n > 0, nil
}

// RoleByID returns one till's role; ok is false when no such till is
// enrolled. On a joined till this reads its synced copy of the roster —
// how a till learns its own role after an admin pull (ut-docs#2781).
// Deliberately does NOT touch last_seen_at.
func (r *TillsRepo) RoleByID(ctx context.Context, id string) (role string, ok bool, err error) {
	err = r.db.QueryRowContext(ctx, `SELECT role FROM tills WHERE id = ?`, id).Scan(&role)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("till role: %w", err)
	}
	return role, true, nil
}

// UpdateName sets an enrolled till's display name to the name that till
// reports for itself (ut-docs#3294). It writes only when the name really
// changed, so a repeated report never bumps sync_admin_version (migration
// 023's tills trigger fires on a name change); changed says whether it did.
// The caller validates the name.
func (r *TillsRepo) UpdateName(ctx context.Context, id, name string) (bool, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE tills SET name = ? WHERE id = ? AND name IS NOT ?`, name, id, name)
	if err != nil {
		return false, fmt.Errorf("update till name: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("update till name: %w", err)
	}
	return n > 0, nil
}

// ListTills returns enrolled tills, newest first.
func (r *TillsRepo) ListTills(ctx context.Context) ([]TillRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, enrolled_at, COALESCE(last_seen_at, ''), role
FROM tills ORDER BY enrolled_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list tills: %w", err)
	}
	defer rows.Close()
	var out []TillRow
	for rows.Next() {
		var t TillRow
		if err := rows.Scan(&t.ID, &t.Name, &t.EnrolledAt, &t.LastSeenAt, &t.Role); err != nil {
			return nil, fmt.Errorf("scan till: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// NameTaken reports whether an enrolled replica already uses this name,
// case-insensitively (ut-docs#1264).
//
// The fold is done in Go, deliberately, not as `lower(name) = lower(?)` in
// SQL (independent review finding): SQLite's built-in lower() only folds
// ASCII, so the SQL form silently misses "Ünite" vs "ünite" or "Café" vs
// "CAFÉ" — real collisions on the tr/fa/ar installs this product ships —
// AND it would disagree with the strings.EqualFold check the enrolment
// handler applies to the primary's own name, making the same pair of names
// a duplicate against the primary but not against a sibling. A shop has a
// handful of tills, so reading the column is cheap.
func (r *TillsRepo) NameTaken(ctx context.Context, name string) (bool, error) {
	return r.NameTakenExcept(ctx, name, "")
}

// NameTakenExcept is NameTaken skipping the row whose id is exceptID (none
// when blank): a joined till's synced roster holds its own row, which its
// own rename must not collide with (ut-docs#3308).
func (r *TillsRepo) NameTakenExcept(ctx context.Context, name, exceptID string) (bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, name FROM tills`)
	if err != nil {
		return false, fmt.Errorf("till name taken: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, existing string
		if err := rows.Scan(&id, &existing); err != nil {
			return false, fmt.Errorf("scan till name: %w", err)
		}
		if exceptID != "" && id == exceptID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(existing), strings.TrimSpace(name)) {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("till name taken: %w", err)
	}
	return false, nil
}

// TillByBearerHash resolves the sync caller; touches last_seen_at.
func (r *TillsRepo) TillByBearerHash(ctx context.Context, bearerHash string) (TillRow, bool, error) {
	var t TillRow
	err := r.db.QueryRowContext(ctx, `
SELECT id, name, enrolled_at, COALESCE(last_seen_at, '')
FROM tills WHERE bearer_hash = ?`, bearerHash).
		Scan(&t.ID, &t.Name, &t.EnrolledAt, &t.LastSeenAt)
	if err == sql.ErrNoRows {
		return TillRow{}, false, nil
	}
	if err != nil {
		return TillRow{}, false, fmt.Errorf("till by bearer: %w", err)
	}
	_, _ = r.db.ExecContext(ctx, `UPDATE tills SET last_seen_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), t.ID)
	return t, true, nil
}

// TouchLastSeen refreshes a till's last_seen_at without a bearer lookup —
// the main-till link (ADR-0114) calls it for a linked till's frames, so
// table-claim TTLs keep treating a linked till as alive. last_seen_at is a
// redactCol, so this never bumps sync_admin_version (migration 023).
func (r *TillsRepo) TouchLastSeen(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE tills SET last_seen_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
	if err != nil {
		return fmt.Errorf("touch till last seen: %w", err)
	}
	return nil
}

// DeleteTill revokes a replica's enrolment.
func (r *TillsRepo) DeleteTill(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM tills WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete till: %w", err)
	}
	return nil
}

// BearerHashByID returns an enrolled till's stored bearer hash, for the
// primary-proof handshake (ut-docs#2722): a replica that re-finds its main
// till over mDNS asks it to prove it holds this till's pairing record before
// sending it the bearer. Deliberately does NOT touch last_seen_at — the proof
// request is unauthenticated, so it must never make a till look alive. A
// missing or empty hash (a redacted snapshot copy) is "not found".
func (r *TillsRepo) BearerHashByID(ctx context.Context, id string) (string, bool, error) {
	var h string
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(bearer_hash, '') FROM tills WHERE id = ?`, id).Scan(&h)
	if err == sql.ErrNoRows || (err == nil && h == "") {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("till bearer hash: %w", err)
	}
	return h, true, nil
}
