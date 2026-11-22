package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/httpx"
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
