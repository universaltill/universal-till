package pages

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/fiscal"
)

// ut-docs#1887 (sibling to #1814): requireFiscalAuthorityForCountryChange's
// 403 used to answer with a bare, untranslated http.Error body — a dead
// end on a pinned kiosk (no nav rail, no way back) and never translated
// regardless of till locale, the same defect class #1814 fixed for
// fiscal_device_page.go. This is the layout/i18n proof for this gate.
func TestRequireFiscalAuthorityForCountryChange_RefusalIsTranslatedFullLayout(t *testing.T) {
	mux, _, d := newFullAuthDeps(t)
	setCountry(t, d, "DE")
	if err := d.Settings.Set(t.Context(), fiscal.SigningDeviceConfiguredKey("DE"), "true"); err != nil {
		t.Fatal(err)
	}

	// mgrUser is a manager, not admin/super_admin — fiscal_tse_override
	// (ADR-0048 Decision 3) refuses it, so the country change must be
	// blocked while DE's signing device is confirmed.
	rec := postForm(mux, "/api/settings/upsert", url.Values{"key": {"store.country"}, "value": {"TR"}}, &mgrUser)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("code = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `class="nav"`) {
		t.Fatalf("error response has no nav rail (bare body, dead end on a pinned kiosk):\n%s", body)
	}
	want := en("fiscaldevice.error.owner_required_country_change")
	if !strings.Contains(body, want) {
		t.Fatalf("expected the translated owner_required_country_change message %q, got: %s", want, body)
	}
	// The shop's country must stay unchanged.
	if got := d.CurrentState().Country; got != "DE" {
		t.Fatalf("country = %q, want unchanged DE", got)
	}
}
