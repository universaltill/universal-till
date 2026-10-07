package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// --- GET /ui/join-notice + POST /ui/join-notice/dismiss (ut-docs#2721):
// a standalone till offers to join the shop's main till it found on the
// LAN, from any page. Mirrors GET /ui/pairing-notice (pending_pairings.go).

var joinNoticeMain = discovery.Candidate{Name: "test-2721-main", TillID: "44444444-4444-4444-8444-444444444444", BaseURL: "http://192.168.1.20:37673"}

// withJoinCandidate wires a JoinWatch over a fake LAN answering cands and
// runs its first tick, as the pull loop would after launch.
func withJoinCandidate(t *testing.T, dp *common.Deps, cands ...discovery.Candidate) {
	t.Helper()
	dp.JoinWatch = discovery.NewJoinWatch(dp.Settings, func(context.Context, time.Duration) ([]discovery.Candidate, error) {
		return cands, nil
	})
	dp.JoinWatch.Tick(t.Context())
}

func getJoinNotice(t *testing.T, mux *http.ServeMux) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/join-notice", nil))
	return rec
}

func assertEmpty200(t *testing.T, rec *httptest.ResponseRecorder, why string) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: expected 200, got %d: %s", why, rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("%s: expected an empty body, got: %s", why, rec.Body.String())
	}
}

