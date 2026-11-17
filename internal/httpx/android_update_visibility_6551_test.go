package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ut-docs#6551: on an Android till a viewer WITHOUT "settings" (the sale-only
// cashier, ut-docs#3079) used to get "Update available vX - In-app update isn't
// available for this install": wrong, because an update IS available and a
// manager can install it. They now get a non-interactive "ask a manager" pill.
func TestBaseLayoutAndroidUpdatePillForSaleOnlyViewer(t *testing.T) {
	origAvail, origBridge := UpdateAvailable, UpdateInstallBridge
	UpdateAvailable = func() bool { return true }
	UpdateInstallBridge = func() bool { return true }
	t.Cleanup(func() { UpdateAvailable, UpdateInstallBridge = origAvail, origBridge })
	InitRailVisibility(func(r *http.Request, predicate string) bool {
		return r.Header.Get("X-Test-Role") == "manager"
	})
	t.Cleanup(func() { InitRailVisibility(nil) })
	InitI18n(realI18n(t), "en")
	render := func(role string) string {
		req := httptest.NewRequest(http.MethodGet, "/pin", nil)
		req.Header.Set("X-Test-Role", role)
		funcs := withHelpHref(FuncsFor("en"), req)
		funcs["canselfupdate"] = func() bool { return false }
		funcs["updatedownloadlink"] = func() bool { return false }
		funcs["latestversion"] = func() string { return "9.9.9" }
		files := append([]string{renderFiles[0], "ui/pages/pin.html"}, renderFiles[1:]...)
		tpl, err := ClonedTemplate("android-pill-6551:"+role, "base.html", funcs, files...)
		if err != nil {
			t.Fatalf("ClonedTemplate: %v", err)
		}
		w := httptest.NewRecorder()
		if err := tpl.ExecuteTemplate(w, "base", map[string]any{"title": "Change PIN", "theme": "", "menuItems": nil, "errKey": ""}); err != nil {
			t.Fatalf("execute base: %v", err)
		}
		return w.Body.String()
	}

	cashier := render("cashier")
	i := strings.Index(cashier, `data-testid="sb-android-update-ask"`)
	if i < 0 {
		t.Fatalf("cashier: want the ask-a-manager pill (data-testid=sb-android-update-ask)")
	}
	open := strings.LastIndex(cashier[:i], "<")
	// The pill holds a nested icon span: it ends at the </span> after its text.
	end := strings.Index(cashier[i:], "manager</span>")
	if end >= 0 {
		end += len("manager")
	}
	if end < 0 || !strings.HasPrefix(cashier[open:], "<span") {
		t.Fatalf("cashier pill must be a non-interactive <span>, got %.200s", cashier[open:])
	}
	pill := cashier[open : i+end]
	for _, want := range []string{`class="sb-item sb-update is-info"`, `role="status"`, "Update available", "9.9.9", "ask a manager", `aria-hidden="true"`} {
		if !strings.Contains(pill, want) {
			t.Errorf("pill lacks %q: %s", want, pill)
		}
	}
	for _, bad := range []string{"<button", "<a ", "href=", "tabindex", "onclick"} {
		if strings.Contains(pill, bad) {
			t.Errorf("pill must not be interactive (sale-only, #3079), found %q: %s", bad, pill)
		}
	}
	if strings.Contains(cashier, "In-app update isn") || strings.Contains(cashier, "only be installed") {
		t.Errorf("cashier still sees the wrong 'not available for this install' copy")
	}
	if strings.Contains(cashier, `id="sb-android-update-btn"`) {
		t.Errorf("cashier must never get the install chip")
	}

	manager := render("manager")
	if !strings.Contains(manager, `id="sb-android-update-btn"`) {
		t.Errorf("manager lost the Android install chip")
	}
	if strings.Contains(manager, `sb-android-update-ask`) {
		t.Errorf("manager must get the chip, not the ask-a-manager pill")
	}
}

// Without the bridge (desktop kiosk, browser) the pill must not appear: the
// existing fall-through copy stays for those.
func TestBaseLayoutAndroidPillAbsentWithoutBridge(t *testing.T) {
	origAvail, origBridge := UpdateAvailable, UpdateInstallBridge
	UpdateAvailable = func() bool { return true }
	UpdateInstallBridge = func() bool { return false }
	t.Cleanup(func() { UpdateAvailable, UpdateInstallBridge = origAvail, origBridge })
	InitRailVisibility(func(r *http.Request, predicate string) bool { return false })
	t.Cleanup(func() { InitRailVisibility(nil) })
	InitI18n(realI18n(t), "en")
	req := httptest.NewRequest(http.MethodGet, "/pin", nil)
	funcs := withHelpHref(FuncsFor("en"), req)
	funcs["canselfupdate"] = func() bool { return false }
	funcs["updatedownloadlink"] = func() bool { return false }
	files := append([]string{renderFiles[0], "ui/pages/pin.html"}, renderFiles[1:]...)
	tpl, err := ClonedTemplate("android-pill-6551:nobridge", "base.html", funcs, files...)
	if err != nil {
		t.Fatalf("ClonedTemplate: %v", err)
	}
	w := httptest.NewRecorder()
	if err := tpl.ExecuteTemplate(w, "base", map[string]any{"title": "x", "theme": "", "menuItems": nil, "errKey": ""}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if strings.Contains(w.Body.String(), "sb-android-update-ask") {
		t.Errorf("ask-a-manager pill rendered with no install bridge")
	}
}
