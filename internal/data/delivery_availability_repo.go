package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Neutral availability signals for delivery channels (ADR-0215 decision 2,
// ADR-0201 §2, ut-docs#6583, migration 124). Two things a shop tells the till
// that every connected delivery channel should follow:
//
//   - an item is "unavailable until" a time (the 86): items.unavailable_until;
//   - the store is open or paused (busy mode), with an optional store prep
//     time: settings delivery.service_*.
//
// Each staff change is audited and queues one neutral signal in
// delivery_signals (availability.set / service.set) that rides to the cloud
// with the check-in; the cloud's adapter maps it to the platform. The till is
// the truth: Reassert re-queues the till's whole state so a provider-side
// unsnooze is put back after the reconcile tick. NOTHING in the sale path
// reads these fields: an unavailable item can always be sold, so the network
// or a channel can never block checkout.

const (
	// SignalAvailabilitySet and SignalServiceSet are delivery_signals.kind.
	SignalAvailabilitySet = "availability.set"
	SignalServiceSet      = "service.set"
	// UnavailableIndefinitely is items.unavailable_until for "until staff turn
	// it back on".
	UnavailableIndefinitely = "9999-12-31T23:59:59Z"

	// ServiceOpen and ServicePaused are the store's service states.
	ServiceOpen   = "open"
	ServicePaused = "paused"

	serviceStateKey   = "delivery.service_state"
	serviceReasonKey  = "delivery.service_reason"
	serviceUntilKey   = "delivery.service_until"
	servicePrepKey    = "delivery.service_prep_minutes"
	maxServiceReason  = 64
	signalBatchLimit  = 200
	unavailableLayout = time.RFC3339
)

// ServiceState is the store's delivery service state. Reason is a short
// machine token or word ("busy", "till_closed"), never customer text; Until is
// "" or a UTC RFC 3339 instant; PrepMinutes is the store-wide prep time (0 =
// not set).
type ServiceState struct {
	State       string
	Reason      string
	Until       string
	PrepMinutes int64
}

// UnavailableItem is an item currently marked unavailable.
type UnavailableItem struct {
	ItemID string
	SKU    string
	Until  string
}

// AvailabilitySnapshot is the till's whole state, for a reconcile.
type AvailabilitySnapshot struct {
	Unavailable []UnavailableItem
	Service     ServiceState
}

// DeliverySignal is a stored outbox row.
type DeliverySignal struct {
	Seq         int64
	SignalID    string
	Kind        string
	PayloadJSON string
	CreatedAt   string
	ActorID     string
}

// DeliveryAvailabilityRepo owns items.unavailable_until, the delivery.service_*
// settings and the delivery_signals outbox.
type DeliveryAvailabilityRepo struct{ db *sql.DB }

func NewDeliveryAvailabilityRepo(db *sql.DB) *DeliveryAvailabilityRepo {
	return &DeliveryAvailabilityRepo{db: db}
}

type signalItem struct {
	ItemID    string `json:"item_id"`
	SKU       string `json:"sku"`
	Available bool   `json:"available"`
	Until     string `json:"until,omitempty"`
}

func (r *DeliveryAvailabilityRepo) queue(ctx context.Context, tx *sql.Tx, kind string, payload any, actor, now string) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode %s: %w", kind, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO delivery_signals (signal_id, kind, payload_json, created_at, actor_id) VALUES (?,?,?,?,?)`,
		uuid.NewString(), kind, string(b), now, actor); err != nil {
		return fmt.Errorf("queue %s: %w", kind, err)
	}
	return nil
}

// SetItemUnavailable marks an item unavailable until `until` (the zero time =
// until staff turn it back on). It refuses an unknown item and a time that is
// not after now. Audited with the staff id; queues one availability.set.
func (r *DeliveryAvailabilityRepo) SetItemUnavailable(ctx context.Context, itemID string, until time.Time, actorID string, now time.Time) error {
	value := UnavailableIndefinitely
	if !until.IsZero() {
		if !until.After(now) {
			return errors.New("unavailable until must be in the future")
		}
		value = until.UTC().Format(unavailableLayout)
	}
	return r.setItem(ctx, itemID, value, actorID, "item_unavailable_set", now)
}

// ClearItemUnavailable makes an item available again (audited; queues an
// availability.set with available=true).
func (r *DeliveryAvailabilityRepo) ClearItemUnavailable(ctx context.Context, itemID, actorID string, now time.Time) error {
	return r.setItem(ctx, itemID, "", actorID, "item_unavailable_cleared", now)
}

