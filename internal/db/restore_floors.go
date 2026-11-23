package db

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// The restore jump's floor keys. They mirror data.RestoreReceiptFloorKey,
// data.RestoreReceiptFloorPrefixKey and data.RestoreZFloorKeyPrefix (db
// cannot import data); TestRestoreFloorKeysMirrorData pins them.
const (
	restoreReceiptFloorKey       = "restore.receipt_floor"
	restoreReceiptFloorPrefixKey = "restore.receipt_floor_prefix"
	restoreZFloorKeyPrefix       = "restore.z_floor."
)

// restoreNumberMax mirrors data.RestoreNumberMax (ut-docs#6906): a larger
// number is corrupt and ignored. TestRestoreFloorKeysMirrorData pins it.
const restoreNumberMax = 1_000_000_000_000_000

// liveNumbers is what the live (about to be replaced) database knows about
// the numbers it issued: its restore floors and its real maxima.
type liveNumbers struct {
	receiptFloor       int64
	receiptFloorPrefix string
	hasReceiptFloor    bool             // a parseable floor and its prefix row were present
	zFloor             map[string]int64 // report kind -> floor
	receiptMax         int64            // highest receipt counter under the queried prefix
	zMax               map[string]int64 // report kind -> MAX(z_number)
}

// parseRestoreFloor reads a stored floor; ok is false for anything that is
// not a non-negative base-10 integer (such a value counts as absent).
func parseRestoreFloor(s string) (int64, bool) {
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil || v < 0 || v > restoreNumberMax {
		return 0, false
	}
	return v, true
}

// readLiveNumbers reads the live DB's restore floors and, best effort, its
// maxima under receipt prefix. No live DB, or one without a settings table,
// gives the zero value. err means the live DB could not be read (its floors
// are unusable); maximaErr means the settings were read but a maxima query
// failed (the floors are still returned).
func readLiveNumbers(dbPath, prefix string) (ln liveNumbers, maximaErr, err error) {
	if _, serr := os.Stat(dbPath); serr != nil {
		return liveNumbers{}, nil, nil
	}
	ld, err := openBackupFile(dbPath)
	if err != nil {
		return liveNumbers{}, nil, err
	}
	defer ld.Close()
	var n int
	if err := ld.QueryRow(`SELECT COUNT(*) FROM sqlite_schema WHERE type = 'table' AND name = 'settings'`).Scan(&n); err != nil {
		return liveNumbers{}, nil, err
	}
	if n == 0 {
		return liveNumbers{}, nil, nil
	}
	floors, err := readFloorRows(ld)
	if err != nil {
		return liveNumbers{}, nil, err
	}
	ln.zFloor = map[string]int64{}
	var floorPrefix string
	var haveFP, haveF bool
	for k, v := range floors {
		switch {
		case k == restoreReceiptFloorPrefixKey:
			floorPrefix, haveFP = v, true
		case k == restoreReceiptFloorKey:
			if f, ok := parseRestoreFloor(v); ok {
				ln.receiptFloor, haveF = f, true
			}
		case strings.HasPrefix(k, restoreZFloorKeyPrefix):
			if f, ok := parseRestoreFloor(v); ok {
				ln.zFloor[strings.TrimPrefix(k, restoreZFloorKeyPrefix)] = f
			}
		}
	}
	ln.receiptFloorPrefix = floorPrefix
	ln.hasReceiptFloor = haveF && haveFP

	// A partial result still counts: each source that could be read is a
	// real lower bound (6293 review L1).
	m, merr := liveReceiptMax(ld, prefix)
	ln.receiptMax = m
	if merr != nil {
		maximaErr = merr
	}
	if zm, zerr := liveZMax(ld); zerr != nil {
		if maximaErr == nil {
			maximaErr = zerr
		}
	} else {
		ln.zMax = zm
	}
	return ln, maximaErr, nil
}

