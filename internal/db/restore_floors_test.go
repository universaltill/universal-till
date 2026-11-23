package db

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// issueReceipt issues one receipt number the way a sale does (counter +
// a sales row) and returns it.
func issueReceipt(t *testing.T, d *DB) string {
	t.Helper()
	ctx := context.Background()
	tx, err := d.Begin()
	if err != nil {
		t.Fatal(err)
	}
	no, err := data.NewPOSRepo(d.DB).NextReceiptNo(ctx, tx)
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("next receipt: %v", err)
	}
	if _, err := tx.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total, created_at) VALUES (?, ?, 100, 100, ?)`,
		"s-"+no, no, time.Now().UTC().Format(time.RFC3339)); err != nil {
		_ = tx.Rollback()
		t.Fatalf("insert sale %s: %v", no, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return no
}

// closeZ closes one report of kind and returns its Z number.
func closeZ(t *testing.T, d *DB, kind string) int64 {
	t.Helper()
	period := fmt.Sprintf("p-%s-%d", kind, time.Now().UnixNano())
	created, err := data.NewPOSRepo(d.DB).ArchiveReport(context.Background(), kind, period, []byte(`{}`), "", "", time.Time{})
	if err != nil || !created {
		t.Fatalf("archive %s: created=%v err=%v", kind, created, err)
	}
	var z int64
	if err := d.QueryRow(`SELECT z_number FROM report_archive WHERE kind = ? AND period = ?`, kind, period).Scan(&z); err != nil {
		t.Fatal(err)
	}
	return z
}

// makeBackup copies d into dbPath's backups folder under a fresh name.
func makeBackup(t *testing.T, d *DB, dbPath, label string) string {
	t.Helper()
	dir, err := BackupDir(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	name := backupPrefix + label + ".db"
	if _, err := d.Exec(`VACUUM INTO ?`, filepath.Join(dir, name)); err != nil {
		t.Fatalf("backup %s: %v", label, err)
	}
	return name
}

// restoreAndJump stages name, applies it and runs the boot jump, as a
// restart would. The live DB must be closed.
func restoreAndJump(t *testing.T, dbPath, name string) {
	t.Helper()
	if err := StageRestore(dbPath, name); err != nil {
		t.Fatalf("stage: %v", err)
	}
	applied, err := ApplyPendingRestore(dbPath)
	if err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	d, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := data.NewPOSRepo(d.DB).JumpNumbersAfterRestore(context.Background(), time.Now(), NumberJumpToken(dbPath)); err != nil {
		t.Fatalf("jump: %v", err)
	}
	ClearNumberJumpPending(dbPath)
}

func receiptSeq(t *testing.T, no string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(no[len(no)-9:], 10, 64)
	if err != nil {
		t.Fatalf("receipt %q: %v", no, err)
	}
	return n
}

// seedBackupHistory gives d a few sales and eod Zs, returning nothing.
func seedBackupHistory(t *testing.T, d *DB) {
	t.Helper()
	for range 3 {
		issueReceipt(t, d)
	}
	closeZ(t, d, "eod")
	closeZ(t, d, "eod")
}

// ut-docs#6293: restoring a second time (the same backup again) must not
// re-issue the numbers sold since the first restore.
func TestRepeatRestoreNeverReissuesNumbers(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedBackupHistory(t, d)
	name := makeBackup(t, d, path, "b")
	d.Close()

	restoreAndJump(t, path, name)

	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var maxReceipt, maxZ int64
	for range 2 {
		if n := receiptSeq(t, issueReceipt(t, d)); n > maxReceipt {
			maxReceipt = n
		}
		if z := closeZ(t, d, "eod"); z > maxZ {
			maxZ = z
		}
	}
	d.Close()

	restoreAndJump(t, path, name)

	d = openTest(t, path)
	if n := receiptSeq(t, issueReceipt(t, d)); n <= maxReceipt {
		t.Errorf("next receipt %d re-issues a number up to %d", n, maxReceipt)
	}
	if z := closeZ(t, d, "eod"); z <= maxZ {
		t.Errorf("next eod Z %d re-issues a number up to %d", z, maxZ)
	}
}

// openLive opens dbPath, registering cleanup.
func openLive(t *testing.T, dbPath string) *DB {
	t.Helper()
	return openTest(t, dbPath)
}

// applyOnly stages and applies name (no boot jump) and returns the restored
// DB for inspection.
func applyOnly(t *testing.T, dbPath, name string) *DB {
	t.Helper()
	if err := StageRestore(dbPath, name); err != nil {
		t.Fatalf("stage: %v", err)
	}
	applied, err := ApplyPendingRestore(dbPath)
	if err != nil || !applied {
		t.Fatalf("apply: %v %v", applied, err)
	}
	return openLive(t, dbPath)
}

func wantSetting(t *testing.T, d *DB, key, want string) {
	t.Helper()
	if got, _ := getSetting(t, d.DB, key); got != want {
		t.Errorf("%s = %q, want %q", key, got, want)
	}
}

func wantNoSetting(t *testing.T, d *DB, key string) {
	t.Helper()
	if got, ok := getSetting(t, d.DB, key); ok {
		t.Errorf("%s = %q, want absent", key, got)
	}
}

func insertArchivedZ(t *testing.T, d *DB, kind string, z int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO report_archive (id, kind, period, content_json, z_number, created_at) VALUES (?, ?, ?, '{}', ?, ?)`,
		fmt.Sprintf("ra-%s-%d", kind, z), kind, fmt.Sprintf("p-%s-%d", kind, z), z,
		time.Now().UTC().Format("2006-01-02 15:04:05")); err != nil {
		t.Fatal(err)
	}
}