func TestJoinNoticeUI_RendersCandidateWithPairStartForm(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerJoinNoticeUI(mux, dp)
	withJoinCandidate(t, dp, joinNoticeMain)

	rec := getJoinNotice(t, mux)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`id="join-notice"`,
		`role="status"`,
		joinNoticeMain.Name,
		// The SAME pair-start the Tills page's discovery results use — no
		// new joining mechanism (pairing_join.go reads these three fields).
		`hx-post="/api/sync/pair-start"`,
		`name="base_url" value="` + joinNoticeMain.BaseURL + `"`,
		`name="till_id" value="` + joinNoticeMain.TillID + `"`,
		`name="name"`,
		// Dismiss is server-persisted, not sessionStorage.
		`class="notice-dismiss"`,
		`hx-post="/ui/join-notice/dismiss"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the notice, got: %s", want, body)
		}
	}
	if strings.Contains(body, "sessionStorage") {
		t.Errorf("the join notice's dismiss must be server-persisted, not sessionStorage: %s", body)
	}
}

func TestJoinNoticeUI_EmptyWithoutManager(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerJoinNoticeUI(mux, dp)
	withJoinCandidate(t, dp, joinNoticeMain)
	assertEmpty200(t, getJoinNotice(t, mux), "no manager session")
}

func TestJoinNoticeUI_EmptyWithoutCandidate(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerJoinNoticeUI(mux, dp)
	withJoinCandidate(t, dp)
	assertEmpty200(t, getJoinNotice(t, mux), "nothing found on the LAN")
}

func TestJoinNoticeUI_EmptyWithNoWatchWired(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerJoinNoticeUI(mux, dp)
	assertEmpty200(t, getJoinNotice(t, mux), "no JoinWatch (demo mode, tests)")
}

func TestJoinNoticeUI_EmptyOnReplica(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerJoinNoticeUI(mux, dp)
	withJoinCandidate(t, dp, joinNoticeMain)
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://10.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	assertEmpty200(t, getJoinNotice(t, mux), "replica till")
}

func TestJoinNoticeDismiss_RequiresManager(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerJoinNoticeUI(mux, dp)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ui/join-notice/dismiss", nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
	if v, _, _ := dp.Settings.Get(t.Context(), discovery.JoinBannerDismissedSettingKey); v != "" {
		t.Fatalf("a refused dismiss must not persist, got %q", v)
	}
}

// Dismiss is permanent: persisted per-till, the notice stays gone across
// polls (and restarts — it's a setting, not session state).
func TestJoinNoticeDismiss_PersistsAndHidesNotice(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerJoinNoticeUI(mux, dp)
	withJoinCandidate(t, dp, joinNoticeMain)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/ui/join-notice/dismiss", nil))
	// Empty 200: the dismiss button's hx-target is the mount, so the empty
	// body clears the notice in place.
	assertEmpty200(t, rec, "dismiss response")
	if v, _, _ := dp.Settings.Get(t.Context(), discovery.JoinBannerDismissedSettingKey); v == "" {
		t.Fatalf("expected %s to be set after dismiss", discovery.JoinBannerDismissedSettingKey)
	}
	assertEmpty200(t, getJoinNotice(t, mux), "after dismiss")
}

// The pull loop drives the watcher on a standalone till (it runs on every
// till; a till with no main till used to return straight away).
func TestSyncPullTick_StandaloneTicksJoinWatch(t *testing.T) {
	dp := newMigratedSyncDeps(t, "standalone.db")
	browses := 0
	dp.JoinWatch = discovery.NewJoinWatch(dp.Settings, func(context.Context, time.Duration) ([]discovery.Candidate, error) {
		browses++
		return []discovery.Candidate{joinNoticeMain}, nil
	})
	client := &http.Client{Timeout: time.Second, Transport: failingTransport{}}
	syncPullTick(t.Context(), dp, client, func(context.Context) {})
	if browses != 1 {
		t.Fatalf("browses = %d, want 1 — the pull loop must tick the join watch on a standalone till", browses)
	}
	if c, ok := dp.JoinWatch.Candidate(); !ok || c != joinNoticeMain {
		t.Fatalf("Candidate() = %+v, %v", c, ok)
	}
}

func TestJoinNoticeMount_InBaseLayout(t *testing.T) {
	chdirRoot(t)
	if !strings.Contains(readBaseHTML(t), `<div id="join-notice-mount" hx-get="/ui/join-notice" hx-trigger="load, every 30s" hx-preserve></div>`) {
		t.Error("base.html lacks the #join-notice-mount placeholder")
	}
}

// Review finding (ut-docs#2721): the join only completes while the waiting
// fragment's own pair-status poll runs (pairStatusHandler is what fetches
// the token and calls completeJoin). The mount is innerHTML-swapped with an
// empty body whenever the handler has nothing to show — a lossy mDNS
// rebrowse or a lapsed session mid-approval — so the fragment's target must
// live OUTSIDE the mount, in base.html, never inside the partial.
func TestJoinNoticeStatusTarget_OutsideMount(t *testing.T) {
	chdirRoot(t)
	base := readBaseHTML(t)
	mount := strings.Index(base, `id="join-notice-mount"`)
	status := strings.Index(base, `<div id="join-notice-status" class="join-notice-status" aria-live="polite"></div>`)
	if mount < 0 || status < 0 || status < mount {
		t.Fatalf("base.html must host #join-notice-status as a sibling AFTER #join-notice-mount (mount at %d, status at %d)", mount, status)
	}
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerJoinNoticeUI(mux, dp)
	withJoinCandidate(t, dp, joinNoticeMain)
	body := getJoinNotice(t, mux).Body.String()
	if strings.Contains(body, `id="join-notice-status"`) {
		t.Errorf("the partial must not render #join-notice-status inside the mount: %s", body)
	}
	if !strings.Contains(body, `hx-target="#join-notice-status"`) {
		t.Errorf("the Link form must still target #join-notice-status: %s", body)
	}
}

func TestJoinNoticeRoutes_DemoClassified(t *testing.T) {
	if !demoAllowedRoutes["GET /ui/join-notice"] {
		t.Error("GET /ui/join-notice must be on the demo allow-list beside GET /ui/pairing-notice")
	}
	if !demoDeniedRoutes["POST /ui/join-notice/dismiss"] {
		t.Error("POST /ui/join-notice/dismiss writes a setting — deny it in the demo")
	}
}

// Tester finding (ut-docs#2721): a MAIN till also has an empty
// sync.primary_url, so it ticks JoinWatch too — and every standalone till
// on the LAN advertises itself (ADR-0033 §1). A main till with enrolled
// replicas must never be offered "link this till" to a freshly-installed
// standalone till: accepting would turn the shop's main till into a replica.
func TestJoinNoticeUI_EmptyOnMainTillWithReplicas(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerJoinNoticeUI(mux, dp)
	if _, err := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "test-2721-replica", "x"); err != nil {
		t.Fatal(err)
	}
	withJoinCandidate(t, dp, discovery.Candidate{Name: "test-2721-new-till", TillID: "55555555-5555-4555-8555-555555555555", BaseURL: "http://192.168.1.30:37673"})
	assertEmpty200(t, getJoinNotice(t, mux), "main till with an enrolled replica")
}

// Inverse of the above: a genuinely standalone till (no enrolled tills,
// no sync.primary_url) still gets the notice.
func TestJoinNoticeUI_RendersOnStandaloneWithNoEnrolledTills(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerJoinNoticeUI(mux, dp)
	if list, err := data.NewTillsRepo(dp.Db).ListTills(t.Context()); err != nil || len(list) != 0 {
		t.Fatalf("precondition: expected zero enrolled tills, got %d (%v)", len(list), err)
	}
	withJoinCandidate(t, dp, joinNoticeMain)
	rec := getJoinNotice(t, mux)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `id="join-notice"`) {
		t.Fatalf("standalone till with no enrolled tills must get the notice, got %d: %s", rec.Code, rec.Body.String())
	}
}

// UX findings (ut-docs#2721): the candidate's name is bidi-isolated so a
// Latin name survives fa/ar text, and the name field has a VISIBLE label
// (not just a placeholder/aria-label), reusing the Tills page's key.
func TestJoinNoticeUI_BdiNameAndVisibleLabel(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	registerJoinNoticeUI(mux, dp)
	withJoinCandidate(t, dp, discovery.Candidate{Name: `test-2721-<b>main</b>`, TillID: joinNoticeMain.TillID, BaseURL: joinNoticeMain.BaseURL})
	body := getJoinNotice(t, mux).Body.String()
	if !strings.Contains(body, `<bdi class="join-notice-name">test-2721-&lt;b&gt;main&lt;/b&gt;</bdi>`) {
		t.Errorf("expected the escaped name wrapped in <bdi>, got: %s", body)
	}
	if strings.Contains(body, `<b>main</b>`) {
		t.Errorf("the candidate name must be HTML-escaped: %s", body)
	}
	if !strings.Contains(body, `<label class="join-notice-label" for="join-notice-name">`) {
		t.Errorf("expected a visible <label for=join-notice-name>, got: %s", body)
	}
	if strings.Contains(body, "placeholder=") {
		t.Errorf("the name field's only label must not be a placeholder: %s", body)
	}
}
