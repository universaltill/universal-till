package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/enroll"
)

// ut-docs#5058 (design §3.3 T2, AC12): a till the owner replaced as the
// shop's main till never acts as one on the shop network again. Other tills
// can no longer enrol with it, link to it or push their journals to it: their
// sales stay queued on them (the re-join to the new main till sends them on,
// UT-5), instead of landing on a till whose sales reach no one.

func markReplacedForTest(t *testing.T, kv enroll.Settings) {
	t.Helper()
	enroll.MarkReplaced(context.Background(), kv, "Counter 2", "2026-10-26T10:00:00Z")
}

func TestReplacedMainTillRefusesNewJoins(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	tokens := registerSyncAPI(pmux, primary)
	registerPairingAPI(pmux, primary, auth.NewService(primary.Db), tokens)
	// A code shown before the replacement is not honoured after it.
	code := issueEnrolCode(t, pmux, "http://192.168.1.10:8080")
	markReplacedForTest(t, primary.Settings)

	rec := httptest.NewRecorder()
	pmux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/sync/enroll-token", strings.NewReader("url=http://192.168.1.10:8080")))
	if rec.Code != http.StatusConflict {
		t.Fatalf("enroll-token on a replaced till: %d %s", rec.Code, rec.Body.String())
	}

	_, token, err := decodeEnrollCode(code)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, body string }{
		{"/api/sync/enroll", `{"token":"` + token + `","name":"Till 9"}`},
		{"/api/sync/pair-request", `{"device_name":"Till 9","commitment":"` + strings.Repeat("a", 64) + `"}`},
	} {
		req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		pmux.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), lanErrTillReplaced) {
			t.Errorf("%s on a replaced till: %d %s, want 409 %s", tc.path, rec.Code, rec.Body.String(), lanErrTillReplaced)
		}
	}
	if n := countTills(t, primary); n != 0 {
		t.Fatalf("a replaced till enrolled %d till(s)", n)
	}
}

func TestReplacedMainTillRefusesJournalsAndLinksFromItsTills(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)
	registerSyncSales(pmux, primary)
	registerSyncNoSales(pmux, primary)
	registerSyncClearedBaskets(pmux, primary)
	registerSyncLink(pmux, primary)
	_, bearer := enrolOnPrimary(t, pmux, "Till 2")
	markReplacedForTest(t, primary.Settings)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/sync/sales"},
		{http.MethodPost, "/api/sync/no-sales"},
		{http.MethodPost, "/api/sync/cleared-baskets"},
		{http.MethodGet, "/api/sync/link"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader("[]"))
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		pmux.ServeHTTP(rec, req)
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), lanErrTillReplaced) {
			t.Errorf("%s %s on a replaced till: %d %s, want 409 %s", tc.method, tc.path, rec.Code, rec.Body.String(), lanErrTillReplaced)
		}
	}
}

// The joining till says why, in its own language, instead of "refused".
func TestJoiningAReplacedMainTillSaysItWasReplaced(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)
	srv := httptest.NewServer(pmux)
	t.Cleanup(srv.Close)
	code := issueEnrolCode(t, pmux, srv.URL)
	markReplacedForTest(t, primary.Settings)

	replica, _ := newSyncDepsWithPath(t, "replica.db")
	rmux := http.NewServeMux()
	registerSyncAPI(rmux, replica)
	rec := postSyncJoin(t, rmux, "/api/sync/join", url.Values{"code": {code}, "name": {"Till 2"}, "replace_local_data": {"1"}})
	if !strings.Contains(rec.Body.String(), "was replaced") {
		t.Fatalf("join against a replaced till: %d %s, want the replaced message", rec.Code, rec.Body.String())
	}
}

func TestNotReplacedMainTillStillEnrols(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)
	enrolOnPrimary(t, pmux, "Till 2")
}

