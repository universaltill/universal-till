package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SettingsRepo owns all SQL for the settings table.
type SettingsRepo struct {
	db *sql.DB
}

func NewSettingsRepo(db *sql.DB) *SettingsRepo {
	return &SettingsRepo{db: db}
}

var settingsObs = newRepoObservability("settings")

// invalidateCachedSetting drops any in-process cache of key after a generic
// write (Set, SetMany, Delete). Today only the barcode symbology set is
// cached (barcode_settings.go); without this a write through the generic
// path -- the additional-till mirror of a write-through, the main till's
// /api/sync/settings/apply, a cloud directive -- left the cached set stale
// until the next dedicated write (ut-docs#2979).
func invalidateCachedSetting(db *sql.DB, key string) {
	if key == BarcodeEnabledSymbologiesKey {
		invalidateBarcodeSymbologyCache(db)
	}
}

func (r *SettingsRepo) Get(ctx context.Context, key string) (string, bool, error) {
	var err error
	done := settingsObs.trace("get")
	defer func() { done(err) }()
	if path, ok := r.bearerFile(key); ok { // ut-docs#5172
		var v string
		var found bool
		v, found, err = readSyncBearerFile(path)
		return v, found, err
	}
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

// KeyAllowNegativeInventory is the shop-wide "sell items without tracking
// stock" settings key. Declared here so internal/data (which decides whether
// stock predictions apply) and internal/pages/common share one spelling.
const KeyAllowNegativeInventory = "pos.allow_negative_inventory"

// ShopStockUntracked reports whether the shop sells without tracking stock
// (ut-docs#4134). A missing or unparseable value is the default, false (stock
// guard on), matching common.LoadState; only a real DB error is returned.
func (r *SettingsRepo) ShopStockUntracked(ctx context.Context) (bool, error) {
	v, ok, err := r.Get(ctx, KeyAllowNegativeInventory)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	b, perr := strconv.ParseBool(v)
	if perr != nil {
		return false, nil
	}
	return b, nil
}

func (r *SettingsRepo) Set(ctx context.Context, key, value string) error {
	var err error
	done := settingsObs.trace("set")
	defer func() { done(err) }()
	if path, ok := r.bearerFile(key); ok { // ut-docs#5172
		err = storeSyncBearer(path, value)
		return err
	}
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
	invalidateCachedSetting(r.db, key)
	return nil
}

// SetTx is Set inside the caller's transaction, so a value commits with
// the writes it describes — the cloud catalogue pull's cursor commits with
// its chunk (ADR-0162 §4, ut-docs#4463).
//
// The sync bearer (ut-docs#5172) lives in a file, which is NOT part of tx: it
// is written immediately and survives a rollback.
func (r *SettingsRepo) SetTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	if path, ok := r.bearerFile(key); ok {
		if err := storeSyncBearer(path, value); err != nil {
			return settingsObs.wrapf("set_tx", "set setting %s", err, key)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
	value = excluded.value,
	updated_at = excluded.updated_at
`, key, value, time.Now().UTC()); err != nil {
		return settingsObs.wrapf("set_tx", "set setting %s", err, key)
	}
	invalidateCachedSetting(r.db, key)
	return nil
}

// SetIfReplica upserts values in one transaction, only while this till still
// has a sync.primary_url (read in the same tx; the DB is _txlock=immediate so
// a concurrent ClearReplicaIdentity is serialised). ErrNotReplica, nothing
// written, otherwise (ut-docs#5903). Keys must not be the sync bearer
// (file-backed, not part of the tx).
func (r *SettingsRepo) SetIfReplica(ctx context.Context, values map[string]string) error {
	var err error
	done := settingsObs.trace("set_if_replica")
	defer func() {
		// A refusal is the expected promote race, not a failure (no ERROR line).
		if errors.Is(err, ErrNotReplica) {
			done(nil)
			return
		}
		done(err)
	}()

	keys := make([]string, 0, len(values))
	for k := range values {
		if _, ok := r.bearerFile(k); ok {
			err = fmt.Errorf("set if replica: %s is file-backed", k)
			return err
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return settingsObs.wrap("set_if_replica", err)
	}
	defer tx.Rollback()
	if err = requireReplica(ctx, tx); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, k := range keys {
		if _, err = tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
	value = excluded.value,
	updated_at = excluded.updated_at
`, k, values[k], now); err != nil {
			return settingsObs.wrapf("set_if_replica", "set setting %s", err, k)
		}
	}
	if err = tx.Commit(); err != nil {
		return settingsObs.wrap("set_if_replica", err)
	}
	for _, k := range keys {
		invalidateCachedSetting(r.db, k)
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
	if path, ok := r.registeredBearerFile(); ok && strings.HasPrefix(SyncBearerSettingsKey, prefix) { // ut-docs#5172
		v, found, ferr := readSyncBearerFile(path)
		if ferr != nil {
			err = ferr
			return nil, err
		}
		if found {
			out[SyncBearerSettingsKey] = v
		}
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

	if path, ok := r.bearerFile(key); ok { // ut-docs#5172
		syncBearerMu.Lock()
		defer syncBearerMu.Unlock()
		v, found, rerr := readSyncBearerFile(path)
		if rerr != nil {
			err = rerr
			return "", err
		}
		if found {
			return v, nil
		}
		if err = removePrevSyncBearer(path); err != nil {
			return "", err
		}
		err = writeSyncBearerFile(path, ifAbsent)
		return ifAbsent, err
	}

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

	// The sync bearer (ut-docs#5172) goes to its file after the tx commits and
	// never reaches the SQL.
	bearerPath, bearerRedirected := r.bearerFile(SyncBearerSettingsKey)
	keys := make([]string, 0, len(kv))
	for k := range kv {
		if k == SyncBearerSettingsKey && bearerRedirected {
			continue
		}
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
	for _, k := range keys {
		invalidateCachedSetting(r.db, k)
	}
	if v, has := kv[SyncBearerSettingsKey]; has && bearerRedirected {
		if err = storeSyncBearer(bearerPath, v); err != nil {
			return settingsObs.wrapf("set_many", "set setting %s", err, SyncBearerSettingsKey)
		}
	}
	return nil
}

// Delete removes a setting by key (no-op if absent).
func (r *SettingsRepo) Delete(ctx context.Context, key string) error {
	var err error
	done := settingsObs.trace("delete")
	defer func() { done(err) }()
	if path, ok := r.bearerFile(key); ok { // ut-docs#5172
		err = storeSyncBearer(path, "")
		return err
	}
	_, err = r.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key)
	if err != nil {
		return settingsObs.wrapf("delete", "delete setting %s", err, key)
	}
	invalidateCachedSetting(r.db, key)
	return nil
}

// ClearReplicaIdentity promotes a replica (ADR-0011 D4): every sync.* key
// goes EXCEPT the receipt prefix — the till keeps stamping T<n>- receipts
// so its numbering never collides with the old primary's.
//
// till.name is shop-wide (ShopWideSettingPrefixes), so on a replica it still
// holds the OLD main till's name, synced down — not this till's own typed
// name, which lived only in sync.till_name. Deleting sync.till_name outright
// would make the newly-promoted till report the wrong name to the cloud
// (enroll.DeviceName) and lose its own name entirely. So before the delete,
// a non-blank sync.till_name is copied into till.name; the promoted till
// keeps its own name (ut-docs#3025).
//
// When the trimmed sync.till_name is blank (the row is missing or
// whitespace-only), there is no name to carry over, so till.name is set
// blank instead of left standing: leaving it would make the promoted till
// keep reporting the OLD main till's name forever. Blank, the till falls
// back to its translated default name (every reader trims) and
// enroll.DeviceName reports "" (ut-docs#3030).
//
// sync.till_id gets the same copy-before-wipe, in the same transaction: it is
// the id the cloud knows this till's device row by, so a non-blank (trimmed)
// sync.till_id is copied into the per-till TillIdentityCloudIDSettingsKey,
// which discovery.ReportedTillID reports once sync.till_id is gone — the
// promoted till keeps its cloud identity instead of reappearing under a
// different id (ut-docs#3307). Unlike till.name, a blank sync.till_id writes
// nothing: there is no id to keep, and a cloud id kept by an earlier
// promotion must not be clobbered.
//
// The LAN discovery id (LANDiscoveryTillIDSettingsKey) is re-minted, always,
// in the same transaction (ut-docs#5893). It is a shop-wide setting, so a
// joined till holds its MAIN till's id (join snapshot and every admin pull);
// left alone, the promoted till would advertise over mDNS, sign verification
// codes and primary proofs with, and filter "self" by an id the old main till
// (and every till still pointed at it) also uses. The pre-join id is gone (the
// snapshot replaced it, ADR-0033 "two till ids"), so a fresh UUID is the only
// option; it is never "kept if present" because a present value on a joined
// till is always the main till's. The cloud id above is unaffected.
//
// An admin pull that is in flight when this commits is refused by
// ApplyAdminWithResult's in-tx sync.primary_url check (ErrNotReplica), so it
// cannot write the old main's lan_discovery.till_id or till.name back
// (ut-docs#5896); the rest of that tick's writes (SetIfReplica's sync.*
// bookkeeping and till role, SyncStockRepo.ApplyStockLevels) re-check it the
// same way (ut-docs#5903).
func (r *SettingsRepo) ClearReplicaIdentity(ctx context.Context) error {
	var err error
	done := settingsObs.trace("clear_replica_identity")
	defer func() { done(err) }()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return settingsObs.wrap("clear_replica_identity", err)
	}
	defer tx.Rollback()

	var tillName string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'sync.till_name'`).Scan(&tillName)
	if err != nil && err != sql.ErrNoRows {
		return settingsObs.wrapf("clear_replica_identity", "read sync.till_name", err)
	}
	if err == sql.ErrNoRows {
		err = nil
	}
	// Always written, blank included (ut-docs#3030): a blank value, not a
	// deleted row, because shop-wide settings reach joined tills only as
	// upserts (ApplyAdmin never prunes settings) — a delete would leave any
	// till later pointed at this one showing the old main till's name.
	if _, err = tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at)
VALUES ('till.name', ?, ?)
ON CONFLICT(key) DO UPDATE SET
	value = excluded.value,
	updated_at = excluded.updated_at
`, strings.TrimSpace(tillName), time.Now().UTC()); err != nil {
		return settingsObs.wrapf("clear_replica_identity", "set setting %s", err, "till.name")
	}

	var tillID string
	err = tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'sync.till_id'`).Scan(&tillID)
	if err != nil && err != sql.ErrNoRows {
		return settingsObs.wrapf("clear_replica_identity", "read sync.till_id", err)
	}
	if err == sql.ErrNoRows {
		err = nil
	}
	if tillID = strings.TrimSpace(tillID); tillID != "" {
		if _, err = tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
	value = excluded.value,
	updated_at = excluded.updated_at
`, TillIdentityCloudIDSettingsKey, tillID, time.Now().UTC()); err != nil {
			return settingsObs.wrapf("clear_replica_identity", "set setting %s", err, TillIdentityCloudIDSettingsKey)
		}
	}

	if _, err = upsertFreshDiscoveryID(ctx, tx); err != nil {
		return settingsObs.wrapf("clear_replica_identity", "set setting %s", err, LANDiscoveryTillIDSettingsKey)
	}

	if _, err = tx.ExecContext(ctx,
		`DELETE FROM settings WHERE key LIKE 'sync.%' AND key != 'sync.receipt_prefix'`); err != nil {
		return settingsObs.wrapf("clear_replica_identity", "clear replica identity", err)
	}

	if err = tx.Commit(); err != nil {
		return settingsObs.wrap("clear_replica_identity", err)
	}
	// The bearer is a sync.* key too (ut-docs#5172); its file is not in the tx.
	if path, ok := r.registeredBearerFile(); ok {
		if err = storeSyncBearer(path, ""); err != nil {
			return settingsObs.wrap("clear_replica_identity", err)
		}
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
		return out, err
	}
	if path, ok := r.registeredBearerFile(); ok { // ut-docs#5172
		v, found, ferr := readSyncBearerFile(path)
		if ferr != nil {
			err = settingsObs.wrap("all", ferr)
			return nil, err
		}
		if found {
			out[SyncBearerSettingsKey] = v
		}
	}
	return out, err
}

