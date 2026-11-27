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

// ErrRoleMismatch means an approval tried to change a request between a till
// role and card_reader (ut-docs#6538): the role is fixed by the request, in
// both directions, and the manager's approval cannot change it.
var ErrRoleMismatch = errors.New("approval cannot change a request between a till and a card reader")

// PairingRepo backs the approve-to-pair pending-request queue (ADR-0033
// part 2/3): a replica's pair request sits here until a manager approves
// or denies it. Only the request's SHA-256 commitment is ever stored,
// never the raw secret (mirrors tills.bearer_hash / TillByBearerHash).
type PairingRepo struct {
	db  *sql.DB
	now func() time.Time // injectable clock for expiry tests (ut-docs#4040)
}

func NewPairingRepo(db *sql.DB) *PairingRepo { return &PairingRepo{db: db, now: time.Now} }

type PendingPairingRow struct {
	ID          string
	DeviceName  string
	Commitment  string
	Token       string
	RequestedAt string
	ExpiresAt   string
	Status      string
	// RequestedRole is the role the replica asked to join as and, once a
	// manager approved it, the role the manager settled on (ut-docs#2781,
	// migration 067) — what /api/sync/enroll enrols the till as.
	RequestedRole string
	// ServedPin is the LAN TLS pin this till served on the connection that
	// carried the request, "" over plain HTTP (ut-docs#4091, migration
	// 070). The verification code binds it.
	ServedPin string
}

// CreatePendingRequest records a new pair request for an additional till;
// the caller has already hashed the replica's request_secret into
// commitment.
func (r *PairingRepo) CreatePendingRequest(ctx context.Context, deviceName, commitment string, ttl time.Duration) (string, error) {
	return r.CreatePendingRequestWithRole(ctx, deviceName, commitment, TillRoleAdditional, "", ttl)
}

// CreatePendingRequestWithRole is CreatePendingRequest with the role the
// replica asked to join as (ut-docs#2781; the caller validates it) and the
// LAN TLS pin served on the request's connection ("" for plain HTTP,
// ut-docs#4091).
func (r *PairingRepo) CreatePendingRequestWithRole(ctx context.Context, deviceName, commitment, role, servedPin string, ttl time.Duration) (string, error) {
	return r.CreatePendingRequestForDevice(ctx, deviceName, commitment, role, servedPin, "", ttl)
}

