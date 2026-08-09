package pages

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// --- registerPairingJoinAPI: the replica-side "select a discovered primary
// -> pair" flow (ADR-0033 part 3/3, ut-docs#185). ---

func postPairStart(t *testing.T, mux *http.ServeMux, baseURL, tillID, name string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"base_url": {baseURL}, "till_id": {tillID}, "name": {name}}
	req := httptest.NewRequest(http.MethodPost, "/api/sync/pair-start", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func getPairStatus(t *testing.T, mux *http.ServeMux) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/sync/pair-status", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func TestPairStart_RequiresManager(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairStart(t, rmux, "http://example.invalid", "primary-till-1", "Till 2")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPairStart_SendsRequestAndShowsIndependentlyDerivedCode(t *testing.T) {
	t.Setenv("UT_AUTH", "off")

	// Real primary serving the pairing backend (#184).
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	psvc := auth.NewService(primary.Db)
	pmux := http.NewServeMux()
	ptokens := registerSyncAPI(pmux, primary)
	registerPairingAPI(pmux, primary, psvc, ptokens)
	srv := httptest.NewServer(pmux)
	t.Cleanup(srv.Close)

	primaryTillID, err := discovery.TillID(t.Context(), data.NewSettingsRepo(primary.Db))
	if err != nil {
		t.Fatalf("discovery.TillID: %v", err)
	}

	// Replica driving POST /api/sync/pair-start against the real primary.
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairStart(t, rmux, srv.URL, primaryTillID, "Bar Till")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 starting a pair request, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The primary must actually have a pending request now.
	preq := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests", nil)
	prec := httptest.NewRecorder()
	pmux.ServeHTTP(prec, preq)
	var pout struct {
		Data struct {
			Pending []struct {
				VerificationCode string `json:"verification_code"`
			} `json:"pending"`
		} `json:"data"`
	}
	if err := json.Unmarshal(prec.Body.Bytes(), &pout); err != nil {
		t.Fatal(err)
	}
	if len(pout.Data.Pending) != 1 {
		t.Fatalf("expected exactly one pending request on the primary, got %+v", pout.Data.Pending)
	}

	// The replica's own rendered waiting screen must show the SAME code,
	// independently computed — not echoed by the primary (the primary's
	// pair-request response never even carries the primary's derivation).
	wantCode := pout.Data.Pending[0].VerificationCode
	if !strings.Contains(body, wantCode) {
		t.Fatalf("expected the replica's waiting screen to show verification code %q, got body: %s", wantCode, body)
	}
}

func TestPairStart_SurfacesUnreachablePrimary(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairStart(t, rmux, "http://127.0.0.1:1", "some-till-id", "Bar Till")
	// Always 200 — htmx (vendored 1.9.12 here) does not swap non-2xx
	// responses by default, so a non-200 render would be invisible to the
	// manager. The failure is encoded in the body as the "error" state
	// instead (no self-polling hx-trigger, so it doesn't loop forever).
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 (error encoded in the body, not the status) when the primary is unreachable, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "cannot reach that primary") {
		t.Fatalf("expected the rendered error state naming the failure, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hx-trigger") {
		t.Fatal("error state must not keep polling")
	}
}

