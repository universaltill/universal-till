package pages

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	appdb "github.com/universaltill/universal-till/internal/db"
)

// ut-docs#3219: the manual pairing path used to need the whole ~90-char
// base64url code typed on the new till. The main till now also shows a short
// one-time code (XXX-XXX) plus its own address, which is what a person types.

// --- pure logic: generation, format, normalisation ---

func TestShortCode_AlphabetLengthAndFormat(t *testing.T) {
	e := &enrolTokens{tokens: map[string]time.Time{}}
	seen := map[string]bool{}
	for i := 0; i < 300; i++ {
		long, short := e.issueWithShort()
		if long == "" {
			t.Fatal("expected a long token alongside the short code")
		}
		if len(short) != shortCodeLen {
			t.Fatalf("short code %q has length %d, want %d", short, len(short), shortCodeLen)
		}
		for _, c := range short {
			if !strings.ContainsRune(shortCodeAlphabet, c) {
				t.Fatalf("short code %q has %q, which is outside the alphabet %q", short, c, shortCodeAlphabet)
			}
		}
		if seen[short] {
			t.Fatalf("short code %q issued twice while both are live", short)
		}
		seen[short] = true
	}
	if shortCodeAlphabet != "23456789ABCDEFGHJKLMNPQRSTUVWXYZ" || len(shortCodeAlphabet) != 32 {
		t.Fatalf("alphabet changed: %q", shortCodeAlphabet)
	}
	if got := formatShortCode("K7P4XQ"); got != "K7P-4XQ" {
		t.Fatalf("formatShortCode = %q, want K7P-4XQ", got)
	}
}

func TestNormaliseShortCode(t *testing.T) {
	ok := map[string]string{
		"K7P-4XQ":     "K7P4XQ",
		"k7p-4xq":     "K7P4XQ",
		" k7p 4xq ":   "K7P4XQ",
		"K7P4XQ":      "K7P4XQ",
		"k7p–4xq":     "K7P4XQ", // an en dash from a phone keyboard
		"K 7 P - 4XQ": "K7P4XQ",
	}
	for in, want := range ok {
		got, valid := normaliseShortCode(in)
		if !valid || got != want {
			t.Errorf("normaliseShortCode(%q) = %q, %v; want %q, true", in, got, valid, want)
		}
	}
	for _, bad := range []string{
		"", "K7P4X", "K7P4XQZ", "K7P-4XO", // O is not in the alphabet
		"K7P-4X1", "K7P-4XI", "K7P-4X0", // nor 1, I, 0
		"0123456789abcdef0123456789abcdef", // a long hex token
		"never-issued",
	} {
		if got, valid := normaliseShortCode(bad); valid {
			t.Errorf("normaliseShortCode(%q) = %q, true; want invalid", bad, got)
		}
	}
}

// --- pure logic: one use, expiry, twin burning, failure budget ---

func TestEnrolTokens_ShortCodeIsOneTimeAndBurnsLongTwin(t *testing.T) {
	e := &enrolTokens{tokens: map[string]time.Time{}}
	long, short := e.issueWithShort()
	if !e.consumeShort(short) {
		t.Fatal("expected the freshly issued short code to be accepted")
	}
	if e.consumeShort(short) {
		t.Fatal("a used short code must be refused")
	}
	if e.consume(long) {
		t.Fatal("using the short code must burn its long twin")
	}

	long2, short2 := e.issueWithShort()
	if !e.consume(long2) {
		t.Fatal("expected the freshly issued long token to be accepted")
	}
	if e.consumeShort(short2) {
		t.Fatal("using the long token must burn its short twin")
	}
}

func TestEnrolTokens_ExpiredShortCodeRefused(t *testing.T) {
	e := &enrolTokens{tokens: map[string]time.Time{}}
	long, short := e.issueWithShort()
	e.mu.Lock()
	e.tokens[long] = time.Now().Add(-time.Second)
	e.mu.Unlock()
	if e.consumeShort(short) {
		t.Fatal("an expired short code must be refused")
	}
}

