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
}

// InsertTill enrols a replica; the caller hashes the bearer.
func (r *TillsRepo) InsertTill(ctx context.Context, name, bearerHash string) (string, error) {
	id := uuid.NewString()
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO tills (id, name, bearer_hash) VALUES (?, ?, ?)`, id, name, bearerHash)
	if err != nil {
		return "", fmt.Errorf("insert till: %w", err)
	}
	return id, nil
}

// ListTills returns enrolled tills, newest first.
func (r *TillsRepo) ListTills(ctx context.Context) ([]TillRow, error) {
	rows, err := r.db.QueryContext(ctx, `
SELECT id, name, enrolled_at, COALESCE(last_seen_at, '')
FROM tills ORDER BY enrolled_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list tills: %w", err)
	}
	defer rows.Close()
	var out []TillRow
	for rows.Next() {
		var t TillRow
		if err := rows.Scan(&t.ID, &t.Name, &t.EnrolledAt, &t.LastSeenAt); err != nil {
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
	rows, err := r.db.QueryContext(ctx, `SELECT name FROM tills`)
	if err != nil {
		return false, fmt.Errorf("till name taken: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var existing string
		if err := rows.Scan(&existing); err != nil {
			return false, fmt.Errorf("scan till name: %w", err)
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

// DeleteTill revokes a replica's enrolment.
func (r *TillsRepo) DeleteTill(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM tills WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete till: %w", err)
	}
	return nil
}
