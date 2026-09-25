package pages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
)

var themeSyncDataTheme = regexp.MustCompile(`data-theme="([^"]*)"`)

// pollThemeSync is one /ui/theme-sync poll, as base.html fires it every 30 s.
func pollThemeSync(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ui/theme-sync", nil))
	m := themeSyncDataTheme.FindStringSubmatch(rec.Body.String())
	if rec.Code != http.StatusOK || m == nil {
		t.Fatalf("theme-sync poll: %d %q", rec.Code, rec.Body.String())
	}
	return m[1]
}

// ut-docs#2783 (Pi5-1, an additional till of the tablet, v0.23.0): changing
// the theme on a joined till "froze" it. Root cause: the theme was a
// shop-wide, admin-synced setting, so the replica's local choice survived
// only until the next admin pull whose fingerprint moved (the main till's
// admin state moved every ~2 min in the field journal), which rewrote it
// with the main till's value and rederived it into the live state; the open
// page's /ui/theme-sync poll then swapped the stylesheet back, and the
// stale <meta name="ut-shell"> signature turned the next tap into a full
// document load. Each re-pick restarted the cycle.
//
// Decision (product owner, 2026-09-25): the theme is per station. This
// drives the real pieces end to end — the Settings theme handler on the
// replica, the replica's admin-pull tick against a real main till whose
// admin state keeps moving, the rederive step, and the page's poll — and
// asserts the poll never reports anything but the replica's own theme, so
// the stylesheet is never re-applied and no reload is ever provoked.
func TestReplicaTheme_SurvivesMovingAdminPullsAndThePollNeverFlips(t *testing.T) {
	primary := newPullTestPrimary(t)
	ctx := t.Context()
	primaryTheme := primary.dp.CurrentState()
	primaryTheme.Theme = "monarch"
	if err := common.SaveState(ctx, primary.dp.Settings, primaryTheme); err != nil {
		t.Fatal(err)
	}

	replica := newPullTestReplica(t, primary.server.URL)
	replicaMux := http.NewServeMux()
	registerSettings(replicaMux, replica)
	registerThemeSync(replicaMux, replica)
	client := &http.Client{Timeout: 5 * time.Second}
	// The theme half of newRederiveSettings: reload state from the store.
	rederive := func(c context.Context) { replica.SetState(common.LoadState(c, replica.Settings, replica.Cfg)) }

	// First pull: the replica inherits the shop's current state.
	syncPullTick(ctx, replica, client, rederive)

	// The owner picks a theme on the replica's Settings page.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/settings/theme", strings.NewReader(url.Values{"theme": {"slate"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	replicaMux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("theme save on the replica = %d, want 204", rec.Code)
	}

	// The main till's admin state keeps moving (as in the field journal:
	// 17:09, 17:11, 17:12, 17:15, 17:17 …); the replica pulls and the open
	// page polls after each.
	seen := map[string]int{}
	for i, name := range []string{"Shop A", "Shop B", "Shop C", "Shop D"} {
		if err := primary.dp.Settings.Set(ctx, "store.name", name); err != nil {
			t.Fatal(err)
		}
		syncPullTick(ctx, replica, client, rederive)
		got := pollThemeSync(t, replicaMux)
		seen[got]++
		if got != "slate" {
			t.Fatalf("pull %d: the replica's theme reverted to %q (want its own slate) — the poll would swap the stylesheet back", i+1, got)
		}
	}
	if len(seen) != 1 {
		t.Fatalf("the poll reported more than one theme across pulls (%v): the stylesheet would be re-applied", seen)
	}
	if v, _, _ := replica.Settings.Get(ctx, "store.name"); v != "Shop D" {
		t.Fatalf("shop-wide settings must keep syncing: store.name = %q", v)
	}

	// And the main till keeps its own theme: nothing flows back.
	if got, _, _ := primary.dp.Settings.Get(ctx, "theme"); got != "monarch" {
		t.Fatalf("main till's theme changed to %q", got)
	}
}
