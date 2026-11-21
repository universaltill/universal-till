package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// ut-docs#6455 (ADR-0198 §2.3, §6.8): payment_attempts is core's own record
// of one card-present attempt, inserted pending BEFORE the customer taps and
// settled only by core (never by a plugin's say-so). It holds the amount,
// currency and key the broker and the native shell must equal, so none of
// them is ever taken from the page or a plugin. Till-local; no card data
// beyond brand and last four from the broker's answer (ADR-0127).

// Attempt states.
const (
	AttemptPending            = "pending"
	AttemptApproved           = "approved"
	AttemptDeclined           = "declined"
	AttemptCancelled          = "cancelled"
	AttemptUnknown            = "unknown"
	AttemptResolvedNotCharged = "resolved_not_charged"
)

// Reasons core itself writes (ut-docs#6795).
const (
	// AttemptReasonTillRestarted: boot found the attempt pending; the collect
	// that owned it died with the last process.
	AttemptReasonTillRestarted = "till_restarted"
	// AttemptReasonChargedNoSale: a manager settled an orphaned attempt as
	// charged with no sale; the refund is made at the provider.
	AttemptReasonChargedNoSale = "charged_no_sale"
)

// attentionScanMax is the hard cap on rows ListNeedingAttention reads.
const attentionScanMax = 500

// AttemptRetention is how long a settled attempt is kept.
const AttemptRetention = 90 * 24 * time.Hour

var (
	ErrAttemptInvalid  = errors.New("payment attempt is invalid")
	ErrAttemptNotFound = errors.New("payment attempt not found")
	// ErrAttemptMismatch: the key already belongs to another amount,
	// currency or provider.
	ErrAttemptMismatch = errors.New("payment attempt key belongs to a different payment")
	// ErrAttemptSettled: the attempt is settled and cannot change again.
	ErrAttemptSettled = errors.New("payment attempt is already settled")
)

var attemptCurrencyRE = regexp.MustCompile(`^[A-Za-z]{3}$`)

// attemptFrom lists, per target state, the states it may be reached from.
// Settled states (approved, declined, cancelled, resolved_not_charged) are
// never a source. pending is entered only by Begin.
var attemptFrom = map[string][]string{
	AttemptApproved:           {AttemptPending, AttemptUnknown},
	AttemptDeclined:           {AttemptPending, AttemptUnknown},
	AttemptCancelled:          {AttemptPending, AttemptUnknown},
	AttemptUnknown:            {AttemptPending, AttemptUnknown},
	AttemptResolvedNotCharged: {AttemptPending, AttemptUnknown},
}

// PaymentAttempt is one row.
type PaymentAttempt struct {
	Key         string
	Provider    string
	AmountMinor int64 // DB/DTO boundary: convert with money.FromMinor
	Currency    string
	State       string
	PIID        string
	CardBrand   string
	Last4       string
	Reason      string
	TenderRef   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Deadline    time.Time
}

// NeedsAttention reports whether the attempt is a payment needing a manager's
// attention (ut-docs#6795): unknown, or pending with its deadline passed (the
// collect that owned it is over and never settled it). A pending attempt with
// a future deadline is in flight in this process and is neither listed nor
// gating. The deadline is compared on the parsed time, never in SQL: the
// stored RFC3339Nano strings do not sort lexically.
func (a PaymentAttempt) NeedsAttention(now time.Time) bool {
	switch a.State {
	case AttemptUnknown:
		return true
	case AttemptPending:
		return !a.Deadline.After(now)
	}
	return false
}

// AttemptUpdate is what a state change may record. Empty fields keep what the
// row already has.
type AttemptUpdate struct {
	PIID, CardBrand, Last4, Reason string
}

// PaymentAttemptRepo owns all SQL for payment_attempts.
type PaymentAttemptRepo struct{ db *sql.DB }

func NewPaymentAttemptRepo(db *sql.DB) *PaymentAttemptRepo { return &PaymentAttemptRepo{db: db} }

