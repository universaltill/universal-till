package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#2135. A German pilot till rendered an ENGLISH sale screen for weeks
// while Settings insisted the language was German. The cause was a `ut_lang`
// cookie — set once by clicking through /setup in English, MaxAge one year —
// which outranks the shop's configured locale on every page. Settings →
// Language changed only the shop default, so the one control an operator
// would go to could not fix what was actually overriding the language: two
// controls disagreeing, with the operator holding the one that loses.
//
// This pins the operator-visible half of the fix.

// The case that matters most, and the one a per-browser cookie clear cannot
// cover: the manager applies the language from their OWN device, and the till
// standing in the shop must pick it up. Note the shop default never moves
// here — it is already Turkish — so only the generation can retire the
// override.
func TestSaveSettings_LocaleRetiresAStaleOverrideOnEveryBrowser(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t) // wires httpx.InitI18n(realBundle, "en")

	// The shop is set to Turkish. (German isn't a shipped base locale — it
	// arrives as a language pack — so the shipped ar/en/fa/tr set stands in
	// for the pilot's de here; the mechanism under test is locale-agnostic.)
	if rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"tr"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("initial locale save = %d", rec.Code)
	}
	if got := httpx.DefaultLocale(); got != "tr" {
		t.Fatalf("shop default = %q, want tr", got)
	}

	// The till's browser holds an English override made while the shop was
	// already Turkish — a live one, not one the shop default has moved past.
	// Assert that first: without it this test could pass for the wrong reason.
	tillCookie := &http.Cookie{Name: "ut_lang", Value: httpx.LocaleOverrideValue("en")}
	if body := renderSettingsWith(t, mux, tillCookie); !strings.Contains(body, `lang="en"`) {
		t.Fatalf("precondition: the till did not render English with the "+
			"override cookie set; body head: %.200s", body)
	}

	// The manager applies Settings → Language → Turkish from a DIFFERENT
	// device, so this request carries no ut_lang cookie at all. The shop
	// default does not change — it is already Turkish.
	if rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"tr"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("re-applying the shop's own locale = %d, want 204", rec.Code)
	}

	// The till — same cookie, untouched, never saw that response.
	if body := renderSettingsWith(t, mux, tillCookie); !strings.Contains(body, `lang="tr"`) {
		t.Fatalf("the till still does not render Turkish after the shop "+
			"restated its language from another device; body head: %.200s", body)
	}
}

// An invalid locale is rejected by this handler (ut-docs#861), so it must not
// retire anyone's override either — otherwise a typo silently resets the
// language for every browser in the shop while changing nothing else.
func TestSaveSettings_RejectedLocaleLeavesOverridesAlone(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	assertRejectedLocaleKeepsOverride(t, mux, "/api/settings/save",
		url.Values{"locale": {"xx-not-real"}})
}

// Same rule for the all-settings table's raw store.locale write: it validates
// the value exactly as the Language card does, so a rejected one must be just
// as inert.
func TestUpsertSettings_RejectedLocaleLeavesOverridesAlone(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	assertRejectedLocaleKeepsOverride(t, mux, "/api/settings/upsert",
		url.Values{"key": {"store.locale"}, "value": {"xx-not-real"}})
}

func assertRejectedLocaleKeepsOverride(t *testing.T, mux *http.ServeMux, path string, form url.Values) {
	t.Helper()
	if rec := postForm(mux, "/api/settings/save", url.Values{"locale": {"tr"}}, &mgrUser); rec.Code != http.StatusNoContent {
		t.Fatalf("initial locale save = %d", rec.Code)
	}
	cookie := &http.Cookie{Name: "ut_lang", Value: httpx.LocaleOverrideValue("en")}
	if body := renderSettingsWith(t, mux, cookie); !strings.Contains(body, `lang="en"`) {
		t.Fatalf("precondition: override not in effect before the rejected write")
	}

	postForm(mux, path, form, &mgrUser)

	if body := renderSettingsWith(t, mux, cookie); !strings.Contains(body, `lang="en"`) {
		t.Fatalf("%s with a rejected locale retired a live override; body head: %.200s", path, body)
	}
}

func renderSettingsWith(t *testing.T, mux *http.ServeMux, cookies ...*http.Cookie) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/settings", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	req = auth.WithUser(req, mgrUser)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /settings = %d, want 200: %.300s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}
