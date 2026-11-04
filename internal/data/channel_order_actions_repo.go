package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Channel order actions (ADR-0169 §9, ut-docs#4768 slice D, migration 096):
// the main till's outbox of staff actions on channel orders. A tap writes a
// row here and an audit row in ONE transaction. It never calls the network,
// never changes channel_orders.status and never moves stock: the order changes
// only when a revision is applied (the answer's `current`, or a
// channel_order_update directive).
//
// Row states: pending (to send), refused (answered "refused", kept to show the
// reason until dismissed or superseded), noted (an applied ship/cancel whose
// channel takes no write-back: kept to show the "do it in the channel's admin"
// note), blocked (the cloud refused this till: kept until staff dismiss).

var (
	// ErrChannelOrderNotFound: the order id names no channel order.
	ErrChannelOrderNotFound = errors.New("channel order not found")
	// ErrChannelActionNotAllowed: the action is impossible for the order's
	// state (or its data does not fit the action).
	ErrChannelActionNotAllowed = errors.New("channel order action not allowed")
	// ErrChannelActionsBlocked: the cloud refused this till's actions and
	// staff have not dismissed the unsent ones yet.
	ErrChannelActionsBlocked = errors.New("channel order actions blocked")
)

// ChannelShipment is the shop-entered shipment data of a ship action.
type ChannelShipment struct{ Carrier, TrackingNumber, TrackingURL string }

// ChannelCancel is the cancel data of a cancel action.
type ChannelCancel struct {
	Reason  string
	Restock bool
}

// ChannelOrderActionInput is one tap. Shipment is for ship only, Cancel for
// cancel only. StaffID is the operator's opaque id, "" when auth is off.
type ChannelOrderActionInput struct {
	OrderID, Action, StaffID string
	Shipment                 *ChannelShipment
	Cancel                   *ChannelCancel
	Now                      time.Time
}

// ChannelOrderAction is a stored outbox row.
type ChannelOrderAction struct {
	Seq                                                      int64
	ActionID, OrderID, ConnectionID, ExternalOrderID, Action string
	BaseRevision                                             int64
	CreatedAt, StaffID                                       string
	Shipment                                                 *ChannelShipment
	Cancel                                                   *ChannelCancel
	State, ResultCode, Writeback, AnsweredAt                 string
}

// Action states.
const (
	ChannelActionPending = "pending"
	ChannelActionRefused = "refused"
	ChannelActionNoted   = "noted"
	ChannelActionBlocked = "blocked"
)

// channelActionRank is the fulfilment step an action leads to.
var channelActionRank = map[string]int{"accept": 1, "pick": 2, "pack": 3, "ship": 4}

// ChannelOrderAllowedActions is the subset of accept, pick, pack, ship, cancel
// that may be queued for an order, in display order (contract rules 1 to 3
// judged on what the till knows). status and rank are the order's stored
// status and fulfilment_rank; pending are the actions already queued for it.
// A queued accept lets pick, pack, ship and cancel be queued behind it; a
// queued ship or cancel ends the chain. A step already reached is hidden (the
// cloud would answer it as a no-op).
func ChannelOrderAllowedActions(status string, rank int, pending []string) []string {
	if status == "cancelled" || status == "refunded" {
		return nil
	}
	eff := rank
	for _, p := range pending {
		if p == "cancel" {
			return nil
		}
		if r := channelActionRank[p]; r > eff {
			eff = r
		}
	}
	var out []string
	if eff == 0 {
		return []string{"accept", "cancel"}
	}
	if eff < 2 {
		out = append(out, "pick")
	}
	if eff < 3 {
		out = append(out, "pack")
	}
	if eff < 4 {
		out = append(out, "ship", "cancel")
	}
	return out
}

// ChannelOrderActionsRepo owns the channel_order_actions table.
type ChannelOrderActionsRepo struct{ db *sql.DB }

func NewChannelOrderActionsRepo(db *sql.DB) *ChannelOrderActionsRepo {
	return &ChannelOrderActionsRepo{db: db}
}

// Enqueue queues one action for an order in one transaction with its audit
// row. A second tap on an action that is already pending returns the existing
// id with duplicate=true and writes nothing. Refused/noted rows of the order
// are superseded (deleted). It never touches the order or stock.
func (r *ChannelOrderActionsRepo) Enqueue(ctx context.Context, in ChannelOrderActionInput) (actionID string, duplicate bool, err error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var blocked int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_order_actions WHERE state = 'blocked'`).Scan(&blocked); err != nil {
		return "", false, fmt.Errorf("count blocked channel actions: %w", err)
	}
	if blocked > 0 {
		return "", false, ErrChannelActionsBlocked
	}
	var conn, ext, status string
	var revision int64
	var rank int
	err = tx.QueryRowContext(ctx, `SELECT connection_id, external_order_id, revision, status, fulfilment_rank FROM channel_orders WHERE id = ?`, in.OrderID).
		Scan(&conn, &ext, &revision, &status, &rank)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, ErrChannelOrderNotFound
	}
	if err != nil {
		return "", false, fmt.Errorf("read channel order: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `SELECT action_id, action FROM channel_order_actions WHERE order_id = ? AND state = 'pending' ORDER BY seq`, in.OrderID)
	if err != nil {
		return "", false, fmt.Errorf("read pending channel actions: %w", err)
	}
	var pending []string
	for rows.Next() {
		var id, a string
		if err := rows.Scan(&id, &a); err != nil {
			rows.Close()
			return "", false, err
		}
		if a == in.Action {
			rows.Close()
			return id, true, nil
		}
		pending = append(pending, a)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", false, err
	}
	rows.Close()

	if (in.Action != "ship" && in.Shipment != nil) || (in.Action != "cancel" && in.Cancel != nil) ||
		(in.Action == "ship" && in.Shipment == nil) || (in.Action == "cancel" && in.Cancel == nil) {
		return "", false, ErrChannelActionNotAllowed
	}
	ok := false
	for _, a := range ChannelOrderAllowedActions(status, rank, pending) {
		if a == in.Action {
			ok = true
		}
	}
	if !ok {
		return "", false, ErrChannelActionNotAllowed
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM channel_order_actions WHERE order_id = ? AND state IN ('refused','noted')`, in.OrderID); err != nil {
		return "", false, fmt.Errorf("supersede channel outcomes: %w", err)
	}
	now := in.Now.UTC().Format(time.RFC3339)
	id := uuid.NewString()
	var sh ChannelShipment
	if in.Shipment != nil {
		sh = *in.Shipment
	}
	var cr string
	var restock int
	if in.Cancel != nil {
		cr, restock = in.Cancel.Reason, boolInt(in.Cancel.Restock)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO channel_order_actions (action_id, order_id, connection_id, external_order_id, action, base_revision, created_at, staff_id,
  carrier, tracking_number, tracking_url, cancel_reason, cancel_restock)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, in.OrderID, conn, ext, in.Action, revision, now, in.StaffID, sh.Carrier, sh.TrackingNumber, sh.TrackingURL, cr, restock); err != nil {
		return "", false, fmt.Errorf("insert channel order action: %w", err)
	}
	// Ids only: no tracking values in the audit log.
	if err := NewPOSRepo(r.db).InsertAudit(ctx, tx, in.StaffID, "channel_order", in.OrderID, "channel_order_action_queued",
		map[string]any{"action_id": id, "action": in.Action}, now, ""); err != nil {
		return "", false, err
	}
	if err := tx.Commit(); err != nil {
		return "", false, fmt.Errorf("commit channel order action: %w", err)
	}
	return id, false, nil
}

