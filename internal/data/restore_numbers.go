package data

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/restorenum"
)

// Settings keys for the number jump after a backup restore (ut-docs#5068).
// A restored backup lacks the receipts and Z reports the dead or rolled-back
// till issued after the backup was taken, so MAX()+1 would hand those numbers
// out a second time. JumpNumbersAfterRestore moves both sequences far ahead
// and records the floors here; NextReceiptNo and ArchiveReport honour them.
const (
	// RestoreReceiptFloorKey holds the highest receipt counter the jump
	// reserved; RestoreReceiptFloorPrefixKey the receipt prefix it was
	// computed for (a floor for another prefix is ignored).
	RestoreReceiptFloorKey       = "restore.receipt_floor"
	RestoreReceiptFloorPrefixKey = "restore.receipt_floor_prefix"
	// RestoreZFloorKeyPrefix + kind holds that report kind's Z floor.
	RestoreZFloorKeyPrefix = "restore.z_floor."
	// RestoreJumpTokenKey holds the token of the last restore whose jump
	// was applied; the same token never jumps twice.
	RestoreJumpTokenKey = "restore.jump_token"
	// RestoreMarkerKey holds the RestoreMarker JSON until the first Z
	// report after the restore has carried it.
	RestoreMarkerKey = "restore.marker"
)

// RestoreNumberMax bounds a counter the jump believes (ut-docs#6906): a larger
// receipt or Z number is corrupt, not a counter, and is ignored (never a lower
// bound), so adding the jump cannot overflow. internal/db mirrors it.
const RestoreNumberMax int64 = restorenum.Max

const (
	restoreMinJump       = 10000
	restoreFloorRounding = 1000
	// restoreJumpMax caps the jump and the days term (ut-docs#6924).
	restoreJumpMax int64 = 100_000_000_000_000
	// restoreInputMax is the highest number the jump believes: with the
	// largest jump and the rounding added, its output still fits
	// RestoreNumberMax, so every reader accepts what the jump writes.
	restoreInputMax = RestoreNumberMax - restoreJumpMax - restoreFloorRounding
)

// RestoreMarker records a restore's number jump. The first day-close report
// afterwards embeds it so the paper trail explains the gap.
type RestoreMarker struct {
	RestoredAt     string `json:"restored_at"`
	LastActivityAt string `json:"last_activity_at"`
	ReceiptPrefix  string `json:"receipt_prefix"`
	LastReceipt    string `json:"last_receipt"`
	NextReceipt    string `json:"next_receipt"`
	LastZ          int64  `json:"last_z"`
	NextZ          int64  `json:"next_z"`
}

// RestoreJump is the result of JumpNumbersAfterRestore: the marker plus the
// numeric from/to values for the audit row.
type RestoreJump struct {
	// AlreadyApplied is true when the token had already been applied: nothing
	// was changed and the other fields are zero.
	AlreadyApplied bool             `json:"-"`
	Marker         RestoreMarker    `json:"marker"`
	Days           int64            `json:"days"`
	PeakPerDay     int64            `json:"peak_per_day"`
	ReceiptFrom    int64            `json:"receipt_from"`
	ReceiptFloor   int64            `json:"receipt_floor"`
	ZFrom          map[string]int64 `json:"z_from"`
	ZFloor         map[string]int64 `json:"z_floor"`
}

// restoreJumpSize is max(10000, 2*peak*days): twice the busiest day ever
// seen, for every day the dead till may have kept trading. It never exceeds
// restoreJumpMax and cannot overflow (ut-docs#6924).
func restoreJumpSize(peak, days int64) int64 {
	if peak > 0 && days > restoreJumpMax/(2*peak) {
		return restoreJumpMax
	}
	j := 2 * peak * days
	if j > restoreJumpMax {
		j = restoreJumpMax
	}
	if j < restoreMinJump {
		j = restoreMinJump
	}
	return j
}

// roundUpTo rounds n up to a multiple of m.
func roundUpTo(n, m int64) int64 {
	if r := n % m; r != 0 {
		return n + m - r
	}
	return n
}

