package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/updates"
)

// stubSetupUpdateEnabled overrides just the UT_UPDATE_CHECK-opt-out seam
// (review finding N2, ut-docs#1165) — separate from stubSetupUpdateSeams
// because most tests want it left at "enabled" (its default) and only the
// opt-out test itself needs to flip it.
func stubSetupUpdateEnabled(t *testing.T, enabled bool) {
	t.Helper()
	orig := setupUpdateEnabled
	t.Cleanup(func() { setupUpdateEnabled = orig })
	setupUpdateEnabled = func() bool { return enabled }
}

// ut-docs#1165: the setup wizard's first screen (step 1) checks for a newer
// release and, if one exists, offers to update BEFORE setup continues —
// never automatically, never blocking, never erroring on an unreachable
// network. These tests fake internal/updates and internal/selfupdate through
// the setupUpdate* package-level seams below (same hermetic-test convention
// as update_api.go's autoUpdate* seams) so nothing here ever hits the real
// GitHub API or tries to re-exec the test binary.

// stubSetupUpdateSeams swaps setupUpdateCheckNow/setupUpdateSupported/
// setupUpdateApply for the duration of one test, restoring the originals
// after — mirrors update_api_test.go's stubAutoUpdateSeams.
func stubSetupUpdateSeams(t *testing.T, checkNow updates.Status, supported bool, applyErr error) *int {
	t.Helper()
	origCheckNow, origSupported, origApply, origEnabled := setupUpdateCheckNow, setupUpdateSupported, setupUpdateApply, setupUpdateEnabled
	t.Cleanup(func() {
		setupUpdateCheckNow, setupUpdateSupported, setupUpdateApply, setupUpdateEnabled = origCheckNow, origSupported, origApply, origEnabled
	})
	applyCalls := 0
	setupUpdateCheckNow = func(context.Context) updates.Status { return checkNow }
	setupUpdateSupported = func() bool { return supported }
	setupUpdateApply = func(context.Context) error {
		applyCalls++
		return applyErr
	}
	setupUpdateEnabled = func() bool { return true }
	return &applyCalls
}

