package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// Promote with a sales copy (ut-docs#5074 M6, AC9).

func promote(t *testing.T, till *mirrorTill) string {
	t.Helper()
	mux := http.NewServeMux()
	registerSyncAPI(mux, till.d)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/promote", strings.NewReader("confirm=PROMOTE"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("promote %s: %d %s", till.name, rec.Code, rec.Body)
	}
	return rec.Body.String()
}

func stockOf(t *testing.T, till *mirrorTill) float64 {
	t.Helper()
	var q float64
	_ = till.d.Db.QueryRow(`SELECT COALESCE(SUM(quantity), 0) FROM inventory`).Scan(&q)
	return q
}

// M is gone; B is promoted. B's sales gain every copied sale it lacked,
// under each sale's origin, verbatim; stock and vouchers do not move; a
// void the copy knows reaches B's own row; B's next Z starts at M's last
// close; adopting again adds nothing.
func TestSalesMirrorPromote_AdoptsTheCopyWithoutMovingStock(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	shop := newMirrorShop(t, "A", "B")
	shop.enable()
	a, b := shop.tills["A"], shop.tills["B"]
	ctx := t.Context()
	// Before the copy: a sale of M's that B inherited as completed (a join
	// snapshot), voided on M later.
	inherited := shop.sell(shop.main, 999)
	var rcpt, created, local string
	_ = shop.main.d.Db.QueryRow(`SELECT receipt_no, created_at, local_date FROM sales WHERE id = ?`, inherited).Scan(&rcpt, &created, &local)
	if _, err := b.d.Db.Exec(`INSERT INTO sales (id, receipt_no, status, subtotal, total, local_date, created_at, completed_at) VALUES (?, ?, 'completed', 999, 999, ?, ?, ?)`,
		inherited, rcpt, local, created, created); err != nil {
		t.Fatal(err)
	}
	if err := data.NewPOSRepo(shop.main.d.Db).UpdateSaleStatus(ctx, nil, inherited, "completed", "voided"); err != nil {
		t.Fatal(err)
	}
	var fromA, fromM []string
	for range 4 {
		fromA = append(fromA, shop.sell(a, 250))
	}
	shop.push(a)
	for range 3 {
		fromM = append(fromM, shop.sell(shop.main, 300))
	}
	// M closes the day: B's next Z must start after it.
	if _, err := data.NewPOSRepo(shop.main.d.Db).ArchiveReport(ctx, "eod", "m-close", []byte(`{}`), "", "", time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ownB := shop.sell(b, 77)
	shop.pull(b)
	shop.pull(b)
	stockBefore := stockOf(t, b)
	salesBefore := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM sales`)

	body := promote(t, b) // M is "dead": nothing here calls it
	// The adoption logged nothing (the change log stays quiet).
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM sales_mirror_log`); n != 0 {
		t.Fatalf("adoption logged %d rows", n)
	}
	// ownB (the harness dates a till's sales two hours back, before M's
	// close) never reached M: it is in no Z, and the panel says so.
	if !strings.Contains(body, "record: 7.") || !strings.Contains(body, "marked voided as in the copy: 1.") ||
		!strings.Contains(body, "may be in no Z report: 1.") {
		t.Fatalf("promote panel does not say what was adopted:\n%s", body)
	}
	for _, id := range fromA {
		var till, status string
		if err := b.d.Db.QueryRow(`SELECT till_id, status FROM sales WHERE id = ?`, id).Scan(&till, &status); err != nil || till != a.tillID || status != "completed" {
			t.Fatalf("A's sale %s on B: till %q status %q err %v", id, till, status, err)
		}
		if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM sale_lines WHERE sale_id = ?`, id); n != 1 {
			t.Fatalf("A's sale %s has %d lines on B", id, n)
		}
		if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM payments WHERE sale_id = ?`, id); n != 1 {
			t.Fatalf("A's sale %s has %d payments on B", id, n)
		}
	}
	for _, id := range fromM {
		var till string
		_ = b.d.Db.QueryRow(`SELECT till_id FROM sales WHERE id = ?`, id).Scan(&till)
		if till != shop.mainID {
			t.Fatalf("M's sale %s adopted under %q, want M's id", id, till)
		}
	}
	var st string
	_ = b.d.Db.QueryRow(`SELECT status FROM sales WHERE id = ?`, inherited).Scan(&st)
	if st != "voided" {
		t.Fatalf("the void did not reach B's inherited row: %q", st)
	}
	if got := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM sales`); got != salesBefore+7 {
		t.Fatalf("B's sales %d -> %d, want +7", salesBefore, got)
	}
	if stockOf(t, b) != stockBefore {
		t.Fatal("adoption moved stock")
	}
	if harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM sales WHERE id = ? AND till_id = ''`, ownB) != 1 {
		t.Fatal("B's own sale changed")
	}
	floor, _, _ := b.d.Settings.Get(ctx, data.EODWindowFloorKey)
	if floor == "" {
		t.Fatal("no Z window floor after promote")
	}
	// B's next Z: after M's close only (the copied sales before it were in
	// M's Z), so here nothing but what was sold after the close.
	rep, _, err := generateEOD(ctx, b.d, "system", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if rep.From == "" || rep.From < floor[:10] {
		t.Fatalf("Z from %q, want at or after the floor %s", rep.From, floor)
	}
	// Nothing M's Z already counted is counted again: the 3 M sales and 4 A
	// sales (all before M's close) are out of B's next Z, and so is ownB
	// (before the close; disclosed above, never double counted).
	if rep.Gross != 0 {
		t.Fatalf("B's next Z gross = %d, want 0 (everything is before M's close)", rep.Gross)
	}

	if v, _, _ := b.d.Settings.Get(ctx, "sync.sales_mirror_off"); v == "1" {
		t.Fatal("the kill switch was left on after adoption")
	}
	// Idempotent.
	again, err := data.NewSalesMirrorRepo(b.d.Db).AdoptCopy(context.Background())
	if err != nil || again.Adopted != 0 || again.Failed != 0 {
		t.Fatalf("second adoption: %+v err %v", again, err)
	}
}

