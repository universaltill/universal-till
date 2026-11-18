package data

import (
	"context"
	"database/sql"
)

// Test-only access to the outbox (production reaches it through the Record*
// helpers, which consult the reporting policy).

func (r *ChangeEventsRepo) executor(tx *sql.Tx) executor {
	if tx != nil {
		return tx
	}
	return r.db
}

// Append stores one event and trims the outbox to ChangeEventCap; it does not
// consult the policy.
func (r *ChangeEventsRepo) Append(ctx context.Context, tx *sql.Tx, ev ChangeEvent) error {
	return appendChangeEvent(ctx, r.executor(tx), ev)
}

// Count is the number of queued events.
func (r *ChangeEventsRepo) Count(ctx context.Context) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM till_change_events`).Scan(&n)
	return n, err
}
