package pages

import (
	"os"
	"strings"
	"testing"
)

// ut-docs#6551 Phase 1: a manager opening Settings on an Android till with an
// update available sees a banner ABOVE every section, plus (inside the
// #android-update form) the "allow installs" guidance panel the bridge's
// needs_permission state reveals.
func TestAndroidUpdateBannerTopOfSettingsForManager(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := renderSettingsAs(t, mux, mgrUser, true)

	b := strings.Index(body, `data-testid="android-update-banner"`)
	if b < 0 {
		t.Fatal("no Android update banner on Settings for a manager")
	}
	shell := strings.Index(body, `id="settings-shell"`)
	if shell < 0 || b > shell {
		t.Errorf("banner must sit above the settings shell/sections (banner at %d, shell at %d)", b, shell)
	}
	open := strings.LastIndex(body[:b], "<")
	end := strings.Index(body[b:], "</div>")
	banner := body[open : b+end]
	for _, want := range []string{`role="region"`, `aria-label="Update available"`, `href="#android-update"`, "Go to update", `class="btn`, "Finish any open sale first"} {
		if !strings.Contains(body[open:b+end+400], want) {
			t.Errorf("banner lacks %q: %s", want, banner)
		}
	}
}

func TestAndroidUpdateBannerAbsentWithoutBridge(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	if body := renderSettingsAs(t, mux, mgrUser, false); strings.Contains(body, `data-testid="android-update-banner"`) {
		t.Error("banner rendered on a till with no install bridge")
	}
}

func TestAndroidUpdateFormCarriesPermissionPanel(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := renderSettingsAs(t, mux, mgrUser, true)
	p := strings.Index(body, `data-testid="android-allow-panel"`)
	if p < 0 {
		t.Fatal("no 'allow installs' panel in the Android update form")
	}
	form := strings.Index(body, `id="android-update-form"`)
	if p < form {
		t.Error("allow panel must live inside the #android-update-form section")
	}
	open := strings.LastIndex(body[:p], "<")
	if !strings.Contains(body[open:p+60], "hidden") {
		t.Errorf("panel must start hidden until the shell reports needs_permission: %.200s", body[open:p+60])
	}
	tail := body[p:]
	for _, want := range []string{"One-time permission needed", "Open Android settings", "Not now", `role="status"`, `data-android-allow-open`, `data-android-allow-later`} {
		if !strings.Contains(body[open:]+tail, want) {
			t.Errorf("panel lacks %q", want)
		}
	}
	// The strings the shared JS shows on resume come from data-* on the form.
	for _, attr := range []string{"data-allowgranted=", "data-allowdenied="} {
		if !strings.Contains(body, attr) {
			t.Errorf("form lacks %s", attr)
		}
	}
}

// The shared flow must know the new shell state and expose the bridge call
// (no argument: the page can never steer what is installed or where to go).
func TestAndroidUpdateJSHandlesNeedsPermission(t *testing.T) {
	js, err := os.ReadFile("web/public/android-update.js")
	if err != nil {
		t.Fatal(err)
	}
	s := string(js)
	for _, want := range []string{"needs_permission", "permission_granted", "openInstallPermissionSettings"} {
		if !strings.Contains(s, want) {
			t.Errorf("android-update.js does not handle %q", want)
		}
	}
	if strings.Contains(s, "ACTION_MANAGE") || strings.Contains(s, "package:") {
		t.Error("the page must not name the Android settings intent; the shell owns it")
	}
}
