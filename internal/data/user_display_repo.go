package data

import (
	"context"
	"database/sql"
)

// UserDisplayRepo owns all SQL for user_display_settings (ut-docs#3149,
// personal display part 1/3): per-operator overrides of display-only
// settings such as the theme and the sell screen's browsing mode. Rows are
// keyed by (user_id, key) using the same key strings as the till-wide
// settings table (common.KeyTheme, common.KeyBrowsingMode), so a resolver
// can fall through from a personal value to the shop/till value for the
// same key.
type UserDisplayRepo struct {
	db *sql.DB
}

func NewUserDisplayRepo(db *sql.DB) *UserDisplayRepo {
	return &UserDisplayRepo{db: db}
}

var userDisplayObs = newRepoObservability("user_display_settings")

// Get returns the operator's personal value for key; ok is false (with a nil
// error) when they have no row for it.
func (r *UserDisplayRepo) Get(ctx context.Context, userID, key string) (string, bool, error) {
	var err error
	done := userDisplayObs.trace("get")
	defer func() { done(err) }()
	var val string
	err = r.db.QueryRowContext(ctx, `
SELECT value FROM user_display_settings WHERE user_id = ? AND key = ?
`, userID, key).Scan(&val)
	if err == sql.ErrNoRows {
		err = nil
		return "", false, nil
	}
	if err != nil {
		return "", false, userDisplayObs.wrapf("get", "get user display setting %s", err, key)
	}
	return val, true, nil
}

// GetAll returns every personal display value the operator has set. An
// operator with none gets an empty, non-nil map.
func (r *UserDisplayRepo) GetAll(ctx context.Context, userID string) (map[string]string, error) {
	var err error
	done := userDisplayObs.trace("get_all")
	defer func() { done(err) }()
	var rows *sql.Rows
	rows, err = r.db.QueryContext(ctx, `
SELECT key, value FROM user_display_settings WHERE user_id = ?
`, userID)
	if err != nil {
		return nil, userDisplayObs.wrap("get_all", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err = rows.Scan(&k, &v); err != nil {
			return nil, userDisplayObs.wrap("get_all", err)
		}
		out[k] = v
	}
	if err = rows.Err(); err != nil {
		return nil, userDisplayObs.wrap("get_all", err)
	}
	return out, nil
}

// Set upserts the operator's personal value for key.
func (r *UserDisplayRepo) Set(ctx context.Context, userID, key, value string) error {
	var err error
	done := userDisplayObs.trace("set")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx, `
INSERT INTO user_display_settings (user_id, key, value)
VALUES (?, ?, ?)
ON CONFLICT(user_id, key) DO UPDATE SET value = excluded.value
`, userID, key, value)
	if err != nil {
		return userDisplayObs.wrapf("set", "set user display setting %s", err, key)
	}
	return nil
}

// Delete removes the operator's personal value for key, so the resolver
// falls back to the till/shop value. Deleting a missing row is not an error.
func (r *UserDisplayRepo) Delete(ctx context.Context, userID, key string) error {
	var err error
	done := userDisplayObs.trace("delete")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx, `
DELETE FROM user_display_settings WHERE user_id = ? AND key = ?
`, userID, key)
	if err != nil {
		return userDisplayObs.wrapf("delete", "delete user display setting %s", err, key)
	}
	return nil
}
