package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#1537. The product owner, on a real tablet: "it should update the app
// even when I click on the bottom of the screen on the update green button."
//
// On desktop the status-bar chip already does exactly that — two-click confirm,
// then POST /api/update/apply, gated by canPerform("plugin_management") with no
// PIN. Android demanded a PIN because installing has to release the kiosk
// lock-task pin, which is what exit-to-os guards. Since ut-docs#1508 the app
// only pins itself in SELF-ORDER mode, so on an ordinary till that PIN guards a
// lock which is not engaged.
//
// So: authorise from the session like desktop does — except in self-order mode,
// where the pin is real and a valid manager cookie may well be sitting in the
// kiosk's own browser (ut-docs#1253: entering self-order never logs the till
// out). There the PIN stays mandatory, and these tests are what stop a future
// refactor from "simplifying" that away.

func androidInstallMux(t *testing.T) (*http.ServeMux, *common.Deps) {
	t.Helper()
	_, _, d := newFullAuthDeps(t)
	api := http.NewServeMux()
	registerUpdateAPI(api, d)
	return api, d
}

// setSelfOrderMode puts the till into the one state where the kiosk lock-task
// pin is genuinely engaged (ADR-0020, ut-docs#1508).
func setSelfOrderMode(t *testing.T, d *common.Deps) {
	t.Helper()
	if err := d.Settings.Set(t.Context(), "display.mode", "self_order"); err != nil {
		t.Fatalf("set display.mode: %v", err)
	}
}

// An ordinary till: a signed-in manager installs from the chip, no PIN.
func TestAndroidInstallAuthorizesFromManagerSession(t *testing.T) {
	api, _ := androidInstallMux(t)

	rec := postForm(api, "/api/update/android-install", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager session with no PIN = %d, want 200 (the desktop chip's own gate): %s", rec.Code, rec.Body.String())
	}
}

// A cashier cannot self-authorise — same as desktop, where POST
// /api/update/apply refuses them. They fall back to the PIN form.
func TestAndroidInstallRefusesCashierSessionWithoutPIN(t *testing.T) {
	api, _ := androidInstallMux(t)

	rec := postForm(api, "/api/update/android-install", url.Values{}, &cashUser)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cashier session with no PIN = %d, want 403", rec.Code)
	}
}

// THE ONE THAT MATTERS. In self-order mode the app really is pinned, and the
// kiosk browser can still be carrying a live manager session (ut-docs#1253) —
// so a customer standing at the machine could tap the update chip and walk the
// till out of its own kiosk. The session is not enough here; the PIN is.
func TestAndroidInstallRequiresPINInSelfOrderModeEvenForAManagerSession(t *testing.T) {
	api, d := androidInstallMux(t)
	setSelfOrderMode(t, d)

	rec := postForm(api, "/api/update/android-install", url.Values{}, &mgrUser)
	if rec.Code != http.StatusForbidden {
		t.Errorf("manager session with no PIN in self-order mode = %d, want 403 — "+
			"authorising from the cookie alone makes the update chip a one-tap kiosk escape (ut-docs#1537)", rec.Code)
	}

	// An empty-string PIN is not a PIN either.
	rec = postForm(api, "/api/update/android-install", url.Values{"manager_pin": {"  "}}, &mgrUser)
	if rec.Code != http.StatusForbidden {
		t.Errorf("blank PIN in self-order mode = %d, want 403", rec.Code)
	}
}

// ut-docs#1537 review: the Settings page and the endpoint must agree about
// whether a PIN is coming, or the page renders a PIN-less button the endpoint
// then refuses (or hides a field it is about to demand). One helper owns the
// decision; this pins that it fails closed on every uncertainty.
func TestAndroidUpdateSessionAuthorizesFailsClosed(t *testing.T) {
	_, _, d := newFullAuthDeps(t)

	mgrReq := func() *http.Request {
		return auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/update/android-install", nil), mgrUser)
	}

	if !androidUpdateSessionAuthorizes(d, mgrReq()) {
		t.Error("a manager on an ordinary till should be authorised by their session alone")
	}

	// Self-order: the kiosk pin is real, so the session is not enough.
	setSelfOrderMode(t, d)
	if androidUpdateSessionAuthorizes(d, mgrReq()) {
		t.Error("self-order mode must demand a PIN even from a manager session")
	}

	// A cashier never authorises from the session, in either mode.
	cashReq := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/update/android-install", nil), cashUser)
	if androidUpdateSessionAuthorizes(d, cashReq) {
		t.Error("a cashier must never authorise an install from their session")
	}

	// No settings store: we cannot know whether the pin is engaged, so assume
	// it is. Failing open here is the one mistake that matters.
	bare := &common.Deps{Db: d.Db, AuthSvc: d.AuthSvc}
	if androidUpdateSessionAuthorizes(bare, mgrReq()) {
		t.Error("with no settings store the mode is unknowable — that must mean 'assume pinned', not 'assume free'")
	}
}

// ut-docs#1545 review, finding 6: the freshness check is an outbound call to
// the releases API. A cashier with no PIN must be refused BEFORE it is made —
// otherwise repeated taps burn the shop's unauthenticated GitHub rate budget
// and starve the daily background check that keeps every till current.
func TestAndroidInstallRefusesCashierBeforeAnyNetworkCall(t *testing.T) {
	api, d := androidInstallMux(t)
	_ = d

	called := 0
	orig := androidInstallCheckNow
	androidInstallCheckNow = func(context.Context) updates.Status {
		called++
		return updates.Status{Available: true}
	}
	t.Cleanup(func() { androidInstallCheckNow = orig })

	rec := postForm(api, "/api/update/android-install", url.Values{}, &cashUser)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cashier with no PIN = %d, want 403", rec.Code)
	}
	if called != 0 {
		t.Errorf("the release check ran %d time(s) for a caller who could never install — that is a free outbound request per tap", called)
	}

	// A manager, by contrast, is allowed to trigger it.
	if rec := postForm(api, "/api/update/android-install", url.Values{}, &mgrUser); rec.Code != http.StatusOK {
		t.Fatalf("manager session = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if called != 1 {
		t.Errorf("expected exactly one release check for the authorised caller, got %d", called)
	}
}
