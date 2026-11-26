package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// ut-docs#6653 (ADR-0136 Decision 9): the durable, append-only journal of
// every electronic payment leg. One row per state change, keyed by the leg's
// authorize request id and an ask counter; a leg is in the state of its
// newest row. Written BEFORE the provider is asked and closed inside the
// sale's own transaction, so a crash between capture and sale leaves a row
// boot can act on. Till-local. No card number, expiry or CVV is ever stored;
// the provider's transaction reference is (a reversal needs it in full, as
// payment_attempts.pi_id already keeps). All SQL for the table lives here.

// Leg states.
const (
	LegAsked         = "asked"
	LegCaptured      = "captured"
	LegUnknown       = "unknown"
	LegDeclined      = "declined"
	LegSettled       = "settled"
	LegReversing     = "reversing"
	LegReversed      = "reversed"
	LegReverseFailed = "reverse_failed"
	LegWarned        = "warned"
	LegResolved      = "resolved"

	// LegReasonRestart is the reason boot writes on a leg it warns about.
	LegReasonRestart = "till_restarted"
	// LegReasonAttemptSettled: boot closed an unknown/asked tap leg whose
	// payment attempt was already settled (ADR-0136 Decision 9.5, ut-docs#7027).
	LegReasonAttemptSettled = "attempt_settled"
	// LegReasonTapChargedNoSale: boot found a captured tap/phone leg with no
	// sale; the customer was charged (ADR-0136 Decision 9.2, ut-docs#7028).
	LegReasonTapChargedNoSale = "tap_charged_no_sale"
	// LegReasonRefundNoReturn: a provider refund was sent back to the customer
	// but the return sale is not recorded (ADR-0136 Decision 9.8, ut-docs#7041).
	LegReasonRefundNoReturn = "refund_no_return"
	// LegRefundKeyPrefix starts the journal key of a refund leg.
	LegRefundKeyPrefix = "refund:"
	// ut-docs#6653 slice 3: why a leg is reverse_failed or resolved.
	LegReasonMainTillOnly    = "main_till_only"
	LegReasonReversalFailed  = "reversal_failed"
	LegReasonNoRefundPlugin  = "no_refund_plugin"
	LegReasonManagerResolved = "manager_resolved"

	legJournalScanMax = 1000
)

// LegJournalRetention is how long a finished (terminal) chain is kept.
const LegJournalRetention = 90 * 24 * time.Hour

var (
	// ErrJournalWrite: the journal could not be written. For the asked row
	// the caller refuses to ask the provider (fail closed, Decision 9.3).
	ErrJournalWrite = errors.New("payment leg journal write failed")
	// ErrLegJournalTransition: the move is not allowed from the leg's state.
	ErrLegJournalTransition = errors.New("payment leg journal: transition not allowed")
	// ErrLegJournalInvalid: the entry is malformed.
	ErrLegJournalInvalid = errors.New("payment leg journal: invalid entry")
	// ErrLegJournalNotFound: the leg has no row.
	ErrLegJournalNotFound = errors.New("payment leg journal: leg not found")
)

// legFrom lists, per target state, the states it may follow ("" = no row
// yet): the allowed moves of Decision 9.2.
var legFrom = map[string][]string{
	LegAsked:         {"", LegUnknown, LegDeclined},
	LegCaptured:      {LegAsked},
	LegUnknown:       {LegAsked},
	LegDeclined:      {LegAsked},
	LegSettled:       {LegCaptured},
	LegReversing:     {LegCaptured, LegReverseFailed},
	LegReversed:      {LegReversing},
	LegReverseFailed: {LegReversing},
	LegWarned:        {LegUnknown, LegAsked, LegReversing, LegCaptured},
	LegResolved:      {LegWarned, LegReverseFailed},
}

// legTerminal: chains whose newest state is one of these are swept after
// LegJournalRetention. warned and reverse_failed are never swept.
const legTerminalSQL = `'settled','reversed','declined','resolved'`

// RefundLegKey is the journal key of a refund leg: the tap refund key
// `refund:<original sale id>:<method>:<request id>` (ut-docs#7041).
func RefundLegKey(saleID, method, requestID string) string {
	return LegRefundKeyPrefix + saleID + ":" + method + ":" + requestID
}

