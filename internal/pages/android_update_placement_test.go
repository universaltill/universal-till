package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#1534. The install machinery from ut-docs#1246 works; the operator
// could not reach it. The owner, on a real tablet running v0.10.3 with v0.10.5
// available, tapped the status chip and landed on the Settings page's
// "Software update" card — which offered only "Check for updates", whose
// Android answer was a bare same-page `#android-update` fragment pointing at a
// manager-PIN form filed under the *Exit to OS* heading much further down.
//
// These tests pin the two properties that failure violated: the status line's
// link must work from ANY page, and the install control must live under the
// heading an operator looking for an update actually opens.

// The Android status line is rendered into the status bar on every page (and
// into the Settings check-for-updates slot). A bare "#android-update" href is
// a same-page fragment: on /sale, /catalog or anywhere else it navigates
// nowhere at all. It must be an absolute path to the settings anchor.
func TestAndroidUpdateStatusLinkIsPageIndependent(t *testing.T) {
	got := updateUnavailableHTML("en", "0.2.51", "android")

	if !strings.Contains(got, `href="/settings#android-update"`) {
		t.Errorf("android status line must link to the settings anchor by absolute path, got %q", got)
	}
	if strings.Contains(got, `href="#android-update"`) {
		t.Errorf("android status line still uses a bare same-page fragment (dead on every page but /settings), got %q", got)
	}
	// Unchanged from ut-docs#1246: this line renders where a cashier can see
	// it, so it must never call the bridge itself — only the PIN-gated form may.
	if strings.Contains(got, "installUpdate") {
		t.Errorf("android status line must not call the install bridge directly (ungated kiosk escape), got %q", got)
	}
}

// renderSettingsAs renders /settings for the given user with the Android
// install bridge forced on or off, so both platform branches are exercised
// regardless of the GOOS the test suite runs on (same seam convention as
// httpx.CrossDeviceLinkActionable, ut-docs#1057).
func renderSettingsAs(t *testing.T, mux *http.ServeMux, user auth.User, bridge bool) string {
	t.Helper()
	orig := httpx.UpdateInstallBridge
	httpx.UpdateInstallBridge = func() bool { return bridge }
	t.Cleanup(func() { httpx.UpdateInstallBridge = orig })
	// ut-docs#1545: the control is gated on an update actually existing now,
	// so these render tests have to say one does. Without the seam this would
	// depend on whatever the real releases API answered when the suite ran.
	origAvail := httpx.UpdateAvailable
	httpx.UpdateAvailable = func() bool { return true }
	t.Cleanup(func() { httpx.UpdateAvailable = origAvail })
	origLatest := httpx.LatestVersion
	httpx.LatestVersion = func() string { return "9.9.9" }
	t.Cleanup(func() { httpx.LatestVersion = origLatest })

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, user)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d: %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// The heart of ut-docs#1534: the install control must sit inside the
// "Software update" card, not under "Exit to OS" in the Display card. Asserted
// positionally against the two headings rather than by mere presence — the
// broken build contained the form too, just nowhere an operator would look.
func TestAndroidInstallFormLivesInTheSoftwareUpdateCard(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := renderSettingsAs(t, mux, mgrUser, true)

	form := strings.Index(body, `id="android-update-form"`)
	if form < 0 {
		t.Fatal("no android install form rendered on an Android till")
	}
	// Walk back to the card the form is actually inside and assert THAT card
	// is the Software update one. An earlier draft asserted only that the
	// form fell between the "Software update" and "Display" headings, which
	// the Theme card sits between too — it would have passed with the form
	// filed under Theme (review finding 5).
	cardStart := strings.LastIndex(body[:form], `<div class="card"`)
	if cardStart < 0 {
		t.Fatal("android install form is not inside a settings card at all")
	}
	card := body[cardStart:form]
	if !strings.Contains(card, httpx.T("en", "settings.update.title")) {
		t.Errorf("android install form is inside a card that is not Software update — "+
			"an operator opening Software update must find the install control there (ut-docs#1534).\nEnclosing card starts: %.300s",
			card)
	}
	// Exactly one heading between the card's start and the form proves the
	// walk-back did not skip over a nested card boundary.
	if n := strings.Count(card, "<h2"); n != 1 {
		t.Errorf("expected exactly one <h2> between the enclosing card and the form, got %d — the enclosing-card walk is unreliable", n)
	}

	// The anchor the status chip and status line both target must be inside
	// the form itself, not merely somewhere on the page.
	formEnd := strings.Index(body[form:], "</form>")
	if formEnd < 0 {
		t.Fatal("android install form is never closed")
	}
	if !strings.Contains(body[form:form+formEnd], `id="android-update"`) {
		t.Error("the /settings#android-update anchor is not inside the install form, so the chip's link lands beside it at best")
	}
	// The gate is unchanged: installing drops the kiosk pin, which is exactly
	// what exit-to-os guards, so the PIN must still be required.
	if !strings.Contains(body, `name="manager_pin"`) {
		t.Error("install form lost its manager-PIN input — that gate is what stops a cashier walking the till out of kiosk mode")
	}
}

