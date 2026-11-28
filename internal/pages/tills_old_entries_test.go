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
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/tills"
)

// ut-docs#7173: the Old entries derivation, the "same phone" hint, the banner
// sentence and the account-side removal route.

func seenAgo(d time.Duration) string { return time.Now().Add(-d).UTC().Format(time.RFC3339) }

// The owner's real case: "phone till" and "iphone-till" are ghosts of the one
// phone that is now "phone-till2". All three kinds of old entry are listed, the
// device-proven ones point at the live phone (tills.MarkSameDevice, #7098), a
// till that just joined is never an old entry.
func TestOldEntriesFrom_ThreeKindsAndTheSameDeviceMark(t *testing.T) {
	o := tillsOthers{
		CloudOK: true,
		OldEntries: []tills.Row{
			{ID: "g1", Name: "iphone-till", InLocal: true, State: tills.StateOldEntry, LongUnseen: true, LastSeen: seenAgo(96 * time.Hour), SameDeviceAs: "phone-till2",
				Actions: []tills.Action{tills.ActionRemove}},
			{ID: "g2", Name: "week tablet", InLocal: true, State: tills.StateLocalOnly, LongUnseen: true, LastSeen: seenAgo(48 * time.Hour),
				Actions: []tills.Action{tills.ActionRemove}},
		},
		Stale: []tills.Row{{ID: "new", Name: "new phone", InLocal: true, State: tills.StateLocalOnly, LastSeen: seenAgo(time.Minute)}},
		CloudOnly: []tills.Row{
			{ID: "g3", Name: "phone till", InCloud: true, State: tills.StateCloudOnly, LastSeen: seenAgo(72 * time.Hour), CloudDeviceID: "dev g3&x",
				SameDeviceAs: "phone-till2", Actions: []tills.Action{tills.ActionRemoveCloud}},
		},
	}
	got := oldEntriesFrom(o)
	if len(got) != 3 {
		t.Fatalf("want 3 entries (the just-joined till is not one), got %d: %+v", len(got), got)
	}
	byID := map[string]oldEntry{}
	for _, e := range got {
		byID[e.ID] = e
	}
	if byID["g1"].Kind != oldKindOld || byID["g2"].Kind != oldKindLocal || byID["g3"].Kind != oldKindCloud {
		t.Fatalf("kinds wrong: %+v", got)
	}
	if got[len(got)-1].ID != "g3" {
		t.Fatalf("cloud-only entry must be listed last: %+v", got)
	}
	if byID["g1"].SameAsName != "phone-till2" || byID["g3"].SameAsName != "phone-till2" || byID["g2"].SameAsName != "" {
		t.Errorf("same-device marks wrong: %+v", got)
	}
}

// An account-only entry is removable only while the shop's removal is on (the
// row carries the cloud action); it goes through main's cloud-remove route,
// named by its device id; Remove all offers only what can be removed now.
func TestOldEntries_CloudOnlyRemovableOnlyWithTheCloudAction(t *testing.T) {
	o := tillsOthers{CloudOK: true,
		OldEntries: []tills.Row{{ID: "a", Name: "A", State: tills.StateLocalOnly, LongUnseen: true, Actions: []tills.Action{tills.ActionRemove}}},
		CloudOnly:  []tills.Row{{ID: "b", Name: "B", State: tills.StateCloudOnly, CloudDeviceID: "dev b&x"}}}
	e := oldEntriesFrom(o)
	if !e[0].Removable || e[1].Removable {
		t.Fatalf("setting off: local removable, cloud not: %+v", e)
	}
	if urls := oldRemoveURLs(e); urls != "/api/sync/tills/a/revoke" {
		t.Fatalf("Remove all must only offer what can be removed now, got %q", urls)
	}
	o.CloudOnly[0].Actions = []tills.Action{tills.ActionRemoveCloud}
	e = oldEntriesFrom(o)
	if !e[1].Removable || e[1].RemoveURL() != "/api/sync/tills/cloud-remove?device_id=dev+b%26x" {
		t.Fatalf("setting on: cloud entry removed through main's cloud-remove, got %+v", e[1])
	}
	if urls := oldRemoveURLs(e); urls != "/api/sync/tills/a/revoke /api/sync/tills/cloud-remove?device_id=dev+b%26x" {
		t.Fatalf("Remove all urls = %q", urls)
	}
}

