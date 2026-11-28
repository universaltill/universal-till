package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/tills"
)

// ut-docs#7173: the Old entries derivation, the "same phone" hint, the banner
// sentence and the account-side removal route.

func seenAgo(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }

// The owner's real case: "phone till" and "iphone-till" are ghosts of the one
// phone that is now "phone-till2". All three kinds of old entry are listed,
// and both ghosts point at the live phone.
func TestOldEntriesFrom_ThreeKindsAndTheSamePhoneHint(t *testing.T) {
	live := tills.Row{ID: "t3", Name: "phone-till2", InLocal: true, InCloud: true, State: tills.StateOK, LastSeen: seenAgo(time.Hour)}
	o := tillsOthers{
		CloudOK: true,
		Rows: []tills.Row{
			{ID: "t0", Name: "mac-till", IsSelf: true, State: tills.StateOK, InLocal: true, InCloud: true},
			live,
		},
		Stale: []tills.Row{
			{ID: "g1", Name: "iphone-till", InLocal: true, State: tills.StateOldEntry, LongUnseen: true, LastSeen: seenAgo(96 * time.Hour)},
			{ID: "g2", Name: "week tablet", InLocal: true, State: tills.StateLocalOnly, LastSeen: seenAgo(48 * time.Hour)},
		},
		CloudOnly: []tills.Row{
			{ID: "g3", Name: "phone till", InCloud: true, State: tills.StateCloudOnly, LastSeen: seenAgo(72 * time.Hour)},
		},
	}
	o.Rows = append(o.Rows, o.Stale...)
	o.Rows = append(o.Rows, o.CloudOnly...)

	got := oldEntriesFrom(o)
	if len(got) != 3 {
		t.Fatalf("want 3 entries, got %d: %+v", len(got), got)
	}
	byID := map[string]oldEntry{}
	for _, e := range got {
		byID[e.ID] = e
	}
	if byID["g1"].Kind != oldKindOld || byID["g2"].Kind != oldKindLocal || byID["g3"].Kind != oldKindCloud {
		t.Fatalf("kinds wrong: %+v", got)
	}
	// Cloud-only entries come last.
	if got[len(got)-1].ID != "g3" {
		t.Fatalf("cloud-only entry must be listed last: %+v", got)
	}
	for _, id := range []string{"g1", "g3"} {
		if byID[id].SameAsName != "phone-till2" || byID[id].SameAsID != "t3" {
			t.Errorf("%s should point at the live phone, got %+v", id, byID[id])
		}
	}
	// Same name as a live till is never a hint (that is the same entry).
	if h := sameDeviceHint(tills.Row{Name: "PHONE-TILL2", LastSeen: seenAgo(96 * time.Hour)}, o.Rows); h != nil {
		t.Errorf("a ghost with the live till's own name is not 'another name': %+v", h)
	}
	// A till seen BEFORE the ghost cannot be its replacement; neither can this till.
	if h := sameDeviceHint(tills.Row{Name: "x", LastSeen: seenAgo(0)}, o.Rows); h != nil {
		t.Errorf("a live till older than the ghost is not a replacement: %+v", h)
	}
	if h := sameDeviceHint(tills.Row{Name: "x"}, o.Rows); h != nil {
		t.Errorf("no last-seen, no hint: %+v", h)
	}
}

// Account removal is a seam until the cloud call exists: the cloud-only entry
// is honest about it (no Remove button), the local ones stay removable.
func TestOldEntries_CloudOnlyIsNotRemovableWhileTheSeamIsAStub(t *testing.T) {
	old := accountRemovalReady
	t.Cleanup(func() { accountRemovalReady = old })

	o := tillsOthers{CloudOK: true,
		Stale:     []tills.Row{{ID: "a", Name: "A", State: tills.StateLocalOnly}},
		CloudOnly: []tills.Row{{ID: "b", Name: "B", State: tills.StateCloudOnly}}}
	accountRemovalReady = false
	e := oldEntriesFrom(o)
	if !e[0].Removable || e[1].Removable {
		t.Fatalf("stub: local removable, cloud not: %+v", e)
	}
	if urls := oldRemoveURLs(e); urls != "/api/sync/tills/a/revoke" {
		t.Fatalf("Remove all must only offer what can be removed now, got %q", urls)
	}
	accountRemovalReady = true
	e = oldEntriesFrom(o)
	if !e[1].Removable || e[1].RemoveURL() != "/api/sync/tills/b/remove-from-account" {
		t.Fatalf("ready: cloud entry removed through the account route, got %+v", e[1])
	}
	if urls := oldRemoveURLs(e); urls != "/api/sync/tills/a/revoke /api/sync/tills/b/remove-from-account" {
		t.Fatalf("Remove all urls = %q", urls)
	}
}