func insertSaleNo(t *testing.T, d *DB, no string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total, created_at) VALUES (?, ?, 100, 100, ?)`,
		"s-"+no, no, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func insertArchivedSaleNo(t *testing.T, d *DB, no string) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO reset_batches (id, created_at) VALUES ('rb-1', ?) ON CONFLICT DO NOTHING`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`INSERT INTO sales_archive (id, receipt_no, status, sale_type, tender_type, offline, sync_status, sync_attempts,
currency, subtotal, discount_total, tax_total, total, rounding, created_at, till_id, service_charge_amount, order_type, order_status, reset_batch_id)
VALUES (?, ?, 'completed', 'sale', 'cash', 0, 'synced', 0, 'GBP', 100, 0, 0, 100, 0, ?, 't', 0, 'sale', 'done', 'rb-1')`,
		"sa-"+no, no, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func setCounter(t *testing.T, d *DB, series string, last int64) {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO number_series (kind, series, last, period) VALUES ('receipt', ?, ?, '')
ON CONFLICT(kind, series) DO UPDATE SET last = excluded.last`, series, last); err != nil {
		t.Fatal(err)
	}
}

// emptyBackup is a backup of a fresh DB with the given settings.
func emptyBackup(t *testing.T, dbPath, label string, settings map[string]string) string {
	t.Helper()
	b, err := Open(filepath.Join(t.TempDir(), "unitill-pos.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for k, v := range settings {
		setSetting(t, b.DB, k, v)
	}
	return makeBackup(t, b, dbPath, label)
}

func TestRepeatRestoreOfOlderBackupNeverReissuesNumbers(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	issueReceipt(t, d)
	closeZ(t, d, "eod")
	older := makeBackup(t, d, path, "b0")
	seedBackupHistory(t, d)
	newer := makeBackup(t, d, path, "b1")
	d.Close()

	restoreAndJump(t, path, newer)
	d, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var maxReceipt, maxZ int64
	for range 2 {
		if n := receiptSeq(t, issueReceipt(t, d)); n > maxReceipt {
			maxReceipt = n
		}
		if z := closeZ(t, d, "eod"); z > maxZ {
			maxZ = z
		}
	}
	d.Close()

	restoreAndJump(t, path, older)
	d = openTest(t, path)
	if n := receiptSeq(t, issueReceipt(t, d)); n <= maxReceipt {
		t.Errorf("next receipt %d re-issues a number up to %d", n, maxReceipt)
	}
	if z := closeZ(t, d, "eod"); z <= maxZ {
		t.Errorf("next eod Z %d re-issues a number up to %d", z, maxZ)
	}
}

// A live till with no floor keys (first restore, or keys lost) is covered by
// its real maxima.
func TestRestoreSeedsFloorsFromLiveMaxima(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	seedBackupHistory(t, d)
	name := makeBackup(t, d, path, "b")
	insertSaleNo(t, d, "000050003")
	setCounter(t, d, "", 50007)
	insertArchivedZ(t, d, "eod", 40)
	d.Close()

	r := applyOnly(t, path, name)
	wantSetting(t, r, restoreReceiptFloorKey, "50007")
	wantSetting(t, r, restoreReceiptFloorPrefixKey, "")
	wantSetting(t, r, restoreZFloorKeyPrefix+"eod", "40")
	r.Close()

	restoreAndJump(t, path, name) // second restore of the same file: jump on top
	r = openLive(t, path)
	if n := receiptSeq(t, issueReceipt(t, r)); n <= 50007 {
		t.Errorf("next receipt %d not above live max 50007", n)
	}
	if z := closeZ(t, r, "eod"); z <= 40 {
		t.Errorf("next Z %d not above live max 40", z)
	}
}

func TestRestoreFloorMergeKeepsHigherStagedFloor(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	setSetting(t, d.DB, restoreReceiptFloorKey, "1000")
	setSetting(t, d.DB, restoreReceiptFloorPrefixKey, "")
	setSetting(t, d.DB, restoreZFloorKeyPrefix+"eod", "10")
	d.Close()
	name := emptyBackup(t, path, "hi", map[string]string{
		restoreReceiptFloorKey: "90000", restoreReceiptFloorPrefixKey: "", restoreZFloorKeyPrefix + "eod": "90",
	})
	r := applyOnly(t, path, name)
	wantSetting(t, r, restoreReceiptFloorKey, "90000")
	wantSetting(t, r, restoreZFloorKeyPrefix+"eod", "90")
}

func TestRestoreFloorIgnoresLiveFloorForOtherPrefix(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	setSetting(t, d.DB, restoreReceiptFloorKey, "70000")
	setSetting(t, d.DB, restoreReceiptFloorPrefixKey, "T9-")
	d.Close()
	name := emptyBackup(t, path, "p", nil)
	r := applyOnly(t, path, name)
	wantNoSetting(t, r, restoreReceiptFloorKey)
	wantNoSetting(t, r, restoreReceiptFloorPrefixKey)
}

func TestRestoreFloorFollowsKeptReplacementPrefix(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	setSetting(t, d.DB, "sync.receipt_prefix", "M3f2a9c1e-")
	insertSaleNo(t, d, "M3f2a9c1e-000000500")
	d.Close()
	name := emptyBackup(t, path, "dead", map[string]string{
		"sync.receipt_prefix": "T1-", restoreReceiptFloorKey: "99000", restoreReceiptFloorPrefixKey: "T1-",
	})
	r := applyOnly(t, path, name)
	wantSetting(t, r, "sync.receipt_prefix", "M3f2a9c1e-")
	wantSetting(t, r, restoreReceiptFloorPrefixKey, "M3f2a9c1e-")
	wantSetting(t, r, restoreReceiptFloorKey, "500")
}

func TestRestoreCarriesZFloorForKindAbsentFromBackup(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	setSetting(t, d.DB, restoreZFloorKeyPrefix+"x", "5")
	insertArchivedZ(t, d, "x", 8)
	insertArchivedZ(t, d, "y", 3)
	d.Close()
	r := applyOnly(t, path, emptyBackup(t, path, "z", nil))
	wantSetting(t, r, restoreZFloorKeyPrefix+"x", "8")
	wantSetting(t, r, restoreZFloorKeyPrefix+"y", "3")
}

func TestRestoreKeepsStagedMarkerAndJumpToken(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	setSetting(t, d.DB, data.RestoreMarkerKey, "live-marker")
	setSetting(t, d.DB, data.RestoreJumpTokenKey, "live-token")
	setSetting(t, d.DB, restoreReceiptFloorKey, "5000")
	setSetting(t, d.DB, restoreReceiptFloorPrefixKey, "")
	d.Close()
	name := emptyBackup(t, path, "m", map[string]string{
		data.RestoreMarkerKey: "backup-marker", data.RestoreJumpTokenKey: "backup-token",
	})
	r := applyOnly(t, path, name)
	wantSetting(t, r, data.RestoreMarkerKey, "backup-marker")
	wantSetting(t, r, data.RestoreJumpTokenKey, "backup-token")
	wantSetting(t, r, restoreReceiptFloorKey, "5000")
}

func TestParseRestoreFloor(t *testing.T) {
	for in, want := range map[string]struct {
		v  int64
		ok bool
	}{"0": {0, true}, "42": {42, true}, "": {0, false}, "abc": {0, false}, "-5": {0, false}, "1.5": {0, false}} {
		if v, ok := parseRestoreFloor(in); v != want.v || ok != want.ok {
			t.Errorf("parseRestoreFloor(%q) = %d,%v want %d,%v", in, v, ok, want.v, want.ok)
		}
	}
}

func TestRestoreIgnoresGarbageFloorValues(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	setSetting(t, d.DB, restoreReceiptFloorKey, "abc")
	setSetting(t, d.DB, restoreReceiptFloorPrefixKey, "")
	setSetting(t, d.DB, restoreZFloorKeyPrefix+"eod", "-5")
	d.Close()
	r := applyOnly(t, path, emptyBackup(t, path, "g", nil))
	wantNoSetting(t, r, restoreReceiptFloorKey)
	wantNoSetting(t, r, restoreZFloorKeyPrefix+"eod")
}

func TestRestoreCarriesFloorsWhenLiveSalesTableMissing(t *testing.T) {
	for _, tbl := range []string{"sales", "report_archive"} {
		t.Run(tbl, func(t *testing.T) {
			path := testDBPath(t)
			d, err := Open(path)
			if err != nil {
				t.Fatal(err)
			}
			setSetting(t, d.DB, restoreReceiptFloorKey, "30000")
			setSetting(t, d.DB, restoreReceiptFloorPrefixKey, "")
			setSetting(t, d.DB, restoreZFloorKeyPrefix+"eod", "12")
			setSetting(t, d.DB, "marketplace.device_id", "live-dev")
			if _, err := d.Exec(`ALTER TABLE ` + tbl + ` RENAME TO ` + tbl + `_gone`); err != nil {
				t.Fatal(err)
			}
			d.Close()
			r := applyOnly(t, path, emptyBackup(t, path, "t"+tbl, nil))
			wantSetting(t, r, restoreReceiptFloorKey, "30000")
			wantSetting(t, r, restoreZFloorKeyPrefix+"eod", "12")
			wantSetting(t, r, "marketplace.device_id", "live-dev")
		})
	}
}

func TestLANJoinLeavesRestoreKeysUntouched(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	setSetting(t, d.DB, restoreReceiptFloorKey, "70000")
	setSetting(t, d.DB, restoreReceiptFloorPrefixKey, "")
	setSetting(t, d.DB, restoreZFloorKeyPrefix+"eod", "70")
	d.Close()
	name := emptyBackup(t, path, "lan", map[string]string{restoreReceiptFloorKey: "10", restoreReceiptFloorPrefixKey: "", restoreZFloorKeyPrefix + "eod": "1"})
	if err := os.WriteFile(ReplicaIdentityPath(path), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := StageRestore(path, name); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPendingRestore(path); err != nil {
		t.Fatalf("apply: %v", err)
	}
	r := openLive(t, path)
	wantSetting(t, r, restoreReceiptFloorKey, "10")
	wantSetting(t, r, restoreZFloorKeyPrefix+"eod", "1")
}

// The db-side maxima must equal what the jump derives in internal/data.
func TestLiveNumberMaximaMirrorData(t *testing.T) {
	for _, counter := range []int64{950, 3} {
		src := filepath.Join(t.TempDir(), "unitill-pos.db")
		d, err := Open(src)
		if err != nil {
			t.Fatal(err)
		}
		// The huge rows are corrupt; both sides must ignore them alike (ut-docs#6906).
		for _, no := range []string{"000000120", "9-000000005", "T2-000000007", "T2-abc", "99999999999999999999"} {
			insertSaleNo(t, d, no)
		}
		insertArchivedSaleNo(t, d, "T2-000000900")
		insertArchivedSaleNo(t, d, "T2-99999999999999999999")
		setCounter(t, d, "T2-", counter)
		insertArchivedZ(t, d, "eod", 7)
		insertArchivedZ(t, d, "eod", 9223372036854775000)
		insertArchivedZ(t, d, "x", 3)
		d.Close()

		for _, prefix := range []string{"", "T2-", "M3f2a9c1e-"} {
			t.Run(fmt.Sprintf("counter%d/%q", counter, prefix), func(t *testing.T) {
				cp := filepath.Join(t.TempDir(), "unitill-pos.db")
				raw, err := os.ReadFile(src)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(cp, raw, 0o644); err != nil {
					t.Fatal(err)
				}
				c := openTest(t, cp)
				if prefix != "" {
					setSetting(t, c.DB, "sync.receipt_prefix", prefix)
				}
				got, err := liveReceiptMax(c.DB, prefix)
				if err != nil {
					t.Fatal(err)
				}
				zm, err := liveZMax(c.DB)
				if err != nil {
					t.Fatal(err)
				}
				j, err := data.NewPOSRepo(c.DB).JumpNumbersAfterRestore(context.Background(), time.Now(), "")
				if err != nil {
					t.Fatal(err)
				}
				if got != j.ReceiptFrom {
					t.Errorf("liveReceiptMax = %d, data jump ReceiptFrom = %d", got, j.ReceiptFrom)
				}
				for k, v := range j.ZFrom {
					if zm[k] != v {
						t.Errorf("liveZMax[%s] = %d, data ZFrom = %d", k, zm[k], v)
					}
				}
				for k, v := range zm {
					if j.ZFrom[k] != v {
						t.Errorf("liveZMax[%s] = %d, data ZFrom = %d", k, v, j.ZFrom[k])
					}
				}
			})
		}
	}
}

func TestRestoreFloorKeysMirrorData(t *testing.T) {
	if restoreReceiptFloorKey != data.RestoreReceiptFloorKey ||
		restoreReceiptFloorPrefixKey != data.RestoreReceiptFloorPrefixKey ||
		restoreZFloorKeyPrefix != data.RestoreZFloorKeyPrefix {
		t.Fatal("restore floor key constants drifted from internal/data")
	}
	if restoreNumberMax != data.RestoreNumberMax {
		t.Fatal("restoreNumberMax drifted from internal/data")
	}
}

// ut-docs#6906: a huge live floor, receipt_no or Z is corrupt: the merge
// carries the sane value, never the huge one.
func TestRestoreFloorMergeIgnoresHugeLiveNumbers(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	setSetting(t, d.DB, restoreReceiptFloorKey, "9223372036854775000")
	setSetting(t, d.DB, restoreReceiptFloorPrefixKey, "")
	setSetting(t, d.DB, restoreZFloorKeyPrefix+"eod", "9223372036854775000")
	insertSaleNo(t, d, "000000120")
	insertSaleNo(t, d, "99999999999999999999")
	insertArchivedZ(t, d, "eod", 7)
	insertArchivedZ(t, d, "eod", 9223372036854775000)
	got, err := liveReceiptMax(d.DB, "")
	if err != nil || got != 120 {
		t.Fatalf("liveReceiptMax = %d, %v, want 120", got, err)
	}
	if zm, err := liveZMax(d.DB); err != nil || zm["eod"] != 7 {
		t.Fatalf("liveZMax = %v, %v, want eod 7", zm, err)
	}
	d.Close()
	name := emptyBackup(t, path, "huge", map[string]string{restoreReceiptFloorKey: "10", restoreReceiptFloorPrefixKey: "", restoreZFloorKeyPrefix + "eod": "1"})
	r := applyOnly(t, path, name)
	wantSetting(t, r, restoreReceiptFloorKey, "120")
	wantSetting(t, r, restoreZFloorKeyPrefix+"eod", "7")
}

// A source that cannot be read must not throw away the others (6293 review
// L1): the stored counter still bounds the receipts when sales is broken.
func TestLiveReceiptMaxKeepsReadableSources(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(`INSERT INTO number_series (kind, series, last) VALUES ('receipt', '', 42000)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(`ALTER TABLE sales RENAME TO sales_gone`); err != nil {
		t.Fatal(err)
	}
	got, err := liveReceiptMax(d.DB, "")
	if err == nil {
		t.Fatal("want an error naming the unreadable sales table")
	}
	if got != 42000 {
		t.Fatalf("liveReceiptMax = %d, want 42000 from the counter", got)
	}
}

// The live counter and the prefixed query ignore huge values too (ut-docs#6906, Tester F3).
func TestLiveReceiptMaxIgnoresHugeCounterAndPrefixedRow(t *testing.T) {
	path := testDBPath(t)
	d, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	insertSaleNo(t, d, "T2-000000120")
	insertSaleNo(t, d, "T2-99999999999999999999")
	setCounter(t, d, "T2-", 9223372036854775000)
	if got, err := liveReceiptMax(d.DB, "T2-"); err != nil || got != 120 {
		t.Fatalf("liveReceiptMax = %d, %v, want 120", got, err)
	}
}