const channelActionSelect = `
SELECT seq, action_id, order_id, connection_id, external_order_id, action, base_revision, created_at, staff_id,
  carrier, tracking_number, tracking_url, cancel_reason, cancel_restock, state, result_code, writeback, answered_at
FROM channel_order_actions a`

func scanChannelAction(s rowScanner) (ChannelOrderAction, error) {
	var a ChannelOrderAction
	var carrier, num, url, reason string
	var restock int
	if err := s.Scan(&a.Seq, &a.ActionID, &a.OrderID, &a.ConnectionID, &a.ExternalOrderID, &a.Action, &a.BaseRevision, &a.CreatedAt,
		&a.StaffID, &carrier, &num, &url, &reason, &restock, &a.State, &a.ResultCode, &a.Writeback, &a.AnsweredAt); err != nil {
		return a, err
	}
	switch a.Action {
	case "ship":
		a.Shipment = &ChannelShipment{Carrier: carrier, TrackingNumber: num, TrackingURL: url}
	case "cancel":
		a.Cancel = &ChannelCancel{Reason: reason, Restock: restock != 0}
	}
	return a, nil
}

func (r *ChannelOrderActionsRepo) query(ctx context.Context, q string, args ...any) ([]ChannelOrderAction, error) {
	rows, err := r.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("channel order actions: %w", err)
	}
	defer rows.Close()
	var out []ChannelOrderAction
	for rows.Next() {
		a, err := scanChannelAction(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ForOrder returns every row of the order, oldest first.
func (r *ChannelOrderActionsRepo) ForOrder(ctx context.Context, orderID string) ([]ChannelOrderAction, error) {
	return r.query(ctx, channelActionSelect+` WHERE a.order_id = ? ORDER BY a.seq`, orderID)
}

// Blocked counts the blocked rows and returns the code of the newest one.
func (r *ChannelOrderActionsRepo) Blocked(ctx context.Context) (count int, code string, err error) {
	if err = r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_order_actions WHERE state = 'blocked'`).Scan(&count); err != nil || count == 0 {
		return 0, "", err
	}
	err = r.db.QueryRowContext(ctx, `SELECT result_code FROM channel_order_actions WHERE state = 'blocked' ORDER BY seq DESC LIMIT 1`).Scan(&code)
	return count, code, err
}

// HasSendable reports whether any row waits to be sent.
func (r *ChannelOrderActionsRepo) HasSendable(ctx context.Context) (bool, error) {
	var one int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM channel_order_actions WHERE state = 'pending' LIMIT 1`).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// SendableBatch returns the pending rows that are the oldest pending row of
// their order, oldest first: at most one action per order (the contract's
// "one in flight per order").
func (r *ChannelOrderActionsRepo) SendableBatch(ctx context.Context, limit int) ([]ChannelOrderAction, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	return r.query(ctx, channelActionSelect+`
WHERE a.state = 'pending' AND a.seq = (SELECT MIN(b.seq) FROM channel_order_actions b WHERE b.order_id = a.order_id AND b.state = 'pending')
ORDER BY a.seq LIMIT ?`, limit)
}

// AnswerApplied records an "applied" answer: the order's older refused/noted
// rows go, this row is deleted (or kept as noted for a ship/cancel whose
// channel takes no write-back), and the order's later pending rows are rebased
// onto the answer's revision. A missing row is not an error.
func (r *ChannelOrderActionsRepo) AnswerApplied(ctx context.Context, actionID string, revision int64, writeback string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var orderID, action string
	err = tx.QueryRowContext(ctx, `SELECT order_id, action FROM channel_order_actions WHERE action_id = ?`, actionID).Scan(&orderID, &action)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read channel action: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM channel_order_actions WHERE order_id = ? AND state IN ('refused','noted') AND action_id <> ?`, orderID, actionID); err != nil {
		return fmt.Errorf("supersede channel outcomes: %w", err)
	}
	if (action == "ship" || action == "cancel") && writeback == "skipped" {
		_, err = tx.ExecContext(ctx, `UPDATE channel_order_actions SET state='noted', writeback=?, answered_at=? WHERE action_id = ?`,
			writeback, time.Now().UTC().Format(time.RFC3339), actionID)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM channel_order_actions WHERE action_id = ?`, actionID)
	}
	if err != nil {
		return fmt.Errorf("record applied channel action: %w", err)
	}
	if revision >= 1 {
		if _, err := tx.ExecContext(ctx, `UPDATE channel_order_actions SET base_revision = ? WHERE order_id = ? AND state = 'pending' AND base_revision < ?`,
			revision, orderID, revision); err != nil {
			return fmt.Errorf("rebase channel actions: %w", err)
		}
	}
	return tx.Commit()
}

