package pages

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pos"
	"github.com/universaltill/universal-till/internal/updates"
)

// When an update exists but in-app apply is unsupported, a Windows desktop
// keeps an actionable download link (the user runs the installer), but a unix
// kiosk must NOT get a website link it can't act on (no way out of fullscreen)
// — it gets a plain "unavailable" message instead. Regression guard for the
// Pi kiosk dead-end (ut-docs#147).
func TestUpdateFallbackHTML(t *testing.T) {
	chdirRoot(t) // so web/locales resolves regardless of test ordering
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")

	win := updateUnavailableHTML("en", "0.2.51", "windows")
	if !strings.Contains(win, `href="https://www.universaltill.com/download"`) {
		t.Errorf("windows fallback should keep the download link, got %q", win)
	}
	// ut-docs#159: a plain same-window navigation is a dead end in the
	// WebView2 desktop shell (no NewWindowRequested/back affordance), so the
	// kept link must open in a new browsing context.
	if !strings.Contains(win, `target="_blank"`) {
		t.Errorf("windows fallback link should open in a new context (target=_blank), got %q", win)
	}

	kiosk := updateUnavailableHTML("en", "0.2.51", "linux")
	if strings.Contains(kiosk, "universaltill.com/download") || strings.Contains(kiosk, "<a ") {
		t.Errorf("kiosk fallback must not contain a website link, got %q", kiosk)
	}
	if !strings.Contains(kiosk, "available for this install") { // apostrophe is HTML-escaped
		t.Errorf("kiosk fallback should state in-app update is unavailable, got %q", kiosk)
	}

	// A Mac that can't self-update (ut-docs#18: an Intel Mac, since no Intel
	// .dmg is ever published) has a browser and can act on a link just like
	// Windows — it must NOT fall into the unix-kiosk dead-end branch, or the
	// Settings page contradicts the status bar (which already links out via
	// base.html's canselfupdate-false branch).
	mac := updateUnavailableHTML("en", "0.2.51", "darwin")
	if !strings.Contains(mac, `href="https://www.universaltill.com/download"`) {
		t.Errorf("macOS fallback should keep the download link, got %q", mac)
	}
	if !strings.Contains(mac, `target="_blank"`) {
		t.Errorf("macOS fallback link should open in a new context (target=_blank), got %q", mac)
	}

	// ut-docs#1246: Android is neither the desktop link branch nor the unix
	// kiosk dead end. The shell can drive the package installer, so the
	// operator gets an actionable control instead of "not available for this
	// install" — the state a real tablet was stuck in, forcing a manual APK
	// reinstall for every build.
	android := updateUnavailableHTML("en", "0.2.51", "android")
	if strings.Contains(android, "available for this install") {
		t.Errorf("android must not fall into the kiosk dead-end branch, got %q", android)
	}
	// ut-docs#1534: absolute, not a bare fragment — this line also renders in
	// the status bar on every other page, where "#android-update" goes nowhere.
	if !strings.Contains(android, `href="/settings#android-update"`) {
		t.Errorf("android fallback should point at the PIN-gated install form by absolute path, got %q", android)
	}
	// Installing drops the kiosk pin, which is what exit-to-os guards. This
	// status line renders on pages a cashier can reach, so it must NOT call
	// the bridge itself — only settings.html's PIN-gated form may.
	if strings.Contains(android, "installUpdate") {
		t.Errorf("android status line must not call the install bridge directly (ungated kiosk escape), got %q", android)
	}
	// A website link is useless here: shouldOverrideUrlLoading confines the
	// WebView to the till's own loopback origin, so an off-origin href is
	// refused before any download starts.
	if strings.Contains(android, "universaltill.com/download") {
		t.Errorf("android fallback must not rely on an off-origin link, got %q", android)
	}

	// Both still surface the available version so the operator knows one exists.
	for _, h := range []string{win, kiosk, mac, android} {
		if !strings.Contains(h, "0.2.51") {
			t.Errorf("fallback should show the available version, got %q", h)
		}
	}
}

