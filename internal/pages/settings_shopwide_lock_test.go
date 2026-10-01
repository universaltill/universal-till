package pages

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/discovery"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#2981: on an additional till whose main till is unreachable, the
// Settings regions that write a shop-wide key render read-only (a disabled
// set-lock fieldset with the "can't reach the main till" note); per-till
// regions stay editable; a main till locks nothing.

// lockFieldsetRe matches one set-lock fieldset's opening tag and captures
// its region id and whether it is disabled.
var lockFieldsetRe = regexp.MustCompile(`<fieldset class="set-lock" data-lock-region="([a-z0-9-]+)"( disabled)?>`)

func getSettingsAsManager(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	req := auth.WithUser(httptest.NewRequest(http.MethodGet, "/settings", nil), mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d %s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// lockStates maps each rendered set-lock region to whether it is disabled.
func lockStates(body string) map[string]bool {
	out := map[string]bool{}
	for _, m := range lockFieldsetRe.FindAllStringSubmatch(body, -1) {
		out[m[1]] = m[2] != ""
	}
	return out
}

// makeMainUnreachable points d at a main till that never answers and lets
// PrimaryWatch count it unreachable — the cached state the status chip
// reads, no probe.
func makeMainUnreachable(t *testing.T, d *common.Deps) {
	t.Helper()
	setReplicaSettings(t, d.Settings, "http://127.0.0.1:1", syncSettingsBearer)
	d.PrimaryWatch = discovery.NewPrimaryWatch(d.Settings, func(context.Context, time.Duration) ([]discovery.Candidate, error) {
		return nil, nil // the main till is nowhere on the network
	})
	for range discovery.UnreachableThreshold {
		d.PrimaryWatch.ContactFailed(t.Context())
	}
	if v := replicaLinkView(t.Context(), d); v.State != linkUnreachable {
		t.Fatalf("link state = %q, want unreachable", v.State)
	}
}

const setLockNote = `data-testid="set-lock-note"`

func TestSettingsPage_ShopWideLockedWhileMainTillUnreachable(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), common.KeyStoreName, "Corner Shop"); err != nil {
		t.Fatal(err)
	}
	makeMainUnreachable(t, d)

	body := getSettingsAsManager(t, mux)
	states := lockStates(body)
	for _, id := range []string{"settings-order-no", "settings-store-name", "settings-idle-lock", "settings-currency"} {
		disabled, ok := states[id]
		if !ok {
			t.Fatalf("region %s has no set-lock fieldset", id)
		}
		if !disabled {
			t.Errorf("region %s is editable while the main till is unreachable", id)
		}
	}
	if !strings.Contains(body, setLockNote) || !strings.Contains(body, html.EscapeString(settingsUnreachableEN)) {
		t.Fatalf("no read-only note while the main till is unreachable")
	}
	// The current local value still shows.
	if !strings.Contains(body, `value="Corner Shop"`) {
		t.Fatalf("locked store name does not show the local value")
	}
	// Per-till: the theme form is never inside a set-lock fieldset, and
	// is not disabled.
	at := strings.Index(body, `hx-post="/api/settings/theme"`)
	if at < 0 {
		t.Fatalf("theme form missing")
	}
	if strings.LastIndex(body[:at], `<fieldset class="set-lock"`) > strings.LastIndex(body[:at], `</fieldset>`) {
		t.Fatalf("per-till theme form sits inside a set-lock fieldset")
	}
}

func TestSettingsPage_NothingLockedOnMainTillOrReachableMain(t *testing.T) {
	t.Run("main till", func(t *testing.T) {
		mux, _, _ := newFullAuthDeps(t)
		body := getSettingsAsManager(t, mux)
		assertNothingLocked(t, body)
	})
	t.Run("additional till, main reachable", func(t *testing.T) {
		mux, _, d := newFullAuthDeps(t)
		setReplicaSettings(t, d.Settings, "http://127.0.0.1:1", syncSettingsBearer)
		if v := replicaLinkView(t.Context(), d); v.State == linkUnreachable {
			t.Fatalf("link state = %q, want not unreachable", v.State)
		}
		body := getSettingsAsManager(t, mux)
		assertNothingLocked(t, body)
	})
}

func assertNothingLocked(t *testing.T, body string) {
	t.Helper()
	states := lockStates(body)
	if len(states) == 0 {
		t.Fatalf("no set-lock fieldsets rendered at all")
	}
	for id, disabled := range states {
		if disabled {
			t.Errorf("region %s locked although the main till is not away", id)
		}
	}
	if strings.Contains(body, setLockNote) {
		t.Fatalf("read-only note shown although the main till is not away")
	}
}

// Drift: every lockable region is a set-lock fieldset in settings.html, and
// every representative key really is shop-wide.
func TestShopWideLockRegions_MatchTemplateAndScope(t *testing.T) {
	chdirRoot(t)
	raw, err := os.ReadFile("web/ui/pages/settings.html")
	if err != nil {
		t.Fatal(err)
	}
	tpl := string(raw)
	if len(shopWideLockRegions) < 2 {
		t.Fatalf("shopWideLockRegions has %d entries", len(shopWideLockRegions))
	}
	for id, key := range shopWideLockRegions {
		open := `<fieldset class="set-lock" data-lock-region="` + id + `"{{ if index $.lockedRegions "` + id + `" }} disabled{{ end }}>`
		if strings.Count(tpl, open) != 1 {
			t.Errorf("region %s: want exactly one %s in settings.html", id, open)
		}
		note := `{{ if index $.lockedRegions "` + id + `" }}{{ template "set-lock-note" }}{{ end }}`
		if !strings.Contains(tpl, note) {
			t.Errorf("region %s: no read-only note (%s)", id, note)
		}
		if got := data.SettingScope(key); got != data.SettingShopWide {
			t.Errorf("region %s: key %q is not shop-wide (scope %d)", id, key, got)
		}
	}
	// And no fieldset in the template names a region the map lacks.
	for _, m := range regexp.MustCompile(`data-lock-region="([a-z0-9-]+)"`).FindAllStringSubmatch(tpl, -1) {
		if _, ok := shopWideLockRegions[m[1]]; !ok {
			t.Errorf("settings.html locks region %s, which shopWideLockRegions does not list", m[1])
		}
	}
}
