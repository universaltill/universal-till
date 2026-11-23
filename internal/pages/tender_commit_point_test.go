package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6923: once a tender commits its sale, clear/park/open are refused
// until the sale's basket is reset.

const commitCashTender = `{"payments":[{"method":"cash","amount":120}]}`

// blockAtCommit parks the tender for dp.Engine inside its commit span (after
// the sale row, before the basket reset). The caller closes release.
func blockAtCommit(t *testing.T, dp *common.Deps) (entered, release chan struct{}) {
	t.Helper()
	entered = make(chan struct{})
	release = make(chan struct{})
	prev := tenderCommittedHook
	tenderCommittedHook = func(engine *pos.Service) {
		if engine != dp.Engine {
			return
		}
		close(entered)
		<-release
	}
	t.Cleanup(func() { tenderCommittedHook = prev })
	return entered, release
}

// tenderInWindow starts a cash tender, waits until it is inside the commit
// span, runs during, then lets the tender finish and returns its response.
func tenderInWindow(t *testing.T, mux *http.ServeMux, dp *common.Deps, during func()) *httptest.ResponseRecorder {
	t.Helper()
	entered, release := blockAtCommit(t, dp)
	res := make(chan *httptest.ResponseRecorder, 1)
	go func() { res <- postTender(t, mux, commitCashTender) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		close(release)
		t.Fatal("tender never reached the commit point")
	}
	func() {
		defer close(release)
		during()
	}()
	select {
	case rec := <-res:
		return rec
	case <-time.After(10 * time.Second):
		t.Fatal("tender did not return")
		return nil
	}
}

func wantCommitRefused(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "tender_in_flight" {
		t.Fatalf("want error.code tender_in_flight, got %q (%v)", rec.Body.String(), err)
	}
	if rec.Header().Get("HX-Reswap") != "none" {
		t.Fatalf("want HX-Reswap none, got %q", rec.Header().Get("HX-Reswap"))
	}
}

func wantSoldOnce(t *testing.T, dp *common.Deps, tender *httptest.ResponseRecorder) {
	t.Helper()
	if tender.Code != http.StatusOK {
		t.Fatalf("tender = %d: %s", tender.Code, tender.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("sales = %d, want 1", n)
	}
	if dp.Engine.HasItemsOrByHand() {
		t.Fatal("basket must be empty after the sale")
	}
}

func commitPointDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newFiscalSignDeps(t)
	registerHoldAPI(mux, dp)
	registerOpenOrders(mux, dp)
	return mux, dp
}

func scanABC(t *testing.T, dp *common.Deps) {
	t.Helper()
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
}

// parkedOrder holds one ABC basket and returns its held id; the basket is
// then empty.
func parkedOrder(t *testing.T, mux *http.ServeMux, dp *common.Deps) string {
	t.Helper()
	scanABC(t, dp)
	if rec := abandonPost(t, mux, "/api/pos/hold", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("setup hold = %d: %s", rec.Code, rec.Body.String())
	}
	return heldID(t, dp)
}

func TestCommitPoint_HoldRefused(t *testing.T) {
	mux, dp := commitPointDeps(t)
	scanABC(t, dp)
	var hold *httptest.ResponseRecorder
	tender := tenderInWindow(t, mux, dp, func() {
		hold = abandonPost(t, mux, "/api/pos/hold", url.Values{})
	})
	wantCommitRefused(t, hold)
	wantSoldOnce(t, dp, tender)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM held_sales"); n != 0 {
		t.Fatalf("held_sales = %d, want 0", n)
	}
}

func TestCommitPoint_ClearRefused(t *testing.T) {
	mux, dp := commitPointDeps(t)
	scanABC(t, dp)
	var rst *httptest.ResponseRecorder
	tender := tenderInWindow(t, mux, dp, func() {
		rst = abandonPost(t, mux, "/api/pos/reset", nil)
	})
	wantCommitRefused(t, rst)
	wantSoldOnce(t, dp, tender)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM cleared_baskets"); n != 0 {
		t.Fatalf("cleared_baskets = %d, want 0", n)
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM audit_log WHERE action = 'pos_reset'"); n != 0 {
		t.Fatalf("pos_reset audit rows = %d, want 0", n)
	}
}

