package data

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ut-docs#5831: a close belongs to the local day of the second BEFORE its
// bound (the last instant its half-open window covers).
func TestArchiveReport_GuardUsesSecondBeforeClose(t *testing.T) {
	if os.Getenv("UT_TEST_5831_TZ") == "1" {
		runGuardUsesSecondBeforeCloseBody(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestArchiveReport_GuardUsesSecondBeforeClose$", "-test.v")
	cmd.Env = append(os.Environ(), "UT_TEST_5831_TZ=1", "TZ=Europe/London")
	out, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "--- PASS") {
		t.Fatalf("subprocess failed: err=%v\n%s", err, out)
	}
}

func runGuardUsesSecondBeforeCloseBody(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	ctx := context.Background()
	d := time.Now().AddDate(0, 0, -200)
	// Winter day in London is UTC+0, so pick a January date for a stable
	// offset: local == UTC.
	y := d.Year()
	if d.Month() >= time.April && d.Month() <= time.October {
		y++
	}
	day := time.Date(y, time.January, 10, 12, 0, 0, 0, time.UTC)
	nextMidnight := time.Date(y, time.January, 11, 0, 0, 0, 0, time.UTC)

	if ok, err := dbx.repo.ArchiveReport(ctx, "eod", day.Format(time.RFC3339), []byte(`{}`), "", "", day); err != nil || !ok {
		t.Fatalf("first close: ok=%v err=%v", ok, err)
	}
	// Bound exactly at next local 00:00:00 is the close of the PREVIOUS day:
	// same day as the first close -> refused.
	if ok, err := dbx.repo.ArchiveReport(ctx, "eod", nextMidnight.Format(time.RFC3339), []byte(`{}`), "", "", nextMidnight); err != nil || ok {
		t.Fatalf("00:00:00 close belongs to day D and must be refused: ok=%v err=%v", ok, err)
	}
	// One second later it is the new day's close.
	later := nextMidnight.Add(time.Second)
	if ok, err := dbx.repo.ArchiveReport(ctx, "eod", later.Format(time.RFC3339), []byte(`{}`), "", "", later); err != nil || !ok {
		t.Fatalf("00:00:01 close is the next day's: ok=%v err=%v", ok, err)
	}
}

// With the close bound T, a sale stamped exactly T is AFTER the close, so the
// display-number reset must count created_at >= close.
func TestNextDisplayNo_SaleAtCloseBoundIsAfterClose(t *testing.T) {
	d := openBatch8DB(t, "display_no_bound.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)

	bound := time.Now().UTC().AddDate(0, 0, -200).Truncate(time.Second)
	mustExec(t, d, `INSERT INTO report_archive (id, kind, period, content_json, created_at)
VALUES ('ra-5831', 'eod', 'p5831', '{}', ?)`, bound.Format(archiveTimestampFmt))
	mustExec(t, d, `INSERT INTO sales (id, receipt_no, display_no, status, sale_type, currency, subtotal, total, created_at)
VALUES ('sale-5831', 'R5831', '1', 'completed', 'sale', 'GBP', 100, 100, ?)`, bound.Format(time.RFC3339))
	got, err := repo.NextDisplayNo(ctx, nil)
	if err != nil || got != "2" {
		t.Fatalf("sale stamped at the close bound is after the close: NextDisplayNo = (%q,%v), want 2", got, err)
	}
}

// ut-docs#5831 review B1: a writer that stamps its own row must take the stamp
// AFTER it holds the write lock, else a row that waited behind a Z carries a
// stamp from before the Z's bound but commits after it and lands in no Z.
// holdThenWrite holds the lock ~1.2 s, starts write() while it is held, and
// returns the instant the lock was released.
func holdThenWrite(t *testing.T, repo *POSRepo, write func() error) time.Time {
	t.Helper()
	ctx := context.Background()
	release, err := repo.HoldWriteLock(ctx)
	if err != nil {
		t.Fatal(err)
	}
	errc := make(chan error, 1)
	go func() { errc <- write() }()
	time.Sleep(1200 * time.Millisecond)
	released := time.Now()
	release()
	if err := <-errc; err != nil {
		t.Fatalf("write behind the held lock: %v", err)
	}
	return released
}

func assertStampedAfter(t *testing.T, stamp string, released time.Time) {
	t.Helper()
	got, err := time.Parse(time.RFC3339, stamp)
	if err != nil {
		t.Fatalf("stamp %q: %v", stamp, err)
	}
	if got.Before(released.Truncate(time.Second)) {
		t.Fatalf("row stamped %s, before the lock was released at %s: it would fall in no Z", stamp, released.UTC().Format(time.RFC3339))
	}
}

func TestInsertClearedBasket_StampedUnderWriteLock(t *testing.T) {
	d := b8OpenDB(t, "cleared-stamp-5831.db")
	repo := NewPOSRepo(d.DB)
	released := holdThenWrite(t, repo, func() error {
		_, err := repo.InsertClearedBasket(context.Background(), ClearedBasket{ID: "cb-5831", LineCount: 1})
		return err
	})
	var at string
	if err := d.DB.QueryRow(`SELECT created_at FROM cleared_baskets WHERE id='cb-5831'`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	assertStampedAfter(t, at, released)
}

func TestAdjustVoucherBalance_StampedUnderWriteLock(t *testing.T) {
	d := b8OpenDB(t, "voucher-stamp-5831.db")
	ctx := context.Background()
	repo := NewPOSRepo(d.DB)
	adjSeedUser(t, repo)
	vSeedVoucher(t, ctx, repo, "GS-5831", 1000)
	released := holdThenWrite(t, repo, func() error {
		in := adjIn("adj-5831", "GS-5831", 1000, 900, "count")
		in.Now = ""
		_, _, err := repo.AdjustVoucherBalance(ctx, nil, in)
		return err
	})
	var at string
	if err := d.DB.QueryRow(`SELECT created_at FROM voucher_transactions WHERE id='adj-5831'`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	assertStampedAfter(t, at, released)
}
