package pages

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
)

// ut-docs#6909: one cashier tender at a time per engine. A second Pay (or the
// split modal's fetch) while the first still waits on the card reader used to
// dispatch the same authorize request id twice and then reverse the payment of
// the sale the first one recorded.

const inflightTenderBody = `{"payments":[{"method":"demopay","amount":120}]}`

// blockedTender starts a tender that parks inside the card authorize until
// release is called, and returns once the plugin is waiting.
type blockedTender struct {
	t       *testing.T
	entered chan struct{}
	gate    chan struct{}
	once    sync.Once
	done    chan *httptest.ResponseRecorder
}

func startBlockedTender(t *testing.T, mux *http.ServeMux, answer string, gate chan struct{}, entered chan struct{}) *blockedTender {
	t.Helper()
	b := &blockedTender{t: t, entered: entered, gate: gate, done: make(chan *httptest.ResponseRecorder, 1)}
	go func() { b.done <- postTender(t, mux, inflightTenderBody) }()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("tender 1 never reached the card authorize")
	}
	return b
}

func (b *blockedTender) release() { b.once.Do(func() { close(b.gate) }) }

func (b *blockedTender) wait() *httptest.ResponseRecorder {
	b.t.Helper()
	b.release()
	select {
	case rec := <-b.done:
		return rec
	case <-time.After(10 * time.Second):
		b.t.Fatal("tender 1 never finished")
		return nil
	}
}