// A copied sale whose receipt number this till already uses is not
// adopted: counted, said, and left in the copy — never half-written.
func TestSalesMirrorPromote_ReceiptClashStaysInTheCopy(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	clash := shop.sell(shop.main, 100)
	shop.pull(b)
	shop.pull(b)
	var rcpt string
	_ = shop.main.d.Db.QueryRow(`SELECT receipt_no FROM sales WHERE id = ?`, clash).Scan(&rcpt)
	if _, err := b.d.Db.Exec(`INSERT INTO sales (id, receipt_no, status, subtotal, total, local_date) VALUES ('other', ?, 'completed', 1, 1, '2026-10-26')`, rcpt); err != nil {
		t.Fatal(err)
	}
	body := promote(t, b)
	if !strings.Contains(body, "could not be added and stay in the copy: 1") {
		t.Fatalf("promote panel:\n%s", body)
	}
	if harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM sales WHERE id = ?`, clash) != 0 ||
		harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM sale_lines WHERE sale_id = ?`, clash) != 0 {
		t.Fatal("a refused sale was half-written")
	}
	if harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales WHERE id = ?`, clash) != 1 {
		t.Fatal("the refused sale left the copy")
	}
}

// Without a copy (the copy never on), promote is exactly as before: no
// adoption line, no floor.
func TestSalesMirrorPromote_NoCopyNoChange(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	shop := newMirrorShop(t, "B")
	b := shop.tills["B"]
	body := promote(t, b)
	if strings.Contains(body, "copied sales") {
		t.Fatalf("adoption line without a copy:\n%s", body)
	}
	if v, ok, _ := b.d.Settings.Get(t.Context(), data.EODWindowFloorKey); ok && v != "" {
		t.Fatal("a floor without a copy")
	}
}

// A copied sale whose reference this till lacks (a payment method it never
// had) is refused whole and stays in the copy; an adoption cut short
// resumes on the maintenance tick.
func TestSalesMirrorPromote_MissingReferenceAndResume(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	ctx := t.Context()
	for range 3 {
		shop.sell(shop.main, 100)
	}
	shop.pull(b)
	shop.pull(b)
	if _, err := b.d.Db.Exec(`UPDATE mirror_payments SET method_id = 'no-such-method' WHERE sale_id = (SELECT id FROM mirror_sales ORDER BY created_at LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	body := promote(t, b)
	if !strings.Contains(body, "record: 2.") || !strings.Contains(body, "stay in the copy: 1") {
		t.Fatalf("promote panel:\n%s", body)
	}
	// Simulate a cut-short adoption: pending set, rows missing.
	if _, err := b.d.Db.Exec(`DELETE FROM payments WHERE sale_id IN (SELECT id FROM mirror_sales)`); err != nil {
		t.Fatal(err)
	}
	_, _ = b.d.Db.Exec(`DELETE FROM sale_lines WHERE sale_id IN (SELECT id FROM mirror_sales)`)
	if _, err := b.d.Db.Exec(`DELETE FROM sales WHERE id IN (SELECT id FROM mirror_sales)`); err != nil {
		t.Fatal(err)
	}
	if _, err := b.d.Db.Exec(`UPDATE mirror_payments SET method_id = 'cash'`); err != nil {
		t.Fatal(err)
	}
	_ = b.d.Settings.Set(ctx, salesMirrorAdoptPendingKey, "1")
	salesMirrorMaintenanceTick(ctx, b.d, time.Now(), nil)
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM sales WHERE id IN (SELECT id FROM mirror_sales)`); n != 3 {
		t.Fatalf("resumed adoption: %d of 3 sales", n)
	}
	if v, _, _ := b.d.Settings.Get(ctx, salesMirrorAdoptPendingKey); v != "" {
		t.Fatal("pending mark left after a finished adoption")
	}
}
