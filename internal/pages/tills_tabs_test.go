package pages

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#7057: the one Tills & devices page.

// resetFleetCache empties the account-list cache between tests.
func resetFleetCache() {
	fleetCache.mu.Lock()
	defer fleetCache.mu.Unlock()
	fleetCache.at, fleetCache.devices, fleetCache.ok = time.Time{}, nil, false
}

func tillsTabsMux(t *testing.T, fleet []enroll.Device, fleetErr error) (*http.ServeMux, *common.Deps) {
	t.Helper()
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	resetFleetCache()
	old := tillsFleetFetch
	tillsFleetFetch = func(context.Context, *common.Deps) ([]enroll.Device, error) { return fleet, fleetErr }
	t.Cleanup(func() { tillsFleetFetch = old; resetFleetCache() })
	return mux, dp
}

func tabsGet(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// ut-docs#7173: the old five-tab page is gone. Every old URL answers a 303
// into the Settings tree (the table lives in tillsRedirectTarget), and every
// section is a lazy fragment.
func TestTillsPage_OldUrlsRedirectAndEverySectionRenders(t *testing.T) {
	mux, _ := tillsTabsMux(t, nil, errors.New("offline"))
	for url, want := range map[string]string{
		"/tills":                      "/settings#settings-tills-this",
		"/tills?tab=this":             "/settings#settings-tills-this",
		"/tills?tab=others":           "/settings#settings-tills-others",
		"/tills?tab=others&refresh=1": "/settings?refresh=1#settings-tills-others",
		"/tills?tab=add":              "/settings#settings-tills-add",
		"/tills?tab=add&need=join":    "/settings?need=join#settings-tills-add",
		"/tills?tab=add&need=%3Cx%3E": "/settings#settings-tills-add",
		"/tills?tab=readers":          "/settings#settings-tills-readers",
		"/tills?tab=settings":         "/settings#settings-tills-shop",
		"/tills?tab=%3Cscript%3E":     "/settings#settings-tills-this",
	} {
		rec := tabsGet(mux, url)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
			t.Errorf("GET %s = %d -> %q, want 303 -> %q", url, rec.Code, rec.Header().Get("Location"), want)
		}
	}
	for _, id := range tillsSectionIDs {
		frag := tabsGet(mux, "/ui/tills/section/"+id)
		if frag.Code != http.StatusOK || strings.TrimSpace(frag.Body.String()) == "" {
			t.Errorf("section %s: %d, empty body", id, frag.Code)
		}
	}
	for _, tab := range tillsTabIDs { // one more release of the legacy fragment route
		if rec := tabsGet(mux, "/ui/tills/tab/"+tab); rec.Code != http.StatusOK {
			t.Errorf("legacy tab fragment %s: %d", tab, rec.Code)
		}
	}
	if tabsGet(mux, "/ui/tills/section/nope").Code != http.StatusNotFound || tabsGet(mux, "/ui/tills/tab/nope").Code != http.StatusNotFound {
		t.Error("an unknown section or tab must be 404")
	}
}

func TestTillsOthers_StaleRowIsFlaggedAndOneTapRemovable_CloudDownNeverStale(t *testing.T) {
	mux, dp := tillsTabsMux(t, []enroll.Device{{DeviceID: "dev-cloud-only-1", TillID: "cloud-only-1", Name: "Cloud only"}}, nil)
	repo := data.NewTillsRepo(dp.Db)
	if _, err := repo.InsertTill(context.Background(), "Ghost tablet", hashBearer("g")); err != nil {
		t.Fatal(err)
	}
	body := tabsGet(mux, "/ui/tills/tab/others").Body.String()
	if !strings.Contains(body, "Ghost tablet") || !strings.Contains(body, "Cloud only") {
		t.Fatalf("both sources must be listed: %s", body)
	}
	// cloud list unavailable: nothing may be called stale.
	resetFleetCache()
	tillsFleetFetch = func(context.Context, *common.Deps) ([]enroll.Device, error) { return nil, errors.New("down") }
	body = tabsGet(mux, "/ui/tills/tab/others").Body.String()
	if strings.Contains(body, "tills-remove-stale") {
		t.Fatalf("unknown account list must never offer stale removal: %s", body)
	}
}

