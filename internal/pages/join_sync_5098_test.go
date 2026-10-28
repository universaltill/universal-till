package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/catalogpull"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#5098: a till that joined a shop with products must not land on the
// empty-shop import welcome. While the join's first catalogue pull runs the
// page shows the "Syncing your shop" state; once products exist it leaves
// for the sale screen; the welcome is only for a catalogue that is really
// empty after the pull finished.

func joinImportDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	mux, d := newRealDBDeps(t)
	registerImport(mux, d)
	if _, err := d.Db.Exec(`INSERT INTO users (id, username, display_name, role) VALUES ('m1', 'mgr-join', 'Mgr', 'manager')`); err != nil {
		t.Fatal(err)
	}
	return mux, d
}

func markJoined(t *testing.T, d *common.Deps, since time.Time) {
	t.Helper()
	if err := d.Settings.Set(t.Context(), catalogpull.KeyJoinSince, strconv.FormatInt(since.Unix(), 10)); err != nil {
		t.Fatal(err)
	}
}

func addActiveItem(t *testing.T, d *common.Deps) {
	t.Helper()
	if _, err := d.Db.Exec(`INSERT INTO items (id, sku, name, base_price, is_active) VALUES ('i-5098', 'SKU-5098', 'Flat white', 350, 1)`); err != nil {
		t.Fatal(err)
	}
}

func TestJoinedTillShowsSyncingStateNotTheEmptyWelcome_5098(t *testing.T) {
	mux, d := joinImportDeps(t)
	markJoined(t, d, time.Now())
	body := getImportAs(mux, "/import?welcome=1&syncing=1", mgrUser, false).Body.String()
	if !strings.Contains(body, `data-testid="join-syncing"`) || strings.Contains(body, "data-welcome-option") {
		t.Fatalf("want the syncing state and no import options:\n%.600s", body)
	}
}

func TestJoinedTillWithProductsLeavesForTheSaleScreen_5098(t *testing.T) {
	mux, d := joinImportDeps(t)
	markJoined(t, d, time.Now())
	addActiveItem(t, d)
	rec := getImportAs(mux, "/import?welcome=1&syncing=1", mgrUser, false)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("code=%d location=%q, want 303 to /", rec.Code, rec.Header().Get("Location"))
	}
}

func TestJoinedTillWithEmptyCatalogueAfterThePullShowsTheWelcome_5098(t *testing.T) {
	mux, d := joinImportDeps(t)
	markJoined(t, d, time.Now())
	if err := d.Settings.Set(t.Context(), catalogpull.KeyFirstPullDone, "1"); err != nil {
		t.Fatal(err)
	}
	body := getImportAs(mux, "/import?welcome=1&syncing=1", mgrUser, false).Body.String()
	if !strings.Contains(body, "data-welcome-option") || strings.Contains(body, "join-syncing") || strings.Contains(body, "cloud-syncing-chip") {
		t.Fatalf("a really empty shop gets the welcome without a syncing claim:\n%.600s", body)
	}
}

func TestJoinSyncGivesUpAfterTheWindow_5098(t *testing.T) {
	mux, d := joinImportDeps(t)
	markJoined(t, d, time.Now().Add(-joinSyncWindow-time.Minute))
	body := getImportAs(mux, "/import?welcome=1&syncing=1", mgrUser, false).Body.String()
	if strings.Contains(body, "join-syncing") || !strings.Contains(body, "data-welcome-option") {
		t.Fatal("an offline join must not sit on the syncing state forever")
	}
}

func TestJoinSyncPollRedirectsWhenDone_5098(t *testing.T) {
	mux, d := joinImportDeps(t)
	markJoined(t, d, time.Now())
	poll := func() *httptest.ResponseRecorder {
		req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/join-sync", nil), mgrUser)
		req.Header.Set("HX-Request", "true")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		return rec
	}
	if rec := poll(); rec.Code != http.StatusNoContent || rec.Header().Get("HX-Redirect") != "" {
		t.Fatalf("while syncing: code=%d redirect=%q, want 204 and none", rec.Code, rec.Header().Get("HX-Redirect"))
	}
	if err := d.Settings.Set(t.Context(), catalogpull.KeyFirstPullDone, "1"); err != nil {
		t.Fatal(err)
	}
	if rec := poll(); rec.Header().Get("HX-Redirect") != "/import?welcome=1" {
		t.Fatalf("empty after the pull: redirect=%q", rec.Header().Get("HX-Redirect"))
	}
	addActiveItem(t, d)
	if rec := poll(); rec.Header().Get("HX-Redirect") != "/" {
		t.Fatalf("with products: redirect=%q", rec.Header().Get("HX-Redirect"))
	}
}

func TestStandaloneSetupNeverSetsTheJoinMarker_5098(t *testing.T) {
	mux, d, _, _ := cloudWizard(t)
	rec := finalCloudPost(mux, "", func(f url.Values) {
		f.Del("cloud_paired")
		f.Del("setup_nonce")
		f.Set("store_name", "Corner Cafe")
	})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%.300s", rec.Code, rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), catalogpull.KeyJoinSince); ok {
		t.Fatal("a standalone setup recorded a join")
	}
	if joinSyncPending(t.Context(), d) {
		t.Fatal("a till that never joined is never syncing a join")
	}
}

func TestJoinSyncClockGoingBackwardsExpires_5098(t *testing.T) {
	_, d := joinImportDeps(t)
	markJoined(t, d, time.Now().Add(time.Hour))
	if joinSyncPending(t.Context(), d) {
		t.Fatal("a join time in the future must not hold the syncing state")
	}
}

func TestCloudSetupMarksTheJoin_5098(t *testing.T) {
	mux, d, _, nonce := cloudWizard(t)
	doCloudPair(mux, nonce, nil)
	rec := finalCloudPost(mux, nonce, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("code=%d body=%.300s", rec.Code, rec.Body.String())
	}
	if v, ok, _ := d.Settings.Get(t.Context(), catalogpull.KeyJoinSince); !ok || v == "" {
		t.Fatal("the wizard did not record the join")
	}
	if !joinSyncPending(t.Context(), d) {
		t.Fatal("a just-joined till is not waiting for its first pull")
	}
}
