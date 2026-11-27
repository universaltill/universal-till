package data

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Per-field version of a core (typed-registry) setting: the till half of the
// cloud's optimistic concurrency (design-B 4.2/4.3). Ordering is by ver, never
// by wall-clock time (tills' clocks are not trusted); At is display-only.

// SettingOrigin values: who wrote the current value.
const (
	SettingOriginTill  = "till"
	SettingOriginCloud = "cloud"
)

// settingInitialVer is the version of a value nobody has versioned yet (a key
// with no setting_meta row), matching the plugin-settings report's first ver.
const settingInitialVer = 1

var (
	// ErrSettingConflict: the directive's base_ver is older than the till's
	// own version, i.e. the till changed the value after the proposer looked.
	// Nothing was written.
	ErrSettingConflict = errors.New("setting changed on the till since the proposal")
	// ErrSettingStaleVersion: the directive's ver is not newer than the till's.
	// Nothing was written.
	ErrSettingStaleVersion = errors.New("setting version is not newer than the till's")
)

// SettingMeta is the version record of one setting.
type SettingMeta struct {
	Ver    int64
	Origin string
	At     time.Time
}

type metaQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// versioned reports whether key gets a setting_meta row: typed-registry keys
// only (the sync bearer and other internal keys never do).
func versioned(key string) bool {
	_, ok := LookupAgentSetting(key)
	return ok
}

func readMeta(ctx context.Context, q metaQuerier, key string) (SettingMeta, error) {
	var m SettingMeta
	var at sql.NullTime
	err := q.QueryRowContext(ctx, `SELECT ver, origin, at FROM setting_meta WHERE key = ?`, key).Scan(&m.Ver, &m.Origin, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return SettingMeta{Ver: settingInitialVer, Origin: SettingOriginTill}, nil
	}
	if err != nil {
		return SettingMeta{}, err
	}
	m.At = at.Time
	return m, nil
}

func writeMeta(ctx context.Context, q metaQuerier, key string, ver int64, origin string, now time.Time) error {
	_, err := q.ExecContext(ctx, `
INSERT INTO setting_meta (key, ver, origin, at) VALUES (?, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET ver = excluded.ver, origin = excluded.origin, at = excluded.at
`, key, ver, origin, now)
	return err
}

// bumpTillMeta is called inside the writing transaction, BEFORE the upsert of
// the value, for a till-side edit: if the value really changes, ver+1 and
// origin till. An unchanged value keeps its version.
func bumpTillMeta(ctx context.Context, q metaQuerier, key, newValue string, now time.Time) error {
	if !versioned(key) {
		return nil
	}
	err := bumpTillMetaChecked(ctx, q, key, newValue, now)
	if err != nil && strings.Contains(err.Error(), "no such table: setting_meta") {
		return nil // a database older than migration 120 (hand-built test schemas); the migrated till always has it
	}
	return err
}

func bumpTillMetaChecked(ctx context.Context, q metaQuerier, key, newValue string, now time.Time) error {
	var old string
	err := q.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&old)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if found && old == newValue {
		return nil
	}
	m, err := readMeta(ctx, q, key)
	if err != nil {
		return err
	}
	return writeMeta(ctx, q, key, m.Ver+1, SettingOriginTill, now)
}

// SettingMeta returns the version record of key; a key never versioned reads
// as ver 1, origin till.
func (r *SettingsRepo) SettingMeta(ctx context.Context, key string) (SettingMeta, error) {
	m, err := readMeta(ctx, r.db, key)
	if err != nil {
		return SettingMeta{}, settingsObs.wrapf("meta", "read setting meta %s", err, key)
	}
	return m, nil
}