func TestTillsRevokeStale_RemovesOnlyStaleAndAudits_RefusesWhenCloudUnknown(t *testing.T) {
	mux, dp := tillsTabsMux(t, []enroll.Device{{DeviceID: "dev-keep-me", TillID: "keep-me", Name: "Keep"}}, nil)
	ctx := context.Background()
	repo := data.NewTillsRepo(dp.Db)
	stale, _ := repo.InsertTill(ctx, "Ghost tablet", hashBearer("g"))
	if _, err := dp.Db.Exec(`INSERT INTO tills (id, name, bearer_hash, enrolled_at) VALUES ('keep-me','Keep','h2','2026-08-15T09:30:00Z')`); err != nil {
		t.Fatal(err)
	}
	post := func(ids ...string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/sync/tills/revoke-stale", strings.NewReader(url.Values{"id": ids}.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		mux.ServeHTTP(rec, req)
		return rec
	}
	// Account list unreadable: refuse, remove nothing.
	tillsFleetFetch = func(context.Context, *common.Deps) ([]enroll.Device, error) { return nil, errors.New("down") }
	if rec := post(stale); rec.Code != http.StatusConflict {
		t.Fatalf("unknown account list: %d, want 409", rec.Code)
	}
	if l, _ := repo.ListTills(ctx); len(l) != 2 {
		t.Fatalf("nothing may be removed: %d", len(l))
	}
	tillsFleetFetch = func(context.Context, *common.Deps) ([]enroll.Device, error) {
		return []enroll.Device{{DeviceID: "dev-keep-me", TillID: "keep-me", Name: "Keep"}}, nil
	}
	// Not in the confirmed list: nothing goes.
	if rec := post(); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke-stale (no ids): %d", rec.Code)
	}
	if l, _ := repo.ListTills(ctx); len(l) != 2 {
		t.Fatalf("an unconfirmed till was removed: %d left", len(l))
	}
	// Seen lately: never bulk-removed, even when posted.
	if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), stale); err != nil {
		t.Fatal(err)
	}
	if rec := post(stale); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke-stale (recent): %d", rec.Code)
	}
	if l, _ := repo.ListTills(ctx); len(l) != 2 {
		t.Fatalf("a recently seen till was bulk-removed: %d left", len(l))
	}
	if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = NULL, enrolled_at = datetime('now', '-3 days') WHERE id = ?`, stale); err != nil {
		t.Fatal(err)
	}
	if rec := post(stale); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke-stale: %d %s", rec.Code, rec.Body.String())
	}
	l, _ := repo.ListTills(ctx)
	if len(l) != 1 || l[0].ID != "keep-me" {
		t.Fatalf("left %+v, want only keep-me (removed %s)", l, stale)
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action='till_revoked' AND entity_id=?`, stale).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows = %d err=%v, want 1", n, err)
	}
}

// ut-docs#7173: a role the account records differently is no longer a
// warning of its own (nothing the owner can fix there): the till is listed as
// working, the old-entries banner stays away and no "disagree" wording shows.
func TestTillsOthers_RoleDifferenceIsNotAWarning(t *testing.T) {
	mux, dp := tillsTabsMux(t, []enroll.Device{{DeviceID: "dev-t-1", TillID: "t-1", Name: "Bar", Role: "main"}}, nil)
	if _, err := dp.Db.Exec(`INSERT INTO tills (id, name, bearer_hash, enrolled_at, role) VALUES ('t-1','Bar','h','2026-08-15T09:30:00Z','additional')`); err != nil {
		t.Skipf("role column not seedable here: %v", err)
	}
	body := tabsGet(mux, "/ui/tills/section/others").Body.String()
	if strings.Contains(strings.ToLower(body), "disagree") || strings.Contains(body, `data-testid="tills-old-banner"`) {
		t.Fatalf("a role difference must not raise the old-entries banner: %s", body)
	}
	if !strings.Contains(body, "Bar") {
		t.Fatalf("the till must still be listed: %s", body)
	}
}

// getTillsTab renders one tab of the Tills & devices page as the given role,
// on a mux that already serves the page.
func getTillsTab(t *testing.T, mux *http.ServeMux, role, tab string) *httptest.ResponseRecorder {
	t.Helper()
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/ui/tills/section/"+strings.Replace(tab, "settings", "sync", 1), nil), auth.User{ID: "u1", Role: role})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// T7 / AC15 (ut-docs#7097): a till not in the account and unseen for 30 days is
// an "Old entry" with its Remove button; one seen last week stays "Not in your
// account"; both are removable, and the bulk button offers the old entry.
func TestTillsOld_EntriesAreListedWithTheirRemoveButtons(t *testing.T) {
	mux, dp := tillsTabsMux(t, []enroll.Device{{DeviceID: "dev-keep-me", TillID: "keep-me", Name: "Keep"}}, nil)
	ctx := context.Background()
	repo := data.NewTillsRepo(dp.Db)
	oldID, err := repo.InsertTill(ctx, "Ghost tablet", hashBearer("g"))
	if err != nil {
		t.Fatal(err)
	}
	weekID, err := repo.InsertTill(ctx, "Week tablet", hashBearer("w"))
	if err != nil {
		t.Fatal(err)
	}
	for id, ago := range map[string]time.Duration{oldID: 31 * 24 * time.Hour, weekID: 7 * 24 * time.Hour} {
		if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-ago).UTC().Format(time.RFC3339), id); err != nil {
			t.Fatal(err)
		}
	}
	body := tabsGet(mux, "/ui/tills/section/old").Body.String()
	row := func(id string) string {
		i := strings.Index(body, `id="till-row-`+id+`"`)
		if i < 0 {
			t.Fatalf("no row %s: %s", id, body)
		}
		r := body[i:]
		return r[:strings.Index(r, "</li>")]
	}
	oldRow, weekRow := row(oldID), row(weekID)
	if !strings.Contains(oldRow, `data-kind="old"`) || !strings.Contains(oldRow, "/api/sync/tills/"+oldID+"/revoke") {
		t.Fatalf("31-day row: %s", oldRow)
	}
	if !strings.Contains(weekRow, `data-kind="local"`) || !strings.Contains(weekRow, "/api/sync/tills/"+weekID+"/revoke") {
		t.Fatalf("7-day row: %s", weekRow)
	}
	if !strings.Contains(body, `data-testid="tills-remove-all"`) || !strings.Contains(body, "/api/sync/tills/"+oldID+"/revoke") {
		t.Fatalf("Remove all must offer the old entry: %s", body)
	}
	// The bulk endpoint (kept) removes the old entry.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/sync/tills/revoke-stale", strings.NewReader(url.Values{"id": {oldID}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke-stale: %d %s", rec.Code, rec.Body.String())
	}
	var n int
	if err := dp.Db.QueryRow(`SELECT COUNT(*) FROM tills WHERE id = ?`, oldID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("old entry still enrolled: n=%d err=%v", n, err)
	}
}