// AnswerRefused records a "refused" answer: the order's older refused/noted
// rows go and this row is kept as refused with the cloud's code. No rebase.
func (r *ChannelOrderActionsRepo) AnswerRefused(ctx context.Context, actionID, code string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var orderID string
	err = tx.QueryRowContext(ctx, `SELECT order_id FROM channel_order_actions WHERE action_id = ?`, actionID).Scan(&orderID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read channel action: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM channel_order_actions WHERE order_id = ? AND state IN ('refused','noted') AND action_id <> ?`, orderID, actionID); err != nil {
		return fmt.Errorf("supersede channel outcomes: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE channel_order_actions SET state='refused', result_code=?, answered_at=? WHERE action_id = ?`,
		code, time.Now().UTC().Format(time.RFC3339), actionID); err != nil {
		return fmt.Errorf("record refused channel action: %w", err)
	}
	return tx.Commit()
}

// BlockPending sets every pending row to blocked with the cloud's refusal
// code (the till is not the main till, or must be paired again) and returns
// how many.
func (r *ChannelOrderActionsRepo) BlockPending(ctx context.Context, code string) (int, error) {
	res, err := r.db.ExecContext(ctx, `UPDATE channel_order_actions SET state='blocked', result_code=?, answered_at=? WHERE state = 'pending'`,
		code, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, fmt.Errorf("block channel actions: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// Dismiss deletes one row of the order that is not pending (a refusal, a
// note or a blocked row) and audits it. It reports whether a row went.
func (r *ChannelOrderActionsRepo) Dismiss(ctx context.Context, orderID, actionID, actorID string) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM channel_order_actions WHERE order_id = ? AND action_id = ? AND state <> 'pending'`, orderID, actionID)
	if err != nil {
		return false, fmt.Errorf("dismiss channel action: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if err := NewPOSRepo(r.db).InsertAudit(ctx, tx, actorID, "channel_order", orderID, "channel_order_action_dismissed",
		map[string]any{"action_id": actionID}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// DismissBlocked deletes every blocked row and audits the count.
func (r *ChannelOrderActionsRepo) DismissBlocked(ctx context.Context, actorID string) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `DELETE FROM channel_order_actions WHERE state = 'blocked'`)
	if err != nil {
		return 0, fmt.Errorf("dismiss blocked channel actions: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return 0, nil
	}
	if err := NewPOSRepo(r.db).InsertAudit(ctx, tx, actorID, "channel_order", "", "channel_order_actions_blocked_dismissed",
		map[string]any{"count": n}, time.Now().UTC().Format(time.RFC3339), ""); err != nil {
		return 0, err
	}
	return int(n), tx.Commit()
}

// SanitizeActionCode keeps a cloud-supplied result code to a short
// [a-z0-9_] token so a hostile answer cannot put text in the page or a log.
func SanitizeActionCode(code string) string {
	if code == "" || len(code) > 40 {
		return "unknown"
	}
	if strings.Trim(code, "abcdefghijklmnopqrstuvwxyz0123456789_") != "" {
		return "unknown"
	}
	return code
}
