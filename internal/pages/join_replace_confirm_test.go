package pages

import (
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#4507: linking replaces this till's whole DB with the main till's
// copy (completeJoin stages the snapshot as a restore). #3796 warned only in
// the ambient join notice; these pin the server-side check on every path —
// pair-start, the code join (manager and first-boot flavours), pair-status
// when the first sale lands mid-approval — and the Tills page's confirm.

// replaceWarningHTML is the warning as html/template and html.EscapeString
// both render it (the apostrophe in "till's" becomes &#39;).
func replaceWarningHTML() string {
	return html.EscapeString(httpx.T("en", "tills.discovery.ambient.has_sales_warning"))
}

// countingPairStub is a fake main till: it accepts a pair request, hands
// out a token on the status poll, and counts every request that reaches it
// by path.
type countingPairStub struct {
	srv          *httptest.Server
	pairRequests atomic.Int32
	enrolls      atomic.Int32
	total        atomic.Int32
}

func newCountingPairStub(t *testing.T) *countingPairStub {
	t.Helper()
	s := &countingPairStub{}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/sync/pair-request", func(w http.ResponseWriter, r *http.Request) {
		s.pairRequests.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"req-4507"},"error":null}`))
	})
	mux.HandleFunc("GET /api/sync/pair-requests/req-4507", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"token":"tok-4507"},"error":null}`))
	})
	mux.HandleFunc("POST /api/sync/enroll", func(w http.ResponseWriter, r *http.Request) {
		s.enrolls.Add(1)
		http.Error(w, "stub", http.StatusInternalServerError)
	})
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.total.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(s.srv.Close)
	return s
}

func postPairStartForm(t *testing.T, mux *http.ServeMux, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPairStart_RefusesWithoutReplaceConfirmWhenTillHasSales(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	stub := newCountingPairStub(t)
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	seedCompletedSaleForStatus(t, replica.Db, "sale-4507")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairStartForm(t, rmux, url.Values{"base_url": {stub.srv.URL}, "till_id": {"main-1"}, "name": {"Bar Till"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-start renders its error fragment as a 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, replaceWarningHTML()) {
		t.Fatalf("expected the replace-data warning in the error fragment, got: %s", body)
	}
	if n := stub.pairRequests.Load(); n != 0 {
		t.Fatalf("no pair request may reach the main till without the confirm, got %d", n)
	}
}

func TestPairStart_SendsWithReplaceConfirmWhenTillHasSales(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	stub := newCountingPairStub(t)
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	seedCompletedSaleForStatus(t, replica.Db, "sale-4507")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairStartForm(t, rmux, url.Values{"base_url": {stub.srv.URL}, "till_id": {"main-1"}, "name": {"Bar Till"}, "replace_local_data": {"1"}})
	if !strings.Contains(rec.Body.String(), `hx-trigger="every 15s"`) {
		t.Fatalf("expected the waiting state once confirmed, got: %s", rec.Body.String())
	}
	if n := stub.pairRequests.Load(); n != 1 {
		t.Fatalf("expected one pair request with the confirm, got %d", n)
	}
}

func TestPairStart_SendsWithoutConfirmWhenTillHasNoSales(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	stub := newCountingPairStub(t)
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairStart(t, rmux, stub.srv.URL, "main-1", "Bar Till")
	if strings.Contains(rec.Body.String(), replaceWarningHTML()) {
		t.Fatalf("a till with no sales must not be asked to confirm, got: %s", rec.Body.String())
	}
	if n := stub.pairRequests.Load(); n != 1 {
		t.Fatalf("expected one pair request from a till with no sales, got %d", n)
	}
}

// The first sale lands after pair-start (sent without the confirm, as the
// till had none) but before the main till approves: the poll must not
// complete the join over it.
func TestPairStatus_RefusesToCompleteWhenFirstSaleLandedAfterPairStart(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	stub := newCountingPairStub(t)
	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	if rec := postPairStart(t, rmux, stub.srv.URL, "main-1", "Bar Till"); !strings.Contains(rec.Body.String(), `hx-trigger="every 15s"`) {
		t.Fatalf("pair-start: expected waiting, got: %s", rec.Body.String())
	}
	seedCompletedSaleForStatus(t, replica.Db, "sale-4507")

	rec := getPairStatus(t, rmux)
	body := rec.Body.String()
	if !strings.Contains(body, replaceWarningHTML()) {
		t.Fatalf("expected the replace-data warning as the error, got: %s", body)
	}
	if n := stub.enrolls.Load(); n != 0 {
		t.Fatalf("completeJoin must not run (enrol hit %d times)", n)
	}
	// Belt and braces: the stub's enrol answers 500, so nothing could be
	// staged even on the old code — the enrol counter above is the check.
	if appdb.PendingRestore(replicaPath) {
		t.Fatal("no restore may be staged over this till's sales")
	}
	// The error is stored: the next poll re-renders it and still doesn't join.
	again := getPairStatus(t, rmux).Body.String()
	if !strings.Contains(again, replaceWarningHTML()) || stub.enrolls.Load() != 0 {
		t.Fatalf("the re-poll must keep the warning and not join, got: %s", again)
	}
}

// Confirmed at pair-start: a sale that exists is no reason to stop.
func TestPairStatus_CompletesWhenConfirmedAtPairStart(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	stub := newCountingPairStub(t)
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	seedCompletedSaleForStatus(t, replica.Db, "sale-4507")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	postPairStartForm(t, rmux, url.Values{"base_url": {stub.srv.URL}, "till_id": {"main-1"}, "name": {"Bar Till"}, "replace_local_data": {"1"}})
	body := getPairStatus(t, rmux).Body.String()
	if strings.Contains(body, replaceWarningHTML()) {
		t.Fatalf("a confirmed attempt must not be refused, got: %s", body)
	}
	if n := stub.enrolls.Load(); n != 1 {
		t.Fatalf("expected completeJoin to enrol once (the stub then refuses), got %d", n)
	}
}

func postSyncJoin(t *testing.T, mux *http.ServeMux, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func assertJoinRefusedForSales(t *testing.T, rec *httptest.ResponseRecorder, stub *countingPairStub, dbPath string) {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("expected text/html so htmx swaps it in, got %q", ct)
	}
	if want := `<span class="muted">✗ ` + replaceWarningHTML() + `</span>`; rec.Body.String() != want {
		t.Fatalf("expected %q, got %q", want, rec.Body.String())
	}
	if n := stub.total.Load(); n != 0 {
		t.Fatalf("the main till must not be contacted, got %d request(s)", n)
	}
	// Belt and braces: the stub's enrol answers 500, so nothing could be
	// staged even on the old code — the request counter above is the check.
	if dbPath != "" && appdb.PendingRestore(dbPath) {
		t.Fatal("no restore may be staged over this till's sales")
	}
}

func TestSyncJoin_RefusesWithoutReplaceConfirmWhenTillHasSales(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	stub := newCountingPairStub(t)
	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	seedCompletedSaleForStatus(t, replica.Db, "sale-4507")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)

	code := `{"url":"` + stub.srv.URL + `","token":"whatever"}`
	rec := postSyncJoin(t, rmux, "/api/sync/join", url.Values{"code": {code}, "name": {"Till 2"}})
	assertJoinRefusedForSales(t, rec, stub, replicaPath)
}

func TestSyncJoin_ProceedsWithReplaceConfirmWhenTillHasSales(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	if err := primary.Settings.Set(t.Context(), "store.name", "Corner Shop"); err != nil {
		t.Fatal(err)
	}
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)
	srv := httptest.NewServer(pmux)
	t.Cleanup(srv.Close)
	code := issueEnrolCode(t, pmux, srv.URL)

	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	seedCompletedSaleForStatus(t, replica.Db, "sale-4507")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)

	rec := postSyncJoin(t, rmux, "/api/sync/join", url.Values{"code": {code}, "name": {"Till 2"}, "replace_local_data": {"1"}})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Corner Shop") {
		t.Fatalf("expected the confirmed join to succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	if !appdb.PendingRestore(replicaPath) {
		t.Fatal("expected a staged restore after a confirmed join")
	}
}