// RefundLegOriginalSale is the original sale id inside a refund leg key;
// false for any other key.
func RefundLegOriginalSale(key string) (string, bool) {
	rest, ok := strings.CutPrefix(key, LegRefundKeyPrefix)
	if !ok {
		return "", false
	}
	id, _, _ := strings.Cut(rest, ":")
	return id, id != ""
}

// LegEntry is one journal row.
type LegEntry struct {
	Seq       int64
	RequestID string
	Ask       int // 1 for the first ask of the request id, +1 per re-ask
	State     string
	MethodID  string
	// AmountMinor is the exact captured amount including any reader tip
	// (DB/DTO boundary: convert with money.FromMinor).
	AmountMinor    int64
	Currency       string
	TipMinor       int64
	ReaderRef      string
	TransactionRef string // the provider's reference, in full
	SaleID         string
	Reason         string
	At             time.Time
}

// PaymentLegJournalRepo owns all SQL for payment_leg_journal.
type PaymentLegJournalRepo struct{ db *sql.DB }

func NewPaymentLegJournalRepo(db *sql.DB) *PaymentLegJournalRepo {
	return &PaymentLegJournalRepo{db: db}
}

const legCols = `seq, request_id, ask, state, method_id, amount_minor, currency, tip_minor, reader_ref, transaction_ref, sale_id, reason, at`