func (r *DeliveryAvailabilityRepo) setItem(ctx context.Context, itemID, value, actorID, auditAction string, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var sku string
	if err := tx.QueryRowContext(ctx, `SELECT sku FROM items WHERE id = ?`, itemID).Scan(&sku); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("item %q not found", itemID)
		}
		return fmt.Errorf("read item: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE items SET unavailable_until = ? WHERE id = ?`, value, itemID); err != nil {
		return fmt.Errorf("set unavailable_until: %w", err)
	}
	ts := now.UTC().Format(time.RFC3339)
	if err := r.queue(ctx, tx, SignalAvailabilitySet, map[string]any{"items": []signalItem{{ItemID: itemID, SKU: sku, Available: value == "", Until: untilForWire(value)}}}, actorID, ts); err != nil {
		return err
	}
	if err := NewPOSRepo(nil).InsertAudit(ctx, tx, actorID, "item", itemID, auditAction, map[string]any{"until": value}, ts, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// untilForWire is the signal's `until`: omitted for available and for the
// indefinite sentinel (an item off "until staff turn it on" has no end time).
func untilForWire(v string) string {
	if v == UnavailableIndefinitely {
		return ""
	}
	return v
}

// ExpireDue turns every item whose time has passed back to available and
// queues one availability.set for them (audited, system actor). The
// indefinite items stay. It returns how many were cleared.
func (r *DeliveryAvailabilityRepo) ExpireDue(ctx context.Context, now time.Time) (int, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ts := now.UTC().Format(unavailableLayout)
	rows, err := tx.QueryContext(ctx, `SELECT id, sku FROM items WHERE unavailable_until <> '' AND unavailable_until <> ? AND unavailable_until <= ?`, UnavailableIndefinitely, ts)
	if err != nil {
		return 0, fmt.Errorf("find expired unavailability: %w", err)
	}
	var due []signalItem
	for rows.Next() {
		var it signalItem
		if err := rows.Scan(&it.ItemID, &it.SKU); err != nil {
			rows.Close()
			return 0, err
		}
		it.Available = true
		due = append(due, it)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if len(due) == 0 {
		return 0, nil
	}
	for _, it := range due {
		if _, err := tx.ExecContext(ctx, `UPDATE items SET unavailable_until = '' WHERE id = ?`, it.ItemID); err != nil {
			return 0, fmt.Errorf("clear unavailable_until: %w", err)
		}
		if err := NewPOSRepo(nil).InsertAudit(ctx, tx, "", "item", it.ItemID, "item_unavailable_expired", map[string]any{}, ts, ""); err != nil {
			return 0, err
		}
	}
	if err := r.queue(ctx, tx, SignalAvailabilitySet, map[string]any{"items": due}, "", ts); err != nil {
		return 0, err
	}
	return len(due), tx.Commit()
}

// Service reads the store's service state (open when never set).
func (r *DeliveryAvailabilityRepo) Service(ctx context.Context) (ServiceState, error) {
	return readService(ctx, r.db)
}

func readService(ctx context.Context, q rowQuerier) (ServiceState, error) {
	st := ServiceState{State: ServiceOpen}
	get := func(key string) (string, error) {
		var v string
		err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return v, err
	}
	state, err := get(serviceStateKey)
	if err != nil {
		return st, fmt.Errorf("read service state: %w", err)
	}
	if state == ServicePaused {
		st.State = ServicePaused
	}
	if st.Reason, err = get(serviceReasonKey); err != nil {
		return st, err
	}
	if st.Until, err = get(serviceUntilKey); err != nil {
		return st, err
	}
	prep, err := get(servicePrepKey)
	if err != nil {
		return st, err
	}
	if prep != "" {
		st.PrepMinutes, _ = strconv.ParseInt(prep, 10, 64)
	}
	return st, nil
}

type servicePayload struct {
	State       string `json:"state"`
	Reason      string `json:"reason,omitempty"`
	Until       string `json:"until,omitempty"`
	PrepMinutes int64  `json:"prep_minutes,omitempty"`
}

func validateService(st ServiceState) error {
	if st.State != ServiceOpen && st.State != ServicePaused {
		return fmt.Errorf("service state %q is not open or paused", st.State)
	}
	if len(st.Reason) > maxServiceReason || strings.ContainsAny(st.Reason, "\r\n\x00") {
		return errors.New("service reason too long or not a single line")
	}
	if st.PrepMinutes < 0 || st.PrepMinutes > DeliveryMaxPrepMinutes {
		return errors.New("service prep minutes out of range")
	}
	if st.Until != "" {
		if _, err := time.Parse(time.RFC3339, st.Until); err != nil {
			return errors.New("service until is not an RFC 3339 time")
		}
	}
	return nil
}

// SetService stores the store's service state (pause / busy mode / store prep
// time), audited with the staff id, and queues one service.set.
func (r *DeliveryAvailabilityRepo) SetService(ctx context.Context, st ServiceState, actorID string, now time.Time) error {
	if err := validateService(st); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	settings := NewSettingsRepo(r.db)
	for k, v := range map[string]string{
		serviceStateKey: st.State, serviceReasonKey: st.Reason, serviceUntilKey: st.Until,
		servicePrepKey: strconv.FormatInt(st.PrepMinutes, 10),
	} {
		if err := settings.SetTx(ctx, tx, k, v); err != nil {
			return err
		}
	}
	ts := now.UTC().Format(time.RFC3339)
	if err := r.queue(ctx, tx, SignalServiceSet, servicePayload{State: st.State, Reason: st.Reason, Until: st.Until, PrepMinutes: st.PrepMinutes}, actorID, ts); err != nil {
		return err
	}
	if err := NewPOSRepo(nil).InsertAudit(ctx, tx, actorID, "store", "delivery_service", "delivery_service_set",
		map[string]any{"state": st.State, "reason": st.Reason, "until": st.Until, "prep_minutes": st.PrepMinutes}, ts, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// Snapshot is the till's whole availability state at `now`: the items still
// unavailable (an expired one is not listed even if ExpireDue has not run) and
// the service state.
func (r *DeliveryAvailabilityRepo) Snapshot(ctx context.Context, now time.Time) (AvailabilitySnapshot, error) {
	var snap AvailabilitySnapshot
	rows, err := r.db.QueryContext(ctx, `SELECT id, sku, unavailable_until FROM items
WHERE unavailable_until <> '' AND (unavailable_until = ? OR unavailable_until > ?) ORDER BY id`, UnavailableIndefinitely, now.UTC().Format(unavailableLayout))
	if err != nil {
		return snap, fmt.Errorf("list unavailable items: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var it UnavailableItem
		if err := rows.Scan(&it.ItemID, &it.SKU, &it.Until); err != nil {
			return snap, err
		}
		snap.Unavailable = append(snap.Unavailable, it)
	}
	if err := rows.Err(); err != nil {
		return snap, err
	}
	snap.Service, err = r.Service(ctx)
	return snap, err
}

// Reassert queues the till's whole state again (one availability.set for every
// unavailable item, if any, and one service.set): the reconcile tick calls it
// when a channel reports a different state, so the till wins on a
// provider-side unsnooze (ADR-0215 decision 2; stock follows the same rule,
// ADR-0169 §8). Audited as a system action.
func (r *DeliveryAvailabilityRepo) Reassert(ctx context.Context, now time.Time, actorID string) error {
	snap, err := r.Snapshot(ctx, now)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ts := now.UTC().Format(time.RFC3339)
	if len(snap.Unavailable) > 0 {
		items := make([]signalItem, 0, len(snap.Unavailable))
		for _, u := range snap.Unavailable {
			items = append(items, signalItem{ItemID: u.ItemID, SKU: u.SKU, Available: false, Until: untilForWire(u.Until)})
		}
		if err := r.queue(ctx, tx, SignalAvailabilitySet, map[string]any{"items": items}, actorID, ts); err != nil {
			return err
		}
	}
	s := snap.Service
	if err := r.queue(ctx, tx, SignalServiceSet, servicePayload{State: s.State, Reason: s.Reason, Until: s.Until, PrepMinutes: s.PrepMinutes}, actorID, ts); err != nil {
		return err
	}
	if err := NewPOSRepo(nil).InsertAudit(ctx, tx, actorID, "store", "delivery_service", "delivery_availability_reasserted",
		map[string]any{"unavailable_items": len(snap.Unavailable), "state": s.State}, ts, ""); err != nil {
		return err
	}
	return tx.Commit()
}

// PendingSignals returns the signals not yet sent, oldest first.
func (r *DeliveryAvailabilityRepo) PendingSignals(ctx context.Context, limit int) ([]DeliverySignal, error) {
	if limit <= 0 || limit > signalBatchLimit {
		limit = signalBatchLimit
	}
	rows, err := r.db.QueryContext(ctx, `SELECT seq, signal_id, kind, payload_json, created_at, actor_id FROM delivery_signals WHERE state = 'pending' ORDER BY seq LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("pending delivery signals: %w", err)
	}
	defer rows.Close()
	var out []DeliverySignal
	for rows.Next() {
		var s DeliverySignal
		if err := rows.Scan(&s.Seq, &s.SignalID, &s.Kind, &s.PayloadJSON, &s.CreatedAt, &s.ActorID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkSent records that the cloud took the signals.
func (r *DeliveryAvailabilityRepo) MarkSent(ctx context.Context, ids []string, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	ts := now.UTC().Format(time.RFC3339)
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `UPDATE delivery_signals SET state='sent', sent_at=? WHERE signal_id=? AND state='pending'`, ts, id); err != nil {
			return fmt.Errorf("mark signal sent: %w", err)
		}
	}
	return tx.Commit()
}
