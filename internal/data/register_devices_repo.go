package data

// ut-docs#7098 (epic #7097): one cloud device = one register for life. The
// link lives in register_devices (migration 130), a main-till-local side table
// that is deliberately NOT an admin table: registers travel to every replica,
// the device ids of other tills must not (ADR-0086).
//
// Retiring a register is soft and reversible: is_active = 0 plus a history row.
// Nothing here ever deletes a register, a sale, a shift or a no-sale event.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Why a register was retired (register_devices.retired_reason).
const (
	RetiredRevoked   = "revoked"    // its till was removed
	RetiredOldEntry  = "old_entry"  // its till is gone / not in the account any more
	RetiredStaleAuto = "stale_auto" // the optional auto-retire sweep
	RetiredManual    = "manual"     // a manager pressed Retire
	RetiredReplaced  = "replaced"   // a newer device took it over
)

// Link sources (register_devices.source).
const (
	RegisterLinkEnrol    = "enrol"
	RegisterLinkRename   = "rename"
	RegisterLinkBackfill = "backfill"
)

// Refusals RetireRegister returns. Every caller shares them: the guards live
// here, not in the handlers.
var (
	ErrDeviceHasRegister = errors.New("device already has a register")
	ErrRetireSelf        = errors.New("cannot retire this till's own register")
	ErrRetireLast        = errors.New("cannot retire the last active register")
	ErrRetireOpenShift   = errors.New("register has an open shift")
	ErrRetireUnsent      = errors.New("register has sales not sent to the cloud yet")
)

// RegisterDevice is one register<->device link row.
type RegisterDevice struct {
	RegisterID    string
	DeviceID      string // "" = unknown
	TillID        string
	Source        string
	SupersededBy  string
	RetiredReason string
	RetiredAt     time.Time // zero when not retired
}

// RetireOpts are the audit details of a retire.
type RetireOpts struct {
	ActorID string
}

// RetireResult reports what RetireRegister did.
type RetireResult struct {
	// AlreadyRetired: the register was inactive already; nothing changed.
	AlreadyRetired bool
}

const registerDeviceCols = `register_id, device_id, till_id, source, superseded_by, retired_reason, retired_at`

func scanRegisterDevice(sc interface{ Scan(...any) error }) (RegisterDevice, error) {
	var d RegisterDevice
	var retiredAt sql.NullString
	if err := sc.Scan(&d.RegisterID, &d.DeviceID, &d.TillID, &d.Source, &d.SupersededBy, &d.RetiredReason, &retiredAt); err != nil {
		return RegisterDevice{}, err
	}
	if retiredAt.Valid && retiredAt.String != "" {
		if t, err := time.Parse(time.RFC3339, retiredAt.String); err == nil {
			d.RetiredAt = t
		}
	}
	return d, nil
}

// RegisterByDevice returns the register linked to a cloud device id. An empty
// id never matches.
func (r *POSRepo) RegisterByDevice(ctx context.Context, deviceID string) (RegisterDevice, bool, error) {
	if deviceID == "" {
		return RegisterDevice{}, false, nil
	}
	d, err := scanRegisterDevice(r.db.QueryRowContext(ctx,
		`SELECT `+registerDeviceCols+` FROM register_devices WHERE device_id = ?`, deviceID))
	if errors.Is(err, sql.ErrNoRows) {
		return RegisterDevice{}, false, nil
	}
	if err != nil {
		return RegisterDevice{}, false, fmt.Errorf("register by device: %w", err)
	}
	return d, true, nil
}

// RegisterByTill returns the register a joined till owns: the link row first,
// else the till_enrolled audit row (EnrolledRegisterID), as before the link
// table existed. ok is false when there is neither.
func (r *POSRepo) RegisterByTill(ctx context.Context, tillID string) (RegisterDevice, bool, error) {
	if tillID == "" {
		return RegisterDevice{}, false, nil
	}
	d, err := scanRegisterDevice(r.db.QueryRowContext(ctx,
		`SELECT `+registerDeviceCols+` FROM register_devices WHERE till_id = ? ORDER BY created_at DESC, rowid DESC LIMIT 1`, tillID))
	if err == nil {
		return d, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return RegisterDevice{}, false, fmt.Errorf("register by till: %w", err)
	}
	id, err := r.EnrolledRegisterID(ctx, tillID)
	if err != nil || id == "" {
		return RegisterDevice{}, false, err
	}
	return RegisterDevice{RegisterID: id, TillID: tillID}, true, nil
}