// CreatePendingRequestForDevice is CreatePendingRequestWithRole for a request
// that names its requesting device (ut-docs#7097, AC16): in the same
// transaction the device's older still-pending rows of the SAME name are
// deleted (the route is unauthenticated: a device id alone never removes
// another requester's request, review 2026-11-27), so a retry
// (or a restarted phone app) replaces its request instead of piling up. A
// blank requesterDeviceID (an older till) replaces nothing. Approved and
// denied rows are never touched.
func (r *PairingRepo) CreatePendingRequestForDevice(ctx context.Context, deviceName, commitment, role, servedPin, requesterDeviceID string, ttl time.Duration) (string, error) {
	if !ValidPairingRole(role) {
		return "", fmt.Errorf("create pending pairing: invalid role %q", role)
	}
	id := uuid.NewString()
	now := r.now().UTC()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("create pending pairing: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if requesterDeviceID != "" {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM pending_pairings WHERE requester_device_id = ? AND device_name = ? AND status = 'pending'`, requesterDeviceID, deviceName); err != nil {
			return "", fmt.Errorf("replace older pending pairings: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO pending_pairings (id, device_name, commitment, requested_at, expires_at, status, requested_role, served_pin, requester_device_id)
VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?)`,
		id, deviceName, commitment, now.Format(time.RFC3339), now.Add(ttl).Format(time.RFC3339), role, servedPin, requesterDeviceID); err != nil {
		return "", fmt.Errorf("create pending pairing: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("create pending pairing: %w", err)
	}
	return id, nil
}

// ListPending returns not-yet-expired pending requests, oldest first, for
// the manager's approve/deny queue. Expired rows are excluded here and
// opportunistically deleted.
func (r *PairingRepo) ListPending(ctx context.Context) ([]PendingPairingRow, error) {
	now := r.now().UTC().Format(time.RFC3339)
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM pending_pairings WHERE expires_at < ?`, now); err != nil {
		return nil, fmt.Errorf("expire pending pairings: %w", err)
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, device_name, commitment, token, requested_at, expires_at, status, requested_role, served_pin
FROM pending_pairings WHERE status = 'pending' ORDER BY requested_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list pending pairings: %w", err)
	}
	defer rows.Close()
	var out []PendingPairingRow
	for rows.Next() {
		var p PendingPairingRow
		if err := rows.Scan(&p.ID, &p.DeviceName, &p.Commitment, &p.Token, &p.RequestedAt, &p.ExpiresAt, &p.Status, &p.RequestedRole, &p.ServedPin); err != nil {
			return nil, fmt.Errorf("scan pending pairing: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ListPendingReadOnly is the same not-yet-expired pending set ListPending
// returns (expired rows filtered in the WHERE clause instead), WITHOUT
// ListPending's own opportunistic DELETE of expired rows -- for read-only
// callers polled every 30s from every open page (the sync-chip badge and
// the nav-level pairing notice, ut-docs#1551), where a write on every poll
// per open page per manager is avoidable SQLite write-lock contention
// against sale writes (offline-first, universal-till/CLAUDE.md). The
// manager-facing approve/deny queue (GET /ui/tills/pending-pairings) still
// calls ListPending on its own identical 30s cadence, so expired rows are
// still swept regularly -- this just isn't the path that has to do it on
// every other page too. Same `id ASC` tiebreak as ListPending: two
// requests landing in the same second (RFC3339 second precision) must
// still sort identically across repeated polls, or a caller fingerprinting
// this list (the notice's dismiss-tracking) would see it change with
// nothing having actually happened.
func (r *PairingRepo) ListPendingReadOnly(ctx context.Context) ([]PendingPairingRow, error) {
	now := r.now().UTC().Format(time.RFC3339)
	rows, err := r.db.QueryContext(ctx, `
SELECT id, device_name, commitment, token, requested_at, expires_at, status, requested_role, served_pin
FROM pending_pairings WHERE status = 'pending' AND expires_at >= ?
ORDER BY requested_at ASC, id ASC`, now)
	if err != nil {
		return nil, fmt.Errorf("list pending pairings (read-only): %w", err)
	}
	defer rows.Close()
	var out []PendingPairingRow
	for rows.Next() {
		var p PendingPairingRow
		if err := rows.Scan(&p.ID, &p.DeviceName, &p.Commitment, &p.Token, &p.RequestedAt, &p.ExpiresAt, &p.Status, &p.RequestedRole, &p.ServedPin); err != nil {
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
SELECT id, device_name, commitment, token, requested_at, expires_at, status, requested_role, served_pin
FROM pending_pairings WHERE id = ? AND expires_at >= ?`,
		id, r.now().UTC().Format(time.RFC3339)).
		Scan(&p.ID, &p.DeviceName, &p.Commitment, &p.Token, &p.RequestedAt, &p.ExpiresAt, &p.Status, &p.RequestedRole, &p.ServedPin)
	if err == sql.ErrNoRows {
		return PendingPairingRow{}, false, nil
	}
	if err != nil {
		return PendingPairingRow{}, false, fmt.Errorf("pending pairing by id: %w", err)
	}
	return p, true, nil
}

// GetByIDForPoll is GetByID without the expiry filter: expired reports
// expires_at < now. Only for the possession-gated poll -- callers act on
// expired/status only after the request secret matched, so an expired or
// denied row is never revealed to a non-holder.
func (r *PairingRepo) GetByIDForPoll(ctx context.Context, id string) (row PendingPairingRow, expired, ok bool, err error) {
	err = r.db.QueryRowContext(ctx, `
SELECT id, device_name, commitment, token, requested_at, expires_at, status, requested_role, served_pin
FROM pending_pairings WHERE id = ?`, id).
		Scan(&row.ID, &row.DeviceName, &row.Commitment, &row.Token, &row.RequestedAt, &row.ExpiresAt, &row.Status, &row.RequestedRole, &row.ServedPin)
	if err == sql.ErrNoRows {
		return PendingPairingRow{}, false, false, nil
	}
	if err != nil {
		return PendingPairingRow{}, false, false, fmt.Errorf("pending pairing by id (poll): %w", err)
	}
	return row, row.ExpiresAt < r.now().UTC().Format(time.RFC3339), true, nil
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
	return r.ApproveWithRole(ctx, id, token, "", extendTTL)
}

// ApproveWithRole is Approve that also records the manager's final role
// for the till (ut-docs#2781) over the one the replica requested; a blank
// role keeps the requested one. The caller validates a non-blank role.
func (r *PairingRepo) ApproveWithRole(ctx context.Context, id, token, role string, extendTTL time.Duration) error {
	if role != "" && !ValidPairingRole(role) {
		return fmt.Errorf("approve pending pairing: invalid role %q", role)
	}
	now := r.now().UTC()
	// ut-docs#6538: the role is fixed by the request. A reader request stays a
	// reader and a till request stays a till; the match is part of the same
	// compare-and-swap, so no race can slip a role change in.
	res, err := r.db.ExecContext(ctx, `
UPDATE pending_pairings SET status = 'approved', token = ?, expires_at = ?,
	requested_role = COALESCE(NULLIF(?, ''), requested_role)
WHERE id = ? AND status = 'pending' AND expires_at >= ?
  AND (? = '' OR (? = 'card_reader') = (requested_role = 'card_reader'))`,
		token, now.Add(extendTTL).Format(time.RFC3339), role, id, now.Format(time.RFC3339), role, role)
	if err != nil {
		return fmt.Errorf("approve pending pairing: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("approve pending pairing: %w", err)
	}
	if n == 0 {
		if role != "" {
			if cur, ok, gerr := r.GetByID(ctx, id); gerr == nil && ok && cur.Status == "pending" {
				return ErrRoleMismatch
			}
		}
		return ErrNotPending
	}
	return nil
}

// Consume deletes an approved request in the same step that hands its result
// over, so an approval can be collected once only (ut-docs#6538). It reports
// whether this call removed it.
func (r *PairingRepo) Consume(ctx context.Context, id string) (bool, error) {
	// An expired approval is never consumed, whatever the caller checked
	// (#6796 review): the bearer is minted only inside the TTL.
	res, err := r.db.ExecContext(ctx, `DELETE FROM pending_pairings WHERE id = ? AND status = 'approved' AND expires_at >= ?`,
		id, r.now().UTC().Format(time.RFC3339))
	if err != nil {
		return false, fmt.Errorf("consume pairing: %w", err)
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RoleForToken returns the role an approved, not-yet-expired request's
// enrolment token enrols its till as (ut-docs#2781): the manager's final
// choice at approval. ok is false when no approved request carries token —
// a QR / pairing-code enrolment, whose role comes from the joining till.
func (r *PairingRepo) RoleForToken(ctx context.Context, token string) (role string, ok bool, err error) {
	if token == "" {
		return "", false, nil
	}
	err = r.db.QueryRowContext(ctx, `
SELECT requested_role FROM pending_pairings
WHERE token = ? AND status = 'approved' AND expires_at >= ?`,
		token, r.now().UTC().Format(time.RFC3339)).Scan(&role)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("pending pairing role by token: %w", err)
	}
	return role, true, nil
}

// NameForToken returns the device name a manager approved for this enrolment
// token (ut-docs#7057): the name the approval card showed. ok is false for a
// token that no approved, unexpired request carries (a typed pairing code, an
// expired approval).
func (r *PairingRepo) NameForToken(ctx context.Context, token string) (name string, ok bool, err error) {
	if token == "" {
		return "", false, nil
	}
	err = r.db.QueryRowContext(ctx, `
SELECT device_name FROM pending_pairings
WHERE token = ? AND status = 'approved' AND expires_at >= ?`,
		token, r.now().UTC().Format(time.RFC3339)).Scan(&name)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("pending pairing name by token: %w", err)
	}
	return name, true, nil
}

// RequesterDeviceForToken returns the device id the requester of this approved,
// unexpired enrolment token named in its pair request ("" for an older till
// that sent none). ok is false when no approved request carries the token (a
// typed code, a QR token, an expired approval). ut-docs#7097 review: the proof
// that a rename may take over an existing till's row.
func (r *PairingRepo) RequesterDeviceForToken(ctx context.Context, token string) (deviceID string, ok bool, err error) {
	if token == "" {
		return "", false, nil
	}
	err = r.db.QueryRowContext(ctx, `
SELECT COALESCE(requester_device_id, '') FROM pending_pairings
WHERE token = ? AND status = 'approved' AND expires_at >= ?`,
		token, r.now().UTC().Format(time.RFC3339)).Scan(&deviceID)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("pending pairing device by token: %w", err)
	}
	return deviceID, true, nil
}

// Deny resolves a request as refused. A till request is deleted outright (a
// later request from the same replica is a brand new row). A card_reader
// request instead keeps a `denied` tombstone with its token cleared and its
// expires_at unchanged, so the phone's poll can tell its holder "denied" for
// the request's remaining TTL (ut-docs#6796, ADR-0033 §4 amendment); the
// tombstone is swept by ListPending's expired-row DELETE. Both statements are
// idempotent. A denied row is never listed, approved or consumed.
func (r *PairingRepo) Deny(ctx context.Context, id string) error {
	if _, err := r.db.ExecContext(ctx, `
UPDATE pending_pairings SET status = 'denied', token = ''
WHERE id = ? AND requested_role = 'card_reader' AND status IN ('pending', 'approved')`, id); err != nil {
		return fmt.Errorf("deny pending pairing: %w", err)
	}
	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM pending_pairings WHERE id = ? AND requested_role <> 'card_reader'`, id); err != nil {
		return fmt.Errorf("deny pending pairing: %w", err)
	}
	return nil
}
