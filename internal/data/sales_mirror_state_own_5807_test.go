package data

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// ut-docs#5807: a state page may carry the receiver's own sales as bare
// headers (the feed's ut-docs#5777 shape). The receiver applies them with the
// same path as a feed page. A 1.0 receiver runs the same validity check and
// own-origin skip, so Rejected == 0 here is also the old-receiver proof.
func TestApplyStatePage_OwnHeaderKeepsArrivalNotCopy_5807(t *testing.T) {
	sqlDB := openMirrorRepoDB(t)
	r := NewSalesMirrorRepo(sqlDB)
	ctx := context.Background()
	now := time.Now()
	day := now.Format("2006-01-02")
	at1 := now.Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	at2 := now.Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	o := MirrorApplyOpts{SelfTillID: "till-self", WindowStart: now.AddDate(0, 0, -30).Format("2006-01-02"), Now: now}

	header := func(at string) *MirrorSale {
		return &MirrorSale{ID: "s-own", OriginTillID: "till-self", OriginTillName: "Till self", ReceiptNo: "T2-own",
			Status: "completed", SaleType: "sale", Currency: "GBP", CreatedAt: at, CompletedAt: &at, LocalDate: day,
			Lines: []MirrorSaleLine{}, Payments: []MirrorPayment{}, Charges: []MirrorSaleCharge{}, Discounts: []MirrorSaleDiscount{}, Links: []MirrorSaleLink{}}
	}
	other := testMirrorSale("s-other", "tillA", 400)
	other.LocalDate = day
	res, err := r.ApplyStatePage(ctx, "", MirrorStatePage{Epoch: "e5", ResumeSeq: 9, More: false,
		Records: []MirrorRecord{saleRec(9, header(at1)), saleRec(9, other)}}, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected != 0 || res.Skipped < 1 {
		t.Fatalf("result = %+v, want Rejected 0 and the own header skipped", res)
	}
	if n := mirrorCount(t, sqlDB, "mirror_sales"); n != 1 {
		t.Fatalf("mirror_sales = %d, want only the other till's sale", n)
	}
	var stored int
	if err := sqlDB.QueryRow(`SELECT COUNT(*) FROM mirror_sales WHERE id = 's-own'`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("own header stored in the copy: %d (%v)", stored, err)
	}
	var got string
	if err := sqlDB.QueryRow(`SELECT completed_at FROM mirror_sale_arrivals WHERE id = 's-own'`).Scan(&got); err != nil || got != at1 {
		t.Fatalf("arrival = %q err %v, want %q", got, err, at1)
	}
	// A second re-sync (a new epoch, first page) refreshes it: last apply wins.
	if err := r.StartResync(ctx, "e6"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ApplyStatePage(ctx, "", MirrorStatePage{Epoch: "e6", ResumeSeq: 12, Records: []MirrorRecord{saleRec(12, header(at2))}}, o); err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.QueryRow(`SELECT completed_at FROM mirror_sale_arrivals WHERE id = 's-own'`).Scan(&got); err != nil || got != at2 {
		t.Fatalf("arrival after re-sync = %q err %v, want %q", got, err, at2)
	}
}

func TestLoadMirrorSaleHeaders_NoChildren_5807(t *testing.T) {
	d, err := db.Open(testsupport.MigratedDBFile(t, "mirror_headers_5807.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ctx := context.Background()
	day := time.Now().Format("2006-01-02")
	for _, s := range []string{
		`INSERT INTO tills (id, name) VALUES ('till-2', 'Front')`,
		`INSERT OR IGNORE INTO payment_methods (id, name, type, is_active) VALUES ('card', 'Card', 'card', 1)`,
		`INSERT INTO sales (id, receipt_no, status, sale_type, currency, subtotal, total, created_at, completed_at, till_id, local_date)
		 VALUES ('s1', 'T2-1', 'completed', 'sale', 'GBP', 1000, 1000, '` + day + `T10:00:00Z', '` + day + `T10:00:05Z', 'till-2', '` + day + `')`,
		`INSERT INTO sales (id, receipt_no, status, subtotal, total, local_date) VALUES ('s2', '2', 'completed', 5, 5, '` + day + `')`,
		`INSERT INTO sales (id, receipt_no, status, subtotal, total, local_date) VALUES ('held', '3', 'active', 5, 5, '` + day + `')`,
		`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('itm-cut', 'CUT', 'Cut', 1000, 1)`,
		`INSERT INTO sale_lines (id, sale_id, line_no, item_id, name_snapshot, quantity, unit_price, tax_rate_bp, tax_amount, total_before_tax, total_after_tax)
		 VALUES ('l1', 's1', 1, 'itm-cut', 'Cut', 1, 1000, 0, 0, 1000, 1000)`,
		`INSERT INTO payments (id, sale_id, method_id, amount, currency, local_date) VALUES ('p1', 's1', 'card', 1000, 'GBP', '` + day + `')`,
	} {
		if _, err := d.DB.Exec(s); err != nil {
			t.Fatalf("seed %q: %v", s, err)
		}
	}
	r := NewSalesMirrorRepo(d.DB)
	src := MirrorSource{MainTillID: "main-1", MainTillName: "Back counter"}
	heads, err := r.LoadMirrorSaleHeaders(ctx, []string{"s1", "s2", "held", "missing"}, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(heads) != 2 || heads["held"] != nil || heads["missing"] != nil {
		t.Fatalf("headers = %v, want s1 and s2 only", heads)
	}
	full, err := r.LoadMirrorSales(ctx, []string{"s1"}, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(full["s1"].Lines) != 1 || len(full["s1"].Payments) != 1 {
		t.Fatal("fixture: the full load must carry children")
	}
	h := heads["s1"]
	if h.Lines == nil || h.Payments == nil || h.Charges == nil || h.Discounts == nil || h.Links == nil ||
		len(h.Lines)+len(h.Payments)+len(h.Charges)+len(h.Discounts)+len(h.Links) != 0 {
		t.Fatalf("children on a header: %+v", h)
	}
	want := *full["s1"]
	want.Lines, want.Payments, want.Charges, want.Discounts, want.Links = h.Lines, h.Payments, h.Charges, h.Discounts, h.Links
	if !reflect.DeepEqual(*h, want) {
		t.Fatalf("header differs from the full load's:\n%+v\n%+v", *h, want)
	}
	if heads["s2"].OriginTillID != "main-1" || heads["s2"].OriginTillName != "Back counter" {
		t.Fatalf("main till's sale origin = %q/%q", heads["s2"].OriginTillID, heads["s2"].OriginTillName)
	}
}
