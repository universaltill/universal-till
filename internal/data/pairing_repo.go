package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrNotPending means the row is gone, already approved/denied, or expired
// by the time Approve ran — a concurrent approve/deny/expiry race, not a
// server fault. Handlers map this to 409, not 500.
var ErrNotPending = errors.New("pending pairing not found or not pending")

// PairingRepo backs the approve-to-pair pending-request queue (ADR-0033
// part 2/3): a replica's pair request sits here until a manager approves
// or denies it. Only the request's SHA-256 commitment is ever stored,
// never the raw secret (mirrors tills.bearer_hash / TillByBearerHash).
type PairingRepo struct{ db *sql.DB }

func NewPairingRepo(db *sql.DB) *PairingRepo { return &PairingRepo{db: db} }

type PendingPairingRow struct {
	ID          string
	DeviceName  string
	Commitment  string
	Token       string
	RequestedAt string
	ExpiresAt   string
	Status      string
}

// CreatePendingRequest records a new pair request; the caller has already
// hashed the replica's request_secret into commitment.
func (r *PairingRepo) CreatePendingRequest(ctx context.Context, deviceName, commitment string, ttl time.Duration) (string, error) {
	id := uuid.NewString()
	now := time.Now().UTC()
	_, err := r.db.ExecContext(ctx, `
INSERT INTO pending_pairings (id, device_name, commitment, requested_at, expires_at, status)
VALUES (?, ?, ?, ?, ?, 'pending')`,
		id, deviceName, commitment, now.Format(time.RFC3339), now.Add(ttl).Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("create pending pairing: %w", err)
	}
	return id, nil
}

// ListPending returns not-yet-expired pending requests, oldest first, for
// the manager's approve/deny queue. Expired rows are excluded here and
// opportunistically deleted.
func (r *PairingRepo) ListPending(ctx context.Context) ([]PendingPairingRow, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM pending_pairings WHERE expires_at < ?`, now); err != nil {
		return nil, fmt.Errorf("expire pending pairings: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, device_name, commitment, token, requested_at, expires_at, status
FROM pending_pairings WHERE status = 'pending' ORDER BY requested_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list pending pairings: %w", err)
	}
	defer rows.Close()
	var out []PendingPairingRow
	for rows.Next() {
		var p PendingPairingRow
		if err := rows.Scan(&p.ID, &p.DeviceName, &p.Commitment, &p.Token, &p.RequestedAt, &p.ExpiresAt, &p.Status); err != nil {
			return nil, fmt.Errorf("scan pending pairing: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetByID returns a not-yet-expired row by id, used both by the
// manager-approval handlers and the replica's possession-gated token
// retrieval. An expired row reports not-found.
func (r *PairingRepo) GetByID(ctx context.Context, id string) (PendingPairingRow, bool, error) {
	var p PendingPairingRow
	err := r.db.QueryRowContext(ctx, `
SELECT id, device_name, commitment, token, requested_at, expires_at, status
FROM pending_pairings WHERE id = ? AND expires_at >= ?`,
		id, time.Now().UTC().Format(time.RFC3339)).
		Scan(&p.ID, &p.DeviceName, &p.Commitment, &p.Token, &p.RequestedAt, &p.ExpiresAt, &p.Status)
	if err == sql.ErrNoRows {
		return PendingPairingRow{}, false, nil
	}
	if err != nil {
		return PendingPairingRow{}, false, fmt.Errorf("pending pairing by id: %w", err)
	}
	return p, true, nil
}

// Approve marks a pending row approved, associates the freshly-issued
// enrolment token with it (possession-gated retrieval hands this back),
// and extends expires_at by extendTTL from now — approving a request that
// was seconds from expiry must not hand the manager a success response
// for a pairing the replica can no longer actually retrieve. Returns
// ErrNotPending if the row is gone/already-resolved/expired (a race, not
// a server fault — the caller had already confirmed the row was pending,
// so this only fires if another request won a concurrent approve/deny).
func (r *PairingRepo) Approve(ctx context.Context, id, token string, extendTTL time.Duration) error {
	now := time.Now().UTC()
	res, err := r.db.ExecContext(ctx, `
UPDATE pending_pairings SET status = 'approved', token = ?, expires_at = ?
WHERE id = ? AND status = 'pending' AND expires_at >= ?`,
		token, now.Add(extendTTL).Format(time.RFC3339), id, now.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("approve pending pairing: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("approve pending pairing: %w", err)
	}
	if n == 0 {
		return ErrNotPending
	}
	return nil
}

// Deny removes the row outright — a later request from the same replica
// is a brand new row, not a resurrection of the denied one.
func (r *PairingRepo) Deny(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM pending_pairings WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deny pending pairing: %w", err)
	}
	return nil
}