// The banner names the entries in plain words, joins them with "and", and
// carries the live phone's name; it never says "disagree".
func TestOldBanner_NamesTheEntriesAndThePhone(t *testing.T) {
	b := oldBannerFrom([]oldEntry{
		{Name: "phone till", Removable: true, SameAsName: "phone-till2"},
		{Name: "iphone-till", Removable: true, SameAsName: "phone-till2"},
	}, "en", "and")
	if b.Count != 2 || b.Removable != 2 || b.Phone != "phone-till2" || b.Pending {
		t.Fatalf("banner = %+v", b)
	}
	if b.Names != "“phone till” and “iphone-till”" {
		t.Fatalf("names = %q", b.Names)
	}
	b = oldBannerFrom([]oldEntry{{Name: "a", Removable: false}}, "en", "and")
	if !b.Pending || b.Removable != 0 || b.Names != "“a”" {
		t.Fatalf("pending banner = %+v", b)
	}
}

func postSection(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	return rec
}

// POST /api/sync/tills/{id}/remove-from-account: answers honestly (501 with
// words) while the cloud call does not exist, and never touches the cloud.
func TestRemoveFromAccount_HonestWhileTheSeamIsAStub(t *testing.T) {
	old := accountRemovalReady
	t.Cleanup(func() { accountRemovalReady = old })
	accountRemovalReady = false
	mux, _ := tillsTabsMux(t, nil, nil)
	rec := postSection(mux, "/api/sync/tills/ghost-1/remove-from-account")
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "next update") {
		t.Fatalf("stub = %d %q, want 501 saying it arrives in the next update", rec.Code, rec.Body.String())
	}
}

// A joined (additional) till cannot remove account entries: only the main
// till speaks for the shop.
func TestRemoveFromAccount_RefusedOnAJoinedTill(t *testing.T) {
	mux, dp := tillsTabsMux(t, nil, nil)
	if err := dp.Settings.Set(t.Context(), "sync.primary_url", "http://main.local"); err != nil {
		t.Fatal(err)
	}
	if rec := postSection(mux, "/api/sync/tills/ghost-1/remove-from-account"); rec.Code != http.StatusConflict {
		t.Fatalf("joined till = %d, want 409", rec.Code)
	}
}