// JumpNumbersAfterRestore moves the receipt and Z sequences ahead after a
// backup restore, in one transaction. now is the boot time. Receipts jump by
// restoreJumpSize past the highest number seen (a floor from an earlier
// restore counts), rounded up to a multiple of 1000. Each Z kind jumps by the
// days since the last activity (days already counts one spare day): at most
// one end-of-day closes per local day, so that is a hard bound on what the
// lost till could have issued.
//
// token identifies one restore (the marker file's content). It makes the call
// idempotent: the jump, the token and the audit row (restore_numbers_jumped)
// commit together, and a call whose token is already stored changes nothing
// and returns AlreadyApplied, so a crash before the marker file is removed
// cannot jump twice.
//
// The floors it starts from include those carried from the live till when the
// restore was applied (internal/db mergeRestoreFloors, ut-docs#6293), so a
// repeat restore never re-issues a number sold since the previous one.
func (r *POSRepo) JumpNumbersAfterRestore(ctx context.Context, now time.Time, token string) (RestoreJump, error) {
	var out RestoreJump
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return out, fmt.Errorf("restore jump: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// believe reports whether a stored number is a usable counter: in
	// [0, restoreInputMax]. Anything else is corrupt and ignored, with a
	// warning so the gap is not silent (ut-docs#6924).
	believe := func(what string, v int64) bool {
		if v < 0 || v > restoreInputMax {
			logging.L().Warnf("restore jump: ignoring %s %d outside 0..%d", what, v, restoreInputMax)
			return false
		}
		return true
	}

	getSetting := func(key string) (string, error) {
		var v string
		err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return v, err
	}
	setSetting := func(key, value string) error {
		_, err := tx.ExecContext(ctx, `
INSERT INTO settings (key, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
			key, value, now.UTC().Format(time.RFC3339))
		return err
	}

	if token != "" {
		done, err := getSetting(RestoreJumpTokenKey)
		if err != nil {
			return out, fmt.Errorf("restore jump: token: %w", err)
		}
		if done == token {
			out.AlreadyApplied = true
			return out, nil
		}
	}

	prefix, err := getSetting("sync.receipt_prefix")
	if err != nil {
		return out, fmt.Errorf("restore jump: receipt prefix: %w", err)
	}

	// The highest number this till ever issued under the prefix: live and
	// archived sales and the stored counter (ut-docs#6663). Live sales alone
	// are far below it on a till that cleared its history, and a floor
	// under the counter would reserve nothing.
	maxReceipt, err := maxReceiptSeqUpTo(ctx, tx, prefix, restoreInputMax)
	if err != nil {
		return out, fmt.Errorf("restore jump: last receipt: %w", err)
	}
	if last, _, ok, err := seriesLast(ctx, tx, numberKindReceipt, prefix); err != nil {
		return out, fmt.Errorf("restore jump: receipt counter: %w", err)
	} else if ok && last > maxReceipt && believe("receipt counter", last) {
		maxReceipt = last
	}
	lastReceipt := maxReceipt
	if fp, err := getSetting(RestoreReceiptFloorPrefixKey); err != nil {
		return out, fmt.Errorf("restore jump: receipt floor prefix: %w", err)
	} else if fp == prefix {
		fv, err := getSetting(RestoreReceiptFloorKey)
		if err != nil {
			return out, fmt.Errorf("restore jump: receipt floor: %w", err)
		}
		if f, ok := restorenum.ParseFloor(fv); ok && f > lastReceipt && believe("receipt floor", f) {
			lastReceipt = f
		}
	}

	// datetime() normalises sales' RFC3339 and report_archive's space form
	// to one UTC text shape, so MAX() is chronological.
	var lastActivityRaw sql.NullString
	if err := tx.QueryRowContext(ctx, `
SELECT MAX(t) FROM (
  SELECT MAX(datetime(created_at)) AS t FROM sales
  UNION ALL
  SELECT MAX(datetime(created_at)) AS t FROM report_archive)`).Scan(&lastActivityRaw); err != nil {
		return out, fmt.Errorf("restore jump: last activity: %w", err)
	}
	days := int64(1)
	lastActivityAt := ""
	if lastActivityRaw.Valid {
		if la := parseStoredTime(lastActivityRaw.String); !la.IsZero() {
			lastActivityAt = la.UTC().Format(time.RFC3339)
			if d := now.Sub(la); d > 0 {
				days = int64((d+24*time.Hour-1)/(24*time.Hour)) + 1
			}
			if days > restoreJumpMax {
				days = restoreJumpMax
			}
		}
	}

	var peak int64
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(MAX(n), 0) FROM (
  SELECT COUNT(*) AS n FROM sales WHERE receipt_no LIKE ? || '%'
  GROUP BY date(created_at, 'localtime'))`, prefix).Scan(&peak); err != nil {
		return out, fmt.Errorf("restore jump: busiest day: %w", err)
	}

	receiptFloor := roundUpTo(lastReceipt+restoreJumpSize(peak, days), restoreFloorRounding)

	// Z floors: every kind that has numbers, and always the end-of-day.
	kinds := map[string]bool{"eod": true}
	krows, err := tx.QueryContext(ctx, `SELECT DISTINCT kind FROM report_archive WHERE z_number IS NOT NULL`)
	if err != nil {
		return out, fmt.Errorf("restore jump: z kinds: %w", err)
	}
	for krows.Next() {
		var k string
		if err := krows.Scan(&k); err != nil {
			_ = krows.Close()
			return out, fmt.Errorf("restore jump: scan z kind: %w", err)
		}
		kinds[k] = true
	}
	if err := krows.Err(); err != nil {
		_ = krows.Close()
		return out, err
	}
	_ = krows.Close()

	out.ZFrom = map[string]int64{}
	out.ZFloor = map[string]int64{}
	var lastEod, nextEod int64
	for kind := range kinds {
		var maxZ int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(z_number), 0) FROM report_archive WHERE kind = ? AND z_number BETWEEN 0 AND ?`,
			kind, restoreInputMax).Scan(&maxZ); err != nil {
			return out, fmt.Errorf("restore jump: last z (%s): %w", kind, err)
		}
		base := maxZ
		fv, err := getSetting(RestoreZFloorKeyPrefix + kind)
		if err != nil {
			return out, fmt.Errorf("restore jump: z floor (%s): %w", kind, err)
		}
		if f, ok := restorenum.ParseFloor(fv); ok && f > base && believe("z floor ("+kind+")", f) {
			base = f
		}
		zFloor := base + days
		if err := setSetting(RestoreZFloorKeyPrefix+kind, strconv.FormatInt(zFloor, 10)); err != nil {
			return out, fmt.Errorf("restore jump: save z floor (%s): %w", kind, err)
		}
		out.ZFrom[kind] = maxZ
		out.ZFloor[kind] = zFloor
		if kind == "eod" {
			lastEod, nextEod = base, zFloor+1
		}
	}

	if err := setSetting(RestoreReceiptFloorKey, strconv.FormatInt(receiptFloor, 10)); err != nil {
		return out, fmt.Errorf("restore jump: save receipt floor: %w", err)
	}
	if err := setSetting(RestoreReceiptFloorPrefixKey, prefix); err != nil {
		return out, fmt.Errorf("restore jump: save receipt floor prefix: %w", err)
	}

	marker := RestoreMarker{
		RestoredAt:     now.UTC().Format(time.RFC3339),
		LastActivityAt: lastActivityAt,
		ReceiptPrefix:  prefix,
		NextReceipt:    fmt.Sprintf("%s%09d", prefix, receiptFloor+1),
		LastZ:          lastEod,
		NextZ:          nextEod,
	}
	if lastReceipt > 0 {
		marker.LastReceipt = fmt.Sprintf("%s%09d", prefix, lastReceipt)
	}
	raw, err := json.Marshal(marker)
	if err != nil {
		return out, fmt.Errorf("restore jump: marshal marker: %w", err)
	}
	if err := setSetting(RestoreMarkerKey, string(raw)); err != nil {
		return out, fmt.Errorf("restore jump: save marker: %w", err)
	}
	if token != "" {
		if err := setSetting(RestoreJumpTokenKey, token); err != nil {
			return out, fmt.Errorf("restore jump: save token: %w", err)
		}
	}
	out.Marker = marker
	out.Days = days
	out.PeakPerDay = peak
	out.ReceiptFrom = maxReceipt
	out.ReceiptFloor = receiptFloor
	if err := r.InsertAudit(ctx, tx, "system", "backup", "restore", "restore_numbers_jumped", out,
		now.UTC().Format(time.RFC3339), ""); err != nil {
		return RestoreJump{}, fmt.Errorf("restore jump: audit: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return RestoreJump{}, fmt.Errorf("restore jump: commit: %w", err)
	}
	return out, nil
}

// PendingRestoreMarker returns the recorded restore jump that no Z report has
// carried yet, or nil when there is none.
func (r *POSRepo) PendingRestoreMarker(ctx context.Context) (*RestoreMarker, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, RestoreMarkerKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("pending restore marker: %w", err)
	}
	var m RestoreMarker
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil, fmt.Errorf("pending restore marker: decode: %w", err)
	}
	return &m, nil
}

// ClearRestoreMarker drops the marker once a Z report has carried it. The
// number floors stay: they keep guarding the sequences.
func (r *POSRepo) ClearRestoreMarker(ctx context.Context) error {
	if _, err := r.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, RestoreMarkerKey); err != nil {
		return fmt.Errorf("clear restore marker: %w", err)
	}
	return nil
}

// AppliedRestoreJumpToken returns the token of the last restore whose number
// jump was applied, "" when none was.
func (r *POSRepo) AppliedRestoreJumpToken(ctx context.Context) (string, error) {
	var v string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, RestoreJumpTokenKey).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("applied restore jump token: %w", err)
	}
	return v, nil
}