// The updater endpoints are manager-gated. Without auth-off and without a
// manager in context, both must refuse before doing any work (crucially, apply
// must not reach selfupdate.Apply, and check must not hit the network). These
// tests deliberately leave UT_AUTH unset so the gate is exercised.
func TestUpdateAPI_ManagerGate(t *testing.T) {
	t.Setenv("UT_AUTH", "") // ensure auth is NOT disabled for this test

	dp := &common.Deps{}
	mux := http.NewServeMux()
	registerUpdateAPI(mux, dp)

	// ut-docs#1246: android-install authorises dropping the kiosk pin, so it
	// must refuse just as hard — with no AuthSvc wired it fails closed, and a
	// blank PIN is rejected before AuthorizeManager so it cannot burn the
	// device-wide lockout budget keypad login shares.
	for _, path := range []string{"/api/update/apply", "/api/update/check", "/api/update/android-install"} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST %s without manager: code %d, want 403 (body %q)",
				path, rec.Code, rec.Body.String())
		}
	}
}

// Regression (ut-docs#387): POST /api/update/apply's respond/respondCurrent
// used to write a bare {"success":…, "message":…} body, not the
// { "data": …, "error": null|"…" } envelope universal-till/CLAUDE.md
// mandates. The handler deliberately does NOT go through the
// autoUpdate*/selfupdate seams (an explicit user action stays available even
// on a dev build — see the comment on those seams above), so it can't be
// driven through httptest without hitting the real GitHub releases API and
// selfupdate.Apply's binary swap/re-exec, which this suite's other
// selfupdate-touching tests deliberately avoid.
//
// respondUpdateApply/respondUpdateApplyCurrent are called HERE directly
// (package-level funcs, not closures copied into the test) — an earlier
// version of this test declared its own local copy of the closure body and
// asserted against that copy, which stayed green even when the real
// registerUpdateAPI handler was reverted to the old bare shape (caught in
// independent review, ut-docs#387). Calling the real functions closes that
// gap: a regression in either one now fails this test directly.
func TestUpdateApplyResponseHelpers_EnvelopeShape(t *testing.T) {
	rec := httptest.NewRecorder()
	respondUpdateApply(rec, http.StatusOK, true, "update installed — restarting")
	var okBody struct {
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &okBody); err != nil {
		t.Fatalf("decode success envelope: %v (%s)", err, rec.Body.String())
	}
	if okBody.Error != nil || okBody.Data.Message != "update installed — restarting" {
		t.Fatalf("expected data.message populated and error:null, got %+v", okBody)
	}

	rec = httptest.NewRecorder()
	respondUpdateApply(rec, http.StatusBadGateway, false, "boom")
	var errBody struct {
		Data  any    `json:"data"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &errBody); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, rec.Body.String())
	}
	if errBody.Data != nil || errBody.Error != "boom" {
		t.Fatalf("expected data:null and error:%q, got %+v", "boom", errBody)
	}

	rec = httptest.NewRecorder()
	respondUpdateApplyCurrent(rec)
	var curBody struct {
		Data struct {
			AlreadyCurrent bool   `json:"already_current"`
			Message        string `json:"message"`
		} `json:"data"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &curBody); err != nil {
		t.Fatalf("decode already-current envelope: %v (%s)", err, rec.Body.String())
	}
	if curBody.Error != nil || !curBody.Data.AlreadyCurrent || curBody.Data.Message == "" {
		t.Fatalf("expected data.already_current:true, data.message non-empty, error:null, got %+v", curBody)
	}
}

// --- autoUpdateDue (pure) ---