func TestEnrolTokens_ShortCodeFailureBudget(t *testing.T) {
	e := &enrolTokens{tokens: map[string]time.Time{}}
	long, short := e.issueWithShort()
	wrong := wrongShortCode(short)
	for i := 0; i < shortCodeFailureBudget-1; i++ {
		if e.consumeShort(wrong) {
			t.Fatal("a wrong short code must be refused")
		}
	}
	// One under the budget: the live code still works.
	if !e.consumeShort(short) {
		t.Fatal("the live code must survive failures below the budget")
	}

	long, short = e.issueWithShort()
	for i := 0; i < shortCodeFailureBudget; i++ {
		_ = e.consumeShort(wrong)
	}
	if e.consumeShort(short) {
		t.Fatal("hitting the failure budget must invalidate every live short code")
	}
	// Only the guessable half dies: a stranger flooding wrong short codes
	// must not also kill QR / full-code pairing (review finding).
	if !e.consume(long) {
		t.Fatal("hitting the failure budget must leave the long twin usable")
	}

	// The counter reset with the purge: a fresh code survives another
	// budget-minus-one failures.
	_, short = e.issueWithShort()
	for i := 0; i < shortCodeFailureBudget-1; i++ {
		_ = e.consumeShort(wrong)
	}
	if !e.consumeShort(short) {
		t.Fatal("the failure counter must reset after a purge")
	}
}

func TestEnrolTokens_IssuingResetsFailureCounter(t *testing.T) {
	e := &enrolTokens{tokens: map[string]time.Time{}}
	_, first := e.issueWithShort()
	wrong := wrongShortCode(first)
	for i := 0; i < shortCodeFailureBudget-1; i++ {
		_ = e.consumeShort(wrong)
	}
	_, second := e.issueWithShort() // resets the counter
	for i := 0; i < shortCodeFailureBudget-1; i++ {
		_ = e.consumeShort(wrongShortCode(second))
	}
	if !e.consumeShort(second) {
		t.Fatal("issuing a new code must reset the shop-wide failure counter")
	}
}

// One live short code per shop: a reset budget must never also give a
// guesser fresh tries against codes shown earlier (review finding).
func TestEnrolTokens_NewShortCodeRetiresOlderOnes(t *testing.T) {
	e := &enrolTokens{tokens: map[string]time.Time{}}
	firstLong, first := e.issueWithShort()
	_, second := e.issueWithShort()
	if first == second {
		t.Skip("random collision")
	}
	if e.consumeShort(first) {
		t.Fatal("an older short code must be retired when a new one is issued")
	}
	if !e.consume(firstLong) {
		t.Fatal("retiring the older short code must leave its long twin (QR) usable")
	}
	if !e.consumeShort(second) {
		t.Fatal("the newest short code must work")
	}
}

// A known code that merely expired is a person re-typing a stale code, not
// a guess: it must not spend the shop-wide budget (review finding).
func TestEnrolTokens_ExpiredShortCodeDoesNotSpendBudget(t *testing.T) {
	e := &enrolTokens{tokens: map[string]time.Time{}}
	long, short := e.issueWithShort()
	e.tokens[long] = time.Now().Add(-time.Second)
	if e.consumeShort(short) {
		t.Fatal("an expired short code must be refused")
	}
	if e.shortFailures != 0 {
		t.Fatalf("an expired real code must not count as a failed guess, failures = %d", e.shortFailures)
	}
}

func TestEnrolTokens_LongTokenFailuresDontSpendBudget(t *testing.T) {
	e := &enrolTokens{tokens: map[string]time.Time{}}
	_, short := e.issueWithShort()
	for i := 0; i < 3*shortCodeFailureBudget; i++ {
		if e.consume("never-issued-long-token") {
			t.Fatal("an unknown long token must be refused")
		}
	}
	if !e.consumeShort(short) {
		t.Fatal("failed long-token attempts must not spend the short-code budget")
	}
}

// wrongShortCode returns a valid-shaped short code that differs from c.
func wrongShortCode(c string) string {
	if c == "222222" {
		return "333333"
	}
	return "222222"
}

// --- HTTP: POST /api/sync/enroll-token renders the short code + address ---

var (
	shortCodeHTML = regexp.MustCompile(`class="pairing-short-code"[^>]*>([^<]+)<`)
	addressHTML   = regexp.MustCompile(`class="pairing-address"[^>]*>([^<]+)<`)
)

// issueShortCode drives the primary's POST /api/sync/enroll-token and returns
// the displayed short code (XXX-XXX), the displayed address and the long code.
func issueShortCode(t *testing.T, mux http.Handler, primaryURL string) (short, address, long string) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll-token",
		strings.NewReader("url="+url.QueryEscape(primaryURL)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("issue enrol token: code %d body %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	m := shortCodeHTML.FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no short code in the enroll-token response: %s", body)
	}
	a := addressHTML.FindStringSubmatch(body)
	if a == nil {
		t.Fatalf("no address in the enroll-token response: %s", body)
	}
	return m[1], a[1], issueEnrolCodeFromBody(t, body)
}

