package pages

import (
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/pages/settingsnav"
)

// checkin-notice-v2 (ADR-0200, owner-approved, legal-reviewed) tells the shop:
// "in Settings > Privacy, switch off "Send usage facts"". The product follows
// the text, never the other way round: these tests fail if the control, the
// card or either name drifts from what the notice says.

func privacyCardBlock(t *testing.T, body string) string {
	t.Helper()
	at := strings.Index(body, `id="settings-privacy"`)
	if at < 0 {
		t.Fatal(`Settings has no card id="settings-privacy"`)
	}
	rest := body[at:]
	// The next card starts the next top-level section.
	if end := strings.Index(rest[1:], `<div class="card`); end > 0 {
		rest = rest[:end+1]
	}
	return rest
}

func TestSettingsPrivacyCardHoldsTheSendUsageFactsSwitch(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	card := privacyCardBlock(t, body)

	title := httpx.T("en", "settings.privacy.title")
	if title != "Privacy" {
		t.Fatalf("settings.privacy.title = %q, want Privacy", title)
	}
	if !regexp.MustCompile(`<h2[^>]*>\s*` + regexp.QuoteMeta(title) + `\b`).MatchString(card) {
		t.Errorf("the Privacy card's heading is not %q", title)
	}
	label := httpx.T("en", "settings.registration.send_usage_facts")
	sw := regexp.MustCompile(`(?s)<label[^>]*>\s*<input[^>]*type="checkbox"[^>]*>\s*` + regexp.QuoteMeta(label) + `\s*</label>`)
	if !sw.MatchString(card) {
		t.Errorf("the Privacy card has no checkbox switch labelled %q:\n%s", label, card)
	}
	if !strings.Contains(card, `data-testid="registration-stop-btn"`) || !strings.Contains(card, "/api/settings/registration-stop") {
		t.Error("the switch does not post to /api/settings/registration-stop")
	}
	if !strings.Contains(card, `data-testid="registration-notice"`) {
		t.Error("the Privacy card does not show the notice")
	}
	if strings.Contains(body, ` id="cloud-record"`) {
		t.Error("the old Cloud record block is still on the page")
	}
}

func TestSettingsPrivacyNavRowIsManagerOnly(t *testing.T) {
	rows := settingsNavPlain("en", nil)
	has := func(rs []settingsnav.Row) bool {
		for _, r := range rs {
			if r.Key == "settings-privacy" {
				return true
			}
		}
		return false
	}
	if !has(rows) {
		t.Fatal("Resolve has no settings-privacy row")
	}
	if !has(filterSettingsNavForRender(rows, true, false, false, false, false, true)) {
		t.Error("a manager does not see the Privacy row")
	}
	if has(filterSettingsNavForRender(rows, false, false, false, false, false, true)) {
		t.Error("a non-manager sees the Privacy row")
	}
}

// One GET refreshes both the Till registration card and, out of band, the
// Privacy card (status and last check-in change with the same events).
func TestSettingsRefreshSpanAlsoSwapsThePrivacyCard(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := getAs(mux, "/settings", &mgrUser).Body.String()
	tag := regexp.MustCompile(`<span[^>]*data-testid="registration-refresh"[^>]*>`).FindString(body)
	if tag == "" {
		t.Fatal("no registration-refresh span")
	}
	if !strings.Contains(tag, `hx-select-oob="#settings-privacy"`) || strings.Count(tag, "hx-get=") != 1 {
		t.Errorf("refresh span must swap #settings-privacy out of band with one GET: %s", tag)
	}
}

// The notice text and the product share two names; a rename of either must
// break the build until the notice is re-approved under a new version id.
func TestNoticeStopHintNamesTheRealCardAndSwitch(t *testing.T) {
	hint := httpx.T("en", "registration.notice.stop_hint")
	card := httpx.T("en", "settings.privacy.title")
	sw := httpx.T("en", "settings.registration.send_usage_facts")
	if card == "settings.privacy.title" || sw == "settings.registration.send_usage_facts" {
		t.Fatal("settings.privacy.title / settings.registration.send_usage_facts missing from en.json")
	}
	if !strings.Contains(hint, "Settings > "+card) {
		t.Errorf("notice stop_hint does not say %q", "Settings > "+card)
	}
	if !strings.Contains(hint, `"`+sw+`"`) {
		t.Errorf("notice stop_hint does not quote the switch label %q", sw)
	}
}