// AC12: the chip "Replaced by <name>" from the persisted state, even with no
// 401 streak in memory (a restarted till), and a banner on every page that
// cannot be dismissed.
func TestReplacedTillShowsChipAndBanner(t *testing.T) {
	mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{})
	registerReplacedBanner(mux, d)
	cashier := auth.User{ID: "c1", Role: "cashier", DisplayName: "C"}

	if b := strings.TrimSpace(getAs(mux, "/ui/replaced-banner", &cashier).Body.String()); b != "" {
		t.Fatalf("banner on a till that was not replaced: %q", b)
	}
	markReplacedForTest(t, d.Settings)

	chip := getAs(mux, "/ui/cloud-auth-chip", &cashier).Body.String()
	if !strings.Contains(chip, `data-cloud-auth="replaced"`) || !strings.Contains(chip, "Replaced by ⁨Counter 2⁩") || !strings.Contains(chip, "2026-10-26") {
		t.Fatalf("chip: %s", chip)
	}
	banner := getAs(mux, "/ui/replaced-banner", &cashier).Body.String()
	if !strings.Contains(banner, `data-testid="replaced-banner"`) || !strings.Contains(banner, "sales here are not reported") || strings.Contains(banner, "notice-dismiss") {
		t.Fatalf("banner: %s", banner)
	}
}

func TestReplacedTillWithoutSuccessorNameSaysTheNewMainTill(t *testing.T) {
	mux, d := newCloudAuthChipDeps(t, cloudsync.AuthChip{})
	registerReplacedBanner(mux, d)
	enroll.MarkReplaced(context.Background(), d.Settings, "", "2026-10-26T10:00:00Z")
	if b := getAs(mux, "/ui/replaced-banner", &mgrUser).Body.String(); !strings.Contains(b, "Replaced by the new main till") {
		t.Fatalf("banner without a name: %s", b)
	}
}

// The other tills see the reason when their pushes are refused: their
// journal stays queued (cursor unmoved) for the re-join to the new main till.
func TestReplicaPushToReplacedMainTillKeepsItsSalesQueued(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)
	registerSyncSales(pmux, primary)
	srv := httptest.NewServer(pmux)
	t.Cleanup(srv.Close)
	_, bearer := enrolOnPrimary(t, pmux, "Till 2")
	markReplacedForTest(t, primary.Settings)

	replica, _ := newSyncDepsWithPath(t, "replica.db")
	seedLocalSale(t, replica, "sale-5058-a", "T2-000000001", "2026-10-26T09:00:00Z")
	syncPushSales(context.Background(), replica, srv.Client(), srv.URL, bearer)
	if cur, _, _ := replica.Settings.Get(context.Background(), "sync.push_cursor"); cur != "" {
		t.Fatalf("the push cursor moved to %q although the replaced main till refused the sale", cur)
	}
	count := func() int {
		var n int
		if err := primary.Db.QueryRow(`SELECT COUNT(*) FROM sales WHERE id = 'sale-5058-a'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count() != 0 {
		t.Fatal("the replaced main till applied the sale")
	}
	// Control: the same push to a main till that was not replaced lands.
	if err := primary.Settings.Set(context.Background(), "till_identity.replaced", ""); err != nil {
		t.Fatal(err)
	}
	syncPushSales(context.Background(), replica, srv.Client(), srv.URL, bearer)
	if count() != 1 {
		t.Fatal("control: the push did not land on a main till that was not replaced")
	}
}

// A replaced main till advertises no link on ping, so its tills poll
// instead of redialling a link it refuses.
func TestReplacedMainTillPingAdvertisesNoLink(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	primary, _ := newSyncDepsWithPath(t, "primary.db")
	pmux := http.NewServeMux()
	registerSyncAPI(pmux, primary)
	_, bearer := enrolOnPrimary(t, pmux, "Till 2")
	ping := func() string {
		req := httptest.NewRequest(http.MethodGet, "/api/sync/ping", nil)
		req.Header.Set("Authorization", "Bearer "+bearer)
		rec := httptest.NewRecorder()
		pmux.ServeHTTP(rec, req)
		return rec.Body.String()
	}
	if !strings.Contains(ping(), `"link":1`) {
		t.Fatal("control: a main till that was not replaced advertises no link")
	}
	markReplacedForTest(t, primary.Settings)
	if b := ping(); !strings.Contains(b, `"link":0`) {
		t.Fatalf("ping on a replaced main till: %s", b)
	}
}
