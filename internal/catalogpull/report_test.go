package catalogpull

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"unicode"
	"unicode/utf8"
)

// ut-docs#4493: the main till reports its whole current set of refused
// rows (cloud_apply_failures) to POST <endpoint>/v1/stores/catalog/
// apply-failures, only when the set changed since the last accepted report.

// fakePoster records every report; err, when set, fails the post.
type fakePoster struct {
	mu    sync.Mutex
	posts [][]Failure
	err   error
}

func (p *fakePoster) post(_ context.Context, fs []Failure) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var cp []Failure
	if fs != nil {
		cp = append(make([]Failure, 0, len(fs)), fs...)
	}
	p.posts = append(p.posts, cp)
	return p.err
}

func (p *fakePoster) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.posts)
}

func (p *fakePoster) last() []Failure {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.posts[len(p.posts)-1]
}

func (h *harness) addFailure(kind, cloudID string, version int64, reason, at string) {
	h.t.Helper()
	if _, err := h.db.Exec(`INSERT INTO cloud_apply_failures (entity, cloud_id, version, reason, at) VALUES (?, ?, ?, ?, ?)
ON CONFLICT(entity, cloud_id) DO UPDATE SET version = excluded.version, reason = excluded.reason, at = excluded.at`,
		kind, cloudID, version, reason, at); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) clearFailures() {
	h.t.Helper()
	if _, err := h.db.Exec(`DELETE FROM cloud_apply_failures`); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) report(p *fakePoster) (bool, error) {
	h.t.Helper()
	return ReportFailures(context.Background(), h.db, testScope, p.post)
}

// testScope is the store/endpoint scope the harness reports under.
const testScope = "store-1\nhttps://cloud.example"

func TestReportFailures_NothingEverRefused_NoPost(t *testing.T) {
	h := newHarness(t)
	p := &fakePoster{}
	sent, err := h.report(p)
	if err != nil || sent || p.count() != 0 {
		t.Fatalf("empty set, never reported: sent=%v err=%v posts=%d, want no post", sent, err, p.count())
	}
	if v := h.setting(KeyFailuresReported); v != "" {
		t.Fatalf("fingerprint stored without a post: %q", v)
	}
}

func TestReportFailures_PostsOnChange_NotWhenUnchanged(t *testing.T) {
	h := newHarness(t)
	p := &fakePoster{}
	h.addFailure("item", itemID(1), 41, "price above the ceiling", "2026-10-16T20:00:00Z")
	h.addFailure("category", cCat, 7, "unknown parent", "2026-10-16T19:00:00Z")

	sent, err := h.report(p)
	if err != nil || !sent || p.count() != 1 {
		t.Fatalf("first report: sent=%v err=%v posts=%d", sent, err, p.count())
	}
	want := []Failure{
		{Kind: "item", CloudID: itemID(1), Version: 41, Reason: "price above the ceiling", At: "2026-10-16T20:00:00Z"},
		{Kind: "category", CloudID: cCat, Version: 7, Reason: "unknown parent", At: "2026-10-16T19:00:00Z"},
	}
	if fmt.Sprint(p.last()) != fmt.Sprint(want) {
		t.Fatalf("posted %+v, want %+v", p.last(), want)
	}
	fp := h.setting(KeyFailuresReported)
	if fp == "" {
		t.Fatal("fingerprint not stored after an accepted post")
	}

	// Same set: no post.
	if sent, err := h.report(p); err != nil || sent || p.count() != 1 {
		t.Fatalf("unchanged set: sent=%v err=%v posts=%d, want no post", sent, err, p.count())
	}

	// A changed reason is a change.
	h.addFailure("item", itemID(1), 42, "price above the ceiling", "2026-10-16T21:00:00Z")
	if sent, err := h.report(p); err != nil || !sent || p.count() != 2 {
		t.Fatalf("changed set: sent=%v err=%v posts=%d", sent, err, p.count())
	}
	if h.setting(KeyFailuresReported) == fp {
		t.Fatal("fingerprint not updated after the changed set was accepted")
	}
}

func TestReportFailures_ClearPostsEmptySet(t *testing.T) {
	h := newHarness(t)
	p := &fakePoster{}
	h.addFailure("item", itemID(1), 41, "price above the ceiling", "2026-10-16T20:00:00Z")
	if sent, err := h.report(p); err != nil || !sent {
		t.Fatalf("first report: sent=%v err=%v", sent, err)
	}
	h.clearFailures()
	sent, err := h.report(p)
	if err != nil || !sent || p.count() != 2 {
		t.Fatalf("cleared set: sent=%v err=%v posts=%d, want an empty post", sent, err, p.count())
	}
	if got := p.last(); got == nil || len(got) != 0 {
		t.Fatalf("clear posted %#v, want a non-nil empty set", got)
	}
	// The cleared state is now what the cloud holds: no repeat post.
	if sent, _ := h.report(p); sent || p.count() != 2 {
		t.Fatalf("empty set re-posted: posts=%d", p.count())
	}
}