const attemptCols = `idempotency_key, provider, amount_minor, currency, state, pi_id, card_brand, last4, reason, tender_ref, created_at, updated_at, deadline_at`

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func scanAttempt(row interface{ Scan(...any) error }) (PaymentAttempt, error) {
	var a PaymentAttempt
	var created, updated, deadline string
	if err := row.Scan(&a.Key, &a.Provider, &a.AmountMinor, &a.Currency, &a.State, &a.PIID, &a.CardBrand, &a.Last4, &a.Reason, &a.TenderRef, &created, &updated, &deadline); err != nil {
		return PaymentAttempt{}, err
	}
	a.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	a.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	a.Deadline, _ = time.Parse(time.RFC3339Nano, deadline)
	return a, nil
}

// Begin writes the attempt pending, before the tap. The same in-flight
// attempt retried returns its row unchanged; a declined, cancelled or
// resolved-not-charged one under the same key, amount and currency starts
// again; an approved one is refused (ErrAttemptSettled); the key bound to a
// different amount, currency or provider is ErrAttemptMismatch. Settled rows
// past AttemptRetention are deleted here, so the table needs no job.
func (r *PaymentAttemptRepo) Begin(ctx context.Context, a PaymentAttempt) (PaymentAttempt, error) {
	a.Currency = strings.ToLower(a.Currency)
	if a.Key == "" || a.Provider == "" || a.AmountMinor <= 0 || !attemptCurrencyRE.MatchString(a.Currency) {
		return PaymentAttempt{}, ErrAttemptInvalid
	}
	now := time.Now().UTC()
	if a.Deadline.IsZero() {
		a.Deadline = now
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return PaymentAttempt{}, fmt.Errorf("begin payment attempt: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	cur, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT `+attemptCols+` FROM payment_attempts WHERE idempotency_key = ?`, a.Key))
	switch {
	case err == nil:
		if cur.Provider != a.Provider || cur.AmountMinor != a.AmountMinor || !strings.EqualFold(cur.Currency, a.Currency) {
			return PaymentAttempt{}, ErrAttemptMismatch
		}
		switch cur.State {
		case AttemptPending, AttemptUnknown:
			return cur, nil
		case AttemptApproved:
			return PaymentAttempt{}, ErrAttemptSettled
		}
		// declined / cancelled / resolved_not_charged: the same tender again.
		if _, err := tx.ExecContext(ctx, `
UPDATE payment_attempts SET state = ?, pi_id = '', card_brand = '', last4 = '', reason = '', tender_ref = ?, updated_at = ?, deadline_at = ?
WHERE idempotency_key = ?`, AttemptPending, a.TenderRef, ts(now), ts(a.Deadline), a.Key); err != nil {
			return PaymentAttempt{}, fmt.Errorf("restart payment attempt: %w", err)
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := tx.ExecContext(ctx, `
INSERT INTO payment_attempts (`+attemptCols+`) VALUES (?, ?, ?, ?, ?, '', '', '', '', ?, ?, ?, ?)`,
			a.Key, a.Provider, a.AmountMinor, a.Currency, AttemptPending, a.TenderRef, ts(now), ts(now), ts(a.Deadline)); err != nil {
			return PaymentAttempt{}, fmt.Errorf("insert payment attempt: %w", err)
		}
	default:
		return PaymentAttempt{}, fmt.Errorf("read payment attempt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM payment_attempts WHERE state NOT IN ('pending','unknown') AND updated_at < ?`, ts(now.Add(-AttemptRetention))); err != nil {
		return PaymentAttempt{}, fmt.Errorf("prune payment attempts: %w", err)
	}
	out, err := scanAttempt(tx.QueryRowContext(ctx, `SELECT `+attemptCols+` FROM payment_attempts WHERE idempotency_key = ?`, a.Key))
	if err != nil {
		return PaymentAttempt{}, fmt.Errorf("read payment attempt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PaymentAttempt{}, fmt.Errorf("commit payment attempt: %w", err)
	}
	return out, nil
}

// Get returns the attempt for key; ok is false when there is none.
func (r *PaymentAttemptRepo) Get(ctx context.Context, key string) (PaymentAttempt, bool, error) {
	a, err := scanAttempt(r.db.QueryRowContext(ctx, `SELECT `+attemptCols+` FROM payment_attempts WHERE idempotency_key = ?`, key))
	if errors.Is(err, sql.ErrNoRows) {
		return PaymentAttempt{}, false, nil
	}
	if err != nil {
		return PaymentAttempt{}, false, fmt.Errorf("get payment attempt: %w", err)
	}
	return a, true, nil
}

// Mark moves the attempt to a new state. A settled attempt never changes
// again (ErrAttemptSettled): a second approve is a no-op error, not a second
// capture. Only pending and unknown attempts move.
func (r *PaymentAttemptRepo) Mark(ctx context.Context, key, state string, u AttemptUpdate) error {
	from, ok := attemptFrom[state]
	if !ok {
		return ErrAttemptInvalid
	}
	args := []any{state, u.PIID, u.CardBrand, u.Last4, u.Reason, ts(time.Now()), key}
	q := `UPDATE payment_attempts SET state = ?,
 pi_id = COALESCE(NULLIF(?, ''), pi_id), card_brand = COALESCE(NULLIF(?, ''), card_brand),
 last4 = COALESCE(NULLIF(?, ''), last4), reason = COALESCE(NULLIF(?, ''), reason), updated_at = ?
WHERE idempotency_key = ? AND state IN (` + strings.TrimSuffix(strings.Repeat("?,", len(from)), ",") + `)`
	for _, s := range from {
		args = append(args, s)
	}
	res, err := r.db.ExecContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("mark payment attempt: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return nil
	}
	if _, found, gerr := r.Get(ctx, key); gerr != nil {
		return gerr
	} else if !found {
		return ErrAttemptNotFound
	}
	return ErrAttemptSettled
}

// PruneSettled deletes settled attempts last touched before now minus
// AttemptRetention; pending and unknown are never deleted.
func (r *PaymentAttemptRepo) PruneSettled(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `DELETE FROM payment_attempts WHERE state NOT IN ('pending','unknown') AND updated_at < ?`, ts(now.Add(-AttemptRetention)))
	if err != nil {
		return 0, fmt.Errorf("prune payment attempts: %w", err)
	}
	return res.RowsAffected()
}

// MarkPendingUnknownAtBoot turns every attempt still pending at boot into
// unknown/till_restarted (ut-docs#6795): its collect, basket and attempt id
// lived in the last process only. Unknown rows keep their reason; settled
// rows are untouched. Call only before this process starts any collect.
func (r *PaymentAttemptRepo) MarkPendingUnknownAtBoot(ctx context.Context, now time.Time) (int64, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE payment_attempts SET state = ?, reason = ?, updated_at = ? WHERE state = ?`,
		AttemptUnknown, AttemptReasonTillRestarted, ts(now), AttemptPending)
	if err != nil {
		return 0, fmt.Errorf("mark pending payment attempts unknown: %w", err)
	}
	return res.RowsAffected()
}

// ListNeedingAttention returns the attempts that NeedAttention, oldest first,
// at most limit (<= 0 or above the scan cap means the cap). provider "" is
// every provider. A settled attempt is never returned.
func (r *PaymentAttemptRepo) ListNeedingAttention(ctx context.Context, now time.Time, provider string, limit int) ([]PaymentAttempt, error) {
	if limit <= 0 || limit > attentionScanMax {
		limit = attentionScanMax
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+attemptCols+` FROM payment_attempts
WHERE state IN ('pending','unknown') AND (? = '' OR provider = ?)
ORDER BY created_at, idempotency_key LIMIT ?`, provider, provider, attentionScanMax)
	if err != nil {
		return nil, fmt.Errorf("list payment attempts needing attention: %w", err)
	}
	defer rows.Close()
	var out []PaymentAttempt
	for rows.Next() {
		a, err := scanAttempt(rows)
		if err != nil {
			return nil, fmt.Errorf("scan payment attempt: %w", err)
		}
		if a.NeedsAttention(now) && len(out) < limit {
			out = append(out, a)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list payment attempts needing attention: %w", err)
	}
	return out, nil
}
