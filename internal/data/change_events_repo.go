package data

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// The till's change-event outbox (ADR-0200, ut-docs#6559). Two kinds of
// event only: "version" (the till's software version changed) and "plugin"
// (a plugin was installed, updated or removed). They wait here until the next
// cloud sync POST carries them (oldest first, ChangeEventBatch at a time) and
// are deleted once the cloud answered 2xx or 402. Nothing in an event names a
// shop, a sale, a customer, a path or an error.
//
// The wire contract (ut-docs reference/contracts, ADR-0039) is mirrored here:
// ids are 32 hex characters, plugin ids match pluginIDRe, versions are at most
// 32 characters of versionRe, times are RFC 3339 UTC. An event the cloud would
// drop is refused here, so the outbox never holds dead weight.

// Event kinds and plugin actions on the wire.
const (
	ChangeKindVersion = "version"
	ChangeKindPlugin  = "plugin"

	// The till emits install, update and remove only: a download is only real
	// once installed. The cloud accepts "download" for forward use.
	ChangeActionInstall = "install"
	ChangeActionUpdate  = "update"
	ChangeActionRemove  = "remove"
)

const (
	// ChangeEventCap bounds the outbox: the newest events are kept.
	ChangeEventCap = 200
	// ChangeEventBatch is the most events one POST carries.
	ChangeEventBatch = 50
)

var (
	pluginIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	versionRe  = regexp.MustCompile(`^[A-Za-z0-9._+-]{0,32}$`)

	// ErrInvalidChangeEvent: the event does not fit the wire contract.
	ErrInvalidChangeEvent = errors.New("data: invalid change event")
)

// ChangeEvent is one outbox row.
type ChangeEvent struct {
	ID       string
	Kind     string
	PluginID string
	Action   string
	From     string
	To       string
	At       time.Time
}

func (e ChangeEvent) validate() error {
	if e.At.IsZero() {
		return fmt.Errorf("%w: no time", ErrInvalidChangeEvent)
	}
	if !versionRe.MatchString(e.From) || !versionRe.MatchString(e.To) {
		return fmt.Errorf("%w: version", ErrInvalidChangeEvent)
	}
	switch e.Kind {
	case ChangeKindVersion:
		if e.PluginID != "" || e.Action != "" {
			return fmt.Errorf("%w: version event carries plugin fields", ErrInvalidChangeEvent)
		}
	case ChangeKindPlugin:
		if !pluginIDRe.MatchString(e.PluginID) {
			return fmt.Errorf("%w: plugin id", ErrInvalidChangeEvent)
		}
		switch e.Action {
		case ChangeActionInstall, ChangeActionUpdate, ChangeActionRemove, "download":
		default:
			return fmt.Errorf("%w: action", ErrInvalidChangeEvent)
		}
	default:
		return fmt.Errorf("%w: kind", ErrInvalidChangeEvent)
	}
	return nil
}

// ChangeEventPolicy decides whether this till may record change events right
// now. get reads one settings value ("" when unset). The policy lives in
// internal/enroll (the same honesty guards as registration); data cannot
// import it, so it is injected once at start-up. With none wired nothing is
// recorded.
type ChangeEventPolicy func(get func(key string) string) bool

var changeEventPolicy atomic.Pointer[ChangeEventPolicy]

// SetChangeEventPolicy wires (or, with nil, clears) the recording policy.
func SetChangeEventPolicy(p ChangeEventPolicy) {
	if p == nil {
		changeEventPolicy.Store(nil)
		return
	}
	changeEventPolicy.Store(&p)
}

// ChangeEventsRepo is the outbox repository.
type ChangeEventsRepo struct{ db *sql.DB }

// NewChangeEventsRepo returns the outbox repository.
func NewChangeEventsRepo(db *sql.DB) *ChangeEventsRepo { return &ChangeEventsRepo{db: db} }

func (r *ChangeEventsRepo) executor(tx *sql.Tx) executor {
	if tx != nil {
		return tx
	}
	return r.db
}

func newChangeEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// Append stores one event (id generated when empty) and trims the outbox to
// ChangeEventCap, dropping the oldest, in the same transaction when tx is set.
// It does not consult the policy: the Record* helpers do.
func (r *ChangeEventsRepo) Append(ctx context.Context, tx *sql.Tx, ev ChangeEvent) error {
	return appendChangeEvent(ctx, r.executor(tx), ev)
}

var changeEventsDropLogged atomic.Bool

func appendChangeEvent(ctx context.Context, exec executor, ev ChangeEvent) error {
	if err := ev.validate(); err != nil {
		return err
	}
	if ev.ID == "" {
		id, err := newChangeEventID()
		if err != nil {
			return err
		}
		ev.ID = id
	}
	if _, err := exec.ExecContext(ctx, `
INSERT OR IGNORE INTO till_change_events (id, kind, plugin_id, action, from_version, to_version, at)
VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ev.ID, ev.Kind, ev.PluginID, ev.Action, ev.From, ev.To, ev.At.UTC().Format(time.RFC3339)); err != nil {
		return fmt.Errorf("change events: append: %w", err)
	}
	res, err := exec.ExecContext(ctx, `
DELETE FROM till_change_events
WHERE rowid NOT IN (SELECT rowid FROM till_change_events ORDER BY rowid DESC LIMIT ?)`, ChangeEventCap)
	if err != nil {
		return fmt.Errorf("change events: trim: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 && changeEventsDropLogged.CompareAndSwap(false, true) {
		logging.L().Infof("change events: outbox is full (%d); the oldest events are dropped until the till reaches the cloud", ChangeEventCap)
	}
	return nil
}

// ListPending returns up to limit events, oldest first.
func (r *ChangeEventsRepo) ListPending(ctx context.Context, limit int) ([]ChangeEvent, error) {
	if limit <= 0 || limit > ChangeEventCap {
		limit = ChangeEventCap
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT id, kind, plugin_id, action, from_version, to_version, at
FROM till_change_events ORDER BY rowid ASC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("change events: list: %w", err)
	}
	defer rows.Close()
	var out []ChangeEvent
	for rows.Next() {
		var ev ChangeEvent
		var at string
		if err := rows.Scan(&ev.ID, &ev.Kind, &ev.PluginID, &ev.Action, &ev.From, &ev.To, &at); err != nil {
			return nil, fmt.Errorf("change events: scan: %w", err)
		}
		ev.At, _ = time.Parse(time.RFC3339, at)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// Ack deletes the events with these ids (the cloud answered 2xx or 402).
func (r *ChangeEventsRepo) Ack(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM till_change_events WHERE id IN (`+ph+`)`, args...); err != nil {
		return fmt.Errorf("change events: ack: %w", err)
	}
	return nil
}

// Count is the number of waiting events.
func (r *ChangeEventsRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM till_change_events`).Scan(&n)
	return n, err
}

// recordAllowed asks the wired policy, reading settings through exec so a
// caller holding a transaction on a one-connection database never waits on a
// second connection.
func recordAllowed(ctx context.Context, exec executor) bool {
	p := changeEventPolicy.Load()
	if p == nil {
		return false
	}
	return (*p)(func(key string) string {
		var v string
		if err := exec.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
			return ""
		}
		return v
	})
}

func recordChangeEvent(ctx context.Context, exec executor, ev ChangeEvent) {
	if !recordAllowed(ctx, exec) {
		return
	}
	ev.At = time.Now().UTC()
	if err := appendChangeEvent(ctx, exec, ev); err != nil {
		// Reporting must never fail an install or a start.
		logging.L().Debugf("change events: not recorded: %v", err)
	}
}

// RecordVersionChange records a software-version event when the policy
// allows it. Never fails the caller.
func (r *ChangeEventsRepo) RecordVersionChange(ctx context.Context, exec executor, from, to string) {
	recordChangeEvent(ctx, exec, ChangeEvent{Kind: ChangeKindVersion, From: from, To: to})
}

// RecordVersionChangeNow is RecordVersionChange on the repo's own connection,
// for callers outside internal/data.
func (r *ChangeEventsRepo) RecordVersionChangeNow(ctx context.Context, from, to string) {
	r.RecordVersionChange(ctx, r.db, from, to)
}
