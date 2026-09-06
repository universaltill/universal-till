package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
)

// --- Paste-a-code join success (POST /api/sync/join, POST /api/setup/join)
// gives the operator the same real restart action pairing_wait.html's
// "joined" branch already gives the discovery-list flow (ut-docs#1550),
// instead of the old dead-end "restart this till to finish" text with
// nothing to press (ut-docs#1615). ---

// joinAndGetBody drives a full paste-a-code join (primary + replica, real
// enrol code) and returns the join handler's response body, so these tests
// exercise the actual registered route rather than rendering the partial
// standalone.
func joinAndGetBody(t *testing.T, joinPath string) string {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	if err := primary.Settings.Set(t.Context(), "store.name", "Corner Shop"); err != nil {
		t.Fatalf("set store name: %v", err)
	}
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)
	srv := httptest.NewServer(pmux)
	t.Cleanup(srv.Close)

	code := issueEnrolCode(t, pmux, srv.URL)

	// AuthSvc: /api/setup/join's gate is NeedsFirstBoot (not a manager
	// session), same requirement as TestSetupPairStatusFullFlowCompletesJoin
	// — harmless for /api/sync/join, which doesn't consult it.
	replica, _ := newSyncDepsWithPath(t, "replica.db")
	replica.AuthSvc = auth.NewService(replica.Db)
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)

	rec := httptest.NewRecorder()
	form := "code=" + url.QueryEscape(code) + "&name=Till+2"
	req := httptest.NewRequest(http.MethodPost, joinPath, strings.NewReader(form))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rmux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 on a successful join, got %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestJoinSuccess_GivesARealRestartActionWhenSupported(t *testing.T) {
	stubPairingRestartSupported(t, true)
	cases := []struct {
		joinPath, restartURL string
		wantAutoLoad         bool
	}{
		{"/api/sync/join", "/api/sync/pairing-restart", false},
		{"/api/setup/join", "/api/setup/pairing-restart", true},
	}
	for _, c := range cases {
		body := joinAndGetBody(t, c.joinPath)
		if !strings.Contains(body, "Corner Shop") {
			t.Fatalf("[%s] join success must still name the shop: %s", c.joinPath, body)
		}
		if !strings.Contains(body, `hx-post="`+c.restartURL+`"`) {
			t.Fatalf("[%s] want a restart trigger posting to %s, got: %s", c.joinPath, c.restartURL, body)
		}
		wantTrigger := `hx-trigger="click"`
		if c.wantAutoLoad {
			wantTrigger = `hx-trigger="load, click"`
		}
		if !strings.Contains(body, wantTrigger) {
			t.Fatalf("[%s] want %s, got: %s", c.joinPath, wantTrigger, body)
		}
		if !strings.Contains(body, httpx.T("en", "tills.pairing.restart_now")) {
			t.Fatalf("[%s] want the visible Restart now button, got: %s", c.joinPath, body)
		}
		if !strings.Contains(body, httpx.T("en", "tills.pairing.restarting")) {
			t.Fatalf("[%s] want the restarting message, got: %s", c.joinPath, body)
		}
		if !strings.Contains(body, "/healthz") || !strings.Contains(body, "/login") {
			t.Fatalf("[%s] want the healthz poll that lands on /login once the till is back: %s", c.joinPath, body)
		}
		// The old dead end this card fixes: text with no action attached.
		if strings.Contains(body, httpx.T("en", "tills.restart_to_finish")) {
			t.Fatalf("[%s] the old manual 'restart this till to finish' text must not remain now a real action exists: %s", c.joinPath, body)
		}
		if strings.Contains(body, httpx.T("en", "tills.pairing.close_and_reopen")) {
			t.Fatalf("[%s] the Windows close-and-reopen instruction must not show where auto-restart works: %s", c.joinPath, body)
		}
	}
}

// Where an in-place restart isn't possible (Windows — ut-docs#1614), the
// paste-a-code success must give the one instruction the operator can
// actually perform, same as the discovery-flow "joined" screen — never a
// visible button wired to nothing.
func TestJoinSuccess_ShowsCloseAndReopenWhenUnsupported(t *testing.T) {
	stubPairingRestartSupported(t, false)
	for _, joinPath := range []string{"/api/sync/join", "/api/setup/join"} {
		body := joinAndGetBody(t, joinPath)
		if !strings.Contains(body, "Corner Shop") {
			t.Fatalf("[%s] join success must still name the shop: %s", joinPath, body)
		}
		if !strings.Contains(body, httpx.T("en", "tills.pairing.close_and_reopen")) {
			t.Fatalf("[%s] want the close-and-reopen instruction, got: %s", joinPath, body)
		}
		for _, forbidden := range []string{"pairing-restart", "/healthz", httpx.T("en", "tills.pairing.restart_now"), httpx.T("en", "tills.pairing.restarting")} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("[%s] unsupported platform must not render %q: %s", joinPath, forbidden, body)
			}
		}
	}
}