func postSetupUpdate(t *testing.T, mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// --- POST /api/setup/update-check ---

// Regression (the card's own headline acceptance criterion): the network
// check failing or timing out must never block or delay the wizard, and must
// never surface an error — the handler renders nothing at all, fast.
// updates.CheckNow's own contract (internal/updates/updates.go) is that a
// failed checkOnce leaves Status.Latest empty, which is what a fully offline
// first boot looks like here.
func TestSetupUpdateCheck_OfflineOrFailedCheckRendersEmptyFast(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	applyCalls := stubSetupUpdateSeams(t, updates.Status{}, true, nil)

	start := time.Now()
	rec := postSetupUpdate(t, mux, "/api/setup/update-check")
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("POST /api/setup/update-check took %v — the offline path must be immediate (the seam never blocks)", elapsed)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "" {
		t.Fatalf("expected an empty body when the check fails/is offline, got %q", body)
	}
	if *applyCalls != 0 {
		t.Fatalf("a failed check must never trigger an apply, got %d apply calls", *applyCalls)
	}
}

// Already on the latest version: nothing to prompt, no banner.
func TestSetupUpdateCheck_AlreadyCurrentRendersEmpty(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	stubSetupUpdateSeams(t, updates.Status{Latest: "0.5.0", Available: false}, true, nil)

	rec := postSetupUpdate(t, mux, "/api/setup/update-check")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "" {
		t.Fatalf("expected an empty body when already current, got %q", body)
	}
}

// An update exists and this install can self-update in place: a banner with
// an apply control targeting /api/setup/update-apply, never applying on its
// own (no auto-apply — consent is required).
func TestSetupUpdateCheck_AvailableAndSupportedRendersBannerWithApplyControl(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	applyCalls := stubSetupUpdateSeams(t, updates.Status{Latest: "0.6.0", Available: true}, true, nil)

	rec := postSetupUpdate(t, mux, "/api/setup/update-check")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-post="/api/setup/update-apply"`) {
		t.Fatalf("banner missing an apply control targeting /api/setup/update-apply:\n%s", body)
	}
	if !strings.Contains(body, "0.6.0") {
		t.Errorf("banner should show the available version, got %q", body)
	}
	if *applyCalls != 0 {
		t.Fatalf("the check alone must never apply anything, got %d apply calls", *applyCalls)
	}
}

// An update exists but in-app apply can't run on this install (Windows/
// non-writable): falls back to the EXISTING updateUnavailableHTML helper
// (update_api.go) — no second copy of that logic.
func TestSetupUpdateCheck_AvailableButUnsupportedFallsBackToUnavailableHTML(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	stubSetupUpdateSeams(t, updates.Status{Latest: "0.6.0", Available: true}, false, nil)

	rec := postSetupUpdate(t, mux, "/api/setup/update-check")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, `hx-post="/api/setup/update-apply"`) {
		t.Error("an unsupported install must not offer an in-app apply control")
	}
	if !strings.Contains(body, "0.6.0") {
		t.Errorf("fallback should still show the available version, got %q", body)
	}
}

// Shares the wizard's first-boot window: once an operator exists, refuse.
func TestSetupUpdateCheck_RefusedAfterFirstBoot(t *testing.T) {
	mux, svc, _ := newFullAuthDeps(t)
	id, err := svc.Repo().CreateUser(t.Context(), "boss", "Boss", "admin")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPIN("1234")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().SetUserPIN(t.Context(), id, hash); err != nil {
		t.Fatal(err)
	}

	rec := postSetupUpdate(t, mux, "/api/setup/update-check")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST /api/setup/update-check after first boot: code=%d loc=%q, want 303 -> /login",
			rec.Code, rec.Header().Get("Location"))
	}
}

// --- POST /api/setup/update-apply ---

// Explicit consent (the operator clicked Update): applies once, and the
// response tells the client to reconnect/reload.
func TestSetupUpdateApply_SuccessAppliesAndRendersRestarting(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	applyCalls := stubSetupUpdateSeams(t, updates.Status{Latest: "0.6.0", Available: true}, true, nil)

	rec := postSetupUpdate(t, mux, "/api/setup/update-apply")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if *applyCalls != 1 {
		t.Fatalf("expected exactly one apply call, got %d", *applyCalls)
	}
	if body := rec.Body.String(); strings.TrimSpace(body) == "" {
		t.Fatal("expected a non-empty response telling the client to reconnect/reload")
	}
}

// Review round 2 gap: the restart poller's ~3min-timeout recovery affordance
// (review finding N1, ut-docs#1165 — mirrors base.html's own "click to
// reload" so a stalled poll never leaves the message stuck with no way out)
// had no direct assertion — TestSetupUpdateApply_SuccessAppliesAndRendersRestarting
// above only checks the body is non-empty, which would keep passing even if
// the recovery wiring were dropped entirely. Assert the actual generated
// markup/script carries it.
func TestSetupUpdateRestartingHTML_HasRecoveryAffordance(t *testing.T) {
	got := setupUpdateRestartingHTML("en")
	if !strings.Contains(got, `id="setup-update-restart-msg"`) {
		t.Fatalf("missing the span id the recovery script targets: %s", got)
	}
	if !strings.Contains(got, "el.onclick") || !strings.Contains(got, "location.reload()") {
		t.Fatalf("missing the click-to-reload recovery handler: %s", got)
	}
	if !strings.Contains(got, "tries>90") {
		t.Fatalf("missing the bounded-timeout condition (same ~3min bound as base.html's poller): %s", got)
	}
	wantTimeoutText := httpx.T("en", "setup.update.restart_timeout")
	if !strings.Contains(got, wantTimeoutText) {
		t.Fatalf("recovery message %q not found in: %s", wantTimeoutText, got)
	}
}

// Re-checks freshness before applying (same staleness guard as the existing
// POST /api/update/apply): an already-current till must not re-download.
func TestSetupUpdateApply_AlreadyCurrentDoesNotApply(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	applyCalls := stubSetupUpdateSeams(t, updates.Status{Latest: "0.5.0", Available: false}, true, nil)

	rec := postSetupUpdate(t, mux, "/api/setup/update-apply")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	if *applyCalls != 0 {
		t.Fatalf("must not apply when already current, got %d apply calls", *applyCalls)
	}
}

// Unsupported install (race between the check rendering the button and the
// click, or a forged request): falls back to updateUnavailableHTML, never
// calls Apply.
func TestSetupUpdateApply_UnsupportedFallsBackToUnavailableHTML(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	applyCalls := stubSetupUpdateSeams(t, updates.Status{Latest: "0.6.0", Available: true}, false, nil)

	rec := postSetupUpdate(t, mux, "/api/setup/update-apply")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	if *applyCalls != 0 {
		t.Fatalf("must not apply when unsupported, got %d apply calls", *applyCalls)
	}
	if !strings.Contains(rec.Body.String(), "0.6.0") {
		t.Errorf("fallback should still show the available version, got %q", rec.Body.String())
	}
}

// A failed apply must tell the operator plainly (never silent) — this is an
// explicit action they took, unlike the background check.
func TestSetupUpdateApply_FailureRendersMessage(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	stubSetupUpdateSeams(t, updates.Status{Latest: "0.6.0", Available: true}, true, context.DeadlineExceeded)

	rec := postSetupUpdate(t, mux, "/api/setup/update-apply")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d, want 200", rec.Code)
	}
	if body := strings.TrimSpace(rec.Body.String()); body == "" {
		t.Fatal("a failed apply must still render something, not silently nothing")
	}
}

// Shares the wizard's first-boot window: once an operator exists, refuse —
// never apply an update to an already-provisioned till through this route.
func TestSetupUpdateApply_RefusedAfterFirstBoot(t *testing.T) {
	mux, svc, _ := newFullAuthDeps(t)
	id, err := svc.Repo().CreateUser(t.Context(), "boss", "Boss", "admin")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPIN("1234")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().SetUserPIN(t.Context(), id, hash); err != nil {
		t.Fatal(err)
	}
	applyCalls := stubSetupUpdateSeams(t, updates.Status{Latest: "0.6.0", Available: true}, true, nil)

	rec := postSetupUpdate(t, mux, "/api/setup/update-apply")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
		t.Fatalf("POST /api/setup/update-apply after first boot: code=%d loc=%q, want 303 -> /login",
			rec.Code, rec.Header().Get("Location"))
	}
	if *applyCalls != 0 {
		t.Fatalf("must never apply once an operator exists, got %d apply calls", *applyCalls)
	}
}

// --- wiring: GET /setup step 1 auto-fires the check, never blocking render ---

func TestSetupWizardStep1WiresUpdateCheckContainer(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	rec := getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup?lang=en: code=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-post="/api/setup/update-check"`) || !strings.Contains(body, `hx-trigger="load"`) {
		t.Fatalf("step 1 is missing the auto-firing update-check container:\n%s", body)
	}
	// Review finding B1: the container (and the banner's own Update-now
	// button, checked below) must never serialize the wizard <form>'s later-
	// step fields (the PIN step's pin/pin_confirm) to these unauthenticated
	// endpoints — Alpine's x-show only hides via CSS, so those inputs are in
	// the DOM from step 1 onward the moment an operator steps forward then
	// back.
	if !strings.Contains(body, `id="setup-update-check" hx-post="/api/setup/update-check" hx-trigger="load" hx-swap="innerHTML" hx-params="none"`) {
		t.Fatalf("step 1's update-check container is missing hx-params=\"none\" (review finding B1):\n%s", body)
	}
}