func TestReportFailures_PostErrorKeepsFingerprint(t *testing.T) {
	h := newHarness(t)
	p := &fakePoster{}
	h.addFailure("item", itemID(1), 41, "price above the ceiling", "2026-10-16T20:00:00Z")
	if _, err := h.report(p); err != nil {
		t.Fatal(err)
	}
	fp := h.setting(KeyFailuresReported)

	h.addFailure("item", itemID(2), 43, "colour not in the palette", "2026-10-16T21:00:00Z")
	p.err = &ReportStatusError{Status: http.StatusServiceUnavailable, Code: "apply_failures_unavailable"}
	sent, err := h.report(p)
	if err == nil || sent {
		t.Fatalf("failed post: sent=%v err=%v, want an error", sent, err)
	}
	if got := h.setting(KeyFailuresReported); got != fp {
		t.Fatalf("fingerprint moved on a failed post: %q → %q", fp, got)
	}
	// Next tick retries the same set.
	p.err = nil
	if sent, err := h.report(p); err != nil || !sent || p.count() != 3 {
		t.Fatalf("retry: sent=%v err=%v posts=%d", sent, err, p.count())
	}
}

func TestReportFailures_CapsAt500NewestFirst(t *testing.T) {
	h := newHarness(t)
	p := &fakePoster{}
	for i := 0; i < MaxReportedFailures+20; i++ {
		// Distinct seconds: i = 0 is the oldest.
		at := fmt.Sprintf("2026-10-16T%02d:%02d:%02dZ", i/3600, (i/60)%60, i%60)
		h.addFailure("item", itemID(i), int64(i+1), "refused", at)
	}
	if sent, err := h.report(p); err != nil || !sent {
		t.Fatalf("report: sent=%v err=%v", sent, err)
	}
	got := p.last()
	if len(got) != MaxReportedFailures {
		t.Fatalf("posted %d failures, want %d", len(got), MaxReportedFailures)
	}
	if got[0].CloudID != itemID(MaxReportedFailures+19) || got[len(got)-1].CloudID != itemID(20) {
		t.Fatalf("posted %s … %s, want the newest 500 newest first", got[0].CloudID, got[len(got)-1].CloudID)
	}
}

func TestReportFailures_SkipsEntriesTheCloudWouldRefuse(t *testing.T) {
	h := newHarness(t)
	p := &fakePoster{}
	long := strings.Repeat("é", 600)
	h.addFailure("item", itemID(1), 41, "price above the ceiling", "2026-10-16T20:00:00Z")
	h.addFailure("item", "invalid", 2, "x", "2026-10-16T20:00:01Z")                              // boundID's placeholder
	h.addFailure("gizmo", itemID(2), 2, "x", "2026-10-16T20:00:02Z")                             // unknown kind
	h.addFailure("category", cCat, 0, "x", "2026-10-16T20:00:03Z")                               // version < 1
	h.addFailure("tax_code", cTax, 3, "bad\x00\x07rate", "2026-10-16T20:00:04Z")                 // control chars stripped
	h.addFailure("item", "00000000-0000-0000-0000-000000000000", 2, "x", "2026-10-16T20:00:05Z") // nil UUID
	h.addFailure("item", itemID(3), 2, "bad \uFFFD byte", "2026-10-16T20:00:06Z")                // U+FFFD → '?'
	h.addFailure("item", itemID(4), 2, "\u200b", "2026-10-16T20:00:07Z")                         // empty once Cf is stripped
	h.addFailure("item", itemID(5), 2, long, "2026-10-16T20:00:08Z")                             // 600 runes → 500
	h.addFailure("item", itemID(6), 2, "x", "yesterday")                                         // bad at
	h.addFailure("item", itemID(7), 2, "\u202eevil\u200d", "2026-10-16T20:00:09Z")               // bidi/zero-width stripped
	h.addFailure("item", strings.ToUpper(itemID(8)), 2, "upper", "2026-10-16T20:00:10Z")         // any hex case, sent lower-case
	if _, err := h.report(p); err != nil {
		t.Fatal(err)
	}
	got := p.last()
	want := map[string]string{
		cTax: "badrate", itemID(1): "price above the ceiling", itemID(3): "bad ? byte",
		itemID(5): strings.Repeat("é", 500), itemID(7): "evil", itemID(8): "upper",
	}
	if len(got) != len(want) {
		t.Fatalf("posted %d entries %+v, want %d", len(got), got, len(want))
	}
	for _, f := range got {
		if r, ok := want[f.CloudID]; !ok || r != f.Reason {
			t.Fatalf("posted %s reason %q, want %q (in set: %v)", f.CloudID, f.Reason, r, ok)
		}
		// What is sent passes the cloud's own check unchanged.
		if c, ok := cloudCleanReason(f.Reason); !ok || c != f.Reason {
			t.Fatalf("cloud would refuse or alter %q: %q %v", f.Reason, c, ok)
		}
	}
}