// LinkRegisterDevice records (or updates) the link of a register to a device,
// keyed by register id. A device id already linked to ANOTHER register is
// refused with ErrDeviceHasRegister (the unique index says the same; this
// gives the handler a clean sentinel). When the register already had a
// different non-empty device id, that old id is kept in superseded_by, so a
// replaced device leaves a trace. tx may be nil.
func (r *POSRepo) LinkRegisterDevice(ctx context.Context, tx *sql.Tx, registerID, deviceID, tillID, source string) error {
	if registerID == "" {
		return errors.New("link register device: empty register id")
	}
	if source == "" {
		source = RegisterLinkEnrol
	}
	ex := r.exec(tx)
	if deviceID != "" {
		var other string
		err := ex.QueryRowContext(ctx,
			`SELECT register_id FROM register_devices WHERE device_id = ? AND register_id <> ?`, deviceID, registerID).Scan(&other)
		if err == nil {
			return ErrDeviceHasRegister
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("link register device: %w", err)
		}
	}
	_, err := ex.ExecContext(ctx, `
INSERT INTO register_devices (register_id, device_id, till_id, source) VALUES (?, ?, ?, ?)
ON CONFLICT(register_id) DO UPDATE SET
  superseded_by = CASE WHEN register_devices.device_id <> '' AND register_devices.device_id <> excluded.device_id
                       THEN register_devices.device_id ELSE register_devices.superseded_by END,
  device_id = excluded.device_id,
  till_id   = CASE WHEN excluded.till_id <> '' THEN excluded.till_id ELSE register_devices.till_id END,
  source    = excluded.source`, registerID, deviceID, tillID, source)
	if err != nil {
		if isUniqueViolation(err) && strings.Contains(err.Error(), "device_id") {
			return ErrDeviceHasRegister
		}
		return fmt.Errorf("link register device: %w", err)
	}
	return nil
}

// ListRegisterDevices returns every link row keyed by register id.
func (r *POSRepo) ListRegisterDevices(ctx context.Context) (map[string]RegisterDevice, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+registerDeviceCols+` FROM register_devices`)
	if err != nil {
		return nil, fmt.Errorf("list register devices: %w", err)
	}
	defer rows.Close()
	out := map[string]RegisterDevice{}
	for rows.Next() {
		d, err := scanRegisterDevice(rows)
		if err != nil {
			return nil, fmt.Errorf("scan register device: %w", err)
		}
		out[d.RegisterID] = d
	}
	return out, rows.Err()
}

// RegisterLastSale reports a register's sales: how many, when the latest was
// made (zero when none) and how many have not reached the cloud yet.
func (r *POSRepo) RegisterLastSale(ctx context.Context, registerID string) (count int, last time.Time, unsent int, err error) {
	var lastStr sql.NullString
	err = r.db.QueryRowContext(ctx, `
SELECT COUNT(*), MAX(created_at), COALESCE(SUM(CASE WHEN sync_status NOT IN ('synced','sent') THEN 1 ELSE 0 END), 0)
FROM sales WHERE register_id = ?`, registerID).Scan(&count, &lastStr, &unsent)
	if err != nil {
		return 0, time.Time{}, 0, fmt.Errorf("register last sale: %w", err)
	}
	last = parseDBTime(lastStr.String)
	return count, last, unsent, nil
}