func TestAutoUpdateDue(t *testing.T) {
	now := time.Date(2026, 8, 2, 22, 30, 0, 0, time.UTC)
	today := now.Format("2006-01-02")
	yesterday := now.AddDate(0, 0, -1).Format("2006-01-02")

	cases := []struct {
		name        string
		enabled     bool
		hhmm        string
		lastAttempt string
		want        bool
	}{
		{"disabled", false, "22:00", "", false},
		{"bad time format", true, "not-a-time", "", false},
		{"already attempted today", true, "22:15", today, false},
		{"time not yet reached", true, "23:00", "", false},
		{"time reached, exact match", true, "22:30", "", true},
		{"time reached, within window", true, "22:05", "", true}, // elapsed 25m, inside the window
		// Regression (ut-docs#79 review): eodDue's unbounded catch-up
		// ("any time >= hhmm today") is fine for a Z-report but wrong here —
		// a till switched off overnight and booted at opening time must NOT
		// auto-update minutes into trading just because it's technically
		// "past" a 03:00 schedule. The window bounds how late a catch-up can
		// still fire.
		{"window expired — booted hours after the scheduled time must NOT trigger", true, "19:00", "", false},
		{"attempted yesterday, due again today", true, "22:15", yesterday, true},
	}
	for _, c := range cases {
		if got := autoUpdateDue(now, c.enabled, c.hhmm, c.lastAttempt); got != c.want {
			t.Errorf("%s: autoUpdateDue() = %v, want %v", c.name, got, c.want)
		}
	}
}

// --- POST /api/settings/update-schedule ---

func TestPostSettingsUpdateSchedule_RequiresManager(t *testing.T) {
	dp := &common.Deps{}
	mux := http.NewServeMux()
	registerUpdateAPI(mux, dp)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/update-schedule", strings.NewReader("enabled=on&time=22:00"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without a manager session, got %d", rec.Code)
	}
}

// Positive counterpart to TestPostSettingsUpdateSchedule_RequiresManager
// (ut-docs#706 — canPerform()/Auth.Can(), not just the no-session
// short-circuit): a REAL session for cashier is denied, manager/admin/
// super_admin get past the gate and the write succeeds. super_admin is
// #554/#555's noted broadening vs. the old isManagerOrAuthOff gate —
// accepted and inert since nothing today creates that role.
func TestPostSettingsUpdateSchedule_RealSessionGatesByRole(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	for role, wantCode := range map[string]int{
		"cashier": http.StatusForbidden, "manager": http.StatusNoContent,
		"admin": http.StatusNoContent, "super_admin": http.StatusNoContent,
	} {
		t.Run(role, func(t *testing.T) {
			dp := newEODTestDeps(t)
			mux := http.NewServeMux()
			registerUpdateAPI(mux, dp)
			req := auth.WithUser(httptest.NewRequest(http.MethodPost, "/api/settings/update-schedule", strings.NewReader("enabled=on&time=03:30")), auth.User{ID: "u1", Role: role})
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code != wantCode {
				t.Fatalf("role=%s: got %d, want %d: %s", role, rec.Code, wantCode, rec.Body.String())
			}
		})
	}
}

func TestPostSettingsUpdateSchedule_ValidatesTimeFormat(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	dp := newEODTestDeps(t)
	mux := http.NewServeMux()
	registerUpdateAPI(mux, dp)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/update-schedule", strings.NewReader("enabled=on&time=not-a-time"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a malformed time when enabled, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/settings/update-schedule", strings.NewReader("enabled=on&time=03:30"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 for a valid time, got %d: %s", rec.Code, rec.Body.String())
	}
	val, _, err := dp.Settings.Get(t.Context(), keyAutoUpdateTime)
	if err != nil || val != "03:30" {
		t.Fatalf("expected the time setting persisted, got %q err=%v", val, err)
	}
	enabledVal, _, err := dp.Settings.Get(t.Context(), keyAutoUpdateEnabled)
	if err != nil || enabledVal != "true" {
		t.Fatalf("expected enabled=true persisted, got %q err=%v", enabledVal, err)
	}

	// Disabling doesn't require a valid time at all.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/settings/update-schedule", strings.NewReader("enabled=off&time="))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 disabling with no time, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- autoUpdateTick (the scheduler's per-tick decision + side effect) ---
//
// These swap the package-level seams (autoUpdateCurrent/autoUpdateCheckNow/
// autoUpdateSupported/autoUpdateApply) for the duration of one test, restoring
// the originals after — same hermetic-test convention as selfupdate's own
// seams (osExecutable, reexecFn, etc.). Never hits the real GitHub API or the
// real selfupdate.Apply (which would try to re-exec the test binary).
//
// autoUpdateTick takes `now` explicitly (not time.Now()) so these tests are
// deterministic regardless of wall-clock time — tickNow/tickHHMM below put
// every test inside the due window without depending on when the suite runs.

var tickNow = time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)

