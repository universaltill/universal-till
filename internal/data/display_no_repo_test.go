package data

import (
	"context"
	"fmt"
	"strconv"
	"testing"
	"time"
)

// ut-docs#6663 (c): the order number is unprefixed, per-till block of 100,
// wraps inside the block and skips numbers an open order still holds.

func nextOrder(t *testing.T, dbx *posTestDB) string {
	t.Helper()
	n, err := dbx.repo.NextDisplayNo(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// openOrder seeds a completed, still-open sale holding order number no.
func openOrder(t *testing.T, dbx *posTestDB, id, no string, at time.Time) {
	t.Helper()
	if _, err := dbx.d.DB.Exec(`INSERT INTO sales (id, receipt_no, display_no, subtotal, total, created_at) VALUES (?, ?, ?, 1, 1, ?)`,
		id, "r-"+id, no, at.UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
}

func closeOrder(t *testing.T, dbx *posTestDB, id string) {
	t.Helper()
	if _, err := dbx.d.DB.Exec(`UPDATE sales SET order_status = 'collected' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
}

func TestNextDisplayNo_Lane0WrapsFrom99To1(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, SaleDisplayNoSchemeKey, DisplayNoSchemeLifetimeNoReset)
	setRestoreSetting(t, dbx, "sync.receipt_prefix", "1-")
	for i := 1; i <= 99; i++ {
		if got := nextOrder(t, dbx); got != strconv.Itoa(i) {
			t.Fatalf("order %d = %q", i, got)
		}
	}
	if got := nextOrder(t, dbx); got != "1" {
		t.Fatalf("after 99 got %q, want wrap to 1 (never 0, never 100)", got)
	}
}

func TestNextDisplayNo_Lane1WrapsFrom199To100(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, SaleDisplayNoSchemeKey, DisplayNoSchemeLifetimeNoReset)
	setRestoreSetting(t, dbx, "sync.receipt_prefix", "2-")
	if got := nextOrder(t, dbx); got != "100" {
		t.Fatalf("till 2 first = %q, want 100", got)
	}
	for i := 101; i <= 199; i++ {
		if got := nextOrder(t, dbx); got != strconv.Itoa(i) {
			t.Fatalf("got %q, want %d", got, i)
		}
	}
	if got := nextOrder(t, dbx); got != "100" {
		t.Fatalf("after 199 got %q, want 100", got)
	}
}

func TestNextDisplayNo_NoPrefixEvenOnJoinedAndReplacementTills(t *testing.T) {
	for prefix, want := range map[string]string{
		"": "1", "1-": "1", "T3-": "200", "3-": "200", "A-": "1", "M3f2a9c1e-": "1",
	} {
		dbx := newPOSLifecycleTestDB(t)
		if prefix != "" {
			setRestoreSetting(t, dbx, "sync.receipt_prefix", prefix)
		}
		if got := nextOrder(t, dbx); got != want {
			t.Errorf("prefix %q: order = %q, want %q", prefix, got, want)
		}
	}
}

func TestNextDisplayNo_SkipsNumbersHeldByOpenOrders(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, SaleDisplayNoSchemeKey, DisplayNoSchemeLifetimeNoReset)
	setRestoreSetting(t, dbx, "sync.receipt_prefix", "1-")
	now := time.Now()
	if got := nextOrder(t, dbx); got != "1" {
		t.Fatalf("got %q, want 1", got)
	}
	openOrder(t, dbx, "a", "2", now)
	openOrder(t, dbx, "b", "3", now)
	if got := nextOrder(t, dbx); got != "4" { // 2 and 3 are still open
		t.Fatalf("got %q, want 4 (2 and 3 held by open orders)", got)
	}
	// Once collected, the number is free on the next wrap, not before.
	closeOrder(t, dbx, "a")
	for i := 5; i <= 99; i++ {
		nextOrder(t, dbx)
	}
	if got := nextOrder(t, dbx); got != "1" {
		t.Fatalf("wrap got %q, want 1", got)
	}
	if got := nextOrder(t, dbx); got != "2" {
		t.Fatalf("got %q, want 2 (collected, free again)", got)
	}
	if got := nextOrder(t, dbx); got != "4" {
		t.Fatalf("got %q, want 4 (3 still open)", got)
	}
}

func TestNextDisplayNo_SkipsNumbersHeldByCounterOrders(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, SaleDisplayNoSchemeKey, DisplayNoSchemeLifetimeNoReset)
	if _, err := dbx.d.DB.Exec(`INSERT INTO kiosk_counter_orders (id, display_no, order_type, lines_json, status, created_at)
VALUES ('k1', 'C-1', 'dine_in', '[]', 'held', ?)`, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if got := nextOrder(t, dbx); got != "2" {
		t.Fatalf("got %q, want 2 (1 is held by counter order C-1)", got)
	}
}

func TestNextDisplayNo_AllHeldReusesOldestAndNeverFails(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, SaleDisplayNoSchemeKey, DisplayNoSchemeLifetimeNoReset)
	setRestoreSetting(t, dbx, "sync.receipt_prefix", "1-")
	base := time.Now().Add(-time.Hour)
	for i := 1; i <= 99; i++ {
		at := base.Add(time.Duration(i) * time.Minute)
		if i == 37 {
			at = base.Add(-time.Hour) // the longest-waiting order
		}
		openOrder(t, dbx, "o"+strconv.Itoa(i), strconv.Itoa(i), at)
	}
	if got := nextOrder(t, dbx); got != "37" {
		t.Fatalf("got %q, want 37 (oldest holder)", got)
	}
}

func TestNextDisplayNo_TradingPeriodResetsAtEODButLifetimeDoesNot(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, "sync.receipt_prefix", "2-")
	nextOrder(t, dbx) // 100
	nextOrder(t, dbx) // 101
	if _, err := dbx.d.DB.Exec(`INSERT INTO report_archive (id, kind, period, content_json, created_at)
VALUES ('eod-1', 'eod', 'p1', '{}', ?)`, time.Now().UTC().Add(time.Minute).Format(archiveTimestampFmt)); err != nil {
		t.Fatal(err)
	}
	if got := nextOrder(t, dbx); got != "100" {
		t.Fatalf("after EOD got %q, want 100 (first of the block)", got)
	}
	if got := nextOrder(t, dbx); got != "101" {
		t.Fatalf("got %q, want 101", got)
	}
	setRestoreSetting(t, dbx, SaleDisplayNoSchemeKey, DisplayNoSchemeLifetimeNoReset)
	if got := nextOrder(t, dbx); got != "102" {
		t.Fatalf("lifetime scheme got %q, want 102 (no reset at EOD)", got)
	}
}

func TestNextDisplayNo_UpgradeContinuesAboveTodaysOldNumbers(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	now := time.Now()
	// Pre-#6663 till 1: numbers 1..40 issued today (closed), legacy T2-42 and
	// a prefixed number from another till must not count.
	for i := 1; i <= 40; i++ {
		openOrder(t, dbx, "old"+strconv.Itoa(i), strconv.Itoa(i), now)
		closeOrder(t, dbx, "old"+strconv.Itoa(i))
	}
	openOrder(t, dbx, "legacy", "T2-42", now)
	if got := nextOrder(t, dbx); got != "41" {
		t.Fatalf("got %q, want 41", got)
	}
}

func TestNextDisplayNo_TwoTillsNeverShareANumber(t *testing.T) {
	seen := map[string]int{}
	for till := 1; till <= 12; till++ {
		dbx := newPOSLifecycleTestDB(t)
		setRestoreSetting(t, dbx, SaleDisplayNoSchemeKey, DisplayNoSchemeLifetimeNoReset)
		prefix := fmt.Sprintf("%d-", till)
		if till == 3 {
			prefix = "T3-" // legacy form is the same till 3
		}
		setRestoreSetting(t, dbx, "sync.receipt_prefix", prefix)
		for i := 0; i < 250; i++ { // more than a block: wraps twice
			n := nextOrder(t, dbx)
			if owner, dup := seen[n]; dup && owner != till {
				t.Fatalf("number %s shown by till %d and till %d", n, owner, till)
			}
			seen[n] = till
		}
	}
}

func TestNextDisplayNo_RestoredCopyKeepsPrefixedCount(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, "sync.receipt_prefix", "R1a2b-")
	openOrder(t, dbx, "x", "R1a2b-7", time.Now())
	if got := nextOrder(t, dbx); got != "R1a2b-8" {
		t.Fatalf("got %q, want R1a2b-8", got)
	}
}

func TestCounterOrderDisplayNo_LaneWrapAndSkip(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_lane.db")
	mustExec(t, d, `INSERT INTO settings (key, value) VALUES ('sync.receipt_prefix', '3-')`)
	repo := NewKioskCounterOrdersRepo(d.DB)
	mk := func() string {
		o, err := repo.Create(context.Background(), KioskCounterOrder{Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}})
		if err != nil {
			t.Fatal(err)
		}
		return o.DisplayNo
	}
	if got := mk(); got != "C-200" {
		t.Fatalf("till 3 first counter order = %q, want C-200", got)
	}
	if got := mk(); got != "C-201" {
		t.Fatalf("got %q, want C-201", got)
	}
	for i := 0; i < 98; i++ { // fill the rest of the block: 202..299
		mk()
	}
	// Block full and every number still held: falls back above the block,
	// never fails the order.
	if got := mk(); got != "C-300" {
		t.Fatalf("saturated block got %q, want C-300", got)
	}
}

// A shop that never taps "collected" (most retail) leaves every sale open by
// the Order status rule. The order number must still go round the block, not
// stick on one number once the block is full (review F1).
func TestNextDisplayNo_NeverCollectedSalesStillGoRound(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, SaleDisplayNoSchemeKey, DisplayNoSchemeLifetimeNoReset)
	setRestoreSetting(t, dbx, "sync.receipt_prefix", "1-")
	base := time.Now().Add(-time.Hour)
	for i := 1; i <= 150; i++ {
		got := nextOrder(t, dbx)
		want := strconv.Itoa((i-1)%99 + 1)
		if got != want {
			t.Fatalf("sale %d: order number %q, want %s", i, got, want)
		}
		openOrder(t, dbx, "s"+strconv.Itoa(i), got, base.Add(time.Duration(i)*time.Second))
	}
}

// trading_period_reset starts again at the block's first number after a day
// close even when yesterday's sales were never marked collected (review F1).
func TestNextDisplayNo_ResetAfterCloseIgnoresLastPeriodsUncollectedSales(t *testing.T) {
	dbx := newPOSLifecycleTestDB(t)
	setRestoreSetting(t, dbx, "sync.receipt_prefix", "1-")
	base := time.Now().Add(-3 * time.Hour)
	for i := 1; i <= 60; i++ {
		openOrder(t, dbx, "y"+strconv.Itoa(i), nextOrder(t, dbx), base.Add(time.Duration(i)*time.Second))
	}
	if _, err := dbx.d.DB.Exec(`INSERT INTO report_archive (id, kind, period, content_json, created_at)
VALUES ('eod-1', 'eod', 'p1', '{}', ?)`, time.Now().UTC().Add(-time.Hour).Format(archiveTimestampFmt)); err != nil {
		t.Fatal(err)
	}
	if got := nextOrder(t, dbx); got != "1" {
		t.Fatalf("first order after the close = %q, want 1", got)
	}
	// An order of the new period that is still open does hold its number.
	openOrder(t, dbx, "t1", "2", time.Now())
	if got := nextOrder(t, dbx); got != "3" {
		t.Fatalf("got %q, want 3 (2 is open in this period)", got)
	}
}

// A full counter-order block goes round inside the till's own block: it must
// never spill into the next till's numbers, where it would collide on the
// main till (ut-docs#2703; review F2).
func TestCounterOrderDisplayNo_FullBlockStaysInItsOwnBlock(t *testing.T) {
	d := openKioskCounterOrdersDB(t, "counter_full.db")
	mustExec(t, d, `INSERT INTO settings (key, value) VALUES ('sync.receipt_prefix', '1-')`)
	repo := NewKioskCounterOrdersRepo(d.DB)
	mk := func() string {
		o, err := repo.Create(context.Background(), KioskCounterOrder{Status: KioskCounterOrderStatusHeld, Lines: []KioskCounterOrderLine{{Name: "Tea", Qty: 1}}})
		if err != nil {
			t.Fatal(err)
		}
		return o.DisplayNo
	}
	base := time.Now().UTC().Add(-time.Hour)
	for i := 1; i <= 99; i++ {
		if got := mk(); got != "C-"+strconv.Itoa(i) {
			t.Fatalf("order %d = %q", i, got)
		}
		// created_at has one-second resolution; make the order of issue explicit.
		if _, err := d.DB.Exec(`UPDATE kiosk_counter_orders SET created_at = ? WHERE display_no = ?`,
			base.Add(time.Duration(i)*time.Second).Format(time.RFC3339), "C-"+strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"C-1", "C-2", "C-3"} {
		if got := mk(); got != want {
			t.Fatalf("full block got %q, want %s (oldest number reused, never C-100)", got, want)
		}
	}
	var n int
	if err := d.DB.QueryRow(`SELECT COUNT(*) FROM kiosk_counter_orders`).Scan(&n); err != nil || n != 102 {
		t.Fatalf("rows = %d (%v), want 102: the reused numbers' old rows are kept", n, err)
	}
}