func TestTender_SecondConcurrentTenderIs409(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	gate := make(chan struct{})
	entered := make(chan struct{}, 4)
	refunds, authIDs := seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string {
		entered <- struct{}{}
		<-gate
		return approvedAnswer
	})
	if _, err := dp.Engine.Scan("ABC"); err != nil {
		t.Fatal(err)
	}
	bt := startBlockedTender(t, mux, approvedAnswer, gate, entered)
	t.Cleanup(bt.release)

	// Bounded: without the guard the second POST parks in the blocked
	// authorize; fail by assertion rather than hang until the test timeout.
	second := make(chan *httptest.ResponseRecorder, 1)
	go func() { second <- postTender(t, mux, inflightTenderBody) }()
	var rec *httptest.ResponseRecorder
	select {
	case rec = <-second:
	case <-time.After(3 * time.Second):
		t.Fatalf("second tender did not return 409 while the first waits (it entered authorize: %d authorize calls)", len(authIDs()))
	}
	if rec.Code != http.StatusConflict {
		t.Fatalf("second tender while the first waits = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("HX-Reswap"); got != "none" {
		t.Fatalf("HX-Reswap = %q, want none (the basket must not be replaced by the refusal)", got)
	}
	var env struct {
		Data  any `json:"data"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("409 body is not the JSON envelope: %v: %s", err, rec.Body.String())
	}
	if env.Error.Code != "tender_in_flight" || env.Data != nil {
		t.Fatalf("envelope = %s", rec.Body.String())
	}
	if len(authIDs()) != 1 {
		t.Fatalf("authorize calls = %d, want exactly 1", len(authIDs()))
	}

	first := bt.wait()
	if first.Code != http.StatusOK {
		t.Fatalf("tender 1 = %d: %s", first.Code, first.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("sales = %d, want 1", n)
	}
	if got := refunds(); len(got) != 0 {
		t.Fatalf("the recorded sale's payment was reversed: %+v", got)
	}
	if l := dp.Engine.Basket().Lines; len(l) != 0 {
		t.Fatalf("basket not reset: %d lines", len(l))
	}
	if dp.Engine.TenderInFlight() {
		t.Fatal("guard still held after tender 1 finished")
	}
}

func TestTender_InFlight409UsesLocalizedEnvelope(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	gate := make(chan struct{})
	entered := make(chan struct{}, 4)
	seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { entered <- struct{}{}; <-gate; return approvedAnswer })
	scan(t, dp)
	bt := startBlockedTender(t, mux, approvedAnswer, gate, entered)
	t.Cleanup(bt.release)
	rec := postTender(t, mux, inflightTenderBody)
	var env struct {
		Error struct{ Message string } `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	want := httpx.T("en", "pos.tender.in_flight")
	if want == "" || want == "pos.tender.in_flight" || env.Error.Message != want {
		t.Fatalf("message = %q, want the en.json text %q", env.Error.Message, want)
	}
	bt.wait()
}

func TestTender_InFlightReleasedOnEveryExit(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer string
		body   string
	}{
		{"success", approvedAnswer, inflightTenderBody},
		{"decline", "REALDECLINE", inflightTenderBody},
		{"plugin error", "DECLINE", inflightTenderBody},
		{"validation error", approvedAnswer, `{"payments":[{"method":"demopay","amount":-5}]}`},
		{"invalid json", approvedAnswer, `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, dp := newFiscalSignDeps(t)
			seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { return tc.answer })
			scan(t, dp)
			rec := postTender(t, mux, tc.body)
			if rec.Code == http.StatusConflict {
				t.Fatalf("first tender must not be 409: %s", rec.Body.String())
			}
			if dp.Engine.TenderInFlight() {
				t.Fatalf("guard still held after %s (response %d)", tc.name, rec.Code)
			}
			scan(t, dp)
			if rec := postTender(t, mux, inflightTenderBody); rec.Code == http.StatusConflict {
				t.Fatalf("a later tender is refused after %s: %s", tc.name, rec.Body.String())
			}
		})
	}
}

// The cashier clears the sale while the card waits (ut-docs#6602 abandon).
func TestTender_InFlightReleasedOnAbandon(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	gate := make(chan struct{})
	entered := make(chan struct{}, 4)
	seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { entered <- struct{}{}; <-gate; return approvedAnswer })
	scan(t, dp)
	bt := startBlockedTender(t, mux, approvedAnswer, gate, entered)
	dp.Engine.Reset()
	if !dp.Engine.TenderInFlight() {
		t.Fatal("a Reset must not release a tender that is still running")
	}
	bt.wait()
	if dp.Engine.TenderInFlight() {
		t.Fatal("guard still held after the abandoned tender ended")
	}
	scan(t, dp)
	if rec := postTender(t, mux, inflightTenderBody); rec.Code == http.StatusConflict {
		t.Fatalf("a later tender is refused after an abandon: %s", rec.Body.String())
	}
}

func TestScan_RefusedWhileTenderInFlight(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	gate := make(chan struct{})
	entered := make(chan struct{}, 4)
	seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { entered <- struct{}{}; <-gate; return approvedAnswer })
	scan(t, dp)
	bt := startBlockedTender(t, mux, approvedAnswer, gate, entered)
	t.Cleanup(bt.release)

	req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader(url.Values{"code": {"ABC"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("scan = %d", rec.Code)
	}
	want := httpx.T("en", "pos.tender.scan_refused")
	if !strings.Contains(strings.ReplaceAll(rec.Body.String(), "&#39;", "'"), want) && !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("scan during a tender must show %q: %s", want, rec.Body.String())
	}
	if l := dp.Engine.Lines(); len(l) != 1 || l[0].Qty != 1 {
		t.Fatalf("the scan changed the basket mid-tender: %+v", l)
	}
	bt.wait()
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sales"); n != 1 {
		t.Fatalf("sales = %d", n)
	}
	if n := countRows(t, dp, "SELECT COALESCE(SUM(quantity),0) FROM sale_lines"); n != 1 {
		t.Fatalf("sale lines qty = %d, want only the original line", n)
	}
}

// ut-docs#6919 (item 1): every cashier basket mutation is refused with the same
// 409 while a tender runs, before it touches the engine or the DB.
type basketEditRoute struct {
	name, method, path string
	form               url.Values
}

// reset, hold, resume and the two open-orders openers are deliberately NOT
// here: they are the cashier's escape from a payment that never answers
// (ut-docs#6602 abandon path: the tender sees its basket replaced, records no
// sale and reverses its captured legs), pinned by abandoned_tender_reversal_test.go.
var basketEditRoutes = []basketEditRoute{
	{"remove", "POST", "/api/pos/remove", url.Values{"key": {"x"}}},
	{"age-check", "POST", "/api/pos/age-check", url.Values{"key": {"x"}}},
	{"line", "POST", "/api/pos/line", url.Values{"key": {"x"}, "qty": {"5"}}},
	{"discount", "POST", "/api/pos/discount", url.Values{"type": {"percent"}, "value": {"10"}}},
	{"order-type", "POST", "/api/pos/order-type", url.Values{"order_type": {"takeaway"}}},
	{"table", "POST", "/api/pos/table", url.Values{"table": {"7"}}},
	{"scan-with-modifiers", "POST", "/api/pos/scan-with-modifiers", url.Values{"sku": {"ABC"}}},
	{"add-by-hand-dismiss", "POST", "/api/pos/add-by-hand/dismiss", nil},
	{"voucher-top-up", "POST", "/vouchers/V1/top-up", url.Values{"amount": {"10"}}},
}

func basketEditMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, dp := newFiscalSignDeps(t)
	registerHoldAPI(mux, dp)
	registerOpenOrders(mux, dp)
	registerVouchers(mux, dp)
	registerPOSModifiersAPI(mux, dp)
	// A real multi-purpose voucher, so the top-up row gets past its lookup and
	// reaches the basket-edit claim (taken after it, ut-docs#6919 review S3).
	if _, err := dp.Db.Exec(`INSERT INTO vouchers(id, original_amount, balance, currency, issued_sale_id) VALUES ('V1', 1000, 1000, 'GBP', 'gv-sale')`); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}
	return mux, dp
}

func doRoute(mux *http.ServeMux, rt basketEditRoute) *httptest.ResponseRecorder {
	var body io.Reader
	if rt.form != nil {
		body = strings.NewReader(rt.form.Encode())
	}
	req := httptest.NewRequest(rt.method, rt.path, body)
	if rt.form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestBasketMutations_RefusedWhileTenderInFlight(t *testing.T) {
	for _, rt := range basketEditRoutes {
		t.Run(rt.name, func(t *testing.T) {
			mux, dp := basketEditMux(t)
			scan(t, dp)
			snap, _ := json.Marshal(dp.Engine.Basket())
			held := countRows(t, dp, "SELECT COUNT(*) FROM held_sales")
			cleared := countRows(t, dp, "SELECT COUNT(*) FROM cleared_baskets")
			if !dp.Engine.TryBeginTender() {
				t.Fatal("claim")
			}
			t.Cleanup(dp.Engine.EndTender)

			rec := doRoute(mux, rt)
			if rec.Code != http.StatusConflict {
				t.Fatalf("%s during a tender = %d, want 409: %s", rt.path, rec.Code, rec.Body.String())
			}
			if code, _ := errCode(t, rec.Body.String()); code != "tender_in_flight" {
				t.Fatalf("error code = %q: %s", code, rec.Body.String())
			}
			if rec.Header().Get("HX-Reswap") != "none" || !strings.Contains(rec.Header().Get("HX-Trigger"), "tender-in-flight") {
				t.Fatalf("headers = %v", rec.Header())
			}
			if after, _ := json.Marshal(dp.Engine.Basket()); string(after) != string(snap) {
				t.Fatalf("basket changed:\n%s\n%s", snap, after)
			}
			if n := countRows(t, dp, "SELECT COUNT(*) FROM held_sales"); n != held {
				t.Fatalf("held_sales %d -> %d", held, n)
			}
			if n := countRows(t, dp, "SELECT COUNT(*) FROM cleared_baskets"); n != cleared {
				t.Fatalf("cleared_baskets %d -> %d", cleared, n)
			}
		})
	}
}

func TestBasketMutations_AllowedAfterEndTender(t *testing.T) {
	for _, rt := range basketEditRoutes {
		t.Run(rt.name, func(t *testing.T) {
			mux, dp := basketEditMux(t)
			scan(t, dp)
			if !dp.Engine.TryBeginTender() {
				t.Fatal("claim")
			}
			dp.Engine.EndTender()
			if rec := doRoute(mux, rt); rec.Code == http.StatusConflict && strings.Contains(rec.Body.String(), "tender_in_flight") {
				t.Fatalf("%s still refused after EndTender: %s", rt.path, rec.Body.String())
			}
		})
	}
}

// The race itself: an edit admitted before the tender started is waited for,
// so the sale matches what the cashier saw (qty 2), not the stale read.
func TestTender_WaitsForRunningBasketEdit(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	gate := make(chan struct{})
	entered := make(chan struct{}, 4)
	seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { entered <- struct{}{}; <-gate; return approvedAnswer })
	scan(t, dp)
	if !dp.Engine.BeginBasketEdit() {
		t.Fatal("begin edit")
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- postTender(t, mux, `{"payments":[{"method":"demopay","amount":240}]}`) }()
	select {
	case <-entered:
		t.Fatal("tender reached the card before the running edit finished")
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := dp.Engine.Scan("ABC"); err != nil { // the late add of the admitted edit
		t.Fatal(err)
	}
	dp.Engine.EndBasketEdit()
	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("tender never proceeded after the edit ended")
	}
	close(gate)
	select {
	case rec := <-done:
		if rec.Code != http.StatusOK {
			t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("tender never finished")
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sale_lines WHERE quantity >= 2"); n != 1 {
		t.Fatalf("recorded sale must carry the late-added qty 2 (rows=%d)", n)
	}
}

// ut-docs#6919 (tester): the scan's own admission is atomic. A scan that is
// still resolving its code when the tender starts holds the tender off until
// its line lands, so the sale carries it (qty 2). A check-then-act
// TenderInFlight() test here would let the tender read the basket first.
type blockingScanResolver struct {
	stubResolver
	armed   chan struct{} // closed: the next ABC resolve blocks
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *blockingScanResolver) ResolveExact(code string) (pos.BasketLine, bool) {
	select {
	case <-r.armed:
		r.once.Do(func() { close(r.entered); <-r.release })
	default:
	}
	return r.Resolve(code)
}

func TestTender_WaitsForRunningScan(t *testing.T) {
	mux, dp := newFiscalSignDeps(t)
	res := &blockingScanResolver{
		stubResolver: stubResolver{"ABC": {SKU: "ABC", Name: "Apple", Qty: 1, PriceCents: 100, ItemID: "itm1", VariantID: "var1", TaxRateBP: 2000}},
		armed:        make(chan struct{}), entered: make(chan struct{}), release: make(chan struct{}),
	}
	dp.Engine = pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000, TaxInclusive: false}, res)
	gate := make(chan struct{})
	cardEntered := make(chan struct{}, 4)
	seedCardPluginWithAnswerFunc(t, dp, nil, func(int) string { cardEntered <- struct{}{}; <-gate; return approvedAnswer })
	scan(t, dp)
	close(res.armed)
	var releaseOnce sync.Once
	rel := func() { releaseOnce.Do(func() { close(res.release) }) }
	t.Cleanup(rel)

	scanDone := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/pos/scan", strings.NewReader(url.Values{"code": {"ABC"}}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		scanDone <- rec.Code
	}()
	select {
	case <-res.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("scan never reached the resolver")
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- postTender(t, mux, `{"payments":[{"method":"demopay","amount":240}]}`) }()
	select {
	case <-cardEntered:
		t.Fatal("tender reached the card while an admitted scan was still running")
	case <-time.After(200 * time.Millisecond):
	}
	rel()
	if c := <-scanDone; c != http.StatusOK {
		t.Fatalf("scan = %d", c)
	}
	if l := dp.Engine.Lines(); len(l) != 1 || l[0].Qty != 2 {
		t.Fatalf("the scan did not add its line: %+v", l)
	}
	select {
	case <-cardEntered:
	case rec := <-done:
		t.Fatalf("tender ended without reaching the card: %d %s", rec.Code, rec.Body.String())
	case <-time.After(15 * time.Second):
		t.Fatal("tender never proceeded after the scan ended")
	}
	close(gate)
	rec := <-done
	if rec.Code != http.StatusOK {
		t.Fatalf("tender = %d: %s", rec.Code, rec.Body.String())
	}
	if n := countRows(t, dp, "SELECT COUNT(*) FROM sale_lines WHERE quantity >= 2"); n != 1 {
		t.Fatalf("recorded sale must carry the scanned qty 2 (rows=%d)", n)
	}
}
