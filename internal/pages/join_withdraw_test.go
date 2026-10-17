package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4566: completeJoin enrols with the main till first, then downloads
// and stages the snapshot. A failure past the enrolment used to leave the
// main till holding a till that never syncs, so the manager's retry with the
// same name was refused as "name already in use". The joining till now
// withdraws that enrolment with the bearer it was just issued.

func countTills(t *testing.T, d *common.Deps) int {
	t.Helper()
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM tills`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// enrolOnPrimary enrols a till called name on the main till's mux and
// returns its id and bearer.
func enrolOnPrimary(t *testing.T, pmux *http.ServeMux, name string) (string, string) {
	t.Helper()
	_, token, err := decodeEnrollCode(issueEnrolCode(t, pmux, "http://192.168.1.10:8080"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]string{"token": token, "name": name})
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	pmux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("enrol %q: %d %s", name, rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			TillID string `json:"till_id"`
			Bearer string `json:"bearer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data.Bearer == "" {
		t.Fatalf("enrol %q: bad response %s", name, rec.Body.String())
	}
	return out.Data.TillID, out.Data.Bearer
}

func postWithdraw(t *testing.T, pmux *http.ServeMux, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll/withdraw", nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	pmux.ServeHTTP(rec, req)
	return rec
}

func tillExists(t *testing.T, d *common.Deps, id string) bool {
	t.Helper()
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM tills WHERE id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n == 1
}

func TestSyncJoin_SaleDuringJoinWithdrawsEnrolmentSoSameNameRetrySucceeds(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)

	replica, _ := newSyncDepsWithPath(t, "replica.db")
	var snapshots atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/snapshot" && snapshots.Add(1) == 1 {
			seedCompletedSaleForStatus(t, replica.Db, "sale-4566")
		}
		pmux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)
	rec := postSyncJoin(t, rmux, "/api/sync/join", url.Values{"code": {issueEnrolCode(t, pmux, srv.URL)}, "name": {"Till 2"}})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), replaceWarningHTML()) {
		t.Fatalf("expected the mid-join sale refusal, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := countTills(t, primary); n != 0 {
		t.Fatalf("the main till must not keep the refused join's enrolment, has %d till(s)", n)
	}

	rec = postSyncJoin(t, rmux, "/api/sync/join", url.Values{"code": {issueEnrolCode(t, pmux, srv.URL)},
		"name": {"Till 2"}, "replace_local_data": {"1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("a same-name retry must succeed without a manual revoke, got %d: %s", rec.Code, rec.Body.String())
	}
	if n := countTills(t, primary); n != 1 {
		t.Fatalf("expected exactly the retried enrolment, got %d till(s)", n)
	}
	// The withdrawn enrolment's register went with it: the retry's register
	// is "Till 2" again, not "Till 2 (2)".
	var names []string
	rows, err := primary.Db.Query(`SELECT name FROM registers WHERE name LIKE 'Till 2%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	if len(names) != 1 || names[0] != "Till 2" {
		t.Fatalf("expected one register \"Till 2\" after the retry, got %q", names)
	}
}

func TestSyncJoin_SnapshotFailureWithdrawsEnrolmentSoSameNameRetrySucceeds(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)

	var snapshots atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sync/snapshot" && snapshots.Add(1) == 1 {
			http.Error(w, "disk full", http.StatusInternalServerError)
			return
		}
		pmux.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)

	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)
	rec := postSyncJoin(t, rmux, "/api/sync/join", url.Values{"code": {issueEnrolCode(t, pmux, srv.URL)}, "name": {"Till 2"}})
	if rec.Code == http.StatusOK {
		t.Fatalf("expected the failed snapshot to fail the join, got 200: %s", rec.Body.String())
	}
	if n := countTills(t, primary); n != 0 {
		t.Fatalf("the main till must not keep the failed join's enrolment, has %d till(s)", n)
	}

	rec = postSyncJoin(t, rmux, "/api/sync/join", url.Values{"code": {issueEnrolCode(t, pmux, srv.URL)}, "name": {"Till 2"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("a same-name retry must succeed without a manual revoke, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSyncEnrollWithdraw_RemovesOnlyTheBearersOwnTill(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	pmux, primary := newSyncAPITestDeps(t)
	idA, bearerA := enrolOnPrimary(t, pmux, "Till A")
	idB, _ := enrolOnPrimary(t, pmux, "Till B")

	if rec := postWithdraw(t, pmux, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no bearer: expected 401, got %d", rec.Code)
	}
	if rec := postWithdraw(t, pmux, "not-a-bearer"); rec.Code != http.StatusUnauthorized {
		t.Fatalf("unknown bearer: expected 401, got %d", rec.Code)
	}
	// A till id in the query or body is not how the till is chosen.
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll/withdraw?id="+idB,
		strings.NewReader(`{"till_id":"`+idB+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+bearerA)
	rec := httptest.NewRecorder()
	pmux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("own withdraw: expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if tillExists(t, primary, idA) {
		t.Fatal("the bearer's own till must be withdrawn")
	}
	if !tillExists(t, primary, idB) {
		t.Fatal("another till must never be withdrawn with a bearer that is not its own")
	}
	if rec := postWithdraw(t, pmux, bearerA); rec.Code != http.StatusUnauthorized {
		t.Fatalf("a withdrawn bearer must no longer authenticate, got %d", rec.Code)
	}
}

func TestSyncEnrollWithdraw_RefusesAnEstablishedTill(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	pmux, primary := newSyncAPITestDeps(t)
	id, bearer := enrolOnPrimary(t, pmux, "Till A")
	if _, err := primary.Db.Exec(`UPDATE tills SET enrolled_at = datetime('now', '-1 hour') WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if rec := postWithdraw(t, pmux, bearer); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a till enrolled an hour ago, got %d", rec.Code)
	}
	if !tillExists(t, primary, id) {
		t.Fatal("an established till is removed by the manager's revoke only")
	}
}

func TestSyncEnrollWithdraw_RefusedOnAReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	pmux, d := newSyncAPITestDeps(t)
	_, bearer := enrolOnPrimary(t, pmux, "Till A")
	if err := d.Settings.Set(t.Context(), "sync.primary_url", "http://192.168.1.10:8080"); err != nil {
		t.Fatal(err)
	}
	if rec := postWithdraw(t, pmux, bearer); rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 on a replica, got %d", rec.Code)
	}
}

func TestWithinJoinWithdrawWindow(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"2026-10-17 11:59:00", true},
		{"2026-10-17 11:45:00", true},
		{"2026-10-17 11:44:59", false},
		{"2026-10-17T11:58:00Z", true},
		{"2026-10-17T10:00:00Z", false},
		{"2026-10-17 12:30:00", false}, // from the future: clock skew beyond a minute
		{"", false},
		{"yesterday", false},
	} {
		if got := withinJoinWithdrawWindow(c.in, now); got != c.want {
			t.Errorf("withinJoinWithdrawWindow(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