// legTS is fixed width, so the stored text also sorts correctly.
func legTS(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func scanLeg(row interface{ Scan(...any) error }) (LegEntry, error) {
	var e LegEntry
	var at string
	if err := row.Scan(&e.Seq, &e.RequestID, &e.Ask, &e.State, &e.MethodID, &e.AmountMinor, &e.Currency, &e.TipMinor, &e.ReaderRef, &e.TransactionRef, &e.SaleID, &e.Reason, &at); err != nil {
		return LegEntry{}, err
	}
	e.At, _ = time.Parse(time.RFC3339Nano, at)
	return e, nil
}

// Append records a state change. The move is validated against the leg's
// newest row in the same transaction (ErrLegJournalTransition). Repeating the
// state the leg is already in is a no-op, so a retried leg's answer is never
// recorded twice. An asked row carries ask+1 of the previous one. Any write
// failure is ErrJournalWrite. An asked row also sweeps finished chains past
// LegJournalRetention.
func (r *PaymentLegJournalRepo) Append(ctx context.Context, e LegEntry) error {
	if e.RequestID == "" || e.MethodID == "" || e.AmountMinor < 0 || e.TipMinor < 0 || len(legFrom[e.State]) == 0 {
		return ErrLegJournalInvalid
	}
	now := e.At
	if now.IsZero() {
		now = time.Now()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin: %v", ErrJournalWrite, err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := latestLeg(ctx, tx, e.RequestID)
	if err != nil {
		return fmt.Errorf("%w: read: %v", ErrJournalWrite, err)
	}
	if _, err := appendTx(ctx, tx, cur, e, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit: %v", ErrJournalWrite, err)
	}
	return nil
}

// appendTx validates e against cur (the leg's newest row, read in tx) and
// inserts it. It returns the new newest row; a same-state repeat writes
// nothing and returns cur.
func appendTx(ctx context.Context, tx *sql.Tx, cur, e LegEntry, now time.Time) (LegEntry, error) {
	if cur.State == e.State {
		return cur, nil
	}
	if !slices.Contains(legFrom[e.State], cur.State) {
		return cur, fmt.Errorf("%w: %q -> %q", ErrLegJournalTransition, cur.State, e.State)
	}
	// captured -> warned exists only for a charged tap with no sale (ut-docs#7028)
	// or a refunded card whose return was not recorded (ut-docs#7041).
	if cur.State == LegCaptured && e.State == LegWarned && e.Reason != LegReasonTapChargedNoSale && e.Reason != LegReasonRefundNoReturn {
		return cur, fmt.Errorf("%w: %q -> %q needs reason %q or %q", ErrLegJournalTransition, cur.State, e.State, LegReasonTapChargedNoSale, LegReasonRefundNoReturn)
	}
	// ...and refund_no_return is written on no other move.
	if e.Reason == LegReasonRefundNoReturn && !(cur.State == LegCaptured && e.State == LegWarned) {
		return cur, fmt.Errorf("%w: reason %q is only for %q -> %q (not %q -> %q)", ErrLegJournalTransition, e.Reason, LegCaptured, LegWarned, cur.State, e.State)
	}
	// ...and the tap reason is written on no other move.
	if e.Reason == LegReasonTapChargedNoSale && !(cur.State == LegCaptured && e.State == LegWarned) {
		return cur, fmt.Errorf("%w: reason %q is only for %q -> %q (not %q -> %q)", ErrLegJournalTransition, e.Reason, LegCaptured, LegWarned, cur.State, e.State)
	}
	// attempt_settled closes a leg boot already warned, and only that move (ut-docs#7027).
	if e.Reason == LegReasonAttemptSettled && !(cur.State == LegWarned && e.State == LegResolved) {
		return cur, fmt.Errorf("%w: reason %q is only for %q -> %q (not %q -> %q)", ErrLegJournalTransition, e.Reason, LegWarned, LegResolved, cur.State, e.State)
	}
	e.Ask = max(cur.Ask, 1)
	if e.State == LegAsked {
		e.Ask = cur.Ask + 1
	}
	if err := insertLeg(ctx, tx, e, now); err != nil {
		return cur, err
	}
	if e.State == LegAsked {
		if _, err := tx.ExecContext(ctx, `
DELETE FROM payment_leg_journal WHERE request_id IN (
  SELECT j.request_id FROM payment_leg_journal j
  WHERE j.seq = (SELECT MAX(seq) FROM payment_leg_journal WHERE request_id = j.request_id)
    AND j.state IN (`+legTerminalSQL+`) AND j.at < ?)`, legTS(now.Add(-LegJournalRetention))); err != nil {
			return cur, fmt.Errorf("%w: sweep: %v", ErrJournalWrite, err)
		}
	}
	e.Seq = 0
	return e, nil
}

// AppendChain appends next in order, in one transaction, after the leg's
// newest row, which must still be from (same Seq), else
// ErrLegJournalTransition and nothing is written. Every step is checked as
// Append checks it. All rows or none (ut-docs#7027). Every row is stamped
// with the time of the call; a step's At is ignored.
func (r *PaymentLegJournalRepo) AppendChain(ctx context.Context, from LegEntry, next ...LegEntry) error {
	if len(next) == 0 {
		return ErrLegJournalInvalid
	}
	for _, e := range next {
		if e.RequestID == "" || e.MethodID == "" || e.AmountMinor < 0 || e.TipMinor < 0 || len(legFrom[e.State]) == 0 || e.State == LegAsked {
			return ErrLegJournalInvalid
		}
	}
	now := time.Now()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin: %v", ErrJournalWrite, err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err := latestLeg(ctx, tx, from.RequestID)
	if err != nil {
		return fmt.Errorf("%w: read: %v", ErrJournalWrite, err)
	}
	if cur.Seq != from.Seq {
		return fmt.Errorf("%w: the leg changed since it was read", ErrLegJournalTransition)
	}
	for _, e := range next {
		if e.RequestID != from.RequestID {
			return ErrLegJournalInvalid
		}
		if cur, err = appendTx(ctx, tx, cur, e, now); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit: %v", ErrJournalWrite, err)
	}
	return nil
}

func insertLeg(ctx context.Context, tx *sql.Tx, e LegEntry, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `
INSERT INTO payment_leg_journal (request_id, ask, state, method_id, amount_minor, currency, tip_minor, reader_ref, transaction_ref, sale_id, reason, at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.RequestID, max(e.Ask, 1), e.State, e.MethodID, e.AmountMinor, e.Currency, e.TipMinor, e.ReaderRef, e.TransactionRef, e.SaleID, e.Reason, legTS(now)); err != nil {
		return fmt.Errorf("%w: insert: %v", ErrJournalWrite, err)
	}
	return nil
}

// AskLeg records the leg as asked before the provider is called. A leg that
// is already asked (an earlier outcome write failed) needs no new row, and a
// leg already captured (a retry that re-asks an approved leg under its
// original id) must stay captured; neither gets one, and only a captured leg
// reports began=false. Unknown and declined legs are asked again, ask+1. Any
// other state is ErrLegJournalTransition.
func (r *PaymentLegJournalRepo) AskLeg(ctx context.Context, e LegEntry) (began bool, err error) {
	cur, ok, err := r.Latest(ctx, e.RequestID)
	if err != nil {
		return false, fmt.Errorf("%w: %v", ErrJournalWrite, err)
	}
	switch {
	case ok && cur.State == LegCaptured:
		return false, nil
	case ok && cur.State == LegAsked:
		return true, nil
	}
	e.State = LegAsked
	if err := r.Append(ctx, e); err != nil {
		return false, err
	}
	return true, nil
}

// SettleLegsTx closes the legs of one sale inside the sale's own transaction:
// one settled row per request id carrying the sale id, unless the leg already
// has a settled row for this sale (the receipt-number retry re-runs the whole
// transaction). Amount, method, currency, reference and reader are copied from
// the leg's newest row. An id with no row is skipped: nothing was journaled
// for it.
func (r *PaymentLegJournalRepo) SettleLegsTx(ctx context.Context, tx *sql.Tx, saleID string, requestIDs []string) error {
	now := time.Now()
	for _, id := range requestIDs {
		if id == "" {
			continue
		}
		cur, err := latestLeg(ctx, tx, id)
		if err != nil {
			return fmt.Errorf("settle payment leg: %w", err)
		}
		if cur.State == "" || (cur.State == LegSettled && cur.SaleID == saleID) {
			continue
		}
		cur.State, cur.SaleID, cur.Reason = LegSettled, saleID, ""
		if err := insertLeg(ctx, tx, cur, now); err != nil {
			return err
		}
	}
	return nil
}

type legQuerier interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// latestLeg returns the newest row for id; State is "" when there is none.
func latestLeg(ctx context.Context, q legQuerier, id string) (LegEntry, error) {
	e, err := scanLeg(q.QueryRowContext(ctx, `SELECT `+legCols+` FROM payment_leg_journal WHERE request_id = ? ORDER BY seq DESC LIMIT 1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return LegEntry{}, nil
	}
	return e, err
}

// Latest returns the newest row for the leg; ok is false when there is none.
func (r *PaymentLegJournalRepo) Latest(ctx context.Context, id string) (LegEntry, bool, error) {
	e, err := latestLeg(ctx, r.db, id)
	if err != nil {
		return LegEntry{}, false, fmt.Errorf("read payment leg journal: %w", err)
	}
	return e, e.State != "", nil
}

// listNewest returns, per leg, the newest row when its state is one of
// states, oldest first, at most legJournalScanMax rows.
func (r *PaymentLegJournalRepo) listNewest(ctx context.Context, states []string) ([]LegEntry, error) {
	if len(states) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(states)+1)
	for _, s := range states {
		args = append(args, s)
	}
	args = append(args, legJournalScanMax)
	rows, err := r.db.QueryContext(ctx, `
SELECT `+legCols+` FROM payment_leg_journal j
WHERE j.seq = (SELECT MAX(seq) FROM payment_leg_journal WHERE request_id = j.request_id)
  AND j.state IN (`+strings.TrimSuffix(strings.Repeat("?,", len(states)), ",")+`)
ORDER BY j.seq LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list payment leg journal: %w", err)
	}
	defer rows.Close()
	var out []LegEntry
	for rows.Next() {
		e, err := scanLeg(rows)
		if err != nil {
			return nil, fmt.Errorf("scan payment leg journal: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// PendingRefundSales returns the original sale ids (among saleIDs) that have a
// refund leg whose newest state is unknown or warned: the provider may have
// paid the customer back but the outcome is not settled (ut-docs#7064). One
// sargable range scan over the refund key prefix (the open legs are a small
// manager attention list), parsed in Go, capped at legJournalScanMax.
func (r *PaymentLegJournalRepo) PendingRefundSales(ctx context.Context, saleIDs []string) (map[string]bool, error) {
	out := map[string]bool{}
	if len(saleIDs) == 0 {
		return out, nil
	}
	want := make(map[string]bool, len(saleIDs))
	for _, id := range saleIDs {
		want[id] = true
	}
	// ';' is ':' + 1, so [prefix, upper) is exactly the keys starting "refund:".
	upper := strings.TrimSuffix(LegRefundKeyPrefix, ":") + ";"
	rows, err := r.db.QueryContext(ctx, `
SELECT j.request_id FROM payment_leg_journal j
WHERE j.request_id >= ? AND j.request_id < ?
  AND j.state IN ('unknown','warned')
  AND NOT EXISTS (SELECT 1 FROM payment_leg_journal n WHERE n.request_id = j.request_id AND n.seq > j.seq)
ORDER BY j.seq DESC LIMIT ?`, LegRefundKeyPrefix, upper, legJournalScanMax)
	if err != nil {
		return nil, fmt.Errorf("list pending refund legs: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("scan pending refund leg: %w", err)
		}
		if id, ok := RefundLegOriginalSale(key); ok && want[id] {
			out[id] = true
		}
	}
	return out, rows.Err()
}

// ListCurrent returns, per leg, the newest row when its state is one of
// states, oldest first, at most limit rows (capped at legJournalScanMax):
// the manager's attention list (ut-docs#6653 slice 3).
func (r *PaymentLegJournalRepo) ListCurrent(ctx context.Context, states []string, limit int) ([]LegEntry, error) {
	rows, err := r.listNewest(ctx, states)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > legJournalScanMax {
		limit = legJournalScanMax
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

// BeginRetry opens a manager's Retry of a reverse_failed leg, in one
// transaction: it writes the reversing row (a copy of the failed row, no
// reason) and returns that failed row and the retry's counter n, the number
// of reversing rows the leg now has. A reverse_failed leg always has at least
// one earlier reversing row, so the first Retry is 2 and never collides with
// the live send's 1 (ADR-0136 Decision 9.6). Any state but reverse_failed is
// ErrLegJournalTransition, reversing included, so a second Retry while one is
// in flight is refused (Append's same-state no-op would let it through, which
// is why Retry never goes through Append). No row is ErrLegJournalNotFound.
// Any other failure is an ErrJournalWrite and nothing was written: the caller
// must not send.
func (r *PaymentLegJournalRepo) BeginRetry(ctx context.Context, requestID string) (cur LegEntry, n int, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return LegEntry{}, 0, fmt.Errorf("%w: begin: %v", ErrJournalWrite, err)
	}
	defer func() { _ = tx.Rollback() }()
	cur, err = latestLeg(ctx, tx, requestID)
	if err != nil {
		return LegEntry{}, 0, fmt.Errorf("%w: read: %v", ErrJournalWrite, err)
	}
	if cur.State == "" {
		return LegEntry{}, 0, ErrLegJournalNotFound
	}
	if cur.State != LegReverseFailed {
		return LegEntry{}, 0, fmt.Errorf("%w: %q -> %q", ErrLegJournalTransition, cur.State, LegReversing)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM payment_leg_journal WHERE request_id = ? AND state = ?`, requestID, LegReversing).Scan(&count); err != nil {
		return LegEntry{}, 0, fmt.Errorf("%w: count: %v", ErrJournalWrite, err)
	}
	next := cur
	next.State, next.Reason = LegReversing, ""
	if err := insertLeg(ctx, tx, next, time.Now()); err != nil {
		return LegEntry{}, 0, err
	}
	if err := tx.Commit(); err != nil {
		return LegEntry{}, 0, fmt.Errorf("%w: commit: %v", ErrJournalWrite, err)
	}
	return cur, count + 1, nil
}

// ListBefore returns, per leg, the newest row when its state is one of
// states and it was written before cutoff, oldest first, at most limit rows
// (capped at legJournalScanMax). Times are compared parsed, in Go.
func (r *PaymentLegJournalRepo) ListBefore(ctx context.Context, cutoff time.Time, states []string, limit int) ([]LegEntry, error) {
	if limit <= 0 || limit > legJournalScanMax {
		limit = legJournalScanMax
	}
	rows, err := r.listNewest(ctx, states)
	if err != nil {
		return nil, err
	}
	var out []LegEntry
	for _, e := range rows {
		if e.At.Before(cutoff) && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}
