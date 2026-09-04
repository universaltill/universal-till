package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	appdb "github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/discovery"
)

// --- First-boot discovery + approve-to-pair on the setup wizard
// (ut-docs#289): the /api/setup/{discover-primaries,pair-start,pair-status}
// mirrors of the manager-gated /api/sync/* trio. Every test here runs the
// request through the REAL auth middleware (auth.Middleware), not the bare
// mux: these routes only work at all because internal/auth/middleware.go
// exempts them, and a test on the bare mux would keep passing if that
// exemption were dropped — the exact class of wiring bug that makes a
// fresh till unable to join (ut-docs#344's lesson, applied to auth). ---

// setupPairingHarness is newFullAuthDeps plus the discovery/pairing-join
// route groups, wrapped in the real session middleware. The users table
// starts empty, so NeedsFirstBoot is true until a test seeds an operator.
func setupPairingHarness(t *testing.T) (http.Handler, *auth.Service, *http.ServeMux) {
	t.Helper()
	mux, svc, d := newFullAuthDeps(t)
	registerDiscoveryAPI(mux, d)
	registerPairingJoinAPI(mux, d)
	return auth.Middleware(mux, svc), svc, mux
}

// seedOperatorWithPIN closes the first-boot window the same way the wizard
// does: an active operator with a usable PIN.
func seedOperatorWithPIN(t *testing.T, svc *auth.Service) {
	t.Helper()
	id, err := svc.Repo().CreateUser(t.Context(), "boss", "Boss", "admin")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPIN("2468")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().SetUserPIN(t.Context(), id, hash); err != nil {
		t.Fatal(err)
	}
}