// Review finding B1: the banner's own Update-now button must carry the same
// hx-params="none" guard as its container — checked directly against the
// rendered banner HTML, not just the container wiring above.
func TestSetupUpdateBannerHTML_NeverSerializesWizardForm(t *testing.T) {
	got := setupUpdateBannerHTML("en", "9.9.9")
	if !strings.Contains(got, `hx-params="none"`) {
		t.Fatalf("update-now button is missing hx-params=\"none\" (review finding B1): %s", got)
	}
}

// Review finding N2: unlike Settings' manual "Check for updates" button (an
// explicit user action), this check fires automatically on step 1's load —
// an air-gapped/opted-out install (UT_UPDATE_CHECK=0) must not make the
// outbound call at all.
func TestSetupUpdateCheck_RespectsUpdateCheckDisabled(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	stubSetupUpdateEnabled(t, false)
	called := false
	orig := setupUpdateCheckNow
	t.Cleanup(func() { setupUpdateCheckNow = orig })
	setupUpdateCheckNow = func(context.Context) updates.Status {
		called = true
		return updates.Status{Latest: "9.9.9", Available: true}
	}

	rec := postSetupUpdate(t, mux, "/api/setup/update-check")
	if rec.Code != http.StatusOK || rec.Body.String() != "" {
		t.Fatalf("POST /api/setup/update-check with UT_UPDATE_CHECK disabled: code=%d body=%q, want 200 empty",
			rec.Code, rec.Body.String())
	}
	if called {
		t.Fatal("setupUpdateCheckNow was called despite the update-check opt-out — an air-gapped till must never make this outbound call")
	}
}

// --- real auth middleware exemption (setup_language_catalog_test.go's own
// TestSetupLanguageInstallExemptFromAuthMiddleware precedent, review finding
// N3, ut-docs#1165): every bare-mux test above would keep passing even if
// internal/auth/middleware.go's exempt() switch dropped these two paths —
// only a request through the real auth.Middleware actually pins it. ---

func TestSetupUpdateCheckExemptFromAuthMiddleware(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, d := newRealDBDeps(t)
	stubSetupUpdateSeams(t, updates.Status{Latest: "9.9.9", Available: true}, true, nil)
	h := auth.Middleware(mux, auth.NewService(d.Db))

	req := httptest.NewRequest(http.MethodPost, "/api/setup/update-check", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatal("POST /api/setup/update-check 401'd behind the real auth middleware — " +
			"the route is missing from internal/auth/middleware.go's exempt() switch")
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "setup-update-banner") {
		t.Fatalf("POST /api/setup/update-check through real middleware: code=%d body=%q, want 200 with a banner",
			rec.Code, rec.Body.String())
	}
}

func TestSetupUpdateApplyExemptFromAuthMiddleware(t *testing.T) {
	t.Setenv("UT_AUTH", "on")
	mux, d := newRealDBDeps(t)
	stubSetupUpdateSeams(t, updates.Status{Latest: "9.9.9", Available: true}, true, nil)
	h := auth.Middleware(mux, auth.NewService(d.Db))

	req := httptest.NewRequest(http.MethodPost, "/api/setup/update-apply", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code == http.StatusUnauthorized {
		t.Fatal("POST /api/setup/update-apply 401'd behind the real auth middleware — " +
			"the route is missing from internal/auth/middleware.go's exempt() switch")
	}
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "setup-update-restart-msg") {
		t.Fatalf("POST /api/setup/update-apply through real middleware: code=%d body=%q, want 200 restarting response",
			rec.Code, rec.Body.String())
	}
}
