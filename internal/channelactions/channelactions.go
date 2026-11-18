// Package channelactions is the till side of the cloud's channel order
// actions (ADR-0169 §9, ut-docs#4768 slice D; reference/contracts/
// channel-order-action.md). The main till queues each staff tap (accept, pick,
// pack, ship, cancel) in the channel_order_actions outbox; Run uploads the
// queue to POST /api/v1/stores/channel/orders/actions and applies the order
// payload (`current`) each answer carries through the till's normal revision
// rule.
//
// Nothing on the request path calls the network: a tap only writes the row and
// an audit row. An unreachable cloud means the rows wait, and a lost answer is
// harmless because the retry re-sends the same action_id and body (the
// cloud's idempotency key). The order's status and the stock change only when
// a revision is applied, never when an action is queued or answered.
//
// Contract rules Run keeps: at most 50 actions per call, at most ONE action per
// order per call (the next one is sent after the previous is answered, its
// base_revision rebased to that answer's revision), an action older than 30
// days is sent with stale: true (decided at each send), and a refused answer
// is never retried.
//
// Who calls Run, and when, is internal/cloudsync's (after the check-in answer,
// before that tick's channel_order(_update) directives are applied).
package channelactions

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
)

const (
	// MaxActions is the most actions in one call (the contract's bound).
	MaxActions = 50
	// maxCallsPerRun bounds one Run (500 actions); more continue on the next trigger.
	maxCallsPerRun = 10
	// StaleAfter is the age after which an action is sent with stale: true.
	StaleAfter = 30 * 24 * time.Hour
)

// Shipment is the wire form of a ship action's shipment.
type Shipment struct {
	Carrier        string `json:"carrier"`
	TrackingNumber string `json:"tracking_number,omitempty"`
	TrackingURL    string `json:"tracking_url,omitempty"`
}

// Cancel is the wire form of a cancel action's data.
type Cancel struct {
	Reason  string `json:"reason"`
	Restock bool   `json:"restock"`
}

// Action is one action as channel-order-action-v1.json takes it.
type Action struct {
	ActionID        string    `json:"action_id"`
	ConnectionID    string    `json:"connection_id"`
	ExternalOrderID string    `json:"external_order_id"`
	BaseRevision    int64     `json:"base_revision"`
	Action          string    `json:"action"`
	CreatedAt       string    `json:"created_at"`
	StaffID         string    `json:"staff_id"`
	Stale           bool      `json:"stale,omitempty"`
	Shipment        *Shipment `json:"shipment,omitempty"`
	Cancel          *Cancel   `json:"cancel,omitempty"`
}

// Result is the cloud's verdict on one action, in request order. Current is
// the order's full payload at the cloud's present revision (absent only for
// unknown_order).
type Result struct {
	ActionID  string         `json:"action_id"`
	Status    string         `json:"status"`
	Code      string         `json:"code"`
	Revision  int64          `json:"revision"`
	Writeback string         `json:"writeback"`
	Current   map[string]any `json:"current"`
}

// Poster sends one call. A *StopError means the cloud will not take actions
// from this till (the rows are blocked); any other error is transient and the
// rows stay pending.
type Poster func(ctx context.Context, actions []Action) ([]Result, error)

// ApplyFunc applies an answer's `current` through the till's revision rule
// (cloudsync's channel_order_update path). An error that wraps
// ErrCurrentRefused is final (the payload itself is bad: the next directive
// brings the order); any other error is transient.
type ApplyFunc func(ctx context.Context, actionID string, current map[string]any) error

var (
	// ErrCurrentRefused wraps the decoder's refusal of an answer's `current`.
	ErrCurrentRefused = errors.New("channel_action_current_refused")
	// ErrAnswerMismatch: the answer does not match the request (count, ids,
	// an unknown status). Nothing is recorded; the rows stay pending.
	ErrAnswerMismatch = errors.New("channel_action_answer_mismatch")
)

// StopError is a refusal that retrying cannot change: 403 main_till_required,
// store_mismatch or device_credential_required.
type StopError struct {
	Status int
	Code   string
}

func (e *StopError) Error() string {
	return fmt.Sprintf("channel order actions not accepted (%d %s)", e.Status, e.Code)
}

// BadRequestError is a 400: the envelope was malformed, which is a till bug
// (a bad single action is a per-action refusal, never a 400).
type BadRequestError struct{ Code string }

func (e *BadRequestError) Error() string {
	return fmt.Sprintf("channel order actions answered 400 %s", e.Code)
}

// StatusError is any other non-200 answer (401, 402, 404, 405, 409, 5xx, an
// unlisted 403).
type StatusError struct {
	Status int
	Code   string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("channel order actions answered %d %s", e.Status, e.Code)
}

