package pages

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#5807: the state re-sync sends the caller its own sales as bare
// headers, everything else in full.

func TestSalesMirrorState_OwnSalesHeaderOnly_5807(t *testing.T) {
	shop := newMirrorShop(t, "A", "B")
	epoch := shop.enable()
	a, b := shop.tills["A"], shop.tills["B"]
	ctx := t.Context()
	shop.sell(shop.main, 100)
	shop.sell(b, 110)
	shop.sell(shop.main, 120)
	shop.sell(b, 130)
	shop.sell(a, 140)
	shop.push(b)
	shop.push(a)

	var order []string
	rows, err := shop.main.d.Db.Query(`SELECT id FROM sales WHERE status IN ('completed','voided') ORDER BY local_date, rowid`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		order = append(order, id)
	}
	rows.Close()
	if len(order) != 5 {
		t.Fatalf("main holds %d sales, want 5", len(order))
	}
	repo := data.NewSalesMirrorRepo(shop.main.d.Db)
	full, err := repo.LoadMirrorSales(ctx, order, salesMirrorSource(ctx, shop.main.d))
	if err != nil {
		t.Fatal(err)
	}

	var got []string
	ownSeen := 0
	cursor, resume := "", ""
	from := time.Now().AddDate(0, 0, -400).Format("2006-01-02")
	for i := 0; ; i++ {
		if i > 20 {
			t.Fatal("state re-sync did not end")
		}
		q := url.Values{"epoch": {epoch}, "from_date": {from}, "cursor": {cursor}, "resume_seq": {resume}, "limit": {"2"}}
		r := shop.get(b, "/api/sync/sales-mirror/state", q)
		if r.status != http.StatusOK {
			t.Fatalf("state page %d: %d %+v", i, r.status, r.err)
		}
		for _, rec := range r.state.Records {
			if rec.Sale == nil {
				continue
			}
			got = append(got, rec.Sale.ID)
			f := full[rec.Sale.ID]
			if rec.Sale.OriginTillID == b.tillID {
				ownSeen++
				if !reflect.DeepEqual(rec.Sale, ownSaleArrival(f)) {
					t.Fatalf("own record is not the header shape:\n%+v\n%+v", rec.Sale, ownSaleArrival(f))
				}
				var completed string
				if err := shop.main.d.Db.QueryRow(`SELECT completed_at FROM sales WHERE id = ?`, rec.Sale.ID).Scan(&completed); err != nil {
					t.Fatal(err)
				}
				s := rec.Sale
				if len(s.Lines) != 0 || len(s.Payments) != 0 || s.Total != 0 || s.Subtotal != 0 || s.TaxTotal != 0 ||
					s.CompletedAt == nil || *s.CompletedAt != completed {
					t.Fatalf("own record leaks or lacks the recording time: %+v", s)
				}
			} else if len(rec.Sale.Lines) == 0 || !reflect.DeepEqual(rec.Sale, f) {
				t.Fatalf("other record %s is not the full sale", rec.Sale.ID)
			}
		}
		if !r.state.More {
			break
		}
		cursor, resume = r.state.NextCursor, fmt.Sprint(r.state.ResumeSeq)
	}
	if ownSeen != 2 {
		t.Fatalf("own headers = %d, want 2", ownSeen)
	}
	if !reflect.DeepEqual(got, order) {
		t.Fatalf("page order %v, want storage order %v", got, order)
	}
}

func TestSalesMirrorResync_RebuildsOwnArrivals_5807(t *testing.T) {
	shop := newMirrorShop(t, "B")
	shop.enable()
	b := shop.tills["B"]
	ctx := t.Context()
	shop.pull(b)
	shop.pull(b)
	s1, s2, s3 := shop.sell(b, 100), shop.sell(b, 200), shop.sell(b, 300)
	shop.push(b)
	// S3 is older than the receiver's window on the main till.
	if _, err := shop.main.d.Db.Exec(`UPDATE sales SET local_date = ? WHERE id = ?`, time.Now().AddDate(0, 0, -4000).Format("2006-01-02"), s3); err != nil {
		t.Fatal(err)
	}
	shop.pull(b)
	shop.pull(b)
	completedOnMain := func(id string) string {
		var c string
		if err := shop.main.d.Db.QueryRow(`SELECT completed_at FROM sales WHERE id = ?`, id).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	arrival := func(id string) (string, int) {
		var c string
		n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sale_arrivals WHERE id = ?`, id)
		_ = b.d.Db.QueryRow(`SELECT completed_at FROM mirror_sale_arrivals WHERE id = ?`, id).Scan(&c)
		return c, n
	}
	if _, n := arrival(s1); n != 1 {
		t.Fatalf("feed kept %d arrivals for S1, want 1", n)
	}
	if _, err := b.d.Db.Exec(`DELETE FROM mirror_sale_arrivals WHERE id = ?`, s1); err != nil {
		t.Fatal(err)
	}
	if _, err := b.d.Db.Exec(`UPDATE mirror_sale_arrivals SET completed_at = ? WHERE id = ?`,
		time.Now().AddDate(0, 0, -10).UTC().Format(time.RFC3339), s2); err != nil {
		t.Fatal(err)
	}

	epoch, err := salesMirrorEpoch(ctx, shop.main.d)
	if err != nil {
		t.Fatal(err)
	}
	if err := data.NewSalesMirrorRepo(b.d.Db).StartResync(ctx, epoch); err != nil {
		t.Fatal(err)
	}
	shop.pull(b)
	shop.pull(b)

	if c, n := arrival(s1); n != 1 || c != completedOnMain(s1) {
		t.Fatalf("S1 arrival = %q (%d rows), want %q", c, n, completedOnMain(s1))
	}
	if c, n := arrival(s2); n != 1 || c != completedOnMain(s2) {
		t.Fatalf("S2 arrival = %q (%d rows), want the main till's %q", c, n, completedOnMain(s2))
	}
	if _, n := arrival(s3); n != 0 {
		t.Fatalf("S3 outside the window kept %d arrivals", n)
	}
	if n := harnessCount(t, b.d.Db, `SELECT COUNT(*) FROM mirror_sales WHERE origin_till_id = ?`, b.tillID); n != 0 {
		t.Fatalf("copy holds %d of B's own sales", n)
	}
	st, err := data.NewSalesMirrorRepo(b.d.Db).State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	head, _ := data.NewSalesMirrorRepo(shop.main.d.Db).LogHead(ctx)
	if st.Rejected != 0 || st.Cursor != head || st.ResyncEpoch != "" {
		t.Fatalf("state = %+v, want Rejected 0, cursor %d, no re-sync open", st, head)
	}
}
