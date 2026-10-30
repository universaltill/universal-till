package data

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Bearer rotation (ut-docs#5198, migration 093). A replica's LAN sync bearer
// used to be valid for as long as the till stayed paired. The main till now
// asks a replica to swap it (POST /api/sync/bearer/rotate) when it predates
// rotation or is older than SyncBearerMaxAge, and keeps the previous bearer
// working for SyncBearerGrace so a replica that never saw the answer is not
// locked out.
const (
	// SyncBearerGrace is how long the previous bearer is still accepted,
	// counted from the first use of the new one.
	SyncBearerGrace = 10 * time.Minute
	// SyncBearerMaxAge is the age after which a bearer is asked to rotate.
	SyncBearerMaxAge = 30 * 24 * time.Hour
)

// TillByBearerHash resolves the sync caller; touches last_seen_at.
func (r *TillsRepo) TillByBearerHash(ctx context.Context, bearerHash string) (TillRow, bool, error) {
	return r.TillByBearerHashAt(ctx, bearerHash, time.Now())
}

// TillByBearerHashAt is TillByBearerHash at a given time.
//
// The hash may be the till's current bearer or the previous one a rotation
// kept. The first use of a new bearer starts the previous bearer's grace
// window. A previous bearer past its window is refused (ok false) and
// cleared; the row returned then carries only ID and BearerPrev, so the
// caller can say whose old credential was used.
func (r *TillsRepo) TillByBearerHashAt(ctx context.Context, bearerHash string, now time.Time) (TillRow, bool, error) {
	var t TillRow
	var cur, prev, issued, prevExp sql.NullString
	err := r.db.QueryRowContext(ctx, `
SELECT t.id, t.name, t.enrolled_at, COALESCE(t.last_seen_at, ''), t.role, p.till_id IS NOT NULL,
  t.bearer_hash, t.bearer_prev_hash, t.bearer_issued_at, t.bearer_prev_expires_at
FROM tills t LEFT JOIN till_tls_pinned p ON p.till_id = t.id
WHERE t.bearer_hash = ? OR t.bearer_prev_hash = ?`, bearerHash, bearerHash).
		Scan(&t.ID, &t.Name, &t.EnrolledAt, &t.LastSeenAt, &t.Role, &t.TLSPinned, &cur, &prev, &issued, &prevExp)
	if err == sql.ErrNoRows {
		return TillRow{}, false, nil
	}
	if err != nil {
		return TillRow{}, false, fmt.Errorf("till by bearer: %w", err)
	}
	if cur.Valid && cur.String == bearerHash {
		if prev.Valid {
			if err := r.settlePrevOnCurrentUse(ctx, t.ID, prevExp, now); err != nil {
				return TillRow{}, false, err
			}
		}
		t.RotateDue = !issued.Valid || now.Sub(parseBearerTime(issued.String)) > SyncBearerMaxAge
	} else {
		// The previous bearer. An unparseable expiry fails closed: expired.
		t.BearerPrev, t.BearerPending = true, !prevExp.Valid
		if prevExp.Valid && !now.Before(parseBearerTime(prevExp.String)) {
			if _, err := r.db.ExecContext(ctx,
				`UPDATE tills SET bearer_prev_hash = NULL, bearer_prev_expires_at = NULL WHERE id = ?`, t.ID); err != nil {
				return TillRow{}, false, fmt.Errorf("clear expired previous bearer: %w", err)
			}
			return TillRow{ID: t.ID, BearerPrev: true}, false, nil
		}
		t.RotateDue = t.BearerPending
	}
	_, _ = r.db.ExecContext(ctx, `UPDATE tills SET last_seen_at = ? WHERE id = ?`,
		now.UTC().Format(time.RFC3339), t.ID)
	return t, true, nil
}

// settlePrevOnCurrentUse runs when a till authenticates with its current
// bearer while a previous one is on file: the first such use starts the
// previous bearer's grace window; a window that has run out is cleared.
func (r *TillsRepo) settlePrevOnCurrentUse(ctx context.Context, id string, prevExp sql.NullString, now time.Time) error {
	var err error
	switch {
	case !prevExp.Valid:
		_, err = r.db.ExecContext(ctx,
			`UPDATE tills SET bearer_prev_expires_at = ? WHERE id = ? AND bearer_prev_hash IS NOT NULL AND bearer_prev_expires_at IS NULL`,
			now.Add(SyncBearerGrace).UTC().Format(time.RFC3339), id)
	case !now.Before(parseBearerTime(prevExp.String)):
		_, err = r.db.ExecContext(ctx,
			`UPDATE tills SET bearer_prev_hash = NULL, bearer_prev_expires_at = NULL WHERE id = ?`, id)
	}
	if err != nil {
		return fmt.Errorf("settle previous bearer: %w", err)
	}
	return nil
}

// parseBearerTime reads a bearer timestamp (RFC 3339 UTC); the zero time on
// anything else, which reads as "long ago" -- expired, due for rotation.
func parseBearerTime(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// RotateBearer swaps an enrolled till's bearer for newHash in one statement.
// presentedHash is the bearer the caller authenticated with:
//   - the current one: it becomes the previous bearer (grace not started
//     until the new one is used) and newHash becomes current;
//   - the previous one while its grace has not started: the caller never
//     received the last answer, so newHash replaces the unused current
//     bearer and the previous one stays pending.
//
// Anything else returns false and changes nothing.
func (r *TillsRepo) RotateBearer(ctx context.Context, id, presentedHash, newHash string, now time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
UPDATE tills SET
  bearer_prev_hash       = CASE WHEN bearer_hash = ?1 THEN bearer_hash ELSE bearer_prev_hash END,
  bearer_prev_expires_at = CASE WHEN bearer_hash = ?1 THEN NULL ELSE bearer_prev_expires_at END,
  bearer_hash            = ?2,
  bearer_issued_at       = ?3
WHERE id = ?4 AND (bearer_hash = ?1 OR (bearer_prev_hash = ?1 AND bearer_prev_expires_at IS NULL))`,
		presentedHash, newHash, now.UTC().Format(time.RFC3339), id)
	if err != nil {
		return false, fmt.Errorf("rotate till bearer: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rotate till bearer: %w", err)
	}
	return n > 0, nil
}

// ProofHashesByID returns an enrolled till's stored bearer hash and, while
// its grace has not started, the previous one, for the primary-proof
// handshake (ut-docs#2722): a replica that re-finds its main till over mDNS
// asks it to prove it holds this till's pairing record before sending it the
// bearer. A replica whose rotation answer was lost still holds the previous
// bearer, hence the second hash. Deliberately does NOT touch last_seen_at --
// the proof request is unauthenticated, so it must never make a till look
// alive. A missing or empty hash (a redacted snapshot copy) is "not found".
func (r *TillsRepo) ProofHashesByID(ctx context.Context, id string) (current, pendingPrev string, ok bool, err error) {
	err = r.db.QueryRowContext(ctx, `
SELECT COALESCE(bearer_hash, ''),
  CASE WHEN bearer_prev_expires_at IS NULL THEN COALESCE(bearer_prev_hash, '') ELSE '' END
FROM tills WHERE id = ?`, id).Scan(&current, &pendingPrev)
	if err == sql.ErrNoRows || (err == nil && current == "") {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("till bearer hash: %w", err)
	}
	return current, pendingPrev, true, nil
}
