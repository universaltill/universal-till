package pages

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3115: Settings → My shop → Shop name edits store.name after the
// setup wizard.

func storeNameSetting(t *testing.T, d *common.Deps) string {
	t.Helper()
	v, _, err := d.Settings.Get(t.Context(), common.KeyStoreName)
	if err != nil {
		t.Fatalf("read %s: %v", common.KeyStoreName, err)
	}
	return v
}

func TestStoreNameSave_SavesAndAudits(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), common.KeyStoreName, "Old Name"); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/settings/store-name", url.Values{"store_name": {"  Corner Café  "}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("save = %d: %s", rec.Code, rec.Body.String())
	}
	if got := storeNameSetting(t, d); got != "Corner Café" {
		t.Fatalf("stored %q, want the trimmed name", got)
	}
	if n := auditActions(t, d, "store_name_changed"); n != 1 {
		t.Fatalf("store_name_changed audit rows = %d, want 1", n)
	}
	var payload string
	if err := d.Db.QueryRow(`SELECT data_json FROM audit_log WHERE action='store_name_changed'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"old":"Old Name"`) || !strings.Contains(payload, `"new":"Corner Café"`) {
		t.Fatalf("audit payload %s must record old and new names", payload)
	}
}

func TestStoreNameSave_RefusesBadNamesWithoutWriting(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), common.KeyStoreName, "Kept Name"); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, value, wantMsg string
	}{
		{"blank", "   ", "Enter your shop"},
		{"placeholder", "My Store", "Enter your shop"},
		{"cloud placeholder", "Universal Till store", "Enter your shop"},
		{"too long", strings.Repeat("x", 81), "80 characters"},
		{"bidi override", "Shop‮exe", "can’t contain"},
		{"control char", "Corner\nCafé", "can’t contain"},
		{"invisible only", "‍", "Enter your shop"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postForm(mux, "/api/settings/store-name", url.Values{"store_name": {tc.value}}, &mgrUser)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("code = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.wantMsg) {
				t.Fatalf("body %q should explain the refusal (%q)", rec.Body.String(), tc.wantMsg)
			}
			// An HTML fragment, so app.js swaps it into the card instead of
			// raising the page-wide error banner (ut-docs#916).
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("refusal Content-Type = %q, want text/html", ct)
			}
			if got := storeNameSetting(t, d); got != "Kept Name" {
				t.Fatalf("a refused name changed store.name to %q", got)
			}
		})
	}
	if n := auditActions(t, d, "store_name_changed"); n != 0 {
		t.Fatalf("refused saves wrote %d audit rows", n)
	}
}

// A refusal answers in the browser's language without setting the ?lang=
// cookie (RequestLocale, same as staff-languages).
func TestStoreNameSave_RefusalIsTranslatedAndSetsNoCookie(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	rec := postForm(mux, "/api/settings/store-name?lang=tr", url.Values{"store_name": {""}}, &mgrUser)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("code = %d, want 400", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Enter your shop") {
		t.Fatalf("?lang=tr refusal came back in English: %q", rec.Body.String())
	}
	if c := rec.Header().Get("Set-Cookie"); c != "" {
		t.Fatalf("a refused POST must not set a cookie, got %q", c)
	}
}

func TestStoreNameSave_UnchangedNameWritesNoAudit(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), common.KeyStoreName, "Corner Café"); err != nil {
		t.Fatal(err)
	}
	rec := postForm(mux, "/api/settings/store-name", url.Values{"store_name": {" Corner Café "}}, &mgrUser)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("unchanged save = %d: %s", rec.Code, rec.Body.String())
	}
	if n := auditActions(t, d, "store_name_changed"); n != 0 {
		t.Fatalf("an unchanged name wrote %d audit rows", n)
	}
}

func TestStoreNameSave_CashierGetsElevationPrompt(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	seedElevationUsers(t, d)
	if err := d.Settings.Set(t.Context(), common.KeyStoreName, "Kept Name"); err != nil {
		t.Fatal(err)
	}
	cashier := auth.User{ID: "cashier-x", Role: "cashier"}
	rec := postForm(mux, "/api/settings/store-name", url.Values{"store_name": {"New Name"}}, &cashier)
	assertElevationPrompt(t, "store-name", rec.Code, rec.Body.String())
	body := rec.Body.String()
	if !strings.Contains(body, "New Name") || !strings.Contains(body, `name="store_name"`) {
		t.Fatalf("prompt must name the new shop name and carry it as a hidden field: %s", body)
	}
	if got := storeNameSetting(t, d); got != "Kept Name" {
		t.Fatalf("a cashier's unelevated save changed store.name to %q", got)
	}

	// A bad name is refused before the gate — no approver PIN is asked for.
	rec = postForm(mux, "/api/settings/store-name", url.Values{"store_name": {""}}, &cashier)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("cashier blank name = %d, want 400 before the elevation gate", rec.Code)
	}

	// With a valid approver PIN the rename lands.
	rec = postForm(mux, "/api/settings/store-name", url.Values{"store_name": {"New Name"}, "override_pin": {"555222"}}, &cashier)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "✓") {
		t.Fatalf("elevated save = %d %s, want 200 with a confirmation", rec.Code, rec.Body.String())
	}
	if got := storeNameSetting(t, d); got != "New Name" {
		t.Fatalf("elevated save stored %q", got)
	}
}

func TestSettingsPage_StoreNameCard(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	if err := d.Settings.Set(t.Context(), common.KeyStoreName, "Corner <Café>"); err != nil {
		t.Fatal(err)
	}
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	for _, want := range []string{
		`id="settings-store-name"`,
		`hx-post="/api/settings/store-name"`,
		`value="Corner &lt;Café&gt;"`,
		`maxlength="80"`,
		`dir="auto"`,
		`id="store-name-msg"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings page missing %s", want)
		}
	}

	// A placeholder name is not prefilled — the owner types the real one.
	if err := d.Settings.Set(t.Context(), common.KeyStoreName, "My Store"); err != nil {
		t.Fatal(err)
	}
	body = getAs(mux, "/settings", &mgrUser).Body.String()
	card := storeNameCard(t, body)
	if strings.Contains(card, "My Store") || !strings.Contains(card, `value=""`) {
		t.Errorf("placeholder store name must not be prefilled: %s", card)
	}
}

// storeNameCard is the Shop name card's own markup (the raw All Settings
// table further down also lists store.name).
func storeNameCard(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, `id="settings-store-name"`)
	if i < 0 {
		t.Fatal("no Shop name card on the settings page")
	}
	j := strings.Index(body[i:], `<div class="card"`)
	if j < 0 {
		t.Fatal("Shop name card is the last card — expected Currency after it")
	}
	return body[i : i+j]
}
