package pages

import (
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/httpx"
)

// ut-docs#5874: the All-settings raw upsert is an operator-typed shop-name
// entry point; it must apply the Shop name card's rule (and the single-line
// rule) before the elevation gate and store the normalised name.

const sn5874Seed = "Corner Bakery"

func sn5874Bad() map[string]string {
	m := tcBadNames()
	m["ends with U+E0100"] = "Cafe\U000E0100"
	m["lrm"] = "Cafe‎"
	return m
}

func TestSettingsUpsert_StoreNameInvisibleCharsRefused(t *testing.T) {
	for label, bad := range sn5874Bad() {
		for who, user := range map[string]*struct{ mgr bool }{"manager": {true}, "cashier": {false}} {
			t.Run(label+" "+who, func(t *testing.T) {
				mux, _, d := newFullAuthDeps(t)
				if err := d.Settings.Set(t.Context(), "store.name", sn5874Seed); err != nil {
					t.Fatal(err)
				}
				u := &cashUser
				if user.mgr {
					u = &mgrUser
				}
				rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.name"}, "value": {bad}}, u)
				if rec.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
				}
				tcWantMsg(t, rec.Body.String())
				if strings.Contains(strings.ToLower(rec.Body.String()), "pin") {
					t.Fatalf("an elevation prompt was rendered: %s", rec.Body.String())
				}
				if got := settingValue(t, d, "store.name"); got != sn5874Seed {
					t.Fatalf("store.name = %q, want it unchanged", got)
				}
			})
		}
	}
}

func TestSettingsUpsert_StoreNameBlankLongPlaceholderRefused(t *testing.T) {
	cases := map[string]struct{ val, key string }{
		"blank":       {"", "settings.store_name.error_required"},
		"whitespace":  {"   ", "settings.store_name.error_required"},
		"placeholder": {"My Store", "settings.store_name.error_required"},
		"81 runes":    {strings.Repeat("é", 81), "settings.store_name.error_too_long"},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			mux, _, d := newFullAuthDeps(t)
			if err := d.Settings.Set(t.Context(), "store.name", sn5874Seed); err != nil {
				t.Fatal(err)
			}
			rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.name"}, "value": {c.val}}, &mgrUser)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400: %s", rec.Code, rec.Body.String())
			}
			want := httpx.T("en", c.key)
			if want == c.key {
				t.Fatal("i18n not initialised: key did not resolve")
			}
			if !strings.Contains(html.UnescapeString(rec.Body.String()), want) {
				t.Fatalf("body = %q, want %q", rec.Body.String(), want)
			}
			if strings.Contains(strings.ToLower(rec.Body.String()), "pin") {
				t.Fatalf("an elevation prompt was rendered: %s", rec.Body.String())
			}
			if got := settingValue(t, d, "store.name"); got != sn5874Seed {
				t.Fatalf("store.name = %q, want it unchanged", got)
			}
		})
	}
}

// An equivalence pin, not a red/green test for #5874: the handler already
// trims, so today both checkers only trim a valid name. It keeps the upsert
// and the Shop name card storing the same bytes if either rule grows.
func TestSettingsUpsert_StoreNameStoredNormalised(t *testing.T) {
	inputs := map[string]string{
		"padded":       "  Café Müller  ",
		"80 runes":     strings.Repeat("é", 80),
		"zwj":          tcGoodNames()["zwj"],
		"padded + zwj": "  " + tcGoodNames()["zwj"] + " ",
	}
	for label, in := range inputs {
		t.Run(label, func(t *testing.T) {
			want, err := config.NormalizeStoreName(in)
			if err != nil {
				t.Fatalf("fixture %q refused by NormalizeStoreName: %v", in, err)
			}
			mux, _, d := newFullAuthDeps(t)
			rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.name"}, "value": {in}}, &mgrUser)
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			if got := settingValue(t, d, "store.name"); got != want {
				t.Fatalf("upsert stored %q, want %q", got, want)
			}
			// Same value as the Shop name card stores for the same input.
			mux2, _, d2 := newFullAuthDeps(t)
			if rec := postForm(mux2, "/api/settings/store-name", url.Values{"store_name": {in}}, &mgrUser); rec.Code >= 400 {
				t.Fatalf("store-name form: status = %d: %s", rec.Code, rec.Body.String())
			}
			if got := settingValue(t, d2, "store.name"); got != want {
				t.Fatalf("form stored %q, want %q", got, want)
			}
		})
	}
}

// The de pack lives in its own repo, so this binary only has fa/tr/ar; fa
// translates the store-name keys, which proves the refusal follows ?lang=.
func TestSettingsUpsert_StoreNameRefusalLocalised(t *testing.T) {
	cases := map[string]struct{ val, key string }{
		"too long": {strings.Repeat("é", 81), "settings.store_name.error_too_long"},
		"blank":    {"", "settings.store_name.error_required"},
		"bidi (key falls back to en until the pack ships it)": {"Bar\u202eabc", keyNameInvalidCharacters},
	}
	for label, c := range cases {
		t.Run(label, func(t *testing.T) {
			want := httpx.T("fa", c.key)
			if want == c.key {
				t.Fatalf("fa key %s did not resolve", c.key)
			}
			if c.key != keyNameInvalidCharacters && want == httpx.T("en", c.key) {
				t.Fatalf("fa text for %s equals en: test proves nothing", c.key)
			}
			mux, _, _ := newFullAuthDeps(t)
			rec := postForm(mux, "/api/settings/upsert?lang=fa", url.Values{"key": {"store.name"}, "value": {c.val}}, &mgrUser)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(html.UnescapeString(rec.Body.String()), want) {
				t.Fatalf("body = %q, want fa text %q", rec.Body.String(), want)
			}
		})
	}
}