func issueEnrolCodeFromBody(t *testing.T, body string) string {
	t.Helper()
	marker := `<code style="user-select:all">`
	start := strings.Index(body, marker)
	if start == -1 {
		t.Fatalf("could not find the long enrol code in %q", body)
	}
	start += len(marker)
	end := strings.Index(body[start:], "</code>")
	if end == -1 {
		t.Fatalf("could not find closing </code> in %q", body)
	}
	return body[start : start+end]
}

func TestSyncEnrollToken_ShowsShortCodeAndAddress(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)

	short, addr, long := issueShortCode(t, mux, "http://192.168.1.10:8080")
	if !regexp.MustCompile(`^[2-9A-HJ-NP-Z]{3}-[2-9A-HJ-NP-Z]{3}$`).MatchString(short) {
		t.Fatalf("short code %q is not displayed as XXX-XXX", short)
	}
	if addr != "192.168.1.10:8080" {
		t.Fatalf("address = %q, want the bare host:port for an http primary", addr)
	}
	// The QR/long code still carries the full URL + the long token.
	u, tok, err := decodeEnrollCode(long)
	if err != nil || u != "http://192.168.1.10:8080" || tok == "" {
		t.Fatalf("long code did not decode: url=%q tok=%q err=%v", u, tok, err)
	}

	// An https primary keeps its scheme: typing the bare host would make the
	// joining till dial plain http.
	_, addr, _ = issueShortCode(t, mux, "https://till.example:8443")
	if addr != "https://till.example:8443" {
		t.Fatalf("address = %q, want the full https URL", addr)
	}
}