// cloudCleanReason is ut-cloud's cleanApplyFailureReason
// (internal/httpapi/handlers/stores_catalog_apply_failures.go), copied.
func cloudCleanReason(s string) (string, bool) {
	if n := utf8.RuneCountInString(s); n < 1 || n > 500 || strings.ContainsRune(s, utf8.RuneError) {
		return "", false
	}
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return -1
		}
		return r
	}, s))
	return s, s != ""
}

// The fingerprint covers the store and endpoint: the same set under a new
// scope (re-enrolled into another store) is posted again.
func TestReportFailures_NewScopePostsTheSameSet(t *testing.T) {
	h := newHarness(t)
	p := &fakePoster{}
	h.addFailure("item", itemID(1), 41, "price above the ceiling", "2026-10-16T20:00:00Z")
	if sent, err := h.report(p); err != nil || !sent {
		t.Fatalf("first: sent=%v err=%v", sent, err)
	}
	if !strings.HasPrefix(h.setting(KeyFailuresReported), "v2:") {
		t.Fatalf("fingerprint %q, want the v2: prefix", h.setting(KeyFailuresReported))
	}
	sent, err := ReportFailures(context.Background(), h.db, "store-2\nhttps://cloud.example", p.post)
	if err != nil || !sent || p.count() != 2 {
		t.Fatalf("new scope: sent=%v err=%v posts=%d, want a post", sent, err, p.count())
	}
}

// A 400 means the cloud refuses this body: it is not re-posted until the
// set changes.
func TestReportFailures_RejectedSetWaitsForAChange(t *testing.T) {
	h := newHarness(t)
	p := &fakePoster{err: &ReportRejectedError{Status: http.StatusBadRequest, Code: "invalid_request"}}
	h.addFailure("item", itemID(1), 41, "price above the ceiling", "2026-10-16T20:00:00Z")
	var rej *ReportRejectedError
	if sent, err := h.report(p); sent || !errors.As(err, &rej) {
		t.Fatalf("rejected: sent=%v err=%v", sent, err)
	}
	if h.setting(KeyFailuresReported) != "" {
		t.Fatal("a rejected set was recorded as accepted")
	}
	if sent, err := h.report(p); sent || err != nil || p.count() != 1 {
		t.Fatalf("same rejected set: sent=%v err=%v posts=%d, want no re-post", sent, err, p.count())
	}
	p.err = nil
	h.addFailure("item", itemID(2), 42, "unknown category", "2026-10-16T21:00:00Z")
	if sent, err := h.report(p); !sent || err != nil || p.count() != 2 {
		t.Fatalf("changed set: sent=%v err=%v posts=%d, want a post", sent, err, p.count())
	}
}

// fakeReportEndpoint is an httptest stand-in for ut-cloud's POST
// /api/v1/stores/catalog/apply-failures.
type fakeReportEndpoint struct {
	mu     sync.Mutex
	status int
	code   string
	method string
	path   string
	auth   string
	ctype  string
	body   []byte
}