func doGet(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func doPostForm(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// GET /api/setup/discover-primaries is reachable — with NO session, through
// the middleware — exactly while it is first boot, and refuses with a
// redirect to /login the moment an operator exists (mirror of
// TestSetupJoinRefusedOnceAnOperatorExists: this endpoint is middleware-
// exempt, so its NeedsFirstBoot gate is the ONLY thing keeping a stranger
// on the LAN from running scans from a configured till).
func TestSetupDiscoverPrimariesFirstBootGate(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	h, svc, _ := setupPairingHarness(t)
	stubBrowse(t, []discovery.Candidate{
		{Name: "Task Runner", TillID: "till-abc123", BaseURL: "http://192.168.1.50:8080"},
	}, nil)

	rec := doGet(t, h, "/api/setup/discover-primaries")
	if rec.Code != http.StatusOK {
		t.Fatalf("during first boot: expected 200 through the real middleware, got %d: %s",
			rec.Code, rec.Body.String())
	}
	var out struct {
		Data struct {
			Primaries []struct {
				Name    string `json:"name"`
				TillID  string `json:"till_id"`
				BaseURL string `json:"base_url"`
			} `json:"primaries"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (body: %s)", err, rec.Body.String())
	}
	if len(out.Data.Primaries) != 1 || out.Data.Primaries[0].TillID != "till-abc123" {
		t.Fatalf("unexpected primaries payload: %+v", out.Data.Primaries)
	}

	seedOperatorWithPIN(t, svc)
	rec = doGet(t, h, "/api/setup/discover-primaries")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("after first boot: code=%d loc=%q, want 303 -> /login (an unauthenticated "+
			"caller must not be able to scan the LAN from a configured till)",
			rec.Code, rec.Header().Get("Location"))
	}
}

// POST /api/setup/pair-start: same window. During first boot a malformed
// submit gets past the gate and fails on its own merits (the rendered
// error state); once an operator exists it must refuse outright.
func TestSetupPairStartFirstBootGate(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	h, svc, _ := setupPairingHarness(t)

	rec := doPostForm(t, h, "/api/setup/pair-start", url.Values{})
	if rec.Code != http.StatusOK {
		t.Fatalf("during first boot: expected 200 (error state in the body), got %d: %s",
			rec.Code, rec.Body.String())
	}
	// ut-docs#1540: the message now names the field as the SCREEN labels it,
	// instead of the server-side "base_url, till_id and name are all
	// required" — three JSON keys the operator cannot see, and the literal
	// text behind every "pairing failed" report from the pilot hardware.
	if !strings.Contains(rec.Body.String(), "Give this till a name first") {
		t.Fatalf("expected the missing-name error naming the visible field, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "base_url") {
		t.Fatalf("the operator must never be shown server-side field names, got: %s", rec.Body.String())
	}

	seedOperatorWithPIN(t, svc)
	rec = doPostForm(t, h, "/api/setup/pair-start", url.Values{
		"base_url": {"http://127.0.0.1:1"}, "till_id": {"x"}, "name": {"Till 2"},
	})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("after first boot: code=%d loc=%q, want 303 -> /login (an unauthenticated "+
			"caller must not be able to start pairing from a configured till)",
			rec.Code, rec.Header().Get("Location"))
	}
}

// GET /api/setup/pair-status: same window. Idle render during first boot
// (no polling trigger), redirect once an operator exists.
func TestSetupPairStatusFirstBootGate(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	h, svc, _ := setupPairingHarness(t)

	rec := doGet(t, h, "/api/setup/pair-status")
	if rec.Code != http.StatusOK {
		t.Fatalf("during first boot: expected 200 with no active attempt, got %d: %s",
			rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hx-trigger") {
		t.Fatal("no active attempt must not render a polling state")
	}

	seedOperatorWithPIN(t, svc)
	rec = doGet(t, h, "/api/setup/pair-status")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("after first boot: code=%d loc=%q, want 303 -> /login",
			rec.Code, rec.Header().Get("Location"))
	}
}

// ut-docs#289 review (F3): unlike the manager-gated flavours, these two
// first-boot routes need no session at all — discover-primaries triggers a
// 4s mDNS scan and pair-start is an outbound-HTTP primitive to an
// attacker-chosen host, so both are rate-limited the same way
// pairing_api.go's inbound endpoints already are. pair-status stays
// unlimited (it's the legitimate 15s self-poll, not an attacker-chosen
// outbound call), so it isn't tested here.

func TestSetupDiscoverPrimariesRateLimited(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	h, _, _ := setupPairingHarness(t)
	stubBrowse(t, nil, nil)

	var last *httptest.ResponseRecorder
	got429 := false
	for i := 0; i < 20; i++ {
		last = doGet(t, h, "/api/setup/discover-primaries")
		if last.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
		if last.Code != http.StatusOK {
			t.Fatalf("unexpected status before the cap: %d: %s", last.Code, last.Body.String())
		}
	}
	if !got429 {
		t.Fatal("expected a rapid burst from one source to eventually get 429 Too Many Requests")
	}
}

func TestSetupPairStartRateLimited(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	h, _, _ := setupPairingHarness(t)

	var last *httptest.ResponseRecorder
	got429 := false
	for i := 0; i < 20; i++ {
		// Missing fields is enough to reach the limiter and fail cheaply
		// afterward — the limiter check runs before form validation.
		last = doPostForm(t, h, "/api/setup/pair-start", url.Values{})
		if last.Code == http.StatusTooManyRequests {
			got429 = true
			break
		}
	}
	if !got429 {
		t.Fatal("expected a rapid burst from one source to eventually get 429 Too Many Requests")
	}
}

// The wizard's pair-start drives the SAME backend flow as the Tills page:
// a pending request appears on the primary and the wizard shows the SAME
// independently-derived 6-digit code the primary's manager sees. The
// waiting fragment must poll the SETUP status route — the /api/sync one is
// not middleware-exempt, so a first-boot till polling it would only ever
// see 401s and the wizard would hang on "waiting" forever.
func TestSetupPairStartShowsCodeAndPollsSetupStatus(t *testing.T) {
	t.Setenv("UT_AUTH", "on")

	// Real primary serving the pairing backend. Its inbound pair-request
	// route is unauthenticated by design (ADR-0033 §8), so UT_AUTH=on is
	// fine on this side too.
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

	h, _, _ := setupPairingHarness(t)
	rec := doPostForm(t, h, "/api/setup/pair-start", url.Values{
		"base_url": {srv.URL}, "till_id": {primaryTillID}, "name": {"Bar Till"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-start: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The primary must actually have a pending request, and the wizard must
	// show the same code the primary's approval card derives. Read the row
	// via PairingRepo (the HTTP pending list is manager-gated and UT_AUTH is
	// deliberately "on" here) and derive the code exactly the way the
	// primary's card does — derivedVerificationCode(commitment, tillID).
	pending, err := data.NewPairingRepo(primary.Db).ListPending(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected exactly one pending request on the primary, got %+v", pending)
	}
	if want := derivedVerificationCode(pending[0].Commitment, primaryTillID); !strings.Contains(body, want) {
		t.Fatalf("expected the wizard's waiting screen to show verification code %q, got: %s", want, body)
	}
	if !strings.Contains(body, `hx-get="/api/setup/pair-status"`) {
		t.Fatalf("the wizard's waiting fragment must poll /api/setup/pair-status "+
			"(the /api/sync route is not middleware-exempt and would 401 on a first-boot till), got: %s", body)
	}
}

// Full round trip through the setup routes: pair-start -> primary manager
// approves -> pair-status completes the join and stages the restore —
// exactly the flow the Tills page already has, now reachable at first boot.
func TestSetupPairStatusFullFlowCompletesJoin(t *testing.T) {
	// UT_AUTH=off so the primary-side approve POST works without a manager
	// session (same as TestPairStatus_FullFlow_CompletesJoinAndStagesRestore).
	// The replica-side setup routes don't care: their gate is NeedsFirstBoot,
	// not the manager check, and the middleware wrap still applies.
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

	// Replica: needs a real DBPath (completeJoin stages a restore next to
	// it), so newSyncDepsWithPath rather than newFullAuthDeps; its seeded
	// users all have empty pin_hash, so NeedsFirstBoot stays true.
	replica, replicaPath := newSyncDepsWithPath(t, "replica.db")
	rsvc := auth.NewService(replica.Db)
	replica.AuthSvc = rsvc
	if fb, err := rsvc.NeedsFirstBoot(t.Context()); err != nil || !fb {
		t.Fatalf("harness premise broken: replica must be in first-boot state (fb=%v err=%v)", fb, err)
	}
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)
	rh := auth.Middleware(rmux, rsvc)

	rec := doPostForm(t, rh, "/api/setup/pair-start", url.Values{
		"base_url": {srv.URL}, "till_id": {primaryTillID}, "name": {"Bar Till"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-start: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Still waiting: the primary hasn't approved yet, nothing staged.
	rec = doGet(t, rh, "/api/setup/pair-status")
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-status (waiting): expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if appdb.PendingRestore(replicaPath) {
		t.Fatal("must not stage a restore before the primary approves")
	}

	// Both route groups share the single outbound attempt (a till only ever
	// has one in flight): the manager-facing status route sees it too.
	srec := httptest.NewRecorder()
	rmux.ServeHTTP(srec, httptest.NewRequest(http.MethodGet, "/api/sync/pair-status", nil))
	if srec.Code != http.StatusOK || !strings.Contains(srec.Body.String(), "hx-trigger") {
		t.Fatalf("expected /api/sync/pair-status to see the same waiting attempt, got %d: %s",
			srec.Code, srec.Body.String())
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

	// Now the wizard's poll must retrieve the token and complete the join.
	rec = doGet(t, rh, "/api/setup/pair-status")
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-status (joined): expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Corner Shop") {
		t.Fatalf("expected the primary's shop name in the joined state, got body: %s", rec.Body.String())
	}
	if !appdb.PendingRestore(replicaPath) {
		t.Fatal("expected a staged restore-pending.db after the wizard completes the join")
	}

	// Idempotent re-poll.
	rec = doGet(t, rh, "/api/setup/pair-status")
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-status (re-poll after joined): expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

// An attempt nobody approves expires after the same TTL as the primary's
// pending row and renders the localized terminal message — the wizard must
// not poll forever (AC: expired/denied states show a clear localized
// message, reusing tills.pairing.*).
func TestSetupPairStatusExpiresAfterTTL(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
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

	h, _, _ := setupPairingHarness(t)
	rec := doPostForm(t, h, "/api/setup/pair-start", url.Values{
		"base_url": {srv.URL}, "till_id": {"stub-till-id"}, "name": {"Bar Till"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("pair-start: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	orig := pairingJoinNow
	pairingJoinNow = func() time.Time { return time.Now().Add(11 * time.Minute) }
	t.Cleanup(func() { pairingJoinNow = orig })

	rec = doGet(t, h, "/api/setup/pair-status")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on an expired attempt, got %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "hx-trigger") {
		t.Fatal("expired state must not keep polling")
	}
	// The harness loads the real en bundle, so the localized message —
	// tills.pairing.no_response — must come out as prose, not a bare key.
	if !strings.Contains(rec.Body.String(), "No response") {
		t.Fatalf("expected the localized expired/no-response message, got: %s", rec.Body.String())
	}
}

// The setup wizard's join step renders the discovery affordance alongside
// (not replacing) the manual-code form (mirror of
// TestSetupPageLoadsHTMXForTheJoinForm's template assertions).
func TestSetupPageRendersDiscoveryAffordance(t *testing.T) {
	// ut-docs#662: hermetic against the developer machine's real OS locale —
	// otherwise ut-docs#590's /setup detection redirect fires on any machine
	// with a locale set and this test never even reaches the wizard.
	withOSLocale(t, "", "")
	mux, _, _ := newFullAuthDeps(t)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/setup", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()

	// The manual fallback stays untouched.
	if !strings.Contains(body, `hx-post="/api/setup/join"`) {
		t.Fatal("the manual-code join form must remain as the documented fallback")
	}
	// Discovery affordance: button, results list, name input the pair
	// button includes, and the status area the waiting fragment swaps into.
	for _, want := range []string{
		`id="setup-discover-btn"`,
		`id="setup-discover-results"`,
		`id="setup-pairing-till-name"`,
		`id="setup-pairing-status-host"`,
		`/api/setup/discover-primaries`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("setup page is missing %s in the join step", want)
		}
	}
	// The wizard must talk to the first-boot-gated routes, not the
	// manager-gated /api/sync ones a first-boot till can never reach.
	if !strings.Contains(body, "/api/setup/pair-start") {
		t.Error("setup page must start pairing via /api/setup/pair-start")
	}
	if strings.Contains(body, `fetch('/api/sync/discover-primaries')`) {
		t.Error("setup page must not call the manager-gated /api/sync/discover-primaries")
	}
}
