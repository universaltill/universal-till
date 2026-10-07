package pages

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#2982: most Settings cards are hx-swap="none" forms whose failure
// path used to only unhide the page-wide "Could not save" banner, so a
// specific, already-translated refusal ("Can't reach the main till — …")
// never reached the operator. inline-actions.js's save-error step now shows
// the response text near the card — but only when the server marks it as an
// operator-facing message with X-UT-Response: refused. Plenty of plain-text
// refusals are untranslated developer strings ("could not save", "minutes
// must be between 0 and 480") that must keep falling back to the generic
// banner, so the marker, not the Content-Type, is the contract.

func wantRefusedText(t *testing.T, path string, code int, rec interface {
	Result() *http.Response
}, body, msg string) {
	t.Helper()
	res := rec.Result()
	if res.StatusCode != code {
		t.Fatalf("%s = %d %q, want %d", path, res.StatusCode, body, code)
	}
	if got := res.Header.Get("X-UT-Response"); got != "refused" {
		t.Fatalf("%s: X-UT-Response = %q, want \"refused\" (the save-error step only shows a marked reason)", path, got)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("%s: Content-Type = %q, want text/plain", path, ct)
	}
	if !strings.Contains(body, msg) {
		t.Fatalf("%s: body %q, want %q", path, body, msg)
	}
}

// The write-through refusal on an additional till whose main till is
// unreachable, for the hx-swap="none" cards the card names.
func TestSettingsRefusal2982_WriteThroughRefusalIsMarked(t *testing.T) {
	mux, _ := newSettingsSyncReplica(t, deadPrimaryURL())
	for _, tc := range []struct {
		path string
		form url.Values
	}{
		{"/api/settings/idle-lock", url.Values{"minutes": {"5"}}},
		{"/api/settings/kiosk-idle-reset", url.Values{"seconds": {"60"}}},
		{"/api/settings/kiosk-payment-mode", url.Values{"mode": {"counter"}}},
		{"/api/settings/allow-negative-inventory", url.Values{"enabled": {"true"}}},
		{"/api/settings/browsing-mode", url.Values{"mode": {"all_filter_chips"}}},
		{"/api/settings/upsert", url.Values{"key": {"store.name"}, "value": {"New Name"}}},
	} {
		rec := postForm(mux, tc.path, tc.form, &mgrUser)
		wantRefusedText(t, tc.path, http.StatusBadGateway, rec, rec.Body.String(), settingsUnreachableEN)
	}
}

// A main-till refusal (409) carries the marker too.
func TestSettingsRefusal2982_MainRefusalIsMarked(t *testing.T) {
	main := newSettingsSyncMain(t)
	setReplicaSettings(t, main.dp.Settings, "http://192.0.2.1:8080", "b-123")
	mux, _ := newSettingsSyncReplica(t, main.srv.URL)
	rec := postForm(mux, "/api/settings/idle-lock", url.Values{"minutes": {"5"}}, &mgrUser)
	wantRefusedText(t, "/api/settings/idle-lock", http.StatusConflict, rec, rec.Body.String(), settingsRefusedEN)
}

// A main-till permission refusal (403) is marked as well.
func TestSettingsRefusal2982_MainForbiddenIsMarked(t *testing.T) {
	main := newSettingsSyncMain(t)
	mux, dp := newSettingsSyncReplica(t, main.srv.URL)
	insertTestUserWithPIN(t, main.dp.Db, "x-1", "xan", "Xan", "cashier", "")
	insertTestUserWithPIN(t, dp.Db, "x-1", "xan", "Xan", "manager", "")
	xan := auth.User{ID: "x-1", Role: "manager"}
	rec := postForm(mux, "/api/settings/idle-lock", url.Values{"minutes": {"5"}}, &xan)
	wantRefusedText(t, "/api/settings/idle-lock", http.StatusForbidden, rec, rec.Body.String(), "")
}

// A translated validation refusal is marked; an untranslated developer
// string is not, so the page keeps showing its generic banner for it.
func TestSettingsRefusal2982_OnlyTranslatedRefusalsAreMarked(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	rec := postForm(mux, "/api/settings/staff-languages", url.Values{}, &mgrUser)
	wantRefusedText(t, "/api/settings/staff-languages", http.StatusBadRequest, rec, rec.Body.String(), "")

	rec = postForm(mux, "/api/settings/idle-lock", url.Values{"minutes": {"9999"}}, &mgrUser)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("idle-lock out of range = %d, want 400", rec.Code)
	}
	if got := rec.Header().Get("X-UT-Response"); got != "" {
		t.Fatalf("an untranslated developer refusal is marked X-UT-Response=%q; the operator would see English", got)
	}
}