// The banner names the entries it can remove in plain words, joins them with
// "and", and carries the live phone's name; entries with no button are not in it.
func TestOldBanner_NamesTheEntriesAndThePhone(t *testing.T) {
	b := oldBannerFrom([]oldEntry{
		{Name: "phone till", Removable: true, SameAsName: "phone-till2"},
		{Name: "iphone-till", Removable: true, SameAsName: "phone-till2"},
		{Name: "waiting", Removable: false},
	}, "en", "and")
	if b.Removable != 2 || b.Phone != "phone-till2" {
		t.Fatalf("banner = %+v", b)
	}
	if b.Names != "“phone till” and “iphone-till”" {
		t.Fatalf("names = %q", b.Names)
	}
	if b = oldBannerFrom([]oldEntry{{Name: "a", Removable: false}}, "en", "and"); b.Removable != 0 || b.Names != "" {
		t.Fatalf("an entry with no button must not raise the banner: %+v", b)
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

// The inline PIN step: a removal waits for POST /api/sync/tills/verify-pin. It
// is the same rule as every removal route (authorizeTillRemoval): no PIN and a
// wrong PIN are refused with words (403, in the person's language), a manager's
// PIN passes (204) whoever is signed in, a manager session alone does not.
func TestVerifyRemovalPIN_ManagerPinRequiredInline(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, dp := newDataAPITestDeps(t)
	registerTillsTabs(mux, dp)

	if rec := postPIN(mux, "/api/sync/tills/verify-pin", "", nil); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "Enter a manager PIN") {
		t.Fatalf("no PIN = %d %q, want 403 asking for a manager PIN", rec.Code, rec.Body.String())
	}
	if rec := postPIN(mux, "/api/sync/tills/verify-pin", "000000", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong PIN = %d, want 403", rec.Code)
	}
	if rec := postPIN(mux, "/api/sync/tills/verify-pin", dataAPITestManagerPIN, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("manager PIN = %d %q, want 204", rec.Code, rec.Body.String())
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
		// Same device id as the live phone: the proof tills.MarkSameDevice
		// uses (names are never compared, ut-docs#7098).
		if _, err := dp.Db.Exec(`UPDATE tills SET last_seen_at = ?, device_id = 'dev-live' WHERE id = ?`, seenAgo(ago), id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := dp.Db.Exec(`UPDATE tills SET device_id = 'dev-live' WHERE id = 'live'`); err != nil {
		t.Fatal(err)
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
			`data-remove-routes="/api/sync/tills/` + id + `/revoke"`,
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
	oldFetch := tillsFleetFetch // package-global: restore for later tests (-shuffle)
	t.Cleanup(func() { tillsFleetFetch = oldFetch; resetFleetCache() })
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
	if !strings.Contains(body, `data-remove-routes="/api/sync/tills/`+id+`/revoke"`) {
		t.Fatalf("the panel must post the existing revoke route")
	}
	if strings.Contains(body, `hx-post="/api/sync/tills/`+id+`/revoke"`) {
		t.Fatalf("Revoke must not post straight from the button any more")
	}
}

// Review 7173: the manager PIN is enforced by the revoke route itself, so a
// direct POST (no browser, no inline panel) cannot skip it; one rule with main's
// removal routes (#7098): none or a wrong PIN is 403, the right one 204.
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
	if rec := postPIN(mux, path, "", nil); rec.Code != http.StatusForbidden || !has() {
		t.Fatalf("no PIN = %d (till kept=%v), want 403 and the till kept", rec.Code, has())
	}
	if rec := postPIN(mux, path, "000000", nil); rec.Code != http.StatusForbidden || !has() {
		t.Fatalf("wrong PIN = %d (till kept=%v), want 403 and the till kept", rec.Code, has())
	}
	if rec := postPIN(mux, path, dataAPITestManagerPIN, nil); rec.Code != http.StatusNoContent || has() {
		t.Fatalf("right PIN = %d (till kept=%v), want 204 and the till gone", rec.Code, has())
	}
}

// Account-side removal is wired to main's cloud-remove (ut-docs#7098): with the
// shop's removal switched on, a ghost only the shop list holds gets the same
// inline PIN panel and posts to cloud-remove with its device id; with it off it
// says "Nothing to do" and has no button. No placeholder copy remains.
func TestOldSection_CloudOnlyGhostIsRemovableWithThePinWhenSwitchedOn(t *testing.T) {
	mux, dp := tillsTabsMux(t, []enroll.Device{{DeviceID: "dev-ghost", TillID: "ghost-1", Name: "phone till"}}, nil)
	body := tabsGet(mux, "/ui/tills/section/old").Body.String()
	if !strings.Contains(body, `data-kind="cloud"`) || !strings.Contains(body, "Nothing to do. This does not affect selling.") || strings.Contains(body, "data-pin-open") {
		t.Fatalf("switched off: want the entry with 'Nothing to do' and no Remove: %s", body)
	}
	if strings.Contains(body, "next update") {
		t.Fatalf("the 'arrives in the next update' placeholder must be gone: %s", body)
	}
	if err := dp.Settings.Set(t.Context(), settingCloudRemove, "1"); err != nil {
		t.Fatal(err)
	}
	body = tabsGet(mux, "/ui/tills/section/old").Body.String()
	for _, want := range []string{`aria-controls="pin-ghost-1-remove"`, `data-remove-routes="/api/sync/tills/cloud-remove?device_id=dev-ghost"`, `name="override_pin"`} {
		if !strings.Contains(body, want) {
			t.Errorf("switched on: missing %s: %s", want, body)
		}
	}
	other := tabsGet(mux, "/ui/tills/section/others").Body.String()
	if !strings.Contains(other, `data-testid="tills-old-banner"`) || !strings.Contains(other, "1 old entry is left over") {
		t.Errorf("switched on: the banner must offer the removal: %s", other)
	}
	// The panel's call: no PIN is refused by the route itself, the manager's PIN removes.
	calls := ""
	oldCall := tillsCloudRemoveCall
	tillsCloudRemoveCall = func(_ context.Context, _ *config.Config, id string) error { calls = id; return nil }
	t.Cleanup(func() { tillsCloudRemoveCall = oldCall })
	if rec := postPIN(mux, "/api/sync/tills/cloud-remove?device_id=dev-ghost", "", nil); rec.Code != http.StatusForbidden || calls != "" {
		t.Fatalf("no PIN = %d (called %q), want 403 and nothing removed", rec.Code, calls)
	}
	if rec := postPIN(mux, "/api/sync/tills/cloud-remove?device_id=dev-ghost", dataAPITestManagerPIN, nil); rec.Code != http.StatusNoContent || calls != "dev-ghost" {
		t.Fatalf("manager PIN = %d (called %q), want 204 and dev-ghost removed", rec.Code, calls)
	}
}