// TestPairStart_RejectsInvalidBaseURL guards the base_url validation added
// after independent review flagged it as unvalidated external input
// (base_url originates from an untrusted LAN mDNS responder — CLAUDE.md:
// "validate all external input").
func TestPairStart_RejectsInvalidBaseURL(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	for _, bad := range []string{"javascript:alert(1)", "not a url", "ftp://example.com"} {
		rec := postPairStart(t, rmux, bad, "some-till-id", "Bar Till")
		if rec.Code != http.StatusOK {
			t.Fatalf("base_url=%q: expected 200 (error encoded in body), got %d", bad, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "not a valid primary address") {
			t.Fatalf("base_url=%q: expected the rendered validation-error state, got: %s", bad, rec.Body.String())
		}
	}
}

func TestPairStatus_NoActiveAttemptRendersSafely(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := getPairStatus(t, rmux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 even with no active pairing attempt, got %d: %s", rec.Code, rec.Body.String())
	}
	// Checking only the status code can't tell "safely renders nothing" apart
	// from "renders a bogus waiting state that polls forever" — both are 200.
	// A no-active-attempt render must not carry the self-polling trigger.
	if strings.Contains(rec.Body.String(), "hx-trigger") {
		t.Fatal("no active attempt must not render a polling state")
	}
}

// TestPairStatus_FullFlow_CompletesJoinAndStagesRestore is the actual
// end-to-end path this card exists to build: discover -> pair-start ->
// primary manager approves -> replica polls pair-status -> completeJoin
// stages a restore + identity, exactly like the QR flow already does.
func TestPairStatus_FullFlow_CompletesJoinAndStagesRestore(t *testing.T) {
	t.Setenv("UT_AUTH", "off")

	primary, _ := newSyncDepsWithPath(t, "primary.db")
	if err := primary.Settings.Set(t.Context(), "store.name", "Corner Shop"); err != nil {
		t.Fatalf("set store name: %v", err)
	}
	psvc := auth.NewService(primary.Db)
	pmux := http.NewServeMux()
	ptokens := registerSyncAPI(pmux, primary)
	registerPairingAPI(pmux, primary, psvc, ptokens)
	srv := httptest.NewServer(pmux)
	t.Cleanup(srv.Close)

	primaryTillID, err := discovery.TillID(t.Context(), data.NewSettingsRepo(primary.Db))
	if err != nil {
		t.Fatalf("discovery.TillID: %v", err)
	}

	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairStart(t, rmux, srv.URL, primaryTillID, "Bar Till")
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-start: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Still waiting: the primary hasn't approved yet.
	rec = getPairStatus(t, rmux)
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-status (waiting): expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if appdb.PendingRestore(replicaPath) {
		t.Fatal("must not stage a restore before the primary approves")
	}

	// The manager approves on the primary.
	preq := httptest.NewRequest(http.MethodGet, "/api/sync/pair-requests", nil)
	prec := httptest.NewRecorder()
	pmux.ServeHTTP(prec, preq)
	var pout struct {
		Data struct {
			Pending []struct {
				ID string `json:"id"`
			} `json:"pending"`
		} `json:"data"`
	}
	if err := json.Unmarshal(prec.Body.Bytes(), &pout); err != nil {
		t.Fatal(err)
	}
	if len(pout.Data.Pending) != 1 {
		t.Fatalf("expected one pending request, got %+v", pout.Data.Pending)
	}
	arec := httptest.NewRecorder()
	areq := httptest.NewRequest(http.MethodPost, "/api/sync/pair-requests/"+pout.Data.Pending[0].ID+"/approve", nil)
	pmux.ServeHTTP(arec, areq)
	if arec.Code != http.StatusOK {
		t.Fatalf("approve: expected 200, got %d: %s", arec.Code, arec.Body.String())
	}

	// Now the replica's poll must retrieve the token and complete the join.
	rec = getPairStatus(t, rmux)
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-status (joined): expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Corner Shop") {
		t.Fatalf("expected the primary's shop name in the joined state, got body: %s", rec.Body.String())
	}
	if !appdb.PendingRestore(replicaPath) {
		t.Fatal("expected a staged restore-pending.db after the replica completes the join")
	}

	// Idempotent: a subsequent poll doesn't error or re-attempt the join.
	rec = getPairStatus(t, rmux)
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-status (re-poll after joined): expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestPairStatus_TreatsPrimary404AsStillWaiting(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	stub := http.NewServeMux()
	stub.HandleFunc("POST /api/sync/pair-request", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"pending-1"},"error":null}`))
	})
	stub.HandleFunc("GET /api/sync/pair-requests/pending-1", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairStart(t, rmux, srv.URL, "stub-till-id", "Bar Till")
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-start: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = getPairStatus(t, rmux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 404-from-primary to render as a normal 200 'still waiting' state, got %d: %s", rec.Code, rec.Body.String())
	}
	// Both the "waiting" and "error" states return 200 (see pairWaitView) —
	// checking only the status code can't tell them apart, so a regression
	// that mis-routed 404 into the error branch would slip past. The
	// self-polling hx-trigger only appears in the "waiting" render.
	if !strings.Contains(rec.Body.String(), `hx-trigger="every 15s"`) {
		t.Fatalf("expected the 'waiting' state (with its polling trigger), got a different state: %s", rec.Body.String())
	}
}

func TestPairStatus_TreatsPrimary429AsStillWaiting(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	stub := http.NewServeMux()
	stub.HandleFunc("POST /api/sync/pair-request", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"pending-1"},"error":null}`))
	})
	stub.HandleFunc("GET /api/sync/pair-requests/pending-1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairStart(t, rmux, srv.URL, "stub-till-id", "Bar Till")
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-start: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = getPairStatus(t, rmux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 429-from-primary to render as a normal 200 'still waiting' state (shared rate limiter, not fatal), got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `hx-trigger="every 15s"`) {
		t.Fatalf("expected the 'waiting' state (with its polling trigger), got a different state: %s", rec.Body.String())
	}
}