// parseDBTime reads the two timestamp layouts the schema writes (RFC3339 and
// SQLite's datetime('now')). Zero on anything else.
func parseDBTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// OwnRegisterID is the id of this till's own register: the persisted
// sync.till_register_id, else the register EnsureRegister seeds.
func (r *POSRepo) OwnRegisterID(ctx context.Context) (string, error) {
	var v string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'sync.till_register_id'`).Scan(&v)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("own register id: %w", err)
	}
	if strings.TrimSpace(v) != "" {
		return v, nil
	}
	return "reg-default", nil
}

// RetireRegister soft-disables a register and records why, in one transaction.
// Never a DELETE, never touches sales, shifts or no_sale_events. Refusals, in
// order: this till's own register (ErrRetireSelf), the last active register
// (ErrRetireLast), an open shift (ErrRetireOpenShift), sales not yet sent to
// the cloud (ErrRetireUnsent). An already-retired register is a no-op.
func (r *POSRepo) RetireRegister(ctx context.Context, registerID, reason, supersededBy string, opts RetireOpts) (RetireResult, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return RetireResult{}, fmt.Errorf("retire register: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var active int
	if err := tx.QueryRowContext(ctx, `SELECT is_active FROM registers WHERE id = ?`, registerID).Scan(&active); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RetireResult{}, fmt.Errorf("retire register: %s not found", registerID)
		}
		return RetireResult{}, fmt.Errorf("retire register: %w", err)
	}
	if active == 0 {
		return RetireResult{AlreadyRetired: true}, nil
	}

	own := "reg-default"
	var v string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = 'sync.till_register_id'`).Scan(&v); err == nil && strings.TrimSpace(v) != "" {
		own = v
	}
	if registerID == own {
		return RetireResult{}, ErrRetireSelf
	}
	var activeCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM registers WHERE is_active = 1`).Scan(&activeCount); err != nil {
		return RetireResult{}, fmt.Errorf("retire register: %w", err)
	}
	if activeCount <= 1 {
		return RetireResult{}, ErrRetireLast
	}
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM shifts WHERE register_id = ? AND closed_at IS NULL`, registerID).Scan(&n); err != nil {
		return RetireResult{}, fmt.Errorf("retire register: %w", err)
	}
	if n > 0 {
		return RetireResult{}, ErrRetireOpenShift
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sales WHERE register_id = ? AND sync_status NOT IN ('synced','sent')`, registerID).Scan(&n); err != nil {
		return RetireResult{}, fmt.Errorf("retire register: %w", err)
	}
	if n > 0 {
		return RetireResult{}, ErrRetireUnsent
	}

	if _, err := tx.ExecContext(ctx, `UPDATE registers SET is_active = 0 WHERE id = ?`, registerID); err != nil {
		return RetireResult{}, fmt.Errorf("retire register: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO register_devices (register_id, source, retired_reason, retired_at, superseded_by) VALUES (?, 'backfill', ?, ?, ?)
ON CONFLICT(register_id) DO UPDATE SET retired_reason = excluded.retired_reason, retired_at = excluded.retired_at,
  superseded_by = CASE WHEN excluded.superseded_by <> '' THEN excluded.superseded_by ELSE register_devices.superseded_by END`,
		registerID, reason, now, supersededBy); err != nil {
		return RetireResult{}, fmt.Errorf("retire register: %w", err)
	}
	var deviceID string
	_ = tx.QueryRowContext(ctx, `SELECT device_id FROM register_devices WHERE register_id = ?`, registerID).Scan(&deviceID)
	payload := map[string]any{"reason": reason, "superseded_by": supersededBy, "device_id": deviceID}
	if err := r.InsertAudit(ctx, tx, opts.ActorID, "register", registerID, "register_retired", payload, now, ""); err != nil {
		// An actor id that is not a user row (the audit FK) must not undo the
		// retire: record the row without the actor, and say who in the payload.
		payload["actor"] = opts.ActorID
		if err := r.InsertAudit(ctx, tx, "", "register", registerID, "register_retired", payload, now, ""); err != nil {
			return RetireResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return RetireResult{}, fmt.Errorf("retire register: %w", err)
	}
	return RetireResult{}, nil
}

// ReactivateRegister is the undo of RetireRegister: is_active = 1 and the
// retired columns cleared, so the link table never calls an active register
// retired.
func (r *POSRepo) ReactivateRegister(ctx context.Context, registerID, actorID string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("reactivate register: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE registers SET is_active = 1 WHERE id = ?`, registerID)
	if err != nil {
		return fmt.Errorf("reactivate register: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("reactivate register: %s not found", registerID)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE register_devices SET retired_reason = '', retired_at = NULL WHERE register_id = ?`, registerID); err != nil {
		return fmt.Errorf("reactivate register: %w", err)
	}
	return tx.Commit()
}

// tillExists reports whether a tills row with this id exists.
func (r *POSRepo) tillExists(ctx context.Context, tillID string) (bool, error) {
	if tillID == "" {
		return false, nil
	}
	var one int
	err := r.db.QueryRowContext(ctx, `SELECT 1 FROM tills WHERE id = ?`, tillID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// RegisterOwnerTillLive reports whether the till that owns a link still has a
// tills row (a revoked till does not).
func (r *POSRepo) RegisterOwnerTillLive(ctx context.Context, link RegisterDevice) (bool, error) {
	return r.tillExists(ctx, link.TillID)
}