// upsertSettingTx upserts one settings row inside tx.
func upsertSettingTx(ctx context.Context, tx *sql.Tx, key, value string) error {
	_, err := tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
	value = excluded.value,
	updated_at = excluded.updated_at
`, key, value, time.Now().UTC())
	return err
}

// upsertFreshDiscoveryID mints a new UUID into LANDiscoveryTillIDSettingsKey
// and records it in TillIdentityOwnDiscoveryIDSettingsKey (the "this till
// minted its own discovery id" marker), in the caller's transaction. Shared by
// ClearReplicaIdentity (ut-docs#5893) and RepairInheritedDiscoveryID
// (ut-docs#5897).
func upsertFreshDiscoveryID(ctx context.Context, tx *sql.Tx) (string, error) {
	id := uuid.NewString()
	if err := upsertSettingTx(ctx, tx, LANDiscoveryTillIDSettingsKey, id); err != nil {
		return "", err
	}
	if err := upsertSettingTx(ctx, tx, TillIdentityOwnDiscoveryIDSettingsKey, id); err != nil {
		return "", err
	}
	return id, nil
}

// DiscoveryIDRepair is the outcome of RepairInheritedDiscoveryID.
type DiscoveryIDRepair struct {
	Repaired     bool
	Deferred     string // "" | "joined_tills" | "pairing_in_flight"
	Signal       string // "mirror" | "cloud_id" | "restored"
	OldID, NewID string
}

// RepairInheritedDiscoveryID re-mints lan_discovery.till_id once on a
// main/standalone till that was promoted before ut-docs#5893 and so still
// holds its former main till's id (ut-docs#5897). A till is detected by
// mirrorMainTillID (SalesMirrorRepo.State().MainTillID, read by the caller)
// equalling the stored id, or a non-blank till_identity.cloud_id with no
// own-discovery-id marker, or the stored id equalling the inherited id an
// earlier repair recorded (a restore of a pre-repair backup brought it back:
// till_identity.* stays the live till's on restore, lan_discovery.* does
// not). It is skipped on a replica (sync.primary_url set)
// and deferred, writing nothing, while any till holds a credential issued by
// this till or a pairing is in flight: a till that paired while the old id was
// advertised proved that id and would be stranded by a change. All writes
// (id, marker, cloud id pin, audit row) share one transaction.
func (r *SettingsRepo) RepairInheritedDiscoveryID(ctx context.Context, mirrorMainTillID string, now time.Time) (res DiscoveryIDRepair, err error) {
	done := settingsObs.trace("repair_inherited_discovery_id")
	defer func() { done(err) }()

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return res, settingsObs.wrap("repair_inherited_discovery_id", err)
	}
	defer tx.Rollback()

	read := func(key string) (string, error) {
		var v string
		e := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
		if e == sql.ErrNoRows {
			return "", nil
		}
		return strings.TrimSpace(v), e
	}
	primaryURL, err := read("sync.primary_url")
	if err != nil {
		return res, settingsObs.wrapf("repair_inherited_discovery_id", "read sync.primary_url", err)
	}
	if primaryURL != "" {
		return res, nil
	}
	oldID, err := read(LANDiscoveryTillIDSettingsKey)
	if err != nil {
		return res, settingsObs.wrapf("repair_inherited_discovery_id", "read discovery id", err)
	}
	if oldID == "" {
		return res, nil
	}
	cloudID, err := read(TillIdentityCloudIDSettingsKey)
	if err != nil {
		return res, settingsObs.wrapf("repair_inherited_discovery_id", "read cloud id", err)
	}
	marker, err := read(TillIdentityOwnDiscoveryIDSettingsKey)
	if err != nil {
		return res, settingsObs.wrapf("repair_inherited_discovery_id", "read marker", err)
	}
	inherited, err := read(TillIdentityInheritedDiscoveryIDSettingsKey)
	if err != nil {
		return res, settingsObs.wrapf("repair_inherited_discovery_id", "read inherited id", err)
	}
	switch mirror := strings.TrimSpace(mirrorMainTillID); {
	case mirror != "" && mirror == oldID:
		res.Signal = "mirror"
	case cloudID != "" && marker == "":
		res.Signal = "cloud_id"
	case inherited != "" && inherited == oldID:
		res.Signal = "restored"
	default:
		return res, nil
	}

	var n int
	if err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM tills WHERE bearer_hash IS NOT NULL OR bearer_prev_hash IS NOT NULL`).Scan(&n); err != nil {
		return res, settingsObs.wrapf("repair_inherited_discovery_id", "count credentialed tills", err)
	}
	if n > 0 {
		return DiscoveryIDRepair{Deferred: "joined_tills", Signal: res.Signal, OldID: oldID}, nil
	}
	if err = tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pending_pairings WHERE status IN ('pending','approved') AND expires_at >= ?`,
		now.UTC().Format(time.RFC3339)).Scan(&n); err != nil {
		return res, settingsObs.wrapf("repair_inherited_discovery_id", "count pairings", err)
	}
	if n > 0 {
		return DiscoveryIDRepair{Deferred: "pairing_in_flight", Signal: res.Signal, OldID: oldID}, nil
	}

	if cloudID == "" {
		// Keep discovery.ReportedTillID stable: it falls back to the
		// discovery id when no cloud id is stored.
		if err = upsertSettingTx(ctx, tx, TillIdentityCloudIDSettingsKey, oldID); err != nil {
			return res, settingsObs.wrapf("repair_inherited_discovery_id", "set setting %s", err, TillIdentityCloudIDSettingsKey)
		}
	}
	newID, err := upsertFreshDiscoveryID(ctx, tx)
	if err != nil {
		return res, settingsObs.wrapf("repair_inherited_discovery_id", "set setting %s", err, LANDiscoveryTillIDSettingsKey)
	}
	if err = upsertSettingTx(ctx, tx, TillIdentityInheritedDiscoveryIDSettingsKey, oldID); err != nil {
		return res, settingsObs.wrapf("repair_inherited_discovery_id", "set setting %s", err, TillIdentityInheritedDiscoveryIDSettingsKey)
	}
	if err = NewPOSRepo(r.db).InsertAudit(ctx, tx, "system", "till", "-", "discovery_id_repaired", map[string]string{
		"old_id": oldID, "new_id": newID, "signal": res.Signal, "reason": "ut-docs#5897",
	}, now.UTC().Format(time.RFC3339), ""); err != nil {
		return DiscoveryIDRepair{}, settingsObs.wrap("repair_inherited_discovery_id", err)
	}
	if err = tx.Commit(); err != nil {
		return DiscoveryIDRepair{}, settingsObs.wrap("repair_inherited_discovery_id", err)
	}
	res.Repaired, res.OldID, res.NewID = true, oldID, newID
	return res, nil
}