// TestPairStatus_ExpiresAfterTTL confirms the replica gives up client-side
// after the same TTL the primary's own pending row uses, rather than
// polling forever on a request that will never resolve (denied and expired
// are indistinguishable from this endpoint alone, per ADR-0033 §4).
// pairingJoinNow is overridden (same seam style as discoveryBrowse/mdnsQuery
// elsewhere in this codebase) so the test doesn't have to sleep 10 minutes.
func TestPairStatus_ExpiresAfterTTL(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	stub := http.NewServeMux()
	stub.HandleFunc("POST /api/sync/pair-request", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"pending-1"},"error":null}`))
	})
	stub.HandleFunc("GET /api/sync/pair-requests/pending-1", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})
	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairStart(t, rmux, srv.URL, "stub-till-id", "Bar Till")
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-start: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	orig := pairingJoinNow
	pairingJoinNow = func() time.Time { return time.Now().Add(11 * time.Minute) }
	t.Cleanup(func() { pairingJoinNow = orig })

	rec = getPairStatus(t, rmux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on an expired attempt, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `hx-trigger`) {
		t.Fatal("expired state must not keep polling (no hx-trigger in the terminal fragment)")
	}
}

// TestPairingSurface_ReachablePastAuthMiddleware is ut-docs#537's regression
// test. A joining replica has no session on the primary at all — it's a
// stranger LAN device sending its first-ever request there — so the two
// inbound pairing endpoints it calls before it holds a token must survive
// the REAL auth.Middleware. This is deliberately NOT the pattern the rest of
// this file (and setup_pairing_test.go's own "real primary" tests) use: they
// construct the primary as a bare *http.ServeMux with no middleware wrap at
// all, so those tests would keep passing even if internal/auth/
// middleware.go's exempt list regressed again — which is exactly how this
// shipped broken (every existing pairing test ran with UT_AUTH=off, or
// against an unwrapped mux, or both).
func TestPairingSurface_ReachablePastAuthMiddleware(t *testing.T) {
	t.Setenv("UT_AUTH", "on")

	primary, _ := newSyncDepsWithPath(t, "primary.db")
	psvc := auth.NewService(primary.Db)
	pmux := http.NewServeMux()
	ptokens := registerSyncAPI(pmux, primary)
	registerPairingAPI(pmux, primary, psvc, ptokens)
	ph := auth.Middleware(pmux, psvc) // the real middleware — the whole point of this test
	srv := httptest.NewServer(ph)
	t.Cleanup(srv.Close)

	// A stranger LAN device: no cookie jar, no session, no prior contact
	// with this primary at all.
	client := &http.Client{Timeout: 5 * time.Second}

	body, _ := json.Marshal(map[string]string{"device_name": "Till 2", "commitment": strings.Repeat("a", 64)})
	resp, err := client.Post(srv.URL+"/api/sync/pair-request", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/sync/pair-request through the real auth middleware: got %d, want 200 — "+
			"a session-less replica must be able to reach this handler (ut-docs#537)", resp.StatusCode)
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil || out.Data.ID == "" {
		t.Fatalf("expected a pending request id in the response, got: %s", resp.Status)
	}

	// Not yet approved: the possession-gated status poll must also survive
	// the middleware and reach the HANDLER's own "not approved yet" 404 —
	// not the middleware's 401 (the wrong secret here is fine; this proves
	// reachability, not the possession check itself — that's pairing_api_test.go's job).
	statusResp, err := client.Get(srv.URL + "/api/sync/pair-requests/" + out.Data.ID + "?request_secret=" + strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /api/sync/pair-requests/{id} through the real auth middleware: got %d, want 404 "+
			"(not-yet-approved, from the handler) — a 401 here means the middleware rejected it "+
			"before the handler ever ran (ut-docs#537)", statusResp.StatusCode)
	}

	// The narrowness half of the same regression: the manager-gated LIST
	// (no id) must stay genuinely behind a session, or any LAN caller could
	// read every pending device name + derived verification code.
	listResp, err := client.Get(srv.URL + "/api/sync/pair-requests")
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("GET /api/sync/pair-requests (the manager list) through the real auth middleware: "+
			"got %d, want 401 — it must stay behind a session", listResp.StatusCode)
	}

	// ut-docs#537 independent review: approve/deny live one path segment
	// deeper under the SAME /api/sync/pair-requests/ prefix as the
	// possession-gated status GET above. A plain HasPrefix match on that
	// prefix (the shipped-then-caught bug) would exempt these too, turning
	// manager approval — ADR-0033 §8's stated trust boundary for inbound
	// pairing — into an anonymous LAN PIN-guessing oracle. Both must still
	// 401 through the real middleware with no session, regardless of the
	// (wrong, deliberately) PIN posted.
	for _, action := range []string{"approve", "deny"} {
		form := url.Values{"manager_pin": {"0000"}}
		actionResp, err := client.Post(srv.URL+"/api/sync/pair-requests/"+out.Data.ID+"/"+action,
			"application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
		if err != nil {
			t.Fatal(err)
		}
		defer actionResp.Body.Close()
		if actionResp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("POST /api/sync/pair-requests/{id}/%s through the real auth middleware, no session: "+
				"got %d, want 401 — manager approval must stay behind a session, not just a PIN "+
				"(ut-docs#537 review finding)", action, actionResp.StatusCode)
		}
	}
}

// TestPairingSurface_FullAuthenticatedRoundTrip is the AC4 "end-to-end test
// that pairs against an auth-enabled primary" this ticket asked for,
// completed rather than just reachability-checked: a session-less replica
// sends the pair request, a REAL logged-in manager (real /api/auth/login,
// real session cookie, real PIN check) approves it through the real
// auth.Middleware, and the replica retrieves a working token. Independent
// review flagged that the reachability test above proves the middleware
// lets requests through but never proves approve/deny stay correctly
// gated for a genuine session — this closes that gap by exercising the
// exact path (manager-authenticated approve) the review's blocker lived in.
func TestPairingSurface_FullAuthenticatedRoundTrip(t *testing.T) {
	t.Setenv("UT_AUTH", "on")

	// A real, fully-migrated schema (not the simplified seedForPages one
	// newSyncDepsWithPath uses) — this test creates a real operator with a
	// real PIN and logs in for real, and seedForPages' users table (built
	// for the join/snapshot tests, not the auth ones) has a NOT NULL
	// pin_hash that doesn't match production's actual first-boot shape.
	// Same pattern as TestPairingFlow_AgainstRealMigratedSchema.
	dbPath := filepath.Join(t.TempDir(), "unitill-pos.db")
	d, err := appdb.Open(dbPath)
	if err != nil {
		t.Fatalf("open real migrated db: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	primary := &common.Deps{
		Cfg: &config.Config{Marketplace: config.MarketplaceConfig{EndpointURL: "http://localhost:8081"}},
		Db:  d.DB,
	}
	psvc := auth.NewService(primary.Db)
	pmux := http.NewServeMux()
	registerPairingAPI(pmux, primary, psvc, &enrolTokens{tokens: map[string]time.Time{}})
	registerAuth(pmux, primary, psvc)
	ph := auth.Middleware(pmux, psvc)
	srv := httptest.NewServer(ph)
	t.Cleanup(srv.Close)
	seedOperatorWithPIN(t, psvc) // username "boss", PIN "2468" — see setup_pairing_test.go

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{
		Timeout: 5 * time.Second,
		Jar:     jar,
		// A successful login 303s to "/", which this minimal test mux never
		// registers (it only carries the auth + pairing routes this test
		// actually needs) — following that redirect would land on an
		// unrelated 404 and mask the login response itself. The Set-Cookie
		// header lands on the redirect response either way, so the jar
		// still picks it up without following it.
		CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse },
	}

	// A real request_secret + its sha256 commitment — same derivation
	// pairStartHandler itself uses (pairing_join.go) — since this test, unlike
	// the reachability-only one above, actually retrieves the token and needs
	// the possession check to genuinely succeed.
	rawSecret := make([]byte, 32)
	if _, err := rand.Read(rawSecret); err != nil {
		t.Fatal(err)
	}
	secret := hex.EncodeToString(rawSecret)
	sum := sha256.Sum256([]byte(secret))
	commitment := hex.EncodeToString(sum[:])

	// A stranger replica sends the pair request — no session needed, same
	// as the reachability test above.
	body, _ := json.Marshal(map[string]string{"device_name": "Till 2", "commitment": commitment})
	resp, err := client.Post(srv.URL+"/api/sync/pair-request", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&out) != nil || out.Data.ID == "" {
		t.Fatalf("pair-request: expected 200 with a pending id, got %d", resp.StatusCode)
	}

	// A real manager logs in on the primary — a genuine session cookie via
	// the real login handler, not a bypass.
	loginResp, err := client.PostForm(srv.URL+"/api/auth/login", url.Values{"pin": {"2468"}})
	if err != nil {
		t.Fatal(err)
	}
	defer loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("manager login: expected 303 (redirect on success — a 200 here means the PIN "+
			"was rejected and renderLogin's error state rendered instead), got %d", loginResp.StatusCode)
	}
	found := false
	for _, c := range jar.Cookies(mustParseURL(t, srv.URL)) {
		if c.Name == auth.CookieName {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a real session cookie (%s) after login — harness premise broken", auth.CookieName)
	}

	// The manager approves — through the real middleware, with the real
	// session cookie (now carried automatically by the jar) plus the
	// manager PIN the approve handler itself requires.
	approveResp, err := client.PostForm(srv.URL+"/api/sync/pair-requests/"+out.Data.ID+"/approve",
		url.Values{"manager_pin": {"2468"}})
	if err != nil {
		t.Fatal(err)
	}
	defer approveResp.Body.Close()
	if approveResp.StatusCode != http.StatusOK {
		t.Fatalf("approve with a real manager session: expected 200, got %d: the fixed exempt-list "+
			"prefix must not have re-widened to swallow this route", approveResp.StatusCode)
	}

	// The replica (still no session — the primary's session cookie is not
	// its concern) retrieves the token with its possession secret.
	statusResp, err := client.Get(srv.URL + "/api/sync/pair-requests/" + out.Data.ID + "?request_secret=" + secret)
	if err != nil {
		t.Fatal(err)
	}
	defer statusResp.Body.Close()
	var tokOut struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if statusResp.StatusCode != http.StatusOK || json.NewDecoder(statusResp.Body).Decode(&tokOut) != nil || tokOut.Data.Token == "" {
		t.Fatalf("expected a real enrolment token after approval, got %d", statusResp.StatusCode)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
