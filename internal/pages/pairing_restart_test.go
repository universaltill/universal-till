package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
)

// --- POST /api/sync/pairing-restart + POST /api/setup/pairing-restart
// (ut-docs#1550): the "joined" screen's restart trigger. Both flavours call
// procrestart.Restart through the pairingRestartFn seam — a real call would
// syscall.Exec the test binary mid-run — and answer the standard
// { "data": …, "error": null } envelope immediately (Restart only
// schedules; the 1.5s re-exec delay is what lets this response flush). ---

// stubPairingRestart replaces the restart seam and returns a counter of
// how many times a handler asked for a restart.
func stubPairingRestart(t *testing.T) *int {
	t.Helper()
	calls := 0
	old := pairingRestartFn
	pairingRestartFn = func() { calls++ }
	t.Cleanup(func() { pairingRestartFn = old })
	return &calls
}

func stubPairingRestartSupported(t *testing.T, v bool) {
	t.Helper()
	old := pairingRestartSupported
	pairingRestartSupported = func() bool { return v }
	t.Cleanup(func() { pairingRestartSupported = old })
}

// stubPairingRestorePending fakes db.PendingRestore's answer (review
// finding, ut-docs#1550: the handler must refuse when nothing is actually
// staged, so every test that expects a restart to be scheduled needs this
// stubbed true — a real staged restore-pending file is not what these
// handler-level tests are about).
func stubPairingRestorePending(t *testing.T, v bool) {
	t.Helper()
	old := pairingRestorePending
	pairingRestorePending = func(string) bool { return v }
	t.Cleanup(func() { pairingRestorePending = old })
}

func postPairingRestart(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	return rec
}

func TestPairingRestart_ManagerRouteRequiresManager(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	calls := stubPairingRestart(t)
	stubPairingRestorePending(t, true)
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairingRestart(t, rmux, "/api/sync/pairing-restart")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d: %s", rec.Code, rec.Body.String())
	}
	if *calls != 0 {
		t.Fatalf("a refused request must never schedule a restart (got %d calls)", *calls)
	}
}