func TestSetupJoin_RefusesWithoutReplaceConfirmWhenTillHasSales(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	registerSyncAPI(mux, d)
	stub := newCountingPairStub(t)
	seedCompletedSaleForStatus(t, d.Db, "sale-4507")

	code := `{"url":"` + stub.srv.URL + `","token":"whatever"}`
	rec := postSyncJoin(t, mux, "/api/setup/join", url.Values{"code": {code}, "name": {"Till 2"}})
	assertJoinRefusedForSales(t, rec, stub, d.Cfg.DBPath)
}

func getTillsPage(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/tills", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /tills: %d", rec.Code)
	}
	return rec.Body.String()
}

func TestTillsPage_JoinWarnsAndRequiresConfirmWhenTillHasSales(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	seedCompletedSaleForStatus(t, dp.Db, "sale-4507")

	body := getTillsPage(t, mux)
	for _, want := range []string{
		`id="pairing-replace-warning"`,
		replaceWarningHTML(),
		`<input type="checkbox" id="pairing-replace-local" name="replace_local_data" value="1" required aria-describedby="pairing-replace-warning">`,
		`for="pairing-replace-local"`,
		html.EscapeString(httpx.T("en", "tills.discovery.ambient.has_sales_confirm")),
		// The code-join form sends the tick and is held back without it.
		`hx-include="#pairing-till-name, #pairing-join-role, #pairing-replace-local"`,
		`data-before-request="trim-validate:pairing-till-name validate:pairing-replace-local"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q on the Tills page of a till with sales", want)
		}
	}
	// The discovery buttons are built in the inline script, which carries
	// the same include and guard (here as a JS string literal).
	if !strings.Contains(body, `#pairing-replace-local`) || strings.Count(body, `validate:pairing-replace-local`) < 2 {
		t.Errorf("the discovery result buttons must include and validate the confirm too: %s", body)
	}
}

func TestTillsPage_JoinHasNoConfirmWithoutSales(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)

	body := getTillsPage(t, mux)
	if !strings.Contains(body, `id="pairing-till-name"`) {
		t.Fatal("expected the join section")
	}
	if strings.Contains(body, "pairing-replace-local") || strings.Contains(body, "pairing-replace-warning") {
		t.Errorf("a till with no sales must not get the replace-data confirm")
	}
}
