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
