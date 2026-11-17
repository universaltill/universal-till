package pages

import (
	"testing"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#5777 (Tester): the bare own-sale headers the feed now sends back
// to their origin till pass the receiver's shape check (no Rejected, which an
// old client would also hit first), are never stored in the copy, and a run
// of pages holding only own headers still moves the receiver's cursor to the
// head. Same validMirrorSale-then-skip path an old (pre-5777) client runs.
func TestSalesMirrorPull_OwnHeadersAdvanceNotRejected_5777(t *testing.T) {
	shop := newMirrorShop(t, "A")
	shop.enable()
	a := shop.tills["A"]
	shop.pull(a) // state re-sync first
	shop.pull(a)
	ids := []string{shop.sell(a, 100), shop.sell(a, 200), shop.sell(a, 300)}
	shop.push(a)
	shop.pull(a)
	shop.pull(a)
	ctx := t.Context()
	head, err := data.NewSalesMirrorRepo(shop.main.d.Db).LogHead(ctx)
	if err != nil {
		t.Fatal(err)
	}
	st, err := data.NewSalesMirrorRepo(a.d.Db).State(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Cursor != head || st.Rejected != 0 {
		t.Fatalf("A's copy cursor %d (head %d), rejected %d; want at head with 0 rejected", st.Cursor, head, st.Rejected)
	}
	for _, id := range ids {
		if n := harnessCount(t, a.d.Db, `SELECT COUNT(*) FROM mirror_sales WHERE id = ?`, id); n != 0 {
			t.Fatalf("own sale %s stored in A's copy", id)
		}
		if n := harnessCount(t, a.d.Db, `SELECT COUNT(*) FROM mirror_sale_arrivals WHERE id = ?`, id); n != 1 {
			t.Fatalf("own sale %s: %d arrival rows, want 1", id, n)
		}
	}
}