// Off-Android the form must not render at all: those platforms either
// self-update in place or have no bridge to drive an installer.
func TestAndroidInstallFormAbsentWithoutTheBridge(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	if body := renderSettingsAs(t, mux, mgrUser, false); strings.Contains(body, `id="android-update-form"`) {
		t.Error("android install form rendered on a platform with no install bridge")
	}
}

// A cashier DOES see the form, and the manager PIN is what stops them
// (ut-docs#1534 review, finding 4). base.html shows the "Update available —
// Download" chip to every role, so hiding the form from a cashier did not
// remove a capability — it just recreated this card's own bug one role down:
// tap the chip, land on a page with nothing on it. The boundary that matters
// is server-side and asserted below.
func TestAndroidInstallFormReachableByCashierButPINGated(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)

	body := renderSettingsAs(t, mux, cashUser, true)
	if !strings.Contains(body, `id="android-update-form"`) {
		t.Error("a cashier following the update chip finds no install form — the chip is a dead end for that role")
	}
	// The manager-only controls in the same card stay manager-only.
	if strings.Contains(body, `hx-post="/api/update/check"`) {
		t.Error("check-for-updates rendered for a cashier — that control has no PIN gate of its own")
	}

	// The real gate: no PIN, no install authorisation, whatever the session.
	// registerUpdateAPI is mounted on its own mux here — newFullAuthDeps wires
	// the settings/setup handlers, not the updater's.
	api := http.NewServeMux()
	registerUpdateAPI(api, d)

	rec := postForm(api, "/api/update/android-install", url.Values{"manager_pin": {""}}, &cashUser)
	if rec.Code != http.StatusForbidden {
		t.Errorf("blank PIN from a cashier = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	rec = postForm(api, "/api/update/android-install", url.Values{"manager_pin": {"0000"}}, &cashUser)
	if rec.Code == http.StatusOK {
		t.Errorf("a wrong PIN authorised an install: %s", rec.Body.String())
	}
	// A MANAGER session with no PIN is deliberately allowed on an ordinary
	// till as of ut-docs#1537 — the same gate the desktop chip uses, and with
	// nothing pinned there is no kiosk lock for it to release. The case where
	// the PIN is still mandatory (self-order mode) is pinned by
	// TestAndroidInstallRequiresPINInSelfOrderModeEvenForAManagerSession.
	rec = postForm(api, "/api/update/android-install", url.Values{"manager_pin": {""}}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Errorf("manager session with no PIN on an ordinary till = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// The first-boot wizard renders the same "an update exists" line, and there
// the absolute /settings link is actively harmful: internal/auth/middleware.go
// bounces that route to /login on a till that has no manager account yet,
// throwing away the wizard's Alpine step state. As a bare fragment the link
// had been an inert no-op there, so making it absolute is what exposed this
// (ut-docs#1534 review, finding 3).
func TestSetupWizardAndroidUpdateLineHasNoLink(t *testing.T) {
	got := setupUnavailableHTML("en", "0.2.51", "android")

	if strings.Contains(got, "<a ") || strings.Contains(got, "/settings") {
		t.Errorf("the wizard must not send a first-boot operator to /settings — it loses their setup progress, got %q", got)
	}
	if !strings.Contains(got, "0.2.51") {
		t.Errorf("the wizard should still say an update exists, got %q", got)
	}

	// Every other platform is unchanged: a website link is as usable at first
	// boot as anywhere else.
	if win := setupUnavailableHTML("en", "0.2.51", "windows"); win != updateUnavailableHTML("en", "0.2.51", "windows") {
		t.Errorf("windows wizard line diverged from the settings line: %q", win)
	}
	if kiosk := setupUnavailableHTML("en", "0.2.51", "linux"); kiosk != updateUnavailableHTML("en", "0.2.51", "linux") {
		t.Errorf("unix kiosk wizard line diverged from the settings line: %q", kiosk)
	}
}

// ut-docs#1545, reported from the pilot tablet: "even if there is no new
// version ... after 10~15 seconds it shows the download window."
//
// Two independent guards, because the template's `updateavailable` comes from
// a cached check that is up to 24h stale by design (updates.Start's daily
// ticker) — so the control can legitimately render on a till that has since
// become current, and the endpoint has to be the one that says no.
func TestAndroidUpdateControlHiddenWhenAlreadyCurrent(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	origBridge := httpx.UpdateInstallBridge
	httpx.UpdateInstallBridge = func() bool { return true }
	t.Cleanup(func() { httpx.UpdateInstallBridge = origBridge })
	origAvail := httpx.UpdateAvailable
	httpx.UpdateAvailable = func() bool { return false }
	t.Cleanup(func() { httpx.UpdateAvailable = origAvail })

	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if strings.Contains(rec.Body.String(), `id="android-update-form"`) {
		t.Error("the install control renders on a till with no update available — a PIN and a ~140MB download that can only reinstall the running version")
	}
}

func TestAndroidInstallRefusesWhenAlreadyCurrent(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	api := http.NewServeMux()
	registerUpdateAPI(api, d)

	orig := androidInstallCheckNow
	androidInstallCheckNow = func(context.Context) updates.Status { return updates.Status{Available: false} }
	t.Cleanup(func() { androidInstallCheckNow = orig })

	rec := postForm(api, "/api/update/android-install", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("already-current = %d, want 200 with already_current: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "already_current") {
		t.Errorf("expected an already_current answer so the page can say 'up to date' instead of downloading, got: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"authorized"`) {
		t.Errorf("a till with no update available must never be authorised to install, got: %s", rec.Body.String())
	}
}

// ut-docs#1545 review, finding 1: the endpoint's already_current answer is
// only worth anything if the PAGE acts on it. The first version of this fix
// read res.status alone, so a 200 + already_current fell through to
// installUpdate() and downloaded ~140MB anyway — the endpoint guard delivered
// nothing in a real browser, which is exactly the complaint it was added for.
//
// Asserted on the rendered markup because the branch lives in inline JS: the
// handler must read the body, and the "up to date" string must be available to
// it as a data-* attribute (inline JS cannot call T() at click time).
func TestAndroidUpdateFormActsOnAlreadyCurrent(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := renderSettingsAs(t, mux, mgrUser, true)

	for _, want := range []string{"already_current", `data-uptodate="`, "res.json()"} {
		if !strings.Contains(body, want) {
			t.Errorf("the install form must act on the server's already_current answer, missing %q", want)
		}
	}
	if strings.Contains(body, `data-uptodate=""`) {
		t.Error("the up-to-date string resolved empty — the key is missing from this locale")
	}
	// The bridge call must sit AFTER the already_current branch, or the branch
	// cannot prevent the download.
	cur := strings.Index(body, "already_current")
	install := strings.Index(body, "AndroidKiosk.installUpdate")
	if cur < 0 || install < 0 || cur > install {
		t.Errorf("already_current must be checked before installUpdate is called (cur=%d install=%d)", cur, install)
	}
}