const tickHHMM = "10:00" // == tickNow's clock, so due with zero elapsed

func stubAutoUpdateSeams(t *testing.T, current updates.Status, checkNow updates.Status, supported bool, applyErr error) *int {
	t.Helper()
	origCurrent, origCheckNow, origSupported, origApply, origBuildVersion :=
		autoUpdateCurrent, autoUpdateCheckNow, autoUpdateSupported, autoUpdateApply, autoUpdateBuildVersion
	t.Cleanup(func() {
		autoUpdateCurrent, autoUpdateCheckNow, autoUpdateSupported, autoUpdateApply, autoUpdateBuildVersion =
			origCurrent, origCheckNow, origSupported, origApply, origBuildVersion
	})
	applyCalls := 0
	autoUpdateCurrent = func() updates.Status { return current }
	autoUpdateCheckNow = func(context.Context) updates.Status { return checkNow }
	autoUpdateSupported = func() bool { return supported }
	autoUpdateApply = func(context.Context) error {
		applyCalls++
		return applyErr
	}
	// A real stamped version by default -- the test binary's own
	// buildinfo.Version is "dev" (never ldflags-stamped), which would
	// otherwise make every one of these tests exercise the ut-docs#369
	// dev-build guard instead of what each actually means to test. The two
	// tests that specifically cover that guard override this after calling
	// stubAutoUpdateSeams.
	autoUpdateBuildVersion = func() string { return "0.2.60" }
	return &applyCalls
}

// newAutoUpdateTestDeps is newEODTestDeps plus a real (empty-basket) pos
// engine — autoUpdateTick reads d.Engine.Basket() to guard against
// restarting mid-sale, so every tick test needs one, same as the pos-touching
// handler tests elsewhere in this package (pos_scan_test.go et al.).
func newAutoUpdateTestDeps(t *testing.T) *common.Deps {
	t.Helper()
	dp := newEODTestDeps(t)
	dp.Engine = pos.NewServiceWithResolver(pos.Config{TaxRateBasisPoints: 2000}, nil)
	return dp
}

// StartAutoUpdateScheduler must register its goroutine on the caller's wg
// (ut-docs#153), same join shape as cloudsync.Start, so app.Run's shutdown
// drain can prove it exited before database.Close() runs — this loop also
// does binary/web-asset renames (internal/selfupdate.Apply), so an unjoined
// shutdown mid-swap has a real (if narrow) window to leave the install
// half-applied.
func TestStartAutoUpdateScheduler_JoinsWaitGroupAndExitsOnCtxCancel(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	StartAutoUpdateScheduler(ctx, dp, &wg)

	// wg.Wait() on a zero counter returns immediately, so without this
	// pre-cancel check the test would pass even if StartAutoUpdateScheduler
	// never called wg.Add at all. Confirm the counter is genuinely non-zero
	// before cancelling.
	registered := make(chan struct{})
	go func() { wg.Wait(); close(registered) }()
	select {
	case <-registered:
		t.Fatal("wg.Wait() returned before ctx was even cancelled — StartAutoUpdateScheduler never called wg.Add, so this test cannot prove the join")
	case <-time.After(100 * time.Millisecond):
	}

	cancel()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("StartAutoUpdateScheduler's goroutine did not call wg.Done() within 2s of ctx cancel — not joined to the shutdown drain")
	}
}

