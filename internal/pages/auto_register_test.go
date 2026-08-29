package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ADR-0071 (ut-docs#879): the setup wizard's last screen asks an explicit,
// not-pre-ticked opt-in question — register this till with the marketplace
// now. On "yes" the wizard persists marketplace.auto_register_opt_in=true
// BEFORE any network attempt and then makes ONE best-effort, time-boxed
// EnsureRegistered call; on "no"/absent it persists "false" and makes no
// call at all (ADR-0015's lazy registration stays the default).
//
// The fake marketplace's /v1/stores/register stub counts hits and then
// refuses (500), which is exactly the seam these tests need: an attempt is
// observable without ever mutating the enroll package's process globals
// across tests (see newFakeMarketplace's own doc comment).

func TestSetupWizardAutoRegisterOptInPersistsAndAttemptsRegistration(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	mkt := newFakeMarketplace(t, nil)
	d.Cfg.Marketplace = mkt.config()

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":           {"2468"},
		"pin_confirm":   {"2468"},
		"country":       {"GB"}, // unmapped in setupBasePlugins: no base-plugin install can also hit register
		"currency":      {"GBP"},
		"store_name":    {"Corner Shop"},
		"auto_register": {"on"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard setup with auto_register=on: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if v, ok, _ := d.Settings.Get(t.Context(), common.KeyAutoRegisterOptIn); !ok || v != "true" {
		t.Fatalf("%s = %q ok=%v, want \"true\"", common.KeyAutoRegisterOptIn, v, ok)
	}
	if hits := mkt.storeRegisterHits(); hits != 1 {
		t.Fatalf("register attempts = %d, want exactly 1 after an explicit opt-in", hits)
	}
	// The stub REFUSED the registration (500) — setup still completed and
	// signed the admin in: registration is best-effort, never a gate.
	if sessionCookie(rec) == "" {
		t.Fatal("wizard did not sign the admin in despite the failed (best-effort) registration")
	}
}

// Offline case: the marketplace is unreachable at wizard-completion time.
// The wizard must still complete, still persist the opt-in choice (persisted
// BEFORE the network attempt, so it survives), and still sign the admin in.
func TestSetupWizardAutoRegisterOptInOfflineStillCompletesSetup(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	// A closed local server: connections are refused immediately, so the
	// test exercises a genuine "marketplace unreachable" failure without
	// paying the attempt timeout (same shape as
	// TestSetupWizardDE_OfflineCompletesAndLeavesPendingForRetry).
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	d.Cfg.Marketplace.EndpointURL = dead.URL
	dead.Close()

	rec := postForm(mux, "/api/setup", url.Values{
		"pin":           {"2468"},
		"pin_confirm":   {"2468"},
		"country":       {"GB"},
		"currency":      {"GBP"},
		"store_name":    {"Corner Shop"},
		"auto_register": {"on"},
	}, nil)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("wizard setup must complete even offline: code=%d body=%s", rec.Code, rec.Body.String())
	}
	if sessionCookie(rec) == "" {
		t.Fatal("wizard setup must still sign the admin in when registration is offline")
	}
	if v, ok, _ := d.Settings.Get(t.Context(), common.KeyAutoRegisterOptIn); !ok || v != "true" {
		t.Fatalf("%s = %q ok=%v, want \"true\" persisted before the network attempt", common.KeyAutoRegisterOptIn, v, ok)
	}
}

// No opt-in (checkbox absent — the unchecked default) and an explicit "off"
// both persist "false" and make NO registration attempt: lazy registration
// (ADR-0015) stays exactly as it is today.
func TestSetupWizardNoAutoRegisterByDefaultMakesNoAttempt(t *testing.T) {
	for _, tc := range []struct {
		name string
		form url.Values
	}{
		{"absent", url.Values{}},
		{"off", url.Values{"auto_register": {"off"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, _, d := newFullAuthDeps(t)
			mkt := newFakeMarketplace(t, nil)
			d.Cfg.Marketplace = mkt.config()

			form := url.Values{
				"pin":         {"2468"},
				"pin_confirm": {"2468"},
				"country":     {"GB"},
				"currency":    {"GBP"},
				"store_name":  {"Corner Shop"},
			}
			for k, v := range tc.form {
				form[k] = v
			}
			rec := postForm(mux, "/api/setup", form, nil)
			if rec.Code != http.StatusSeeOther {
				t.Fatalf("wizard setup: code=%d body=%s", rec.Code, rec.Body.String())
			}
			if v, ok, _ := d.Settings.Get(t.Context(), common.KeyAutoRegisterOptIn); !ok || v != "false" {
				t.Fatalf("%s = %q ok=%v, want \"false\"", common.KeyAutoRegisterOptIn, v, ok)
			}
			if hits := mkt.storeRegisterHits(); hits != 0 {
				t.Fatalf("register attempts = %d, want 0 without an explicit opt-in", hits)
			}
		})
	}
}

// The wizard's last screen carries the opt-in checkbox, NOT pre-ticked
// (ADR-0071 decision 1) — inside the done step, before the submit button.
func TestSetupWizardRendersAutoRegisterCheckboxUnchecked(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	rec := getSetup(mux, "?lang=en", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /setup: code=%d", rec.Code)
	}
	body := rec.Body.String()
	idx := strings.Index(body, `name="auto_register"`)
	if idx < 0 {
		t.Fatal("setup wizard does not render the auto_register opt-in checkbox")
	}
	// Not pre-ticked: the input tag itself must not carry `checked`.
	tagEnd := strings.Index(body[idx:], ">")
	if tagEnd >= 0 && strings.Contains(body[idx:idx+tagEnd], "checked") {
		t.Fatal("auto_register checkbox is pre-ticked — ADR-0071 requires an explicit, unchecked opt-in")
	}
}

// --- Settings toggle (ADR-0071 decision 4) ---

// POST /api/settings/auto-register is elevation-gated exactly like
// POST /api/enrol/now: a cashier session gets the in-place PIN prompt and
// nothing is persisted or attempted.
func TestSettingsAutoRegisterRequiresElevation(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	mkt := newFakeMarketplace(t, nil)
	d.Cfg.Marketplace = mkt.config()

	rec := postForm(mux, "/api/settings/auto-register", url.Values{"optIn": {"on"}}, &cashUser)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "elevation-dialog") {
		t.Fatalf("cashier toggle: code=%d body=%s, want 200 with the elevation prompt", rec.Code, rec.Body.String())
	}
	if _, ok, _ := d.Settings.Get(t.Context(), common.KeyAutoRegisterOptIn); ok {
		t.Fatal("denied toggle persisted the setting anyway")
	}
	if hits := mkt.storeRegisterHits(); hits != 0 {
		t.Fatalf("denied toggle attempted registration (%d hits)", hits)
	}
}

// Toggling ON persists "true", audits the change, and fires ONE best-effort
// EnsureRegistered attempt (register now, not just "arm a future trigger" —
// ADR-0071 decision 4).
func TestSettingsAutoRegisterOnPersistsAndTriggersRegistration(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	mkt := newFakeMarketplace(t, nil)
	d.Cfg.Marketplace = mkt.config()

	rec := postForm(mux, "/api/settings/auto-register", url.Values{"optIn": {"on"}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("manager toggle on: code=%d body=%s, want 204", rec.Code, rec.Body.String())
	}
	if v, ok, _ := d.Settings.Get(t.Context(), common.KeyAutoRegisterOptIn); !ok || v != "true" {
		t.Fatalf("%s = %q ok=%v, want \"true\"", common.KeyAutoRegisterOptIn, v, ok)
	}
	if hits := mkt.storeRegisterHits(); hits != 1 {
		t.Fatalf("register attempts = %d, want exactly 1 on toggling on", hits)
	}
	var n int
	if err := d.Db.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'auto_register_opt_in_changed'`).Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n != 1 {
		t.Fatalf("auto_register_opt_in_changed audit rows = %d, want 1", n)
	}
}

// Toggling OFF persists "false" and makes NO call — and there is
// deliberately no deregistration flow (ADR-0071 decision 4): an identity
// already minted stays untouched.
func TestSettingsAutoRegisterOffPersistsFalseWithoutCall(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	mkt := newFakeMarketplace(t, nil)
	d.Cfg.Marketplace = mkt.config()
	if err := d.Settings.Set(t.Context(), common.KeyAutoRegisterOptIn, "true"); err != nil {
		t.Fatal(err)
	}

	rec := postForm(mux, "/api/settings/auto-register", url.Values{}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("manager toggle off: code=%d body=%s, want 204", rec.Code, rec.Body.String())
	}
	if v, ok, _ := d.Settings.Get(t.Context(), common.KeyAutoRegisterOptIn); !ok || v != "false" {
		t.Fatalf("%s = %q ok=%v, want \"false\"", common.KeyAutoRegisterOptIn, v, ok)
	}
	if hits := mkt.storeRegisterHits(); hits != 0 {
		t.Fatalf("register attempts = %d, want 0 on toggling off", hits)
	}
}