func TestPairingRestart_ManagerRouteSchedulesRestartAndAnswersEnvelope(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	calls := stubPairingRestart(t)
	stubPairingRestorePending(t, true)
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairingRestart(t, rmux, "/api/sync/pairing-restart")
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if *calls != 1 {
		t.Fatalf("expected exactly one restart to be scheduled, got %d", *calls)
	}
	var out struct {
		Data struct {
			Restarting bool `json:"restarting"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not the JSON envelope: %v: %s", err, rec.Body.String())
	}
	if !out.Data.Restarting || out.Error != nil {
		t.Fatalf("want {data:{restarting:true}, error:null}, got %s", rec.Body.String())
	}
}

// The first-boot flavour must work with NO session, through the REAL auth
// middleware — it only works at all because internal/auth/middleware.go
// exempts it, and a bare-mux test would keep passing if that exemption
// were missing (the exact 401-before-the-handler failure mode
// pairing_join.go's header comment warns about for pair-status).
func TestPairingRestart_SetupRouteWorksUnauthenticatedOnFirstBoot(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	calls := stubPairingRestart(t)
	stubPairingRestorePending(t, true)
	h, _, _ := setupPairingHarness(t)

	rec := postPairingRestart(t, h, "/api/setup/pairing-restart")
	if rec.Code != http.StatusOK {
		t.Fatalf("first-boot restart with no session: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if *calls != 1 {
		t.Fatalf("expected exactly one restart to be scheduled, got %d", *calls)
	}
	if !strings.Contains(rec.Body.String(), `"restarting":true`) {
		t.Fatalf("expected the restarting envelope, got %s", rec.Body.String())
	}
}

// Once an operator exists the first-boot window is closed: the exempt route
// must refuse (redirect to /login, like every /api/setup sibling) and, above
// all, must NOT restart a configured till on an anonymous LAN request.
func TestPairingRestart_SetupRouteRefusedOnceAnOperatorExists(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	calls := stubPairingRestart(t)
	h, svc, _ := setupPairingHarness(t)
	seedOperatorWithPIN(t, svc)

	rec := postPairingRestart(t, h, "/api/setup/pairing-restart")
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("expected 303 once an operator exists, got %d: %s", rec.Code, rec.Body.String())
	}
	if *calls != 0 {
		t.Fatalf("a refused request must never schedule a restart (got %d calls)", *calls)
	}
}

// The manager-gated sibling with a real session: a cashier is denied, a
// manager gets past the gate (same shape as
// TestSyncManagementEndpoints_RealSessionGatesByRole).
func TestPairingRestart_ManagerRouteRealSessionGatesByRole(t *testing.T) {
	calls := stubPairingRestart(t)
	stubPairingRestorePending(t, true)
	dp := newSyncPairingGateTestDeps(t)
	mux := http.NewServeMux()
	registerPairingJoinAPI(mux, dp)

	req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/sync/pairing-restart", nil), auth.User{ID: "u1", Role: "cashier"})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || *calls != 0 {
		t.Fatalf("cashier = %d (calls %d), want 403 and no restart", rec.Code, *calls)
	}

	req = auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/sync/pairing-restart", nil), auth.User{ID: "u1", Role: "manager"})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || *calls != 1 {
		t.Fatalf("manager = %d (calls %d), want 200 and one restart", rec.Code, *calls)
	}
}

// Review finding, ut-docs#1550: without a staged restore, the first-boot
// route is an unauthenticated, unconditional restart any anonymous LAN
// device could fire in a loop. completeJoin stages the restore strictly
// BEFORE the state flips to "joined", so this is true on every legitimate
// call and false on every abusive one — proven here by NOT staging one.
func TestPairingRestart_RefusesWhenNothingIsStaged(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	calls := stubPairingRestart(t)
	stubPairingRestorePending(t, false)
	h, _, _ := setupPairingHarness(t)

	rec := postPairingRestart(t, h, "/api/setup/pairing-restart")
	if rec.Code != http.StatusConflict {
		t.Fatalf("no staged restore: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if *calls != 0 {
		t.Fatalf("a refused request must never schedule a restart (got %d calls)", *calls)
	}
	var out struct {
		Data  any    `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not the JSON envelope: %v: %s", err, rec.Body.String())
	}
	if out.Data != nil || out.Error == "" {
		t.Fatalf("want {data:null, error:\"…\"}, got %s", rec.Body.String())
	}
}

// The manager flavour applies the SAME staged-restore guard as the
// first-boot one — it isn't a first-boot-only check.
func TestPairingRestart_ManagerRouteRefusesWhenNothingIsStaged(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	calls := stubPairingRestart(t)
	stubPairingRestorePending(t, false)
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerPairingJoinAPI(rmux, replica)

	rec := postPairingRestart(t, rmux, "/api/sync/pairing-restart")
	if rec.Code != http.StatusConflict {
		t.Fatalf("no staged restore: expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
	if *calls != 0 {
		t.Fatalf("a refused request must never schedule a restart (got %d calls)", *calls)
	}
}

// Review finding, ut-docs#1550: the first-boot restart route must carry the
// same rate limit as its sibling /api/setup/pair-start — otherwise, once a
// join IS staged, any anonymous LAN device can hold the till in a restart
// loop for as long as it likes.
func TestPairingRestart_SetupRouteIsRateLimited(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	stubPairingRestart(t)
	stubPairingRestorePending(t, true)
	h, _, _ := setupPairingHarness(t)

	var last *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		last = postPairingRestart(t, h, "/api/setup/pairing-restart")
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("6th request in the same window: expected 429, got %d: %s", last.Code, last.Body.String())
	}
}

// --- The "joined" render itself (web/ui/partials/pairing_wait.html). ---

func renderJoined(t *testing.T, statusURL string) string {
	t.Helper()
	chdirRoot(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, statusURL, nil)
	pairWaitView(rec, req, statusURL, "joined", "", "Corner Shop", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("joined render: %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// Where an in-place restart is possible (every non-Windows build), the
// joined screen restarts the till itself. The first-boot wizard fires the
// restart POST on load (a Pi kiosk has no shell to press anything from);
// the manager-driven /tills flow does NOT auto-fire (review finding,
// ut-docs#1550 — a configured, possibly-in-use till restarts only on the
// manager's explicit click). BOTH always carry a visible "Restart now"
// button wired to the same endpoint — the manual fallback must never
// depend on the auto-trigger script (status/lock/exit must always be
// reachable). The route it posts to follows the same manager-vs-first-boot
// split as the status URL it was rendered for.
func TestPairingWait_JoinedAutoRestartsWhenSupported(t *testing.T) {
	stubPairingRestartSupported(t, true)
	cases := []struct {
		statusURL, restartURL string
		wantAutoLoad          bool
	}{
		{"/api/sync/pair-status", "/api/sync/pairing-restart", false},
		{"/api/setup/pair-status", "/api/setup/pairing-restart", true},
	}
	for _, c := range cases {
		body := renderJoined(t, c.statusURL)
		if !strings.Contains(body, "Corner Shop") {
			t.Fatalf("[%s] joined render must still name the shop: %s", c.statusURL, body)
		}
		if !strings.Contains(body, `hx-post="`+c.restartURL+`"`) {
			t.Fatalf("[%s] want a restart trigger posting to %s, got: %s", c.statusURL, c.restartURL, body)
		}
		wantTrigger := `hx-trigger="click"`
		if c.wantAutoLoad {
			wantTrigger = `hx-trigger="load, click"`
		}
		if !strings.Contains(body, wantTrigger) {
			t.Fatalf("[%s] want %s, got: %s", c.statusURL, wantTrigger, body)
		}
		if !strings.Contains(body, httpx.T("en", "tills.pairing.restart_now")) {
			t.Fatalf("[%s] want the visible Restart now button, got: %s", c.statusURL, body)
		}
		if !strings.Contains(body, httpx.T("en", "tills.pairing.restarting")) {
			t.Fatalf("[%s] want the restarting message, got: %s", c.statusURL, body)
		}
		if !strings.Contains(body, "/healthz") || !strings.Contains(body, "/login") {
			t.Fatalf("[%s] want the healthz poll that lands on /login once the till is back: %s", c.statusURL, body)
		}
		if strings.Contains(body, httpx.T("en", "tills.restart_to_finish")) {
			t.Fatalf("[%s] the old manual 'restart this till to finish' text must not remain on the auto-restart path: %s", c.statusURL, body)
		}
		if strings.Contains(body, httpx.T("en", "tills.pairing.close_and_reopen")) {
			t.Fatalf("[%s] the Windows close-and-reopen instruction must not show where auto-restart works: %s", c.statusURL, body)
		}
		if strings.Contains(body, "hx-trigger=\"every") {
			t.Fatalf("[%s] joined is terminal — no status self-poll: %s", c.statusURL, body)
		}
	}
}

// Where it isn't (Windows, ut-docs#1614 tracks a native restart there), the
// screen must not show a button that does nothing: it gives the one
// instruction the operator can physically perform — close and reopen the
// app, which re-runs ApplyPendingRestore on the next start.
func TestPairingWait_JoinedShowsCloseAndReopenWhenUnsupported(t *testing.T) {
	stubPairingRestartSupported(t, false)
	body := renderJoined(t, "/api/sync/pair-status")
	if !strings.Contains(body, "Corner Shop") {
		t.Fatalf("joined render must still name the shop: %s", body)
	}
	if !strings.Contains(body, httpx.T("en", "tills.pairing.close_and_reopen")) {
		t.Fatalf("want the close-and-reopen instruction, got: %s", body)
	}
	for _, forbidden := range []string{"pairing-restart", "/healthz", httpx.T("en", "tills.pairing.restart_now"), httpx.T("en", "tills.pairing.restarting")} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unsupported platform must not render %q: %s", forbidden, body)
		}
	}
}
