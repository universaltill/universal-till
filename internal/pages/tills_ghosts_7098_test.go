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
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#7098: the owner's real Mac (2026-11-27): mac-till (this), android-till
// and phone-till2 live; "phone till" and "iphone-till" left over.

func othersBody(t *testing.T, mux *http.ServeMux, u *auth.User) string {
	t.Helper()
	resetFleetCache()
	req := httptest.NewRequest(http.MethodGet, "/ui/tills/section/others?refresh=1", nil)
	if u != nil {
		req = auth.WithUser(req, *u)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("others tab: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// oldBody is the Old entries section (the tree's home of the old entries).
func oldBody(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := tabsGet(mux, "/ui/tills/section/old?refresh=1")
	if rec.Code != http.StatusOK {
		t.Fatalf("old section: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func seedTill(t *testing.T, dp *common.Deps, name string, ago time.Duration) string {
	t.Helper()
	id, err := data.NewTillsRepo(dp.Db).InsertTill(context.Background(), name, hashBearer("b-"+name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-ago).UTC().Format(time.RFC3339), id); err != nil {
		t.Fatal(err)
	}
	return id
}

func stubFleet(t *testing.T, devs []enroll.Device, removed []enroll.RemovedTill) {
	t.Helper()
	old := tillsFleetFetch
	tillsFleetFetch = func(context.Context, *common.Deps) ([]enroll.Device, error) {
		noteRemovedFromFetch(removed)
		return devs, nil
	}
	t.Cleanup(func() { tillsFleetFetch = old; resetFleetCache() })
}

// TestOthersBannerOnlyWhenActionable: one old entry raises the banner with the
// plain-words text; a till that just joined raises nothing.
func TestOthersBannerOnlyWhenActionable(t *testing.T) {
	mux, dp := tillsTabsMux(t, nil, nil)
	android := seedTill(t, dp, "android-till", 2*time.Minute)
	phone2 := seedTill(t, dp, "phone-till2", 3*time.Minute)
	ghost := seedTill(t, dp, "phone till", 49*time.Hour)
	stubFleet(t, []enroll.Device{{DeviceID: "d1", TillID: android, Name: "android-till"}, {DeviceID: "d2", TillID: phone2, Name: "phone-till2"}},
		[]enroll.RemovedTill{{DeviceID: "d0", TillID: ghost}})
	body := othersBody(t, mux, nil)
	for _, want := range []string{`data-testid="tills-old-banner"`, "Some tills need tidying", "1 old entry is left over", "Remove old entry"} {
		if !strings.Contains(body, want) {
			t.Errorf("Other tills lacks %q", want)
		}
	}
	// The Old entries section lists it with its one Remove (revoke route).
	ob := oldBody(t, mux)
	if !strings.Contains(ob, `id="till-row-`+ghost+`"`) || !strings.Contains(ob, `data-kind="old"`) || !strings.Contains(ob, "/api/sync/tills/"+ghost+"/revoke") {
		t.Errorf("the old entry must be listed with its Remove: %s", ob)
	}

	// Only a till that joined a minute ago and is not in the list yet.
	mux2, dp2 := tillsTabsMux(t, nil, nil)
	seedTill(t, dp2, "new-phone", time.Minute)
	stubFleet(t, []enroll.Device{{DeviceID: "dz", TillID: "someone", Name: "someone"}}, nil)
	body = othersBody(t, mux2, nil)
	if strings.Contains(body, `data-testid="tills-old-banner"`) || strings.Contains(body, "Remove old entry") {
		t.Fatalf("a just-joined till must raise no banner and no Remove: %s", body)
	}
	if !strings.Contains(body, "new-phone") {
		t.Fatal("the new till must still be listed")
	}
	if ob := oldBody(t, mux2); strings.Contains(ob, "new-phone") {
		t.Fatalf("a just-joined till is not an old entry: %s", ob)
	}
}

// TestOthersNeverMentionsMyLink: no jargon, no link out.
func TestOthersNeverMentionsMyLink(t *testing.T) {
	mux, dp := tillsTabsMux(t, nil, nil)
	seedTill(t, dp, "old one", 49*time.Hour)
	stubFleet(t, []enroll.Device{{DeviceID: "g1", TillID: "ghost-1", Name: "ghost one"}}, nil)
	body := strings.ToLower(othersBody(t, mux, nil) + oldBody(t, mux))
	for _, bad := range []string{"my.universaltill.com", "disagree", "not added here", "your account"} {
		if strings.Contains(body, bad) {
			t.Errorf("Other tills page still says %q", bad)
		}
	}
}

// TestOwnerFourRowCase: the new cloud serves 3 devices: no banner, nothing
// odd. The old cloud serves 5: the two extras say "Nothing to do", no link.
func TestOwnerFourRowCase(t *testing.T) {
	mux, dp := tillsTabsMux(t, nil, nil)
	android := seedTill(t, dp, "android-till", time.Minute)
	phone2 := seedTill(t, dp, "phone-till2", time.Minute)
	three := []enroll.Device{{DeviceID: "dm", Name: "mac-till"}, {DeviceID: "da", TillID: android, Name: "android-till"}, {DeviceID: "dp", TillID: phone2, Name: "phone-till2"}}
	stubFleet(t, three, nil)
	body := othersBody(t, mux, nil)
	if !strings.Contains(body, "android-till") || !strings.Contains(body, "phone-till2") || !strings.Contains(body, `data-testid="tills-nothing-needed"`) {
		t.Fatalf("new cloud: want both tills listed and 'nothing needed': %s", body)
	}
	if strings.Contains(body, `data-testid="tills-old-banner"`) {
		t.Fatal("new cloud (3 devices): the banner must be gone")
	}
	if ob := oldBody(t, mux); !strings.Contains(ob, `data-testid="tills-old-none"`) {
		t.Fatalf("new cloud: nothing to clean up: %s", ob)
	}

	five := append(append([]enroll.Device{}, three...),
		enroll.Device{DeviceID: "x1", TillID: "ghost-a", Name: "phone till"},
		enroll.Device{DeviceID: "x2", TillID: "ghost-b", Name: "iphone-till"})
	stubFleet(t, five, nil)
	body = othersBody(t, mux, nil)
	ob := oldBody(t, mux)
	if strings.Count(ob, `data-kind="cloud"`) != 2 || strings.Count(ob, "Nothing to do. This does not affect selling.") != 2 || strings.Contains(ob, "data-pin-open") {
		t.Fatalf("old cloud: want two account-only entries saying Nothing to do and no Remove: %s", ob)
	}
	if strings.Contains(body+ob, "my.universaltill.com") || strings.Contains(body, `data-testid="tills-old-banner"`) {
		t.Fatal("old cloud: no link out and no banner for rows with nothing to do")
	}
}

// TestRemoveOldEntryRemovesRowAndBanner: a manager PIN removes the entry, its
// register is retired, and the banner is gone on the next read.
func TestRemoveOldEntryRemovesRowAndBanner(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	dp.AuthSvc = auth.NewService(dp.Db)
	seed3134Users(t, dp.Db)
	out := parseEnrol(t, enrolForTest(t, mux, mintEnrolTokenForTest(t, mux), "phone till", devA))
	quietTill(t, dp, out.TillID)
	t.Setenv("UT_AUTH", "")
	stubFleet(t, nil, []enroll.RemovedTill{{DeviceID: devA, TillID: out.TillID}})

	if body := othersBody(t, mux, &manager3134); !strings.Contains(body, `data-testid="tills-old-banner"`) {
		t.Fatalf("banner expected before: %s", body)
	}
	rec := postRemoval(mux, cashier3134, "/api/sync/tills/"+out.TillID+"/revoke", url.Values{"manager_pin": {perm3134MgrPIN}})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("remove: %d %s", rec.Code, rec.Body.String())
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM tills`); n != 0 {
		t.Fatalf("tills rows = %d, want 0", n)
	}
	if n := rowCount(t, dp, `SELECT is_active FROM registers WHERE id = ?`, out.RegisterID); n != 0 {
		t.Fatal("the register must be retired")
	}
	if body := othersBody(t, mux, &manager3134); strings.Contains(body, `data-testid="tills-old-banner"`) {
		t.Fatalf("banner must be gone after the removal: %s", body)
	}
}

// TestCloudRemoveRequiresFreshListedRow: the till side of the cloud remove.
func TestCloudRemoveRequiresFreshListedRow(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newSyncAPITestDeps(t)
	dp.AuthSvc = auth.NewService(dp.Db)
	seed3134Users(t, dp.Db)
	t.Setenv("UT_AUTH", "")
	stubFleet(t, []enroll.Device{{DeviceID: "dev-ghost", TillID: "ghost-1", Name: "phone till"}}, nil)
	calls := 0
	var callErr error
	old := tillsCloudRemoveCall
	tillsCloudRemoveCall = func(_ context.Context, _ *config.Config, id string) error { calls++; return callErr }
	t.Cleanup(func() { tillsCloudRemoveCall = old })
	post := func(u auth.User, id string, pin string) *httptest.ResponseRecorder {
		f := url.Values{"device_id": {id}}
		if pin != "" {
			f.Set("manager_pin", pin)
		}
		return postRemoval(mux, u, "/api/sync/tills/cloud-remove", f)
	}

	if rec := post(manager3134, "dev-ghost", perm3134MgrPIN); rec.Code != http.StatusNotFound || calls != 0 {
		t.Fatalf("flag off: %d calls=%d, want 404 and 0 calls", rec.Code, calls)
	}
	// flag off: a cashier with a wrong PIN gets 404, the PIN is never checked
	// (no lockout attempt burned on a call that cannot work).
	if rec := post(cashier3134, "dev-ghost", "0000"); rec.Code != http.StatusNotFound || calls != 0 {
		t.Fatalf("flag off + wrong PIN: %d calls=%d, want 404 and 0 calls", rec.Code, calls)
	}
	if err := dp.Settings.Set(context.Background(), settingCloudRemove, "1"); err != nil {
		t.Fatal(err)
	}
	if rec := post(cashier3134, "dev-ghost", ""); rec.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("cashier without PIN: %d calls=%d, want 403 and 0 calls", rec.Code, calls)
	}
	// One PIN rule (ut-docs#7173): a signed-in manager types a PIN too.
	if rec := post(manager3134, "dev-ghost", ""); rec.Code != http.StatusForbidden || calls != 0 {
		t.Fatalf("manager session without PIN: %d calls=%d, want 403 and 0 calls", rec.Code, calls)
	}
	if rec := post(manager3134, "dev-not-listed", perm3134MgrPIN); rec.Code != http.StatusConflict || calls != 0 {
		t.Fatalf("unlisted id: %d calls=%d, want 409 and 0 calls", rec.Code, calls)
	}
	callErr = errors.New("offline")
	rec := post(manager3134, "dev-ghost", perm3134MgrPIN)
	if rec.Code != http.StatusBadGateway || calls != 1 || !strings.Contains(rec.Body.String(), "Could not reach your shop list. Nothing was changed. Try again when the till is online.") {
		t.Fatalf("offline: %d calls=%d body=%q", rec.Code, calls, rec.Body.String())
	}
	// cloud answers 404 not_enabled: plain words, marked as the operator's reason
	callErr = enroll.ErrRemoveNotEnabled
	rec = post(manager3134, "dev-ghost", perm3134MgrPIN)
	if rec.Code != http.StatusBadGateway || rec.Header().Get("X-UT-Response") != "refused" ||
		!strings.Contains(rec.Body.String(), "Removing entries from your shop list is not switched on yet, so nothing was changed.") {
		t.Fatalf("not enabled: %d hdr=%q body=%q", rec.Code, rec.Header().Get("X-UT-Response"), rec.Body.String())
	}
	callErr = nil
	if rec := post(cashier3134, "dev-ghost", perm3134MgrPIN); rec.Code != http.StatusNoContent || calls != 3 {
		t.Fatalf("listed + manager PIN: %d calls=%d, want 204 and 3 calls", rec.Code, calls)
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM audit_log WHERE action = 'till_cloud_removed' AND actor_id = 'mgr1'`); n != 1 {
		t.Fatalf("audit rows till_cloud_removed by the PIN's manager = %d, want 1", n)
	}
}

// TestRejoinWithoutApprovalNeverReplaces (AC13): a join that names the device id
// of a LIVE row, with no manager choice, never touches that row.
func TestRejoinWithoutApprovalNeverReplaces(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp, _ := newPairingAPITestDeps(t)
	tok, _ := pairRequestAndApprove(t, mux, dp, "Phone", devA, "", "")
	first := parseEnrol(t, enrolForTest(t, mux, tok, "Phone", devA))
	if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = ? WHERE id = ?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), first.TillID); err != nil {
		t.Fatal(err)
	}
	tok, _ = pairRequestAndApprove(t, mux, dp, "Impostor", devA, "", "")
	second := parseEnrol(t, enrolForTest(t, mux, tok, "Impostor", devA))
	if second.TillID == first.TillID {
		t.Fatal("a join with no manager choice replaced a live row")
	}
	var name string
	if err := dp.Db.QueryRow(`SELECT name FROM tills WHERE id = ?`, first.TillID).Scan(&name); err != nil || name != "Phone" {
		t.Fatalf("live row name = %q err=%v, want Phone", name, err)
	}
	if n := rowCount(t, dp, `SELECT COUNT(*) FROM tills`); n != 2 {
		t.Fatalf("tills rows = %d, want 2", n)
	}
}
