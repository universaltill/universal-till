package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/fxlevel"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/settings"
)

const testGiB = uint64(1) << 30

// withFxSignals fixes the host signals resolveEffectsLevel sees.
func withFxSignals(t *testing.T, s fxlevel.Signals) {
	t.Helper()
	old := readFxSignals
	readFxSignals = func() fxlevel.Signals { return s }
	t.Cleanup(func() { readFxSignals = old })
}

var (
	piSignals      = fxlevel.Signals{Cores: 4, RAMBytes: 8 * testGiB, PiModel: "Raspberry Pi 5 Model B Rev 1.0", GOOS: "linux", GOARCH: "arm64"}
	desktopSignals = fxlevel.Signals{Cores: 16, RAMBytes: 32 * testGiB, GOOS: "linux", GOARCH: "amd64"}
)

func fxGet(t *testing.T, s *settings.Store, key string) string {
	t.Helper()
	v, _, err := s.Get(t.Context(), key)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// ADR-0119 §4: on a fresh till (nothing stored) Auto detects the host,
// stores result, reason and fingerprint, and resolves to the detection.
func TestResolveEffectsLevel_AutoDetectsAndStores(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	withFxSignals(t, piSignals)
	if got := resolveEffectsLevel(t.Context(), d.Settings); got != fxlevel.Light {
		t.Fatalf("resolved = %q on a Pi, want light", got)
	}
	if v := fxGet(t, d.Settings, fxlevel.KeyDetected); v != fxlevel.Light {
		t.Errorf("stored detected = %q", v)
	}
	if v := fxGet(t, d.Settings, fxlevel.KeyReason); v != "pi,cores=4,ram=8g" {
		t.Errorf("stored reason = %q", v)
	}
	if v := fxGet(t, d.Settings, fxlevel.KeyFingerprint); v != piSignals.Fingerprint() {
		t.Errorf("stored fingerprint = %q", v)
	}
	// The setting itself is never written by detection: it stays auto.
	if v := fxGet(t, d.Settings, fxlevel.KeyLevel); v != "" {
		t.Errorf("detection wrote the level setting: %q", v)
	}
}

// Same fingerprint → the stored detection stands (it may have come from a
// later refinement), nothing is re-detected.
func TestResolveEffectsLevel_SameFingerprintKeepsStoredDetection(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	withFxSignals(t, desktopSignals)
	if err := d.Settings.SetMany(t.Context(), map[string]string{
		fxlevel.KeyDetected:    fxlevel.Balanced,
		fxlevel.KeyReason:      "cores=16,ram=32g",
		fxlevel.KeyFingerprint: desktopSignals.Fingerprint(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := resolveEffectsLevel(t.Context(), d.Settings); got != fxlevel.Balanced {
		t.Fatalf("resolved = %q, want the stored balanced", got)
	}
}

// A changed machine (different fingerprint) re-detects at boot.
func TestResolveEffectsLevel_ChangedFingerprintRedetects(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	withFxSignals(t, desktopSignals)
	if err := d.Settings.SetMany(t.Context(), map[string]string{
		fxlevel.KeyDetected:    fxlevel.Light,
		fxlevel.KeyReason:      "pi,cores=4,ram=8g",
		fxlevel.KeyFingerprint: piSignals.Fingerprint(),
	}); err != nil {
		t.Fatal(err)
	}
	if got := resolveEffectsLevel(t.Context(), d.Settings); got != fxlevel.Full {
		t.Fatalf("resolved = %q after moving to a desktop, want full", got)
	}
	if v := fxGet(t, d.Settings, fxlevel.KeyReason); v != "cores=16,ram=32g" {
		t.Errorf("reason not re-detected: %q", v)
	}
}

// An explicit operator level wins and is never overwritten, but detection
// still runs and is stored so Settings can show it next to the choice.
func TestResolveEffectsLevel_ExplicitLevelWinsButDetectionStillStored(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	withFxSignals(t, piSignals)
	if err := d.Settings.Set(t.Context(), fxlevel.KeyLevel, fxlevel.Full); err != nil {
		t.Fatal(err)
	}
	if got := resolveEffectsLevel(t.Context(), d.Settings); got != fxlevel.Full {
		t.Fatalf("resolved = %q, want the operator's full", got)
	}
	if v := fxGet(t, d.Settings, fxlevel.KeyLevel); v != fxlevel.Full {
		t.Errorf("operator level overwritten: %q", v)
	}
	if v := fxGet(t, d.Settings, fxlevel.KeyDetected); v != fxlevel.Light {
		t.Errorf("detection not stored next to an explicit level: %q", v)
	}
}

// A corrupt stored level behaves as auto.
func TestResolveEffectsLevel_UnknownLevelIsAuto(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	withFxSignals(t, piSignals)
	if err := d.Settings.Set(t.Context(), fxlevel.KeyLevel, "turbo"); err != nil {
		t.Fatal(err)
	}
	if got := resolveEffectsLevel(t.Context(), d.Settings); got != fxlevel.Light {
		t.Fatalf("resolved = %q, want the detection (light)", got)
	}
}

// Boot and the post-sync re-derive publish the resolved level through
// publishCachedSettings.
func TestPublishCachedSettings_PublishesEffectsLevel(t *testing.T) {
	_, _, d := newFullAuthDeps(t)
	withFxSignals(t, piSignals)
	t.Cleanup(func() { httpx.InitEffectsLevel(fxlevel.Full) })
	httpx.InitEffectsLevel(fxlevel.Full)
	publishCachedSettings(t.Context(), d.Settings, d.CurrentState(), true)
	if got := httpx.FuncsFor("en")["fxlevel"].(func() string)(); got != fxlevel.Light {
		t.Fatalf("published fxlevel = %q after boot on a Pi, want light", got)
	}
}

// POST /api/settings/effects-level (ADR-0119 §6): same role as ui-scale —
// any signed-in session; 400 on a bad value, 204 on success, the global
// republished at once.
func TestEffectsLevelHandler(t *testing.T) {
	mux, svc, d := newFullAuthDeps(t)
	withFxSignals(t, piSignals)
	t.Cleanup(func() { httpx.InitEffectsLevel(fxlevel.Full) })
	cashier := auth.User{ID: "m1", Role: "cashier", DisplayName: "Cashier"}
	fx := func() string { return httpx.FuncsFor("en")["fxlevel"].(func() string)() }

	for _, bad := range []string{"", "fx-light", "LIGHT", "turbo"} {
		if rec := postForm(mux, "/api/settings/effects-level", url.Values{"level": {bad}}, &cashier); rec.Code != http.StatusBadRequest {
			t.Errorf("level=%q = %d, want 400", bad, rec.Code)
		}
	}
	if v := fxGet(t, d.Settings, fxlevel.KeyLevel); v != "" {
		t.Fatalf("a rejected value was stored: %q", v)
	}

	if rec := postForm(mux, "/api/settings/effects-level", url.Values{"level": {"balanced"}}, &cashier); rec.Code != http.StatusNoContent {
		t.Fatalf("level=balanced = %d, want 204", rec.Code)
	}
	if v := fxGet(t, d.Settings, fxlevel.KeyLevel); v != fxlevel.Balanced {
		t.Errorf("stored level = %q", v)
	}
	if fx() != fxlevel.Balanced {
		t.Errorf("global not republished on save: %q", fx())
	}
	// Auto resolves to the host detection (a Pi here).
	if rec := postForm(mux, "/api/settings/effects-level", url.Values{"level": {"auto"}}, &cashier); rec.Code != http.StatusNoContent {
		t.Fatalf("level=auto = %d, want 204", rec.Code)
	}
	if fx() != fxlevel.Light {
		t.Errorf("auto on a Pi published %q, want light", fx())
	}

	// Behind the real auth middleware, no session → refused, nothing saved.
	guarded := auth.Middleware(mux, svc)
	req := httptest.NewRequest(http.MethodPost, "/api/settings/effects-level", strings.NewReader(url.Values{"level": {"full"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	guarded.ServeHTTP(rec, req)
	if rec.Code == http.StatusNoContent {
		t.Fatalf("unauthenticated POST was accepted (%d)", rec.Code)
	}
	if v := fxGet(t, d.Settings, fxlevel.KeyLevel); v != fxlevel.Auto {
		t.Errorf("unauthenticated POST changed the level to %q", v)
	}
}

// The Display card renders the four options and the detection in words.
func TestEffectsLevelViewFrom(t *testing.T) {
	v := effectsLevelViewFrom(map[string]string{
		fxlevel.KeyDetected: "light",
		fxlevel.KeyReason:   "pi,cores=4,ram=8g,bogus",
	})
	if v.Level != fxlevel.Auto || v.Detected != fxlevel.Light {
		t.Fatalf("view = %+v", v)
	}
	want := []effectsReasonPart{
		{Key: "settings.display.effects_reason_pi"},
		{Key: "settings.display.effects_reason_cores", Arg: 4},
		{Key: "settings.display.effects_reason_ram", Arg: 8},
	}
	if len(v.ReasonParts) != len(want) {
		t.Fatalf("reason parts = %+v", v.ReasonParts)
	}
	for i := range want {
		if v.ReasonParts[i] != want[i] {
			t.Errorf("part %d = %+v, want %+v", i, v.ReasonParts[i], want[i])
		}
	}
}

// Settings → Display: the four-option selector, the operator's choice
// selected, and the host detection in words (ADR-0119 §6).
func TestSettingsPage_EffectsLevelCard(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	withFxSignals(t, piSignals)
	t.Cleanup(func() { httpx.InitEffectsLevel(fxlevel.Full) })
	if err := d.Settings.Set(t.Context(), fxlevel.KeyLevel, fxlevel.Balanced); err != nil {
		t.Fatal(err)
	}
	resolveEffectsLevel(t.Context(), d.Settings)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, `hx-post="/api/settings/effects-level"`) {
		t.Fatalf("Display card has no effects-level form")
	}
	for _, v := range []string{"auto", "full", "balanced", "light"} {
		if !strings.Contains(body, `<option value="`+v+`"`) {
			t.Errorf("effects selector lacks the %q option", v)
		}
	}
	if !regexp.MustCompile(`<option value="balanced"\s+selected>`).MatchString(body) {
		t.Errorf("the stored level (balanced) is not the selected option")
	}
	if !strings.Contains(body, "Detected for this till: Light (Raspberry Pi, 4 CPU cores, 8 GB memory)") {
		i := strings.Index(body, `data-testid="effects-detected"`)
		t.Errorf("detection not shown in words; got: %.300s", body[max(i, 0):])
	}
}