// SettingMetaAll returns every stored version record (registry keys only, so
// the set is bounded by the registry), for the check-in report.
func (r *SettingsRepo) SettingMetaAll(ctx context.Context) (map[string]SettingMeta, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT key, ver, origin, at FROM setting_meta`)
	if err != nil {
		return nil, settingsObs.wrap("meta_all", err)
	}
	defer rows.Close()
	out := map[string]SettingMeta{}
	for rows.Next() {
		var k string
		var m SettingMeta
		var at sql.NullTime
		if err := rows.Scan(&k, &m.Ver, &m.Origin, &at); err != nil {
			return nil, settingsObs.wrap("meta_all", err)
		}
		m.At = at.Time
		out[k] = m
	}
	return out, settingsObs.wrap("meta_all", rows.Err())
}

// SetFromCloud writes a value a cloud directive carries, with the version
// rule of design-B 4.3, atomically (one immediate transaction, so a local save
// cannot slip between the check and the write):
//
//   - baseVer != nil and baseVer < local ver -> ErrSettingConflict, nothing written;
//   - ver != nil and ver <= local ver        -> ErrSettingStaleVersion, nothing written;
//   - otherwise the value is written with ver (or local ver + 1 when the cloud
//     sent none: an older cloud's unconditional write) and origin cloud.
func (r *SettingsRepo) SetFromCloud(ctx context.Context, key, value string, ver, baseVer *int64) error {
	var err error
	done := settingsObs.trace("set_from_cloud")
	defer func() { done(err) }()
	if !versioned(key) {
		err = r.Set(ctx, key, value) // not a registry key: no version to check
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return settingsObs.wrap("set_from_cloud", err)
	}
	defer tx.Rollback()
	local, err := readMeta(ctx, tx, key)
	if err != nil {
		return settingsObs.wrapf("set_from_cloud", "read setting meta %s", err, key)
	}
	if baseVer != nil && *baseVer < local.Ver {
		err = ErrSettingConflict
		return err
	}
	if ver != nil && *ver <= local.Ver {
		err = ErrSettingStaleVersion
		return err
	}
	next := local.Ver + 1
	if ver != nil {
		next = *ver
	}
	now := time.Now().UTC()
	if _, err = tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
	value = excluded.value,
	updated_at = excluded.updated_at
`, key, value, now); err != nil {
		return settingsObs.wrapf("set_from_cloud", "set setting %s", err, key)
	}
	if err = writeMeta(ctx, tx, key, next, SettingOriginCloud, now); err != nil {
		return settingsObs.wrapf("set_from_cloud", "write setting meta %s", err, key)
	}
	if err = tx.Commit(); err != nil {
		return settingsObs.wrap("set_from_cloud", err)
	}
	invalidateCachedSetting(r.db, key)
	return nil
}

// CheckCloudVersion applies the version rule of SetFromCloud without writing,
// for a directive whose write goes through another path (the shop language is
// several keys at once). Returns ErrSettingConflict / ErrSettingStaleVersion.
func (r *SettingsRepo) CheckCloudVersion(ctx context.Context, key string, ver, baseVer *int64) error {
	if !versioned(key) {
		return nil
	}
	local, err := r.SettingMeta(ctx, key)
	if err != nil {
		return err
	}
	if baseVer != nil && *baseVer < local.Ver {
		return ErrSettingConflict
	}
	if ver != nil && *ver <= local.Ver {
		return ErrSettingStaleVersion
	}
	return nil
}

// MarkCloudApplied records that the current value of key came from the cloud
// at ver (never lowering the version), after a write that went through a path
// other than SetFromCloud.
func (r *SettingsRepo) MarkCloudApplied(ctx context.Context, key string, ver *int64) error {
	if !versioned(key) {
		return nil
	}
	local, err := r.SettingMeta(ctx, key)
	if err != nil {
		return err
	}
	next := local.Ver
	if ver != nil && *ver > next {
		next = *ver
	}
	if err := writeMeta(ctx, r.db, key, next, SettingOriginCloud, time.Now().UTC()); err != nil {
		return settingsObs.wrapf("mark_cloud", "write setting meta %s", err, key)
	}
	return nil
}

// deleteVersioned removes a registry key's value; if there was one, that is a
// till-side change and bumps the version.
func (r *SettingsRepo) deleteVersioned(ctx context.Context, key string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return settingsObs.wrap("delete", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	if err != nil {
		return settingsObs.wrapf("delete", "delete setting %s", err, key)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		m, err := readMeta(ctx, tx, key)
		if err != nil {
			return settingsObs.wrapf("delete", "version setting %s", err, key)
		}
		if err := writeMeta(ctx, tx, key, m.Ver+1, SettingOriginTill, time.Now().UTC()); err != nil {
			return settingsObs.wrapf("delete", "version setting %s", err, key)
		}
	}
	return settingsObs.wrap("delete", tx.Commit())
}
