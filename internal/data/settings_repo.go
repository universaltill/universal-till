package data

import (
	"context"
	"database/sql"
	"time"
)

// SettingsRepo owns all SQL for the settings table.
type SettingsRepo struct {
	db *sql.DB
}

func NewSettingsRepo(db *sql.DB) *SettingsRepo {
	return &SettingsRepo{db: db}
}

var settingsObs = newRepoObservability("settings")

func (r *SettingsRepo) Get(ctx context.Context, key string) (string, bool, error) {
	var err error
	done := settingsObs.trace("get")
	defer func() { done(err) }()
	var val string
	err = r.db.QueryRowContext(ctx, `
SELECT value FROM settings WHERE key = ?
`, key).Scan(&val)
	if err == sql.ErrNoRows {
		err = nil
		return "", false, nil
	}
	if err != nil {
		return "", false, settingsObs.wrapf("get", "get setting %s", err, key)
	}
	return val, true, nil
}

func (r *SettingsRepo) Set(ctx context.Context, key, value string) error {
	var err error
	done := settingsObs.trace("set")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
	value = excluded.value,
	updated_at = excluded.updated_at
`, key, value, time.Now().UTC())
	if err != nil {
		return settingsObs.wrapf("set", "set setting %s", err, key)
	}
	return nil
}

// Delete removes a setting by key (no-op if absent).
func (r *SettingsRepo) Delete(ctx context.Context, key string) error {
	var err error
	done := settingsObs.trace("delete")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	if err != nil {
		return settingsObs.wrapf("delete", "delete setting %s", err, key)
	}
	return nil
}

// All returns all key/value pairs from settings.
func (r *SettingsRepo) All(ctx context.Context) (map[string]string, error) {
	var err error
	done := settingsObs.trace("all")
	defer func() { done(err) }()
	rows, err := r.db.QueryContext(ctx, `SELECT key, value FROM settings`)
	if err != nil {
		return nil, settingsObs.wrap("all", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, settingsObs.wrap("all", err)
		}
		out[k] = v
	}
	err = rows.Err()
	if err != nil {
		err = settingsObs.wrap("all", err)
	}
	return out, err
}