func TestAutoUpdateTick_SkipsWhenNotDue(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	// enabled=false: never due regardless of cached/fresh status.
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "false")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)
	applyCalls := stubAutoUpdateSeams(t, updates.Status{Available: true}, updates.Status{Available: true}, true, nil)

	autoUpdateTick(t.Context(), dp, tickNow)

	if *applyCalls != 0 {
		t.Fatalf("expected Apply not called when disabled, got %d calls", *applyCalls)
	}
	if v, _, _ := dp.Settings.Get(t.Context(), keyAutoUpdateLastAttempt); v != "" {
		t.Fatalf("expected no attempt recorded when not due, got %q", v)
	}
}

func TestAutoUpdateTick_SkipsWhenCachedStatusNotAvailable(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)
	// Cached Current() says nothing available — this must be a free/no-network
	// no-op, and must NOT mark an attempt (so it can retry for free next tick
	// once the existing background updates checker eventually flips this true).
	applyCalls := stubAutoUpdateSeams(t, updates.Status{Available: false}, updates.Status{Available: true}, true, nil)

	autoUpdateTick(t.Context(), dp, tickNow)

	if *applyCalls != 0 {
		t.Fatalf("expected Apply not called when cached status is not available, got %d calls", *applyCalls)
	}
	if v, _, _ := dp.Settings.Get(t.Context(), keyAutoUpdateLastAttempt); v != "" {
		t.Fatalf("expected no attempt recorded when cached status is not available, got %q", v)
	}
}

func TestAutoUpdateTick_SkipsWhenUnsupported(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)
	applyCalls := stubAutoUpdateSeams(t, updates.Status{Available: true}, updates.Status{Available: true}, false, nil)

	autoUpdateTick(t.Context(), dp, tickNow)

	if *applyCalls != 0 {
		t.Fatalf("expected Apply not called when unsupported, got %d calls", *applyCalls)
	}
}

// Regression (ut-docs#369): a `make build` binary used to silently report
// Version="dev" (the ldflags -X target was a symbol that doesn't exist), and
// internal/updates.Newer treats "dev" as older than every release -- so an
// unattended auto-update would replace a just-built/deployed developer
// binary with the latest GitHub release minutes later. Now that the ldflags
// bug is fixed a "dev" build should be rare, but unattended self-replacement
// of a developer's own build is never the right default regardless of how
// it got that way -- decline to act, same as the other guard conditions
// above. The manual "Update now" button (a separate handler,
// POST /api/update/apply) is unaffected: it's an explicit user action, not
// something this silent scheduler should also gate.
func TestAutoUpdateTick_SkipsWhenBuildVersionIsDev(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)
	applyCalls := stubAutoUpdateSeams(t, updates.Status{Available: true}, updates.Status{Available: true}, true, nil)
	autoUpdateBuildVersion = func() string { return "dev" }

	autoUpdateTick(t.Context(), dp, tickNow)

	if *applyCalls != 0 {
		t.Fatalf("expected Apply not called on a dev build, got %d calls", *applyCalls)
	}
	if v, _, _ := dp.Settings.Get(t.Context(), keyAutoUpdateLastAttempt); v != "" {
		t.Fatalf("expected no attempt recorded on a dev build, got %q", v)
	}
}

// A real stamped version (stubAutoUpdateSeams's default) must still
// auto-update normally -- the guard above isn't a blanket "auto-update
// never fires" regression in disguise. Covered by
// TestAutoUpdateTick_AppliesOnceWhenDueAndAvailable already exercising the
// default; this test exists to name that coverage explicitly against
// ut-docs#369, since a future change to the default could otherwise make it
// silently disappear.
func TestAutoUpdateTick_FiresWithRealBuildVersion(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)
	applyCalls := stubAutoUpdateSeams(t, updates.Status{Available: true}, updates.Status{Available: true}, true, nil)

	autoUpdateTick(t.Context(), dp, tickNow)

	if *applyCalls != 1 {
		t.Fatalf("expected Apply called once with a real build version, got %d calls", *applyCalls)
	}
}

