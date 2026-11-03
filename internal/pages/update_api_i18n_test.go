package pages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/selfupdate"
	"github.com/universaltill/universal-till/internal/updates"
)

// ut-docs#5274: the apply/install endpoints must never put err.Error() in the
// response; the operator gets a translated message, the log gets the detail.

func TestUpdateApplyErrorKey(t *testing.T) {
	for _, c := range []struct {
		name string
		err  error
		want string
	}{
		{"unsupported", selfupdate.ErrUnsupported, "settings.update.unavailable_here"},
		{"wrapped unsupported", fmt.Errorf("x: %w", selfupdate.ErrUnsupported), "settings.update.unavailable_here"},
		{"busy", selfupdate.ErrApplyInProgress, "settings.update.error.busy"},
		{"wrapped busy", fmt.Errorf("x: %w", selfupdate.ErrApplyInProgress), "settings.update.error.busy"},
		{"network", fmt.Errorf("download: %w", &url.Error{Op: "Get", URL: "https://x", Err: errors.New("no route")}), "settings.update.check_failed"},
		{"releases API", errors.New("releases API: HTTP 403"), "settings.update.check_failed"},
		{"checksum mismatch", errors.New("checksum mismatch: got a want b"), "settings.update.error.verify_failed"},
		{"no checksums", errors.New("release v1 has no checksums.txt — refusing to install an unverified update"), "settings.update.error.verify_failed"},
		{"untrusted asset (stand-in for #2285)", errors.New("release asset is not on the trusted release host"), "settings.update.error.failed"},
		{"unknown", errors.New("disk full"), "settings.update.error.failed"},
	} {
		if got := updateApplyErrorKey(c.err); got != c.want {
			t.Errorf("%s: updateApplyErrorKey(%q) = %q, want %q", c.name, c.err, got, c.want)
		}
	}
}

func TestUpdateApplyAlreadyCurrentRace(t *testing.T) {
	if !updateApplyAlreadyCurrent(fmt.Errorf("wrap: %w", errors.New("already on the latest version"))) &&
		!updateApplyAlreadyCurrent(errors.New("already on the latest version")) {
		t.Error("the already-on-the-latest race must read as already-current")
	}
	if updateApplyAlreadyCurrent(errors.New("disk full")) || updateApplyAlreadyCurrent(selfupdate.ErrApplyInProgress) {
		t.Error("other errors are not already-current")
	}
}

func TestRespondUpdateApplyError_LocalizedNoRawText(t *testing.T) {
	chdirRoot(t)
	initAuthTestI18n(t)
	raw := errors.New("copy /opt/ut/bin/unitill: permission denied (secret-path-xyz)")
	for _, loc := range []string{"en", "tr"} {
		req := httptest.NewRequest(http.MethodPost, "/api/update/apply?lang="+loc, nil)
		rec := httptest.NewRecorder()
		respondUpdateApplyError(rec, req, http.StatusBadGateway, "settings.update.error.failed", "update_apply", raw)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("%s: status %d, want 502", loc, rec.Code)
		}
		var env struct {
			Data  any    `json:"data"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("%s: decode: %v (%s)", loc, err, rec.Body.String())
		}
		want := httpx.T(loc, "settings.update.error.failed")
		if env.Data != nil || env.Error != want || want == "settings.update.error.failed" {
			t.Errorf("%s: envelope %+v, want data:null error:%q", loc, env, want)
		}
		if strings.Contains(rec.Body.String(), "secret-path-xyz") || strings.Contains(rec.Body.String(), "permission denied") {
			t.Errorf("%s: raw error leaked into the response: %s", loc, rec.Body.String())
		}
	}
	if httpx.T("tr", "settings.update.error.failed") == httpx.T("en", "settings.update.error.failed") {
		t.Error("tr must not be an English copy")
	}
}

func TestAndroidInstallResponsesAreLocalized(t *testing.T) {
	api, _ := androidInstallMux(t)
	orig := androidInstallCheckNow
	androidInstallCheckNow = func(context.Context) updates.Status { return updates.Status{Available: true} }
	t.Cleanup(func() { androidInstallCheckNow = orig })

	errOf := func(rec *httptest.ResponseRecorder) string {
		var env struct {
			Error string `json:"error"`
			Data  struct {
				Message string `json:"message"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		if env.Error != "" {
			return env.Error
		}
		return env.Data.Message
	}

	// 403: cashier, no PIN.
	rec := postForm(api, "/api/update/android-install?lang=tr", url.Values{}, &cashUser)
	if rec.Code != http.StatusForbidden || errOf(rec) != httpx.T("tr", "settings.update.android.pin_needed") {
		t.Errorf("cashier = %d %q, want 403 + tr pin_needed", rec.Code, errOf(rec))
	}
	// 200: manager session.
	rec = postForm(api, "/api/update/android-install?lang=tr", url.Values{}, &mgrUser)
	if rec.Code != http.StatusOK || errOf(rec) != httpx.T("tr", "settings.update.android.authorized") {
		t.Errorf("manager = %d %q, want 200 + tr authorized", rec.Code, errOf(rec))
	}
	if errOf(rec) == "authorized" || httpx.T("tr", "settings.update.android.authorized") == "settings.update.android.authorized" {
		t.Errorf("authorized message not translated: %s", rec.Body.String())
	}
}

func TestUpdateApplySuccessHelpersAreLocalized(t *testing.T) {
	chdirRoot(t)
	initAuthTestI18n(t)
	rec := httptest.NewRecorder()
	respondUpdateApplyCurrent(rec, "tr")
	if !strings.Contains(rec.Body.String(), httpx.T("tr", "status.update_uptodate")) {
		t.Errorf("current message not localized: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	respondUpdateApplyInstalled(rec, "tr", "1.2.3")
	if !strings.Contains(rec.Body.String(), httpx.T("tr", "status.update_restarting")) || !strings.Contains(rec.Body.String(), `"version":"1.2.3"`) {
		t.Errorf("installed message not localized / version missing: %s", rec.Body.String())
	}
}

// Tester addition (ut-docs#5274): the wrong-PIN 403 and the lockout 429 must
// also be localized (a mutation of the 429 key previously survived).
func TestAndroidInstallPINFailuresAreLocalized(t *testing.T) {
	api, _ := androidInstallMux(t)
	orig := androidInstallCheckNow
	androidInstallCheckNow = func(context.Context) updates.Status { return updates.Status{Available: true} }
	t.Cleanup(func() { androidInstallCheckNow = orig })

	errOf := func(rec *httptest.ResponseRecorder) string {
		var env struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v (%s)", err, rec.Body.String())
		}
		return env.Error
	}
	form := url.Values{"manager_pin": {"9999"}}
	rec := postForm(api, "/api/update/android-install?lang=tr", form, &cashUser)
	if rec.Code != http.StatusForbidden || errOf(rec) != httpx.T("tr", "settings.update.android.pin_needed") {
		t.Errorf("wrong PIN = %d %q, want 403 + tr pin_needed", rec.Code, errOf(rec))
	}
	for range 5 {
		postForm(api, "/api/update/android-install?lang=tr", form, &cashUser)
	}
	rec = postForm(api, "/api/update/android-install?lang=tr", form, &cashUser)
	if rec.Code != http.StatusTooManyRequests || errOf(rec) != httpx.T("tr", "auth.error.locked") {
		t.Errorf("locked out = %d %q, want 429 + tr auth.error.locked", rec.Code, errOf(rec))
	}
	if httpx.T("tr", "auth.error.locked") == "auth.error.locked" {
		t.Error("auth.error.locked missing in tr")
	}
}