func postPIN(mux *http.ServeMux, path, pin string, user *auth.User) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(url.Values{"override_pin": {pin}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if user != nil {
		req = auth.WithUser(req, *user)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The inline PIN step: a removal waits for POST /api/sync/tills/verify-pin.
// No PIN and a wrong PIN are refused with words (401, in the person's
// language), the manager's own PIN passes (204), a cashier session never
// reaches the PIN check.
func TestVerifyRemovalPIN_ManagerPinRequiredInline(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	registerTillsTabs(mux, dp)

	if rec := postPIN(mux, "/api/sync/tills/verify-pin", "", nil); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "Invalid PIN") {
		t.Fatalf("no PIN = %d %q, want 401 Invalid PIN", rec.Code, rec.Body.String())
	}
	if rec := postPIN(mux, "/api/sync/tills/verify-pin", "000000", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong PIN = %d, want 401", rec.Code)
	}
	if rec := postPIN(mux, "/api/sync/tills/verify-pin", dataAPITestManagerPIN, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("manager PIN = %d %q, want 204", rec.Code, rec.Body.String())
	}
}

func TestVerifyRemovalPIN_CashierSessionIsRefusedBeforeAnyPinLookup(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp := newDataAPITestDeps(t)
	registerTillsTabs(mux, dp)
	cashier := auth.User{ID: "c1", Role: "cashier"}
	if rec := postPIN(mux, "/api/sync/tills/verify-pin", dataAPITestManagerPIN, &cashier); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier = %d, want 403 even with a right PIN", rec.Code)
	}
}

// The account-side removal checks the PIN itself (it has no other guard).
func TestRemoveFromAccount_AsksForTheManagerPin(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	old, oldRemove := accountRemovalReady, removeFromAccount
	t.Cleanup(func() { accountRemovalReady, removeFromAccount = old, oldRemove })
	accountRemovalReady = true
	called := ""
	removeFromAccount = func(_ context.Context, _ *common.Deps, id string) error { called = id; return nil }

	mux, dp := newDataAPITestDeps(t)
	registerTillsTabs(mux, dp)
	resetFleetCache()
	tillsFleetFetch = func(context.Context, *common.Deps) ([]enroll.Device, error) {
		return []enroll.Device{{DeviceID: "d1", TillID: "ghost-1", Name: "phone till"}}, nil
	}
	t.Cleanup(func() { resetFleetCache() })

	if rec := postPIN(mux, "/api/sync/tills/ghost-1/remove-from-account", "000000", nil); rec.Code != http.StatusUnauthorized || called != "" {
		t.Fatalf("wrong PIN = %d (removed %q), want 401 and nothing removed", rec.Code, called)
	}
	if rec := postPIN(mux, "/api/sync/tills/ghost-1/remove-from-account", dataAPITestManagerPIN, nil); rec.Code != http.StatusNoContent || called != "ghost-1" {
		t.Fatalf("right PIN = %d (removed %q), want 204 and ghost-1 removed", rec.Code, called)
	}
	if rec := postPIN(mux, "/api/sync/tills/not-listed/remove-from-account", dataAPITestManagerPIN, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("an entry the account does not list = %d, want 404", rec.Code)
	}
}

// seedGhosts enrols a live phone and two ghosts of it, and lists the live one
// in the account: the owner's real case (two ghosts for one phone).
func seedGhostsForOneLivePhone(t *testing.T) (*http.ServeMux, string, string) {
	mux, _, g1, g2 := seedGhostsForOneLivePhoneAll(t)
	return mux, g1, g2
}

func seedGhostsForOneLivePhoneDeps(t *testing.T) (*http.ServeMux, *common.Deps) {
	mux, dp, _, _ := seedGhostsForOneLivePhoneAll(t)
	return mux, dp
}

func seedGhostsForOneLivePhoneAll(t *testing.T) (*http.ServeMux, *common.Deps, string, string) {
	t.Helper()
	mux, dp := tillsTabsMux(t, []enroll.Device{{DeviceID: "dev-live", TillID: "live", Name: "phone-till2"}}, nil)
	ctx := t.Context()
	repo := data.NewTillsRepo(dp.Db)
	live, _ := repo.InsertTill(ctx, "phone-till2", hashBearer("l"))
	if _, err := dp.Db.Exec(`UPDATE tills SET id = 'live', last_seen_at = ? WHERE id = ?`, seenAgo(time.Hour), live); err != nil {
		t.Fatal(err)
	}
	g1, _ := repo.InsertTill(ctx, "phone till", hashBearer("g1"))
	g2, _ := repo.InsertTill(ctx, "iphone-till", hashBearer("g2"))
	for id, ago := range map[string]time.Duration{g1: 90 * time.Hour, g2: 60 * time.Hour} {
		if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = ? WHERE id = ?`, seenAgo(ago), id); err != nil {
			t.Fatal(err)
		}
	}
	return mux, dp, g1, g2
}

// The removal asks for the PIN inline: an opener button, a password field in a
// panel under it, and none of the things the till bans on its surfaces.
func TestOldSection_RemovalIsAnInlinePinPanelNotADialog(t *testing.T) {
	mux, g1, g2 := seedGhostsForOneLivePhone(t)
	body := tabsGet(mux, "/ui/tills/section/old").Body.String()
	for _, id := range []string{g1, g2} {
		for _, want := range []string{
			`aria-controls="pin-` + id + `-remove"`,
			`id="pin-` + id + `-remove"`,
			`data-remove-urls="/api/sync/tills/` + id + `/revoke"`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("missing %s in the old-entries section", want)
			}
		}
	}
	for _, want := range []string{`type="password"`, `name="override_pin"`, `data-pin-confirm`, `data-pin-cancel`, "Manager PIN", `data-pin-panel hidden`} {
		if !strings.Contains(body, want) {
			t.Errorf("PIN panel lacks %q", want)
		}
	}
	for _, banned := range []string{"hx-confirm", "<dialog", "showModal", "window.confirm"} {
		if strings.Contains(body, banned) {
			t.Errorf("the removal must not use %s", banned)
		}
	}
	// "Might be the same phone" is offered as a hint to replace, never a fact.
	if !strings.Contains(body, "same phone as phone-till2") {
		t.Errorf("same-phone replace button missing: %s", body)
	}
	if !strings.Contains(body, `data-testid="tills-remove-all"`) {
		t.Errorf("two removable entries must offer Remove all")
	}
}

// The Other tills banner says in plain words what is wrong, names the old
// entries and the phone, carries the one button that fixes it, and has no
// link out to my.universaltill.com and no "disagree".
func TestOthersSection_BannerIsPlainWordsWithOneFixButton(t *testing.T) {
	mux, _, _ := seedGhostsForOneLivePhone(t)
	body := tabsGet(mux, "/ui/tills/section/others").Body.String()
	for _, want := range []string{`data-testid="tills-old-banner"`, "Some tills need tidying", "phone-till2", `data-testid="tills-banner-remove"`, "Remove 2 old entries", `data-pin-panel hidden`} {
		if !strings.Contains(body, want) {
			t.Errorf("banner lacks %q", want)
		}
	}
	low := strings.ToLower(body)
	if strings.Contains(low, "disagree") || strings.Contains(low, "my.universaltill.com") {
		t.Errorf("banner must not use 'disagree' or send the owner to another site")
	}
	if strings.Contains(body, `data-testid="tills-nothing-needed"`) {
		t.Errorf("'nothing needed' must not show beside the banner")
	}
}

// Nothing wrong: the section says so instead of showing an empty banner.
func TestOthersSection_NothingNeededWhenAccountAndNetworkAgree(t *testing.T) {
	mux, dp := tillsTabsMux(t, []enroll.Device{{DeviceID: "dev-a", TillID: "a", Name: "Till A"}}, nil)
	repo := data.NewTillsRepo(dp.Db)
	id, _ := repo.InsertTill(t.Context(), "Till A", hashBearer("a"))
	if _, err := dp.Db.Exec(`UPDATE tills SET id = 'a', last_seen_at = ? WHERE id = ?`, seenAgo(time.Minute), id); err != nil {
		t.Fatal(err)
	}
	body := tabsGet(mux, "/ui/tills/section/others").Body.String()
	if !strings.Contains(body, `data-testid="tills-nothing-needed"`) || strings.Contains(body, `data-testid="tills-old-banner"`) {
		t.Fatalf("want the 'nothing needed' state: %s", body)
	}
}

// The account list cannot be read: nothing is called old, and the section says
// it cannot check (with a Try again), never "all fine".
func TestOldSection_AccountUnreadableIsNeverCalledFine(t *testing.T) {
	mux, _ := tillsTabsMux(t, nil, errors.New("offline"))
	body := tabsGet(mux, "/ui/tills/section/old").Body.String()
	if strings.Contains(body, `data-testid="tills-old-none"`) || strings.Contains(body, "till-old-entry") {
		t.Fatalf("an unreadable account must not read as nothing to clean up: %s", body)
	}
}

// The tree's count tag follows the cached list only (no network on /settings)
// and the retired words stay out of every new English string.
func TestTillsTree_NoMasterSlaveWording(t *testing.T) {
	chdirRoot(t)
	raw, err := os.ReadFile("web/locales/en.json")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for k, v := range m {
		if !strings.HasPrefix(k, "tills.") && !strings.HasPrefix(k, "settings.tills") {
			continue
		}
		low := strings.ToLower(v)
		if strings.Contains(low, "master") || strings.Contains(low, "slave") {
			t.Errorf("%s uses retired wording: %q", k, v)
		}
	}
}

// The tree's count tag comes from the local roster and the CACHED account
// list only: unknown account list is 0 (never a fetch, never a guess), a
// loaded one counts the ghosts.
func TestOldEntriesCountCached_NeverFetches(t *testing.T) {
	mux, dp := seedGhostsForOneLivePhoneDeps(t)
	fetches := 0
	tillsFleetFetch = func(context.Context, *common.Deps) ([]enroll.Device, error) {
		fetches++
		return []enroll.Device{{DeviceID: "dev-live", TillID: "live", Name: "phone-till2"}}, nil
	}
	resetFleetCache()
	if n := oldEntriesCountCached(t.Context(), dp); n != 0 || fetches != 0 {
		t.Fatalf("cold cache: count=%d fetches=%d, want 0 and no fetch", n, fetches)
	}
	tabsGet(mux, "/ui/tills/section/old") // loads and caches the account list
	if fetches != 1 {
		t.Fatalf("section load fetched %d times, want 1", fetches)
	}
	if n := oldEntriesCountCached(t.Context(), dp); n != 2 || fetches != 1 {
		t.Fatalf("warm cache: count=%d fetches=%d, want 2 and still 1 fetch", n, fetches)
	}
}

// The live roster's Revoke asks the same way: the PIN inline, no confirm.
func TestRoster_RevokeUsesTheInlinePinPanel(t *testing.T) {
	mux, dp := tillsTabsMux(t, nil, nil)
	id, err := data.NewTillsRepo(dp.Db).InsertTill(t.Context(), "Counter", hashBearer("c"))
	if err != nil {
		t.Fatal(err)
	}
	body := tabsGet(mux, "/ui/tills/section/others").Body.String()
	i := strings.Index(body, `aria-controls="pin-`+id+`-revoke"`)
	if i < 0 {
		t.Fatalf("no inline PIN opener on the Revoke button: %s", body)
	}
	if !strings.Contains(body, `data-remove-urls="/api/sync/tills/`+id+`/revoke"`) {
		t.Fatalf("the panel must post the existing revoke route")
	}
	if strings.Contains(body, `hx-post="/api/sync/tills/`+id+`/revoke"`) {
		t.Fatalf("Revoke must not post straight from the button any more")
	}
}

// Review 7173: the manager PIN is enforced by the revoke route itself, so a
// direct POST (no browser, no inline panel) cannot skip it.
func TestRevoke_EnforcesTheManagerPinServerSide(t *testing.T) {
	mux, dp, g1, _ := seedGhostsForOneLivePhoneAll(t)
	repo := data.NewTillsRepo(dp.Db)
	has := func() bool {
		rows, _ := repo.ListTills(t.Context())
		for _, r := range rows {
			if r.ID == g1 {
				return true
			}
		}
		return false
	}
	path := "/api/sync/tills/" + g1 + "/revoke"
	if rec := postPIN(mux, path, "", nil); rec.Code != http.StatusUnauthorized || !has() {
		t.Fatalf("no PIN = %d (till kept=%v), want 401 and the till kept", rec.Code, has())
	}
	if rec := postPIN(mux, path, "000000", nil); rec.Code != http.StatusUnauthorized || !has() {
		t.Fatalf("wrong PIN = %d (till kept=%v), want 401 and the till kept", rec.Code, has())
	}
	if rec := postPIN(mux, path, dataAPITestManagerPIN, nil); rec.Code != http.StatusNoContent || has() {
		t.Fatalf("right PIN = %d (till kept=%v), want 204 and the till gone", rec.Code, has())
	}
}
