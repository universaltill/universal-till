package data

import (
	"context"
	"database/sql"
	"sort"
	"strings"
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

// GetByPrefix returns every settings key/value pair whose key starts with
// prefix, in one query — in place of one Get per key (ut-docs#1323: the
// sale screen previously called Get once per active payment method just to
// read its "payments.fee.<id>" row, on the product's highest-traffic page
// load). Literal '%'/'_' (and '\') in prefix are escaped so they match
// themselves instead of acting as LIKE wildcards — same discipline as
// PluginRepo.ListStorageByPrefix. A prefix matching nothing returns an
// empty, non-nil map.
func (r *SettingsRepo) GetByPrefix(ctx context.Context, prefix string) (map[string]string, error) {
	var err error
	done := settingsObs.trace("get_by_prefix")
	defer func() { done(err) }()
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(prefix)
	var rows *sql.Rows
	rows, err = r.db.QueryContext(ctx, `
SELECT key, value FROM settings WHERE key LIKE ? ESCAPE '\'
`, escaped+"%")
	if err != nil {
		return nil, settingsObs.wrap("get_by_prefix", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err = rows.Scan(&k, &v); err != nil {
			return nil, settingsObs.wrap("get_by_prefix", err)
		}
		out[k] = v
	}
	if err = rows.Err(); err != nil {
		return nil, settingsObs.wrap("get_by_prefix", err)
	}
	return out, nil
}

// GetOrCreate atomically returns the current value for key, seeding it with
// ifAbsent first if no row exists yet. Unlike a caller doing Get-then-Set
// itself, two concurrent callers racing on the same not-yet-created key
// converge on the SAME value: the INSERT...ON CONFLICT DO NOTHING and the
// SELECT that reads it back run inside one transaction, so whichever caller's
// INSERT actually lands is the value every caller's SELECT then sees — never
// two different generated values with "last Set wins" silently discarding
// one (ut-docs#271).
//
// Historical note on the INSERT-before-SELECT ordering: it was load-bearing
// when the DSN opened plain deferred transactions — a deferred tx that read
// first took only a SHARED lock, and the later SHARED→RESERVED upgrade
// failed instantly (SQLite's busy handler does not retry that promotion).
// Since the DSN gained `_txlock=immediate` (ut-docs#311), every BeginTx runs
// BEGIN IMMEDIATE and takes the write lock at BEGIN regardless of statement
// order, so the ordering no longer matters for locking — it's kept simply
// because there's no reason to churn it.
func (r *SettingsRepo) GetOrCreate(ctx context.Context, key, ifAbsent string) (string, error) {
	var err error
	done := settingsObs.trace("get_or_create")
	defer func() { done(err) }()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return "", settingsObs.wrapf("get_or_create", "get-or-create setting %s", err, key)
	}
	defer tx.Rollback()

	if _, err = tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO NOTHING
`, key, ifAbsent, time.Now().UTC()); err != nil {
		return "", settingsObs.wrapf("get_or_create", "get-or-create setting %s", err, key)
	}

	var val string
	if err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&val); err != nil {
		return "", settingsObs.wrapf("get_or_create", "get-or-create setting %s", err, key)
	}

	if err = tx.Commit(); err != nil {
		return "", settingsObs.wrapf("get_or_create", "get-or-create setting %s", err, key)
	}
	return val, nil
}

// SetMany upserts every key/value pair in one transaction — all or nothing.
// Keys are written in sorted order so failures are deterministic.
func (r *SettingsRepo) SetMany(ctx context.Context, kv map[string]string) error {
	var err error
	done := settingsObs.trace("set_many")
	defer func() { done(err) }()

	if len(kv) == 0 {
		return nil
	}

	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return settingsObs.wrap("set_many", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC()
	for _, k := range keys {
		if _, err = tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
	value = excluded.value,
	updated_at = excluded.updated_at
`, k, kv[k], now); err != nil {
			return settingsObs.wrapf("set_many", "set setting %s", err, k)
		}
	}
	if err = tx.Commit(); err != nil {
		return settingsObs.wrap("set_many", err)
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

// ClearReplicaIdentity promotes a replica (ADR-0011 D4): every sync.* key
// goes EXCEPT the receipt prefix — the till keeps stamping T<n>- receipts
// so its numbering never collides with the old primary's.
func (r *SettingsRepo) ClearReplicaIdentity(ctx context.Context) error {
	var err error
	done := settingsObs.trace("clear_replica_identity")
	defer func() { done(err) }()
	_, err = r.db.ExecContext(ctx,
		`DELETE FROM settings WHERE key LIKE 'sync.%' AND key != 'sync.receipt_prefix'`)
	if err != nil {
		return settingsObs.wrapf("clear_replica_identity", "clear replica identity", err)
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
