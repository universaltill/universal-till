package data

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// SalesMirrorRepo (ut-docs#5074): apply, idempotency, bounds, the change
// log's acknowledgements and pruning, against the real migrated schema.

func openMirrorRepoDB(t *testing.T) *sql.DB {
	t.Helper()
	d, err := db.Open(testsupport.MigratedDBFile(t, "mirror_repo.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d.DB
}

func strp(s string) *string { return &s }

func testMirrorSale(id, origin string, total int64) *MirrorSale {
	return &MirrorSale{
		ID: id, OriginTillID: origin, OriginTillName: "Till " + origin,
		ReceiptNo: "T2-" + id, Status: "completed", SaleType: "sale", TenderType: "cash",
		Currency: "GBP", Subtotal: total, Total: total, CreatedAt: "2026-10-26T10:00:00Z",
		LocalDate: "2026-10-26", CustomerID: strp("cust-1"),
		Lines: []MirrorSaleLine{{
			ID: id + "-l1", LineNo: 1, NameSnapshot: "Coffee", Quantity: 1, UnitPrice: total,
			TotalAfterTax: total, Modifiers: []MirrorLineModifier{{ID: id + "-m1", GroupNameSnapshot: "Milk", OptionNameSnapshot: "Oat", PriceDeltaMinor: 30}},
		}},
		Payments:  []MirrorPayment{{ID: id + "-p1", MethodID: "card", Amount: total, Currency: "GBP", PaidAt: "2026-10-26T10:00:00Z", MaskedPAN: strp("VISA **** 4242")}},
		Charges:   []MirrorSaleCharge{{Seq: 1, Key: "service", AmountMinor: 10, Base: "net_lines"}},
		Discounts: []MirrorSaleDiscount{{ID: id + "-d1", Type: "fixed", Value: 5, Amount: 5}},
		Links:     []MirrorSaleLink{{ID: id + "-k1", OriginalSaleID: "orig-1"}},
	}
}

func saleRec(seq int64, s *MirrorSale) MirrorRecord {
	return MirrorRecord{Seq: seq, Op: MirrorOpSale, Sale: s}
}

// startCopy puts the till at (epoch, cursor) as a finished state re-sync
// would.
func startCopy(t *testing.T, r *SalesMirrorRepo, epoch string, cursor int64) {
	t.Helper()
	if _, err := r.ApplyStatePage(context.Background(), "", MirrorStatePage{Epoch: epoch, ResumeSeq: cursor}, MirrorApplyOpts{Now: time.Now()}); err != nil {
		t.Fatalf("start copy: %v", err)
	}
}

func mirrorCount(t *testing.T, sqlDB *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSalesMirrorRepo_ApplyFeedPage_StoresEveryChild(t *testing.T) {
	sqlDB := openMirrorRepoDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	startCopy(t, r, "e1", 10)
	res, err := r.ApplyFeedPage(ctx, MirrorFeedPage{Epoch: "e1", MainTillID: "main", FromSeq: 10, ToSeq: 12, Horizon: 3,
		Records: []MirrorRecord{saleRec(11, testMirrorSale("s1", "tillA", 1000)), saleRec(12, testMirrorSale("s2", "main", 500))},
	}, MirrorApplyOpts{SelfTillID: "tillB", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 2 || res.Skipped != 0 || res.Rejected != 0 {
		t.Fatalf("result = %+v", res)
	}
	for tb, want := range map[string]int{"mirror_sales": 2, "mirror_sale_lines": 2, "mirror_sale_line_modifiers": 2,
		"mirror_payments": 2, "mirror_sale_charges": 2, "mirror_sale_discounts": 2, "mirror_sale_links": 2} {
		if n := mirrorCount(t, sqlDB, tb); n != want {
			t.Errorf("%s = %d, want %d", tb, n, want)
		}
	}
	st, err := r.State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Epoch != "e1" || st.Cursor != 12 || st.MainTillID != "main" || st.Horizon != 3 || st.LastApplyAt == "" {
		t.Fatalf("state = %+v", st)
	}
	// The copy never reaches sales.
	if n := mirrorCount(t, sqlDB, "sales"); n != 0 {
		t.Fatalf("sales has %d rows after a copy apply", n)
	}
}

// Duplicates, reordering and gaps change nothing (AC5): only a page that
// continues the cursor in the same epoch is applied.
func TestSalesMirrorRepo_ApplyFeedPage_RefusesDuplicateAndGap(t *testing.T) {
	sqlDB := openMirrorRepoDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	o := MirrorApplyOpts{SelfTillID: "tillB", Now: time.Now()}
	startCopy(t, r, "e1", 0)
	p1 := MirrorFeedPage{Epoch: "e1", FromSeq: 0, ToSeq: 1, Records: []MirrorRecord{saleRec(1, testMirrorSale("s1", "tillA", 100))}}
	if _, err := r.ApplyFeedPage(ctx, p1, o); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]MirrorFeedPage{
		"duplicate":   p1,
		"gap":         {Epoch: "e1", FromSeq: 5, ToSeq: 6, Records: []MirrorRecord{saleRec(6, testMirrorSale("s6", "tillA", 1))}},
		"other epoch": {Epoch: "e2", FromSeq: 1, ToSeq: 2, Records: []MirrorRecord{saleRec(2, testMirrorSale("s2", "tillA", 1))}},
	} {
		if _, err := r.ApplyFeedPage(ctx, p, o); !errors.Is(err, ErrMirrorCursorMoved) {
			t.Errorf("%s: err = %v, want ErrMirrorCursorMoved", name, err)
		}
	}
	if n := mirrorCount(t, sqlDB, "mirror_sales"); n != 1 {
		t.Fatalf("mirror_sales = %d, want 1", n)
	}
	if st, _ := r.State(ctx); st.Cursor != 1 {
		t.Fatalf("cursor = %d, want 1", st.Cursor)
	}
}

// A newer state of the same sale (a void) replaces the copy and its
// children wholesale; an older one is ignored.
func TestSalesMirrorRepo_NewerStateWinsOlderIgnored(t *testing.T) {
	sqlDB := openMirrorRepoDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	o := MirrorApplyOpts{SelfTillID: "tillB", Now: time.Now()}
	startCopy(t, r, "e1", 0)
	s := testMirrorSale("s1", "tillA", 100)
	if _, err := r.ApplyFeedPage(ctx, MirrorFeedPage{Epoch: "e1", FromSeq: 0, ToSeq: 5, Records: []MirrorRecord{saleRec(5, s)}}, o); err != nil {
		t.Fatal(err)
	}
	voided := testMirrorSale("s1", "tillA", 100)
	voided.Status = "voided"
	voided.VoidedAt = strp("2026-10-26T11:00:00Z")
	voided.Lines = voided.Lines[:0]
	if _, err := r.ApplyFeedPage(ctx, MirrorFeedPage{Epoch: "e1", FromSeq: 5, ToSeq: 6, Records: []MirrorRecord{saleRec(6, voided)}}, o); err != nil {
		t.Fatal(err)
	}
	var status string
	_ = sqlDB.QueryRow(`SELECT status FROM mirror_sales WHERE id = 's1'`).Scan(&status)
	if status != "voided" || mirrorCount(t, sqlDB, "mirror_sale_lines") != 0 || mirrorCount(t, sqlDB, "mirror_sale_line_modifiers") != 0 {
		t.Fatalf("after newer state: status=%s lines=%d", status, mirrorCount(t, sqlDB, "mirror_sale_lines"))
	}
	// An older state (a state re-sync captured at seq 4 < stored 6, same
	// epoch) is ignored.
	res, err := r.ApplyStatePage(ctx, "", MirrorStatePage{Epoch: "e1", ResumeSeq: 4, NextCursor: "c", More: true, Records: []MirrorRecord{saleRec(4, s)}}, o)
	if err != nil {
		t.Fatal(err)
	}
	_ = sqlDB.QueryRow(`SELECT status FROM mirror_sales WHERE id = 's1'`).Scan(&status)
	if res.Skipped != 1 || status != "voided" {
		t.Fatalf("older record: res=%+v status=%s", res, status)
	}
	// A record whose seq is outside its page's range is rejected, never
	// stored with a seq that would shadow a later real state (finding 4).
	inflated := testMirrorSale("s9", "tillA", 1)
	res, err = r.ApplyFeedPage(ctx, MirrorFeedPage{Epoch: "e1", FromSeq: 6, ToSeq: 7, Records: []MirrorRecord{saleRec(99, inflated)}}, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected != 1 || mirrorCount(t, sqlDB, "mirror_sales") != 1 {
		t.Fatalf("out-of-range seq: res=%+v rows=%d", res, mirrorCount(t, sqlDB, "mirror_sales"))
	}
}

// Own-origin, gone, out-of-window and malformed records are skipped, and
// the cursor still moves past them (never wedges).
func TestSalesMirrorRepo_SkipsOwnGoneOldAndMalformed(t *testing.T) {
	sqlDB := openMirrorRepoDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	startCopy(t, r, "e1", 0)
	old := testMirrorSale("old", "tillA", 1)
	old.LocalDate = "2025-01-01"
	bad := testMirrorSale("bad", "tillA", 1)
	bad.Status = "held"
	res, err := r.ApplyFeedPage(ctx, MirrorFeedPage{Epoch: "e1", FromSeq: 0, ToSeq: 5, Records: []MirrorRecord{
		saleRec(1, testMirrorSale("mine", "tillB", 1)),
		{Seq: 2, Op: MirrorOpSale, ID: "x", Gone: true},
		saleRec(3, old),
		saleRec(4, bad),
		{Seq: 5, Op: "bogus"},
	}}, MirrorApplyOpts{SelfTillID: "tillB", WindowStart: "2025-09-21", Now: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied != 0 || res.Skipped != 3 || res.Rejected != 2 {
		t.Fatalf("result = %+v, want 0 applied, 3 skipped, 2 rejected", res)
	}
	st, _ := r.State(ctx)
	if st.Cursor != 5 || st.Rejected != 2 || mirrorCount(t, sqlDB, "mirror_sales") != 0 {
		t.Fatalf("state = %+v rows=%d", st, mirrorCount(t, sqlDB, "mirror_sales"))
	}
}

// A state re-sync records progress per page (crash-safe) and only moves the
// cursor on its last page.
func TestSalesMirrorRepo_StateResyncIsResumable(t *testing.T) {
	sqlDB := openMirrorRepoDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	o := MirrorApplyOpts{SelfTillID: "tillB", Now: time.Now()}
	if _, err := r.ApplyStatePage(ctx, "", MirrorStatePage{Epoch: "e9", ResumeSeq: 40, NextCursor: "c1", More: true,
		Records: []MirrorRecord{saleRec(40, testMirrorSale("s1", "tillA", 1))}}, o); err != nil {
		t.Fatal(err)
	}
	st, _ := r.State(ctx)
	if st.Epoch != "" || st.ResyncEpoch != "e9" || st.ResyncResumeSeq != 40 || st.ResyncCursor != "c1" {
		t.Fatalf("mid re-sync state = %+v", st)
	}
	// A page that does not continue the re-sync is refused.
	if _, err := r.ApplyStatePage(ctx, "zz", MirrorStatePage{Epoch: "e9", ResumeSeq: 40}, o); !errors.Is(err, ErrMirrorCursorMoved) {
		t.Fatalf("stray page err = %v", err)
	}
	if _, err := r.ApplyStatePage(ctx, "c1", MirrorStatePage{Epoch: "e9", ResumeSeq: 40,
		Records: []MirrorRecord{saleRec(40, testMirrorSale("s2", "main", 1))}}, o); err != nil {
		t.Fatal(err)
	}
	st, _ = r.State(ctx)
	if st.Epoch != "e9" || st.Cursor != 40 || st.ResyncEpoch != "" || mirrorCount(t, sqlDB, "mirror_sales") != 2 {
		t.Fatalf("after re-sync state = %+v", st)
	}
}

func TestSalesMirrorRepo_CloseUpsertNewerWins(t *testing.T) {
	sqlDB := openMirrorRepoDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	o := MirrorApplyOpts{SelfTillID: "tillB", Now: time.Now()}
	startCopy(t, r, "e1", 0)
	z := int64(7)
	c := &MirrorClose{ID: "ra1", OriginTillID: "main", Kind: "eod", Period: "2026-10-26T18:00:00Z", ZNumber: &z, ContentJSON: `{"v":1}`, CreatedAt: "2026-10-26 18:00:00"}
	if _, err := r.ApplyFeedPage(ctx, MirrorFeedPage{Epoch: "e1", FromSeq: 0, ToSeq: 3, Records: []MirrorRecord{{Seq: 3, Op: MirrorOpClose, Close: c}}}, o); err != nil {
		t.Fatal(err)
	}
	older := *c
	older.ContentJSON = `{"v":0}`
	res, err := r.ApplyStatePage(ctx, "", MirrorStatePage{Epoch: "e1", ResumeSeq: 2, NextCursor: "c", More: true, Records: []MirrorRecord{{Seq: 2, Op: MirrorOpClose, Close: &older}}}, o)
	if err != nil {
		t.Fatal(err)
	}
	var content string
	_ = sqlDB.QueryRow(`SELECT content_json FROM mirror_closes`).Scan(&content)
	if res.Skipped != 1 || content != `{"v":1}` || mirrorCount(t, sqlDB, "report_archive") != 0 {
		t.Fatalf("res=%+v content=%s archive=%d", res, content, mirrorCount(t, sqlDB, "report_archive"))
	}
}

func TestSalesMirrorRepo_PruneCopy(t *testing.T) {
	sqlDB := openMirrorRepoDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	o := MirrorApplyOpts{SelfTillID: "tillB", Now: time.Now()}
	startCopy(t, r, "e1", 0)
	var recs []MirrorRecord
	for i, d := range []string{"2025-01-01", "2025-06-01", "2026-10-01"} {
		s := testMirrorSale(fmt.Sprintf("s%d", i), "tillA", 1)
		s.LocalDate = d
		recs = append(recs, saleRec(int64(i+1), s))
	}
	if _, err := r.ApplyFeedPage(ctx, MirrorFeedPage{Epoch: "e1", FromSeq: 0, ToSeq: 3, Records: recs}, o); err != nil {
		t.Fatal(err)
	}
	stats, err := r.CopyStats(ctx)
	if err != nil || stats.Sales != 3 || stats.OldestLocalDate != "2025-01-01" || stats.BytesEstimate() <= 0 {
		t.Fatalf("stats = %+v err=%v", stats, err)
	}
	n, err := r.PruneCopyBefore(ctx, "2025-09-01", "")
	if err != nil || n != 2 {
		t.Fatalf("prune: n=%d err=%v", n, err)
	}
	if mirrorCount(t, sqlDB, "mirror_sale_lines") != 1 || mirrorCount(t, sqlDB, "mirror_payments") != 1 {
		t.Fatal("prune did not cascade to children")
	}

}

// A record the schema would refuse (duplicate child ids) is rejected before
// any write and the page still applies (M1 review finding 1: never wedge).
func TestSalesMirrorRepo_DuplicateChildIDsRejectedNotWedged(t *testing.T) {
	sqlDB := openMirrorRepoDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	startCopy(t, r, "e1", 0)
	dupLine := testMirrorSale("dup", "tillA", 1)
	dupLine.Lines = append(dupLine.Lines, dupLine.Lines[0])
	dupCharge := testMirrorSale("dupc", "tillA", 1)
	dupCharge.Charges = append(dupCharge.Charges, dupCharge.Charges[0])
	// A child id that another copied sale already uses: only the schema can
	// tell, so the savepoint rejects it.
	clash := testMirrorSale("clash", "tillA", 1)
	clash.Payments[0].ID = "ok-p1"
	res, err := r.ApplyFeedPage(ctx, MirrorFeedPage{Epoch: "e1", FromSeq: 0, ToSeq: 4, Records: []MirrorRecord{
		saleRec(1, testMirrorSale("ok", "tillA", 1)), saleRec(2, dupLine), saleRec(3, dupCharge), saleRec(4, clash),
	}}, MirrorApplyOpts{SelfTillID: "tillB", Now: time.Now()})
	if err != nil {
		t.Fatalf("page failed: %v", err)
	}
	if res.Applied != 1 || res.Rejected != 3 {
		t.Fatalf("result = %+v, want 1 applied, 3 rejected", res)
	}
	if st, _ := r.State(ctx); st.Cursor != 4 {
		t.Fatalf("cursor = %d, want 4", st.Cursor)
	}
	if n := mirrorCount(t, sqlDB, "mirror_sales"); n != 1 {
		t.Fatalf("mirror_sales = %d, want 1 (the rejected record left nothing behind)", n)
	}
}

// One feed page of 100 sales (2.5 lines, a modifier, a payment each) applied
// in one transaction — the write-lock hold an additional till pays per page
// (design §11: p95 ≤ 50 ms on a Pi 5; reported in the review record).
func BenchmarkSalesMirrorApplyFeedPage100(b *testing.B) {
	d, err := db.Open(testsupport.MigratedDBFile(b, "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	defer d.Close()
	r := NewSalesMirrorRepo(d.DB)
	ctx := context.Background()
	if _, err := r.ApplyStatePage(ctx, "", MirrorStatePage{Epoch: "e", ResumeSeq: 0}, MirrorApplyOpts{Now: time.Now()}); err != nil {
		b.Fatal(err)
	}
	cursor := int64(0)
	b.ResetTimer()
	for i := 0; b.Loop(); i++ {
		recs := make([]MirrorRecord, 100)
		for k := range recs {
			s := testMirrorSale(fmt.Sprintf("b%d-%d", i, k), "tillA", 1000)
			s.Lines = append(s.Lines, MirrorSaleLine{ID: s.ID + "-l2", LineNo: 2, NameSnapshot: "Tea", Quantity: 2, UnitPrice: 300})
			recs[k] = saleRec(cursor+int64(k)+1, s)
		}
		if _, err := r.ApplyFeedPage(ctx, MirrorFeedPage{Epoch: "e", FromSeq: cursor, ToSeq: cursor + 100, Records: recs}, MirrorApplyOpts{SelfTillID: "tillB", Now: time.Now()}); err != nil {
			b.Fatal(err)
		}
		cursor += 100
	}
}

// The previous main till's own sales are adopted under their register (the
// key its cloud sales aggregates used), another till's under that till's id
// (M6 review finding 1).
func TestSalesMirrorRepo_AdoptKeepsTheAggregateTillKey(t *testing.T) {
	sqlDB := openMirrorRepoDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	if _, err := sqlDB.Exec(`INSERT INTO registers (id, name, is_active) VALUES ('reg-m', 'Front', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqlDB.Exec(`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('card', 'Card', 'card', 1)`); err != nil {
		t.Fatal(err)
	}
	startCopy(t, r, "e1", 0)
	m := testMirrorSale("m1", "main-id", 100)
	m.RegisterID = strp("reg-m")
	m.Lines, m.CustomerID = nil, nil
	other := testMirrorSale("a1", "till-a", 100)
	other.Lines, other.CustomerID = nil, nil
	if _, err := r.ApplyFeedPage(ctx, MirrorFeedPage{Epoch: "e1", MainTillID: "main-id", FromSeq: 0, ToSeq: 2,
		Records: []MirrorRecord{saleRec(1, m), saleRec(2, other)}}, MirrorApplyOpts{SelfTillID: "till-b", Now: time.Now()}); err != nil {
		t.Fatal(err)
	}
	res, err := r.AdoptCopy(ctx)
	if err != nil || res.Adopted != 2 {
		t.Fatalf("adopt: %+v err %v", res, err)
	}
	for id, want := range map[string]string{"m1": "reg-m", "a1": "till-a"} {
		var got string
		_ = sqlDB.QueryRow(`SELECT till_id FROM sales WHERE id = ?`, id).Scan(&got)
		if got != want {
			t.Errorf("%s adopted under %q, want %q", id, got, want)
		}
	}
}