func TestSyncEnrollToken_LongCodeIsInADisclosure(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll-token",
		strings.NewReader("url=http://192.168.1.10:8080"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	body := rec.Body.String()
	d := strings.Index(body, "<details")
	c := strings.Index(body, `<code style="user-select:all">`)
	e := strings.Index(body, "</details>")
	if d == -1 || c == -1 || e == -1 || !(d < c && c < e) {
		t.Fatalf("the long code must sit inside a <details> disclosure: %s", body)
	}
	if !strings.Contains(body, "<img") {
		t.Fatalf("the QR must still be shown: %s", body)
	}
}

// --- HTTP: POST /api/sync/enroll accepts the short code ---

func enrollWith(mux http.Handler, token, name, remote string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]string{"token": token, "name": name})
	req := httptest.NewRequest(http.MethodPost, "/api/sync/enroll", strings.NewReader(string(b)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestSyncEnroll_ShortCodeEnrolsAndBurnsLongTwin(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)
	short, _, long := issueShortCode(t, mux, "http://192.168.1.10:8080")

	// Typed in lowercase with a space instead of the dash.
	typed := strings.ToLower(strings.Replace(short, "-", " ", 1))
	rec := enrollWith(mux, typed, "Till 2", "10.0.0.2:5000")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 enrolling with the short code, got %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Bearer string `json:"bearer"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Data.Bearer == "" {
		t.Fatalf("expected a bearer back, got %s", rec.Body.String())
	}

	if rec := enrollWith(mux, short, "Till 3", "10.0.0.3:5000"); rec.Code != http.StatusForbidden {
		t.Fatalf("a used short code must be refused with 403, got %d", rec.Code)
	}
	_, tok, _ := decodeEnrollCode(long)
	if rec := enrollWith(mux, tok, "Till 3", "10.0.0.3:5000"); rec.Code != http.StatusForbidden {
		t.Fatalf("the long twin of a used short code must be refused with 403, got %d", rec.Code)
	}
}

func TestSyncEnroll_LongTokenBurnsShortTwin(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)
	short, _, long := issueShortCode(t, mux, "http://192.168.1.10:8080")
	_, tok, _ := decodeEnrollCode(long)
	if rec := enrollWith(mux, tok, "Till 2", "10.0.0.2:5000"); rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with the long token, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := enrollWith(mux, short, "Till 3", "10.0.0.3:5000"); rec.Code != http.StatusForbidden {
		t.Fatalf("the short twin of a used long token must be refused with 403, got %d", rec.Code)
	}
}

func TestSyncEnroll_ShortCodeRateLimitedPerSource(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)
	short, _, _ := issueShortCode(t, mux, "http://192.168.1.10:8080")
	wrong := wrongShortCode(strings.ReplaceAll(short, "-", ""))

	for i := 0; i < 5; i++ {
		if rec := enrollWith(mux, wrong, "Till 2", "10.0.0.9:4000"); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: expected 403 for a wrong short code, got %d", i+1, rec.Code)
		}
	}
	// Sixth attempt from the same source within the minute — even with the
	// right code — is throttled, and does not burn the code.
	if rec := enrollWith(mux, short, "Till 2", "10.0.0.9:4001"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 on the 6th short-code attempt from one source, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec := enrollWith(mux, short, "Till 2", "10.0.0.10:4000"); rec.Code != http.StatusOK {
		t.Fatalf("a throttled attempt must not burn the code; another source got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSyncEnroll_ShortCodeFailureBudgetInvalidatesLiveCode(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)
	short, _, long := issueShortCode(t, mux, "http://192.168.1.10:8080")
	wrong := wrongShortCode(strings.ReplaceAll(short, "-", ""))

	// Ten failures spread over sources, so no per-source limit is hit.
	for i := 0; i < shortCodeFailureBudget; i++ {
		remote := "10.0.1." + string(rune('1'+i%5)) + ":4000"
		if rec := enrollWith(mux, wrong, "Till 2", remote); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: expected 403, got %d", i+1, rec.Code)
		}
	}
	if rec := enrollWith(mux, short, "Till 2", "10.0.2.1:4000"); rec.Code != http.StatusForbidden {
		t.Fatalf("after the shop-wide failure budget the right code must fail too, got %d", rec.Code)
	}
	_, tok, _ := decodeEnrollCode(long)
	if rec := enrollWith(mux, tok, "Till 2", "10.0.2.1:4000"); rec.Code != http.StatusOK {
		t.Fatalf("the failure budget must leave the long twin (QR) usable, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSyncEnroll_LongTokenFailuresNotThrottledNorBudgeted(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)
	short, _, _ := issueShortCode(t, mux, "http://192.168.1.10:8080")
	for i := 0; i < 2*shortCodeFailureBudget; i++ {
		if rec := enrollWith(mux, "never-issued-long-token", "Till 2", "10.0.0.9:4000"); rec.Code != http.StatusForbidden {
			t.Fatalf("attempt %d: a bad long token must stay a plain 403, got %d", i+1, rec.Code)
		}
	}
	if rec := enrollWith(mux, short, "Till 2", "10.0.0.9:4000"); rec.Code != http.StatusOK {
		t.Fatalf("failed long-token attempts must neither throttle nor spend the budget, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- joinPrimary: short code + typed address ---

func joinKind(t *testing.T, err error) joinErrKind {
	t.Helper()
	var je *joinError
	if !errors.As(err, &je) {
		t.Fatalf("expected a *joinError, got %v", err)
	}
	return je.kind
}

func TestJoinPrimary_ShortCodeNeedsAddress(t *testing.T) {
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	r := httptest.NewRequest(http.MethodPost, "/api/sync/join", nil)
	for _, addr := range []string{"", "   "} {
		_, err := joinPrimary(r, replica, "K7P-4XQ", addr, "Till 2")
		if k := joinKind(t, err); k != joinErrNeedAddress {
			t.Fatalf("address %q: kind = %v, want joinErrNeedAddress", addr, k)
		}
	}
}

func TestJoinPrimary_ShortCodeRejectsBadAddress(t *testing.T) {
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	r := httptest.NewRequest(http.MethodPost, "/api/sync/join", nil)
	for _, addr := range []string{
		"ftp://x", "http://x/path", "http://x?y=1", "http://u:p@x", "http://", "://x", "x y",
		"192.168.1.10:8080#", "192.168.1.10:8080?",
	} {
		_, err := joinPrimary(r, replica, "K7P-4XQ", addr, "Till 2")
		if k := joinKind(t, err); k != joinErrBadAddress {
			t.Errorf("address %q: kind = %v, want joinErrBadAddress", addr, k)
		}
	}
}

func TestJoinPrimary_GarbageCodeStillBadCode(t *testing.T) {
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	r := httptest.NewRequest(http.MethodPost, "/api/sync/join", nil)
	_, err := joinPrimary(r, replica, "not-a-code", "192.168.1.10:8080", "Till 2")
	if k := joinKind(t, err); k != joinErrBadCode {
		t.Fatalf("kind = %v, want joinErrBadCode", k)
	}
}

// End to end through two real tills: the short code and an address typed
// without a scheme (exactly what the main till displays) join the shop.
func TestSyncJoin_ShortCodeWithBareAddress_FullFlow(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	if err := primary.Settings.Set(t.Context(), "store.name", "Corner Shop"); err != nil {
		t.Fatalf("set store name: %v", err)
	}
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)
	srv := httptest.NewServer(pmux)
	t.Cleanup(srv.Close)

	short, addr, _ := issueShortCode(t, pmux, srv.URL)
	if addr != strings.TrimPrefix(srv.URL, "http://") {
		t.Fatalf("displayed address = %q, want %q", addr, strings.TrimPrefix(srv.URL, "http://"))
	}

	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)

	form := url.Values{"code": {strings.ToLower(short)}, "address": {addr + "/"}, "name": {"Till 2"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/join", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rmux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 joining with the short code, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Corner Shop") {
		t.Fatalf("expected the shop name back, got %q", rec.Body.String())
	}
	if !appdb.PendingRestore(replicaPath) {
		t.Fatal("expected a staged restore after a short-code join")
	}
	idRaw, err := os.ReadFile(appdb.ReplicaIdentityPath(replicaPath))
	if err != nil {
		t.Fatalf("expected a staged identity: %v", err)
	}
	var id appdb.ReplicaIdentity
	if err := json.Unmarshal(idRaw, &id); err != nil {
		t.Fatal(err)
	}
	if id.PrimaryURL != srv.URL {
		t.Fatalf("staged primary_url = %q, want %q", id.PrimaryURL, srv.URL)
	}
}

// The long code keeps working with no address at all (and an address typed
// alongside it is ignored).
func TestSyncJoin_LongCodeIgnoresAddress(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)
	srv := httptest.NewServer(pmux)
	t.Cleanup(srv.Close)
	_, _, long := issueShortCode(t, pmux, srv.URL)

	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)
	form := url.Values{"code": {long}, "address": {"ftp://ignored"}, "name": {"Till 2"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/join", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rmux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 joining with the long code, got %d: %s", rec.Code, rec.Body.String())
	}
	if !appdb.PendingRestore(replicaPath) {
		t.Fatal("expected a staged restore after a long-code join")
	}
}

func TestCompleteJoin_Maps429ToTooManyAttempts(t *testing.T) {
	stub := http.NewServeMux()
	stub.HandleFunc("POST /api/sync/enroll", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "slow down", http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	r := httptest.NewRequest(http.MethodPost, "/api/sync/join", nil)
	_, err := completeJoin(r, replica, srv.URL, "K7P4XQ", "Till 2")
	if k := joinKind(t, err); k != joinErrTooManyAttempts {
		t.Fatalf("kind = %v, want joinErrTooManyAttempts", k)
	}
	if appdb.PendingRestore(replicaPath) {
		t.Fatal("a throttled join must not stage a restore")
	}
}

func TestJoinForms_HaveAddressField(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newSyncAPITestDeps(t)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/tills", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /tills: %d %s", rec.Code, rec.Body.String())
	}
	assertJoinFormFields(t, "/tills", rec.Body.String())

	withOSLocale(t, "", "")
	smux, _, _ := newFullAuthDeps(t)
	rec = httptest.NewRecorder()
	smux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup: %d %s", rec.Code, rec.Body.String())
	}
	assertJoinFormFields(t, "/setup", rec.Body.String())
}

func assertJoinFormFields(t *testing.T, page, body string) {
	t.Helper()
	address := regexp.MustCompile(`<input[^>]*name="address"[^>]*>`).FindString(body)
	if address == "" {
		t.Fatalf("%s: join form has no address field", page)
	}
	for _, want := range []string{`inputmode="url"`, `autocapitalize="off"`, `autocomplete="off"`, `spellcheck="false"`} {
		if !strings.Contains(address, want) {
			t.Errorf("%s: address field %s lacks %s", page, address, want)
		}
	}
	code := regexp.MustCompile(`<input[^>]*name="code"[^>]*>`).FindString(body)
	for _, want := range []string{`autocapitalize="characters"`, `autocomplete="off"`, "K7P-4XQ"} {
		if !strings.Contains(code, want) {
			t.Errorf("%s: code field %s lacks %s", page, code, want)
		}
	}
}
