package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5445: the four Settings -> Data actions run as one till-wide
// in-process background job. These tests pin the contract: 202 + job_id on
// start, a poll endpoint, one slot, and -- the bug itself -- a job that is
// not rolled back when the starting request's context is cancelled.

var dataJobIDRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// dataJobPoll GETs /api/data/jobs/{id} and returns the recorder.
func dataJobPoll(mux *http.ServeMux, id string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/data/jobs/"+id, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// awaitDataJob asserts the start response is a 202 job start, waits for the
// job to finish and returns the job's own http_status and result body (what
// the synchronous handler used to answer).
func awaitDataJob(t *testing.T, mux *http.ServeMux, start *httptest.ResponseRecorder) (int, map[string]any) {
	t.Helper()
	if start.Code != http.StatusAccepted {
		t.Fatalf("start: expected 202, got %d: %s", start.Code, start.Body.String())
	}
	body := dataAPIJSONBody(t, start)
	d, _ := body["data"].(map[string]any)
	id, _ := d["job_id"].(string)
	if !dataJobIDRe.MatchString(id) {
		t.Fatalf("start: job_id %q is not 32 hex chars: %s", id, start.Body.String())
	}
	dataJobs.wg.Wait()
	rec := dataJobPoll(mux, id)
	if rec.Code != http.StatusOK {
		t.Fatalf("poll: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	pb := dataAPIJSONBody(t, rec)
	pd, _ := pb["data"].(map[string]any)
	if pd["state"] != "done" {
		t.Fatalf("poll: expected state done, got %s", rec.Body.String())
	}
	status, _ := pd["http_status"].(float64)
	result, _ := pd["result"].(map[string]any)
	return int(status), result
}

// holdDataJob starts a job that blocks until release() is called.
func holdDataJob(t *testing.T, dp *common.Deps) (id string, release func()) {
	t.Helper()
	ch := make(chan struct{})
	id, err := dataJobs.start(dp, context.Background(), "reset_transactions", func(ctx context.Context) (int, map[string]any) {
		<-ch
		return http.StatusOK, map[string]any{"data": map[string]any{"message": "held"}, "error": nil}
	})
	if err != nil {
		t.Fatalf("hold: %v", err)
	}
	var once bool
	release = func() {
		if !once {
			once = true
			close(ch)
		}
		dataJobs.wg.Wait()
	}
	t.Cleanup(release)
	return id, release
}

func seedOneSale(t *testing.T, dp *common.Deps) {
	t.Helper()
	if _, err := dp.Db.ExecContext(t.Context(), `INSERT INTO sales(id,receipt_no,status,sale_type,currency,subtotal,discount_total,tax_total,total,created_at) VALUES('s1','R001','completed','sale','GBP',100,0,0,100,datetime('now'))`); err != nil {
		t.Fatal(err)
	}
}

func TestDataJob_StartReturns202WithJobID(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	// A batch with no sales: restorable and (no trading history) purgeable.
	_, batchID, err := data.NewPOSRepo(dp.Db).ResetTransactionHistory(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct{ path, action string }{
		{"/api/data/cleanup-catalog", "catalog_cleanup"},
		{"/api/data/reset-archives/" + batchID + "/purge", "archive_purge"},
		{"/api/data/reset-transactions", "reset_transactions"},
	}
	for _, c := range cases {
		rec := postForm(mux, c.path, url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: expected 202, got %d: %s", c.path, rec.Code, rec.Body.String())
		}
		body := dataAPIJSONBody(t, rec)
		if body["error"] != nil {
			t.Fatalf("%s: error must be null: %s", c.path, rec.Body.String())
		}
		d, _ := body["data"].(map[string]any)
		if id, _ := d["job_id"].(string); !dataJobIDRe.MatchString(id) {
			t.Errorf("%s: bad job_id in %s", c.path, rec.Body.String())
		}
		if d["action"] != c.action {
			t.Errorf("%s: action = %v, want %s", c.path, d["action"], c.action)
		}
		if d["state"] != "running" {
			t.Errorf("%s: state = %v, want running", c.path, d["state"])
		}
		dataJobs.wg.Wait()
	}
	// restore: needs a batch and an empty live side; the reset above made a
	// newer batch holding zero sales, so restore the oldest-known id is gone
	// after purge -- create a fresh one.
	_, batch2, err := data.NewPOSRepo(dp.Db).ResetTransactionHistory(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/data/reset-archives/"+batch2+"/restore", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("restore: expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	if d, _ := dataAPIJSONBody(t, rec)["data"].(map[string]any); d["action"] != "archive_restore" || d["state"] != "running" {
		t.Errorf("restore: bad start body %s", rec.Body.String())
	}
	dataJobs.wg.Wait()
}

func TestDataJob_PollRunningDoneFailedGone(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)

	// running -> done, with the stored sync-shaped result.
	id, release := holdDataJob(t, dp)
	rec := dataJobPoll(mux, id)
	if rec.Code != http.StatusOK {
		t.Fatalf("poll running: %d %s", rec.Code, rec.Body.String())
	}
	d, _ := dataAPIJSONBody(t, rec)["data"].(map[string]any)
	if d["state"] != "running" || d["job_id"] != id || d["action"] != "reset_transactions" {
		t.Fatalf("poll running: %s", rec.Body.String())
	}
	if _, has := d["result"]; has {
		t.Errorf("running poll must not carry a result: %s", rec.Body.String())
	}
	release()
	d, _ = dataAPIJSONBody(t, dataJobPoll(mux, id))["data"].(map[string]any)
	if d["state"] != "done" || d["http_status"] != float64(200) {
		t.Fatalf("poll done: %v", d)
	}
	if res, _ := d["result"].(map[string]any); res["error"] != nil || res["data"] == nil {
		t.Fatalf("done result should be the sync body: %v", d)
	}

	// panic -> failed, process survives, slot freed.
	id, err := dataJobs.start(dp, context.Background(), "catalog_cleanup", func(ctx context.Context) (int, map[string]any) {
		panic("boom")
	})
	if err != nil {
		t.Fatal(err)
	}
	dataJobs.wg.Wait()
	d, _ = dataAPIJSONBody(t, dataJobPoll(mux, id))["data"].(map[string]any)
	if d["state"] != "failed" {
		t.Fatalf("panicking job: want failed, got %v", d)
	}
	if dataJobs.running() {
		t.Fatal("slot must be free after a failed job")
	}

	// unknown id -> 404 with the interrupted message.
	rec = dataJobPoll(mux, "deadbeefdeadbeefdeadbeefdeadbeef")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: expected 404, got %d", rec.Code)
	}
	if b := dataAPIJSONBody(t, rec); b["data"] != nil || b["error"] == "" || b["error"] == nil {
		t.Fatalf("unknown id body: %s", rec.Body.String())
	}

	// result past its TTL -> 404.
	old := dataJobResultTTL
	dataJobResultTTL = time.Millisecond
	t.Cleanup(func() { dataJobResultTTL = old })
	id, err = dataJobs.start(dp, context.Background(), "catalog_cleanup", func(ctx context.Context) (int, map[string]any) {
		return http.StatusOK, map[string]any{"data": nil, "error": nil}
	})
	if err != nil {
		t.Fatal(err)
	}
	dataJobs.wg.Wait()
	time.Sleep(5 * time.Millisecond)
	if rec := dataJobPoll(mux, id); rec.Code != http.StatusNotFound {
		t.Fatalf("expired result: expected 404, got %d", rec.Code)
	}
}

// The bug (#5445): the request's context was the transaction's context, so
// the client's 30 s abort rolled the action back. A job must commit even if
// the starting request's context is cancelled mid-flight.
func TestDataJob_SurvivesRequestCancel(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	_, dp := newDataAPITestDeps(t)
	seedOneSale(t, dp)

	parent, cancel := context.WithCancel(t.Context())
	gate := make(chan struct{})
	_, err := dataJobs.start(dp, parent, "reset_transactions", func(ctx context.Context) (int, map[string]any) {
		<-gate // the request is cancelled while the job is "running"
		_, _, err := data.NewPOSRepo(dp.Db).ResetTransactionHistory(ctx, "", "")
		if err != nil {
			return http.StatusInternalServerError, map[string]any{"data": nil, "error": err.Error()}
		}
		return http.StatusOK, map[string]any{"data": map[string]any{}, "error": nil}
	})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	close(gate)
	dataJobs.wg.Wait()

	var sales, archived, batches, audits int
	_ = dp.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&sales)
	_ = dp.Db.QueryRow(`SELECT COUNT(*) FROM sales_archive`).Scan(&archived)
	_ = dp.Db.QueryRow(`SELECT COUNT(*) FROM reset_batches`).Scan(&batches)
	_ = dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'transactions_reset'`).Scan(&audits)
	if sales != 0 || archived != 1 || batches != 1 {
		t.Fatalf("cancelled parent rolled the job back: sales=%d archived=%d batches=%d", sales, archived, batches)
	}
	_ = audits // audit action name is asserted in the audit-approver test
}

func TestDataJob_SecondStartRefused409(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	_, batchID, err := data.NewPOSRepo(dp.Db).ResetTransactionHistory(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	_, release := holdDataJob(t, dp)

	paths := []string{
		"/api/data/reset-transactions",
		"/api/data/cleanup-catalog",
		"/api/data/reset-archives/" + batchID + "/restore",
		"/api/data/reset-archives/" + batchID + "/purge",
	}
	for _, p := range paths {
		// No PIN at all: the busy refusal comes BEFORE the PIN prompt.
		rec := postForm(mux, p, url.Values{}, nil)
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s while busy: expected 409, got %d: %s", p, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "override_pin") {
			t.Fatalf("%s: busy must be refused before the elevation prompt", p)
		}
		b := dataAPIJSONBody(t, rec)
		if b["error"] == nil || b["error"] == "" {
			t.Errorf("%s: busy 409 needs an error message: %s", p, rec.Body.String())
		}
	}

	// Racing past the pre-check (slot taken between check and start): start
	// itself refuses.
	if _, err := dataJobs.start(dp, context.Background(), "catalog_cleanup", func(context.Context) (int, map[string]any) {
		return 200, nil
	}); err != errDataJobBusy {
		t.Fatalf("second start: got %v, want errDataJobBusy", err)
	}

	release()
	rec := postForm(mux, "/api/data/cleanup-catalog", url.Values{"override_pin": {dataAPITestManagerPIN}}, nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("after release: expected 202, got %d: %s", rec.Code, rec.Body.String())
	}
	dataJobs.wg.Wait()
}

func TestDataJob_BusyBodyCarriesCode(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	_, release := holdDataJob(t, dp)
	defer release()
	rec := postForm(mux, "/api/data/reset-archives/x/restore", url.Values{}, nil)
	d, _ := dataAPIJSONBody(t, rec)["data"].(map[string]any)
	if d["code"] != "job_busy" {
		t.Fatalf("busy 409 must carry data.code=job_busy so the client need not match translated text: %s", rec.Body.String())
	}
}

func TestDataJob_PollRequiresManager(t *testing.T) {
	// No UT_AUTH=off and no session user: 403, and the body never reveals
	// whether the id exists.
	mux, dp := newDataAPITestDeps(t)
	id, release := holdDataJob(t, dp)
	defer release()
	rec := dataJobPoll(mux, id)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("poll without manager: expected 403, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), id) {
		t.Fatalf("403 must not echo the job: %s", rec.Body.String())
	}
}

// Restore's in-tx re-check still maps to 422 -- now reported through the
// poll, not the start response.
func TestDataJob_ResultBuilders(t *testing.T) {
	st, b := restoreResult("en", 0, data.ErrArchiveReferencesRemoved)
	if st != http.StatusUnprocessableEntity || b["error"] == nil {
		t.Fatalf("restoreResult references removed = %d %v", st, b)
	}
	st, b = restoreResult("en", 0, data.ErrShopHasTradedSinceReset)
	if st != http.StatusConflict || b["error"] == nil {
		t.Fatalf("restoreResult traded = %d %v", st, b)
	}
}

// mustDataJob waits for the job a start response began and asserts its own
// result status; it returns the result body (what the old synchronous
// handler wrote).
func mustDataJob(t *testing.T, mux *http.ServeMux, start *httptest.ResponseRecorder, want int) map[string]any {
	t.Helper()
	st, body := awaitDataJob(t, mux, start)
	if st != want {
		t.Fatalf("job result: expected %d, got %d: %v", want, st, body)
	}
	return body
}

// R4: a reload mid-job resumes. The Data-management card carries the running
// job's id and action; a finished result is replayed until a poll has
// delivered it, then not again; a cashier never gets the hint.
func TestSettingsPage_DataJobResumeHint(t *testing.T) {
	clearDataJobs(t)
	mux, _, d := newFullAuthDeps(t)
	if body := getAs(mux, "/settings", &mgrUser).Body.String(); strings.Contains(body, "data-job-id=") {
		t.Fatal("resume hint rendered with no job")
	}
	if !strings.Contains(getAs(mux, "/settings", &mgrUser).Body.String(), `id="data-job-msg"`) {
		t.Fatal("manager card lacks the #data-job-msg live region")
	}
	id, release := holdDataJob(t, d)
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	if !strings.Contains(body, `data-job-id="`+id+`"`) || !strings.Contains(body, `data-job-action="reset_transactions"`) {
		t.Fatal("running job not announced on the card")
	}
	if strings.Contains(getAs(mux, "/settings", &cashUser).Body.String(), "data-job-id=") {
		t.Fatal("cashier received the job id")
	}
	release()
	if !strings.Contains(getAs(mux, "/settings", &mgrUser).Body.String(), `data-job-id="`+id+`"`) {
		t.Fatal("an unfetched result must still resume")
	}
	if _, ok := dataJobs.get(id); !ok {
		t.Fatal("job vanished")
	}
	if strings.Contains(getAs(mux, "/settings", &mgrUser).Body.String(), "data-job-id=") {
		t.Fatal("a delivered result must not be replayed on later reloads")
	}
}

// clearDataJobs forgets any earlier test's finished job (the slot is
// process-wide) after waiting for it to finish.
func clearDataJobs(t *testing.T) {
	t.Helper()
	dataJobs.wg.Wait()
	dataJobs.mu.Lock()
	dataJobs.cur = nil
	dataJobs.mu.Unlock()
}