func (f *fakeReportEndpoint) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.method, f.path, f.auth, f.ctype = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
	f.body, _ = io.ReadAll(r.Body)
	if f.status != 0 {
		writeErr(w, f.status, f.code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"data":{"stored":1},"error":null}`)
}

func TestHTTPReporter_Request(t *testing.T) {
	ep := &fakeReportEndpoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	post := NewHTTPReporter(srv.Client(), srv.URL+"/", "store-1", "tok-1")
	fs := []Failure{{Kind: "item", CloudID: itemID(1), Version: 41, Reason: "price above the ceiling", At: "2026-10-16T20:00:00Z"}}
	if err := post(context.Background(), fs); err != nil {
		t.Fatal(err)
	}
	if ep.method != http.MethodPost || ep.path != "/v1/stores/catalog/apply-failures" {
		t.Fatalf("request %s %s", ep.method, ep.path)
	}
	if ep.auth != "Bearer tok-1" || !strings.HasPrefix(ep.ctype, "application/json") {
		t.Fatalf("headers: auth=%q content-type=%q", ep.auth, ep.ctype)
	}
	want := `{"store_id":"store-1","failures":[{"kind":"item","cloud_id":"` + itemID(1) + `","version":41,"reason":"price above the ceiling","at":"2026-10-16T20:00:00Z"}]}`
	if strings.TrimSpace(string(ep.body)) != want {
		t.Fatalf("body\n got %s\nwant %s", ep.body, want)
	}

	// A clear is an explicit empty array, never null.
	if err := post(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(ep.body)); got != `{"store_id":"store-1","failures":[]}` {
		t.Fatalf("clear body %s", got)
	}
}

func TestHTTPReporter_Errors(t *testing.T) {
	const secret = "tok-secret-4493"
	for _, tc := range []struct {
		status int
		code   string
		stop   bool
		rej    bool
	}{
		{http.StatusBadRequest, "invalid_request", false, true},
		{http.StatusUnauthorized, "unauthorized", true, false},
		{http.StatusForbidden, "device_credential_required", true, false},
		{http.StatusConflict, "", true, false},
		{http.StatusServiceUnavailable, "apply_failures_unavailable", false, false},
	} {
		ep := &fakeReportEndpoint{status: tc.status, code: tc.code}
		srv := httptest.NewServer(ep)
		post := NewHTTPReporter(srv.Client(), srv.URL, "store-1", secret)
		err := post(context.Background(), []Failure{})
		srv.Close()
		if err == nil {
			t.Fatalf("%d: no error", tc.status)
		}
		var stop *ReportStopError
		var st *ReportStatusError
		var rej *ReportRejectedError
		switch {
		case tc.stop && !errors.As(err, &stop):
			t.Fatalf("%d: %T %v, want *ReportStopError", tc.status, err, err)
		case tc.rej && !errors.As(err, &rej):
			t.Fatalf("%d: %T %v, want *ReportRejectedError", tc.status, err, err)
		case !tc.stop && !tc.rej && !errors.As(err, &st):
			t.Fatalf("%d: %T %v, want *ReportStatusError", tc.status, err, err)
		}
		if tc.code != "" && !strings.Contains(err.Error(), tc.code) {
			t.Fatalf("%d: error %q lacks code %q", tc.status, err, tc.code)
		}
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("%d: token leaked into the error: %q", tc.status, err)
		}
	}

	// A transport failure does not carry the token either.
	post := NewHTTPReporter(http.DefaultClient, "http://127.0.0.1:1", "store-1", secret)
	err := post(context.Background(), []Failure{})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("transport error %v", err)
	}

	// ReportFailures passes the error through without the token.
	h := newHarness(t)
	ep := &fakeReportEndpoint{status: http.StatusServiceUnavailable, code: "apply_failures_unavailable"}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h.addFailure("item", itemID(1), 41, "x", "2026-10-16T20:00:00Z")
	_, err = ReportFailures(context.Background(), h.db, testScope, NewHTTPReporter(srv.Client(), srv.URL, "store-1", secret))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("ReportFailures error %v", err)
	}
}

// The posted JSON is what the end-to-end report path produces.
func TestReportFailures_EndToEndJSON(t *testing.T) {
	h := newHarness(t)
	ep := &fakeReportEndpoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	h.addFailure("item", itemID(1), 41, "price above the ceiling", "2026-10-16T20:00:00Z")
	sent, err := ReportFailures(context.Background(), h.db, testScope, NewHTTPReporter(srv.Client(), srv.URL, "store-1", "tok-1"))
	if err != nil || !sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	var body struct {
		StoreID  string            `json:"store_id"`
		Failures []json.RawMessage `json:"failures"`
	}
	if err := json.Unmarshal(ep.body, &body); err != nil || body.StoreID != "store-1" || len(body.Failures) != 1 {
		t.Fatalf("body %s: %v", ep.body, err)
	}
}

// The cloud refuses a body over 256 KiB as a whole; the oldest entries wait
// rather than the whole report failing forever.
func TestReportFailures_FitsTheBodyLimit(t *testing.T) {
	h := newHarness(t)
	ep := &fakeReportEndpoint{}
	srv := httptest.NewServer(ep)
	defer srv.Close()
	long := strings.Repeat("😀", 300) // 1200 bytes
	for i := 0; i < MaxReportedFailures; i++ {
		at := fmt.Sprintf("2026-10-16T%02d:%02d:%02dZ", i/3600, (i/60)%60, i%60)
		h.addFailure("item", itemID(i), int64(i+1), long, at)
	}
	sent, err := ReportFailures(context.Background(), h.db, testScope, NewHTTPReporter(srv.Client(), srv.URL, "store-1", "tok-1"))
	if err != nil || !sent {
		t.Fatalf("sent=%v err=%v", sent, err)
	}
	if len(ep.body) > 256<<10 {
		t.Fatalf("body %d bytes, over the cloud's limit", len(ep.body))
	}
	var body struct {
		Failures []Failure `json:"failures"`
	}
	if err := json.Unmarshal(ep.body, &body); err != nil {
		t.Fatal(err)
	}
	if n := len(body.Failures); n == 0 || n >= MaxReportedFailures || body.Failures[0].CloudID != itemID(MaxReportedFailures-1) {
		t.Fatalf("posted %d entries, want the newest that fit", n)
	}
}