// readFloorRows returns every restore floor row of a settings table.
func readFloorRows(q interface {
	Query(string, ...any) (*sql.Rows, error)
}) (map[string]string, error) {
	rows, err := q.Query(`SELECT key, value FROM settings WHERE key IN (?, ?) OR key LIKE 'restore.z\_floor.%' ESCAPE '\'`,
		restoreReceiptFloorKey, restoreReceiptFloorPrefixKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// liveReceiptMax mirrors data.maxReceiptSeq plus the stored receipt counter
// (data.seriesLast) under prefix: the highest receipt counter the till has
// issued there. TestLiveNumberMaximaMirrorData pins it to the data
// definition through JumpNumbersAfterRestore. A source that cannot be read
// is skipped and reported in the error; top is the highest of the rest.
func liveReceiptMax(q *sql.DB, prefix string) (int64, error) {
	var top int64
	var errs []error
	for _, tbl := range []string{"sales", "sales_archive"} {
		var v sql.NullInt64
		var err error
		if prefix == "" {
			err = q.QueryRow(`SELECT MAX(CAST(receipt_no AS INTEGER)) FROM `+tbl+
				` WHERE receipt_no NOT LIKE '%-%' AND CAST(receipt_no AS INTEGER) <= ?`, restoreNumberMax).Scan(&v)
		} else {
			err = q.QueryRow(`SELECT MAX(CAST(substr(receipt_no, ?) AS INTEGER)) FROM `+tbl+
				` WHERE receipt_no LIKE ? || '%' AND CAST(substr(receipt_no, ?) AS INTEGER) <= ?`,
				len(prefix)+1, prefix, len(prefix)+1, restoreNumberMax).Scan(&v)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("max receipt in %s: %w", tbl, err))
			continue
		}
		if v.Valid && v.Int64 > top {
			top = v.Int64
		}
	}
	var last int64
	err := q.QueryRow(`SELECT last FROM number_series WHERE kind = 'receipt' AND series = ?`, prefix).Scan(&last)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		errs = append(errs, fmt.Errorf("receipt counter: %w", err))
	}
	if err == nil && last > top && last <= restoreNumberMax {
		top = last
	}
	return top, errors.Join(errs...)
}

// liveZMax is the highest numbered Z per report kind.
func liveZMax(q *sql.DB) (map[string]int64, error) {
	rows, err := q.Query(`SELECT kind, MAX(z_number) FROM report_archive WHERE z_number IS NOT NULL AND z_number <= ? GROUP BY kind`, restoreNumberMax)
	if err != nil {
		return nil, fmt.Errorf("max z: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var v int64
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("max z: %w", err)
		}
		out[k] = v
	}
	return out, rows.Err()
}

// mergeRestoreFloors raises the staged file's restore floors, in tx, to at
// least every number the live till could have issued (ut-docs#6293): a floor
// is only ever raised, never replaced by a lower live value. prefix is the
// receipt prefix the restored till will use. It returns the key/value pairs
// it wrote.
func mergeRestoreFloors(tx *sql.Tx, live liveNumbers, prefix string) ([][2]string, error) {
	staged, err := readFloorRows(tx)
	if err != nil {
		return nil, fmt.Errorf("read staged floors: %w", err)
	}
	var wrote [][2]string
	put := func(k, v string) error {
		if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, k, v); err != nil {
			return fmt.Errorf("carry %s: %w", k, err)
		}
		wrote = append(wrote, [2]string{k, v})
		return nil
	}

	// Receipts: only a floor for the prefix the restored till issues under
	// counts (data.restoreFloor ignores any other).
	var best int64
	have := false
	consider := func(v int64) {
		if !have || v > best {
			best, have = v, true
		}
	}
	var stagedF int64
	stagedHasF := false
	if sp, ok := staged[restoreReceiptFloorPrefixKey]; ok && sp == prefix {
		if f, ok := parseRestoreFloor(staged[restoreReceiptFloorKey]); ok {
			stagedF, stagedHasF = f, true
			consider(f)
		}
	}
	if live.hasReceiptFloor && live.receiptFloorPrefix == prefix {
		consider(live.receiptFloor)
	}
	if live.receiptMax > 0 {
		consider(live.receiptMax)
	}
	if have && (!stagedHasF || best != stagedF) {
		if err := put(restoreReceiptFloorKey, strconv.FormatInt(best, 10)); err != nil {
			return nil, err
		}
		if err := put(restoreReceiptFloorPrefixKey, prefix); err != nil {
			return nil, err
		}
	}

	// Z numbers, per kind.
	kinds := map[string]bool{}
	for k := range staged {
		if strings.HasPrefix(k, restoreZFloorKeyPrefix) {
			kinds[strings.TrimPrefix(k, restoreZFloorKeyPrefix)] = true
		}
	}
	for k := range live.zFloor {
		kinds[k] = true
	}
	for k := range live.zMax {
		kinds[k] = true
	}
	sorted := make([]string, 0, len(kinds))
	for k := range kinds {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, kind := range sorted {
		sf, stagedHas := parseRestoreFloor(staged[restoreZFloorKeyPrefix+kind])
		v := sf
		if lf := live.zFloor[kind]; lf > v {
			v = lf
		}
		if lm := live.zMax[kind]; lm > v {
			v = lm
		}
		if v > sf || (!stagedHas && v > 0) {
			if err := put(restoreZFloorKeyPrefix+kind, strconv.FormatInt(v, 10)); err != nil {
				return nil, err
			}
		}
	}
	return wrote, nil
}