func TestCommitPoint_ResumeRefused(t *testing.T) {
	mux, dp := commitPointDeps(t)
	b := parkedOrder(t, mux, dp)
	scanABC(t, dp)
	var res *httptest.ResponseRecorder
	tender := tenderInWindow(t, mux, dp, func() {
		res = abandonPost(t, mux, "/api/pos/resume", url.Values{"id": {b}})
	})
	wantCommitRefused(t, res)
	wantSoldOnce(t, dp, tender)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM held_sales WHERE id = '"+b+"'"); n != 1 {
		t.Fatalf("held order %s must be untouched, rows=%d", b, n)
	}
}

func TestCommitPoint_ReparkRefused(t *testing.T) {
	mux, dp := commitPointDeps(t)
	id := parkedOrder(t, mux, dp)
	if rec := abandonPost(t, mux, "/api/pos/resume", url.Values{"id": {id}}); rec.Code != http.StatusOK || dp.Engine.HeldOrigin().IsZero() {
		t.Fatalf("setup resume = %d", rec.Code)
	}
	var rst *httptest.ResponseRecorder
	tender := tenderInWindow(t, mux, dp, func() {
		rst = abandonPost(t, mux, "/api/pos/reset", nil)
	})
	wantCommitRefused(t, rst)
	wantSoldOnce(t, dp, tender)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM held_sales"); n != 0 {
		t.Fatalf("held_sales = %d, want 0 (no re-park of a sold order)", n)
	}
}

func TestCommitPoint_OpenOrdersResumeRefused(t *testing.T) {
	mux, dp := commitPointDeps(t)
	b := parkedOrder(t, mux, dp)
	scanABC(t, dp)
	var res *httptest.ResponseRecorder
	tender := tenderInWindow(t, mux, dp, func() {
		res = abandonPost(t, mux, "/open-orders/resume", url.Values{"id": {b}})
	})
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/open-orders?err=pos.tender.in_flight" {
		t.Fatalf("want 303 to the in-flight notice, got %d %q", res.Code, res.Header().Get("Location"))
	}
	wantSoldOnce(t, dp, tender)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM held_sales WHERE id = '"+b+"'"); n != 1 {
		t.Fatalf("held order must be untouched, rows=%d", n)
	}
}

func TestCommitPoint_CounterOpenRefused(t *testing.T) {
	mux, dp := commitPointDeps(t)
	scanABC(t, dp)
	legacy, err := data.NewKioskCounterOrdersRepo(dp.Db).Create(t.Context(), data.KioskCounterOrder{
		OrderType: "takeaway", Lines: []data.KioskCounterOrderLine{{Name: "Zzz Unmatched", Qty: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var res *httptest.ResponseRecorder
	tender := tenderInWindow(t, mux, dp, func() {
		res = abandonPost(t, mux, "/open-orders/counter/open", url.Values{"id": {legacy.ID}})
	})
	if res.Code != http.StatusSeeOther || res.Header().Get("Location") != "/open-orders?tab=counter&err=pos.tender.in_flight" {
		t.Fatalf("want 303 to the in-flight notice, got %d %q", res.Code, res.Header().Get("Location"))
	}
	wantSoldOnce(t, dp, tender)
	if n := countRows(t, dp, "SELECT COUNT(*) FROM held_sales"); n != 0 {
		t.Fatalf("counter order must not be converted, held_sales=%d", n)
	}
}

func TestCommitPoint_AbandonAfterCommitEndsIsAdmitted(t *testing.T) {
	mux, dp := commitPointDeps(t)
	scanABC(t, dp)
	wantSoldOnce(t, dp, postTender(t, mux, commitCashTender))
	scanABC(t, dp)
	if rec := abandonPost(t, mux, "/api/pos/hold", url.Values{}); rec.Code != http.StatusOK {
		t.Fatalf("hold after the sale = %d: %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM held_sales"); n != 1 {
		t.Fatalf("held_sales = %d, want 1", n)
	}
}

func TestCommitPoint_ReleasedOnCompleteSaleError(t *testing.T) {
	mux, dp := commitPointDeps(t)
	scanABC(t, dp)
	if _, err := dp.Db.Exec(`ALTER TABLE sales RENAME TO sales_gone`); err != nil {
		t.Fatal(err)
	}
	rec := postTender(t, mux, commitCashTender)
	if rec.Code == http.StatusOK && strings.Contains(rec.Body.String(), `"sale_id"`) {
		t.Fatalf("setup: the tender must fail: %s", rec.Body.String())
	}
	if !dp.Engine.BeginAbandon() {
		t.Fatal("a failed commit must release the abandon exclusion")
	}
	dp.Engine.EndAbandon()
}