// Summary is what one Run did.
type Summary struct {
	Sent, Applied, Refused, Noted, Blocked int
	// Busy: another Run was in flight; this one did nothing.
	Busy bool
	// Stopped / StopStatus: the cloud's refusal code, when it refused; the
	// pending rows were blocked (Blocked counts them).
	Stopped    string
	StopStatus int
}

var running sync.Mutex

// ToWire converts a stored row to the wire. Stale is decided here, at each
// send: an unparseable created_at is not stale (the cloud judges it).
func ToWire(a data.ChannelOrderAction, now time.Time) Action {
	w := Action{ActionID: a.ActionID, ConnectionID: a.ConnectionID, ExternalOrderID: a.ExternalOrderID,
		BaseRevision: a.BaseRevision, Action: a.Action, CreatedAt: a.CreatedAt, StaffID: a.StaffID}
	if t, err := time.Parse(time.RFC3339, a.CreatedAt); err == nil && now.Sub(t) > StaleAfter {
		w.Stale = true
	}
	if a.Shipment != nil {
		w.Shipment = &Shipment{Carrier: a.Shipment.Carrier, TrackingNumber: a.Shipment.TrackingNumber, TrackingURL: a.Shipment.TrackingURL}
	}
	if a.Cancel != nil {
		w.Cancel = &Cancel{Reason: a.Cancel.Reason, Restock: a.Cancel.Restock}
	}
	return w
}

// Run uploads the pending actions: up to MaxActions per call, one per order,
// until nothing is pending, the cloud refuses (Summary.Stopped; pending rows
// blocked), an error ends it (returned; rows kept), or maxCallsPerRun is
// reached. One Run at a time: a second returns Busy at once. The caller
// bounds Run's total time with ctx.
func Run(ctx context.Context, db *sql.DB, post Poster, apply ApplyFunc, now func() time.Time) (Summary, error) {
	var sum Summary
	if !running.TryLock() {
		sum.Busy = true
		return sum, nil
	}
	defer running.Unlock()
	repo := data.NewChannelOrderActionsRepo(db)
	for call := 0; call < maxCallsPerRun; call++ {
		if err := ctx.Err(); err != nil {
			return sum, err
		}
		batch, err := repo.SendableBatch(ctx, MaxActions)
		if err != nil {
			return sum, err
		}
		if len(batch) == 0 {
			break
		}
		t := now()
		wire := make([]Action, len(batch))
		for i, b := range batch {
			wire[i] = ToWire(b, t)
		}
		results, err := post(ctx, wire)
		var stop *StopError
		if errors.As(err, &stop) {
			n, berr := repo.BlockPending(ctx, stop.Code)
			sum.Stopped, sum.StopStatus, sum.Blocked = stop.Code, stop.Status, n
			return sum, berr
		}
		if err != nil {
			return sum, err
		}
		if len(results) != len(batch) {
			return sum, fmt.Errorf("%w: %d answers for %d actions", ErrAnswerMismatch, len(results), len(batch))
		}
		for i, r := range results {
			if r.ActionID != batch[i].ActionID {
				return sum, fmt.Errorf("%w: answer %d is for another action", ErrAnswerMismatch, i)
			}
			if r.Status != "applied" && r.Status != "refused" {
				return sum, fmt.Errorf("%w: unknown status for answer %d", ErrAnswerMismatch, i)
			}
		}
		sum.Sent += len(batch)
		for i, r := range results {
			if r.Current != nil {
				// Apply first, but the verdict never depends on it: a persistent
				// apply failure (no stock location, a failing validation) must not
				// stall this and every later verdict, nor re-send the batch forever.
				// The next channel_order_update brings the same revision again. Only
				// a cancelled context stops the batch (the rows stay pending).
				if err := apply(ctx, r.ActionID, r.Current); err != nil {
					if ctx.Err() != nil {
						return sum, err
					}
					if !errors.Is(err, ErrCurrentRefused) {
						logging.L().Warnf("channelactions: applying the cloud's current order for action %s failed (%v); the verdict is still recorded", r.ActionID, err)
					}
				}
			}
			switch r.Status {
			case "applied":
				if err := repo.AnswerApplied(ctx, r.ActionID, r.Revision, r.Writeback); err != nil {
					return sum, err
				}
				sum.Applied++
				if a := batch[i].Action; r.Writeback == "skipped" && (a == "ship" || a == "cancel") {
					sum.Noted++
				}
			case "refused":
				if err := repo.AnswerRefused(ctx, r.ActionID, data.SanitizeActionCode(r.Code)); err != nil {
					return sum, err
				}
				sum.Refused++
			}
		}
	}
	return sum, nil
}