// Regression (ut-docs#79 review, BLOCKING-2): an unattended restart mid-sale
// silently destroys the in-memory basket — there's no persistence and no
// human confirming it, unlike the manual button's hx-confirm. autoUpdateTick
// must refuse to fire while the basket has items, and must NOT mark the
// attempt (so it retries — still bounded by the window — once the sale
// clears rather than giving up on today's window entirely).
func TestAutoUpdateTick_SkipsWhenBasketHasItems(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)
	dp.Engine.AddLineWithModifiers(pos.BasketLine{SKU: "sku-1", Name: "Coffee", Qty: 1, PriceCents: 250}, 1, nil)
	applyCalls := stubAutoUpdateSeams(t, updates.Status{Available: true}, updates.Status{Available: true}, true, nil)

	autoUpdateTick(t.Context(), dp, tickNow)

	if *applyCalls != 0 {
		t.Fatalf("expected Apply not called while a sale is in progress, got %d calls", *applyCalls)
	}
	if v, _, _ := dp.Settings.Get(t.Context(), keyAutoUpdateLastAttempt); v != "" {
		t.Fatalf("expected no attempt recorded while a sale is in progress, got %q", v)
	}
}

func TestAutoUpdateTick_AppliesOnceWhenDueAndAvailable(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)
	applyCalls := stubAutoUpdateSeams(t, updates.Status{Available: true}, updates.Status{Available: true}, true, nil)

	autoUpdateTick(t.Context(), dp, tickNow)

	if *applyCalls != 1 {
		t.Fatalf("expected Apply called exactly once, got %d calls", *applyCalls)
	}
	want := tickNow.Format("2006-01-02")
	if v, _, _ := dp.Settings.Get(t.Context(), keyAutoUpdateLastAttempt); v != want {
		t.Fatalf("expected last-attempt date recorded as %s, got %q", want, v)
	}
}

func TestAutoUpdateTick_FreshRecheckAvoidsStaleApply(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)
	// Cached Current() is stale-optimistic (still Available from before an
	// earlier update), but a fresh CheckNow says already current — Apply must
	// NOT fire (guards the same staleness class of bug as the manual button,
	// ut-docs#79 comment: Current() can be up to 24h stale).
	applyCalls := stubAutoUpdateSeams(t, updates.Status{Available: true}, updates.Status{Available: false}, true, nil)

	autoUpdateTick(t.Context(), dp, tickNow)

	if *applyCalls != 0 {
		t.Fatalf("expected Apply not called when the fresh recheck says not available, got %d calls", *applyCalls)
	}
	// The attempt is still recorded (a fresh check was made), so this doesn't
	// spam the network again for the rest of the day.
	want := tickNow.Format("2006-01-02")
	if v, _, _ := dp.Settings.Get(t.Context(), keyAutoUpdateLastAttempt); v != want {
		t.Fatalf("expected last-attempt date recorded even when the fresh check found nothing to apply, got %q", v)
	}
}

func TestAutoUpdateTick_FailedApplyDoesNotRetrySameDay(t *testing.T) {
	dp := newAutoUpdateTestDeps(t)
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateEnabled, "true")
	_ = dp.Settings.Set(t.Context(), keyAutoUpdateTime, tickHHMM)
	applyCalls := stubAutoUpdateSeams(t, updates.Status{Available: true}, updates.Status{Available: true}, true, errors.New("boom"))

	autoUpdateTick(t.Context(), dp, tickNow) // first tick: attempts and fails
	autoUpdateTick(t.Context(), dp, tickNow) // second tick, same day: must not retry

	if *applyCalls != 1 {
		t.Fatalf("expected Apply called exactly once despite two ticks the same day, got %d calls", *applyCalls)
	}
}
